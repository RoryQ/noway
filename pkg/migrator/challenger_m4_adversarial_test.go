package migrator

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/roryq/noway/pkg/checksum"
	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/database/mock"
)

// ============================================================================
// CHALLENGER M4: EMPIRICAL ADVERSARIAL VERIFICATION OF VALIDATE INVARIANTS
// ============================================================================

// 1. Out-of-order pending checks when outOfOrder=false:
//    Unapplied V1 when V2 is applied -> verify validation error.
func TestChallenger_OutOfOrder_Disabled_ReportsValidationError(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V2__second.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	cfg.OutOfOrder = false

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// Apply V2
	migRes, err := m.Migrate(ctx)
	if err != nil || migRes.MigrationsExecuted != 1 {
		t.Fatalf("initial Migrate failed: %v, exec=%d", err, migRes.MigrationsExecuted)
	}

	// Now introduce older pending migration V1 locally
	fs["migrations/V1__first.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t1 (id INT64);"),
	}

	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate returned unexpected system error: %v", err)
	}
	if valRes.Valid {
		t.Fatalf("expected validation failure when older pending migration V1 exists and outOfOrder=false")
	}
	if len(valRes.Errors) != 1 {
		t.Fatalf("expected exactly 1 validation error, got %d", len(valRes.Errors))
	}
	if !strings.Contains(valRes.Errors[0].Message, "Detected resolved migration not applied to database: 1") {
		t.Errorf("expected out-of-order pending message mentioning version 1, got: %s", valRes.Errors[0].Message)
	}
}

// 2. Out-of-order enabled (outOfOrder=true):
//    Unapplied V1 when V2 is applied -> verify validation passes.
func TestChallenger_OutOfOrder_Enabled_ValidationPasses(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V2__second.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	cfg.OutOfOrder = true

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// Apply V2
	migRes, err := m.Migrate(ctx)
	if err != nil || migRes.MigrationsExecuted != 1 {
		t.Fatalf("initial Migrate failed: %v, exec=%d", err, migRes.MigrationsExecuted)
	}

	// Now introduce older pending migration V1 locally
	fs["migrations/V1__first.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t1 (id INT64);"),
	}

	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate returned unexpected system error: %v", err)
	}
	if !valRes.Valid {
		t.Fatalf("expected validation to pass when outOfOrder=true, got errors: %s", valRes.Error())
	}
}

// 1b. Multi-gap out-of-order pending migrations:
//     V2 and V4 applied, V1 and V3 pending -> verify both reported.
func TestChallenger_OutOfOrder_Disabled_MultiplePendingGaps(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V2__second.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
		"migrations/V4__fourth.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t4 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	cfg.OutOfOrder = false

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	_, err = m.Migrate(ctx)
	if err != nil {
		t.Fatalf("initial Migrate failed: %v", err)
	}

	// Add pending V1 and V3
	fs["migrations/V1__first.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t1 (id INT64);"),
	}
	fs["migrations/V3__third.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t3 (id INT64);"),
	}

	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if valRes.Valid {
		t.Fatalf("expected validation failure for pending V1 and V3")
	}
	if len(valRes.Errors) != 2 {
		t.Fatalf("expected 2 validation errors, got %d: %s", len(valRes.Errors), valRes.Error())
	}

	errMsg := valRes.Error()
	if !strings.Contains(errMsg, "not applied to database: 1") || !strings.Contains(errMsg, "not applied to database: 3") {
		t.Errorf("expected errors for both version 1 and 3, got: %s", errMsg)
	}
}

// 1c. Forward pending migration (V2 pending when V1 applied) is NOT out-of-order:
//     Must pass validation even when OutOfOrder=false.
func TestChallenger_OutOfOrder_Disabled_ForwardPendingAllowed(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__first.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	cfg.OutOfOrder = false

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	_, err = m.Migrate(ctx)
	if err != nil {
		t.Fatalf("initial Migrate failed: %v", err)
	}

	// Add forward pending V2
	fs["migrations/V2__second.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t2 (id INT64);"),
	}

	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !valRes.Valid {
		t.Errorf("forward pending migration V2 should not fail validation when outOfOrder=false, got: %s", valRes.Error())
	}
}

