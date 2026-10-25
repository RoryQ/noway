package migrator_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/roryq/noway/pkg/checksum"
	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/database/mock"
	"github.com/roryq/noway/pkg/migrator"
)

func TestValidate_IgnoreFutureMigrations(t *testing.T) {
	ctx := context.Background()

	v1SQL := "CREATE TABLE t1 (id INT64);"
	v2SQL := "CREATE TABLE t2 (id INT64);"
	v3SQL := "CREATE TABLE t3 (id INT64);"

	// Local filesystem only has V1 and V2
	fs := fstest.MapFS{
		"migrations/V1__init.sql":  &fstest.MapFile{Data: []byte(v1SQL)},
		"migrations/V2__table.sql": &fstest.MapFile{Data: []byte(v2SQL)},
		"migrations/.keep":         &fstest.MapFile{Data: []byte("")},
	}

	// Database has V1, V2, and V3 applied (e.g. from newer app version)
	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_schema")
	_ = db.EnsureHistoryTable(ctx, "test_schema", "flyway_schema_history")

	v1Cs, _ := checksum.CalculateString(v1SQL)
	v2Cs, _ := checksum.CalculateString(v2SQL)
	v3Cs, _ := checksum.CalculateString(v3SQL)
	v1Str := "1"
	v2Str := "2"
	v3Str := "3"

	_ = db.InsertHistory(ctx, "test_schema", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1, Version: &v1Str, Description: "init", Type: "SQL", Script: "V1__init.sql", Checksum: &v1Cs, Success: true,
	})
	_ = db.InsertHistory(ctx, "test_schema", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2, Version: &v2Str, Description: "table", Type: "SQL", Script: "V2__table.sql", Checksum: &v2Cs, Success: true,
	})
	_ = db.InsertHistory(ctx, "test_schema", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 3, Version: &v3Str, Description: "future", Type: "SQL", Script: "V3__future.sql", Checksum: &v3Cs, Success: true,
	})

	t.Run("Default_IgnoreFutureMigrations_True_Passes", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valRes.Valid {
			t.Fatalf("expected Validate to pass with IgnoreFutureMigrations=true (default), got: %s", valRes.Error())
		}
	})

	t.Run("Strict_IgnoreFutureMigrations_False_Fails", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.IgnoreFutureMigrations = false

		m, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected Validate to fail with IgnoreFutureMigrations=false")
		}
		if !strings.Contains(valRes.Error(), "(future)") {
			t.Errorf("expected (future) error message, got: %s", valRes.Error())
		}
	})
}

func TestValidate_IgnoreMissingMigrations(t *testing.T) {
	ctx := context.Background()

	v1SQL := "CREATE TABLE t1 (id INT64);"
	v2SQL := "CREATE TABLE t2 (id INT64);"

	// Local filesystem only has V2
	fs := fstest.MapFS{
		"migrations/V2__table.sql": &fstest.MapFile{Data: []byte(v2SQL)},
		"migrations/.keep":         &fstest.MapFile{Data: []byte("")},
	}

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_schema")
	_ = db.EnsureHistoryTable(ctx, "test_schema", "flyway_schema_history")

	v1Cs, _ := checksum.CalculateString(v1SQL)
	v2Cs, _ := checksum.CalculateString(v2SQL)
	v1Str := "1"
	v2Str := "2"

	_ = db.InsertHistory(ctx, "test_schema", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1, Version: &v1Str, Description: "init", Type: "SQL", Script: "V1__init.sql", Checksum: &v1Cs, Success: true,
	})
	_ = db.InsertHistory(ctx, "test_schema", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2, Version: &v2Str, Description: "table", Type: "SQL", Script: "V2__table.sql", Checksum: &v2Cs, Success: true,
	})

	t.Run("Default_IgnoreMissingMigrations_False_Fails", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected Validate to fail when missing versioned migration on disk")
		}
		if !strings.Contains(valRes.Error(), "Detected applied migration not resolved locally: V1__init.sql") {
			t.Errorf("unexpected error message: %s", valRes.Error())
		}
	})

	t.Run("IgnoreMissingMigrations_True_Passes", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.IgnoreMissingMigrations = true

		m, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valRes.Valid {
			t.Fatalf("expected Validate to pass with IgnoreMissingMigrations=true, got: %s", valRes.Error())
		}
	})
}

