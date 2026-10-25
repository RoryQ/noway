package migrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/earthboundkid/versioninfo/v2"
	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/parser"
	"github.com/roryq/noway/pkg/resolver"
	"github.com/roryq/noway/pkg/version"
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

// MigrateResult holds the outcome of a migration run matching Flyway schema.
type MigrateResult struct {
	InitialSchemaVersion string            `json:"initialSchemaVersion"`
	TargetSchemaVersion  string            `json:"targetSchemaVersion"`
	SchemaName           string            `json:"schemaName"`
	Migrations           []ExecutedSummary `json:"migrations"`
	MigrationsExecuted   int               `json:"migrationsExecuted"`
	Success              bool              `json:"success"`
	FlywayVersion        string            `json:"flywayVersion"`
	Database             string            `json:"database"`
	Warnings             []string          `json:"warnings"`
	Operation            string            `json:"operation"`

	// Deprecated: Alias fields for backward-compatibility with Go code and tests
	InitialVersion     string            `json:"-"`
	TargetVersion      string            `json:"-"`
	ExecutedMigrations []ExecutedSummary `json:"-"`
	TotalExecutionTime int64             `json:"-"`
}

// ExecutedSummary describes an applied migration during the run matching Flyway MigrateOutput.
type ExecutedSummary struct {
	Category      string `json:"category"`
	Version       string `json:"version"`
	Description   string `json:"description"`
	Type          string `json:"type"`
	Script        string `json:"filepath"`
	ExecutionTime int64  `json:"executionTime"`
}

// New creates a new Migrator instance.
func New(in *config.Configuration, db database.Database) (*Migrator, error) {
	if in == nil {
		in = config.NewDefaultConfiguration()
	}
	cfg := in.Clone()
	if err := cfg.Finalize(); err != nil {
		return nil, fmt.Errorf("configuration error: %w", err)
	}

	suffixes := cfg.SQLMigrationSuffixes
	if len(suffixes) == 0 {
		suffixes = []string{".sql", ".sh", ".bash", ".cmd", ".ps1", ".bat", ".py"}
	} else {
		hasScript := false
		for _, s := range suffixes {
			if isScriptFile("x" + s) {
				hasScript = true
				break
			}
		}
		if !hasScript {
			suffixes = append(suffixes, ".sh", ".bash", ".cmd", ".ps1", ".bat", ".py")
		}
	}

	bqParser := parser.NewBigQueryParser()
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled:        cfg.PlaceholderReplacement,
		Prefix:         cfg.PlaceholderPrefix,
		Suffix:         cfg.PlaceholderSuffix,
		Separator:      cfg.PlaceholderSeparator,
		Values:         cfg.Placeholders,
		ScriptPrefix:   cfg.ScriptPlaceholderPrefix,
		ScriptSuffix:   cfg.ScriptPlaceholderSuffix,
	})

	defaultSchema := cfg.GetDefaultSchema()
	user := cfg.User
	if user == "" {
		user = cfg.InstalledBy
	}
	if user == "" {
		user = "flyway"
	}

	builtins := parser.BuiltinPlaceholders{
		DefaultSchema: defaultSchema,
		Table:         cfg.Table,
		Database:      cfg.GCPProjectID,
		User:          user,
		Timestamp:     time.Now(),
		Environment:   cfg.Environment,
	}

	res := resolver.NewResolver(resolver.ResolverConfig{
		Locations:               cfg.ResolvedLocations(),
		FS:                      cfg.FS,
		Prefix:                  cfg.SQLMigrationPrefix,
		RepeatablePrefix:        cfg.RepeatableSQLMigrationPrefix,
		UndoPrefix:              cfg.UndoSQLMigrationPrefix,
		BaselinePrefix:          cfg.BaselineSQLMigrationPrefix,
		Separator:               cfg.SQLMigrationSeparator,
		Suffixes:                suffixes,
		SQLMigrationSuffixes:    cfg.SQLMigrationSuffixes,
		Encoding:                cfg.Encoding,
		FailOnMissingLocations:  cfg.FailOnMissingLocations,
		ValidateMigrationNaming: cfg.ValidateMigrationNaming,
		PlaceholderReplacement:  &cfg.PlaceholderReplacement,
		Replacer:                replacer,
		Builtins:                builtins,
	})

	// Discover callbacks
	resolvedCallbacks := make(map[string][]resolver.ResolvedCallback)
	if resolveRes, err := res.Resolve(); err == nil {
		resolvedCallbacks = resolveRes.Callbacks
	}

	cbRunner := NewCallbackRunner(db, resolvedCallbacks, bqParser, replacer, builtins)
	cbRunner.SetConfig(cfg)

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

