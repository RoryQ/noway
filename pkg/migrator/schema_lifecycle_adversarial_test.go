package migrator_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/database/mock"
	"github.com/roryq/noway/pkg/migrator"
	"github.com/roryq/noway/pkg/resolver"
)

// TestMigrate_FreshEmptyDatabase_BaselineOnMigrateDoesNotSkipV1 verifies that when
// running Migrate() on a completely empty database with baselineOnMigrate=true,
// the empty database is NOT falsely baselined (skipping V1), but instead executes V1.
func TestMigrate_FreshEmptyDatabase_BaselineOnMigrateDoesNotSkipV1(t *testing.T) {
	ctx := context.Background()
	mockFS := fstest.MapFS{
		"migrations/V1__initial_schema.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V2__add_column.sql": &fstest.MapFile{
			Data: []byte("ALTER TABLE users ADD COLUMN name STRING;"),
		},
		"migrations/R__users_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW users_v AS SELECT id FROM users;"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "fresh_dataset"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.BaselineOnMigrate = true
	cfg.BaselineVersion = "1"

	db := mock.NewMockDatabase()
	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	if !res.Success {
		t.Fatalf("Migrate reported failure")
	}

	// Should execute V1, V2, and R (3 migrations total), NOT baseline at V1 and only run V2+R
	if res.MigrationsExecuted != 3 {
		t.Fatalf("expected 3 migrations executed on fresh database with baselineOnMigrate=true, got %d", res.MigrationsExecuted)
	}

	if res.TargetVersion != "2" {
		t.Fatalf("expected TargetVersion '2', got %s", res.TargetVersion)
	}

	history, err := db.FetchHistory(ctx, "fresh_dataset", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory failed: %v", err)
	}

	if len(history) != 3 {
		t.Fatalf("expected 3 history records, got %d", len(history))
	}

	if history[0].Version.String() != "1" || history[0].Type != "SQL" {
		t.Errorf("expected history[0] to be V1 SQL migration, got: %+v", history[0])
	}
	if history[1].Version.String() != "2" || history[1].Type != "SQL" {
		t.Errorf("expected history[1] to be V2 SQL migration, got: %+v", history[1])
	}
	if history[2].Version != nil || history[2].Script != "R__users_view.sql" {
		t.Errorf("expected history[2] to be repeatable migration, got: %+v", history[2])
	}
}

// TestMigrate_NonEmptyUnmanagedDatabase_BaselineOnMigrateSkipsV1 verifies that when
// running Migrate() on a database containing pre-existing user tables with baselineOnMigrate=true,
// it baselines at baselineVersion and applies only subsequent migrations.
func TestMigrate_NonEmptyUnmanagedDatabase_BaselineOnMigrateSkipsV1(t *testing.T) {
	ctx := context.Background()
	mockFS := fstest.MapFS{
		"migrations/V1__initial_schema.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE legacy_table (id INT64);"),
		},
		"migrations/V2__add_new_feature.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE new_feature (id INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "legacy_dataset"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.BaselineOnMigrate = true
	cfg.BaselineVersion = "1"
	cfg.BaselineDescription = "Base migration"

	db := mock.NewMockDatabase()
	// Ensure schema exists and has a pre-existing table (non-empty database)
	_ = db.EnsureSchema(ctx, "legacy_dataset")
	_ = db.EnsureHistoryTable(ctx, "legacy_dataset", "pre_existing_table")

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Should baseline at V1 and execute only V2
	if res.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed (V2), got %d", res.MigrationsExecuted)
	}
	if res.TargetVersion != "2" {
		t.Fatalf("expected TargetVersion '2', got %s", res.TargetVersion)
	}

	history, err := db.FetchHistory(ctx, "legacy_dataset", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory failed: %v", err)
	}

	if len(history) != 2 {
		t.Fatalf("expected 2 history records (BASELINE + V2), got %d: %+v", len(history), history)
	}

	if history[0].Type != "BASELINE" || history[0].Version.String() != "1" {
		t.Errorf("expected history[0] to be BASELINE at version 1, got: %+v", history[0])
	}
	if history[1].Type != "SQL" || history[1].Version.String() != "2" {
		t.Errorf("expected history[1] to be V2 SQL, got: %+v", history[1])
	}
}

// TestMigrate_MissingSchema_CreateSchemasFalse_FailsAndCallsAfterMigrateError verifies
// that when the schema does not exist and createSchemas=false, Migrate fails and invokes afterMigrateError.
func TestMigrate_MissingSchema_CreateSchemasFalse_FailsAndCallsAfterMigrateError(t *testing.T) {
	ctx := context.Background()
	var callbackEvents []string

	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t (id INT64);"),
		},
		"migrations/afterMigrateError__log.sql": &fstest.MapFile{
			Data: []byte("SELECT 'error_handled';"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "non_existent_dataset"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.CreateSchemas = false

	db := mock.NewMockDatabase()
	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create migrator: %v", err)
	}

	_, err = m.Migrate(ctx)
	if err == nil {
		t.Fatalf("expected Migrate to fail when schema does not exist and createSchemas=false")
	}

	if !strings.Contains(err.Error(), "does not exist and createSchemas is false") {
		t.Errorf("expected error mentioning createSchemas is false, got: %v", err)
	}

	executedStmts := db.ExecutedStatements()
	foundErrorCallback := false
	for _, stmt := range executedStmts {
		if strings.Contains(stmt, "error_handled") {
			foundErrorCallback = true
			break
		}
	}
	if !foundErrorCallback {
		t.Errorf("expected afterMigrateError callback to be executed, executed stmts: %v", executedStmts)
	}
	_ = callbackEvents
}

