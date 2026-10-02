package migrator

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/RoryQ/noway/pkg/checksum"
	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database"
	"github.com/RoryQ/noway/pkg/database/mock"
	"github.com/RoryQ/noway/pkg/resolver"
)

func TestMigrateBasic(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__create_users.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64, name STRING);"),
		},
		"sql/V2__create_posts.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE posts (id INT64, user_id INT64, content STRING);"),
		},
		"sql/R__create_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW user_posts AS SELECT * FROM users JOIN posts ON users.id = posts.user_id;"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_dataset"
	cfg.FS = mockFS
	cfg.Locations = []string{"sql"}

	db := mock.NewMockDatabase()
	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create Migrator: %v", err)
	}

	ctx := context.Background()
	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	if res.MigrationsExecuted != 3 {
		t.Errorf("expected 3 migrations executed, got %d", res.MigrationsExecuted)
	}
	if res.TargetVersion != "2" {
		t.Errorf("expected TargetVersion '2', got %s", res.TargetVersion)
	}

	// Verify history table
	history, err := db.FetchHistory(ctx, "test_dataset", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory error: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history records, got %d", len(history))
	}

	if history[0].Version.String() != "1" || history[0].InstalledRank != 1 {
		t.Errorf("unexpected record 0: %+v", history[0])
	}
	if history[1].Version.String() != "2" || history[1].InstalledRank != 2 {
		t.Errorf("unexpected record 1: %+v", history[1])
	}
	if history[2].Version != nil || history[2].InstalledRank != 3 {
		t.Errorf("unexpected repeatable record 2: %+v", history[2])
	}

	// Run migrate again -> 0 migrations executed
	res2, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate error: %v", err)
	}
	if res2.MigrationsExecuted != 0 {
		t.Errorf("expected 0 migrations on second run, got %d", res2.MigrationsExecuted)
	}
}

func TestMigrateRepeatableUpdate(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__create_users.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"sql/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v1 AS SELECT 1;"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_dataset"
	cfg.FS = mockFS

	db := mock.NewMockDatabase()
	m, _ := New(cfg, db)
	ctx := context.Background()

	_, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("first Migrate error: %v", err)
	}

	// Modify repeatable migration content
	mockFS["sql/R__view.sql"] = &fstest.MapFile{
		Data: []byte("CREATE VIEW v1 AS SELECT 2;"),
	}

	m2, _ := New(cfg, db)
	res, err := m2.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate error: %v", err)
	}
	if res.MigrationsExecuted != 1 {
		t.Errorf("expected 1 repeatable migration re-applied, got %d", res.MigrationsExecuted)
	}
}

func TestValidationAndRepair(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__create_users.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_dataset"
	cfg.FS = mockFS

	db := mock.NewMockDatabase()
	m, _ := New(cfg, db)
	ctx := context.Background()

	_, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("initial Migrate error: %v", err)
	}

	// Tamper with local migration file (checksum mismatch)
	mockFS["sql/V1__create_users.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE users (id INT64, age INT64);"),
	}

	m2, _ := New(cfg, db)
	valRes, err := m2.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if valRes.Valid {
		t.Errorf("expected validation failure due to checksum mismatch")
	}

	// Run repair
	repairRes, err := m2.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair error: %v", err)
	}
	if len(repairRes.AlignedChecksums) != 1 {
		t.Errorf("expected 1 aligned checksum, got %d", len(repairRes.AlignedChecksums))
	}

	// Validate again -> should now pass
	valRes2, err := m2.Validate(ctx)
	if err != nil || !valRes2.Valid {
		t.Errorf("expected validation to pass after repair, got valid=%v err=%v", valRes2.Valid, err)
	}
}

func TestBaselineAndTarget(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__old.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE old (id INT64);"),
		},
		"sql/V2__base.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE base (id INT64);"),
		},
		"sql/V3__new.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE new (id INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_dataset"
	cfg.BaselineVersion = "2"
	cfg.FS = mockFS

	db := mock.NewMockDatabase()
	m, _ := New(cfg, db)
	ctx := context.Background()

	// Baseline at version 2
	_, err := m.Baseline(ctx)
	if err != nil {
		t.Fatalf("Baseline error: %v", err)
	}

	// Migrate -> should only apply V3 (V1 and V2 are below or at baseline)
	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate error: %v", err)
	}
	if res.MigrationsExecuted != 1 {
		t.Errorf("expected 1 migration applied (V3), got %d", res.MigrationsExecuted)
	}
	if res.ExecutedMigrations[0].Version != "3" {
		t.Errorf("expected V3 executed, got %s", res.ExecutedMigrations[0].Version)
	}
}