// 1d. Target cutoff interactions with OutOfOrder:
//     V2 applied, V1 pending. If target is set below V1, V1 is beyond target cutoff.
func TestChallenger_OutOfOrder_TargetCutoffInteractions(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V2__second.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
		"migrations/V1__first.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	cfg.OutOfOrder = false

	// Apply only V2 to DB by directly inserting history record
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
	v2Str := "2"
	cs, _ := checksum.CalculateString("CREATE TABLE t2 (id INT64);")
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v2Str,
		Description:   "second",
		Type:          "SQL",
		Script:        "V2__second.sql",
		Checksum:      &cs,
		Success:       true,
	})

	// Case 1: Target = "0.5" -> V1 (1.0) is beyond target cutoff -> validation passes
	cfg.Target = "0.5"
	m1, _ := New(cfg, db)
	val1, err := m1.Validate(ctx)
	if err != nil || !val1.Valid {
		t.Errorf("expected Target=0.5 to skip V1 pending check, got valid=%v, err=%v", val1.Valid, err)
	}

	// Case 2: Target = "1.0" -> V1 is within target cutoff -> validation fails
	cfg.Target = "1.0"
	m2, _ := New(cfg, db)
	val2, err := m2.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if val2.Valid {
		t.Errorf("expected Target=1.0 to include V1 and fail validation")
	}

	// Case 3: Target = "current" -> effective target is maxAppliedVersion (2) -> V1 is included -> validation fails
	cfg.Target = "current"
	m3, _ := New(cfg, db)
	val3, err := m3.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if val3.Valid {
		t.Errorf("expected Target=current to include V1 and fail validation")
	}
}

// 1e. Conditional shouldExecute interaction with out-of-order validation:
//     V2 applied, V1 pending with shouldExecute = false -> skipped during validation.
func TestChallenger_OutOfOrder_ShouldExecuteFalse_Skipped(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V2__second.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
		"migrations/V1__first.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"migrations/V1__first.sql.conf": &fstest.MapFile{
			Data: []byte("shouldExecute = false\n"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	cfg.OutOfOrder = false

	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
	v2Str := "2"
	cs, _ := checksum.CalculateString("CREATE TABLE t2 (id INT64);")
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v2Str,
		Description:   "second",
		Type:          "SQL",
		Script:        "V2__second.sql",
		Checksum:      &cs,
		Success:       true,
	})

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !valRes.Valid {
		t.Errorf("expected pending V1 with shouldExecute=false to be skipped during Validate, got: %s", valRes.Error())
	}
}

// 3. History record with Type: "DELETE":
//    Verify ignored during Validate(), for both versioned and repeatable, with or without nil checksum.
//    Also verify Type: "DELETE" does not increment maxAppliedVersion.
func TestChallenger_HistoryRecord_DeleteType_Ignored(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
	}

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	v1Str := "1"
	cs1, _ := checksum.CalculateString("CREATE TABLE t1 (id INT64);")
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "init",
		Type:          "SQL",
		Script:        "V1__init.sql",
		Checksum:      &cs1,
		Success:       true,
	})

	// Record 2: Versioned migration deleted from disk and marked as DELETE with non-nil checksum
	v2Str := "2"
	dummyCs := int64(12345)
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2,
		Version:       &v2Str,
		Description:   "deleted versioned",
		Type:          "DELETE",
		Script:        "V2__deleted.sql",
		Checksum:      &dummyCs,
		Success:       true,
	})

	// Record 3: Repeatable migration deleted from disk and marked as DELETE with nil checksum
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 3,
		Version:       nil,
		Description:   "deleted repeatable",
		Type:          "DELETE",
		Script:        "R__deleted.sql",
		Checksum:      nil,
		Success:       true,
	})

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	cfg.OutOfOrder = false

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !valRes.Valid {
		t.Fatalf("expected Validate to succeed when history records have Type=DELETE, got: %s", valRes.Error())
	}

	// Adversarial test: verify that Type: "DELETE" record does NOT set maxAppliedVersion!
	// Suppose V3 is marked DELETE. Disk has pending V2 (which is older than 3, but newer than applied V1).
	// If V3 were counted as maxAppliedVersion, V2 would fail out-of-order check (2 < 3).
	// But because V3 is DELETE, maxAppliedVersion is 1. V2 (2 > 1) is a forward pending migration -> PASS!
	v3Str := "3"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 4,
		Version:       &v3Str,
		Description:   "retired v3",
		Type:          "DELETE",
		Script:        "V3__retired.sql",
		Success:       true,
	})
	fs["migrations/V2__pending.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t2 (id INT64);"),
	}

	m2, _ := New(cfg, db)
	valRes2, err := m2.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !valRes2.Valid {
		t.Errorf("Type: DELETE record must not set maxAppliedVersion; pending V2 should pass validation, got: %s", valRes2.Error())
	}
}

