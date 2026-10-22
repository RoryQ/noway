package migrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/roryq/noway/pkg/checksum"
	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/database/mock"
	"github.com/roryq/noway/pkg/resolver"
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

	// Verify history table retains V1, V2 and appends UNDO_SQL record
	history, _ := db.FetchHistory(ctx, "test_dataset", "flyway_schema_history")
	if len(history) != 3 {
		t.Fatalf("expected 3 records in history after undo, got %d", len(history))
	}
	if history[2].Type != "UNDO_SQL" || history[2].Version.String() != "2" {
		t.Errorf("expected 3rd record to be UNDO_SQL for version 2, got type=%s ver=%v", history[2].Type, history[2].Version)
	}
	processed := processAppliedHistory(history)
	if len(processed.AppliedVersioned) != 1 || processed.AppliedVersioned["1"].Version.String() != "1" {
		t.Errorf("expected only V1 to be active in processed history, got %v", processed.AppliedVersioned)
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

	// Insert R__missing (applied in DB, but not present locally)
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 3,
		Version:       nil,
		Description:   "missing repeatable view",
		Type:          "SQL",
		Script:        "R__missing_view.sql",
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
	if stateMap["R__missing_view.sql"] != resolver.StateMissingSuccess {
		t.Errorf("expected R__missing_view to be Missing, got %s", stateMap["R__missing_view.sql"])
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

	// 1. Verify history checksum matches Flyway SqlMigrationResolver:
	// - Versioned migration V1 uses raw checksum (stable across environments)
	// - Repeatable migration R__ uses placeholder-replaced checksum
	history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}

	rawChecksum, _ := checksum.CalculateString(rawSQL)
	expectedRepeatableReplacedSQL := "CREATE VIEW user_view AS SELECT * FROM real_users_table;"
	expectedRepeatableChecksum, _ := checksum.CalculateString(expectedRepeatableReplacedSQL)

	v1History := history[0]
	if v1History.Checksum == nil {
		t.Fatalf("expected non-nil checksum for V1")
	}
	if *v1History.Checksum != rawChecksum {
		t.Errorf("expected V1 checksum %d (from raw sql), got %d", rawChecksum, *v1History.Checksum)
	}

	rHistory := history[1]
	if rHistory.Checksum == nil {
		t.Fatalf("expected non-nil checksum for R__")
	}
	if *rHistory.Checksum != expectedRepeatableChecksum {
		t.Errorf("expected R__ checksum %d (from replaced sql), got %d", expectedRepeatableChecksum, *rHistory.Checksum)
	}

	// 2. Validate passes
	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if !valRes.Valid {
		t.Errorf("expected validation to pass, errors: %v", valRes.Error())
	}

	// 3. Changing placeholder value keeps V1 valid (raw checksum unchanged) and causes repeatable R__ to re-execute
	cfgChanged := config.NewDefaultConfiguration()
	cfgChanged.DefaultSchema = "test_ds"
	cfgChanged.FS = mockFS
	cfgChanged.Placeholders = map[string]string{
		"table_name": "changed_users_table",
	}

	mChanged, err := New(cfgChanged, db)
	if err != nil {
		t.Fatalf("failed to create changed migrator: %v", err)
	}

	// V1 still validates because versioned checksum is raw (with ignorePending=true as used in migrate)
	valResChanged, err := mChanged.Validate(ctx, true)
	if err != nil {
		t.Fatalf("Validate error on changed config: %v", err)
	}
	if !valResChanged.Valid {
		t.Errorf("expected validation to pass for V1 when placeholder changed, got errors: %v", valResChanged.Error())
	}

	// Migrate re-runs repeatable migration R__view because its replaced checksum changed
	resChanged, err := mChanged.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if resChanged.MigrationsExecuted != 1 {
		t.Errorf("expected 1 repeatable migration re-executed due to placeholder checksum change, got %d", resChanged.MigrationsExecuted)
	}
}

