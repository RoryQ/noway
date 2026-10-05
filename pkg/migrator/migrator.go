package migrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

	res := resolver.NewResolver(resolver.ResolverConfig{
		Locations:        cfg.Locations,
		FS:               cfg.FS,
		Prefix:           cfg.SQLMigrationPrefix,
		RepeatablePrefix: cfg.RepeatableSQLMigrationPrefix,
		UndoPrefix:       cfg.UndoSQLMigrationPrefix,
		BaselinePrefix:   cfg.BaselineSQLMigrationPrefix,
		Separator:        cfg.SQLMigrationSeparator,
		Suffixes:               suffixes,
		Encoding:               cfg.Encoding,
		PlaceholderReplacement: &cfg.PlaceholderReplacement,
		Replacer:               replacer,
		Builtins:               builtins,
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

// Migrate executes pending migrations.
func (m *Migrator) Migrate(ctx context.Context) (*MigrateResult, error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table
	overallStart := time.Now()

	// 1. Ensure schemas exist
	if m.config.CreateSchemas {
		for _, schema := range m.config.Schemas {
			if err := m.db.EnsureSchema(ctx, schema); err != nil {
				return nil, fmt.Errorf("failed to ensure schema '%s' exists: %w", schema, err)
			}
		}
	} else {
		for _, schema := range m.config.Schemas {
			exists, err := m.db.SchemaExists(ctx, schema)
			if err != nil {
				return nil, fmt.Errorf("failed to check schema '%s' existence: %w", schema, err)
			}
			if !exists {
				return nil, fmt.Errorf("schema '%s' does not exist and createSchemas is false", schema)
			}
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
	effectiveTargetVersion := m.determineEffectiveTarget(
		maxAppliedVersion,
		baselineVersion,
		appliedByVersion,
		resolved.VersionedMigrations,
	)

	// Check for Cumulative Baseline Migrations on Fresh Database
	var selectedBaseline *resolver.ResolvedMigration
	if len(applied) == 0 && len(resolved.BaselineMigrations) > 0 {
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

	// 7. Find pending versioned migrations
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

	m.callbackRunner.SetOperation("MIGRATE")

	// 9. Fire beforeMigrate callback
	if err := m.callbackRunner.Fire(ctx, "beforeMigrate"); err != nil {
		_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
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

	// Only execute migration loops if totalToApply > 0
	if totalToApply > 0 {
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

			execSummary, err := m.executeMigration(ctx, mig, defaultSchema, table, nextInstalledRank, user)
			if err != nil {
				_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
				result.Success = false
				return result, err
			}
			result.ExecutedMigrations = append(result.ExecutedMigrations, *execSummary)
			result.MigrationsExecuted++
			nextInstalledRank++
			if currentVersion == nil || mig.Version.IsNewerThan(*currentVersion) {
				currentVersion = mig.Version
			}
		}

		// Fire afterVersioned callback
		if err := m.callbackRunner.Fire(ctx, "afterVersioned"); err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			result.Success = false
			return result, err
		}

		// Fire beforeRepeatables callback
		if err := m.callbackRunner.Fire(ctx, "beforeRepeatables"); err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
			result.Success = false
			return result, err
		}

		// 11. Execute Repeatable Migrations
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

		// Fire afterMigrateApplied callback if len(appliedMigrations) > 0
		if len(result.ExecutedMigrations) > 0 {
			if err := m.callbackRunner.Fire(ctx, "afterMigrateApplied"); err != nil {
				_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
				result.Success = false
				return result, err
			}
		}
	}

	// 12. Fire afterMigrate callback
	if err := m.callbackRunner.Fire(ctx, "afterMigrate"); err != nil {
		_ = m.callbackRunner.Fire(ctx, "afterMigrateError")
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
		replaced, err := m.replacer.ReplaceContent(sql, m.builtins)
		if err != nil {
			return nil, fmt.Errorf("error replacing placeholders in %s: %w", mig.Script, err)
		}
		sql = replaced
	}

	// Split statements
	stmts := m.parser.SplitStatements(sql)
	startTime := time.Now()

	var execErr error
	for _, stmt := range stmts {
		if err := m.callbackRunner.Fire(ctx, "beforeEachMigrateStatement"); err != nil {
			execErr = err
			break
		}
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

	historyRec := database.HistoryRecord{
		InstalledRank: rank,
		Version:       verStr,
		Description:   mig.Description,
		Type:          recType,
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
	scriptContent := mig.Content

	shouldReplace := m.config.PlaceholderReplacement
	if mig.Config.PlaceholderReplacement != nil {
		shouldReplace = *mig.Config.PlaceholderReplacement
	}
	if shouldReplace {
		replaced, err := m.replacer.ReplaceContent(scriptContent, m.builtins)
		if err == nil {
			scriptContent = replaced
		}
	}

	var scriptPath string
	var cleanup func()

	if mig.PhysicalLocation != "" && scriptContent == mig.Content {
		if fi, err := os.Stat(mig.PhysicalLocation); err == nil && !fi.IsDir() {
			scriptPath = mig.PhysicalLocation
			_ = os.Chmod(scriptPath, 0644)
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
		if _, err := tmpFile.WriteString(scriptContent); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return nil, fmt.Errorf("failed writing temp script for %s: %w", mig.Script, err)
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

	if mig.PhysicalLocation != "" {
		dir := filepath.Dir(mig.PhysicalLocation)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			cmd.Dir = dir
		}
	}

	env := os.Environ()
	env = append(env,
		"FLYWAY_DATABASE="+schema,
		"NOWAY_DATABASE="+schema,
		"FLYWAY_USER="+user,
		"NOWAY_USER="+user,
		"FLYWAY_DEFAULT_SCHEMA="+schema,
		"NOWAY_DEFAULT_SCHEMA="+schema,
		"FLYWAY_SCHEMAS="+strings.Join(m.config.Schemas, ","),
		"NOWAY_SCHEMAS="+strings.Join(m.config.Schemas, ","),
		"FLYWAY_TABLE="+table,
		"NOWAY_TABLE="+table,
		"FLYWAY_TYPE=SCRIPT",
		"NOWAY_TYPE=SCRIPT",
		"FLYWAY_SCRIPT="+mig.Script,
		"NOWAY_SCRIPT="+mig.Script,
		"FLYWAY_DESCRIPTION="+mig.Description,
		"NOWAY_DESCRIPTION="+mig.Description,
		"FLYWAY_GCP_PROJECT_ID="+m.config.GCPProjectID,
		"NOWAY_GCP_PROJECT_ID="+m.config.GCPProjectID,
		"FLYWAY_GCP_DATASET="+m.config.GCPDataset,
		"NOWAY_GCP_DATASET="+m.config.GCPDataset,
		"FLYWAY_GCP_LOCATION="+m.config.GCPLocation,
		"NOWAY_GCP_LOCATION="+m.config.GCPLocation,
		"FLYWAY_BIGQUERY_ENDPOINT="+m.config.GCPBigQueryEndpoint,
		"NOWAY_BIGQUERY_ENDPOINT="+m.config.GCPBigQueryEndpoint,
	)
	if mig.Version != nil {
		env = append(env,
			"FLYWAY_VERSION="+mig.Version.String(),
			"NOWAY_VERSION="+mig.Version.String(),
		)
	}
	for k, v := range m.config.Placeholders {
		env = append(env,
			"FP__"+k+"="+v,
			"FLYWAY_PLACEHOLDER_"+k+"="+v,
			"NOWAY_PLACEHOLDER_"+k+"="+v,
		)
	}
	cmd.Env = env

	output, execErr := cmd.CombinedOutput()
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
		Type:          "SCRIPT",
		Script:        mig.Script,
		Checksum:      &cs,
		InstalledBy:   user,
		InstalledOn:   time.Now(),
		ExecutionTime: execTime,
		Success:       execErr == nil,
	}

	if recErr := m.db.InsertHistory(ctx, schema, table, historyRec); recErr != nil {
		return nil, fmt.Errorf("script migration %s executed, but failed to insert history record: %w", mig.Script, recErr)
	}

	if execErr != nil {
		_ = m.callbackRunner.Fire(ctx, "beforeEachMigrateError")
		_ = m.callbackRunner.Fire(ctx, "afterEachMigrateError")
		return nil, fmt.Errorf("script migration %s failed (exit %v): %w\nOutput:\n%s", mig.Script, execErr, execErr, string(output))
	}

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
		Type:          "SCRIPT",
		Script:        mig.Script,
		ExecutionTime: execTime,
	}, nil
}