func TestUndo(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"sql/V2__add_t2.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
		"sql/U2__drop_t2.sql": &fstest.MapFile{
			Data: []byte("DROP TABLE t2;"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_dataset"
	cfg.FS = mockFS

	db := mock.NewMockDatabase()
	m, _ := New(cfg, db)
	ctx := context.Background()

	_, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	undoRes, err := m.Undo(ctx)
	if err != nil {
		t.Fatalf("Undo error: %v", err)
	}

	if undoRes.UndoneVersion != "2" {
		t.Errorf("expected undone version '2', got %s", undoRes.UndoneVersion)
	}

	// Verify history table now only has V1
	history, _ := db.FetchHistory(ctx, "test_dataset", "flyway_schema_history")
	if len(history) != 1 || history[0].Version.String() != "1" {
		t.Errorf("expected only V1 in history, got %v", history)
	}
}

func TestClean(t *testing.T) {
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_dataset"
	cfg.CleanDisabled = true

	db := mock.NewMockDatabase()
	ctx := context.Background()
	_ = db.EnsureSchema(ctx, "test_dataset")
	m, _ := New(cfg, db)

	// Should fail with cleanDisabled=true
	_, err := m.Clean(ctx)
	if err == nil || !strings.Contains(err.Error(), "cleanDisabled is set to true") {
		t.Errorf("expected error with cleanDisabled, got %v", err)
	}

	// Enable clean
	cfg.CleanDisabled = false
	m2, _ := New(cfg, db)
	cleanRes, err := m2.Clean(ctx)
	if err != nil {
		t.Fatalf("Clean error: %v", err)
	}
	if len(cleanRes.SchemasCleaned) != 1 {
		t.Errorf("expected 1 schema cleaned, got %d", len(cleanRes.SchemasCleaned))
	}
}

func TestBaselineOnMigrateNonEmptySchema(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__initial.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"sql/V2__new_feature.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	ctx := context.Background()
	_ = db.EnsureSchema(ctx, "existing_ds")
	_ = db.EnsureHistoryTable(ctx, "existing_ds", "some_existing_table") // simulates non-empty DB

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "existing_ds"
	cfg.FS = mockFS
	cfg.BaselineOnMigrate = true
	cfg.BaselineVersion = "1"

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate with BaselineOnMigrate error: %v", err)
	}

	// Only V2 should have been executed because baseline was version 1
	if res.MigrationsExecuted != 1 {
		t.Errorf("expected 1 migration executed, got %d", res.MigrationsExecuted)
	}
	if len(res.ExecutedMigrations) != 1 || res.ExecutedMigrations[0].Version != "2" {
		t.Errorf("expected V2 executed, got %+v", res.ExecutedMigrations)
	}

	// Verify history table contains baseline + V2
	history, err := db.FetchHistory(ctx, "existing_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory error: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history records (baseline + V2), got %d", len(history))
	}
	if history[0].Type != "BASELINE" || history[0].Version.String() != "1" {
		t.Errorf("expected record 0 to be BASELINE v1, got %+v", history[0])
	}
	if history[1].Version.String() != "2" {
		t.Errorf("expected record 1 to be V2, got %+v", history[1])
	}
}

func TestMigrateTargetNextAndCurrent(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__first.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"sql/V2__second.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
		"sql/V3__third.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t3 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	ctx := context.Background()

	// 1. Test target="next" -> should only apply V1
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = mockFS
	cfg.Target = "next"

	m1, _ := New(cfg, db)
	res1, err := m1.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate target=next error: %v", err)
	}
	if res1.MigrationsExecuted != 1 || res1.ExecutedMigrations[0].Version != "1" {
		t.Errorf("expected only V1 executed with target=next, got %d migrations", res1.MigrationsExecuted)
	}

	// 2. Test target="current" -> should apply 0 versioned migrations
	cfg.Target = "current"
	m2, _ := New(cfg, db)
	res2, err := m2.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate target=current error: %v", err)
	}
	if res2.MigrationsExecuted != 0 {
		t.Errorf("expected 0 migrations executed with target=current, got %d", res2.MigrationsExecuted)
	}

	// 3. Test target="next" again -> should apply V2
	cfg.Target = "next"
	m3, _ := New(cfg, db)
	res3, err := m3.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate target=next second run error: %v", err)
	}
	if res3.MigrationsExecuted != 1 || res3.ExecutedMigrations[0].Version != "2" {
		t.Errorf("expected V2 executed with target=next, got %+v", res3.ExecutedMigrations)
	}
}

