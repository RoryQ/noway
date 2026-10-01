package migrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database"
	"github.com/RoryQ/noway/pkg/parser"
	"github.com/RoryQ/noway/pkg/resolver"
	"github.com/RoryQ/noway/pkg/version"
)

// Migrator is the central coordinator for all Flyway database migration operations.
type Migrator struct {
	config         *config.Configuration
	db             database.Database
	resolver       *resolver.Resolver
	parser         *parser.BigQueryParser
	replacer       *parser.PlaceholderReplacer
	callbackRunner *CallbackRunner
	builtins       parser.BuiltinPlaceholders
}

// MigrateResult holds the outcome of a migration run.
type MigrateResult struct {
	InitialVersion      string            `json:"initialVersion"`
	TargetVersion       string            `json:"targetVersion"`
	MigrationsExecuted  int               `json:"migrationsExecuted"`
	Success             bool              `json:"success"`
	ExecutedMigrations  []ExecutedSummary `json:"executedMigrations"`
	TotalExecutionTime  int64             `json:"totalExecutionTime"`
}

// ExecutedSummary describes an applied migration during the run.
type ExecutedSummary struct {
	Category      string `json:"category"`
	Version       string `json:"version"`
	Description   string `json:"description"`
	Type          string `json:"type"`
	Script        string `json:"filepath"`
	ExecutionTime int64  `json:"executionTime"`
}

// New creates a new Migrator instance.
func New(cfg *config.Configuration, db database.Database) (*Migrator, error) {
	if err := cfg.Finalize(); err != nil {
		return nil, fmt.Errorf("configuration error: %w", err)
	}

	res := resolver.NewResolver(resolver.ResolverConfig{
		Locations:        cfg.Locations,
		FS:               cfg.FS,
		Prefix:           cfg.SQLMigrationPrefix,
		RepeatablePrefix: cfg.RepeatableSQLMigrationPrefix,
		UndoPrefix:       cfg.UndoSQLMigrationPrefix,
		BaselinePrefix:   cfg.BaselineSQLMigrationPrefix,
		Separator:        cfg.SQLMigrationSeparator,
		Suffixes:         cfg.SQLMigrationSuffixes,
		Encoding:         cfg.Encoding,
	})

	bqParser := parser.NewBigQueryParser()
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled:   cfg.PlaceholderReplacement,
		Prefix:    cfg.PlaceholderPrefix,
		Suffix:    cfg.PlaceholderSuffix,
		Separator: cfg.PlaceholderSeparator,
		Values:    cfg.Placeholders,
	})

	defaultSchema := cfg.GetDefaultSchema()
	builtins := parser.BuiltinPlaceholders{
		DefaultSchema: defaultSchema,
		Table:         cfg.Table,
		Database:      cfg.GCPProjectID,
		Timestamp:     time.Now(),
	}

	// Discover callbacks
	resolvedCallbacks := make(map[string][]resolver.ResolvedCallback)
	if resolveRes, err := res.Resolve(); err == nil {
		resolvedCallbacks = resolveRes.Callbacks
	}

	cbRunner := NewCallbackRunner(db, resolvedCallbacks, bqParser, replacer, builtins)

	return &Migrator{
		config:         cfg,
		db:             db,
		resolver:       res,
		parser:         bqParser,
		replacer:       replacer,
		callbackRunner: cbRunner,
		builtins:       builtins,
	}, nil
}

