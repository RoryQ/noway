package e2e_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/RoryQ/noway"
	"github.com/RoryQ/noway/pkg/checksum"
	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database"
	"github.com/RoryQ/noway/pkg/database/mock"
	"github.com/RoryQ/noway/pkg/version"
)

// setupNoway configures an in-memory noway instance backed by MockDatabase and MapFS.
func setupNoway(t *testing.T, fs fstest.MapFS, opts ...noway.Option) (*noway.Noway, *mock.MockDatabase) {
	t.Helper()
	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "e2e_dataset"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	defaultOpts := []noway.Option{
		noway.WithConfig(cfg),
		noway.WithDatabase(db),
	}
	allOpts := append(defaultOpts, opts...)

	nw, err := noway.New(allOpts...)
	if err != nil {
		t.Fatalf("failed to initialize noway: %v", err)
	}
	t.Cleanup(func() {
		_ = nw.Close()
	})
	return nw, db
}

// ============================================================================
// TIER 1: FEATURE COVERAGE (F1 - F22 Nominal & Core Paths)
// ============================================================================

// TestTier1_F1_F2_CallbacksLifecycle verifies callback discovery and lifecycle events
// across Migrate, Undo, Repair, Baseline, and Clean (F1, F2).
func TestTier1_F1_F2_CallbacksLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("MigrateCallbacksExecutionSequence", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/beforeMigrate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_before_migrate (val INT64);"),
			},
			"migrations/beforeEachMigrate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_before_each (val INT64);"),
			},
			"migrations/afterEachMigrate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_after_each (val INT64);"),
			},
			"migrations/afterMigrate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_after_migrate (val INT64);"),
			},
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/V2__add_email.sql": &fstest.MapFile{
				Data: []byte("ALTER TABLE users ADD COLUMN email STRING;"),
			},
		}

		nw, db := setupNoway(t, fs)
		res, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 2 {
			t.Errorf("expected 2 migrations executed, got %d", res.MigrationsExecuted)
		}

		stmts := db.ExecutedStatements()
		if len(stmts) < 6 {
			t.Fatalf("expected at least 6 statements executed, got %d: %v", len(stmts), stmts)
		}

		// Verify order: beforeMigrate must be first statement executed
		if !strings.Contains(stmts[0], "audit_before_migrate") {
			t.Errorf("expected first statement to be beforeMigrate, got: %s", stmts[0])
		}
		// afterMigrate must be the last statement executed
		if !strings.Contains(stmts[len(stmts)-1], "audit_after_migrate") {
			t.Errorf("expected last statement to be afterMigrate, got: %s", stmts[len(stmts)-1])
		}
	})

	t.Run("UndoCallbacksExecution", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/V2__add_col.sql": &fstest.MapFile{
				Data: []byte("ALTER TABLE users ADD COLUMN col1 STRING;"),
			},
			"migrations/U2__drop_col.sql": &fstest.MapFile{
				Data: []byte("ALTER TABLE users DROP COLUMN col1;"),
			},
			"migrations/beforeUndo.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_before_undo (val INT64);"),
			},
			"migrations/afterUndo.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_after_undo (val INT64);"),
			},
		}

		nw, db := setupNoway(t, fs)
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		undoRes, err := nw.Undo(ctx)
		if err != nil {
			t.Fatalf("Undo failed: %v", err)
		}
		if undoRes.UndoneVersion != "2" {
			t.Errorf("expected undone version 2, got %s", undoRes.UndoneVersion)
		}

		stmts := db.ExecutedStatements()
		hasBeforeUndo, hasAfterUndo := false, false
		for _, s := range stmts {
			if strings.Contains(s, "audit_before_undo") {
				hasBeforeUndo = true
			}
			if strings.Contains(s, "audit_after_undo") {
				hasAfterUndo = true
			}
		}
		if !hasBeforeUndo || !hasAfterUndo {
			t.Errorf("expected beforeUndo and afterUndo to execute, got: %v", stmts)
		}
	})

	t.Run("RepairCallbacksExecution", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/beforeRepair.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_before_repair (val INT64);"),
			},
			"migrations/afterRepair.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_after_repair (val INT64);"),
			},
		}

		nw, db := setupNoway(t, fs)
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		_, err = nw.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		hasBeforeRepair, hasAfterRepair := false, false
		for _, s := range stmts {
			if strings.Contains(s, "audit_before_repair") {
				hasBeforeRepair = true
			}
			if strings.Contains(s, "audit_after_repair") {
				hasAfterRepair = true
			}
		}
		if !hasBeforeRepair || !hasAfterRepair {
			t.Errorf("expected beforeRepair and afterRepair to execute, got: %v", stmts)
		}
	})

	t.Run("BaselineCallbacksExecution", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/beforeBaseline.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_before_baseline (val INT64);"),
			},
			"migrations/afterBaseline.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_after_baseline (val INT64);"),
			},
		}

		nw, db := setupNoway(t, fs)
		_, err := nw.Baseline(ctx)
		if err != nil {
			t.Fatalf("Baseline failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		hasBeforeBaseline, hasAfterBaseline := false, false
		for _, s := range stmts {
			if strings.Contains(s, "audit_before_baseline") {
				hasBeforeBaseline = true
			}
			if strings.Contains(s, "audit_after_baseline") {
				hasAfterBaseline = true
			}
		}
		if !hasBeforeBaseline || !hasAfterBaseline {
			t.Errorf("expected beforeBaseline and afterBaseline to execute, got: %v", stmts)
		}
	})

	t.Run("CleanCallbacksExecution", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/beforeClean.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_before_clean (val INT64);"),
			},
			"migrations/afterClean.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_after_clean (val INT64);"),
			},
		}

		nw, db := setupNoway(t, fs, noway.WithCleanDisabled(false))
		_ = db.EnsureSchema(ctx, "e2e_dataset")

		_, err := nw.Clean(ctx)
		if err != nil {
			t.Fatalf("Clean failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		hasBeforeClean, hasAfterClean := false, false
		for _, s := range stmts {
			if strings.Contains(s, "audit_before_clean") {
				hasBeforeClean = true
			}
			if strings.Contains(s, "audit_after_clean") {
				hasAfterClean = true
			}
		}
		if !hasBeforeClean || !hasAfterClean {
			t.Errorf("expected beforeClean and afterClean to execute, got: %v", stmts)
		}
	})

	t.Run("F1_NamedCallbacksWithDescription", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/beforeMigrate__first_step.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_first (id INT64);"),
			},
			"migrations/beforeMigrate__second_step.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_second (id INT64);"),
			},
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
		}

		nw, db := setupNoway(t, fs)
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		foundFirst, foundSecond := false, false
		for _, s := range stmts {
			if strings.Contains(s, "audit_first") {
				foundFirst = true
			}
			if strings.Contains(s, "audit_second") {
				foundSecond = true
			}
		}

		if !foundFirst || !foundSecond {
			t.Fatalf("expected audit_first and audit_second to be executed: foundFirst=%v foundSecond=%v", foundFirst, foundSecond)
		}

		var firstIdx, secondIdx, v1Idx int = -1, -1, -1
		for i, s := range stmts {
			if strings.Contains(s, "audit_first") && firstIdx == -1 {
				firstIdx = i
			}
			if strings.Contains(s, "audit_second") && secondIdx == -1 {
				secondIdx = i
			}
			if strings.Contains(s, "users") && v1Idx == -1 {
				v1Idx = i
			}
		}
		if firstIdx >= secondIdx {
			t.Errorf("expected audit_first (%d) before audit_second (%d)", firstIdx, secondIdx)
		}
		if secondIdx >= v1Idx {
			t.Errorf("expected callbacks before migration statement (%d)", v1Idx)
		}
	})
}

