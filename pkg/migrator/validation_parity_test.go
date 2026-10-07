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

	t.Run("IgnorePendingMigrations_True_Passes", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.IgnorePendingMigrations = true

		m, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valRes.Valid {
			t.Fatalf("expected Validate to pass with IgnorePendingMigrations=true, got: %s", valRes.Error())
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

	// 2. Modify U1__init.sql content on disk -> Validate should fail with checksum mismatch
	fs["migrations/U1__init.sql"] = &fstest.MapFile{Data: []byte("DROP TABLE t1; -- modified")}
	mModified, _ := migrator.New(cfg, db)
	valRes2, err := mModified.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if valRes2.Valid {
		t.Fatalf("expected Validate to fail on modified undo migration checksum")
	}
	if !strings.Contains(valRes2.Error(), "Migration checksum mismatch for undo migration version 1") {
		t.Errorf("unexpected error message: %s", valRes2.Error())
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