// determineEffectiveTarget unifies target version resolution across Migrate() and Info().
// Handles:
// - "latest": returns nil (no cutoff)
// - "current": returns maxAppliedVersion, or version.Empty if none applied
// - "next": returns first eligible unapplied versioned migration (skipping < maxAppliedVersion if !OutOfOrder)
// - concrete numeric version strings (e.g. "2.0", with optional "?" trimmed) or cfg.TargetVersion
func (m *Migrator) determineEffectiveTarget(
	maxAppliedVersion *version.Version,
	baselineVersion *version.Version,
	appliedByVersion map[string]resolver.AppliedMigration,
	resolvedVersioned []resolver.ResolvedMigration,
) *version.Version {
	targetStr := strings.TrimSpace(m.config.Target)
	targetVer := m.config.TargetVersion

	isCurrent := strings.EqualFold(targetStr, "current") || (targetVer != nil && targetVer.IsCurrent())
	if isCurrent {
		if maxAppliedVersion != nil {
			return maxAppliedVersion
		}
		empty := version.Empty
		return &empty
	}

	isNext := strings.EqualFold(targetStr, "next") || (targetVer != nil && targetVer.IsNext())
	if isNext {
		for _, res := range resolvedVersioned {
			if res.Version == nil {
				continue
			}
			verKey := res.Version.Normalized()
			if _, wasApplied := appliedByVersion[verKey]; wasApplied {
				continue
			}
			if baselineVersion != nil && baselineVersion.IsAtLeast(*res.Version) {
				continue
			}
			if maxAppliedVersion != nil && maxAppliedVersion.IsNewerThan(*res.Version) && !m.config.OutOfOrder {
				continue
			}
			return res.Version
		}
		if maxAppliedVersion != nil {
			return maxAppliedVersion
		}
		empty := version.Empty
		return &empty
	}

	isLatest := strings.EqualFold(targetStr, "latest") || (targetVer != nil && targetVer.IsLatest())
	if isLatest {
		return nil
	}

	if targetVer != nil && !targetVer.IsPredefined() {
		return targetVer
	}

	if targetStr != "" {
		cleanTarget := strings.TrimSuffix(targetStr, "?")
		v, err := version.Parse(cleanTarget)
		if err == nil && !v.IsPredefined() {
			return &v
		}
	}

	return nil
}

// DatabaseName returns the logical database name matching Flyway conventions.
func (m *Migrator) DatabaseName() string {
	if m.config.GCPProjectID != "" {
		return m.config.GCPProjectID
	}
	if m.config.GCPSpannerDatabaseID != "" {
		return m.config.GCPSpannerDatabaseID
	}
	if m.config.GCPDataset != "" {
		return m.config.GCPDataset
	}
	if m.config.Driver != "" {
		return m.config.Driver
	}
	return "BigQuery"
}

// FlywayVersion returns the Flyway/Noway version string matching Flyway conventions.
func (m *Migrator) FlywayVersion() string {
	if versioninfo.Version != "" && versioninfo.Version != "unknown" && versioninfo.Version != "(devel)" {
		return versioninfo.Version
	}
	return versioninfo.Short()
}

func (m *Migrator) ensureUser(ctx context.Context) string {
	user := m.config.User
	if user == "" {
		user = m.config.InstalledBy
	}
	if user == "" && m.db != nil {
		currentUser, _ := m.db.GetCurrentUser(ctx)
		if currentUser != "" && currentUser != "unknown" {
			user = currentUser
		}
	}
	if user == "" {
		user = "flyway"
	}
	m.builtins.User = user
	if m.resolver != nil {
		m.resolver.SetBuiltins(m.builtins)
	}
	if m.callbackRunner != nil {
		m.callbackRunner.SetBuiltins(m.builtins)
	}
	return user
}