// TestMigrate_MissingSchema_CreateSchemasTrue_ExecutesLifecycleCallbacks verifies
// that when a missing schema is created with createSchemas=true, all schema creation callbacks fire in order.
func TestMigrate_MissingSchema_CreateSchemasTrue_ExecutesLifecycleCallbacks(t *testing.T) {
	ctx := context.Background()
	mockFS := fstest.MapFS{
		"migrations/beforeCreateSchema__1.sql": &fstest.MapFile{
			Data: []byte("SELECT 'beforeCreateSchema';"),
		},
		"migrations/createSchema__2.sql": &fstest.MapFile{
			Data: []byte("SELECT 'createSchema';"),
		},
		"migrations/afterCreateSchema__3.sql": &fstest.MapFile{
			Data: []byte("SELECT 'afterCreateSchema';"),
		},
		"migrations/beforeMigrate__4.sql": &fstest.MapFile{
			Data: []byte("SELECT 'beforeMigrate';"),
		},
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/afterMigrate__5.sql": &fstest.MapFile{
			Data: []byte("SELECT 'afterMigrate';"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "brand_new_schema"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.CreateSchemas = true

	db := mock.NewMockDatabase()
	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	if !res.Success || res.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed successfully, got %d (success=%v)", res.MigrationsExecuted, res.Success)
	}

	// Verify schema exists
	exists, err := db.SchemaExists(ctx, "brand_new_schema")
	if err != nil || !exists {
		t.Fatalf("expected schema brand_new_schema to exist")
	}

	// Verify statement execution order
	stmts := db.ExecutedStatements()
	expectedCallbacks := []string{"beforeCreateSchema", "createSchema", "afterCreateSchema", "beforeMigrate", "CREATE TABLE users", "afterMigrate"}
	stmtIdx := 0

	for _, expected := range expectedCallbacks {
		found := false
		for i := stmtIdx; i < len(stmts); i++ {
			if strings.Contains(stmts[i], expected) {
				found = true
				stmtIdx = i + 1
				break
			}
		}
		if !found {
			t.Errorf("expected statement '%s' in order, but not found after previous statements. All stmts: %v", expected, stmts)
		}
	}
}

// TestMigrate_BeforeMigrateRunsBeforeValidation verifies that beforeMigrate runs before
// ValidateOnMigrate, and if validation fails, afterMigrateError is triggered.
func TestMigrate_BeforeMigrateRunsBeforeValidation(t *testing.T) {
	ctx := context.Background()
	mockFS := fstest.MapFS{
		"migrations/beforeMigrate__1.sql": &fstest.MapFile{
			Data: []byte("SELECT 'beforeMigrate_hook';"),
		},
		"migrations/afterMigrateError__2.sql": &fstest.MapFile{
			Data: []byte("SELECT 'afterMigrateError_hook';"),
		},
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE v1 (id INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "val_fail_ds"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.ValidateOnMigrate = true

	db := mock.NewMockDatabase()
	// Simulate an applied migration in history with corrupted checksum to cause validation failure
	_ = db.EnsureSchema(ctx, "val_fail_ds")
	_ = db.EnsureHistoryTable(ctx, "val_fail_ds", "flyway_schema_history")
	wrongChecksum := int64(99999999)
	v1Str := "1"
	_ = db.InsertHistory(ctx, "val_fail_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "init",
		Type:          "SQL",
		Script:        "V1__init.sql",
		Checksum:      &wrongChecksum,
		Success:       true,
	})

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create migrator: %v", err)
	}

	_, err = m.Migrate(ctx)
	if err == nil {
		t.Fatalf("expected Migrate to fail due to checksum validation error")
	}

	stmts := db.ExecutedStatements()
	foundBeforeMigrate := false
	foundAfterMigrateError := false

	for _, s := range stmts {
		if strings.Contains(s, "beforeMigrate_hook") {
			foundBeforeMigrate = true
		}
		if strings.Contains(s, "afterMigrateError_hook") {
			foundAfterMigrateError = true
		}
	}

	if !foundBeforeMigrate {
		t.Errorf("expected beforeMigrate hook to have executed before validation failure, stmts: %v", stmts)
	}
	if !foundAfterMigrateError {
		t.Errorf("expected afterMigrateError hook to have executed after validation failure, stmts: %v", stmts)
	}
}

// TestInfo_SchemaHistoryWithSchemaRecord verifies that Info() renders SCHEMA type records
// with Category 'Schema' and State 'Success', without misidentifying them as repeatable migrations.
func TestInfo_SchemaHistoryWithSchemaRecord(t *testing.T) {
	ctx := context.Background()
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW users_v AS SELECT id FROM users;"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema_ds"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_schema_ds")
	_ = db.EnsureHistoryTable(ctx, "test_schema_ds", "flyway_schema_history")

	// Insert SCHEMA record
	_ = db.InsertHistory(ctx, "test_schema_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       nil,
		Description:   "<< Flyway Schema Creation >>",
		Type:          "SCHEMA",
		Script:        "test_schema_ds",
		Checksum:      nil,
		InstalledBy:   "flyway_user",
		Success:       true,
	})

	// Insert V1 record
	v1Str := "1"
	cs := int64(12345)
	_ = db.InsertHistory(ctx, "test_schema_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2,
		Version:       &v1Str,
		Description:   "init",
		Type:          "SQL",
		Script:        "V1__init.sql",
		Checksum:      &cs,
		InstalledBy:   "flyway_user",
		Success:       true,
	})

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create migrator: %v", err)
	}

	infoRes, err := m.Info(ctx)
	if err != nil {
		t.Fatalf("Info failed: %v", err)
	}

	if len(infoRes.Migrations) != 3 {
		t.Fatalf("expected 3 items in Info (SCHEMA, V1, R), got %d", len(infoRes.Migrations))
	}

	schemaItem := infoRes.Migrations[0]
	if schemaItem.Type != "SCHEMA" || schemaItem.State != resolver.StateSuccess || schemaItem.Version != nil {
		t.Errorf("unexpected schemaItem: %+v", schemaItem)
	}

	// Verify FlywayFormat categories
	if len(infoRes.FlywayFormat) != 3 {
		t.Fatalf("expected 3 FlywayFormat items, got %d", len(infoRes.FlywayFormat))
	}

	if infoRes.FlywayFormat[0].Category != "Schema" {
		t.Errorf("expected Category 'Schema', got '%s'", infoRes.FlywayFormat[0].Category)
	}
	if infoRes.FlywayFormat[1].Category != "Versioned" {
		t.Errorf("expected Category 'Versioned', got '%s'", infoRes.FlywayFormat[1].Category)
	}
	if infoRes.FlywayFormat[2].Category != "Repeatable" {
		t.Errorf("expected Category 'Repeatable', got '%s'", infoRes.FlywayFormat[2].Category)
	}

	tableStr := infoRes.StringTable()
	if !strings.Contains(tableStr, "Schema") {
		t.Errorf("expected rendered table to include 'Schema' category, got:\n%s", tableStr)
	}
}

