package migrator

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/roryq/noway/pkg/checksum"
	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/database/mock"
)

// -----------------------------------------------------------------------------
// FEATURE 9: REPEATABLE MIGRATIONS ADVERSARIAL SUITE
// -----------------------------------------------------------------------------

// TestChallengerRepeatableReexecutionOnChecksumChange tests that repeatable migrations
// re-run if and only if their checksum changes or prior run failed.
func TestChallengerRepeatableReexecutionOnChecksumChange(t *testing.T) {
	ctx := context.Background()

	t.Run("single repeatable re-execution on content update", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"sql/R__view.sql": &fstest.MapFile{Data: []byte("CREATE VIEW v1 AS SELECT 1;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS
		db := mock.NewMockDatabase()

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		// Initial migration run
		res1, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("run 1 failed: %v", err)
		}
		if res1.MigrationsExecuted != 1 {
			t.Fatalf("run 1: expected 1 migration executed, got %d", res1.MigrationsExecuted)
		}

		// Second migration run without modifying content -> 0 migrations executed
		res2, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("run 2 failed: %v", err)
		}
		if res2.MigrationsExecuted != 0 {
			t.Fatalf("run 2: expected 0 migrations executed, got %d", res2.MigrationsExecuted)
		}

		// Update content -> checksum changes
		mockFS["sql/R__view.sql"] = &fstest.MapFile{Data: []byte("CREATE VIEW v1 AS SELECT 2;")}

		// Third migration run -> repeatable executes again!
		res3, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("run 3 failed: %v", err)
		}
		if res3.MigrationsExecuted != 1 {
			t.Fatalf("run 3: expected 1 migration executed, got %d", res3.MigrationsExecuted)
		}
		if res3.ExecutedMigrations[0].Script != "R__view.sql" {
			t.Errorf("run 3: expected R__view.sql executed, got %s", res3.ExecutedMigrations[0].Script)
		}

		// Verify schema history has 2 records with increasing installed ranks
		history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if err != nil {
			t.Fatalf("FetchHistory failed: %v", err)
		}
		if len(history) != 2 {
			t.Fatalf("expected 2 history records, got %d", len(history))
		}
		if history[0].InstalledRank != 1 || history[1].InstalledRank != 2 {
			t.Errorf("expected ranks 1 and 2, got %d and %d", history[0].InstalledRank, history[1].InstalledRank)
		}
		if *history[0].Checksum == *history[1].Checksum {
			t.Errorf("expected checksums to differ between runs, got identical %d", *history[0].Checksum)
		}

		// Fourth run without changes -> 0 migrations executed
		res4, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("run 4 failed: %v", err)
		}
		if res4.MigrationsExecuted != 0 {
			t.Fatalf("run 4: expected 0 migrations executed, got %d", res4.MigrationsExecuted)
		}
	})

	t.Run("multiple repeatables: only modified repeatable re-runs", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"sql/R__a_view.sql": &fstest.MapFile{Data: []byte("SELECT 'a1';")},
			"sql/R__b_view.sql": &fstest.MapFile{Data: []byte("SELECT 'b1';")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS
		db := mock.NewMockDatabase()

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		// Initial run applies both
		res1, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("run 1 failed: %v", err)
		}
		if res1.MigrationsExecuted != 2 {
			t.Fatalf("run 1: expected 2 migrations, got %d", res1.MigrationsExecuted)
		}

		// Modify ONLY R__b_view.sql
		mockFS["sql/R__b_view.sql"] = &fstest.MapFile{Data: []byte("SELECT 'b2_modified';")}

		// Run 2: ONLY R__b_view.sql should execute
		res2, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("run 2 failed: %v", err)
		}
		if res2.MigrationsExecuted != 1 {
			t.Fatalf("run 2: expected 1 migration executed, got %d", res2.MigrationsExecuted)
		}
		if res2.ExecutedMigrations[0].Script != "R__b_view.sql" {
			t.Errorf("run 2: expected R__b_view.sql executed, got %s", res2.ExecutedMigrations[0].Script)
		}

		// History table should now have 3 records (a1, b1, b2)
		history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if len(history) != 3 {
			t.Fatalf("expected 3 history records, got %d", len(history))
		}
	})

	t.Run("repeatable re-executes if prior run failed even without checksum change", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"sql/R__flaky.sql": &fstest.MapFile{Data: []byte("FAIL_THIS_STATEMENT;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS
		db := mock.NewMockDatabase()
		db.SetFailOnExecute("FAIL_THIS_STATEMENT")

		m, _ := New(cfg, db)
		_, err := m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected migration to fail, got nil")
		}

		// History table should record failed migration
		history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if len(history) != 1 || history[0].Success != false {
			t.Fatalf("expected 1 failed history record, got %+v", history)
		}

		// Clear database error without modifying the migration file
		db.SetFailOnExecute("")
		cfg.ValidateOnMigrate = false
		mRetry, _ := New(cfg, db)

		// Migrate should re-attempt the failed repeatable
		res2, err := mRetry.Migrate(ctx)
		if err != nil {
			t.Fatalf("second Migrate run failed: %v", err)
		}
		if res2.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed on retry, got %d", res2.MigrationsExecuted)
		}
	})
}