// Migrate executes pending migrations.
func (m *Migrator) Migrate(ctx context.Context) (res *MigrateResult, retErr error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table
	overallStart := time.Now()

	m.callbackRunner.SetOperation("MIGRATE")

	// Determine user early for callbacks & metadata
	user := m.ensureUser(ctx)
	installedBy := m.config.InstalledBy
	if installedBy == "" {
		installedBy = user
	}

	var createdSchemas []string
	isDryRun := m.config.DryRunOutput != ""

	// 1. Ensure schemas exist
	for _, schema := range m.config.Schemas {
		exists, err := m.db.SchemaExists(ctx, schema)
		if err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			return nil, fmt.Errorf("failed to check schema '%s' existence: %w", schema, err)
		}
		if !exists {
			if !m.config.CreateSchemas {
				_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
				return nil, fmt.Errorf("schema '%s' does not exist and createSchemas is false", schema)
			}
			createdSchemas = append(createdSchemas, schema)
			if !isDryRun {
				if err := m.callbackRunner.Fire(ctx, "beforeCreateSchema"); err != nil {
					_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
					return nil, err
				}
				if err := m.callbackRunner.Fire(ctx, "createSchema"); err != nil {
					_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
					return nil, err
				}
				if err := m.db.EnsureSchema(ctx, schema); err != nil {
					_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
					return nil, fmt.Errorf("failed to ensure schema '%s' exists: %w", schema, err)
				}
				if err := m.callbackRunner.Fire(ctx, "afterCreateSchema"); err != nil {
					_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
					return nil, err
				}
			}
		}
	}

	// 2. Ensure history table exists and handle baselineOnMigrate
	historyExists, err := m.db.HistoryTableExists(ctx, defaultSchema, table)
	if err != nil {
		_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
		return nil, fmt.Errorf("failed to check history table: %w", err)
	}

	var nonEmptySchemas []string
	if !historyExists {
		for _, schema := range m.config.Schemas {
			empty, err := m.db.SchemaEmpty(ctx, schema)
			if err != nil {
				_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
				return nil, fmt.Errorf("failed to check if schema '%s' is empty: %w", schema, err)
			}
			if !empty {
				nonEmptySchemas = append(nonEmptySchemas, schema)
			}
		}

		if len(nonEmptySchemas) > 0 && !m.config.BaselineOnMigrate {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			return nil, fmt.Errorf("Found non-empty schema(s) %s but no schema history table. Use baseline() or set baselineOnMigrate to true to initialize the schema history table.", strings.Join(nonEmptySchemas, ", "))
		}

		if !isDryRun {
			if len(nonEmptySchemas) == 0 {
				// All schemas empty -> create history table
				if err := m.db.EnsureHistoryTable(ctx, defaultSchema, table); err != nil {
					_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
					return nil, fmt.Errorf("failed to create history table: %w", err)
				}
			} else if m.config.BaselineOnMigrate {
				if err := m.db.EnsureHistoryTable(ctx, defaultSchema, table); err != nil {
					_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
					return nil, fmt.Errorf("failed to create history table: %w", err)
				}
				_, err := m.baselineInternal(ctx, defaultSchema, table)
				if err != nil {
					_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
					return nil, fmt.Errorf("baseline on migrate failed: %w", err)
				}
			}
		}
	}

	// 3. Acquire database lock (if not dry run)
	if !isDryRun {
		slog.Debug("Acquiring database lock", "schema", defaultSchema, "table", table)
		unlock, err := m.db.Lock(ctx, defaultSchema, table)
		if err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			return nil, fmt.Errorf("failed to acquire database lock: %w", err)
		}
		slog.Debug("Acquired database lock", "schema", defaultSchema, "table", table)
		defer func() {
			slog.Debug("Releasing database lock", "schema", defaultSchema, "table", table)
			if uErr := unlock(context.Background()); uErr != nil && retErr == nil {
				retErr = uErr
			}
		}()

		// 4. Fire beforeMigrate callback
		if err := m.callbackRunner.Fire(ctx, "beforeMigrate"); err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			return nil, err
		}
	}

	// 5. Fetch applied migrations
	var applied []resolver.AppliedMigration
	if historyExists || !isDryRun {
		applied, err = m.db.FetchHistory(ctx, defaultSchema, table)
		if err != nil && !isDryRun {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			return nil, fmt.Errorf("failed to fetch schema history: %w", err)
		}
	}

	// Count non-schema applied migrations
	nonSchemaApplied := 0
	for _, app := range applied {
		if app.Type != "SCHEMA" {
			nonSchemaApplied++
		}
	}

	// 6. Resolve local migrations
	resolved, err := m.resolver.Resolve()
	if err != nil {
		if !isDryRun {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
		}
		return nil, fmt.Errorf("failed to resolve migrations: %w", err)
	}
	m.callbackRunner.callbacks = resolved.Callbacks

	// 7. Validate on migrate
	if m.config.ValidateOnMigrate {
		valRes, err := m.validate(ctx, validateOptions{ignorePending: true})
		if err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			return nil, fmt.Errorf("validation error: %w", err)
		}
		if !valRes.Valid {
			if m.config.CleanOnValidationError && !m.config.CleanDisabled {
				if _, err := m.cleanInternal(ctx, table, true); err != nil {
					_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
					return nil, fmt.Errorf("cleanOnValidationError failed: %w", err)
				}
				applied = nil
			} else {
				_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
				return nil, fmt.Errorf("%s", valRes.Error())
			}
		}
	}

	// Target check for failOnMissingTarget
	targetStr := strings.TrimSpace(m.config.Target)
	if targetStr != "" && !strings.EqualFold(targetStr, "latest") && !strings.EqualFold(targetStr, "current") && !strings.EqualFold(targetStr, "next") {
		cleanTarget := strings.TrimSuffix(targetStr, "?")
		isOptional := strings.HasSuffix(targetStr, "?")
		if m.config.FailOnMissingTarget && !isOptional {
			found := false
			for _, res := range resolved.VersionedMigrations {
				if res.Version != nil && (res.Version.String() == cleanTarget || res.Version.Normalized() == cleanTarget) {
					found = true
					break
				}
			}
			if !found {
				if !isDryRun {
					_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
				}
				return nil, fmt.Errorf("no migration with a target version %s could be found", cleanTarget)
			}
		}
	}

	if len(m.config.CherryPick) > 0 && m.config.FailOnMissingTarget {
		for _, cp := range m.config.CherryPick {
			found := false
			singleCfg := config.Configuration{CherryPick: []string{cp}}
			for _, res := range resolved.VersionedMigrations {
				if singleCfg.IsPicked(res.Version, res.Description, res.Script) {
					found = true
					break
				}
			}
			if !found {
				for _, res := range resolved.RepeatableMigrations {
					if singleCfg.IsPicked(nil, res.Description, res.Script) {
						found = true
						break
					}
				}
			}
			if !found {
				if !isDryRun {
					_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
				}
				return nil, fmt.Errorf("no migration with version %s could be found to cherry pick", cp)
			}
		}
	}

	// Process applied history: failed, undone, and deleted migrations are NOT counted as applied
	history := processAppliedHistory(applied)

	var maxResolvedVersion *version.Version
	for _, res := range resolved.VersionedMigrations {
		if res.Version != nil {
			if maxResolvedVersion == nil || res.Version.IsNewerThan(*maxResolvedVersion) {
				maxResolvedVersion = res.Version
			}
		}
	}

	// Flyway parity: refuse to migrate if schema history contains an unrepaired failed migration
	// (even when validateOnMigrate=false), because non-transactional DDL databases (BigQuery, Spanner)
	// would re-apply partial changes or apply later migrations on top of a broken state.
	// EXCEPTION: Flyway only warns if exactly ONE failed migration exists, and that migration is a future version (with ignoreFutureMigrations=true or future ignored).
	var blockingFailed []resolver.AppliedMigration
	var futureFailedWarnings []string
	if len(history.FailedMigrations) == 1 {
		failed := history.FailedMigrations[0]
		isFuture := failed.Version != nil && !failed.Version.IsEmpty() && (maxResolvedVersion == nil || failed.Version.IsNewerThan(*maxResolvedVersion))
		if isFuture && m.isIgnored("future", failed) {
			futureFailedWarnings = append(futureFailedWarnings, fmt.Sprintf("Schema %s contains a failed future migration to version %s (%s)!", defaultSchema, failed.Version.String(), failed.Script))
		} else {
			blockingFailed = append(blockingFailed, failed)
		}
	} else if len(history.FailedMigrations) > 1 {
		blockingFailed = append(blockingFailed, history.FailedMigrations...)
	}

	if len(blockingFailed) > 0 {
		if !isDryRun {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
		}
		failed := blockingFailed[0]
		if failed.Version != nil && !failed.Version.IsEmpty() {
			return nil, fmt.Errorf("Schema %s contains a failed migration to version %s (%s)! Please run repair to fix the schema history.", defaultSchema, failed.Version.String(), failed.Script)
		}
		return nil, fmt.Errorf("Schema %s contains a failed repeatable migration (%s)! Please run repair to fix the schema history.", defaultSchema, failed.Script)
	}

	nextInstalledRank := history.NextInstalledRank
	maxAppliedVersion := history.MaxAppliedVersion
	baselineVersion := history.BaselineVersion
	appliedByVersion := history.AppliedVersioned

	if isDryRun && !historyExists && len(nonEmptySchemas) > 0 && m.config.BaselineOnMigrate {
		bVerStr := m.config.BaselineVersion
		if bVerStr == "" {
			bVerStr = "1"
		}
		if v, err := version.Parse(bVerStr); err == nil {
			baselineVersion = &v
		}
		if nextInstalledRank <= 1 {
			nextInstalledRank = 2
		}
	}

	initialVersionStr := "<< Empty >>"
	if maxAppliedVersion != nil {
		initialVersionStr = maxAppliedVersion.String()
	}

	// Determine effective target version (supports latest, current, next, or specific version)
	effectiveTargetVersion := m.determineEffectiveTarget(
		maxAppliedVersion,
		baselineVersion,
		appliedByVersion,
		resolved.VersionedMigrations,
	)

	// Check for Cumulative Baseline Migrations on Fresh Database
	var selectedBaseline *resolver.ResolvedMigration
	if nonSchemaApplied == 0 && len(resolved.BaselineMigrations) > 0 {
		for i := len(resolved.BaselineMigrations) - 1; i >= 0; i-- {
			b := resolved.BaselineMigrations[i]
			if b.Version == nil {
				continue
			}
			if effectiveTargetVersion == nil || !b.Version.IsNewerThan(*effectiveTargetVersion) {
				selectedBaseline = &resolved.BaselineMigrations[i]
				break
			}
		}
	}

	// 8. Find pending versioned migrations
	var pendingVersioned []resolver.ResolvedMigration
	if selectedBaseline != nil {
		baselineVersion = selectedBaseline.Version
		pendingVersioned = append(pendingVersioned, *selectedBaseline)
	}

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

		// Check cherryPick
		if len(m.config.CherryPick) > 0 {
			if !m.config.IsPicked(res.Version, res.Description, res.Script) {
				continue
			}
		}

		// Check target version
		if effectiveTargetVersion != nil && res.Version.IsNewerThan(*effectiveTargetVersion) {
			continue
		}

		// Check out-of-order (bypassed if cherryPick is specified)
		if maxAppliedVersion != nil && maxAppliedVersion.IsNewerThan(*res.Version) && !m.config.OutOfOrder && len(m.config.CherryPick) == 0 {
			continue
		}

		pendingVersioned = append(pendingVersioned, res)
	}

	// 9. Find pending / modified repeatable migrations
	var pendingRepeatable []resolver.ResolvedMigration
	for _, res := range resolved.RepeatableMigrations {
		if len(m.config.CherryPick) > 0 && !m.config.IsPicked(nil, res.Description, res.Script) {
			continue
		}
		lastRun, wasApplied := history.FindAppliedRepeatable(res)
		if !wasApplied {
			pendingRepeatable = append(pendingRepeatable, res)
		} else if !lastRun.Success || !res.ChecksumMatches(lastRun.Checksum) {
			pendingRepeatable = append(pendingRepeatable, res)
		}
	}

	totalToApply := len(pendingVersioned) + len(pendingRepeatable)

	if isDryRun {
		return m.migrateDryRun(
			ctx,
			defaultSchema, table, user,
			createdSchemas,
			historyExists,
			nonEmptySchemas,
			pendingVersioned, pendingRepeatable,
			initialVersionStr,
			effectiveTargetVersion,
			maxAppliedVersion,
			nextInstalledRank,
		)
	}

	result := &MigrateResult{
		InitialSchemaVersion: initialVersionStr,
		InitialVersion:       initialVersionStr,
		SchemaName:           defaultSchema,
		Migrations:           make([]ExecutedSummary, 0, totalToApply),
		ExecutedMigrations:   make([]ExecutedSummary, 0, totalToApply),
		Success:              true,
		FlywayVersion:        m.FlywayVersion(),
		Database:             m.DatabaseName(),
		Warnings:             futureFailedWarnings,
		Operation:            "migrate",
	}
	if result.Warnings == nil {
		result.Warnings = make([]string, 0)
	}

	currentVersion := maxAppliedVersion

	// 10. Execute Versioned Migrations
	for _, mig := range pendingVersioned {
		if mig.Config.ShouldExecute != "" {
			shouldExec, err := resolver.EvaluateShouldExecute(mig.Config.ShouldExecute, m.replacer, m.builtins)
			if err != nil {
				_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
				result.Success = false
				return result, fmt.Errorf("error evaluating shouldExecute on %s: %w", mig.Script, err)
			}
			if !shouldExec {
				continue
			}
		}

		execSummary, err := m.executeMigration(ctx, mig, defaultSchema, table, nextInstalledRank, installedBy)
		if err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			result.Success = false
			return result, err
		}
		result.Migrations = append(result.Migrations, *execSummary)
		result.ExecutedMigrations = result.Migrations
		result.MigrationsExecuted++
		nextInstalledRank++
		if currentVersion == nil || mig.Version.IsNewerThan(*currentVersion) {
			currentVersion = mig.Version
		}
	}

	// Always fire afterVersioned callback after the versioned loop
	if err := m.callbackRunner.Fire(ctx, "afterVersioned"); err != nil {
		_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
		result.Success = false
		return result, err
	}

	// 11. Execute Repeatable Migrations
	firedBeforeRepeatables := false
	for _, mig := range pendingRepeatable {
		if mig.Config.ShouldExecute != "" {
			shouldExec, err := resolver.EvaluateShouldExecute(mig.Config.ShouldExecute, m.replacer, m.builtins)
			if err != nil {
				_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
				result.Success = false
				return result, fmt.Errorf("error evaluating shouldExecute on %s: %w", mig.Script, err)
			}
			if !shouldExec {
				continue
			}
		}

		if !firedBeforeRepeatables {
			if err := m.callbackRunner.Fire(ctx, "beforeRepeatables"); err != nil {
				_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
				result.Success = false
				return result, err
			}
			firedBeforeRepeatables = true
		}

		execSummary, err := m.executeMigration(ctx, mig, defaultSchema, table, nextInstalledRank, installedBy)
		if err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			result.Success = false
			return result, err
		}
		result.Migrations = append(result.Migrations, *execSummary)
		result.ExecutedMigrations = result.Migrations
		result.MigrationsExecuted++
		nextInstalledRank++
	}

	// Fire afterMigrateApplied callback if len(appliedMigrations) > 0
	if len(result.Migrations) > 0 {
		if err := m.callbackRunner.Fire(ctx, "afterMigrateApplied"); err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			result.Success = false
			return result, err
		}
	}

	// 12. Fire afterMigrate callback
	if err := m.callbackRunner.Fire(ctx, "afterMigrate"); err != nil {
		_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
		return nil, err
	}

	if currentVersion != nil {
		result.TargetSchemaVersion = currentVersion.String()
		result.TargetVersion = currentVersion.String()
	} else {
		result.TargetSchemaVersion = initialVersionStr
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
	if mig.Type == resolver.TypeScript || mig.IsScript {
		return m.executeScriptMigration(ctx, mig, schema, table, rank, user)
	}
	return m.executeSQLMigration(ctx, mig, schema, table, rank, user)
}

