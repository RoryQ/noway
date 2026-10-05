package migrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/roryq/noway/pkg/checksum"
	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/database/mock"
)

// -----------------------------------------------------------------------------
// ADVERSARIAL TEST 1: REPEATABLE CHECKSUM REALIGNMENT
// -----------------------------------------------------------------------------

func TestChallenger_RepairRepeatableChecksumRealignment_SingleAndMultiple(t *testing.T) {
	ctx := context.Background()

	t.Run("SingleRepeatable_ModifiedDisk_AlignedInHistory_NoReexecution", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/R__view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW v AS SELECT 100;"),
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

		// Initial Migrate
		res, err := m.Migrate(ctx)
		if err != nil || res.MigrationsExecuted != 1 {
			t.Fatalf("initial Migrate failed: %v, executed=%d", err, res.MigrationsExecuted)
		}

		origHistory, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if err != nil || len(origHistory) != 1 {
			t.Fatalf("FetchHistory failed, len=%d", len(origHistory))
		}
		origCs := *origHistory[0].Checksum

		// Modify script on disk
		newSQL := "CREATE VIEW v AS SELECT 200;"
		fs["migrations/R__view.sql"] = &fstest.MapFile{
			Data: []byte(newSQL),
		}
		expectedNewCs, _ := checksum.CalculateString(newSQL)

		// Run Repair
		repairRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.AlignedChecksums) != 1 {
			t.Errorf("expected 1 aligned checksum, got %+v", repairRes.AlignedChecksums)
		}

		// Verify history table has updated checksum
		afterHistory, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if err != nil || len(afterHistory) != 1 {
			t.Fatalf("FetchHistory after repair failed, len=%d", len(afterHistory))
		}
		if *afterHistory[0].Checksum != expectedNewCs {
			t.Errorf("expected aligned checksum %d, got %d (orig was %d)", expectedNewCs, *afterHistory[0].Checksum, origCs)
		}

		// Subsequent Migrate must NOT re-execute
		migRes2, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate after repair failed: %v", err)
		}
		if migRes2.MigrationsExecuted != 0 {
			t.Errorf("expected 0 migrations executed after repair alignment, got %d", migRes2.MigrationsExecuted)
		}
	})

	t.Run("MultipleRepeatables_SelectiveModification_ExactTargeting", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/R__view1.sql": &fstest.MapFile{Data: []byte("CREATE VIEW v1 AS SELECT 1;")},
			"migrations/R__view2.sql": &fstest.MapFile{Data: []byte("CREATE VIEW v2 AS SELECT 2;")},
			"migrations/R__view3.sql": &fstest.MapFile{Data: []byte("CREATE VIEW v3 AS SELECT 3;")},
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

		res, err := m.Migrate(ctx)
		if err != nil || res.MigrationsExecuted != 3 {
			t.Fatalf("initial Migrate failed: %v, executed=%d", err, res.MigrationsExecuted)
		}

		origHistory, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		var v2Cs int64
		for _, h := range origHistory {
			if h.Script == "R__view2.sql" {
				v2Cs = *h.Checksum
			}
		}

		// Modify view1 and view3, leave view2 unchanged
		fs["migrations/R__view1.sql"] = &fstest.MapFile{Data: []byte("CREATE VIEW v1 AS SELECT 111;")}
		fs["migrations/R__view3.sql"] = &fstest.MapFile{Data: []byte("CREATE VIEW v3 AS SELECT 333;")}
		expectedV1Cs, _ := checksum.CalculateString("CREATE VIEW v1 AS SELECT 111;")
		expectedV3Cs, _ := checksum.CalculateString("CREATE VIEW v3 AS SELECT 333;")

		repairRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.AlignedChecksums) != 2 {
			t.Fatalf("expected 2 aligned checksums, got %d: %+v", len(repairRes.AlignedChecksums), repairRes.AlignedChecksums)
		}

		// Verify history
		historyAfter, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		for _, h := range historyAfter {
			switch h.Script {
			case "R__view1.sql":
				if *h.Checksum != expectedV1Cs {
					t.Errorf("R__view1 checksum mismatch: got %d, want %d", *h.Checksum, expectedV1Cs)
				}
			case "R__view2.sql":
				if *h.Checksum != v2Cs {
					t.Errorf("R__view2 checksum changed unexpectedly: got %d, want %d", *h.Checksum, v2Cs)
				}
			case "R__view3.sql":
				if *h.Checksum != expectedV3Cs {
					t.Errorf("R__view3 checksum mismatch: got %d, want %d", *h.Checksum, expectedV3Cs)
				}
			}
		}

		// Migrate() executes 0 migrations because all are aligned
		m2, _ := New(cfg, db)
		migRes2, err := m2.Migrate(ctx)
		if err != nil || migRes2.MigrationsExecuted != 0 {
			t.Errorf("expected 0 executed migrations after repair, got %d, err=%v", migRes2.MigrationsExecuted, err)
		}

		// Now modify view2 WITHOUT Repair(): Migrate() must re-execute only view2
		fs["migrations/R__view2.sql"] = &fstest.MapFile{Data: []byte("CREATE VIEW v2 AS SELECT 222;")}
		m3, _ := New(cfg, db)
		migRes3, err := m3.Migrate(ctx)
		if err != nil || migRes3.MigrationsExecuted != 1 {
			t.Errorf("expected exactly 1 migration executed for modified view2, got %d, err=%v", migRes3.MigrationsExecuted, err)
		}
	})

	t.Run("RepeatableWithPlaceholders_ReplacedChecksumAligned", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/R__placeholder.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW v AS SELECT '${app_env}';"),
			},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.Placeholders = map[string]string{"app_env": "production"}

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		_, err = m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		// Modify SQL on disk
		fs["migrations/R__placeholder.sql"] = &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT '${app_env}', 42;"),
		}
		expectedReplacedCs, _ := checksum.CalculateString("CREATE VIEW v AS SELECT 'production', 42;")

		repairRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.AlignedChecksums) != 1 {
			t.Fatalf("expected 1 aligned checksum, got %d", len(repairRes.AlignedChecksums))
		}

		history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if len(history) != 1 || *history[0].Checksum != expectedReplacedCs {
			t.Errorf("expected history checksum %d, got %v", expectedReplacedCs, history[0].Checksum)
		}

		// Subsequent Migrate executes 0 migrations
		m2, _ := New(cfg, db)
		migRes2, err := m2.Migrate(ctx)
		if err != nil || migRes2.MigrationsExecuted != 0 {
			t.Errorf("expected 0 executed migrations, got %d, err=%v", migRes2.MigrationsExecuted, err)
		}
	})

	t.Run("Idempotency_SuccessiveRepairRunsDoNotDuplicateOrAlter", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/R__v.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, _ = m.Migrate(ctx)

		// Modify
		fs["migrations/R__v.sql"] = &fstest.MapFile{Data: []byte("SELECT 2;")}

		// First repair
		rep1, err := m.Repair(ctx)
		if err != nil || len(rep1.AlignedChecksums) != 1 {
			t.Fatalf("first repair failed: %v, aligned=%d", err, len(rep1.AlignedChecksums))
		}

		// Second repair
		rep2, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("second repair failed: %v", err)
		}
		if len(rep2.AlignedChecksums) != 0 {
			t.Errorf("expected 0 aligned checksums on idempotent second run, got %d", len(rep2.AlignedChecksums))
		}
	})
}