func TestValidate_IgnorePendingMigrations(t *testing.T) {
	ctx := context.Background()

	v1SQL := "CREATE TABLE t1 (id INT64);"
	v2SQL := "CREATE TABLE t2 (id INT64);"

	// Local filesystem has V1 and V2
	fs := fstest.MapFS{
		"migrations/V1__init.sql":  &fstest.MapFile{Data: []byte(v1SQL)},
		"migrations/V2__table.sql": &fstest.MapFile{Data: []byte(v2SQL)},
		"migrations/.keep":         &fstest.MapFile{Data: []byte("")},
	}

	// Database only has V2 applied (out-of-order state)
	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_schema")
	_ = db.EnsureHistoryTable(ctx, "test_schema", "flyway_schema_history")

	v2Cs, _ := checksum.CalculateString(v2SQL)
	v2Str := "2"

	_ = db.InsertHistory(ctx, "test_schema", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1, Version: &v2Str, Description: "table", Type: "SQL", Script: "V2__table.sql", Checksum: &v2Cs, Success: true,
	})

	t.Run("Default_OutOfOrder_False_Fails", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected Validate to fail on pending out-of-order migration V1")
		}
	})

	t.Run("IgnorePendingMigrations_True_OutOfOrder_True_Passes", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.IgnorePendingMigrations = true
		cfg.OutOfOrder = true

		m, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valRes.Valid {
			t.Fatalf("expected Validate to pass with IgnorePendingMigrations=true and OutOfOrder=true, got: %s", valRes.Error())
		}
	})

	t.Run("IgnoreIgnoredMigrations_True_Passes", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.IgnoreIgnoredMigrations = true

		m, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valRes.Valid {
			t.Fatalf("expected Validate to pass with IgnoreIgnoredMigrations=true, got: %s", valRes.Error())
		}
	})
}

func TestValidate_UndoMigrations(t *testing.T) {
	ctx := context.Background()

	u1SQL := "DROP TABLE t1;"
	fs := fstest.MapFS{
		"migrations/V1__init.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		"migrations/U1__init.sql":  &fstest.MapFile{Data: []byte(u1SQL)},
		"migrations/.keep":         &fstest.MapFile{Data: []byte("")},
	}

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_schema")
	_ = db.EnsureHistoryTable(ctx, "test_schema", "flyway_schema_history")

	u1Cs, _ := checksum.CalculateString(u1SQL)
	v1Str := "1"

	// Insert history record for UNDO_SQL
	_ = db.InsertHistory(ctx, "test_schema", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "init",
		Type:          "UNDO_SQL",
		Script:        "U1__init.sql",
		Checksum:      &u1Cs,
		Success:       true,
	})

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// 1. Validate passes when U1__init.sql matches database history
	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !valRes.Valid {
		t.Fatalf("expected Validate to pass on matched UNDO_SQL migration, got: %s", valRes.Error())
	}

	// 2. Modify U1__init.sql content on disk -> Validate should still pass (undo rows skipped in checksum check)
	fs["migrations/U1__init.sql"] = &fstest.MapFile{Data: []byte("DROP TABLE t1; -- modified")}
	mModified, _ := migrator.New(cfg, db)
	valRes2, err := mModified.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if !valRes2.Valid {
		t.Fatalf("expected Validate to pass on modified undo migration, got: %s", valRes2.Error())
	}

	// 3. Remove U1__init.sql from disk -> Validate should still pass (undo rows skipped in not resolved locally check)
	delete(fs, "migrations/U1__init.sql")
	mRemoved, _ := migrator.New(cfg, db)
	valRes3, err := mRemoved.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if !valRes3.Valid {
		t.Fatalf("expected Validate to pass on missing undo migration file, got: %s", valRes3.Error())
	}
}