func TestInfoFutureAndMissingStates(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V2__resolved.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	ctx := context.Background()
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	// Insert V1 (Missing, since resolved max is V2) and V3 (Future, since resolved max is V2)
	v1Str := "1"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "missing v1",
		Type:          "SQL",
		Script:        "V1__missing.sql",
		Success:       true,
	})
	v3Str := "3"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2,
		Version:       &v3Str,
		Description:   "future v3",
		Type:          "SQL",
		Script:        "V3__future.sql",
		Success:       true,
	})

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = mockFS

	m, _ := New(cfg, db)
	infoRes, err := m.Info(ctx)
	if err != nil {
		t.Fatalf("Info error: %v", err)
	}

	stateMap := make(map[string]resolver.MigrationState)
	for _, item := range infoRes.Migrations {
		stateMap[item.Script] = item.State
	}

	if stateMap["V1__missing.sql"] != resolver.StateMissingSuccess {
		t.Errorf("expected V1 to be Missing, got %s", stateMap["V1__missing.sql"])
	}
	if stateMap["V3__future.sql"] != resolver.StateFutureSuccess {
		t.Errorf("expected V3 to be Future, got %s", stateMap["V3__future.sql"])
	}
	if stateMap["V2__resolved.sql"] != resolver.StateIgnored {
		t.Errorf("expected V2 to be Ignored when outOfOrder=false, got %s", stateMap["V2__resolved.sql"])
	}

	// When outOfOrder is enabled, V2 becomes Pending
	cfg.OutOfOrder = true
	mOutOfOrder, _ := New(cfg, db)
	infoRes2, _ := mOutOfOrder.Info(ctx)
	for _, item := range infoRes2.Migrations {
		if item.Script == "V2__resolved.sql" && item.State != resolver.StatePending {
			t.Errorf("expected V2 to be Pending when outOfOrder=true, got %s", item.State)
		}
	}
}

func TestMigrateScriptMigrations(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__create_table.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE items (id INT64);"),
		},
		"sql/V2__seed_script.sh": &fstest.MapFile{
			Data: []byte(`#!/bin/bash
if [ "$FLYWAY_DATABASE" != "test_dataset" ]; then
    echo "Expected FLYWAY_DATABASE=test_dataset, got $FLYWAY_DATABASE" >&2
    exit 1
fi
if [ "$FLYWAY_TYPE" != "SCRIPT" ]; then
    echo "Expected FLYWAY_TYPE=SCRIPT, got $FLYWAY_TYPE" >&2
    exit 1
fi
echo "Bash script migration executed successfully"
exit 0
`),
		},
		"sql/R__repeatable_task.bash": &fstest.MapFile{
			Data: []byte(`#!/bin/bash
echo "Repeatable script migration run"
exit 0
`),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_dataset"
	cfg.FS = mockFS
	cfg.Locations = []string{"sql"}

	db := mock.NewMockDatabase()
	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create Migrator: %v", err)
	}

	ctx := context.Background()
	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed for script migrations: %v", err)
	}

	if res.MigrationsExecuted != 3 {
		t.Fatalf("expected 3 migrations executed, got %d", res.MigrationsExecuted)
	}

	// Verify history record for script migration
	history, err := db.FetchHistory(ctx, "test_dataset", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory error: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history records, got %d", len(history))
	}

	if history[1].Type != "SCRIPT" || history[1].Script != "V2__seed_script.sh" {
		t.Errorf("expected history record 1 to be SCRIPT, got %+v", history[1])
	}
	if history[2].Type != "SCRIPT" || history[2].Script != "R__repeatable_task.bash" {
		t.Errorf("expected history record 2 to be SCRIPT, got %+v", history[2])
	}
}

func TestMigrateShouldExecute(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__always.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"sql/V1__always.sql.conf": &fstest.MapFile{
			Data: []byte("shouldExecute=true"),
		},
		"sql/V2__skip_me.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2_skipped (id INT64);"),
		},
		"sql/V2__skip_me.sql.conf": &fstest.MapFile{
			Data: []byte("shouldExecute=false"),
		},
		"sql/V3__env_conditional.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t3_prod (id INT64);"),
		},
		"sql/V3__env_conditional.sql.conf": &fstest.MapFile{
			Data: []byte("shouldExecute=${env} == 'production'"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_dataset"
	cfg.FS = mockFS
	cfg.Locations = []string{"sql"}
	cfg.Placeholders = map[string]string{
		"env": "production",
	}

	db := mock.NewMockDatabase()
	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create Migrator: %v", err)
	}

	ctx := context.Background()
	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// V1 (true) and V3 (production == production) should execute, V2 (false) should be skipped
	if res.MigrationsExecuted != 2 {
		t.Fatalf("expected 2 migrations executed (V1 and V3), got %d", res.MigrationsExecuted)
	}

	history, _ := db.FetchHistory(ctx, "test_dataset", "flyway_schema_history")
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}

	if history[0].Script != "V1__always.sql" || history[1].Script != "V3__env_conditional.sql" {
		t.Errorf("unexpected history records applied: %+v", history)
	}

	// Verify Info marks V2 as Ignored
	infoRes, err := m.Info(ctx)
	if err != nil {
		t.Fatalf("Info error: %v", err)
	}

	var v2State resolver.MigrationState
	for _, item := range infoRes.Migrations {
		if item.Script == "V2__skip_me.sql" {
			v2State = item.State
		}
	}
	if v2State != resolver.StateIgnored {
		t.Errorf("expected V2 to be StateIgnored in Info, got %s", v2State)
	}
}