// -----------------------------------------------------------------------------
// ADVERSARIAL TEST 2: MISSING VERSIONED MIGRATION
// -----------------------------------------------------------------------------

func TestChallenger_RepairMissingVersionedMigration_MarkedDeleteAndValidatePasses(t *testing.T) {
	ctx := context.Background()

	t.Run("SingleVersioned_DeletedFromDisk_MarkedDelete_ValidatePasses", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
			"migrations/.keep":        &fstest.MapFile{Data: []byte("")},
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

		_, err = m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		// Delete V1 from disk
		delete(fs, "migrations/V1__init.sql")

		// Before Repair: Validate MUST fail
		valBefore, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if valBefore.Valid {
			t.Fatalf("expected Validate to fail when applied versioned migration is missing locally")
		}

		// Run Repair
		repairRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.DeletedMigrations) != 1 || repairRes.DeletedMigrations[0] != "V1__init.sql" {
			t.Fatalf("expected DeletedMigrations to have V1__init.sql, got %+v", repairRes.DeletedMigrations)
		}

		// Verify record has Type == "DELETE"
		history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if err != nil || len(history) != 1 {
			t.Fatalf("FetchHistory failed, len=%d", len(history))
		}
		if history[0].Type != "DELETE" {
			t.Errorf("expected record type 'DELETE', got %q", history[0].Type)
		}
		if history[0].Version == nil || history[0].Version.String() != "1" {
			t.Errorf("expected version 1 preserved, got %v", history[0].Version)
		}

		// After Repair: Validate MUST pass
		valAfter, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate after repair returned error: %v", err)
		}
		if !valAfter.Valid {
			t.Fatalf("expected Validate to pass after repair marked record as DELETE, got errors: %s", valAfter.Error())
		}

		// Subsequent Migrate must execute 0 migrations
		m2, _ := New(cfg, db)
		migRes2, err := m2.Migrate(ctx)
		if err != nil || migRes2.MigrationsExecuted != 0 {
			t.Errorf("expected 0 migrations executed after repair, got %d, err=%v", migRes2.MigrationsExecuted, err)
		}
	})

	t.Run("MiddleVersioned_DeletedFromDisk_NonDeletedRetainSQLType", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__first.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
			"migrations/V2__middle.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t2 (id INT64);")},
			"migrations/V3__latest.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t3 (id INT64);")},
			"migrations/.keep":          &fstest.MapFile{Data: []byte("")},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		res, err := m.Migrate(ctx)
		if err != nil || res.MigrationsExecuted != 3 {
			t.Fatalf("initial Migrate failed: %v, exec=%d", err, res.MigrationsExecuted)
		}

		// Delete middle migration V2 from disk
		delete(fs, "migrations/V2__middle.sql")

		valBefore, _ := m.Validate(ctx)
		if valBefore.Valid {
			t.Fatalf("expected Validate to fail before repair")
		}

		repairRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.DeletedMigrations) != 1 || repairRes.DeletedMigrations[0] != "V2__middle.sql" {
			t.Errorf("expected V2__middle.sql in DeletedMigrations, got %+v", repairRes.DeletedMigrations)
		}

		history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		for _, h := range history {
			switch h.Script {
			case "V1__first.sql":
				if h.Type != "SQL" {
					t.Errorf("expected V1 Type to remain 'SQL', got %q", h.Type)
				}
			case "V2__middle.sql":
				if h.Type != "DELETE" {
					t.Errorf("expected V2 Type to be 'DELETE', got %q", h.Type)
				}
			case "V3__latest.sql":
				if h.Type != "SQL" {
					t.Errorf("expected V3 Type to remain 'SQL', got %q", h.Type)
				}
			}
		}

		// Validate passes
		valAfter, err := m.Validate(ctx)
		if err != nil || !valAfter.Valid {
			t.Fatalf("expected Validate to pass after repair, got valid=%v, err=%v", valAfter.Valid, err)
		}

		// Add V4 and migrate: only V4 must run
		fs["migrations/V4__new.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE t4 (id INT64);")}
		m2, _ := New(cfg, db)
		migRes2, err := m2.Migrate(ctx)
		if err != nil || migRes2.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration executed (V4), got %d, err=%v", migRes2.MigrationsExecuted, err)
		}
	})

	t.Run("Idempotency_RepeatRepairOnDeletedVersioned", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/.keep":        &fstest.MapFile{Data: []byte("")},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, _ = m.Migrate(ctx)
		delete(fs, "migrations/V1__init.sql")

		rep1, err := m.Repair(ctx)
		if err != nil || len(rep1.DeletedMigrations) != 1 {
			t.Fatalf("first repair failed: %v, deleted=%d", err, len(rep1.DeletedMigrations))
		}

		rep2, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("second repair failed: %v", err)
		}
		if len(rep2.DeletedMigrations) != 0 {
			t.Errorf("expected 0 deleted migrations on second repair run, got %d", len(rep2.DeletedMigrations))
		}
	})
}