// 4. History record with Checksum: nil:
//    Verify accepted during Validate() for versioned, repeatable, and baseline records.
func TestChallenger_HistoryRecord_NilChecksum_Accepted(t *testing.T) {
	ctx := context.Background()

	fs := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"migrations/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v1 AS SELECT 1;"),
		},
	}

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	// Versioned migration with Checksum: nil
	v1Str := "1"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "init",
		Type:          "SQL",
		Script:        "V1__init.sql",
		Checksum:      nil,
		Success:       true,
	})

	// Repeatable migration with Checksum: nil
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2,
		Version:       nil,
		Description:   "view",
		Type:          "SQL",
		Script:        "R__view.sql",
		Checksum:      nil,
		Success:       true,
	})

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !valRes.Valid {
		t.Errorf("expected Validate to accept records with Checksum=nil, got: %s", valRes.Error())
	}
}

// 5. Pre-baseline migrations (v <= baselineVersion):
//    Verify exempted during Validate() from checksum mismatch, missing local file,
//    and out-of-order pending checks.
func TestChallenger_PreBaselineMigrations_Exemptions(t *testing.T) {
	ctx := context.Background()

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

	// Rank 1: Pre-baseline V1 (version "1.0", applied checksum 111111)
	v1Str := "1.0"
	cs1 := int64(111111)
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1,
		Version:       &v1Str,
		Description:   "pre baseline",
		Type:          "SQL",
		Script:        "V1.0__pre_baseline.sql",
		Checksum:      &cs1,
		Success:       true,
	})

	// Rank 2: Baseline migration at V2.0
	v2Str := "2.0"
	cs2 := int64(222222)
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 2,
		Version:       &v2Str,
		Description:   "baseline marker",
		Type:          "BASELINE",
		Script:        "B2.0__baseline.sql",
		Checksum:      &cs2,
		Success:       true,
	})

	// Rank 3: Normal migration at V3.0
	v3Str := "3.0"
	cs3, _ := checksum.CalculateString("CREATE TABLE t3 (id INT64);")
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 3,
		Version:       &v3Str,
		Description:   "post baseline",
		Type:          "SQL",
		Script:        "V3.0__post_baseline.sql",
		Checksum:      &cs3,
		Success:       true,
	})

	// Subtest 5a: V1 exists locally but has tampered checksum (999999 != 111111).
	// Since V1 <= baselineVersion (2.0), it MUST be exempted from checksum mismatch!
	fs := fstest.MapFS{
		"migrations/V1.0__pre_baseline.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1_tampered (id INT64, extra STRING);"),
		},
		"migrations/V3.0__post_baseline.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t3 (id INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	cfg.OutOfOrder = false

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	valRes, err := m.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !valRes.Valid {
		t.Errorf("expected pre-baseline V1 checksum mismatch to be exempted, got: %s", valRes.Error())
	}

	// Subtest 5b: V1 missing locally from disk entirely.
	// Since V1 <= baselineVersion (2.0), missing local file MUST be exempted!
	delete(fs, "migrations/V1.0__pre_baseline.sql")
	m2, _ := New(cfg, db)
	valRes2, err := m2.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !valRes2.Valid {
		t.Errorf("expected pre-baseline V1 missing local file to be exempted, got: %s", valRes2.Error())
	}

	// Subtest 5c: Unapplied pre-baseline migration V1.5 on disk.
	// OutOfOrder = false, maxAppliedVersion is 3.0.
	// Since V1.5 <= baselineVersion (2.0), it is covered by baseline and MUST NOT trigger out-of-order error!
	fs["migrations/V1.5__pre_baseline_unapplied.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t1_5 (id INT64);"),
	}
	m3, _ := New(cfg, db)
	valRes3, err := m3.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !valRes3.Valid {
		t.Errorf("expected unapplied V1.5 <= baselineVersion to be exempted from out-of-order pending check, got: %s", valRes3.Error())
	}

	// Subtest 5d: Unapplied post-baseline migration V2.5 on disk.
	// V2.5 > baselineVersion (2.0) and V2.5 < maxApplied (3.0), with OutOfOrder = false.
	// This is NOT covered by baseline and MUST fail out-of-order pending validation!
	fs["migrations/V2.5__post_baseline_unapplied.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t2_5 (id INT64);"),
	}
	m4, _ := New(cfg, db)
	valRes4, err := m4.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if valRes4.Valid {
		t.Errorf("expected unapplied V2.5 > baselineVersion to FAIL out-of-order pending check, but got valid")
	}

	// Subtest 5e: Post-baseline V3 checksum mismatch MUST NOT be exempted.
	delete(fs, "migrations/V2.5__post_baseline_unapplied.sql")
	fs["migrations/V3.0__post_baseline.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t3_tampered (id INT64);"),
	}
	m5, _ := New(cfg, db)
	valRes5, err := m5.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if valRes5.Valid {
		t.Errorf("expected post-baseline V3 checksum mismatch to be detected, but got valid")
	}
}