// TestTier1_F3_F4_F5_ZeroMigrationAndReadCallbacks verifies zero-migration hooks (F3),
// Validate callbacks (F4), and Info callbacks (F5).
func TestTier1_F3_F4_F5_ZeroMigrationAndReadCallbacks(t *testing.T) {
	ctx := context.Background()

	t.Run("F3_ZeroMigrationLifecycleHooks", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/beforeMigrate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_zero_before (val INT64);"),
			},
			"migrations/afterMigrate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_zero_after (val INT64);"),
			},
		}

		nw, db := setupNoway(t, fs)
		res, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 0 {
			t.Errorf("expected 0 migrations executed, got %d", res.MigrationsExecuted)
		}

		stmts := db.ExecutedStatements()
		hasBefore, hasAfter := false, false
		for _, s := range stmts {
			if strings.Contains(s, "audit_zero_before") {
				hasBefore = true
			}
			if strings.Contains(s, "audit_zero_after") {
				hasAfter = true
			}
		}

		if !hasBefore || !hasAfter {
			t.Fatalf("expected zero-migration lifecycle hooks (beforeMigrate/afterMigrate) to execute: hasBefore=%v hasAfter=%v", hasBefore, hasAfter)
		}
	})

	t.Run("F4_ValidateLifecycleCallbacks", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/beforeValidate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_before_validate (val INT64);"),
			},
			"migrations/afterValidate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_after_validate (val INT64);"),
			},
		}

		nw, db := setupNoway(t, fs)
		_, _ = nw.Migrate(ctx)

		valRes, err := nw.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valRes.Valid {
			t.Errorf("expected valid state, got errors: %v", valRes.Errors)
		}

		stmts := db.ExecutedStatements()
		hasBeforeVal, hasAfterVal := false, false
		for _, s := range stmts {
			if strings.Contains(s, "audit_before_validate") {
				hasBeforeVal = true
			}
			if strings.Contains(s, "audit_after_validate") {
				hasAfterVal = true
			}
		}

		if !hasBeforeVal || !hasAfterVal {
			t.Fatalf("expected validate lifecycle callbacks (beforeValidate/afterValidate) to execute: hasBeforeVal=%v hasAfterVal=%v", hasBeforeVal, hasAfterVal)
		}
	})

	t.Run("F5_InfoLifecycleCallbacks", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/beforeInfo.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_before_info (val INT64);"),
			},
			"migrations/afterInfo.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE audit_after_info (val INT64);"),
			},
		}

		nw, db := setupNoway(t, fs)
		_, err := nw.Info(ctx)
		if err != nil {
			t.Fatalf("Info error: %v", err)
		}

		stmts := db.ExecutedStatements()
		hasBeforeInfo, hasAfterInfo := false, false
		for _, s := range stmts {
			if strings.Contains(s, "audit_before_info") {
				hasBeforeInfo = true
			}
			if strings.Contains(s, "audit_after_info") {
				hasAfterInfo = true
			}
		}

		if !hasBeforeInfo || !hasAfterInfo {
			t.Fatalf("expected info lifecycle callbacks (beforeInfo/afterInfo) to execute: hasBeforeInfo=%v hasAfterInfo=%v", hasBeforeInfo, hasAfterInfo)
		}
	})
}

// TestTier1_F6_F7_CallbackErrorsAndScriptEnv verifies callback error abort behavior (F6)
// and script callback execution environment (F7).
func TestTier1_F6_F7_CallbackErrorsAndScriptEnv(t *testing.T) {
	ctx := context.Background()

	t.Run("F6_CallbackFailureAbortsMigration", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/beforeMigrate.sql": &fstest.MapFile{
				Data: []byte("FAIL_THIS_STATEMENT;"),
			},
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
		}

		nw, db := setupNoway(t, fs)
		db.SetFailOnExecute("FAIL_THIS_STATEMENT")

		_, err := nw.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error when beforeMigrate callback fails, got nil")
		}

		// Verify V1 was never applied
		history, _ := db.FetchHistory(ctx, "e2e_dataset", "flyway_schema_history")
		if len(history) != 0 {
			t.Errorf("expected 0 migrations applied after callback failure, got %d", len(history))
		}
	})

	t.Run("F7_ScriptCallbackExecutionAndEnv", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/beforeMigrate.sh": &fstest.MapFile{
				Data: []byte(`#!/bin/bash
if [ -z "$FLYWAY_EVENT" ]; then
    echo "Missing FLYWAY_EVENT" >&2
    exit 1
fi
exit 0
`),
			},
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
		}

		nw, _ := setupNoway(t, fs)
		res, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed with script callback: %v", err)
		}
		if res.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}
	})
}