func TestMigrateScriptFailure(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__failing_script.sh": &fstest.MapFile{
			Data: []byte(`#!/bin/bash
echo "Simulating failure" >&2
exit 42
`),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_dataset"
	cfg.FS = mockFS
	cfg.Locations = []string{"sql"}

	db := mock.NewMockDatabase()
	m, _ := New(cfg, db)

	ctx := context.Background()
	_, err := m.Migrate(ctx)
	if err == nil {
		t.Fatalf("expected error from failing script migration, got nil")
	}

	if !strings.Contains(err.Error(), "exit status 42") && !strings.Contains(err.Error(), "Simulating failure") {
		t.Errorf("expected failure message containing exit code or output, got: %v", err)
	}

	// Verify failed history record inserted with success = false
	history, _ := db.FetchHistory(ctx, "test_dataset", "flyway_schema_history")
	if len(history) != 1 || history[0].Success != false {
		t.Errorf("expected 1 failed history record, got %+v", history)
	}
}

func TestMigrateCreateSchemas(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
	}

	t.Run("CreateSchemas_True_CreatesSchema", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "auto_schema"
		cfg.Schemas = []string{"auto_schema"}
		cfg.CreateSchemas = true
		cfg.FS = mockFS

		db := mock.NewMockDatabase()
		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		ctx := context.Background()
		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}

		exists, _ := db.SchemaExists(ctx, "auto_schema")
		if !exists {
			t.Errorf("expected schema 'auto_schema' to have been created")
		}
	})

	t.Run("CreateSchemas_False_FailsIfMissing", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "missing_schema"
		cfg.Schemas = []string{"missing_schema"}
		cfg.CreateSchemas = false
		cfg.FS = mockFS

		db := mock.NewMockDatabase()
		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		ctx := context.Background()
		_, err = m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error because schema does not exist and createSchemas=false, got nil")
		}
		if !strings.Contains(err.Error(), "does not exist and createSchemas is false") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("CreateSchemas_False_SucceedsIfSchemaExists", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "existing_schema"
		cfg.Schemas = []string{"existing_schema"}
		cfg.CreateSchemas = false
		cfg.FS = mockFS

		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(context.Background(), "existing_schema")

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		ctx := context.Background()
		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("expected migrate success for existing schema, got: %v", err)
		}
		if res.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}
	})
}

func TestMigratePlaceholderChecksumming(t *testing.T) {
	// Raw SQL contains ${table_name}
	rawSQL := "CREATE TABLE ${table_name} (id INT64, env STRING);"
	mockFS := fstest.MapFS{
		"sql/V1__create_table.sql": &fstest.MapFile{
			Data: []byte(rawSQL),
		},
		"sql/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW user_view AS SELECT * FROM ${table_name};"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = mockFS
	cfg.Placeholders = map[string]string{
		"table_name": "real_users_table",
	}

	db := mock.NewMockDatabase()
	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	ctx := context.Background()
	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if res.MigrationsExecuted != 2 {
		t.Fatalf("expected 2 migrations executed, got %d", res.MigrationsExecuted)
	}

	// 1. Verify history checksum matches post-replacement SQL
	history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}

	expectedReplacedSQL := "CREATE TABLE real_users_table (id INT64, env STRING);"
	expectedChecksum, _ := checksum.CalculateString(expectedReplacedSQL)
	rawChecksum, _ := checksum.CalculateString(rawSQL)

	v1History := history[0]
	if v1History.Checksum == nil {
		t.Fatalf("expected non-nil checksum")
	}
	if *v1History.Checksum != expectedChecksum {
		t.Errorf("expected checksum %d (from replaced sql), got %d (raw was %d)", expectedChecksum, *v1History.Checksum, rawChecksum)
	}

	// 2. Validate passes
	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if !valRes.Valid {
		t.Errorf("expected validation to pass, errors: %v", valRes.Error())
	}
}