func TestMigrateEnvPlaceholderCaseInsensitive(t *testing.T) {
	t.Setenv("FLYWAY_PLACEHOLDERS_DATASET", "prod_analytics")

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	config.LoadFromEnv(cfg)

	mockFS := fstest.MapFS{
		"sql/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${dataset}.users (id INT64);"),
		},
	}
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

	// Verify executed statement in mock database
	stmts := db.ExecutedStatements()
	if len(stmts) != 1 {
		t.Fatalf("expected 1 executed statement, got %d", len(stmts))
	}
	expectedSQL := "CREATE TABLE prod_analytics.users (id INT64)"
	if stmts[0] != expectedSQL {
		t.Errorf("expected SQL %q, got %q", expectedSQL, stmts[0])
	}
}

func TestOutOfOrderTargetVersionPreserved(t *testing.T) {
	// Pre-populate history with V1 and V3
	db := mock.NewMockDatabase()
	ctx := context.Background()
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	v1Str := "1"
	v3Str := "3"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "first",
		Type:          "SQL",
		Script:        "V1__first.sql",
		Success:       true,
	})
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2,
		Version:       &v3Str,
		Description:   "third",
		Type:          "SQL",
		Script:        "V3__third.sql",
		Success:       true,
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
	// TargetVersion must NOT downgrade to "2", it should remain at "3"
	if res.TargetVersion != "3" {
		t.Errorf("expected TargetVersion '3', got %s", res.TargetVersion)
	}
}

func TestTargetNextWithOutOfOrderDisabledNoStarvation(t *testing.T) {
	// Pre-populate history with V1 and V3
	db := mock.NewMockDatabase()
	ctx := context.Background()
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	v1Str := "1"
	v3Str := "3"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "first",
		Type:          "SQL",
		Script:        "V1__first.sql",
		Success:       true,
	})
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2,
		Version:       &v3Str,
		Description:   "third",
		Type:          "SQL",
		Script:        "V3__third.sql",
		Success:       true,
	})

	// Local has V1, V2, V3, V4. With outOfOrder=false and target=next:
	// V2 is out-of-order and skipped. The eligible "next" migration is V4!
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
	cfg.Target = "next"
	cfg.ValidateOnMigrate = false

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
	if res.ExecutedMigrations[0].Version != "4" {
		t.Errorf("expected V4 executed as next, got %s", res.ExecutedMigrations[0].Version)
	}
	if res.TargetVersion != "4" {
		t.Errorf("expected TargetVersion '4', got %s", res.TargetVersion)
	}
}

func TestCumulativeBaselineFreshDatabaseExecution(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__init.sql":     &fstest.MapFile{Data: []byte("CREATE TABLE v1 (id INT64);")},
		"sql/V2__old.sql":      &fstest.MapFile{Data: []byte("CREATE TABLE v2 (id INT64);")},
		"sql/B__2__base.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE base_v2 (id INT64);")},
		"sql/V3__feature.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE v3 (id INT64);")},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = mockFS

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	ctx := context.Background()
	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// B2 and V3 should execute (V1 and V2 superseded)
	if res.MigrationsExecuted != 2 {
		t.Fatalf("expected 2 migrations executed, got %d", res.MigrationsExecuted)
	}
	if res.ExecutedMigrations[0].Version != "2" || res.ExecutedMigrations[0].Type != "BASELINE" {
		t.Errorf("expected executed[0] to be BASELINE v2, got %+v", res.ExecutedMigrations[0])
	}
	if res.ExecutedMigrations[1].Version != "3" || res.ExecutedMigrations[1].Type != "SQL" {
		t.Errorf("expected executed[1] to be SQL v3, got %+v", res.ExecutedMigrations[1])
	}
	if res.TargetVersion != "3" {
		t.Errorf("expected TargetVersion '3', got %s", res.TargetVersion)
	}

	// Verify history table
	history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory error: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}
	if history[0].Type != "BASELINE" || history[0].Version.String() != "2" {
		t.Errorf("expected history record 0 to be BASELINE v2, got %+v", history[0])
	}
	if history[1].Type != "SQL" || history[1].Version.String() != "3" {
		t.Errorf("expected history record 1 to be SQL v3, got %+v", history[1])
	}

	// Re-run migrate -> 0 migrations
	res2, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
	if res2.MigrationsExecuted != 0 {
		t.Errorf("expected 0 migrations on second run, got %d", res2.MigrationsExecuted)
	}
}