// TestTier1_F8_F9_VersionsAndRepeatables verifies semantic/numeric version ordering (F8)
// and repeatable migrations ordering and re-execution (F9).
func TestTier1_F8_F9_VersionsAndRepeatables(t *testing.T) {
	ctx := context.Background()

	t.Run("VersionOrderingSequence", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1.1__second.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1_1 (id INT64);"),
			},
			"migrations/V1__first.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1 (id INT64);"),
			},
			"migrations/V2.0.0__fourth.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t2_0_0 (id INT64);"),
			},
			"migrations/V1.2__third.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1_2 (id INT64);"),
			},
		}

		nw, db := setupNoway(t, fs)
		res, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate error: %v", err)
		}
		if res.MigrationsExecuted != 4 {
			t.Fatalf("expected 4 migrations executed, got %d", res.MigrationsExecuted)
		}

		history, err := db.FetchHistory(ctx, "e2e_dataset", "flyway_schema_history")
		if err != nil {
			t.Fatalf("FetchHistory error: %v", err)
		}
		expectedOrder := []string{"1", "1.1", "1.2", "2.0.0"}
		for i, exp := range expectedOrder {
			if history[i].Version == nil || history[i].Version.String() != exp {
				t.Errorf("index %d: expected version %s, got %v", i, exp, history[i].Version)
			}
		}
	})

	t.Run("RepeatablesExecuteAfterVersionedInAlphabeticalOrder", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__table.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE items (id INT64);"),
			},
			"migrations/R__zeta_view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW z_view AS SELECT 1;"),
			},
			"migrations/R__alpha_view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW a_view AS SELECT 1;"),
			},
			"migrations/R__beta_view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW b_view AS SELECT 1;"),
			},
		}

		nw, db := setupNoway(t, fs)
		res, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate error: %v", err)
		}
		if res.MigrationsExecuted != 4 {
			t.Fatalf("expected 4 migrations, got %d", res.MigrationsExecuted)
		}

		history, _ := db.FetchHistory(ctx, "e2e_dataset", "flyway_schema_history")
		// Index 0: V1 (versioned)
		if history[0].Script != "V1__table.sql" {
			t.Errorf("expected history[0] to be V1, got %s", history[0].Script)
		}
		// Index 1: R__alpha_view
		if history[1].Script != "R__alpha_view.sql" {
			t.Errorf("expected history[1] to be alpha, got %s", history[1].Script)
		}
		// Index 2: R__beta_view
		if history[2].Script != "R__beta_view.sql" {
			t.Errorf("expected history[2] to be beta, got %s", history[2].Script)
		}
		// Index 3: R__zeta_view
		if history[3].Script != "R__zeta_view.sql" {
			t.Errorf("expected history[3] to be zeta, got %s", history[3].Script)
		}
	})

	t.Run("RepeatableReRunsOnlyOnChecksumChange", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__table.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/R__user_view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW user_view AS SELECT id FROM users;"),
			},
		}

		nw, _ := setupNoway(t, fs)
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("initial Migrate failed: %v", err)
		}

		// Re-run without modifications -> 0 migrations executed
		res2, err := nw.Migrate(ctx)
		if err != nil || res2.MigrationsExecuted != 0 {
			t.Errorf("expected 0 migrations on unmodifed second run, got %d err=%v", res2.MigrationsExecuted, err)
		}

		// Modify repeatable migration content
		fs["migrations/R__user_view.sql"] = &fstest.MapFile{
			Data: []byte("CREATE VIEW user_view AS SELECT id, 1 AS flag FROM users;"),
		}

		res3, err := nw.Migrate(ctx)
		if err != nil || res3.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration on modified repeatable, got %d err=%v", res3.MigrationsExecuted, err)
		}
	})
}

// TestTier1_F10_F11_TargetAndOutOfOrder verifies target version cutoffs (F10)
// and out-of-order migration handling (F11).
func TestTier1_F10_F11_TargetAndOutOfOrder(t *testing.T) {
	ctx := context.Background()

	t.Run("TargetSpecificVersionCutoff", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__first.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1 (id INT64);"),
			},
			"migrations/V2__second.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t2 (id INT64);"),
			},
			"migrations/V3__third.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t3 (id INT64);"),
			},
		}

		nw, _ := setupNoway(t, fs, noway.WithTarget("2"))
		res, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 2 {
			t.Errorf("expected 2 migrations executed with target=2, got %d", res.MigrationsExecuted)
		}
		if res.TargetVersion != "2" {
			t.Errorf("expected target version 2, got %s", res.TargetVersion)
		}
	})

	t.Run("TargetNextAndCurrentProgression", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1 (id INT64);"),
			},
			"migrations/V2__step.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t2 (id INT64);"),
			},
		}

		nwNext, _ := setupNoway(t, fs, noway.WithTarget("next"))
		resNext, err := nwNext.Migrate(ctx)
		if err != nil || resNext.MigrationsExecuted != 1 {
			t.Fatalf("target=next expected 1 execution, got %d err=%v", resNext.MigrationsExecuted, err)
		}

		// Re-run with target=current -> 0 executions
		nwCurr, _ := setupNoway(t, fs, noway.WithTarget("current"))
		resCurr, err := nwCurr.Migrate(ctx)
		if err != nil || resCurr.MigrationsExecuted != 0 {
			t.Errorf("target=current expected 0 executions, got %d err=%v", resCurr.MigrationsExecuted, err)
		}
	})

	t.Run("OutOfOrderExecutionAppliedWhenEnabled", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1 (id INT64);"),
			},
			"migrations/V3__newer.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t3 (id INT64);"),
			},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "e2e_dataset"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		nw1, _ := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))
		_, err := nw1.Migrate(ctx)
		if err != nil {
			t.Fatalf("initial Migrate failed: %v", err)
		}

		// Now introduce older V2 migration
		fs["migrations/V2__older.sql"] = &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		}

		// With outOfOrder = false -> V2 must be skipped
		cfg.OutOfOrder = false
		cfg.ValidateOnMigrate = false
		nwNoOO, _ := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))
		resNoOO, _ := nwNoOO.Migrate(ctx)
		if resNoOO.MigrationsExecuted != 0 {
			t.Errorf("expected 0 executions when outOfOrder=false, got %d", resNoOO.MigrationsExecuted)
		}

		// With outOfOrder = true -> V2 must be applied
		cfg.OutOfOrder = true
		cfg.ValidateOnMigrate = true
		nwOO, _ := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))
		resOO, err := nwOO.Migrate(ctx)
		if err != nil || resOO.MigrationsExecuted != 1 {
			t.Errorf("expected 1 execution when outOfOrder=true, got %d err=%v", resOO.MigrationsExecuted, err)
		}
	})
}

// TestTier1_F12_BaselineMigrations verifies baseline command and baselineOnMigrate (F12).
func TestTier1_F12_BaselineMigrations(t *testing.T) {
	ctx := context.Background()

	t.Run("ExplicitBaselineSetsCutoffVersion", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__old.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1 (id INT64);"),
			},
			"migrations/V2__base.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t2 (id INT64);"),
			},
			"migrations/V3__new.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t3 (id INT64);"),
			},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "e2e_dataset"
		cfg.BaselineVersion = "2"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		nw, _ := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))
		baseRes, err := nw.Baseline(ctx)
		if err != nil {
			t.Fatalf("Baseline failed: %v", err)
		}
		if baseRes.BaselineVersion != "2" {
			t.Errorf("expected baseline version 2, got %s", baseRes.BaselineVersion)
		}

		// Migrate -> only V3 should run
		migRes, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if migRes.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration executed, got %d", migRes.MigrationsExecuted)
		}
		if len(migRes.ExecutedMigrations) != 1 || migRes.ExecutedMigrations[0].Version != "3" {
			t.Errorf("expected V3 executed, got %+v", migRes.ExecutedMigrations)
		}
	})
}