// -----------------------------------------------------------------------------
// ADVERSARIAL TEST 3: MISSING REPEATABLE MIGRATION
// -----------------------------------------------------------------------------

func TestChallenger_RepairMissingRepeatableMigration_MarkedDeleteAndValidatePasses(t *testing.T) {
	ctx := context.Background()

	t.Run("SingleRepeatable_DeletedFromDisk_MarkedDelete_ValidatePasses", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/R__view.sql": &fstest.MapFile{Data: []byte("CREATE VIEW v AS SELECT 1;")},
			"migrations/.keep":       &fstest.MapFile{Data: []byte("")},
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

		_, err = m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}

		// Delete R__view.sql from disk
		delete(fs, "migrations/R__view.sql")

		// Before Repair: Validate MUST fail
		valBefore, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if valBefore.Valid {
			t.Fatalf("expected Validate to fail when applied repeatable is missing from disk")
		}

		// Run Repair
		repairRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.DeletedMigrations) != 1 || repairRes.DeletedMigrations[0] != "R__view.sql" {
			t.Fatalf("expected R__view.sql in DeletedMigrations, got %+v", repairRes.DeletedMigrations)
		}

		// Verify record has Type == "DELETE"
		history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if err != nil || len(history) != 1 {
			t.Fatalf("FetchHistory failed, len=%d", len(history))
		}
		if history[0].Type != "DELETE" {
			t.Errorf("expected record type 'DELETE', got %q", history[0].Type)
		}

		// After Repair: Validate MUST pass
		valAfter, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate after repair failed: %v", err)
		}
		if !valAfter.Valid {
			t.Fatalf("expected Validate to pass after repair marked repeatable as DELETE, got: %s", valAfter.Error())
		}

		// Subsequent Migrate executes 0
		m2, _ := New(cfg, db)
		migRes2, err := m2.Migrate(ctx)
		if err != nil || migRes2.MigrationsExecuted != 0 {
			t.Errorf("expected 0 executed migrations after repair, got %d, err=%v", migRes2.MigrationsExecuted, err)
		}
	})

	t.Run("MultipleRepeatables_OneDeletedOneRetained", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/R__view1.sql": &fstest.MapFile{Data: []byte("CREATE VIEW v1 AS SELECT 1;")},
			"migrations/R__view2.sql": &fstest.MapFile{Data: []byte("CREATE VIEW v2 AS SELECT 2;")},
			"migrations/.keep":        &fstest.MapFile{Data: []byte("")},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, _ = m.Migrate(ctx)

		// Delete view1 only
		delete(fs, "migrations/R__view1.sql")

		repairRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.DeletedMigrations) != 1 || repairRes.DeletedMigrations[0] != "R__view1.sql" {
			t.Errorf("expected R__view1.sql in DeletedMigrations, got %+v", repairRes.DeletedMigrations)
		}

		history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		for _, h := range history {
			if h.Script == "R__view1.sql" && h.Type != "DELETE" {
				t.Errorf("expected R__view1 to be DELETE, got %q", h.Type)
			}
			if h.Script == "R__view2.sql" && h.Type == "DELETE" {
				t.Errorf("expected R__view2 NOT to be DELETE")
			}
		}

		valAfter, err := m.Validate(ctx)
		if err != nil || !valAfter.Valid {
			t.Errorf("expected Validate to pass, got valid=%v, err=%v", valAfter.Valid, err)
		}

		// Modifying view2 executes ONLY view2
		fs["migrations/R__view2.sql"] = &fstest.MapFile{Data: []byte("CREATE VIEW v2 AS SELECT 200;")}
		m2, _ := New(cfg, db)
		migRes2, err := m2.Migrate(ctx)
		if err != nil || migRes2.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration executed for view2, got %d, err=%v", migRes2.MigrationsExecuted, err)
		}
	})

	t.Run("Idempotency_RepeatRepairOnDeletedRepeatable", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/R__view.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/.keep":       &fstest.MapFile{Data: []byte("")},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, _ = m.Migrate(ctx)
		delete(fs, "migrations/R__view.sql")

		rep1, err := m.Repair(ctx)
		if err != nil || len(rep1.DeletedMigrations) != 1 {
			t.Fatalf("first repair failed: %v", err)
		}

		rep2, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("second repair failed: %v", err)
		}
		if len(rep2.DeletedMigrations) != 0 {
			t.Errorf("expected 0 deleted migrations on second repair, got %d", len(rep2.DeletedMigrations))
		}
	})
}