// Migrate executes pending migrations.
func (m *Migrator) Migrate(ctx context.Context) (*MigrateResult, error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table
	overallStart := time.Now()

	// 1. Ensure schemas exist
	for _, schema := range m.config.Schemas {
		if err := m.db.EnsureSchema(ctx, schema); err != nil {
			return nil, fmt.Errorf("failed to ensure schema '%s' exists: %w", schema, err)
		}
	}

	// 2. Ensure history table exists
	historyExists, err := m.db.HistoryTableExists(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to check history table: %w", err)
	}

	if !historyExists {
		if err := m.db.EnsureHistoryTable(ctx, defaultSchema, table); err != nil {
			return nil, fmt.Errorf("failed to create history table: %w", err)
		}
	}

	// 3. Acquire database lock
	unlock, err := m.db.Lock(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire database lock: %w", err)
	}
	defer func() {
		_ = unlock(context.Background())
	}()

	// 4. Fetch applied migrations
	applied, err := m.db.FetchHistory(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch schema history: %w", err)
	}

	// Handle baselineOnMigrate if newly created history table or non-empty unmanaged DB
	if len(applied) == 0 && m.config.BaselineOnMigrate {
		isEmpty, err := m.db.SchemaEmpty(ctx, defaultSchema)
		if err == nil && !isEmpty {
			_, err = m.baselineInternal(ctx, defaultSchema, table)
			if err != nil {
				return nil, fmt.Errorf("baseline on migrate failed: %w", err)
			}
			applied, err = m.db.FetchHistory(ctx, defaultSchema, table)
			if err != nil {
				return nil, err
			}
		}
	}

	// 5. Resolve local migrations
	resolved, err := m.resolver.Resolve()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve migrations: %w", err)
	}
	m.callbackRunner.callbacks = resolved.Callbacks

	// 6. Validate on migrate
	if m.config.ValidateOnMigrate {
		valRes, err := m.Validate(ctx)
		if err != nil {
			return nil, fmt.Errorf("validation error: %w", err)
		}
		if !valRes.Valid {
			if m.config.CleanOnValidationError && !m.config.CleanDisabled {
				_, _ = m.Clean(ctx)
				applied = nil
			} else {
				return nil, fmt.Errorf("%s", valRes.Error())
			}
		}
	}

	// Determine installed rank sequence and current highest version
	nextInstalledRank := 1
	var maxAppliedVersion *version.Version
	var baselineVersion *version.Version
	appliedByVersion := make(map[string]resolver.AppliedMigration)
	appliedRepeatables := make(map[string]resolver.AppliedMigration) // script -> last applied

	for _, app := range applied {
		if app.InstalledRank >= nextInstalledRank {
			nextInstalledRank = app.InstalledRank + 1
		}
		if app.Type == "BASELINE" && app.Version != nil {
			baselineVersion = app.Version
		}
		if app.Version != nil && !app.Version.IsEmpty() {
			appliedByVersion[app.Version.Normalized()] = app
			if maxAppliedVersion == nil || app.Version.IsNewerThan(*maxAppliedVersion) {
				maxAppliedVersion = app.Version
			}
		} else {
			appliedRepeatables[app.Script] = app
		}
	}

	initialVersionStr := "<< Empty >>"
	if maxAppliedVersion != nil {
		initialVersionStr = maxAppliedVersion.String()
	}

	// Determine effective target version (supports latest, current, next, or specific version)
	var effectiveTargetVersion *version.Version
	if m.config.TargetVersion != nil {
		effectiveTargetVersion = m.config.TargetVersion
	} else if strings.EqualFold(m.config.Target, "current") {
		if maxAppliedVersion != nil {
			effectiveTargetVersion = maxAppliedVersion
		} else {
			emptyVer := version.Empty
			effectiveTargetVersion = &emptyVer
		}
	} else if strings.EqualFold(m.config.Target, "next") {
		for _, res := range resolved.VersionedMigrations {
			if res.Version == nil {
				continue
			}
			verKey := res.Version.Normalized()
			if _, wasApplied := appliedByVersion[verKey]; !wasApplied {
				if baselineVersion == nil || !baselineVersion.IsAtLeast(*res.Version) {
					effectiveTargetVersion = res.Version
					break
				}
			}
		}
		if effectiveTargetVersion == nil {
			effectiveTargetVersion = maxAppliedVersion
		}
	}

	// 7. Find pending versioned migrations
	var pendingVersioned []resolver.ResolvedMigration
	for _, res := range resolved.VersionedMigrations {
		if res.Version == nil {
			continue
		}

		verKey := res.Version.Normalized()
		if _, wasApplied := appliedByVersion[verKey]; wasApplied {
			continue
		}

		// Check baseline
		if baselineVersion != nil && baselineVersion.IsAtLeast(*res.Version) {
			continue
		}

		// Check target version
		if effectiveTargetVersion != nil && res.Version.IsNewerThan(*effectiveTargetVersion) {
			continue
		}

		// Check out-of-order
		if maxAppliedVersion != nil && maxAppliedVersion.IsNewerThan(*res.Version) && !m.config.OutOfOrder {
			continue
		}

		pendingVersioned = append(pendingVersioned, res)
	}

	// 8. Find pending / modified repeatable migrations
	var pendingRepeatable []resolver.ResolvedMigration
	for _, res := range resolved.RepeatableMigrations {
		lastRun, wasApplied := appliedRepeatables[res.Script]
		if !wasApplied {
			pendingRepeatable = append(pendingRepeatable, res)
		} else if lastRun.Checksum == nil || *lastRun.Checksum != res.Checksum || !lastRun.Success {
			pendingRepeatable = append(pendingRepeatable, res)
		}
	}

	totalToApply := len(pendingVersioned) + len(pendingRepeatable)
	if totalToApply == 0 {
		return &MigrateResult{
			InitialVersion:     initialVersionStr,
			TargetVersion:      initialVersionStr,
			MigrationsExecuted: 0,
			Success:            true,
			TotalExecutionTime: time.Since(overallStart).Milliseconds(),
		}, nil
	}

	// 9. Fire beforeMigrate callback
	if err := m.callbackRunner.Fire(ctx, "beforeMigrate"); err != nil {
		return nil, err
	}

	// Determine user
	user := m.config.InstalledBy
	if user == "" {
		currentUser, _ := m.db.GetCurrentUser(ctx)
		if currentUser != "" {
			user = currentUser
		} else {
			user = "flyway"
		}
	}
	m.builtins.User = user

	result := &MigrateResult{
		InitialVersion:     initialVersionStr,
		Success:            true,
		ExecutedMigrations: make([]ExecutedSummary, 0, totalToApply),
	}

	currentVersion := maxAppliedVersion

	// 10. Execute Versioned Migrations
	for _, mig := range pendingVersioned {
		execSummary, err := m.executeMigration(ctx, mig, defaultSchema, table, nextInstalledRank, user)
		if err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			result.Success = false
			return result, err
		}
		result.ExecutedMigrations = append(result.ExecutedMigrations, *execSummary)
		result.MigrationsExecuted++
		nextInstalledRank++
		currentVersion = mig.Version
	}

	// 11. Execute Repeatable Migrations
	for _, mig := range pendingRepeatable {
		execSummary, err := m.executeMigration(ctx, mig, defaultSchema, table, nextInstalledRank, user)
		if err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			result.Success = false
			return result, err
		}
		result.ExecutedMigrations = append(result.ExecutedMigrations, *execSummary)
		result.MigrationsExecuted++
		nextInstalledRank++
	}

	// 12. Fire afterMigrate callback
	if err := m.callbackRunner.Fire(ctx, "afterMigrate"); err != nil {
		return nil, err
	}

	if currentVersion != nil {
		result.TargetVersion = currentVersion.String()
	} else {
		result.TargetVersion = initialVersionStr
	}
	result.TotalExecutionTime = time.Since(overallStart).Milliseconds()

	return result, nil
}

