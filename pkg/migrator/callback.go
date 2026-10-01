package migrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/RoryQ/noway/pkg/database"
	"github.com/RoryQ/noway/pkg/parser"
	"github.com/RoryQ/noway/pkg/resolver"
)

// CallbackRunner executes callback SQL scripts for lifecycle events.
type CallbackRunner struct {
	db        database.Database
	callbacks map[string][]resolver.ResolvedCallback
	parser    *parser.BigQueryParser
	replacer  *parser.PlaceholderReplacer
	builtins  parser.BuiltinPlaceholders
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
			_ = os.Chmod(scriptPath, 0755)
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
		_ = os.Chmod(tmpFile.Name(), 0755)
		scriptPath = tmpFile.Name()
		cleanup = func() { _ = os.Remove(tmpFile.Name()) }
	}
	if cleanup != nil {
		defer cleanup()
	}

	cmd := exec.CommandContext(ctx, "/bin/bash", scriptPath)
	if filepath.Ext(scriptPath) == ".cmd" || filepath.Ext(scriptPath) == ".bat" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", scriptPath)
	} else if filepath.Ext(scriptPath) == ".ps1" {
		cmd = exec.CommandContext(ctx, "powershell", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	}

	if cb.PhysicalLocation != "" {
		dir := filepath.Dir(cb.PhysicalLocation)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			cmd.Dir = dir
		}
	}

	env := os.Environ()
	env = append(env,
		"FLYWAY_EVENT="+event,
		"NOWAY_EVENT="+event,
		"FLYWAY_CALLBACK="+cb.Filename,
		"NOWAY_CALLBACK="+cb.Filename,
		"FLYWAY_USER="+c.builtins.User,
		"NOWAY_USER="+c.builtins.User,
		"FLYWAY_DEFAULT_SCHEMA="+c.builtins.DefaultSchema,
		"NOWAY_DEFAULT_SCHEMA="+c.builtins.DefaultSchema,
		"FLYWAY_TABLE="+c.builtins.Table,
		"NOWAY_TABLE="+c.builtins.Table,
	)
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("callback script %s (%s) failed: %w\nOutput:\n%s", cb.Filename, event, err, string(output))
	}
	return nil
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