// 6. Repeatable migrations:
//    Verify lookup by description, type validation, and checksum difference tolerance.
func TestChallenger_RepeatableMigrations_Invariants(t *testing.T) {
	ctx := context.Background()

	// Subtest 6a: Lookup by description when filename is different on disk
	t.Run("LookupByDescription_WhenRenamedOnDisk", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		cs := int64(12345)
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       nil,
			Description:   "custom user view",
			Type:          "SQL",
			Script:        "R__legacy_path_file.sql",
			Checksum:      &cs,
			Success:       true,
		})

		// On disk, the file is R__custom_user_view.sql (which resolves to description "custom user view")
		// The script filename "R__custom_user_view.sql" differs from history "R__legacy_path_file.sql"
		fs := fstest.MapFS{
			"migrations/R__custom_user_view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW custom_user_view AS SELECT 1;"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if !valRes.Valid {
			t.Errorf("expected repeatable migration to be matched by description even when filename changed, got: %s", valRes.Error())
		}
	})

	// Subtest 6b: Checksum difference tolerance
	t.Run("ChecksumDifferenceTolerance", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		oldCs := int64(111111)
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       nil,
			Description:   "compute metrics",
			Type:          "SQL",
			Script:        "R__compute_metrics.sql",
			Checksum:      &oldCs,
			Success:       true,
		})

		// Disk has updated SQL which results in a completely different checksum
		fs := fstest.MapFS{
			"migrations/R__compute_metrics.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW compute_metrics AS SELECT 2, 3, 4;"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if !valRes.Valid {
			t.Errorf("repeatable migrations with modified checksums must NOT fail validation (outdated state meant for re-run), got: %s", valRes.Error())
		}
	})

	// Subtest 6c: Type validation mismatch
	t.Run("TypeValidationMismatch", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		cs := int64(12345)
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       nil,
			Description:   "my view",
			Type:          "JDBC", // In DB it was recorded as JDBC, but resolved as SQL
			Script:        "R__my_view.sql",
			Checksum:      &cs,
			Success:       true,
		})

		fs := fstest.MapFS{
			"migrations/R__my_view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW my_view AS SELECT 1;"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected repeatable type mismatch to fail validation")
		}
		if !strings.Contains(valRes.Error(), "type mismatch") {
			t.Errorf("expected 'type mismatch' error message, got: %s", valRes.Error())
		}
	})

	// Subtest 6d: Missing applied repeatable migration
	t.Run("MissingAppliedRepeatableMigration", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       nil,
			Description:   "lost view",
			Type:          "SQL",
			Script:        "R__lost_view.sql",
			Success:       true,
		})

		fs := fstest.MapFS{
			"migrations/.keep": &fstest.MapFile{
				Data: []byte("keep"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		// In Flyway, missing/deleted repeatable migrations do NOT cause validation failure (treated as DELETED)
		if !valRes.Valid {
			t.Fatalf("expected missing repeatable migration to pass validation (Flyway parity), got error: %s", valRes.Error())
		}
	})

	// Subtest 6e: Description mismatch when matched via script fallback
	t.Run("DescriptionMismatchViaScriptFallback", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")

		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       nil,
			Description:   "old recorded description",
			Type:          "SQL",
			Script:        "R__my_view.sql",
			Success:       true,
		})

		// On disk, filename is R__my_view.sql, so description resolves to "my view"
		fs := fstest.MapFS{
			"migrations/R__my_view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW my_view AS SELECT 1;"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected description mismatch to fail validation")
		}
		if !strings.Contains(valRes.Error(), "description mismatch") {
			t.Errorf("expected 'description mismatch' error, got: %s", valRes.Error())
		}
	})
}