func (m *Migrator) executeMigration(
	ctx context.Context,
	mig resolver.ResolvedMigration,
	schema, table string,
	rank int,
	user string,
) (*ExecutedSummary, error) {
	// Fire beforeEachMigrate callback
	if err := m.callbackRunner.Fire(ctx, "beforeEachMigrate"); err != nil {
		return nil, err
	}

	// Perform placeholder replacement
	sql, err := m.replacer.Replace(mig.Content, m.builtins)
	if err != nil {
		return nil, fmt.Errorf("error replacing placeholders in %s: %w", mig.Script, err)
	}

	// Split statements
	stmts := m.parser.SplitStatements(sql)
	startTime := time.Now()

	var execErr error
	for _, stmt := range stmts {
		if err := m.db.ExecuteStatement(ctx, stmt.SQL); err != nil {
			execErr = fmt.Errorf("error executing statement at line %d in %s: %w", stmt.LineNumber, mig.Script, err)
			break
		}
	}

	execTime := time.Since(startTime).Milliseconds()

	var verStr *string
	if mig.Version != nil {
		s := mig.Version.String()
		verStr = &s
	}
	cs := mig.Checksum

	historyRec := database.HistoryRecord{
		InstalledRank: rank,
		Version:       verStr,
		Description:   mig.Description,
		Type:          "SQL",
		Script:        mig.Script,
		Checksum:      &cs,
		InstalledBy:   user,
		InstalledOn:   time.Now(),
		ExecutionTime: execTime,
		Success:       execErr == nil,
	}

	// Record in history table
	if recErr := m.db.InsertHistory(ctx, schema, table, historyRec); recErr != nil {
		return nil, fmt.Errorf("migration %s executed, but failed to insert history record: %w", mig.Script, recErr)
	}

	if execErr != nil {
		_ = m.callbackRunner.Fire(ctx, "beforeEachMigrateError")
		_ = m.callbackRunner.Fire(ctx, "afterEachMigrateError")
		return nil, execErr
	}

	// Fire afterEachMigrate callback
	if err := m.callbackRunner.Fire(ctx, "afterEachMigrate"); err != nil {
		return nil, err
	}

	verOutput := ""
	cat := "Repeatable"
	if mig.Version != nil {
		verOutput = mig.Version.String()
		cat = "Versioned"
	}

	return &ExecutedSummary{
		Category:      cat,
		Version:       verOutput,
		Description:   mig.Description,
		Type:          "SQL",
		Script:        mig.Script,
		ExecutionTime: execTime,
	}, nil
}