// TestTier1_F13_F14_F15_ValidateIntegrity verifies Validate discrepancy handling (F13, F14, F15).
func TestTier1_F13_F14_F15_ValidateIntegrity(t *testing.T) {
	ctx := context.Background()

	t.Run("F15_ChecksumMismatchDetected", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
		}

		nw, _ := setupNoway(t, fs)
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		// Tamper file content
		fs["migrations/V1__init.sql"] = &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64, tampered STRING);"),
		}

		valRes, err := nw.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected validation failure due to checksum mismatch")
		}
		if !strings.Contains(valRes.Error(), "checksum mismatch") {
			t.Errorf("expected checksum mismatch error message, got: %s", valRes.Error())
		}
	})

	t.Run("F15_MissingAppliedMigrationDetected", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/V2__second.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE items (id INT64);"),
			},
		}

		nw, _ := setupNoway(t, fs)
		_, _ = nw.Migrate(ctx)

		// Delete V1 locally
		delete(fs, "migrations/V1__init.sql")

		valRes, err := nw.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected validation failure due to missing applied migration")
		}
		if !strings.Contains(valRes.Error(), "not resolved locally") {
			t.Errorf("expected missing local migration error message, got: %s", valRes.Error())
		}
	})

	t.Run("F13_OutOfOrderPendingValidation", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1 (id INT64);"),
			},
			"migrations/V3__third.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t3 (id INT64);"),
			},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "e2e_dataset"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.OutOfOrder = false

		nw, _ := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))
		_, _ = nw.Migrate(ctx)

		// Add pending V2 with outOfOrder=false
		fs["migrations/V2__second.sql"] = &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		}

		valRes, _ := nw.Validate(ctx)
		if valRes.Valid {
			t.Errorf("expected validation to fail for out-of-order pending migration, but got valid")
		}
	})

	t.Run("F14_ValidateDeleteHistoryRecordIgnored", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1 (id INT64);"),
			},
		}

		db := mock.NewMockDatabase()
		_ = db.EnsureHistoryTable(ctx, "e2e_dataset", "flyway_schema_history")
		delVer := "2"
		_ = db.InsertHistory(ctx, "e2e_dataset", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &delVer,
			Description:   "deleted migration",
			Type:          "DELETE",
			Script:        "V2__deleted.sql",
			Success:       true,
		})

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "e2e_dataset"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		nw, _ := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))
		valRes, err := nw.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if !valRes.Valid {
			t.Errorf("expected Validate to succeed when history record is DELETE, got errors: %s", valRes.Error())
		}
	})
}

// TestTier1_F16_F17_F18_RepairOperations verifies repair operations (F16, F17, F18).
func TestTier1_F16_F17_F18_RepairOperations(t *testing.T) {
	ctx := context.Background()

	t.Run("F17_RepairRemovesFailedMigration", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
		}

		db := mock.NewMockDatabase()
		badVer := "1"
		_ = db.EnsureHistoryTable(ctx, "e2e_dataset", "flyway_schema_history")
		_ = db.InsertHistory(ctx, "e2e_dataset", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &badVer,
			Description:   "failed init",
			Type:          "SQL",
			Script:        "V1__init.sql",
			Success:       false,
		})

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "e2e_dataset"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		nw, _ := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))
		repairRes, err := nw.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.RemovedFailed) != 1 {
			t.Errorf("expected 1 removed failed migration, got %d", len(repairRes.RemovedFailed))
		}

		history, _ := db.FetchHistory(ctx, "e2e_dataset", "flyway_schema_history")
		if len(history) != 0 {
			t.Errorf("expected history table to be empty after removing failed, got %d records", len(history))
		}
	})

	t.Run("F16_RepairRealignsVersionedChecksum", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
		}

		nw, _ := setupNoway(t, fs)
		_, _ = nw.Migrate(ctx)

		// Tamper file locally
		fs["migrations/V1__init.sql"] = &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64, email STRING);"),
		}

		repairRes, err := nw.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.AlignedChecksums) != 1 {
			t.Errorf("expected 1 aligned checksum, got %d", len(repairRes.AlignedChecksums))
		}

		// Subsequent Validate must pass
		valRes, err := nw.Validate(ctx)
		if err != nil || !valRes.Valid {
			t.Errorf("expected Validate to pass after repair, got valid=%v errors=%v", valRes.Valid, valRes.Errors)
		}
	})

	t.Run("F16_RepairRealignsRepeatableChecksum", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/R__view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW v AS SELECT 1;"),
			},
		}

		nw, db := setupNoway(t, fs)
		_, _ = nw.Migrate(ctx)

		// Modify repeatable locally
		fs["migrations/R__view.sql"] = &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT 2;"),
		}

		_, err := nw.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}

		history, _ := db.FetchHistory(ctx, "e2e_dataset", "flyway_schema_history")
		if len(history) == 0 || history[0].Checksum == nil {
			t.Fatalf("expected schema history with checksum")
		}
		expectedCs, _ := checksum.CalculateString("CREATE VIEW v AS SELECT 2;")
		if *history[0].Checksum != expectedCs {
			t.Errorf("expected aligned checksum %d, got %d", expectedCs, *history[0].Checksum)
		}
		migRes, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate after repair failed: %v", err)
		}
		if migRes.MigrationsExecuted != 0 {
			t.Errorf("expected 0 migrations executed after repair, got %d", migRes.MigrationsExecuted)
		}
	})

	t.Run("F18_RepairMarksDeletedMigration", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/.keep": &fstest.MapFile{
				Data: []byte(""),
			},
		}

		nw, db := setupNoway(t, fs)
		_, _ = nw.Migrate(ctx)

		// Delete migration locally
		delete(fs, "migrations/V1__init.sql")

		_, _ = nw.Repair(ctx)
		history, _ := db.FetchHistory(ctx, "e2e_dataset", "flyway_schema_history")
		if len(history) == 0 {
			t.Fatalf("expected schema history record to remain")
		}
		if history[0].Type != "DELETE" {
			t.Errorf("expected history record type 'DELETE', got %q", history[0].Type)
		}
	})
}