func TestCumulativeBaselineIgnoredOnExistingDatabase(t *testing.T) {
	// Database already has V1 and V2 applied
	db := mock.NewMockDatabase()
	ctx := context.Background()
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	v1Str := "1"
	v2Str := "2"
	cs1, _ := checksum.CalculateString("SELECT 1;")
	cs2, _ := checksum.CalculateString("SELECT 2;")
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "init",
		Type:          "SQL",
		Script:        "V1__init.sql",
		Checksum:      &cs1,
		Success:       true,
	})
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2,
		Version:       &v2Str,
		Description:   "old",
		Type:          "SQL",
		Script:        "V2__old.sql",
		Checksum:      &cs2,
		Success:       true,
	})

	mockFS := fstest.MapFS{
		"sql/V1__init.sql":    &fstest.MapFile{Data: []byte("SELECT 1;")},
		"sql/V2__old.sql":     &fstest.MapFile{Data: []byte("SELECT 2;")},
		"sql/B2__base.sql":    &fstest.MapFile{Data: []byte("SELECT 'base';")},
		"sql/V3__new.sql":     &fstest.MapFile{Data: []byte("SELECT 3;")},
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

	// Only V3 should run; B2 is ignored on existing DB
	if res.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
	}
	if res.ExecutedMigrations[0].Version != "3" {
		t.Errorf("expected V3 executed, got %s", res.ExecutedMigrations[0].Version)
	}
}

func TestCumulativeBaselineWithTarget(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/B1__v1.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE b1 (id INT64);")},
		"sql/B3__v3.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE b3 (id INT64);")},
		"sql/V4__v4.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE v4 (id INT64);")},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = mockFS
	cfg.Target = "2"

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	ctx := context.Background()
	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// B1 is <= target (2), B3 is > target. V4 is > target. So only B1 should run!
	if res.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
	}
	if res.ExecutedMigrations[0].Version != "1" || res.ExecutedMigrations[0].Type != "BASELINE" {
		t.Errorf("expected B1 executed as BASELINE, got %+v", res.ExecutedMigrations[0])
	}
}

func TestInfoWithTargetNext(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
		"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = mockFS
	cfg.Target = "next"

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	ctx := context.Background()
	infoRes, err := m.Info(ctx)
	if err != nil {
		t.Fatalf("Info failed: %v", err)
	}

	stateByScript := make(map[string]resolver.MigrationState)
	for _, item := range infoRes.Migrations {
		stateByScript[item.Script] = item.State
	}

	// V1 is the "next" migration -> Pending
	if stateByScript["V1__first.sql"] != resolver.StatePending {
		t.Errorf("expected V1 to be StatePending, got %s", stateByScript["V1__first.sql"])
	}
	// V2 and V3 are above target ("next") -> Above Target
	if stateByScript["V2__second.sql"] != resolver.StateAboveTarget {
		t.Errorf("expected V2 to be StateAboveTarget, got %s", stateByScript["V2__second.sql"])
	}
	if stateByScript["V3__third.sql"] != resolver.StateAboveTarget {
		t.Errorf("expected V3 to be StateAboveTarget, got %s", stateByScript["V3__third.sql"])
	}
}