func (m *Migrator) executeSQLMigration(
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
	sql := mig.Content
	shouldReplace := m.config.PlaceholderReplacement
	if mig.Config.PlaceholderReplacement != nil {
		shouldReplace = *mig.Config.PlaceholderReplacement
	}
	if shouldReplace {
		builtins := m.builtins
		if builtins.Filename == "" {
			builtins.Filename = filepath.Base(mig.Script)
		}
		replaced, err := m.replacer.ReplaceContent(sql, builtins)
		if err != nil {
			return nil, fmt.Errorf("error replacing placeholders in %s: %w", mig.Script, err)
		}
		sql = replaced
	}

	// Split statements
	stmts := m.parser.SplitStatements(sql)
	startTime := time.Now()

	var execErr error
	if !m.config.SkipExecutingMigrations {
		for _, stmt := range stmts {
			if err := m.callbackRunner.Fire(ctx, "beforeEachMigrateStatement"); err != nil {
				execErr = err
				break
			}
			slog.Debug("Executing SQL statement", "script", mig.Script, "line", stmt.LineNumber)
			if err := m.db.ExecuteStatement(ctx, stmt.SQL); err != nil {
				_ = m.callbackRunner.Fire(ctx, "afterEachMigrateStatementError")
				execErr = fmt.Errorf("error executing statement at line %d in %s: %w", stmt.LineNumber, mig.Script, err)
				break
			}
			if err := m.callbackRunner.Fire(ctx, "afterEachMigrateStatement"); err != nil {
				execErr = err
				break
			}
		}
	}

	execTime := time.Since(startTime).Milliseconds()

	var verStr *string
	if mig.Version != nil {
		s := mig.Version.String()
		verStr = &s
	}
	cs := mig.Checksum

	recType := string(mig.Type)
	if recType == "" {
		recType = "SQL"
	}

	if execErr == nil {
		if afterErr := m.callbackRunner.Fire(ctx, "afterEachMigrate"); afterErr != nil {
			execErr = afterErr
		}
	}

	historyRec := database.HistoryRecord{
		InstalledRank: rank,
		Version:       verStr,
		Description:   database.AbbreviateDescription(mig.Description),
		Type:          recType,
		Script:        database.AbbreviateScript(mig.Script),
		Checksum:      &cs,
		InstalledBy:   user,
		InstalledOn:   time.Now(),
		ExecutionTime: execTime,
		Success:       execErr == nil,
	}

	if execErr != nil {
		_ = m.callbackRunner.Fire(ctx, "beforeEachMigrateError")
		_ = m.callbackRunner.Fire(ctx, "afterEachMigrateError")
	}

	// Record in history table
	if recErr := m.db.InsertHistory(ctx, schema, table, historyRec); recErr != nil {
		if execErr == nil {
			return nil, fmt.Errorf("CRITICAL: migration %s SQL statements succeeded, but failed to insert schema history record (%w). The database schema has been modified, but the migration is unrecorded in %s. Manual intervention is required: manually insert the missing history row into %s or revert the schema changes before running migrate again.", mig.Script, recErr, table, table)
		}
		return nil, fmt.Errorf("migration %s executed with error (%v), and also failed to insert history record: %w", mig.Script, execErr, recErr)
	}

	if execErr != nil {
		return nil, execErr
	}

	verOutput := ""
	cat := "Repeatable"
	if mig.Type == resolver.TypeBaseline {
		cat = "Baseline"
		if mig.Version != nil {
			verOutput = mig.Version.String()
		}
	} else if mig.Version != nil {
		verOutput = mig.Version.String()
		cat = "Versioned"
	}

	return &ExecutedSummary{
		Category:      cat,
		Version:       verOutput,
		Description:   mig.Description,
		Type:          recType,
		Script:        mig.Script,
		ExecutionTime: execTime,
	}, nil
}