// TestChallengerRepeatableExecutionOrdering_Adversarial tests that repeatables execute
// strictly by description order, and always after all pending versioned migrations.
func TestChallengerRepeatableExecutionOrdering_Adversarial(t *testing.T) {
	ctx := context.Background()

	mockFS := fstest.MapFS{
		"sql/V1__first.sql":        &fstest.MapFile{Data: []byte("SELECT 'v1';")},
		"sql/V2__second.sql":       &fstest.MapFile{Data: []byte("SELECT 'v2';")},
		"sql/R__zebra.sql":         &fstest.MapFile{Data: []byte("SELECT 'r_zebra';")},
		"sql/R__00_first_rep.sql":  &fstest.MapFile{Data: []byte("SELECT 'r_00';")},
		"sql/R__apple.sql":         &fstest.MapFile{Data: []byte("SELECT 'r_apple';")},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = mockFS
	db := mock.NewMockDatabase()

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	if res.MigrationsExecuted != 5 {
		t.Fatalf("expected 5 migrations executed, got %d", res.MigrationsExecuted)
	}

	// Execution sequence must be: V1, V2, R__00_first_rep, R__apple, R__zebra
	expectedExecution := []struct {
		script   string
		category string
	}{
		{"V1__first.sql", "Versioned"},
		{"V2__second.sql", "Versioned"},
		{"R__00_first_rep.sql", "Repeatable"},
		{"R__apple.sql", "Repeatable"},
		{"R__zebra.sql", "Repeatable"},
	}

	for i, exp := range expectedExecution {
		got := res.ExecutedMigrations[i]
		if got.Script != exp.script {
			t.Errorf("step %d: expected script %q, got %q", i, exp.script, got.Script)
		}
		if got.Category != exp.category {
			t.Errorf("step %d: expected category %q, got %q", i, exp.category, got.Category)
		}
	}
}

// -----------------------------------------------------------------------------
// FEATURE 12: CUMULATIVE BASELINE ADVERSARIAL SUITE
// -----------------------------------------------------------------------------

// TestChallengerCumulativeBaselineFreshDatabase_Adversarial tests:
// Fresh database with V1, V2, B2, V3:
// - B2 runs first (Type: "BASELINE")
// - V1 and V2 are skipped
// - V3 runs (Type: "SQL")
// - TargetVersion is 3
func TestChallengerCumulativeBaselineFreshDatabase_Adversarial(t *testing.T) {
	ctx := context.Background()

	mockFS := fstest.MapFS{
		"sql/V1__init.sql":    &fstest.MapFile{Data: []byte("CREATE TABLE v1 (id INT64);")},
		"sql/V2__old.sql":     &fstest.MapFile{Data: []byte("CREATE TABLE v2 (id INT64);")},
		"sql/B2__base.sql":    &fstest.MapFile{Data: []byte("CREATE TABLE base2 (id INT64);")},
		"sql/V3__feature.sql": &fstest.MapFile{Data: []byte("CREATE TABLE v3 (id INT64);")},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = mockFS
	db := mock.NewMockDatabase()

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	if res.MigrationsExecuted != 2 {
		t.Fatalf("expected exactly 2 migrations executed, got %d", res.MigrationsExecuted)
	}

	// Verify B2 executed first as BASELINE
	if res.ExecutedMigrations[0].Script != "B2__base.sql" ||
		res.ExecutedMigrations[0].Type != "BASELINE" ||
		res.ExecutedMigrations[0].Category != "Baseline" ||
		res.ExecutedMigrations[0].Version != "2" {
		t.Errorf("executed[0] mismatch: %+v", res.ExecutedMigrations[0])
	}

	// Verify V3 executed second as SQL
	if res.ExecutedMigrations[1].Script != "V3__feature.sql" ||
		res.ExecutedMigrations[1].Type != "SQL" ||
		res.ExecutedMigrations[1].Category != "Versioned" ||
		res.ExecutedMigrations[1].Version != "3" {
		t.Errorf("executed[1] mismatch: %+v", res.ExecutedMigrations[1])
	}

	if res.TargetVersion != "3" {
		t.Errorf("expected TargetVersion '3', got %s", res.TargetVersion)
	}

	// Verify schema history table
	history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory failed: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}
	if history[0].InstalledRank != 1 || history[0].Type != "BASELINE" || history[0].Version.String() != "2" {
		t.Errorf("history[0] mismatch: %+v", history[0])
	}
	if history[1].InstalledRank != 2 || history[1].Type != "SQL" || history[1].Version.String() != "3" {
		t.Errorf("history[1] mismatch: %+v", history[1])
	}

	// Verify executed SQL statements: only B2 and V3 executed, NOT V1 or V2
	stmts := db.ExecutedStatements()
	for _, stmt := range stmts {
		if stmt == "CREATE TABLE v1 (id INT64);" || stmt == "CREATE TABLE v2 (id INT64);" {
			t.Errorf("superseded migration statement was executed: %s", stmt)
		}
	}
}

// TestChallengerCumulativeBaselinePopulatedDatabase_Adversarial tests that on a
// database with existing history, cumulative baseline scripts are NOT applied.
func TestChallengerCumulativeBaselinePopulatedDatabase_Adversarial(t *testing.T) {
	ctx := context.Background()

	t.Run("existing history with V1 and V2: B2 ignored, only V3 applied", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		v1 := "1"
		v2 := "2"
		cs1, _ := checksum.CalculateString("SELECT 1;")
		cs2, _ := checksum.CalculateString("SELECT 2;")

		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1,
			Description:   "init",
			Type:          "SQL",
			Script:        "V1__init.sql",
			Checksum:      &cs1,
			Success:       true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2,
			Version:       &v2,
			Description:   "upgrade",
			Type:          "SQL",
			Script:        "V2__upgrade.sql",
			Checksum:      &cs2,
			Success:       true,
		})

		mockFS := fstest.MapFS{
			"sql/V1__init.sql":    &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__upgrade.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/B2__base.sql":    &fstest.MapFile{Data: []byte("SELECT 'base';")},
			"sql/V3__next.sql":    &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		// Only V3 should run; B2 must NOT run
		if res.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}
		if res.ExecutedMigrations[0].Script != "V3__next.sql" {
			t.Errorf("expected V3__next.sql executed, got %s", res.ExecutedMigrations[0].Script)
		}
	})

	t.Run("existing history with V1 applied: B2 ignored, V2 and V3 applied", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		v1 := "1"
		cs1, _ := checksum.CalculateString("SELECT 1;")
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1,
			Description:   "init",
			Type:          "SQL",
			Script:        "V1__init.sql",
			Checksum:      &cs1,
			Success:       true,
		})

		mockFS := fstest.MapFS{
			"sql/V1__init.sql":    &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/B2__base.sql":    &fstest.MapFile{Data: []byte("SELECT 'base';")},
			"sql/V2__second.sql":  &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__third.sql":   &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		// B2 must NOT run; V2 and V3 must execute
		if res.MigrationsExecuted != 2 {
			t.Fatalf("expected 2 migrations executed (V2, V3), got %d", res.MigrationsExecuted)
		}
		if res.ExecutedMigrations[0].Script != "V2__second.sql" || res.ExecutedMigrations[1].Script != "V3__third.sql" {
			t.Errorf("unexpected executed migrations: %+v", res.ExecutedMigrations)
		}
	})

	t.Run("existing history with only repeatable migration: B2 ignored, V3 applied", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		csSeed, _ := checksum.CalculateString("SELECT 'seed';")
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       nil,
			Description:   "seed",
			Type:          "SQL",
			Script:        "R__seed.sql",
			Checksum:      &csSeed,
			Success:       true,
		})

		mockFS := fstest.MapFS{
			"sql/R__seed.sql":  &fstest.MapFile{Data: []byte("SELECT 'seed';")},
			"sql/B2__base.sql": &fstest.MapFile{Data: []byte("SELECT 'base';")},
			"sql/V3__new.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		// Database is NOT fresh (history table contains applied migration R__seed.sql).
		// Therefore B2 must NOT run; only V3 should execute!
		if res.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration executed (V3 only, B2 ignored on populated database), got %d: %+v", res.MigrationsExecuted, res.ExecutedMigrations)
		}
		if len(res.ExecutedMigrations) > 0 && res.ExecutedMigrations[0].Script != "V3__new.sql" {
			t.Errorf("expected V3__new.sql executed, got %s", res.ExecutedMigrations[0].Script)
		}
	})

	t.Run("existing history with both versioned and repeatable migrations: B2 ignored, V3 applied", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		v1 := "1"
		cs1, _ := checksum.CalculateString("SELECT 1;")
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1,
			Description:   "init",
			Type:          "SQL",
			Script:        "V1__init.sql",
			Checksum:      &cs1,
			Success:       true,
		})

		csSeed, _ := checksum.CalculateString("SELECT 'seed';")
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2,
			Version:       nil,
			Description:   "seed",
			Type:          "SQL",
			Script:        "R__seed.sql",
			Checksum:      &csSeed,
			Success:       true,
		})

		mockFS := fstest.MapFS{
			"sql/V1__init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/R__seed.sql":  &fstest.MapFile{Data: []byte("SELECT 'seed';")},
			"sql/B2__base.sql": &fstest.MapFile{Data: []byte("SELECT 'base';")},
			"sql/V3__new.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		// Database is populated with both versioned and repeatable migrations.
		// B2 must NOT run; R__seed has identical checksum so it shouldn't re-run; only V3 executes!
		if res.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration executed (V3 only, B2 ignored on populated database), got %d: %+v", res.MigrationsExecuted, res.ExecutedMigrations)
		}
		if len(res.ExecutedMigrations) > 0 && res.ExecutedMigrations[0].Script != "V3__new.sql" {
			t.Errorf("expected V3__new.sql executed, got %s", res.ExecutedMigrations[0].Script)
		}

		// Verify history table contains 3 records in correct rank order
		history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if err != nil {
			t.Fatalf("FetchHistory failed: %v", err)
		}
		if len(history) != 3 {
			t.Fatalf("expected 3 history records, got %d", len(history))
		}
		if history[2].InstalledRank != 3 || history[2].Type != "SQL" || history[2].Script != "V3__new.sql" {
			t.Errorf("history[2] mismatch: %+v", history[2])
		}
	})
}

