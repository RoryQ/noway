package migrator

import (
	"context"
	"fmt"

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
