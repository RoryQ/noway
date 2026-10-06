package migrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/database/mock"
	"github.com/roryq/noway/pkg/migrator"
)

func TestMigrate_DryRunOutput_GeneratesValidScriptAndDoesNotTouchDatabase(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dryRunFile := filepath.Join(tempDir, "dryrun_migrate.sql")

	mockFS := fstest.MapFS{
		"migrations/V1__create_users.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${schema}.users (id INT64, name STRING);"),
		},
		"migrations/V2__create_orders.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${schema}.orders (id INT64, user_id INT64, amount FLOAT64);"),
		},
		"migrations/R__users_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW ${schema}.v_users AS SELECT id, name FROM ${schema}.users;"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "dry_run_ds"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.DryRunOutput = dryRunFile
	cfg.Placeholders = map[string]string{
		"schema": "dry_run_ds",
	}

	db := mock.NewMockDatabase()
	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create Migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate dry run failed: %v", err)
	}

	if !res.Success {
		t.Errorf("expected dry run result success=true")
	}
	if res.MigrationsExecuted != 3 {
		t.Errorf("expected 3 migrations in dry run, got %d", res.MigrationsExecuted)
	}
	if res.TargetVersion != "2" {
		t.Errorf("expected TargetVersion '2', got %s", res.TargetVersion)
	}

	// 1. Verify database is completely UNTOUCHED
	schemaExists, _ := db.SchemaExists(ctx, "dry_run_ds")
	if schemaExists {
		t.Errorf("expected schema to not exist in database during dry run")
	}
	tableExists, _ := db.HistoryTableExists(ctx, "dry_run_ds", "flyway_schema_history")
	if tableExists {
		t.Errorf("expected history table to not exist in database during dry run")
	}
	history, _ := db.FetchHistory(ctx, "dry_run_ds", "flyway_schema_history")
	if len(history) != 0 {
		t.Errorf("expected 0 history records in database, got %d", len(history))
	}
	if len(db.ExecutedStatements()) != 0 {
		t.Errorf("expected 0 executed statements in database, got: %v", db.ExecutedStatements())
	}

	// 2. Verify dry run output file content
	data, err := os.ReadFile(dryRunFile)
	if err != nil {
		t.Fatalf("failed to read dry run output file: %v", err)
	}
	sqlContent := string(data)

	// Check headers
	if !strings.Contains(sqlContent, "-- Flyway / Noway Dry Run Output (migrate)") {
		t.Errorf("missing dry run header in output")
	}
	if !strings.Contains(sqlContent, "-- Schema: dry_run_ds") {
		t.Errorf("missing schema header in output")
	}

	// Check table creation
	if !strings.Contains(sqlContent, "CREATE TABLE IF NOT EXISTS `dry_run_ds`.`flyway_schema_history`") {
		t.Errorf("missing history table creation in output:\n%s", sqlContent)
	}

	// Check placeholder replacement
	if !strings.Contains(sqlContent, "CREATE TABLE dry_run_ds.users") {
		t.Errorf("placeholder ${schema} was not replaced in V1:\n%s", sqlContent)
	}
	if !strings.Contains(sqlContent, "CREATE TABLE dry_run_ds.orders") {
		t.Errorf("placeholder ${schema} was not replaced in V2:\n%s", sqlContent)
	}
	if !strings.Contains(sqlContent, "CREATE VIEW dry_run_ds.v_users") {
		t.Errorf("placeholder ${schema} was not replaced in R:\n%s", sqlContent)
	}

	// Check history table inserts
	if !strings.Contains(sqlContent, "INSERT INTO `dry_run_ds`.`flyway_schema_history`") {
		t.Errorf("missing history table insert in output:\n%s", sqlContent)
	}
	if !strings.Contains(sqlContent, "'V1__create_users.sql'") {
		t.Errorf("missing V1 history record insert:\n%s", sqlContent)
	}
	if !strings.Contains(sqlContent, "'V2__create_orders.sql'") {
		t.Errorf("missing V2 history record insert:\n%s", sqlContent)
	}
	if !strings.Contains(sqlContent, "'R__users_view.sql'") {
		t.Errorf("missing R history record insert:\n%s", sqlContent)
	}
}

func TestMigrate_DryRunOutput_WithBaselineOnMigrate(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dryRunFile := filepath.Join(tempDir, "dryrun_baseline.sql")

	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V2__add_t.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE orders (id INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "legacy_ds"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.BaselineOnMigrate = true
	cfg.BaselineVersion = "1"
	cfg.BaselineDescription = "Initial Baseline"
	cfg.DryRunOutput = dryRunFile

	db := mock.NewMockDatabase()
	// Pre-existing table in schema (unmanaged non-empty DB)
	_ = db.EnsureSchema(ctx, "legacy_ds")
	_ = db.EnsureHistoryTable(ctx, "legacy_ds", "pre_existing_table")

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create Migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate dry run failed: %v", err)
	}

	// Should baseline at V1 and plan V2
	if res.MigrationsExecuted != 1 {
		t.Errorf("expected 1 migration in dry run (V2), got %d", res.MigrationsExecuted)
	}
	if res.TargetVersion != "2" {
		t.Errorf("expected TargetVersion '2', got %s", res.TargetVersion)
	}

	data, err := os.ReadFile(dryRunFile)
	if err != nil {
		t.Fatalf("failed to read dry run file: %v", err)
	}
	sql := string(data)

	if !strings.Contains(sql, "-- Baseline schema history") {
		t.Errorf("missing baseline header in output:\n%s", sql)
	}
	if !strings.Contains(sql, "'Initial Baseline'") {
		t.Errorf("missing baseline description in output:\n%s", sql)
	}
	if !strings.Contains(sql, "CREATE TABLE orders (id INT64);") {
		t.Errorf("missing V2 migration SQL in output:\n%s", sql)
	}
	if strings.Contains(sql, "CREATE TABLE users (id INT64);") {
		t.Errorf("V1 should not be in output because it was baselined:\n%s", sql)
	}
}