// -----------------------------------------------------------------------------
// FEATURE 11: OUT-OF-ORDER EXECUTION ADVERSARIAL SUITE
// -----------------------------------------------------------------------------

// TestChallengerOutOfOrderFalse_Adversarial tests that outOfOrder=false:
// - skips older unapplied versions
// - does NOT corrupt result.TargetVersion
func TestChallengerOutOfOrderFalse_Adversarial(t *testing.T) {
	ctx := context.Background()

	t.Run("outOfOrder=false with newer pending version V4: skips V2, executes V4, TargetVersion=4", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		v1 := "1"
		v3 := "3"
		cs1, _ := checksum.CalculateString("SELECT 1;")
		cs3, _ := checksum.CalculateString("SELECT 3;")
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1, Description: "first", Type: "SQL", Script: "V1__first.sql", Checksum: &cs1, Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v3, Description: "third", Type: "SQL", Script: "V3__third.sql", Checksum: &cs3, Success: true,
		})

		mockFS := fstest.MapFS{
			"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
			"sql/V4__fourth.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS
		cfg.OutOfOrder = false
		cfg.ValidateOnMigrate = false

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		// Only V4 should run; V2 skipped
		if res.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}
		if res.ExecutedMigrations[0].Version != "4" {
			t.Errorf("expected V4 executed, got %s", res.ExecutedMigrations[0].Version)
		}
		// TargetVersion must advance to 4
		if res.TargetVersion != "4" {
			t.Errorf("expected TargetVersion '4', got %s", res.TargetVersion)
		}
	})

	t.Run("outOfOrder=false with no newer pending version: skips V2, 0 executed, TargetVersion preserves '3'", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		v1 := "1"
		v3 := "3"
		cs1, _ := checksum.CalculateString("SELECT 1;")
		cs3, _ := checksum.CalculateString("SELECT 3;")
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1, Description: "first", Type: "SQL", Script: "V1__first.sql", Checksum: &cs1, Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v3, Description: "third", Type: "SQL", Script: "V3__third.sql", Checksum: &cs3, Success: true,
		})

		mockFS := fstest.MapFS{
			"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS
		cfg.OutOfOrder = false
		cfg.ValidateOnMigrate = false

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		// V2 is skipped, 0 migrations executed
		if res.MigrationsExecuted != 0 {
			t.Fatalf("expected 0 migrations executed, got %d", res.MigrationsExecuted)
		}
		// TargetVersion must NOT corrupt to '2' or empty, must remain '3'
		if res.TargetVersion != "3" {
			t.Errorf("expected TargetVersion '3', got %s", res.TargetVersion)
		}
		if res.InitialVersion != "3" {
			t.Errorf("expected InitialVersion '3', got %s", res.InitialVersion)
		}
	})

	t.Run("outOfOrder=false with multiple skipped versions V2 and V4 at V5: TargetVersion preserves '5'", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		v1 := "1"
		v3 := "3"
		v5 := "5"
		cs1, _ := checksum.CalculateString("SELECT 1;")
		cs3, _ := checksum.CalculateString("SELECT 3;")
		cs5, _ := checksum.CalculateString("SELECT 5;")
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1, Description: "first", Type: "SQL", Script: "V1__first.sql", Checksum: &cs1, Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v3, Description: "third", Type: "SQL", Script: "V3__third.sql", Checksum: &cs3, Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 3, Version: &v5, Description: "fifth", Type: "SQL", Script: "V5__fifth.sql", Checksum: &cs5, Success: true,
		})

		mockFS := fstest.MapFS{
			"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
			"sql/V4__fourth.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
			"sql/V5__fifth.sql":  &fstest.MapFile{Data: []byte("SELECT 5;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS
		cfg.OutOfOrder = false
		cfg.ValidateOnMigrate = false

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		if res.MigrationsExecuted != 0 {
			t.Fatalf("expected 0 migrations executed, got %d", res.MigrationsExecuted)
		}
		if res.TargetVersion != "5" {
			t.Errorf("expected TargetVersion '5', got %s", res.TargetVersion)
		}
	})
}