// 7. Validate lifecycle callbacks:
//    Verify beforeValidate, afterValidate on success, and afterValidateError on validation failure.
func TestChallenger_Validate_Callbacks_Executed(t *testing.T) {
	ctx := context.Background()

	// Scenario A: Valid state -> beforeValidate and afterValidate fired
	fsValid := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"migrations/beforeValidate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE cb_bv (id INT64);"),
		},
		"migrations/afterValidate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE cb_av (id INT64);"),
		},
		"migrations/afterValidateError.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE cb_ave (id INT64);"),
		},
	}

	dbValid := mock.NewMockDatabase()
	cfgValid := config.NewDefaultConfiguration()
	cfgValid.DefaultSchema = "test_ds"
	cfgValid.FS = fsValid
	cfgValid.Locations = []string{"migrations"}

	mValid, _ := New(cfgValid, dbValid)
	_, _ = mValid.Migrate(ctx)

	// Now validate valid state
	valRes, err := mValid.Validate(ctx)
	if err != nil || !valRes.Valid {
		t.Fatalf("expected valid validation, err=%v, res=%+v", err, valRes)
	}

	statementsValid := dbValid.ExecutedStatements()
	hasBV := false
	hasAV := false
	hasAVE := false
	for _, stmt := range statementsValid {
		if strings.Contains(stmt, "cb_bv") {
			hasBV = true
		}
		if strings.Contains(stmt, "cb_av") {
			hasAV = true
		}
		if strings.Contains(stmt, "cb_ave") {
			hasAVE = true
		}
	}
	if !hasBV {
		t.Errorf("expected beforeValidate to be executed")
	}
	if !hasAV {
		t.Errorf("expected afterValidate to be executed on valid state")
	}
	if hasAVE {
		t.Errorf("afterValidateError should NOT be executed on valid state")
	}

	// Scenario B: Invalid state (tampered file) -> beforeValidate and afterValidateError fired
	fsValid["migrations/V1__init.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t1_tampered (id INT64);"),
	}

	valResInvalid, err := mValid.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if valResInvalid.Valid {
		t.Fatalf("expected validation failure due to tampered checksum")
	}

	statementsInvalid := dbValid.ExecutedStatements()
	hasAVEAfterFailure := false
	for _, stmt := range statementsInvalid {
		if strings.Contains(stmt, "cb_ave") {
			hasAVEAfterFailure = true
		}
	}
	if !hasAVEAfterFailure {
		t.Errorf("expected afterValidateError to be executed when validation fails")
	}
}
