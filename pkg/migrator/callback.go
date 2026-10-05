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

		sql, err := c.replacer.Replace(cb.Content, c.builtins)
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
	content := cb.Content
	replaced, err := c.replacer.Replace(content, c.builtins)
	if err == nil {
		content = replaced
	}

	var scriptPath string
	var cleanup func()

	if cb.PhysicalLocation != "" {
		if fi, err := os.Stat(cb.PhysicalLocation); err == nil && !fi.IsDir() {
			scriptPath = cb.PhysicalLocation
			_ = os.Chmod(scriptPath, 0644)
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
		if _, err := tmpFile.WriteString(content); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return fmt.Errorf("failed writing temp callback script for %s: %w", cb.Filename, err)
		}
		tmpFile.Close()
		_ = os.Chmod(tmpFile.Name(), 0644)
		scriptPath = tmpFile.Name()
		cleanup = func() { _ = os.Remove(tmpFile.Name()) }
	}
	if cleanup != nil {
		defer cleanup()
	}

	var cmd *exec.Cmd
	switch filepath.Ext(scriptPath) {
	case ".cmd", ".bat":
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", scriptPath)
	case ".ps1":
		cmd = exec.CommandContext(ctx, "powershell", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	case ".py":
		cmd = exec.CommandContext(ctx, "python3", scriptPath)
	default:
		cmd = exec.CommandContext(ctx, "/bin/bash", scriptPath)
	}

	if cb.PhysicalLocation != "" {
		dir := filepath.Dir(cb.PhysicalLocation)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			cmd.Dir = dir
		}
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

	env := os.Environ()
	env = append(env,
		"FLYWAY_OPERATION="+op,
		"NOWAY_OPERATION="+op,
		"FLYWAY_EVENT="+event,
		"NOWAY_EVENT="+event,
		"FLYWAY_CALLBACK="+cb.Filename,
		"NOWAY_CALLBACK="+cb.Filename,
		"FLYWAY_USER="+c.builtins.User,
		"NOWAY_USER="+c.builtins.User,
		"FLYWAY_DATABASE="+dbName,
		"NOWAY_DATABASE="+dbName,
		"FLYWAY_DEFAULT_SCHEMA="+c.builtins.DefaultSchema,
		"NOWAY_DEFAULT_SCHEMA="+c.builtins.DefaultSchema,
		"FLYWAY_SCHEMAS="+schemasStr,
		"NOWAY_SCHEMAS="+schemasStr,
		"FLYWAY_TABLE="+c.builtins.Table,
		"NOWAY_TABLE="+c.builtins.Table,
		"FLYWAY_GCP_PROJECT_ID="+c.config.GCPProjectID,
		"NOWAY_GCP_PROJECT_ID="+c.config.GCPProjectID,
		"FLYWAY_GCP_DATASET="+c.config.GCPDataset,
		"NOWAY_GCP_DATASET="+c.config.GCPDataset,
		"FLYWAY_GCP_LOCATION="+c.config.GCPLocation,
		"NOWAY_GCP_LOCATION="+c.config.GCPLocation,
		"FLYWAY_BIGQUERY_ENDPOINT="+c.config.GCPBigQueryEndpoint,
		"NOWAY_BIGQUERY_ENDPOINT="+c.config.GCPBigQueryEndpoint,
		"FP__flyway_defaultSchema="+c.builtins.DefaultSchema,
		"FP__flyway_table="+c.builtins.Table,
		"FP__flyway_user="+c.builtins.User,
		"FP__flyway_database="+dbName,
	)

	for k, v := range c.config.Placeholders {
		env = append(env,
			"FP__"+k+"="+v,
			"FLYWAY_PLACEHOLDER_"+k+"="+v,
			"NOWAY_PLACEHOLDER_"+k+"="+v,
		)
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
