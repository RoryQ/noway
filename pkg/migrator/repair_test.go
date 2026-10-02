package migrator

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/RoryQ/noway/pkg/checksum"
	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database"
	"github.com/RoryQ/noway/pkg/database/mock"
)

func TestRepairRepeatableChecksumAlignment(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT 1;"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// 1. Initial Migrate
	migRes, err := m.Migrate(ctx)
	if err != nil || migRes.MigrationsExecuted != 1 {
		t.Fatalf("initial Migrate failed: %v, exec=%d", err, migRes.MigrationsExecuted)
	}

	history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if err != nil || len(history) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(history))
	}
	initialCs := *history[0].Checksum

	// 2. Modify repeatable script locally
	fs["migrations/R__view.sql"] = &fstest.MapFile{
		Data: []byte("CREATE VIEW v AS SELECT 2;"),
	}
	expectedCs, _ := checksum.CalculateString("CREATE VIEW v AS SELECT 2;")

	// 3. Run Repair
	repairRes, err := m.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if len(repairRes.AlignedChecksums) != 1 {
		t.Errorf("expected 1 aligned checksum, got %d", len(repairRes.AlignedChecksums))
	}

	// 4. Verify updated checksum in database
	historyAfter, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if err != nil || len(historyAfter) != 1 {
		t.Fatalf("FetchHistory failed, len=%d", len(historyAfter))
	}
	if *historyAfter[0].Checksum == initialCs {
		t.Errorf("expected checksum to change from %d, but remained identical", initialCs)
	}
	if *historyAfter[0].Checksum != expectedCs {
		t.Errorf("expected checksum %d, got %d", expectedCs, *historyAfter[0].Checksum)
	}

	// 5. Subsequent Migrate must not re-run because checksums are aligned
	m2, _ := New(cfg, db)
	migRes2, err := m2.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
	if migRes2.MigrationsExecuted != 0 {
		t.Errorf("expected 0 migrations executed after repair alignment, got %d", migRes2.MigrationsExecuted)
	}
}

func TestRepairMarksMissingVersionedMigrationAsDeleted(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"migrations/.keep": &fstest.MapFile{
			Data: []byte("keep dir"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// 1. Initial Migrate
	_, err = m.Migrate(ctx)
	if err != nil {
		t.Fatalf("initial Migrate failed: %v", err)
	}

	// 2. Remove V1 from disk
	delete(fs, "migrations/V1__init.sql")

	// 3. Before repair, Validate fails because applied migration is missing locally
	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if valRes.Valid {
		t.Errorf("expected Validate to fail when applied migration is missing on disk")
	}

	// 4. Run Repair
	repairRes, err := m.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if len(repairRes.DeletedMigrations) != 1 || repairRes.DeletedMigrations[0] != "V1__init.sql" {
		t.Errorf("expected DeletedMigrations to contain V1__init.sql, got %+v", repairRes.DeletedMigrations)
	}

	// 5. Verify record has type DELETE in history
	history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if err != nil || len(history) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(history))
	}
	if history[0].Type != "DELETE" {
		t.Errorf("expected history record type 'DELETE', got %q", history[0].Type)
	}

	// 6. After repair, Validate should succeed because DELETE records are ignored (F14)
	valRes2, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate after repair failed: %v", err)
	}
	if !valRes2.Valid {
		t.Errorf("expected Validate to succeed after repair marked record as DELETE, got errors: %s", valRes2.Error())
	}
}

func TestRepairMarksMissingRepeatableMigrationAsDeleted(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT 1;"),
		},
		"migrations/.keep": &fstest.MapFile{
			Data: []byte("keep dir"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// 1. Initial Migrate
	_, err = m.Migrate(ctx)
	if err != nil {
		t.Fatalf("initial Migrate failed: %v", err)
	}

	// 2. Remove R__view.sql from disk
	delete(fs, "migrations/R__view.sql")

	// 3. Run Repair
	repairRes, err := m.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if len(repairRes.DeletedMigrations) != 1 || repairRes.DeletedMigrations[0] != "R__view.sql" {
		t.Errorf("expected DeletedMigrations to contain R__view.sql, got %+v", repairRes.DeletedMigrations)
	}

	// 4. Verify record in history has type DELETE
	history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if err != nil || len(history) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(history))
	}
	if history[0].Type != "DELETE" {
		t.Errorf("expected history record type 'DELETE', got %q", history[0].Type)
	}

	// 5. Validate after repair succeeds
	valRes, err := m.Validate(ctx)
	if err != nil || !valRes.Valid {
		t.Errorf("expected Validate to succeed after repair marked repeatable as DELETE, err=%v", err)
	}
}

func TestRepairRemovesFailedMigrationRecord(t *testing.T) {
	ctx := context.Background()

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	v1Str := "1"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "failing",
		Type:          "SQL",
		Script:        "V1__failing.sql",
		Success:       false,
	})

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fstest.MapFS{
		"migrations/V1__failing.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	}
	cfg.Locations = []string{"migrations"}

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	repairRes, err := m.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if len(repairRes.RemovedFailed) != 1 || repairRes.RemovedFailed[0] != "V1__failing.sql" {
		t.Errorf("expected RemovedFailed to contain V1__failing.sql, got %+v", repairRes.RemovedFailed)
	}

	history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory failed: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("expected 0 history records after removing failed migration, got %d", len(history))
	}
}