func TestValidate_NonEmptySchemaWithoutHistoryTable(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		"migrations/.keep":        &fstest.MapFile{Data: []byte("")},
	}

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_schema")
	// Add an untracked table to the schema without creating flyway_schema_history
	db.AddTable("test_schema", "existing_table")

	t.Run("BaselineOnMigrate_False_Fails", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.Schemas = []string{"test_schema"}
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.BaselineOnMigrate = false

		m, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected Validate to fail on non-empty schema without history table when baselineOnMigrate=false")
		}
		if !strings.Contains(valRes.Error(), "Found non-empty schema(s)") {
			t.Errorf("unexpected error message: %s", valRes.Error())
		}
	})

	t.Run("BaselineOnMigrate_True_Passes", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.Schemas = []string{"test_schema"}
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.BaselineOnMigrate = true

		m, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valRes.Valid {
			t.Fatalf("expected Validate to pass with baselineOnMigrate=true, got: %s", valRes.Error())
		}
	})
}

func TestValidate_PendingMigrationReporting(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__first.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		"migrations/V2__second.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t2 (id INT64);")},
	}

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_schema")
	_ = db.EnsureHistoryTable(ctx, "test_schema", "flyway_schema_history")

	v1Str := "1"
	cs, _ := checksum.CalculateString("CREATE TABLE t1 (id INT64);")
	_ = db.InsertHistory(ctx, "test_schema", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "first",
		Type:          "SQL",
		Script:        "V1__first.sql",
		Checksum:      &cs,
		Success:       true,
	})

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// 1. Standard Validate reports pending V2
	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if valRes.Valid {
		t.Fatalf("expected Validate to fail on pending V2")
	}
	if !strings.Contains(valRes.Error(), "Detected resolved migration not applied to database: 2") {
		t.Errorf("expected error message to mention detected resolved migration not applied, got: %s", valRes.Error())
	}

	// 2. Validate with ignorePending=true passes
	valResIgnore, err := m.Validate(ctx, true)
	if err != nil || !valResIgnore.Valid {
		t.Fatalf("expected Validate with ignorePending=true to pass, got: %v", err)
	}

	// 3. Validate with IgnoreMigrationPatterns = ["*:pending"] passes
	cfg2 := *cfg
	cfg2.IgnoreMigrationPatterns = []string{"*:pending"}
	cfg2.IgnoreMigrationPatternsExplicitlySet = true
	m2, _ := migrator.New(&cfg2, db)
	valRes2, err := m2.Validate(ctx)
	if err != nil || !valRes2.Valid {
		t.Fatalf("expected Validate with *:pending pattern to pass, got: %v", err)
	}
}

func TestValidate_DeletedRepeatableReporting(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/R__my_view.sql": &fstest.MapFile{Data: []byte("CREATE VIEW v1 AS SELECT 1;")},
		"migrations/.keep":          &fstest.MapFile{Data: []byte("")},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// Migrate initial repeatable
	if _, err := m.Migrate(ctx); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Delete repeatable file from disk
	delete(fs, "migrations/R__my_view.sql")

	// 1. Validate reports missing repeatable
	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if valRes.Valid {
		t.Fatalf("expected Validate to fail when repeatable is deleted without DELETE row")
	}
	if !strings.Contains(valRes.Error(), "Detected applied migration not resolved locally: R__my_view.sql") {
		t.Errorf("unexpected error message: %s", valRes.Error())
	}

	// 2. Repair writes DELETE row
	repRes, err := m.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if len(repRes.DeletedMigrations) != 1 || repRes.DeletedMigrations[0] != "R__my_view.sql" {
		t.Errorf("expected R__my_view.sql marked as DELETE, got: %+v", repRes.DeletedMigrations)
	}

	// 3. Subsequent Validate passes
	valResAfter, err := m.Validate(ctx)
	if err != nil || !valResAfter.Valid {
		t.Fatalf("expected Validate to pass after repair inserted DELETE row, got: %v", err)
	}
}