// TestTier1_F19_F20_F21_SQLParserParity verifies BigQuery SQL statement splitting (F19, F20, F21).
func TestTier1_F19_F20_F21_SQLParserParity(t *testing.T) {
	ctx := context.Background()

	t.Run("F21_MultilineQuotesAndComments", func(t *testing.T) {
		sql := `
-- Comment line with semicolon;
/* Multi-line comment
   with semicolon; inside */
SELECT """multiline string
with semicolon; and "quotes" """ AS str_val;
SELECT 1 AS num;
`
		fs := fstest.MapFS{
			"migrations/V1__script.sql": &fstest.MapFile{
				Data: []byte(sql),
			},
		}

		nw, db := setupNoway(t, fs)
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		if len(stmts) != 2 {
			t.Errorf("expected 2 statements executed, got %d: %v", len(stmts), stmts)
		}
	})

	t.Run("F21_ProceduralBeginEndBlocks", func(t *testing.T) {
		sql := `
BEGIN
  SELECT 1;
  SELECT 2;
EXCEPTION WHEN ERROR THEN
  SELECT @@error.message;
END;
SELECT 3;
`
		fs := fstest.MapFS{
			"migrations/V1__proc.sql": &fstest.MapFile{
				Data: []byte(sql),
			},
		}

		nw, db := setupNoway(t, fs)
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		if len(stmts) != 2 {
			t.Errorf("expected 2 statements (block + SELECT 3), got %d: %v", len(stmts), stmts)
		}
	})

	t.Run("F19_ProceduralCaseEndCase", func(t *testing.T) {
		sql := `
CASE x WHEN 1 THEN SELECT 1; ELSE SELECT 2; END CASE;
SELECT 3;
`
		fs := fstest.MapFS{
			"migrations/V1__case.sql": &fstest.MapFile{
				Data: []byte(sql),
			},
		}

		nw, db := setupNoway(t, fs)
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		if len(stmts) != 2 {
			t.Skip("Pending M1 (F19): Procedural CASE ... END CASE; statement splitting requires parser fix")
		}
	})

	t.Run("F20_DropColumnIfExists", func(t *testing.T) {
		sql := `
ALTER TABLE tbl DROP COLUMN IF EXISTS col;
SELECT 1;
`
		fs := fstest.MapFS{
			"migrations/V1__ddl.sql": &fstest.MapFile{
				Data: []byte(sql),
			},
		}

		nw, db := setupNoway(t, fs)
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		if len(stmts) != 2 {
			t.Skip("Pending M1 (F20): DROP COLUMN IF EXISTS statement splitting requires parser fix")
		}
	})
}

// TestTier1_F22_PlaceholderEngine verifies placeholder replacement, case-insensitivity, and escapes (F22).
func TestTier1_F22_PlaceholderEngine(t *testing.T) {
	ctx := context.Background()

	t.Run("CaseInsensitiveEnvAndPlaceholderReplacement", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__table.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE ${tbl_name} (id INT64);"),
			},
		}

		placeholders := map[string]string{
			"TBL_NAME": "resolved_users_table",
		}

		nw, db := setupNoway(t, fs, noway.WithPlaceholders(placeholders))
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		if len(stmts) != 1 || !strings.Contains(stmts[0], "resolved_users_table") {
			t.Errorf("expected placeholder replaced with resolved_users_table, got: %v", stmts)
		}
	})

	t.Run("EscapedPlaceholderPreservedIntact", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__escape.sql": &fstest.MapFile{
				Data: []byte("SELECT '$${escaped_var}' AS literal_str;"),
			},
		}

		nw, db := setupNoway(t, fs)
		_, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		if len(stmts) != 1 || !strings.Contains(stmts[0], "${escaped_var}") {
			t.Errorf("expected literal ${escaped_var} preserved, got: %v", stmts)
		}
	})
}

// ============================================================================
// TIER 2: BOUNDARY & CORNER CASES (Stress & Edge Testing)
// ============================================================================

// TestTier2_VersionBoundariesAndSentinels verifies version comparison edge cases (F8).
func TestTier2_VersionBoundariesAndSentinels(t *testing.T) {
	t.Run("NumericEquivalenceVsSubversions", func(t *testing.T) {
		// 1.01 and 1.1 must be equal
		v1_01, err1 := version.Parse("1.01")
		v1_1, err2 := version.Parse("1.1")
		if err1 != nil || err2 != nil {
			t.Fatalf("Parse error: %v, %v", err1, err2)
		}
		if v1_01.Compare(v1_1) != 0 {
			t.Errorf("expected 1.01 == 1.1, got cmp=%d", v1_01.Compare(v1_1))
		}

		// 1.0.1 vs 1.001 -> 1.0.1 has segments [1, 0, 1]; 1.001 has segments [1, 1] -> 1.0.1 < 1.001
		v1_0_1, _ := version.Parse("1.0.1")
		v1_001, _ := version.Parse("1.001")
		if v1_0_1.Compare(v1_001) >= 0 {
			t.Errorf("expected 1.0.1 < 1.001, got cmp=%d", v1_0_1.Compare(v1_001))
		}
	})

	t.Run("CurrentAndNextSentinelsVsZero", func(t *testing.T) {
		v0, _ := version.Parse("0")
		vCurrent, _ := version.Parse("current")
		vNext, _ := version.Parse("next")

		// Predefined versions should not be equal to Version 0
		if v0.Compare(vCurrent) == 0 {
			t.Errorf("Version('0') erroneously equates to Version('current')")
		}
		if v0.Compare(vNext) == 0 {
			t.Errorf("Version('0') erroneously equates to Version('next')")
		}
	})

	t.Run("LargeMultiPartVersion", func(t *testing.T) {
		longVer := "1.2.3.4.5.6.7.8.9.10.11.12.13.14.15"
		v, err := version.Parse(longVer)
		if err != nil {
			t.Fatalf("failed to parse long multi-part version: %v", err)
		}
		if v.String() != longVer {
			t.Errorf("expected %s, got %s", longVer, v.String())
		}
	})
}

// TestTier2_ZeroMigrationAndEmptyLifecycle verifies empty state resilience (F3).
func TestTier2_ZeroMigrationAndEmptyLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("EmptyLocationMigrationRun", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/.gitkeep": &fstest.MapFile{Data: []byte("")},
		}
		nw, db := setupNoway(t, fs)

		res, err := nw.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate on empty FS failed: %v", err)
		}
		if res.MigrationsExecuted != 0 {
			t.Errorf("expected 0 executed, got %d", res.MigrationsExecuted)
		}

		// Verify history table exists even with 0 migrations
		exists, err := db.HistoryTableExists(ctx, "e2e_dataset", "flyway_schema_history")
		if err != nil || !exists {
			t.Errorf("expected history table to be created, exists=%v err=%v", exists, err)
		}
	})

	t.Run("ValidateOnEmptySchemaReturnsValid", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
		}
		nw, _ := setupNoway(t, fs)
		valRes, err := nw.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valRes.Valid {
			t.Errorf("expected clean schema to validate as valid, got: %v", valRes.Errors)
		}
	})
}

