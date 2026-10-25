package migrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/parser"
	"github.com/roryq/noway/pkg/resolver"
)

// CallbackRunner executes callback SQL scripts for lifecycle events.
type CallbackRunner struct {
	db        database.Database
	callbacks map[string][]resolver.ResolvedCallback
	parser    *parser.BigQueryParser
	replacer  *parser.PlaceholderReplacer
	builtins  parser.BuiltinPlaceholders
	config    config.Configuration
	operation string
}

// NewCallbackRunner creates a CallbackRunner.
func NewCallbackRunner(
	db database.Database,
	callbacks map[string][]resolver.ResolvedCallback,
	parser *parser.BigQueryParser,
	replacer *parser.PlaceholderReplacer,
	builtins parser.BuiltinPlaceholders,
) *CallbackRunner {
	return &CallbackRunner{
		db:        db,
		callbacks: callbacks,
		parser:    parser,
		replacer:  replacer,
		builtins:  builtins,
		config:    *config.NewDefaultConfiguration(),
	}
}

// SetCallbacks updates the resolved callbacks map.
func (c *CallbackRunner) SetCallbacks(callbacks map[string][]resolver.ResolvedCallback) {
	c.callbacks = callbacks
}

// SetOperation sets the active operation name (e.g. MIGRATE, VALIDATE, INFO).
func (c *CallbackRunner) SetOperation(op string) {
	c.operation = op
}

// SetConfig sets the configuration for environment variable and placeholder injection.
func (c *CallbackRunner) SetConfig(cfg *config.Configuration) {
	if cfg != nil {
		c.config = *cfg
	}
}

// SetBuiltins updates the builtin placeholders.
func (c *CallbackRunner) SetBuiltins(builtins parser.BuiltinPlaceholders) {
	c.builtins = builtins
}


// Fire executes all registered callbacks for the given event name.
func (c *CallbackRunner) Fire(ctx context.Context, event string) error {
	scripts, ok := c.callbacks[event]
	if !ok || len(scripts) == 0 {
		return nil
	}

	for _, cb := range scripts {
		if isScriptFile(cb.Filename) {
			if err := c.executeScriptCallback(ctx, cb, event); err != nil {
				return err
			}
			continue
		}

		builtins := c.builtins
		if builtins.Filename == "" {
			builtins.Filename = cb.Filename
		}
		sql, err := c.replacer.Replace(cb.Content, builtins)
		if err != nil {
			return fmt.Errorf("error replacing placeholders in callback '%s' (%s): %w", cb.Filename, event, err)
		}

		stmts := c.parser.SplitStatements(sql)
		for _, stmt := range stmts {
			if err := c.db.ExecuteStatement(ctx, stmt.SQL); err != nil {
				return fmt.Errorf("error executing callback '%s' statement (line %d): %w", cb.Filename, stmt.LineNumber, err)
			}
		}
	}

	return nil
}