// -----------------------------------------------------------------------------
// ADVERSARIAL TEST 4: FAILED MIGRATION CLEANUP AND RETRY
// -----------------------------------------------------------------------------

func TestChallenger_RepairFailedMigration_RemovedAndSubsequentRetry(t *testing.T) {
	ctx := context.Background()

	t.Run("VersionedMigration_FailureDuringMigrate_CleanedByRepair_RetrySucceeds", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__good.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
			"migrations/V2__bad.sql":  &fstest.MapFile{Data: []byte("FAIL_SQL_SYNTAX_ERROR;")},
		}

		db := mock.NewMockDatabase()
		db.SetFailOnExecute("FAIL_SQL_SYNTAX_ERROR")

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		// Initial Migrate must fail on V2
		migRes, err := m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected Migrate to fail on V2 syntax error, got success")
		}
		if migRes != nil && migRes.Success {
			t.Errorf("expected migRes.Success = false")
		}

		// History table has V1 (success) and V2 (failed)
		historyBefore, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if err != nil || len(historyBefore) != 2 {
			t.Fatalf("expected 2 history records before repair, got %d", len(historyBefore))
		}
		var foundFailed bool
		for _, h := range historyBefore {
			if h.Script == "V2__bad.sql" && !h.Success {
				foundFailed = true
			}
		}
		if !foundFailed {
			t.Fatalf("expected failed record for V2__bad.sql in history table")
		}

		// Validate before Repair must fail
		valBefore, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if valBefore.Valid {
			t.Fatalf("expected Validate to fail when failed migration row exists")
		}

		// Run Repair
		repairRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.RemovedFailed) != 1 || repairRes.RemovedFailed[0] != "V2__bad.sql" {
			t.Fatalf("expected RemovedFailed to contain V2__bad.sql, got %+v", repairRes.RemovedFailed)
		}

		// Verify V2 was DELETED from history table
		historyAfter, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if err != nil || len(historyAfter) != 1 {
			t.Fatalf("expected exactly 1 history record after repair, got %d", len(historyAfter))
		}
		if historyAfter[0].Script != "V1__good.sql" {
			t.Errorf("expected surviving record to be V1__good.sql, got %s", historyAfter[0].Script)
		}

		// Fix V2 on disk and clear mock failure trigger
		db.SetFailOnExecute("")
		fs["migrations/V2__bad.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE t2 (id INT64);")}

		// Validate now passes
		m2, _ := New(cfg, db)
		valAfter, err := m2.Validate(ctx)
		if err != nil || !valAfter.Valid {
			t.Fatalf("expected Validate to pass after repair and SQL fix, valid=%v, err=%v", valAfter.Valid, err)
		}

		// Migrate again: V2 must successfully apply!
		migRes2, err := m2.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate after repair and fix failed: %v", err)
		}
		if migRes2.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration executed (fixed V2), got %d", migRes2.MigrationsExecuted)
		}

		// Verify history table now has V1 and V2 both successful
		historyFinal, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if len(historyFinal) != 2 {
			t.Fatalf("expected 2 history records, got %d", len(historyFinal))
		}
		for _, h := range historyFinal {
			if !h.Success {
				t.Errorf("expected record %s to be Success=true", h.Script)
			}
		}
	})

	t.Run("RepeatableMigration_FailureDuringMigrate_CleanedByRepair_RetrySucceeds", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/R__view.sql": &fstest.MapFile{Data: []byte("FAIL_REPEATABLE_SYNTAX;")},
		}

		db := mock.NewMockDatabase()
		db.SetFailOnExecute("FAIL_REPEATABLE_SYNTAX")

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, err := m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected Migrate to fail on repeatable syntax error")
		}

		// History has failed repeatable record
		history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if len(history) != 1 || history[0].Success {
			t.Fatalf("expected 1 failed repeatable record in history")
		}

		// Repair removes it
		repairRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.RemovedFailed) != 1 || repairRes.RemovedFailed[0] != "R__view.sql" {
			t.Fatalf("expected RemovedFailed to contain R__view.sql, got %+v", repairRes.RemovedFailed)
		}

		// History is empty
		historyAfter, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if len(historyAfter) != 0 {
			t.Errorf("expected 0 history records after repair, got %d", len(historyAfter))
		}

		// Fix script and migrate
		db.SetFailOnExecute("")
		fs["migrations/R__view.sql"] = &fstest.MapFile{Data: []byte("CREATE VIEW v AS SELECT 1;")}

		m2, _ := New(cfg, db)
		migRes2, err := m2.Migrate(ctx)
		if err != nil || migRes2.MigrationsExecuted != 1 {
			t.Errorf("expected successful Migrate with 1 execution, got %d, err=%v", migRes2.MigrationsExecuted, err)
		}
	})

	t.Run("MultipleFailedRecords_AllRemovedCleanly", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		v1 := "1"
		v2 := "2"
		v3 := "3"
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1, Script: "V1__f1.sql", Success: false,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v2, Script: "V2__ok.sql", Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 3, Version: &v3, Script: "V3__f2.sql", Success: false,
		})

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fstest.MapFS{
			"migrations/V2__ok.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"migrations/.keep":      &fstest.MapFile{Data: []byte("")},
		}
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		repairRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repairRes.RemovedFailed) != 2 {
			t.Errorf("expected 2 removed failed records, got %d: %+v", len(repairRes.RemovedFailed), repairRes.RemovedFailed)
		}

		historyAfter, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if len(historyAfter) != 1 || historyAfter[0].Script != "V2__ok.sql" {
			t.Errorf("expected only V2__ok.sql to remain, got len=%d", len(historyAfter))
		}
	})
}