func TestMigrateDoesNotCountFailedUndoneDeletedAsApplied(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__one.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		"migrations/V2__two.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE t2 (id INT64);")},
		"migrations/V3__three.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t3 (id INT64);")},
		"migrations/V4__four.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE t4 (id INT64);")},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	// 1. Migrate V1 and V2
	cfg.Target = "2"
	m1, _ := New(cfg, db)
	res1, err := m1.Migrate(ctx)
	if err != nil || res1.MigrationsExecuted != 2 {
		t.Fatalf("first migrate failed: %v, exec=%d", err, res1.MigrationsExecuted)
	}

	// 2. Induce failure on V3
	cfg.Target = "latest"
	db.SetFailOnExecute("FAIL_V3")
	fs["migrations/V3__three.sql"] = &fstest.MapFile{Data: []byte("FAIL_V3;")}
	m2, _ := New(cfg, db)
	_, err = m2.Migrate(ctx)
	if err == nil {
		t.Fatalf("expected V3 to fail")
	}

	// Verify history has V3 in failed state
	history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if len(history) != 3 || history[2].Success != false {
		t.Fatalf("expected 3 history records with V3 failed, got %+v", history)
	}

	// 3. Clear failure condition and fix V3 SQL
	db.SetFailOnExecute("")
	fs["migrations/V3__three.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE t3 (id INT64);")}

	// With validateOnMigrate=false, Flyway MUST refuse to migrate while a failed migration
	// exists in history (it must neither skip V3 to apply V4 nor re-run V3 before repair).
	cfg.ValidateOnMigrate = false
	m3, _ := New(cfg, db)
	_, err = m3.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "failed migration") {
		t.Fatalf("expected migrate with validateOnMigrate=false to refuse failed V3, got: %v", err)
	}

	// Verify neither V3 nor V4 was executed
	historyAfterRefusal, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if len(historyAfterRefusal) != 3 {
		t.Fatalf("expected history to remain unchanged at 3 records after refusal, got %d", len(historyAfterRefusal))
	}

	// 4. After Repair removes the failed V3, Migrate executes V3 and V4
	if _, err := m3.Repair(ctx); err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	res3, err := m3.Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate after repair failed: %v", err)
	}
	if res3.MigrationsExecuted != 2 {
		t.Errorf("expected 2 migrations executed (V3 and V4), got %d", res3.MigrationsExecuted)
	}
	if len(res3.Migrations) != 2 || res3.Migrations[0].Version != "3" || res3.Migrations[1].Version != "4" {
		t.Errorf("expected V3 then V4 executed, got: %+v", res3.Migrations)
	}

	// 5. Verify UNDO_SQL and DELETE audit rows are not counted as applied
	v4Str := "4"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 5,
		Version:       &v4Str,
		Description:   "undo four",
		Type:          "UNDO_SQL",
		Script:        "U4__four.sql",
		Success:       true,
	})
	v3Str := "3"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 6,
		Version:       &v3Str,
		Description:   "three",
		Type:          "DELETE",
		Script:        "V3__three.sql",
		Success:       true,
	})

	res4, err := m3.Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate after UNDO_SQL/DELETE rows failed: %v", err)
	}
	if res4.MigrationsExecuted != 2 {
		t.Fatalf("expected 2 migrations re-executed (deleted V3 and undone V4), got %d", res4.MigrationsExecuted)
	}
}