// TestChallengerOutOfOrderTrue_Adversarial tests that outOfOrder=true:
// - applies older unapplied versions
// - does NOT regress result.TargetVersion
func TestChallengerOutOfOrderTrue_Adversarial(t *testing.T) {
	ctx := context.Background()

	t.Run("outOfOrder=true applies V2 when at V3 without regressing TargetVersion from 3", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		v1 := "1"
		v3 := "3"
		cs1, _ := checksum.CalculateString("SELECT 1;")
		cs3, _ := checksum.CalculateString("SELECT 3;")
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1, Description: "first", Type: "SQL", Script: "V1__first.sql", Checksum: &cs1, Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v3, Description: "third", Type: "SQL", Script: "V3__third.sql", Checksum: &cs3, Success: true,
		})

		mockFS := fstest.MapFS{
			"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS
		cfg.OutOfOrder = true

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		if res.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}
		if res.ExecutedMigrations[0].Version != "2" {
			t.Errorf("expected V2 executed, got %s", res.ExecutedMigrations[0].Version)
		}
		// TargetVersion must NOT regress to '2'
		if res.TargetVersion != "3" {
			t.Errorf("expected TargetVersion '3', got %s", res.TargetVersion)
		}
	})

	t.Run("outOfOrder=true applies V2 and V4 when at V3, advancing TargetVersion to 4", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		v1 := "1"
		v3 := "3"
		cs1, _ := checksum.CalculateString("SELECT 1;")
		cs3, _ := checksum.CalculateString("SELECT 3;")
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1, Description: "first", Type: "SQL", Script: "V1__first.sql", Checksum: &cs1, Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v3, Description: "third", Type: "SQL", Script: "V3__third.sql", Checksum: &cs3, Success: true,
		})

		mockFS := fstest.MapFS{
			"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
			"sql/V4__fourth.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS
		cfg.OutOfOrder = true

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		if res.MigrationsExecuted != 2 {
			t.Fatalf("expected 2 migrations executed, got %d", res.MigrationsExecuted)
		}
		if res.ExecutedMigrations[0].Version != "2" || res.ExecutedMigrations[1].Version != "4" {
			t.Errorf("expected V2 then V4, got %+v", res.ExecutedMigrations)
		}
		// TargetVersion must advance to 4
		if res.TargetVersion != "4" {
			t.Errorf("expected TargetVersion '4', got %s", res.TargetVersion)
		}
	})

	t.Run("outOfOrder=true applies multiple unapplied versions V2, V5, V8 when at V10", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		v10 := "10"
		cs10, _ := checksum.CalculateString("SELECT 10;")
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v10, Description: "ten", Type: "SQL", Script: "V10__ten.sql", Checksum: &cs10, Success: true,
		})

		mockFS := fstest.MapFS{
			"sql/V2__two.sql":   &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V5__five.sql":  &fstest.MapFile{Data: []byte("SELECT 5;")},
			"sql/V8__eight.sql": &fstest.MapFile{Data: []byte("SELECT 8;")},
			"sql/V10__ten.sql":  &fstest.MapFile{Data: []byte("SELECT 10;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = mockFS
		cfg.OutOfOrder = true

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		if res.MigrationsExecuted != 3 {
			t.Fatalf("expected 3 migrations executed, got %d", res.MigrationsExecuted)
		}
		expectedVers := []string{"2", "5", "8"}
		for i, v := range expectedVers {
			if res.ExecutedMigrations[i].Version != v {
				t.Errorf("step %d: expected version %s, got %s", i, v, res.ExecutedMigrations[i].Version)
			}
		}
		// TargetVersion must remain 10 without regressing
		if res.TargetVersion != "10" {
			t.Errorf("expected TargetVersion '10', got %s", res.TargetVersion)
		}
	})

	t.Run("target='next' behavior difference between outOfOrder=false and outOfOrder=true", func(t *testing.T) {
		setup := func(outOfOrder bool) (*MigrateResult, error) {
			db := mock.NewMockDatabase()
			_ = db.EnsureSchema(ctx, "test_ds")
			_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

			v1 := "1"
			v3 := "3"
			cs1, _ := checksum.CalculateString("SELECT 1;")
			cs3, _ := checksum.CalculateString("SELECT 3;")
			_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
				InstalledRank: 1, Version: &v1, Description: "first", Type: "SQL", Script: "V1__first.sql", Checksum: &cs1, Success: true,
			})
			_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
				InstalledRank: 2, Version: &v3, Description: "third", Type: "SQL", Script: "V3__third.sql", Checksum: &cs3, Success: true,
			})

			mockFS := fstest.MapFS{
				"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
				"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
				"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
				"sql/V4__fourth.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
			}

			cfg := config.NewDefaultConfiguration()
			cfg.DefaultSchema = "test_ds"
			cfg.FS = mockFS
			cfg.Target = "next"
			cfg.OutOfOrder = outOfOrder
			if !outOfOrder {
				cfg.ValidateOnMigrate = false
			}

			m, err := New(cfg, db)
			if err != nil {
				return nil, err
			}
			return m.Migrate(ctx)
		}

		// When OutOfOrder=false, next must skip V2 and select V4
		resFalse, err := setup(false)
		if err != nil {
			t.Fatalf("setup(false) failed: %v", err)
		}
		if resFalse.MigrationsExecuted != 1 || resFalse.ExecutedMigrations[0].Version != "4" {
			t.Errorf("OutOfOrder=false: expected V4 executed, got %+v", resFalse.ExecutedMigrations)
		}
		if resFalse.TargetVersion != "4" {
			t.Errorf("OutOfOrder=false: expected TargetVersion '4', got %s", resFalse.TargetVersion)
		}

		// When OutOfOrder=true, next must select V2
		resTrue, err := setup(true)
		if err != nil {
			t.Fatalf("setup(true) failed: %v", err)
		}
		if resTrue.MigrationsExecuted != 1 || resTrue.ExecutedMigrations[0].Version != "2" {
			t.Errorf("OutOfOrder=true: expected V2 executed, got %+v", resTrue.ExecutedMigrations)
		}
		if resTrue.TargetVersion != "3" {
			t.Errorf("OutOfOrder=true: expected TargetVersion '3' (not regressed to 2), got %s", resTrue.TargetVersion)
		}
	})
}