// -----------------------------------------------------------------------------
// ADVERSARIAL TEST 5: COMPREHENSIVE COMBINED GAUNTLET
// -----------------------------------------------------------------------------

func TestChallenger_RepairComprehensive_AllInvariantsCombined(t *testing.T) {
	ctx := context.Background()

	// Initial configuration with:
	// - V1: Healthy versioned migration
	// - V2: Versioned migration that will be deleted from disk
	// - R1: Healthy repeatable migration
	// - R2: Repeatable migration that will be modified on disk
	// - R3: Repeatable migration that will be deleted from disk
	fs := fstest.MapFS{
		"migrations/V1__base.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		"migrations/V2__delete.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t2 (id INT64);")},
		"migrations/R__r1_keep.sql": &fstest.MapFile{Data: []byte("CREATE VIEW r1 AS SELECT 1;")},
		"migrations/R__r2_mod.sql":  &fstest.MapFile{Data: []byte("CREATE VIEW r2 AS SELECT 2;")},
		"migrations/R__r3_del.sql":  &fstest.MapFile{Data: []byte("CREATE VIEW r3 AS SELECT 3;")},
		"migrations/.keep":          &fstest.MapFile{Data: []byte("")},
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

	// 1. Initial Migrate: applies V1, V2, R1, R2, R3
	migRes, err := m.Migrate(ctx)
	if err != nil || migRes.MigrationsExecuted != 5 {
		t.Fatalf("initial Migrate failed: %v, exec=%d", err, migRes.MigrationsExecuted)
	}

	// 2. Add failing migration V3
	fs["migrations/V3__failing.sql"] = &fstest.MapFile{Data: []byte("FAIL_PROCEDURAL_ERROR;")}
	db.SetFailOnExecute("FAIL_PROCEDURAL_ERROR")

	mFailing, _ := New(cfg, db)
	_, _ = mFailing.Migrate(ctx) // Fails on V3, records failed row

	// 3. Now induce all discrepancy states simultaneously:
	// - V2 deleted from disk
	delete(fs, "migrations/V2__delete.sql")
	// - R3 deleted from disk
	delete(fs, "migrations/R__r3_del.sql")
	// - R2 modified on disk
	fs["migrations/R__r2_mod.sql"] = &fstest.MapFile{Data: []byte("CREATE VIEW r2 AS SELECT 2000;")}
	expectedR2Cs, _ := checksum.CalculateString("CREATE VIEW r2 AS SELECT 2000;")
	// - V3 remains in failed state in history

	// 4. Validate before Repair MUST fail with multiple errors
	valBefore, err := mFailing.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if valBefore.Valid {
		t.Fatalf("expected Validate to fail on combined discrepancies")
	}
	if len(valBefore.Errors) < 3 {
		t.Errorf("expected at least 3 validation errors, got %d: %s", len(valBefore.Errors), valBefore.Error())
	}

	// 5. Execute Repair() in a single call
	repairRes, err := mFailing.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}

	// Assertions on RepairResult:
	// - V3 removed from failed
	if len(repairRes.RemovedFailed) != 1 || repairRes.RemovedFailed[0] != "V3__failing.sql" {
		t.Errorf("expected RemovedFailed to contain V3__failing.sql, got %+v", repairRes.RemovedFailed)
	}
	// - V2 and R3 marked as deleted
	expectedDeleted := map[string]bool{"V2__delete.sql": true, "R__r3_del.sql": true}
	for _, del := range repairRes.DeletedMigrations {
		if !expectedDeleted[del] {
			t.Errorf("unexpected deleted migration reported: %s", del)
		}
		delete(expectedDeleted, del)
	}
	if len(expectedDeleted) != 0 {
		t.Errorf("missing expected deleted migrations: %+v", expectedDeleted)
	}
	// - R2 checksum aligned
	if len(repairRes.AlignedChecksums) != 1 || repairRes.AlignedChecksums[0] != "R__r2_mod.sql" {
		t.Errorf("expected AlignedChecksums to contain R__r2_mod.sql, got %+v", repairRes.AlignedChecksums)
	}

	// 6. Inspect database history records directly
	historyAfter, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("FetchHistory failed: %v", err)
	}

	for _, h := range historyAfter {
		switch h.Script {
		case "V1__base.sql":
			if h.Type != "SQL" || !h.Success {
				t.Errorf("V1 in unexpected state: type=%s, success=%v", h.Type, h.Success)
			}
		case "V2__delete.sql":
			if h.Type != "DELETE" {
				t.Errorf("V2 expected type 'DELETE', got %q", h.Type)
			}
		case "V3__failing.sql":
			t.Errorf("V3 should have been deleted from history, but still present!")
		case "R__r1_keep.sql":
			if h.Type != "SQL" || !h.Success {
				t.Errorf("R1 in unexpected state: type=%s, success=%v", h.Type, h.Success)
			}
		case "R__r2_mod.sql":
			if *h.Checksum != expectedR2Cs {
				t.Errorf("R2 checksum mismatch: got %d, want %d", *h.Checksum, expectedR2Cs)
			}
		case "R__r3_del.sql":
			if h.Type != "DELETE" {
				t.Errorf("R3 expected type 'DELETE', got %q", h.Type)
			}
		default:
			t.Errorf("unexpected history record: %s", h.Script)
		}
	}

	// 7. Fix V3 on disk with valid SQL and clear trigger
	db.SetFailOnExecute("")
	fs["migrations/V3__failing.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE t3 (id INT64);")}

	// 8. Validate MUST now succeed completely!
	mClean, _ := New(cfg, db)
	valAfter, err := mClean.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate after repair and fix failed: %v", err)
	}
	if !valAfter.Valid {
		t.Fatalf("expected Validate to be valid after repair, got errors:\n%s", valAfter.Error())
	}

	// 9. Migrate(): MUST execute ONLY V3!
	// - V1, V2: already applied / retired
	// - R1: untouched
	// - R2: checksum was aligned by repair, so it must NOT re-execute!
	// - R3: deleted
	migResFinal, err := mClean.Migrate(ctx)
	if err != nil {
		t.Fatalf("final Migrate failed: %v", err)
	}
	if migResFinal.MigrationsExecuted != 1 {
		t.Errorf("expected EXACTLY 1 migration executed (only fixed V3), got %d", migResFinal.MigrationsExecuted)
	}
}

