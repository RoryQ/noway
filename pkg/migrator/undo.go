package migrator

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/resolver"
)

// UndoResult contains the undo operation result matching Flyway's output schema.
type UndoResult struct {
	InitialSchemaVersion string       `json:"initialSchemaVersion"`
	TargetSchemaVersion  string       `json:"targetSchemaVersion"`
	SchemaName           string       `json:"schemaName"`
	Migrations           []UndoOutput `json:"migrations"`
	MigrationsUndone     int          `json:"migrationsUndone"`
	Success              bool         `json:"success"`
	FlywayVersion        string       `json:"flywayVersion"`
	Database             string       `json:"database"`
	Warnings             []string     `json:"warnings"`
	Operation            string       `json:"operation"`

	// Deprecated: Alias fields for backward-compatibility with Go code and tests
	UndoneVersion string `json:"-"`
	Script        string `json:"-"`
	ExecutionTime int64  `json:"-"`
}

// UndoOutput describes an undone migration matching Flyway UndoOutput.
type UndoOutput struct {
	Category      string `json:"category"`
	Version       string `json:"version"`
	Description   string `json:"description"`
	Filepath      string `json:"filepath"`
	ExecutionTime int64  `json:"executionTime"`
}

// UndoItem associates an applied migration with its resolved undo migration and replaced SQL.
type UndoItem struct {
	Applied  resolver.AppliedMigration
	Resolved resolver.ResolvedMigration
	SQL      string
}