func TestMigrator_SkipExecutingMigrations(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__create_table.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	cfg.SkipExecutingMigrations = true

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	migRes, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if migRes.MigrationsExecuted != 1 {
		t.Errorf("expected 1 migration executed (history record written), got %d", migRes.MigrationsExecuted)
	}

	// Verify history table has V1 record
	history, err := db.FetchHistory(ctx, "test_schema", "flyway_schema_history")
	if err != nil || len(history) != 1 {
		t.Fatalf("expected 1 history record, got len=%d, err=%v", len(history), err)
	}
	if history[0].Script != "V1__create_table.sql" || !history[0].Success {
		t.Errorf("unexpected history record: %+v", history[0])
	}

	// Verify SQL was NOT executed: ExecutedStatements is empty
	if len(db.ExecutedStatements()) != 0 {
		t.Errorf("expected 0 executed statements when SkipExecutingMigrations=true, got %v", db.ExecutedStatements())
	}
}

func TestMigrator_CherryPickAndFailOnMissingTarget(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__one.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		"migrations/V2__two.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE t2 (id INT64);")},
		"migrations/V3__three.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t3 (id INT64);")},
	}

	// 1. CherryPick: only migrate V2
	db1 := mock.NewMockDatabase()
	cfg1 := config.NewDefaultConfiguration()
	cfg1.DefaultSchema = "test_schema"
	cfg1.FS = fs
	cfg1.Locations = []string{"migrations"}
	cfg1.CherryPick = []string{"2"}

	m1, err := migrator.New(cfg1, db1)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}
	res1, err := m1.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if res1.MigrationsExecuted != 1 || res1.ExecutedMigrations[0].Version != "2" {
		t.Errorf("expected only V2 to be cherry-picked, got %+v", res1.ExecutedMigrations)
	}

	// 2. FailOnMissingTarget with missing target version
	db2 := mock.NewMockDatabase()
	cfg2 := config.NewDefaultConfiguration()
	cfg2.DefaultSchema = "test_schema"
	cfg2.FS = fs
	cfg2.Locations = []string{"migrations"}
	cfg2.Target = "99"
	cfg2.FailOnMissingTarget = true

	m2, _ := migrator.New(cfg2, db2)
	_, err = m2.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "no migration with a target version 99 could be found") {
		t.Errorf("expected error for missing target version, got: %v", err)
	}

	// 3. FailOnMissingTarget with missing cherry-pick version
	db3 := mock.NewMockDatabase()
	cfg3 := config.NewDefaultConfiguration()
	cfg3.DefaultSchema = "test_schema"
	cfg3.FS = fs
	cfg3.Locations = []string{"migrations"}
	cfg3.CherryPick = []string{"99"}
	cfg3.FailOnMissingTarget = true

	m3, _ := migrator.New(cfg3, db3)
	_, err = m3.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "no migration with version 99 could be found to cherry pick") {
		t.Errorf("expected error for missing cherry-pick version, got: %v", err)
	}
}