// -----------------------------------------------------------------------------
// ADVERSARIAL TEST 6: CALLBACKS AND LOCKING
// -----------------------------------------------------------------------------

func TestChallenger_RepairCallbacksAndLocking(t *testing.T) {
	ctx := context.Background()

	t.Run("RepairFiresLifecycleCallbacks", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql":        &fstest.MapFile{Data: []byte("CREATE TABLE u (id INT64);")},
			"migrations/beforeRepair.sql":    &fstest.MapFile{Data: []byte("CREATE TABLE audit_before_repair (val INT64);")},
			"migrations/afterRepair.sql":     &fstest.MapFile{Data: []byte("CREATE TABLE audit_after_repair (val INT64);")},
			"migrations/.keep":               &fstest.MapFile{Data: []byte("")},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, _ = m.Migrate(ctx)

		// Delete V1 to trigger a repair action
		delete(fs, "migrations/V1__init.sql")

		_, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}

		stmts := db.ExecutedStatements()
		var foundBefore, foundAfter bool
		for _, stmt := range stmts {
			if strings.Contains(stmt, "audit_before_repair") {
				foundBefore = true
			}
			if strings.Contains(stmt, "audit_after_repair") {
				foundAfter = true
			}
		}

		if !foundBefore {
			t.Errorf("expected beforeRepair callback to execute, statements were: %v", stmts)
		}
		if !foundAfter {
			t.Errorf("expected afterRepair callback to execute, statements were: %v", stmts)
		}
	})

	t.Run("LockIsReleasedAfterRepair", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, _ = m.Migrate(ctx)

		_, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}

		// Verify database lock can be immediately acquired by another operation
		unlock, err := db.Lock(ctx, "test_ds", "flyway_schema_history")
		if err != nil {
			t.Fatalf("failed to acquire lock after Repair: lock was not released! %v", err)
		}
		_ = unlock(ctx)
	})

	t.Run("ConcurrentRepairInvocationsSafelyLock", func(t *testing.T) {
		fs := fstest.MapFS{
			"migrations/R__view.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		}

		db := mock.NewMockDatabase()
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, _ = m.Migrate(ctx)

		fs["migrations/R__view.sql"] = &fstest.MapFile{Data: []byte("SELECT 2;")}

		var wg sync.WaitGroup
		errs := make(chan error, 5)

		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				mConcurrent, _ := New(cfg, db)
				_, err := mConcurrent.Repair(ctx)
				if err != nil {
					errs <- err
				}
			}()
		}

		wg.Wait()
		close(errs)

		// Any errors that occur must be exclusively lock-contention errors
		for err := range errs {
			if !strings.Contains(err.Error(), "already locked") {
				t.Errorf("unexpected concurrent error: %v", err)
			}
		}

		// When contention finishes, a sequential repair must succeed cleanly
		mSequential, _ := New(cfg, db)
		repRes, err := mSequential.Repair(ctx)
		if err != nil {
			t.Fatalf("sequential repair after concurrency failed: %v", err)
		}
		_ = repRes

		// History must be properly aligned
		history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
		if len(history) != 1 {
			t.Fatalf("expected 1 history record, got %d", len(history))
		}
		expectedCs, _ := checksum.CalculateString("SELECT 2;")
		if *history[0].Checksum != expectedCs {
			t.Errorf("expected checksum %d, got %d", expectedCs, *history[0].Checksum)
		}
	})
}