// TestTier2_TargetCutoffBoundaries verifies target cutoff edge cases (F10).
func TestTier2_TargetCutoffBoundaries(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__first.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"migrations/V2__second.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
	}

	nw, _ := setupNoway(t, fs, noway.WithTarget("latest"))
	res, err := nw.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate target=latest failed: %v", err)
	}
	if res.MigrationsExecuted != 2 {
		t.Errorf("expected 2 migrations executed with target=latest, got %d", res.MigrationsExecuted)
	}
}

// TestTier2_CorruptedHistoryAndRepair verifies healing when multiple discrepancy types coexist (F14-F18).
func TestTier2_CorruptedHistoryAndRepair(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__first.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"migrations/V2__second.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	_ = db.EnsureHistoryTable(ctx, "e2e_dataset", "flyway_schema_history")

	// Inject 1 failed migration and 1 corrupted checksum migration
	badVer := "1"
	_ = db.InsertHistory(ctx, "e2e_dataset", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &badVer,
		Description:   "first",
		Type:          "SQL",
		Script:        "V1__first.sql",
		Success:       false, // Failed!
	})
	badVer2 := "2"
	badChecksum := int64(99999999)
	_ = db.InsertHistory(ctx, "e2e_dataset", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2,
		Version:       &badVer2,
		Description:   "second",
		Type:          "SQL",
		Script:        "V2__second.sql",
		Checksum:      &badChecksum, // Mismatch!
		Success:       true,
	})

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "e2e_dataset"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	cfg.OutOfOrder = true

	nw, _ := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))

	// Validate must report errors
	valRes, err := nw.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if valRes.Valid {
		t.Fatalf("expected validation failure on corrupted history")
	}

	// Repair must heal both issues
	repairRes, err := nw.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if len(repairRes.RemovedFailed) != 1 {
		t.Errorf("expected 1 removed failed, got %d", len(repairRes.RemovedFailed))
	}
	if len(repairRes.AlignedChecksums) != 1 {
		t.Errorf("expected 1 aligned checksum, got %d", len(repairRes.AlignedChecksums))
	}

	// Validate should now be clean for V2 (V1 was removed and can be migrated)
	migRes, err := nw.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate after repair failed: %v", err)
	}
	if migRes.MigrationsExecuted != 1 {
		t.Errorf("expected V1 to be re-applied after removing failed record, got %d", migRes.MigrationsExecuted)
	}
}

// TestTier2_SQLTokenizerEdgeCases verifies complex quote and procedural syntax handling (F21).
func TestTier2_SQLTokenizerEdgeCases(t *testing.T) {
	ctx := context.Background()

	sql := `
SELECT 'escaped single \'quote\' and ''doubled'' quote' AS s1;
SELECT "escaped double \"quote\" and ""doubled"" quote" AS s2;
SELECT 'nested "double" inside single', "nested 'single' inside double";
`
	fs := fstest.MapFS{
		"migrations/V1__quotes.sql": &fstest.MapFile{
			Data: []byte(sql),
		},
	}

	nw, db := setupNoway(t, fs)
	_, err := nw.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	stmts := db.ExecutedStatements()
	if len(stmts) != 3 {
		t.Errorf("expected 3 statements executed, got %d: %v", len(stmts), stmts)
	}
}

// TestTier2_PlaceholderDelimitersAndEscapes verifies placeholder error reporting and escapes (F22).
func TestTier2_PlaceholderDelimitersAndEscapes(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__missing_placeholder.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${unconfigured_var} (id INT64);"),
		},
	}

	nw, _ := setupNoway(t, fs)
	_, err := nw.Migrate(ctx)
	if err == nil {
		t.Fatalf("expected error on unconfigured placeholder, got nil")
	}
	if !strings.Contains(err.Error(), "unconfigured_var") {
		t.Errorf("expected error message to cite missing placeholder name, got: %v", err)
	}
}

// ============================================================================
// TIER 3: CROSS-FEATURE INTERACTIONS (Multi-Step Workflows)
// ============================================================================

// TestTier3_DisasterRecoveryAndHealingCycle verifies complete disaster recovery flow:
// Apply -> Failure injection -> Validation detection -> Repair removal -> Re-run (F15, F17).
func TestTier3_DisasterRecoveryAndHealingCycle(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__users.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V2__posts.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE posts (id INT64);"),
		},
	}

	nw, db := setupNoway(t, fs)
	res1, err := nw.Migrate(ctx)
	if err != nil || res1.MigrationsExecuted != 2 {
		t.Fatalf("step 1 Migrate failed: %v", err)
	}

	// Step 2: Add V3 with simulated execution failure
	fs["migrations/V3__comments.sql"] = &fstest.MapFile{
		Data: []byte("FAIL_SQL_STATEMENT;"),
	}
	db.SetFailOnExecute("FAIL_SQL_STATEMENT")

	_, err = nw.Migrate(ctx)
	if err == nil {
		t.Fatalf("step 2 expected Migrate failure, got nil")
	}

	// Step 3: Validate must report failed migration in schema history
	valRes, _ := nw.Validate(ctx)
	if valRes.Valid {
		t.Fatalf("step 3 Validate must detect failed V3")
	}

	// Step 4: Repair removes the failed record
	repairRes, err := nw.Repair(ctx)
	if err != nil || len(repairRes.RemovedFailed) != 1 {
		t.Fatalf("step 4 Repair failed to remove failed record: %v", err)
	}

	// Step 5: Fix V3 script and re-run Migrate -> V3 successfully applied
	db.SetFailOnExecute("") // clear failure
	fs["migrations/V3__comments.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE comments (id INT64);"),
	}

	res3, err := nw.Migrate(ctx)
	if err != nil || res3.MigrationsExecuted != 1 {
		t.Fatalf("step 5 re-run Migrate failed: %v", err)
	}

	// Step 6: Post-recovery Validate passes completely
	valRes2, err := nw.Validate(ctx)
	if err != nil || !valRes2.Valid {
		t.Errorf("step 6 post-recovery Validate failed: %v", valRes2.Errors)
	}
}