// TestChallengerBaselineAndOutOfOrderInterplay tests the interaction between
// cumulative baseline and out-of-order execution when an unapplied migration
// falls between baselineVersion and maxAppliedVersion.
func TestChallengerBaselineAndOutOfOrderInterplay(t *testing.T) {
	ctx := context.Background()

	// Initial fresh database deployment with B2 and V3
	db := mock.NewMockDatabase()
	mockFS := fstest.MapFS{
		"sql/B2__base.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE base (id INT64);")},
		"sql/V3__three.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE v3 (id INT64);")},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = mockFS

	m1, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	res1, err := m1.Migrate(ctx)
	if err != nil {
		t.Fatalf("initial Migrate failed: %v", err)
	}
	if res1.MigrationsExecuted != 2 {
		t.Fatalf("expected 2 migrations executed, got %d", res1.MigrationsExecuted)
	}
	if res1.TargetVersion != "3" {
		t.Fatalf("expected TargetVersion '3', got %s", res1.TargetVersion)
	}

	// Now introduce V2.5 (newer than baseline 2, but older than applied 3)
	mockFS["sql/V2.5__mid.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE v2_5 (id INT64);")}

	// 1. With outOfOrder=false: V2.5 must be skipped, TargetVersion remains 3
	cfg.OutOfOrder = false
	cfg.ValidateOnMigrate = false
	m2, _ := New(cfg, db)
	res2, err := m2.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate(outOfOrder=false) failed: %v", err)
	}
	if res2.MigrationsExecuted != 0 {
		t.Errorf("expected 0 migrations executed when outOfOrder=false, got %d", res2.MigrationsExecuted)
	}
	if res2.TargetVersion != "3" {
		t.Errorf("expected TargetVersion '3', got %s", res2.TargetVersion)
	}

	// 2. With outOfOrder=true: V2.5 must execute, TargetVersion remains 3
	cfg.OutOfOrder = true
	cfg.ValidateOnMigrate = true
	m3, _ := New(cfg, db)
	res3, err := m3.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate(outOfOrder=true) failed: %v", err)
	}
	if res3.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed when outOfOrder=true, got %d", res3.MigrationsExecuted)
	}
	if res3.ExecutedMigrations[0].Version != "2.5" {
		t.Errorf("expected V2.5 executed, got %s", res3.ExecutedMigrations[0].Version)
	}
	if res3.TargetVersion != "3" {
		t.Errorf("expected TargetVersion '3' preserved, got %s", res3.TargetVersion)
	}
}