func TestMigrate_FutureFailedMigration_WarnsWhenIgnoreFuture(t *testing.T) {
	ctx := context.Background()

	// Local files only go up to V2
	fs := fstest.MapFS{
		"migrations/V1__one.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"migrations/V2__two.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
	}

	db := mock.NewMockDatabase()
	v1Str := "1"
	v5Str := "5"
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1, Version: &v1Str, Description: "one", Type: "SQL", Script: "V1__one.sql", Success: true,
	})
	// V5 is a future failed migration (higher than maxResolvedVersion V2)
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2, Version: &v5Str, Description: "five", Type: "SQL", Script: "V5__five.sql", Success: false,
	})

	// 1. With IgnoreFutureMigrations=true (default): Migrate succeeds and warns
	cfgIgnore := config.NewDefaultConfiguration()
	cfgIgnore.DefaultSchema = "test_ds"
	cfgIgnore.FS = fs
	cfgIgnore.Locations = []string{"migrations"}
	cfgIgnore.IgnoreFutureMigrations = true

	mIgnore, err := New(cfgIgnore, db)
	if err != nil {
		t.Fatalf("New migrator failed: %v", err)
	}
	res, err := mIgnore.Migrate(ctx)
	if err != nil {
		t.Fatalf("expected Migrate to succeed with warning for future failed migration, got error: %v", err)
	}
	if res.MigrationsExecuted != 1 {
		t.Errorf("expected 1 migration executed (V2), got %d", res.MigrationsExecuted)
	}
	foundWarning := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "failed future migration to version 5") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Errorf("expected warning about failed future migration to version 5, got warnings: %v", res.Warnings)
	}

	// 2. With IgnoreFutureMigrations=false: Migrate fails (via validation error)
	cfgStrict := config.NewDefaultConfiguration()
	cfgStrict.DefaultSchema = "test_ds"
	cfgStrict.FS = fs
	cfgStrict.Locations = []string{"migrations"}
	cfgStrict.IgnoreFutureMigrations = false

	mStrict, err := New(cfgStrict, db)
	if err != nil {
		t.Fatalf("New migrator failed: %v", err)
	}
	_, err = mStrict.Migrate(ctx)
	if err == nil || (!strings.Contains(err.Error(), "Migration V5__five.sql failed in database") && !strings.Contains(err.Error(), "contains a failed migration to version 5")) {
		t.Errorf("expected error refusing failed migration to version 5 when IgnoreFutureMigrations=false, got: %v", err)
	}

	// 3. With IgnoreFutureMigrations=false and ValidateOnMigrate=false: Migrate refuses via history.FailedMigrations check
	cfgStrictNoVal := *cfgStrict
	cfgStrictNoVal.ValidateOnMigrate = false
	mStrictNoVal, _ := New(&cfgStrictNoVal, db)
	_, err = mStrictNoVal.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "contains a failed migration to version 5") {
		t.Errorf("expected error refusing failed migration to version 5 when ValidateOnMigrate=false, got: %v", err)
	}

	// 4. Parity: multiple failed future migrations (e.g. V10 and V11 when local ends at V9)
	// Flyway stops if MORE than 1 failed migration exists in history, even if both are future versions
	fsV9 := fstest.MapFS{
		"migrations/V9__nine.sql": &fstest.MapFile{Data: []byte("SELECT 9;")},
	}
	dbMultiple := mock.NewMockDatabase()
	v9Str := "9"
	v10Str := "10"
	v11Str := "11"
	_ = dbMultiple.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
	_ = dbMultiple.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1, Version: &v9Str, Description: "nine", Type: "SQL", Script: "V9__nine.sql", Success: true,
	})
	_ = dbMultiple.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2, Version: &v10Str, Description: "ten", Type: "SQL", Script: "V10__ten.sql", Success: false,
	})
	_ = dbMultiple.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 3, Version: &v11Str, Description: "eleven", Type: "SQL", Script: "V11__eleven.sql", Success: false,
	})

	cfgMultiple := config.NewDefaultConfiguration()
	cfgMultiple.DefaultSchema = "test_ds"
	cfgMultiple.FS = fsV9
	cfgMultiple.Locations = []string{"migrations"}
	cfgMultiple.IgnoreFutureMigrations = true
	cfgMultiple.ValidateOnMigrate = false

	mMultiple, _ := New(cfgMultiple, dbMultiple)
	_, err = mMultiple.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "contains a failed migration") {
		t.Fatalf("expected Migrate to stop when multiple failed future migrations exist, got: %v", err)
	}

	// 5. Parity: No versioned files resolve locally, exactly 1 failed versioned migration exists in history
	// In Flyway, if no versioned files resolve, any versioned migration is in the future, so 1 failed row warns
	fsNoVersioned := fstest.MapFS{
		"migrations/R__repeatable.sql": &fstest.MapFile{Data: []byte("SELECT 'repeatable';")},
	}
	dbSingleFuture := mock.NewMockDatabase()
	_ = dbSingleFuture.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
	_ = dbSingleFuture.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1, Version: &v10Str, Description: "ten", Type: "SQL", Script: "V10__ten.sql", Success: false,
	})

	cfgNoVersioned := config.NewDefaultConfiguration()
	cfgNoVersioned.DefaultSchema = "test_ds"
	cfgNoVersioned.FS = fsNoVersioned
	cfgNoVersioned.Locations = []string{"migrations"}
	cfgNoVersioned.IgnoreFutureMigrations = true
	cfgNoVersioned.ValidateOnMigrate = false

	mNoVersioned, _ := New(cfgNoVersioned, dbSingleFuture)
	resNoVersioned, err := mNoVersioned.Migrate(ctx)
	if err != nil {
		t.Fatalf("expected Migrate to warn and succeed when 0 versioned files resolve and 1 failed future migration exists, got: %v", err)
	}
	foundWarn := false
	for _, w := range resNoVersioned.Warnings {
		if strings.Contains(w, "failed future migration to version 10") {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Errorf("expected warning about failed future migration to version 10, got warnings: %v", resNoVersioned.Warnings)
	}
	if resNoVersioned.MigrationsExecuted != 1 {
		t.Errorf("expected 1 migration executed (repeatable), got %d", resNoVersioned.MigrationsExecuted)
	}

	// 6. Parity: No versioned files resolve locally, but 2 failed versioned migrations exist
	// Flyway stops because more than 1 failed migration exists
	_ = dbSingleFuture.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2, Version: &v11Str, Description: "eleven", Type: "SQL", Script: "V11__eleven.sql", Success: false,
	})
	mNoVersionedMulti, _ := New(cfgNoVersioned, dbSingleFuture)
	_, err = mNoVersionedMulti.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "contains a failed migration") {
		t.Fatalf("expected Migrate to fail when 0 versioned files resolve and 2 failed migrations exist, got: %v", err)
	}
}