// TestMigrate_NonEmptySchemaWithoutHistoryTable_BaselineOnMigrateFalse_FailsWithFlywayError
// verifies that if a non-empty schema exists with no history table and baselineOnMigrate=false,
// Migrate fails with the exact Flyway error message.
func TestMigrate_NonEmptySchemaWithoutHistoryTable_BaselineOnMigrateFalse_FailsWithFlywayError(t *testing.T) {
	ctx := context.Background()
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "existing_schema"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.BaselineOnMigrate = false

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "existing_schema")
	_ = db.EnsureHistoryTable(ctx, "existing_schema", "user_table") // simulates pre-existing table

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create migrator: %v", err)
	}

	_, err = m.Migrate(ctx)
	if err == nil {
		t.Fatalf("expected Migrate to fail on non-empty schema with baselineOnMigrate=false")
	}

	expectedSubstr := "Found non-empty schema(s) existing_schema but no schema history table"
	if !strings.Contains(err.Error(), expectedSubstr) {
		t.Errorf("expected error containing '%s', got: %v", expectedSubstr, err)
	}
}

// TestMigrate_MultipleSchemas_BaselineOnMigrate_ChecksAllSchemas
// verifies that all configured schemas are checked for emptiness when deciding to baseline.
func TestMigrate_MultipleSchemas_BaselineOnMigrate_ChecksAllSchemas(t *testing.T) {
	ctx := context.Background()
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V2__add_t.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t (id INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "schema_a"
	cfg.Schemas = []string{"schema_a", "schema_b"}
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.BaselineOnMigrate = true
	cfg.BaselineVersion = "1"

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "schema_a") // empty
	_ = db.EnsureSchema(ctx, "schema_b")
	_ = db.EnsureHistoryTable(ctx, "schema_b", "legacy_b_table") // non-empty schema_b

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("Failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Since schema_b is non-empty, baselineOnMigrate should have triggered and only V2 executed
	if res.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed after baseline (V2), got %d", res.MigrationsExecuted)
	}
	if res.TargetVersion != "2" {
		t.Fatalf("expected TargetVersion '2', got %s", res.TargetVersion)
	}
}