// -----------------------------------------------------------------------------
// ADVERSARIAL TEST 7: VERSIONED CHECKSUM REALIGNMENT & BASELINE PRESERVATION
// -----------------------------------------------------------------------------

func TestChallenger_RepairVersionedChecksumRealignment(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE users (id INT64);")},
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
		t.Fatalf("Migrate failed: %v", err)
	}

	// 2. Modify versioned file on disk (e.g. whitespace, comment, formatting)
	newSQL := "CREATE TABLE users (id INT64); -- formatted"
	fs["migrations/V1__init.sql"] = &fstest.MapFile{Data: []byte(newSQL)}
	expectedNewCs, _ := checksum.CalculateString(newSQL)

	// 3. Validate MUST fail with checksum mismatch
	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if valRes.Valid {
		t.Fatalf("expected Validate to fail on versioned checksum mismatch")
	}

	// 4. Run Repair
	repairRes, err := m.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if len(repairRes.AlignedChecksums) != 1 {
		t.Fatalf("expected 1 aligned checksum, got %+v", repairRes.AlignedChecksums)
	}

	// 5. History table checksum matches disk
	history, err := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if err != nil || len(history) != 1 {
		t.Fatalf("FetchHistory failed, len=%d", len(history))
	}
	if *history[0].Checksum != expectedNewCs {
		t.Errorf("expected history checksum %d, got %d", expectedNewCs, *history[0].Checksum)
	}

	// 6. Validate now passes
	valRes2, err := m.Validate(ctx)
	if err != nil || !valRes2.Valid {
		t.Errorf("expected Validate to pass after repair, got valid=%v, err=%v", valRes2.Valid, err)
	}

	// 7. Migrate does NOT re-run versioned migration
	migRes, err := m.Migrate(ctx)
	if err != nil || migRes.MigrationsExecuted != 0 {
		t.Errorf("expected 0 migrations executed, got %d, err=%v", migRes.MigrationsExecuted, err)
	}
}