func TestInfo_RepeatableMatchedByDescription(t *testing.T) {
	ctx := context.Background()

	// Local file was renamed from R__seed_old.sql to R__seed_new.sql, but description is identical: 'seed'
	fs := fstest.MapFS{
		"migrations/R__seed_new.sql": &fstest.MapFile{Data: []byte("SELECT 'seed';")},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	db := mock.NewMockDatabase()
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("New migrator failed: %v", err)
	}

	// Resolve local migration to compute its checksum
	resolved, err := m.resolver.Resolve()
	if err != nil || len(resolved.RepeatableMigrations) != 1 {
		t.Fatalf("Resolve failed: %v", err)
	}
	cs := resolved.RepeatableMigrations[0].Checksum

	// Database history has the old script filename with identical description and checksum
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       nil,
		Description:   resolved.RepeatableMigrations[0].Description,
		Type:          "SQL",
		Script:        "R__seed_old.sql",
		Checksum:      &cs,
		Success:       true,
	})

	infoRes, err := m.Info(ctx)
	if err != nil {
		t.Fatalf("Info failed: %v", err)
	}

	if len(infoRes.Migrations) != 1 {
		t.Fatalf("expected 1 migration info item, got %d: %+v", len(infoRes.Migrations), infoRes.Migrations)
	}

	item := infoRes.Migrations[0]
	if item.State != resolver.StateSuccess {
		t.Errorf("expected repeatable migration to be recognized as StateSuccess by description, got state=%s", item.State)
	}
}

func TestScriptMigration_PhysicalFilePreservesExecBit(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	scriptPath := filepath.Join(tmpDir, "V1__script.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.Locations = []string{"filesystem:" + tmpDir}

	db := mock.NewMockDatabase()
	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("New migrator failed: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if res.MigrationsExecuted != 1 {
		t.Errorf("expected 1 migration executed, got %d", res.MigrationsExecuted)
	}

	fi, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("failed to stat script: %v", err)
	}
	if fi.Mode().Perm()&0111 == 0 {
		t.Errorf("expected exec bit to be preserved on user file, got permissions: %v", fi.Mode().Perm())
	}
}

func TestScriptMigration_ShellVariablesExecuteWithoutPlaceholderError(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	scriptPath := filepath.Join(tmpDir, "V1__shell_var.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho \"HOME=${HOME}\"\nexit 0\n"), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.Locations = []string{"filesystem:" + tmpDir}
	cfg.PlaceholderReplacement = true

	db := mock.NewMockDatabase()
	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("New migrator failed: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("expected script migration with ${HOME} to succeed, got: %v", err)
	}
	if res.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
	}
}