func TestUndo_DryRunOutput_GeneratesUndoScriptAndDoesNotTouchDatabase(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dryRunFile := filepath.Join(tempDir, "dryrun_undo.sql")

	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/U1__undo_init.sql": &fstest.MapFile{
			Data: []byte("DROP TABLE ${schema}.users;"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "undo_ds"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.DryRunOutput = dryRunFile
	cfg.Placeholders = map[string]string{
		"schema": "undo_ds",
	}

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "undo_ds")
	_ = db.EnsureHistoryTable(ctx, "undo_ds", "flyway_schema_history")
	v1Str := "1"
	cs := int64(12345)
	_ = db.InsertHistory(ctx, "undo_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "init",
		Type:          "SQL",
		Script:        "V1__init.sql",
		Checksum:      &cs,
		Success:       true,
	})

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create Migrator: %v", err)
	}

	undoRes, err := m.Undo(ctx)
	if err != nil {
		t.Fatalf("Undo dry run failed: %v", err)
	}

	if undoRes.UndoneVersion != "1" || undoRes.Script != "U1__undo_init.sql" {
		t.Errorf("unexpected undo result: %+v", undoRes)
	}

	// 1. Verify database was NOT changed
	history, _ := db.FetchHistory(ctx, "undo_ds", "flyway_schema_history")
	if len(history) != 1 {
		t.Errorf("history record should not be deleted from DB during dry run, got len %d", len(history))
	}
	if len(db.ExecutedStatements()) != 0 {
		t.Errorf("expected 0 executed statements in DB during dry run, got: %v", db.ExecutedStatements())
	}

	// 2. Verify dry run output file
	data, err := os.ReadFile(dryRunFile)
	if err != nil {
		t.Fatalf("failed to read dry run file: %v", err)
	}
	sql := string(data)

	if !strings.Contains(sql, "-- Flyway / Noway Dry Run Output (undo)") {
		t.Errorf("missing undo header in output:\n%s", sql)
	}
	if !strings.Contains(sql, "DROP TABLE undo_ds.users;") {
		t.Errorf("missing replaced undo SQL in output:\n%s", sql)
	}
	if !strings.Contains(sql, "DELETE FROM `undo_ds`.`flyway_schema_history` WHERE `installed_rank` = 1;") {
		t.Errorf("missing history table delete statement in output:\n%s", sql)
	}
}

func TestMigrate_RealExecutionAfterDryRun_SucceedsCleanly(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dryRunFile := filepath.Join(tempDir, "dryrun_migrate.sql")

	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V2__add_col.sql": &fstest.MapFile{
			Data: []byte("ALTER TABLE users ADD COLUMN name STRING;"),
		},
	}

	db := mock.NewMockDatabase()

	// 1. Run Dry Run first
	cfgDry := config.NewDefaultConfiguration()
	cfgDry.DefaultSchema = "exec_after_dry_ds"
	cfgDry.FS = mockFS
	cfgDry.Locations = []string{"migrations"}
	cfgDry.DryRunOutput = dryRunFile

	mDry, _ := migrator.New(cfgDry, db)
	resDry, err := mDry.Migrate(ctx)
	if err != nil {
		t.Fatalf("Dry run failed: %v", err)
	}
	if resDry.MigrationsExecuted != 2 {
		t.Errorf("expected 2 planned migrations in dry run, got %d", resDry.MigrationsExecuted)
	}

	// Database is still untouched
	history, _ := db.FetchHistory(ctx, "exec_after_dry_ds", "flyway_schema_history")
	if len(history) != 0 {
		t.Fatalf("expected 0 history records after dry run, got %d", len(history))
	}

	// 2. Run Real Migration
	cfgReal := config.NewDefaultConfiguration()
	cfgReal.DefaultSchema = "exec_after_dry_ds"
	cfgReal.FS = mockFS
	cfgReal.Locations = []string{"migrations"}
	cfgReal.DryRunOutput = "" // Live execution

	mReal, _ := migrator.New(cfgReal, db)
	resReal, err := mReal.Migrate(ctx)
	if err != nil {
		t.Fatalf("Real migrate failed: %v", err)
	}
	if resReal.MigrationsExecuted != 2 {
		t.Errorf("expected 2 migrations executed in real run, got %d", resReal.MigrationsExecuted)
	}

	// Database now has both migrations applied
	historyAfter, err := db.FetchHistory(ctx, "exec_after_dry_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory failed: %v", err)
	}
	if len(historyAfter) != 2 {
		t.Fatalf("expected 2 history records after real migration, got %d", len(historyAfter))
	}
	if historyAfter[0].Version.String() != "1" || historyAfter[1].Version.String() != "2" {
		t.Errorf("unexpected history records: %+v", historyAfter)
	}
}