// Undo reverts applied versioned migrations in descending order down to target.
func (m *Migrator) Undo(ctx context.Context) (res *UndoResult, retErr error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table
	isDryRun := m.config.DryRunOutput != ""

	m.ensureUser(ctx)

	// 1. Ensure history table exists
	exists, err := m.db.HistoryTableExists(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed checking history table: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("no schema history table found to undo migrations")
	}

	// 2. Lock history table if not dry run
	if !isDryRun {
		unlock, err := m.db.Lock(ctx, defaultSchema, table)
		if err != nil {
			return nil, fmt.Errorf("failed to acquire lock for undo: %w", err)
		}
		defer func() {
			if uErr := unlock(context.Background()); uErr != nil && retErr == nil {
				retErr = uErr
			}
		}()
	}

	// 3. Fetch applied history
	applied, err := m.db.FetchHistory(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed fetching schema history: %w", err)
	}

	history := processAppliedHistory(applied)

	// Collect applied versioned migrations that can be undone
	var activeApplied []resolver.AppliedMigration
	for _, app := range history.Records {
		if app.Version != nil && !app.Version.IsEmpty() && app.Success && (app.Type == "SQL" || app.Type == "SCRIPT") && !app.Deleted && !app.Undone {
			activeApplied = append(activeApplied, app)
		}
	}

	if len(activeApplied) == 0 {
		return nil, fmt.Errorf("no versioned migrations found to undo")
	}

	// Sort activeApplied by version ascending to determine initial schema version
	sort.Slice(activeApplied, func(i, j int) bool {
		return activeApplied[i].Version.Compare(*activeApplied[j].Version) < 0
	})
	initialSchemaVersion := activeApplied[len(activeApplied)-1].Version.String()

	// Find the most recently applied migration (highest installed rank) for default undo
	var mostRecentlyApplied resolver.AppliedMigration
	for _, app := range activeApplied {
		if mostRecentlyApplied.InstalledRank == 0 || app.InstalledRank > mostRecentlyApplied.InstalledRank {
			mostRecentlyApplied = app
		}
	}

	// Determine candidates to undo based on target
	var toUndo []resolver.AppliedMigration
	if m.config.TargetVersion != nil {
		for _, app := range activeApplied {
			if app.Version.IsNewerThan(*m.config.TargetVersion) {
				toUndo = append(toUndo, app)
			}
		}
	} else if m.config.Target == "0" {
		toUndo = append(toUndo, activeApplied...)
	} else {
		// Default: undo only the most recently applied versioned migration
		toUndo = append(toUndo, mostRecentlyApplied)
	}

	if len(toUndo) == 0 {
		return nil, fmt.Errorf("no versioned migrations found to undo")
	}

	// Sort toUndo descending by version
	sort.Slice(toUndo, func(i, j int) bool {
		return toUndo[i].Version.IsNewerThan(*toUndo[j].Version)
	})

	// 4. Resolve local undo migrations
	resolved, err := m.resolver.Resolve()
	if err != nil {
		return nil, fmt.Errorf("failed resolving migrations: %w", err)
	}

	undoByVer := make(map[string]resolver.ResolvedMigration)
	for _, u := range resolved.UndoMigrations {
		if u.Version != nil {
			undoByVer[u.Version.Normalized()] = u
		}
	}

	var items []UndoItem
	for _, app := range toUndo {
		u, found := undoByVer[app.Version.Normalized()]
		if !found {
			return nil, fmt.Errorf("no undo migration found for version %s (expected U%s__*.sql)", app.Version.String(), app.Version.String())
		}
		sql, err := m.replacer.Replace(u.Content, m.builtins)
		if err != nil {
			return nil, fmt.Errorf("error replacing placeholders in undo script '%s': %w", u.Script, err)
		}
		items = append(items, UndoItem{Applied: app, Resolved: u, SQL: sql})
	}

	// Determine target schema version (highest remaining version not undone)
	targetSchemaVersion := ""
	undoneSet := make(map[string]bool)
	for _, item := range items {
		undoneSet[item.Applied.Version.Normalized()] = true
	}
	for i := len(activeApplied) - 1; i >= 0; i-- {
		vNorm := activeApplied[i].Version.Normalized()
		if !undoneSet[vNorm] {
			targetSchemaVersion = activeApplied[i].Version.String()
			break
		}
	}

	user := m.config.InstalledBy
	if user == "" {
		currentUser, _ := m.db.GetCurrentUser(ctx)
		if currentUser != "" {
			user = currentUser
		} else {
			user = "flyway"
		}
	}

	if isDryRun {
		return m.undoDryRun(ctx, defaultSchema, table, user, items, initialSchemaVersion, targetSchemaVersion, history.NextInstalledRank)
	}

	// 5. Fire beforeUndo callbacks
	if err := m.callbackRunner.Fire(ctx, "beforeUndo"); err != nil {
		return nil, err
	}

	var outputs []UndoOutput
	rank := history.NextInstalledRank

	for _, item := range items {
		if err := m.callbackRunner.Fire(ctx, "beforeEachUndo"); err != nil {
			return nil, err
		}

		startTime := time.Now()
		stmts := m.parser.SplitStatements(item.SQL)
		for _, stmt := range stmts {
			if err := m.db.ExecuteStatement(ctx, stmt.SQL); err != nil {
				_ = m.callbackRunner.Fire(ctx, "beforeEachUndoError")
				_ = m.callbackRunner.Fire(ctx, "afterEachUndoError")
				_ = m.callbackRunner.Fire(ctx, "afterUndoError")
				return nil, fmt.Errorf("error executing undo statement (line %d) for '%s': %w", stmt.LineNumber, item.Resolved.Script, err)
			}
		}
		execTime := time.Since(startTime).Milliseconds()

		recType := "UNDO_SQL"
		if item.Resolved.IsScript {
			recType = "UNDO_SCRIPT"
		}
		verStr := item.Resolved.Version.String()
		cs := item.Resolved.Checksum

		historyRec := database.HistoryRecord{
			InstalledRank: rank,
			Version:       &verStr,
			Description:   database.AbbreviateDescription(item.Resolved.Description),
			Type:          recType,
			Script:        database.AbbreviateScript(item.Resolved.Script),
			Checksum:      &cs,
			InstalledBy:   user,
			InstalledOn:   time.Now(),
			ExecutionTime: execTime,
			Success:       true,
		}
		rank++

		if err := m.db.InsertHistory(ctx, defaultSchema, table, historyRec); err != nil {
			return nil, fmt.Errorf("failed inserting undo history record for %s: %w", item.Resolved.Script, err)
		}

		if err := m.callbackRunner.Fire(ctx, "afterEachUndo"); err != nil {
			return nil, err
		}

		outputs = append(outputs, UndoOutput{
			Category:      "Versioned",
			Version:       item.Resolved.Version.String(),
			Description:   item.Resolved.Description,
			Filepath:      item.Resolved.Script,
			ExecutionTime: execTime,
		})
	}

	// 6. Fire afterUndo callbacks
	if err := m.callbackRunner.Fire(ctx, "afterUndo"); err != nil {
		return nil, err
	}

	result := &UndoResult{
		InitialSchemaVersion: initialSchemaVersion,
		TargetSchemaVersion:  targetSchemaVersion,
		SchemaName:           defaultSchema,
		Migrations:           outputs,
		MigrationsUndone:     len(outputs),
		Success:              true,
		FlywayVersion:        m.FlywayVersion(),
		Database:             m.DatabaseName(),
		Warnings:             make([]string, 0),
		Operation:            "undo",
	}
	if len(outputs) > 0 {
		result.UndoneVersion = outputs[len(outputs)-1].Version
		result.Script = outputs[len(outputs)-1].Filepath
		result.ExecutionTime = outputs[len(outputs)-1].ExecutionTime
	}

	return result, nil
}