func TestMigrator_CherryPick_ConsecutiveRuns(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__one.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		"migrations/V2__two.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE t2 (id INT64);")},
		"migrations/V3__three.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t3 (id INT64);")},
		"migrations/V4__four.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE t4 (id INT64);")},
		"migrations/R__view.sql":   &fstest.MapFile{Data: []byte("CREATE VIEW v1 AS SELECT 1;")},
		"migrations/.keep":         &fstest.MapFile{Data: []byte("")},
	}

	db := mock.NewMockDatabase()

	// 1. First run: cherry-pick V3
	cfg1 := config.NewDefaultConfiguration()
	cfg1.DefaultSchema = "test_schema"
	cfg1.FS = fs
	cfg1.Locations = []string{"migrations"}
	cfg1.CherryPick = []string{"3"}

	m1, err := migrator.New(cfg1, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}
	res1, err := m1.Migrate(ctx)
	if err != nil {
		t.Fatalf("first Migrate failed: %v", err)
	}
	if res1.MigrationsExecuted != 1 || res1.ExecutedMigrations[0].Version != "3" {
		t.Fatalf("expected only V3 to be executed, got %+v", res1.ExecutedMigrations)
	}

	// 2. Second run: cherry-pick V4 on the same database
	// This must succeed without failing validation for unapplied V1 and V2
	cfg2 := config.NewDefaultConfiguration()
	cfg2.DefaultSchema = "test_schema"
	cfg2.FS = fs
	cfg2.Locations = []string{"migrations"}
	cfg2.CherryPick = []string{"4"}

	m2, err := migrator.New(cfg2, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}
	res2, err := m2.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
	if res2.MigrationsExecuted != 1 || res2.ExecutedMigrations[0].Version != "4" {
		t.Fatalf("expected only V4 to be executed, got %+v", res2.ExecutedMigrations)
	}

	// 3. Third run: cherry-pick repeatable migration by script name
	cfg3 := config.NewDefaultConfiguration()
	cfg3.DefaultSchema = "test_schema"
	cfg3.FS = fs
	cfg3.Locations = []string{"migrations"}
	cfg3.CherryPick = []string{"R__view.sql"}

	m3, err := migrator.New(cfg3, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}
	res3, err := m3.Migrate(ctx)
	if err != nil {
		t.Fatalf("third Migrate failed: %v", err)
	}
	if res3.MigrationsExecuted != 1 || res3.ExecutedMigrations[0].Description != "view" {
		t.Fatalf("expected only R__view to be executed, got %+v", res3.ExecutedMigrations)
	}

	// 4. Fourth run: cherry-pick a LOWER version V2 when V3 and V4 are already applied and outOfOrder=false
	cfg4 := config.NewDefaultConfiguration()
	cfg4.DefaultSchema = "test_schema"
	cfg4.FS = fs
	cfg4.Locations = []string{"migrations"}
	cfg4.CherryPick = []string{"2"}

	m4, err := migrator.New(cfg4, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}
	res4, err := m4.Migrate(ctx)
	if err != nil {
		t.Fatalf("fourth Migrate (lower version cherry-pick) failed: %v", err)
	}
	if res4.MigrationsExecuted != 1 || res4.ExecutedMigrations[0].Version != "2" {
		t.Fatalf("expected only V2 to be executed, got %+v", res4.ExecutedMigrations)
	}
}