func (m *Migrator) executeScriptMigration(
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

	startTime := time.Now()

	var scriptPath string
	var cleanup func()

	if mig.PhysicalLocation != "" {
		if fi, err := os.Stat(mig.PhysicalLocation); err == nil && !fi.IsDir() {
			scriptPath = mig.PhysicalLocation
		}
	}

	if scriptPath == "" {
		ext := filepath.Ext(mig.Script)
		if ext == "" {
			ext = ".sh"
		}
		tmpFile, err := os.CreateTemp("", "noway-migration-*"+ext)
		if err != nil {
			return nil, fmt.Errorf("failed creating temp script for %s: %w", mig.Script, err)
		}
		if _, err := tmpFile.WriteString(mig.Content); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return nil, fmt.Errorf("failed writing temp script for %s: %w", mig.Script, err)
		}
		tmpFile.Close()
		_ = os.Chmod(tmpFile.Name(), 0700)
		scriptPath = tmpFile.Name()
		cleanup = func() { _ = os.Remove(tmpFile.Name()) }
	}
	if cleanup != nil {
		defer cleanup()
	}

	slog.Debug("Executing script migration", "script", mig.Script, "path", scriptPath)

	absScriptPath, err := filepath.Abs(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("resolving script path %s: %w", scriptPath, err)
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

	env := os.Environ()
	setIfNotEmpty := func(k, v string) {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}

	setIfNotEmpty("FLYWAY_URL", m.config.URL)
	setIfNotEmpty("NOWAY_URL", m.config.URL)

	userIfKnown := m.config.User
	if userIfKnown == "" && m.config.InstalledBy != "" {
		userIfKnown = m.config.InstalledBy
	} else if userIfKnown == "" && user != "flyway" && user != "" {
		userIfKnown = user
	}
	setIfNotEmpty("FLYWAY_USER", userIfKnown)
	setIfNotEmpty("NOWAY_USER", userIfKnown)
	setIfNotEmpty("FLYWAY_PASSWORD", m.config.Password)
	setIfNotEmpty("NOWAY_PASSWORD", m.config.Password)

	setIfNotEmpty("FLYWAY_DATABASE", schema)
	setIfNotEmpty("NOWAY_DATABASE", schema)
	setIfNotEmpty("FLYWAY_DEFAULT_SCHEMA", schema)
	setIfNotEmpty("NOWAY_DEFAULT_SCHEMA", schema)
	setIfNotEmpty("FLYWAY_SCHEMAS", strings.Join(m.config.Schemas, ","))
	setIfNotEmpty("NOWAY_SCHEMAS", strings.Join(m.config.Schemas, ","))
	setIfNotEmpty("FLYWAY_TABLE", table)
	setIfNotEmpty("NOWAY_TABLE", table)
	setIfNotEmpty("FLYWAY_TYPE", "SCRIPT")
	setIfNotEmpty("NOWAY_TYPE", "SCRIPT")
	setIfNotEmpty("FLYWAY_SCRIPT", mig.Script)
	setIfNotEmpty("NOWAY_SCRIPT", mig.Script)
	setIfNotEmpty("FLYWAY_DESCRIPTION", mig.Description)
	setIfNotEmpty("NOWAY_DESCRIPTION", mig.Description)
	setIfNotEmpty("FLYWAY_GCP_PROJECT_ID", m.config.GCPProjectID)
	setIfNotEmpty("NOWAY_GCP_PROJECT_ID", m.config.GCPProjectID)
	setIfNotEmpty("FLYWAY_GCP_DATASET", m.config.GCPDataset)
	setIfNotEmpty("NOWAY_GCP_DATASET", m.config.GCPDataset)
	setIfNotEmpty("FLYWAY_GCP_LOCATION", m.config.GCPLocation)
	setIfNotEmpty("NOWAY_GCP_LOCATION", m.config.GCPLocation)
	setIfNotEmpty("FLYWAY_BIGQUERY_ENDPOINT", m.config.GCPBigQueryEndpoint)
	setIfNotEmpty("NOWAY_BIGQUERY_ENDPOINT", m.config.GCPBigQueryEndpoint)

	scriptPfx := m.config.ScriptPlaceholderPrefix
	if scriptPfx == "" {
		scriptPfx = "FP__"
	}
	scriptSfx := m.config.ScriptPlaceholderSuffix
	if scriptSfx == "" {
		scriptSfx = "__"
	}

	setIfNotEmpty(scriptPfx+"flyway_defaultSchema"+scriptSfx, schema)
	setIfNotEmpty(scriptPfx+"flyway_table"+scriptSfx, table)
	setIfNotEmpty(scriptPfx+"flyway_user"+scriptSfx, userIfKnown)
	setIfNotEmpty(scriptPfx+"flyway_database"+scriptSfx, schema)
	setIfNotEmpty(scriptPfx+"flyway_filename"+scriptSfx, filepath.Base(mig.Script))

	wd := m.builtins.WorkingDirectory
	if wd == "" {
		if getwd, err := os.Getwd(); err == nil {
			wd = getwd
		}
	}
	setIfNotEmpty(scriptPfx+"flyway_workingDirectory"+scriptSfx, wd)

	if !m.builtins.Timestamp.IsZero() {
		setIfNotEmpty(scriptPfx+"flyway_timestamp"+scriptSfx, m.builtins.Timestamp.Format("2006-01-02 15:04:05"))
	}
	setIfNotEmpty(scriptPfx+"flyway_environment"+scriptSfx, m.config.Environment)

	if mig.Version != nil {
		setIfNotEmpty("FLYWAY_VERSION", mig.Version.String())
		setIfNotEmpty("NOWAY_VERSION", mig.Version.String())
	}
	for k, v := range m.config.Placeholders {
		cleanK := strings.ReplaceAll(k, ":", "_")
		setIfNotEmpty(scriptPfx+cleanK+scriptSfx, v)
	}
	cmd.Env = env

	var output []byte
	var execErr error
	if !m.config.SkipExecutingMigrations {
		output, execErr = cmd.CombinedOutput()
	}
	execTime := time.Since(startTime).Milliseconds()

	var verStr *string
	if mig.Version != nil {
		s := mig.Version.String()
		verStr = &s
	}
	cs := mig.Checksum

	if execErr == nil {
		if afterErr := m.callbackRunner.Fire(ctx, "afterEachMigrate"); afterErr != nil {
			execErr = afterErr
		}
	}

	historyRec := database.HistoryRecord{
		InstalledRank: rank,
		Version:       verStr,
		Description:   database.AbbreviateDescription(mig.Description),
		Type:          "SCRIPT",
		Script:        database.AbbreviateScript(mig.Script),
		Checksum:      &cs,
		InstalledBy:   user,
		InstalledOn:   time.Now(),
		ExecutionTime: execTime,
		Success:       execErr == nil,
	}

	if execErr != nil {
		_ = m.callbackRunner.Fire(ctx, "beforeEachMigrateError")
		_ = m.callbackRunner.Fire(ctx, "afterEachMigrateError")
	}

	if recErr := m.db.InsertHistory(ctx, schema, table, historyRec); recErr != nil {
		if execErr == nil {
			return nil, fmt.Errorf("CRITICAL: script migration %s completed successfully, but failed to insert schema history record (%w). The database schema has been modified, but the migration is unrecorded in %s. Manual intervention is required: manually insert the missing history row into %s or revert the schema changes before running migrate again.", mig.Script, recErr, table, table)
		}
		return nil, fmt.Errorf("script migration %s executed with error (%v), and also failed to insert history record: %w", mig.Script, execErr, recErr)
	}

	if execErr != nil {
		return nil, fmt.Errorf("script migration %s failed (exit %v): %w\nOutput:\n%s", mig.Script, execErr, execErr, string(output))
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
		Type:          "SCRIPT",
		Script:        mig.Script,
		ExecutionTime: execTime,
	}, nil
}
