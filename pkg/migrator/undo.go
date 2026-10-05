package migrator

import (
	"context"
	"fmt"
	"time"

	"github.com/roryq/noway/pkg/resolver"
)

// UndoResult contains the undo operation result.
type UndoResult struct {
	UndoneVersion string `json:"undoneVersion"`
	Script        string `json:"script"`
	ExecutionTime int64  `json:"executionTime"`
}

// Undo reverts the most recently applied versioned migration.
func (m *Migrator) Undo(ctx context.Context) (*UndoResult, error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table

	// 1. Ensure history table exists
	exists, err := m.db.HistoryTableExists(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed checking history table: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("no schema history table found to undo migrations")
	}

	// 2. Lock history table
	unlock, err := m.db.Lock(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire lock for undo: %w", err)
	}
	defer func() {
		_ = unlock(context.Background())
	}()

	// 3. Fetch applied history
	applied, err := m.db.FetchHistory(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed fetching schema history: %w", err)
	}

	// Find the latest applied versioned migration
	var latestApplied *resolver.AppliedMigration
	for i := len(applied) - 1; i >= 0; i-- {
		app := applied[i]
		if app.Version != nil && !app.Version.IsEmpty() && app.Success && app.Type == "SQL" {
			latestApplied = &applied[i]
			break
		}
	}

	if latestApplied == nil {
		return nil, fmt.Errorf("no versioned migrations found to undo")
	}

	// 4. Resolve local undo migrations
	resolved, err := m.resolver.Resolve()
	if err != nil {
		return nil, fmt.Errorf("failed resolving migrations: %w", err)
	}

	var targetUndo *resolver.ResolvedMigration
	for _, u := range resolved.UndoMigrations {
		if u.Version != nil && u.Version.Equals(*latestApplied.Version) {
			targetUndo = &u
			break
		}
	}

	if targetUndo == nil {
		return nil, fmt.Errorf("no undo migration found for version %s (expected U%s__*.sql)", latestApplied.Version.String(), latestApplied.Version.String())
	}

	// 5. Fire beforeUndo callbacks
	if err := m.callbackRunner.Fire(ctx, "beforeUndo"); err != nil {
		return nil, err
	}
	if err := m.callbackRunner.Fire(ctx, "beforeEachUndo"); err != nil {
		return nil, err
	}

	// 6. Execute undo script
	sql, err := m.replacer.Replace(targetUndo.Content, m.builtins)
	if err != nil {
		return nil, fmt.Errorf("error replacing placeholders in undo script '%s': %w", targetUndo.Script, err)
	}

	stmts := m.parser.SplitStatements(sql)
	startTime := time.Now()

	for _, stmt := range stmts {
		if err := m.db.ExecuteStatement(ctx, stmt.SQL); err != nil {
			_ = m.callbackRunner.Fire(ctx, "beforeEachUndoError")
			_ = m.callbackRunner.Fire(ctx, "afterEachUndoError")
			_ = m.callbackRunner.Fire(ctx, "afterUndoError")
			return nil, fmt.Errorf("error executing undo statement (line %d) for '%s': %w", stmt.LineNumber, targetUndo.Script, err)
		}
	}

	execTime := time.Since(startTime).Milliseconds()

	// 7. Remove the undone record from history table
	if err := m.db.DeleteHistory(ctx, defaultSchema, table, latestApplied.InstalledRank); err != nil {
		return nil, fmt.Errorf("failed removing history record for undone migration %s: %w", latestApplied.Script, err)
	}

	// 8. Fire afterUndo callbacks
	if err := m.callbackRunner.Fire(ctx, "afterEachUndo"); err != nil {
		return nil, err
	}
	if err := m.callbackRunner.Fire(ctx, "afterUndo"); err != nil {
		return nil, err
	}

	return &UndoResult{
		UndoneVersion: latestApplied.Version.String(),
		Script:        targetUndo.Script,
		ExecutionTime: execTime,
	}, nil
}