func TestValidateAndRepair_LongDescriptionAndScript(t *testing.T) {
	ctx := context.Background()

	// 250 chars description
	longDesc := strings.Repeat("d", 250)
	filename := "V1__" + longDesc + ".sql"
	repFilename := "R__" + longDesc + ".sql"

	fs := fstest.MapFS{
		"migrations/" + filename:    &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		"migrations/" + repFilename: &fstest.MapFile{Data: []byte("CREATE VIEW v1 AS SELECT 1;")},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// 1. Migrate
	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if res.MigrationsExecuted != 2 {
		t.Fatalf("expected 2 migrations executed, got %d", res.MigrationsExecuted)
	}

	// Verify history table has abbreviated descriptions
	history, err := db.FetchHistory(ctx, "test_schema", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory failed: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}
	expectedAbbr := strings.Repeat("d", 197) + "..."
	if history[0].Description != expectedAbbr {
		t.Errorf("expected V1 history description to be truncated, got %q", history[0].Description)
	}
	if history[1].Description != expectedAbbr {
		t.Errorf("expected R history description to be truncated, got %q", history[1].Description)
	}

	// 2. Validate succeeds without reporting description mismatch
	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if !valRes.ValidationSuccessful {
		t.Fatalf("Validate failed on long descriptions: %v", valRes.Error())
	}

	// 3. Repair succeeds without rewriting/misaligning
	repRes, err := m.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if len(repRes.MigrationsAligned) != 0 {
		t.Errorf("expected no alignments needed, got %+v", repRes.MigrationsAligned)
	}
}

func TestValidate_PendingAndOutdatedRepeatableMigrations(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/R__my_view.sql": &fstest.MapFile{Data: []byte("CREATE VIEW my_view AS SELECT 1;")},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// 1. Initial history table exists (e.g. from prior migrations)
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	// Validate reports pending repeatable migration
	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if valRes.Valid {
		t.Fatalf("expected Validate to fail on pending repeatable migration")
	}
	expectedPendingErr := "Detected resolved repeatable migration not applied to database: my view. To fix this error, either run migrate, or set -ignoreMigrationPatterns='*:pending'."
	if !strings.Contains(valRes.Error(), expectedPendingErr) {
		t.Errorf("expected error message %q, got: %s", expectedPendingErr, valRes.Error())
	}

	// Ignoring pending migrations allows Validate to pass
	cfgIgnorePending := *cfg
	cfgIgnorePending.IgnoreMigrationPatterns = []string{"*:pending"}
	cfgIgnorePending.IgnoreMigrationPatternsExplicitlySet = true
	mIgnorePending, _ := migrator.New(&cfgIgnorePending, db)
	valResIgnore, err := mIgnorePending.Validate(ctx)
	if err != nil || !valResIgnore.Valid {
		t.Fatalf("expected Validate to pass with *:pending, got errors: %v", err)
	}

	// 2. Migrate executes the repeatable migration
	migRes, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if migRes.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed, got %d", migRes.MigrationsExecuted)
	}

	// After migrate, Validate passes
	valResAfterMigrate, err := m.Validate(ctx)
	if err != nil || !valResAfterMigrate.Valid {
		t.Fatalf("expected Validate to pass after migrate, got: %v", err)
	}

	// 3. Modify repeatable migration on disk (changed checksum -> OUTDATED)
	fs["migrations/R__my_view.sql"] = &fstest.MapFile{Data: []byte("CREATE VIEW my_view AS SELECT 2;")}

	valResOutdated, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if valResOutdated.Valid {
		t.Fatalf("expected Validate to fail on outdated repeatable migration")
	}
	expectedOutdatedErr := "Detected outdated resolved repeatable migration that should be re-applied to database: my view. Run migrate to execute this migration."
	if !strings.Contains(valResOutdated.Error(), expectedOutdatedErr) {
		t.Errorf("expected error message %q, got: %s", expectedOutdatedErr, valResOutdated.Error())
	}

	// Outdated repeatable is also ignored by *:pending
	valResOutdatedIgnored, err := mIgnorePending.Validate(ctx)
	if err != nil || !valResOutdatedIgnored.Valid {
		t.Fatalf("expected outdated repeatable to be ignored by *:pending, got: %v", err)
	}

	// In Flyway, repeatable:pending alone does NOT ignore outdated repeatables (only *:pending or both versioned:pending and repeatable:pending do)
	cfgRepeatablePending := *cfg
	cfgRepeatablePending.IgnoreMigrationPatterns = []string{"repeatable:pending"}
	cfgRepeatablePending.IgnoreMigrationPatternsExplicitlySet = true
	mRepeatablePending, _ := migrator.New(&cfgRepeatablePending, db)
	valResRepIgnored, err := mRepeatablePending.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if valResRepIgnored.Valid {
		t.Fatalf("expected repeatable:pending alone NOT to ignore outdated repeatable")
	}

	// But both versioned:pending and repeatable:pending DO ignore outdated repeatables
	cfgBothPending := *cfg
	cfgBothPending.IgnoreMigrationPatterns = []string{"versioned:pending", "repeatable:pending"}
	cfgBothPending.IgnoreMigrationPatternsExplicitlySet = true
	mBothPending, _ := migrator.New(&cfgBothPending, db)
	valResBoth, err := mBothPending.Validate(ctx)
	if err != nil || !valResBoth.Valid {
		t.Fatalf("expected both versioned:pending,repeatable:pending to ignore outdated repeatable, got: %v", err)
	}

	// 4. Migrate re-runs outdated repeatable
	migRes2, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
	if migRes2.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed, got %d", migRes2.MigrationsExecuted)
	}

	// 5. Subsequent Validate passes cleanly
	valResFinal, err := m.Validate(ctx)
	if err != nil || !valResFinal.Valid {
		t.Fatalf("expected final Validate to pass, got: %v", err)
	}
}