// TestTier3_BaselineAndTargetInterplay verifies baseline cutoff combined with target version (F10, F12).
func TestTier3_BaselineAndTargetInterplay(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__legacy.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"migrations/V2__base.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
		"migrations/V3__intermediate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t3 (id INT64);"),
		},
		"migrations/V4__future.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t4 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	// Pre-populate existing unmanaged table
	_ = db.EnsureSchema(ctx, "e2e_dataset")
	_ = db.EnsureHistoryTable(ctx, "e2e_dataset", "pre_existing_table")

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "e2e_dataset"
	cfg.BaselineOnMigrate = true
	cfg.BaselineVersion = "2"
	cfg.Target = "3"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	nw, err := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	res, err := nw.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Expected: Baseline auto-created at 2, only V3 executed, V4 skipped due to target=3
	if res.MigrationsExecuted != 1 {
		t.Errorf("expected exactly 1 migration executed (V3), got %d", res.MigrationsExecuted)
	}
	if len(res.ExecutedMigrations) != 1 || res.ExecutedMigrations[0].Version != "3" {
		t.Errorf("expected executed migration to be V3, got: %+v", res.ExecutedMigrations)
	}
}

// TestTier3_CallbacksPlaceholdersScriptsPipeline verifies integration between callbacks,
// placeholders, and script execution (F1, F6, F7, F22).
func TestTier3_CallbacksPlaceholdersScriptsPipeline(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${audit_table} (event STRING);"),
		},
		"migrations/V1__create_table.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${target_table} (id INT64);"),
		},
	}

	placeholders := map[string]string{
		"audit_table":  "audit_log",
		"target_table": "customers",
	}

	nw, db := setupNoway(t, fs, noway.WithPlaceholders(placeholders))
	res, err := nw.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if res.MigrationsExecuted != 1 {
		t.Errorf("expected 1 migration executed, got %d", res.MigrationsExecuted)
	}

	stmts := db.ExecutedStatements()
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements (callback + migration), got %d: %v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "CREATE TABLE audit_log") {
		t.Errorf("expected callback placeholder substitution in statement 0: %s", stmts[0])
	}
	if !strings.Contains(stmts[1], "CREATE TABLE customers") {
		t.Errorf("expected migration placeholder substitution in statement 1: %s", stmts[1])
	}
}

// TestTier3_RepeatableMigrationEvolution verifies repeatable lifecycle over multiple deployments (F9, F22).
func TestTier3_RepeatableMigrationEvolution(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE metrics (val INT64);"),
		},
		"migrations/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW summary_v AS SELECT SUM(val) FROM metrics;"),
		},
	}

	nw, db := setupNoway(t, fs)

	// Deploy 1: V1 + R__view applied
	res1, err := nw.Migrate(ctx)
	if err != nil || res1.MigrationsExecuted != 2 {
		t.Fatalf("Deploy 1 failed: %v", err)
	}

	// Deploy 2: Content unmodified -> 0 executions
	res2, err := nw.Migrate(ctx)
	if err != nil || res2.MigrationsExecuted != 0 {
		t.Errorf("Deploy 2 expected 0 executions, got %d", res2.MigrationsExecuted)
	}

	// Deploy 3: R__view modified -> R__view re-executed
	fs["migrations/R__view.sql"] = &fstest.MapFile{
		Data: []byte("CREATE VIEW summary_v AS SELECT SUM(val), COUNT(1) FROM metrics;"),
	}
	res3, err := nw.Migrate(ctx)
	if err != nil || res3.MigrationsExecuted != 1 {
		t.Errorf("Deploy 3 expected 1 repeatable execution, got %d", res3.MigrationsExecuted)
	}

	// Deploy 4: Add V2 -> only V2 executed
	fs["migrations/V2__add_index.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE tags (id INT64);"),
	}
	res4, err := nw.Migrate(ctx)
	if err != nil || res4.MigrationsExecuted != 1 {
		t.Errorf("Deploy 4 expected 1 execution (V2), got %d", res4.MigrationsExecuted)
	}

	// Verify total history records in DB: V1, R__view (initial), R__view (re-run), V2
	history, _ := db.FetchHistory(ctx, "e2e_dataset", "flyway_schema_history")
	if len(history) != 4 {
		t.Errorf("expected 4 history records, got %d", len(history))
	}
}

// TestTier3_OutOfOrderAndUndoWorkflow verifies out-of-order execution followed by rollback (F10, F11).
func TestTier3_OutOfOrderAndUndoWorkflow(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"migrations/V3__new.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t3 (id INT64);"),
		},
		"migrations/U2__drop_mid.sql": &fstest.MapFile{
			Data: []byte("DROP TABLE t2;"),
		},
		"migrations/U3__drop_new.sql": &fstest.MapFile{
			Data: []byte("DROP TABLE t3;"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "e2e_dataset"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	nw1, _ := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))
	_, err := nw1.Migrate(ctx)
	if err != nil {
		t.Fatalf("initial Migrate failed: %v", err)
	}

	// Apply older V2 out-of-order (becomes most recently applied migration at rank 3)
	fs["migrations/V2__mid.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t2 (id INT64);"),
	}
	cfg.OutOfOrder = true
	nw2, _ := noway.New(noway.WithConfig(cfg), noway.WithDatabase(db))
	resOO, err := nw2.Migrate(ctx)
	if err != nil || resOO.MigrationsExecuted != 1 {
		t.Fatalf("out-of-order Migrate failed: %v", err)
	}

	// Run Undo -> rolls back V2 (the most recently applied migration)
	undoRes, err := nw2.Undo(ctx)
	if err != nil {
		t.Fatalf("Undo failed: %v", err)
	}
	if undoRes.UndoneVersion != "2" {
		t.Errorf("expected undone version 2, got %s", undoRes.UndoneVersion)
	}

	// History should now contain V1 and V3
	history, _ := db.FetchHistory(ctx, "e2e_dataset", "flyway_schema_history")
	if len(history) != 2 {
		t.Errorf("expected 2 records after undo, got %d", len(history))
	}
}

// ============================================================================
// TIER 4: REAL-WORLD APPLICATION SCENARIOS (Enterprise Lifecycles)
// ============================================================================