func (c *CallbackRunner) executeScriptCallback(ctx context.Context, cb resolver.ResolvedCallback, event string) error {
	var scriptPath string
	var cleanup func()

	if cb.PhysicalLocation != "" {
		if fi, err := os.Stat(cb.PhysicalLocation); err == nil && !fi.IsDir() {
			scriptPath = cb.PhysicalLocation
		}
	}

	if scriptPath == "" {
		ext := filepath.Ext(cb.Filename)
		if ext == "" {
			ext = ".sh"
		}
		tmpFile, err := os.CreateTemp("", "noway-callback-*"+ext)
		if err != nil {
			return fmt.Errorf("failed creating temp callback script for %s: %w", cb.Filename, err)
		}
		if _, err := tmpFile.WriteString(cb.Content); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return fmt.Errorf("failed writing temp callback script for %s: %w", cb.Filename, err)
		}
		tmpFile.Close()
		_ = os.Chmod(tmpFile.Name(), 0700)
		scriptPath = tmpFile.Name()
		cleanup = func() { _ = os.Remove(tmpFile.Name()) }
	}
	if cleanup != nil {
		defer cleanup()
	}

	absScriptPath, err := filepath.Abs(scriptPath)
	if err != nil {
		return fmt.Errorf("resolving script path %s: %w", scriptPath, err)
	}

	var cmd *exec.Cmd
	switch filepath.Ext(absScriptPath) {
	case ".cmd", ".bat":
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", absScriptPath)
	case ".ps1":
		cmd = exec.CommandContext(ctx, "powershell", "-ExecutionPolicy", "Bypass", "-File", absScriptPath)
	case ".py":
		cmd = exec.CommandContext(ctx, "python3", absScriptPath)
	default:
		cmd = exec.CommandContext(ctx, "/bin/bash", absScriptPath)
	}

	op := c.operation
	if op == "" {
		op = determineOperation(event)
	}

	dbName := c.builtins.Database
	if dbName == "" {
		if c.config.GCPDataset != "" {
			dbName = c.config.GCPDataset
		} else if c.builtins.DefaultSchema != "" {
			dbName = c.builtins.DefaultSchema
		} else {
			dbName = "flyway"
		}
	}

	schemasStr := strings.Join(c.config.Schemas, ",")
	if schemasStr == "" {
		if c.builtins.DefaultSchema != "" {
			schemasStr = c.builtins.DefaultSchema
		} else {
			schemasStr = "flyway"
		}
	}

	cbUserIfKnown := c.config.User
	if cbUserIfKnown == "" && c.config.InstalledBy != "" {
		cbUserIfKnown = c.config.InstalledBy
	} else if cbUserIfKnown == "" && c.builtins.User != "flyway" && c.builtins.User != "" {
		cbUserIfKnown = c.builtins.User
	}

	env := os.Environ()
	setIfNotEmpty := func(k, v string) {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}

	setIfNotEmpty("FLYWAY_URL", c.config.URL)
	setIfNotEmpty("NOWAY_URL", c.config.URL)
	setIfNotEmpty("FLYWAY_USER", cbUserIfKnown)
	setIfNotEmpty("NOWAY_USER", cbUserIfKnown)
	setIfNotEmpty("FLYWAY_PASSWORD", c.config.Password)

	setIfNotEmpty("FLYWAY_OPERATION", op)
	setIfNotEmpty("NOWAY_OPERATION", op)
	setIfNotEmpty("FLYWAY_EVENT", event)
	setIfNotEmpty("NOWAY_EVENT", event)
	setIfNotEmpty("FLYWAY_CALLBACK", cb.Filename)
	setIfNotEmpty("NOWAY_CALLBACK", cb.Filename)
	setIfNotEmpty("FLYWAY_DATABASE", dbName)
	setIfNotEmpty("NOWAY_DATABASE", dbName)
	setIfNotEmpty("FLYWAY_DEFAULT_SCHEMA", c.builtins.DefaultSchema)
	setIfNotEmpty("NOWAY_DEFAULT_SCHEMA", c.builtins.DefaultSchema)
	setIfNotEmpty("FLYWAY_SCHEMAS", schemasStr)
	setIfNotEmpty("NOWAY_SCHEMAS", schemasStr)
	setIfNotEmpty("FLYWAY_TABLE", c.builtins.Table)
	setIfNotEmpty("NOWAY_TABLE", c.builtins.Table)
	setIfNotEmpty("FLYWAY_GCP_PROJECT_ID", c.config.GCPProjectID)
	setIfNotEmpty("NOWAY_GCP_PROJECT_ID", c.config.GCPProjectID)
	setIfNotEmpty("FLYWAY_GCP_DATASET", c.config.GCPDataset)
	setIfNotEmpty("NOWAY_GCP_DATASET", c.config.GCPDataset)
	setIfNotEmpty("FLYWAY_GCP_LOCATION", c.config.GCPLocation)
	setIfNotEmpty("NOWAY_GCP_LOCATION", c.config.GCPLocation)
	setIfNotEmpty("FLYWAY_BIGQUERY_ENDPOINT", c.config.GCPBigQueryEndpoint)
	setIfNotEmpty("NOWAY_BIGQUERY_ENDPOINT", c.config.GCPBigQueryEndpoint)

	scriptPfx := c.config.ScriptPlaceholderPrefix
	if scriptPfx == "" {
		scriptPfx = "FP__"
	}
	scriptSfx := c.config.ScriptPlaceholderSuffix
	if scriptSfx == "" {
		scriptSfx = "__"
	}

	setIfNotEmpty(scriptPfx+"flyway_defaultSchema"+scriptSfx, c.builtins.DefaultSchema)
	setIfNotEmpty(scriptPfx+"flyway_table"+scriptSfx, c.builtins.Table)
	setIfNotEmpty(scriptPfx+"flyway_user"+scriptSfx, cbUserIfKnown)
	setIfNotEmpty(scriptPfx+"flyway_database"+scriptSfx, dbName)
	setIfNotEmpty(scriptPfx+"flyway_filename"+scriptSfx, cb.Filename)

	wd := c.builtins.WorkingDirectory
	if wd == "" {
		if getwd, err := os.Getwd(); err == nil {
			wd = getwd
		}
	}
	setIfNotEmpty(scriptPfx+"flyway_workingDirectory"+scriptSfx, wd)

	if !c.builtins.Timestamp.IsZero() {
		setIfNotEmpty(scriptPfx+"flyway_timestamp"+scriptSfx, c.builtins.Timestamp.Format("2006-01-02 15:04:05"))
	}
	setIfNotEmpty(scriptPfx+"flyway_environment"+scriptSfx, c.config.Environment)

	for k, v := range c.config.Placeholders {
		cleanK := strings.ReplaceAll(k, ":", "_")
		setIfNotEmpty(scriptPfx+cleanK+scriptSfx, v)
	}
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("callback script %s (%s) failed: %w\nOutput:\n%s", cb.Filename, event, err, string(output))
	}
	return nil
}

func determineOperation(event string) string {
	lower := strings.ToLower(event)
	switch {
	case strings.Contains(lower, "migrate"):
		return "MIGRATE"
	case strings.Contains(lower, "validate"):
		return "VALIDATE"
	case strings.Contains(lower, "info"):
		return "INFO"
	case strings.Contains(lower, "clean"):
		return "CLEAN"
	case strings.Contains(lower, "undo"):
		return "UNDO"
	case strings.Contains(lower, "baseline"):
		return "BASELINE"
	case strings.Contains(lower, "repair"):
		return "REPAIR"
	default:
		return "COMMAND"
	}
}

func isScriptFile(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.HasSuffix(lower, ".sh") ||
		strings.HasSuffix(lower, ".bash") ||
		strings.HasSuffix(lower, ".cmd") ||
		strings.HasSuffix(lower, ".ps1") ||
		strings.HasSuffix(lower, ".bat") ||
		strings.HasSuffix(lower, ".py")
}