func TestChallenger_RepairBaselineAndSchemaRecordsPreserved(t *testing.T) {
	ctx := context.Background()

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	baseVer := "1.0"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &baseVer,
		Description:   "<< Flyway Baseline >>",
		Type:          "BASELINE",
		Script:        "<< Flyway Baseline >>",
		Success:       true,
	})
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2,
		Version:       nil,
		Description:   "<< Flyway Schema Creation >>",
		Type:          "SCHEMA",
		Script:        "<< Flyway Schema Creation >>",
		Success:       true,
	})

	fs := fstest.MapFS{
		"migrations/.keep": &fstest.MapFile{Data: []byte("")},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, _ := New(cfg, db)

	// Repair should NOT touch BASELINE or SCHEMA records
	repRes, err := m.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if len(repRes.DeletedMigrations) != 0 || len(repRes.AlignedChecksums) != 0 || len(repRes.RemovedFailed) != 0 {
		t.Errorf("expected no repair actions on baseline/schema records, got: %+v", repRes)
	}

	history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}
	if history[0].Type != "BASELINE" || history[1].Type != "SCHEMA" {
		t.Errorf("records altered: %+v", history)
	}
}

func TestChallenger_RepairRestoredRepeatableMigrationReactivates(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/R__view.sql": &fstest.MapFile{Data: []byte("CREATE VIEW v AS SELECT 1;")},
		"migrations/.keep":       &fstest.MapFile{Data: []byte("")},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, _ := New(cfg, db)
	_, _ = m.Migrate(ctx)

	// 1. Delete from disk and Repair -> marked DELETE
	delete(fs, "migrations/R__view.sql")
	_, _ = m.Repair(ctx)

	history, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if len(history) != 1 || history[0].Type != "DELETE" {
		t.Fatalf("expected record to be marked DELETE, got: %+v", history)
	}

	// 2. Restore file on disk (same description)
	fs["migrations/R__view.sql"] = &fstest.MapFile{Data: []byte("CREATE VIEW v AS SELECT 1;")}

	// 3. Repair again -> restores Type to SQL
	m2, _ := New(cfg, db)
	repRes, err := m2.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if len(repRes.AlignedChecksums) != 1 {
		t.Errorf("expected 1 aligned checksum for restored repeatable, got %+v", repRes.AlignedChecksums)
	}

	historyAfter, _ := db.FetchHistory(ctx, "test_ds", "flyway_schema_history")
	if len(historyAfter) != 1 || historyAfter[0].Type != "SQL" {
		t.Errorf("expected record restored to SQL, got %q", historyAfter[0].Type)
	}

	// 4. Validate passes
	valRes, err := m2.Validate(ctx)
	if err != nil || !valRes.Valid {
		t.Errorf("expected Validate to pass, valid=%v, err=%v", valRes.Valid, err)
	}

	// 5. Migrate executes 0 migrations (checksum matches)
	migRes, err := m2.Migrate(ctx)
	if err != nil || migRes.MigrationsExecuted != 0 {
		t.Errorf("expected 0 executed migrations, got %d, err=%v", migRes.MigrationsExecuted, err)
	}
}