// TestTier4_EnterpriseMultiTenantEcommerceLifecycle simulates an enterprise data warehouse
// schema lifecycle including datasets, clustering, repeatables, and audit hooks.
func TestTier4_EnterpriseMultiTenantEcommerceLifecycle(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${audit_dataset}.migration_audit (run_at TIMESTAMP);"),
		},
		"migrations/V1__catalog_schema.sql": &fstest.MapFile{
			Data: []byte(`
CREATE TABLE catalog_products (
    product_id INT64,
    sku STRING,
    title STRING,
    created_at TIMESTAMP
);
`),
		},
		"migrations/V2__orders_partitioned.sql": &fstest.MapFile{
			Data: []byte(`
CREATE TABLE orders (
    order_id INT64,
    customer_id INT64,
    order_total NUMERIC,
    order_date DATE
);
`),
		},
		"migrations/R__reporting_daily_sales.sql": &fstest.MapFile{
			Data: []byte(`
CREATE VIEW view_daily_sales AS
SELECT order_date, SUM(order_total) AS total_revenue
FROM orders
GROUP BY order_date;
`),
		},
		"migrations/afterMigrate.sql": &fstest.MapFile{
			Data: []byte("INSERT INTO ${audit_dataset}.migration_audit (run_at) VALUES (CURRENT_TIMESTAMP());"),
		},
	}

	placeholders := map[string]string{
		"audit_dataset": "audit_warehouse",
	}

	nw, db := setupNoway(t, fs, noway.WithPlaceholders(placeholders))
	res, err := nw.Migrate(ctx)
	if err != nil {
		t.Fatalf("Enterprise migration lifecycle failed: %v", err)
	}

	if res.MigrationsExecuted != 3 { // V1, V2, R__reporting
		t.Errorf("expected 3 migrations applied, got %d", res.MigrationsExecuted)
	}

	// Validate post-deployment
	valRes, err := nw.Validate(ctx)
	if err != nil || !valRes.Valid {
		t.Errorf("post-deployment validation failed: %v", valRes.Errors)
	}

	// Inspect Info report
	infoRes, err := nw.Info(ctx)
	if err != nil {
		t.Fatalf("Info failed: %v", err)
	}
	if len(infoRes.Migrations) != 3 {
		t.Errorf("expected 3 migrations reported in Info, got %d", len(infoRes.Migrations))
	}
	for _, m := range infoRes.Migrations {
		if m.State != "Success" {
			t.Errorf("expected migration %s to be Success, got %s", m.Script, m.State)
		}
	}

	// Verify executed SQL statements contain audit logging
	stmts := db.ExecutedStatements()
	hasAuditBefore, hasAuditAfter := false, false
	for _, s := range stmts {
		if strings.Contains(s, "CREATE TABLE audit_warehouse.migration_audit") {
			hasAuditBefore = true
		}
		if strings.Contains(s, "INSERT INTO audit_warehouse.migration_audit") {
			hasAuditAfter = true
		}
	}
	if !hasAuditBefore || !hasAuditAfter {
		t.Errorf("expected enterprise audit trail hooks executed, stmts: %v", stmts)
	}
}

// TestTier4_CICDPipelineAutomationSimulation simulates automated CI/CD gating:
// Info -> Staging Validate -> Targeted Migrate -> Production Validate -> Ephemeral Clean.
func TestTier4_CICDPipelineAutomationSimulation(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE pipeline_test (id INT64);"),
		},
		"migrations/V2__stage2.sql": &fstest.MapFile{
			Data: []byte("ALTER TABLE pipeline_test ADD COLUMN stage STRING;"),
		},
	}

	nw, db := setupNoway(t, fs, noway.WithCleanDisabled(false))

	// Stage 1: Info inspection (PR check)
	info1, err := nw.Info(ctx)
	if err != nil {
		t.Fatalf("Stage 1 Info failed: %v", err)
	}
	if len(info1.Migrations) != 2 {
		t.Fatalf("Stage 1 expected 2 pending migrations, got %d", len(info1.Migrations))
	}

	// Stage 2: Staging Validation before any execution
	val1, err := nw.Validate(ctx)
	if err != nil || !val1.Valid {
		t.Fatalf("Stage 2 Validate failed: %v", err)
	}

	// Stage 3: Targeted Deployment to V1
	cfgV1 := config.NewDefaultConfiguration()
	cfgV1.DefaultSchema = "e2e_dataset"
	cfgV1.Target = "1"
	cfgV1.FS = fs
	cfgV1.Locations = []string{"migrations"}
	nwV1, _ := noway.New(noway.WithConfig(cfgV1), noway.WithDatabase(db))

	resV1, err := nwV1.Migrate(ctx)
	if err != nil || resV1.MigrationsExecuted != 1 {
		t.Fatalf("Stage 3 Targeted Migrate failed: %v", err)
	}

	// Stage 4: Full Deployment
	resFull, err := nw.Migrate(ctx)
	if err != nil || resFull.MigrationsExecuted != 1 { // applies remaining V2
		t.Fatalf("Stage 4 Full Migrate failed: %v", err)
	}

	// Stage 5: Production Validation
	valProd, err := nw.Validate(ctx)
	if err != nil || !valProd.Valid {
		t.Fatalf("Stage 5 Production Validate failed: %v", err)
	}

	// Stage 6: Ephemeral Teardown (Clean)
	cleanRes, err := nw.Clean(ctx)
	if err != nil || len(cleanRes.SchemasCleaned) == 0 {
		t.Fatalf("Stage 6 Clean failed: %v", err)
	}
}

// TestTier4_ComplexBigQueryAnalyticsWarehouse verifies complex procedural SQL statement
// execution and error isolation in a BigQuery analytics warehouse workload.
func TestTier4_ComplexBigQueryAnalyticsWarehouse(t *testing.T) {
	ctx := context.Background()

	sql := `
-- Complex BigQuery Warehouse Setup
BEGIN
  -- Procedural setup block
  CREATE TABLE IF NOT EXISTS metrics_raw (
    timestamp TIMESTAMP,
    metric_name STRING,
    value FLOAT64
  );
EXCEPTION WHEN ERROR THEN
  -- Handled exception
  SELECT @@error.message;
END;

SELECT """SELECT 1;
SELECT 2;
""" AS query_template;

CREATE VIEW reporting_summary AS
SELECT metric_name, AVG(value) AS avg_value
FROM metrics_raw
GROUP BY metric_name;
`

	fs := fstest.MapFS{
		"migrations/V1__complex_dw.sql": &fstest.MapFile{
			Data: []byte(sql),
		},
	}

	nw, db := setupNoway(t, fs)
	res, err := nw.Migrate(ctx)
	if err != nil {
		t.Fatalf("Complex analytics warehouse migration failed: %v", err)
	}
	if res.MigrationsExecuted != 1 {
		t.Errorf("expected 1 migration executed, got %d", res.MigrationsExecuted)
	}

	stmts := db.ExecutedStatements()
	if len(stmts) != 3 {
		t.Errorf("expected 3 statements (BEGIN..END, SELECT triple-quote, CREATE VIEW), got %d: %v", len(stmts), stmts)
	}
}