func TestStandaloneClean_TakesLock(t *testing.T) {
	ctx := context.Background()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.CleanDisabled = false

	db := mock.NewMockDatabase()
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "user_table")

	// 1. If database is already locked by another process, Clean() must fail
	unlock, err := db.Lock(ctx, "test_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("setup lock failed: %v", err)
	}

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("New migrator failed: %v", err)
	}

	_, err = m.Clean(ctx)
	if err == nil {
		t.Fatalf("expected Clean to fail when database is locked, got nil")
	}
	if !strings.Contains(err.Error(), "failed to acquire database lock for clean") {
		t.Errorf("expected lock error message, got: %v", err)
	}

	// Release lock
	_ = unlock(ctx)

	// 2. Standalone clean should succeed, acquiring lock and dropping tables
	cleanRes, err := m.Clean(ctx)
	if err != nil {
		t.Fatalf("Clean failed: %v", err)
	}
	if len(cleanRes.SchemasCleaned) != 1 {
		t.Errorf("expected 1 schema cleaned, got %d", len(cleanRes.SchemasCleaned))
	}

	// Verify history table is dropped
	histExists, _ := db.HistoryTableExists(ctx, "test_ds", "flyway_schema_history")
	if histExists {
		t.Errorf("expected history table to be dropped by standalone clean")
	}

	// 3. A waiting process fails because the table does not exist and it does not migrate into a schema that is being cleaned
	_, lockErr := db.Lock(ctx, "test_ds", "flyway_schema_history")
	if lockErr == nil {
		t.Fatalf("expected lock attempt to fail because history table was dropped, got nil")
	}
}

type unlockCheckMockDB struct {
	*mock.MockDatabase
	unlockCalled bool
	failOnUnlock error
}

func (u *unlockCheckMockDB) Lock(ctx context.Context, schema, table string) (database.UnlockFunc, error) {
	unl, err := u.MockDatabase.Lock(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		u.unlockCalled = true
		_ = unl(ctx)
		if u.failOnUnlock != nil {
			return u.failOnUnlock
		}
		return nil
	}, nil
}

func TestStandaloneClean_CallsUnlock(t *testing.T) {
	ctx := context.Background()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.CleanDisabled = false

	baseDB := mock.NewMockDatabase()
	_ = baseDB.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
	_ = baseDB.EnsureHistoryTable(ctx, "test_ds", "user_table")

	wrapped := &unlockCheckMockDB{
		MockDatabase: baseDB,
	}

	m, err := New(cfg, wrapped)
	if err != nil {
		t.Fatalf("New migrator failed: %v", err)
	}

	res, err := m.Clean(ctx)
	if err != nil {
		t.Fatalf("Clean failed: %v", err)
	}
	if !wrapped.unlockCalled {
		t.Errorf("expected unlock to be called by Clean after history table dropped")
	}
	if len(res.SchemasCleaned) != 1 {
		t.Errorf("expected 1 schema cleaned, got %d", len(res.SchemasCleaned))
	}
}

func TestStandaloneClean_PropagatesUnlockError(t *testing.T) {
	ctx := context.Background()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.CleanDisabled = false

	baseDB := mock.NewMockDatabase()
	_ = baseDB.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	expectedErr := fmt.Errorf("lock heartbeat failed: lock lost")
	wrapped := &unlockCheckMockDB{
		MockDatabase: baseDB,
		failOnUnlock: expectedErr,
	}

	m, err := New(cfg, wrapped)
	if err != nil {
		t.Fatalf("New migrator failed: %v", err)
	}

	_, err = m.Clean(ctx)
	if err == nil || !strings.Contains(err.Error(), "lock heartbeat failed") {
		t.Fatalf("expected unlock error to be propagated by Clean, got %v", err)
	}
	if !wrapped.unlockCalled {
		t.Errorf("expected unlock to be called by Clean")
	}
}

