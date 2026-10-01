package migrator

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database/mock"
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
