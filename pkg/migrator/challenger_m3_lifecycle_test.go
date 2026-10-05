package migrator

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database/mock"
)

// ============================================================================
// CHALLENGER M3: ADVERSARIAL VERIFICATION OF LIFECYCLE HOOKS & CALLBACKS
// ============================================================================

// 1. Multiple callbacks for the same event with alphabetical ordering by description
func TestChallenger_MultipleCallbacksAlphabeticalOrder(t *testing.T) {
	ctx := context.Background()

	// Provide plain callback (no description separator) alongside multiple described callbacks.
	// In Flyway / noway:
	// beforeMigrate.sql          -> desc: ""
	// beforeMigrate__01.sql      -> desc: "01"
	// beforeMigrate__a_step.sql  -> desc: "a step"
	// beforeMigrate__b.sql       -> desc: "b"
	// beforeMigrate__z.sql       -> desc: "z"
	//
	// afterMigrate.sql           -> desc: ""
	// afterMigrate__1.sql        -> desc: "1"
	// afterMigrate__2.sql        -> desc: "2"
	// afterMigrate__final.sql    -> desc: "final"
	mockFS := fstest.MapFS{
		"migrations/beforeMigrate__z.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_before_z (id INT64);"),
		},
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_before_plain (id INT64);"),
		},
		"migrations/beforeMigrate__b.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_before_b (id INT64);"),
		},
		"migrations/beforeMigrate__01.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_before_01 (id INT64);"),
		},
		"migrations/beforeMigrate__a_step.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_before_a (id INT64);"),
		},
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/afterMigrate__final.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_after_final (id INT64);"),
		},
		"migrations/afterMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_after_plain (id INT64);"),
		},
		"migrations/afterMigrate__2.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_after_2 (id INT64);"),
		},
		"migrations/afterMigrate__1.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_after_1 (id INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	db := mock.NewMockDatabase()

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected successful migration run")
	}
	if res.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
	}

	executedStmts := db.ExecutedStatements()

	expectedOrder := []string{
		"audit_before_plain", // desc: ""
		"audit_before_01",    // desc: "01"
		"audit_before_a",     // desc: "a step"
		"audit_before_b",     // desc: "b"
		"audit_before_z",     // desc: "z"
		"CREATE TABLE users", // V1 migration
		"audit_after_plain",  // desc: ""
		"audit_after_1",      // desc: "1"
		"audit_after_2",      // desc: "2"
		"audit_after_final",  // desc: "final"
	}

	// Filter executed statements for our tracked tokens
	var matchedStmts []string
	for _, stmt := range executedStmts {
		for _, exp := range expectedOrder {
			if strings.Contains(stmt, exp) {
				matchedStmts = append(matchedStmts, exp)
				break
			}
		}
	}

	if len(matchedStmts) != len(expectedOrder) {
		t.Fatalf("expected %d tracked statements, got %d: %v", len(expectedOrder), len(matchedStmts), matchedStmts)
	}

	for i, exp := range expectedOrder {
		if matchedStmts[i] != exp {
			t.Errorf("execution order mismatch at index %d: expected %q, got %q (full sequence: %v)",
				i, exp, matchedStmts[i], matchedStmts)
		}
	}
}

// 2. Multiple callbacks for beforeEachMigrate and afterEachMigrate
func TestChallenger_MultipleCallbacksPerEachMigrate(t *testing.T) {
	ctx := context.Background()

	mockFS := fstest.MapFS{
		"migrations/beforeEachMigrate__2.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE bem_2 (v INT64);"),
		},
		"migrations/beforeEachMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE bem_plain (v INT64);"),
		},
		"migrations/beforeEachMigrate__1.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE bem_1 (v INT64);"),
		},
		"migrations/V1__one.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"migrations/V2__two.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t2 (id INT64);"),
		},
		"migrations/afterEachMigrate__z.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE aem_z (v INT64);"),
		},
		"migrations/afterEachMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE aem_plain (v INT64);"),
		},
		"migrations/afterEachMigrate__a.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE aem_a (v INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	db := mock.NewMockDatabase()

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if res.MigrationsExecuted != 2 {
		t.Fatalf("expected 2 migrations, got %d", res.MigrationsExecuted)
	}

	executedStmts := db.ExecutedStatements()

	expectedSequence := []string{
		// First migration (V1)
		"bem_plain",
		"bem_1",
		"bem_2",
		"CREATE TABLE t1",
		"aem_plain",
		"aem_a",
		"aem_z",
		// Second migration (V2)
		"bem_plain",
		"bem_1",
		"bem_2",
		"CREATE TABLE t2",
		"aem_plain",
		"aem_a",
		"aem_z",
	}

	var matched []string
	for _, s := range executedStmts {
		for _, exp := range []string{"bem_plain", "bem_1", "bem_2", "CREATE TABLE t1", "CREATE TABLE t2", "aem_plain", "aem_a", "aem_z"} {
			if strings.Contains(s, exp) {
				matched = append(matched, exp)
				break
			}
		}
	}

	if len(matched) != len(expectedSequence) {
		t.Fatalf("expected %d matched events, got %d: %v", len(expectedSequence), len(matched), matched)
	}

	for i, exp := range expectedSequence {
		if matched[i] != exp {
			t.Errorf("step %d mismatch: expected %s, got %s", i, exp, matched[i])
		}
	}
}

// 3. Callback failure halts execution chain immediately
func TestChallenger_CallbackHaltsExecutionImmediately(t *testing.T) {
	ctx := context.Background()

	mockFS := fstest.MapFS{
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE before_ok (id INT64);"),
		},
		"migrations/beforeMigrate__a_fail.sql": &fstest.MapFile{
			Data: []byte("FAIL_CALLBACK_STATEMENT;"),
		},
		"migrations/beforeMigrate__b_never.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE before_never (id INT64);"),
		},
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/afterMigrateError.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_after_error (id INT64);"),
		},
		"migrations/afterMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_after_success (id INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	db := mock.NewMockDatabase()
	db.SetFailOnExecute("FAIL_CALLBACK_STATEMENT")

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err == nil {
		t.Fatalf("expected error from failing callback, got success")
	}
	if res != nil && res.Success {
		t.Fatalf("expected result not success")
	}

	stmts := db.ExecutedStatements()

	hasBeforeOk := false
	hasBeforeNever := false
	hasUsers := false
	hasAfterError := false
	hasAfterSuccess := false

	for _, s := range stmts {
		if strings.Contains(s, "before_ok") {
			hasBeforeOk = true
		}
		if strings.Contains(s, "before_never") {
			hasBeforeNever = true
		}
		if strings.Contains(s, "CREATE TABLE users") {
			hasUsers = true
		}
		if strings.Contains(s, "audit_after_error") {
			hasAfterError = true
		}
		if strings.Contains(s, "audit_after_success") {
			hasAfterSuccess = true
		}
	}

	if !hasBeforeOk {
		t.Errorf("expected before_ok to execute before the failure")
	}
	if hasBeforeNever {
		t.Errorf("before_never MUST NOT execute after callback failure")
	}
	if hasUsers {
		t.Errorf("migration V1 MUST NOT execute after callback failure")
	}
	if !hasAfterError {
		t.Errorf("afterMigrateError MUST execute on callback failure")
	}
	if hasAfterSuccess {
		t.Errorf("afterMigrate MUST NOT execute on callback failure")
	}
}

// 4. Zero-migration runs: execute Migrate() with 0 migrations
func TestChallenger_ZeroMigrationRun_FreshDB(t *testing.T) {
	ctx := context.Background()

	// Only callbacks exist, zero migrations (no V, no R)
	mockFS := fstest.MapFS{
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE zm_before_migrate (v INT64);"),
		},
		"migrations/afterMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE zm_after_migrate (v INT64);"),
		},
		"migrations/beforeEachMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE zm_bem (v INT64);"),
		},
		"migrations/afterEachMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE zm_aem (v INT64);"),
		},
		"migrations/afterVersioned.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE zm_after_versioned (v INT64);"),
		},
		"migrations/beforeRepeatables.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE zm_before_repeatables (v INT64);"),
		},
		"migrations/afterMigrateApplied.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE zm_after_migrate_applied (v INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	db := mock.NewMockDatabase()

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success")
	}
	if res.MigrationsExecuted != 0 {
		t.Fatalf("expected 0 migrations executed, got %d", res.MigrationsExecuted)
	}

	stmts := db.ExecutedStatements()

	hasBeforeMigrate := false
	hasAfterMigrate := false
	hasBEM := false
	hasAEM := false
	hasAfterVersioned := false
	hasBeforeRepeatables := false
	hasAfterMigrateApplied := false

	for _, s := range stmts {
		if strings.Contains(s, "zm_before_migrate") {
			hasBeforeMigrate = true
		}
		if strings.Contains(s, "zm_after_migrate") {
			hasAfterMigrate = true
		}
		if strings.Contains(s, "zm_bem") {
			hasBEM = true
		}
		if strings.Contains(s, "zm_aem") {
			hasAEM = true
		}
		if strings.Contains(s, "zm_after_versioned") {
			hasAfterVersioned = true
		}
		if strings.Contains(s, "zm_before_repeatables") {
			hasBeforeRepeatables = true
		}
		if strings.Contains(s, "zm_after_migrate_applied") {
			hasAfterMigrateApplied = true
		}
	}

	if !hasBeforeMigrate {
		t.Errorf("expected beforeMigrate to execute on zero-migration run")
	}
	if !hasAfterMigrate {
		t.Errorf("expected afterMigrate to execute on zero-migration run")
	}
	if hasBEM {
		t.Errorf("beforeEachMigrate MUST NOT execute when 0 migrations are run")
	}
	if hasAEM {
		t.Errorf("afterEachMigrate MUST NOT execute when 0 migrations are run")
	}
	if hasAfterVersioned {
		t.Errorf("afterVersioned MUST NOT execute when 0 migrations are run")
	}
	if hasBeforeRepeatables {
		t.Errorf("beforeRepeatables MUST NOT execute when 0 migrations are run")
	}
	if hasAfterMigrateApplied {
		t.Errorf("afterMigrateApplied MUST NOT execute when 0 migrations are run")
	}
}

// Zero-migration run when all migrations are already applied
func TestChallenger_ZeroMigrationRun_AlreadyUpToDate(t *testing.T) {
	ctx := context.Background()

	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE zm2_before (v INT64);"),
		},
		"migrations/afterMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE zm2_after (v INT64);"),
		},
		"migrations/afterMigrateApplied.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE zm2_applied (v INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	db := mock.NewMockDatabase()

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// First run applies V1
	res1, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("first migrate failed: %v", err)
	}
	if res1.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration on first run, got %d", res1.MigrationsExecuted)
	}

	// Reset tracked statements
	db.ExecutedStatements() // drain or check

	// Second run: up-to-date, 0 migrations pending!
	res2, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
	if res2.MigrationsExecuted != 0 {
		t.Fatalf("expected 0 migrations on second run, got %d", res2.MigrationsExecuted)
	}

	// Look at statements executed since first run:
	stmts := db.ExecutedStatements()
	hasBefore := false
	hasAfter := false
	hasApplied := false

	// Count occurrences of zm2_before and zm2_after
	beforeCount := 0
	afterCount := 0
	for _, s := range stmts {
		if strings.Contains(s, "zm2_before") {
			beforeCount++
			hasBefore = true
		}
		if strings.Contains(s, "zm2_after") {
			afterCount++
			hasAfter = true
		}
		if strings.Contains(s, "zm2_applied") {
			hasApplied = true
		}
	}

	if beforeCount != 2 {
		t.Errorf("expected beforeMigrate to run in both runs (total count 2), got %d", beforeCount)
	}
	if afterCount != 2 {
		t.Errorf("expected afterMigrate to run in both runs (total count 2), got %d", afterCount)
	}
	// zm2_applied was run in first run (count 1), but should NOT run again in second run
	appliedCount := 0
	for _, s := range stmts {
		if strings.Contains(s, "zm2_applied") {
			appliedCount++
		}
	}
	if appliedCount != 1 {
		t.Errorf("expected afterMigrateApplied only on first run (count 1), got %d", appliedCount)
	}
	_ = hasBefore
	_ = hasAfter
	_ = hasApplied
}

// 5. Validate() lifecycle: confirm beforeValidate and afterValidate execute on valid,
// afterValidateError executes on invalid.
func TestChallenger_ValidateLifecycle_ValidAndInvalidStates(t *testing.T) {
	ctx := context.Background()

	t.Run("Valid state fires beforeValidate and afterValidate", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/beforeValidate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_before (v INT64);"),
			},
			"migrations/beforeValidate__step2.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_before_2 (v INT64);"),
			},
			"migrations/afterValidate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_after (v INT64);"),
			},
			"migrations/afterValidateError.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_after_error (v INT64);"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = mockFS
		cfg.Locations = []string{"migrations"}
		db := mock.NewMockDatabase()

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		// Apply V1 first so DB has valid history
		_, err = m.Migrate(ctx)
		if err != nil {
			t.Fatalf("migrate failed: %v", err)
		}

		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if !valRes.Valid {
			t.Fatalf("expected valid state, got errors: %v", valRes.Errors)
		}

		stmts := db.ExecutedStatements()
		hasBefore := false
		hasBefore2 := false
		hasAfter := false
		hasAfterErr := false

		for _, s := range stmts {
			if strings.Contains(s, "val_before") && !strings.Contains(s, "val_before_2") {
				hasBefore = true
			}
			if strings.Contains(s, "val_before_2") {
				hasBefore2 = true
			}
			if strings.Contains(s, "val_after") && !strings.Contains(s, "val_after_error") {
				hasAfter = true
			}
			if strings.Contains(s, "val_after_error") {
				hasAfterErr = true
			}
		}

		if !hasBefore || !hasBefore2 {
			t.Errorf("expected both beforeValidate callbacks to execute: hasBefore=%v hasBefore2=%v", hasBefore, hasBefore2)
		}
		if !hasAfter {
			t.Errorf("expected afterValidate to execute on valid state")
		}
		if hasAfterErr {
			t.Errorf("afterValidateError MUST NOT execute on valid state")
		}
	})

	t.Run("Invalid state (checksum mismatch) fires beforeValidate and afterValidateError", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/beforeValidate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_inv_before (v INT64);"),
			},
			"migrations/afterValidate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_inv_after (v INT64);"),
			},
			"migrations/afterValidateError.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_inv_after_error (v INT64);"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = mockFS
		cfg.Locations = []string{"migrations"}
		db := mock.NewMockDatabase()

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		// Apply V1
		_, err = m.Migrate(ctx)
		if err != nil {
			t.Fatalf("migrate failed: %v", err)
		}

		// Tamper with local migration file content to induce checksum mismatch
		mockFS["migrations/V1__init.sql"] = &fstest.MapFile{
			Data: []byte("CREATE TABLE users_TAMPERED (id INT64);"),
		}

		initialCount := len(db.ExecutedStatements())
		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate returned unexpected err: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected invalid state due to checksum mismatch")
		}

		stmts := db.ExecutedStatements()[initialCount:]
		hasBefore := false
		hasAfter := false
		hasAfterErr := false

		for _, s := range stmts {
			if strings.Contains(s, "val_inv_before") {
				hasBefore = true
			}
			if strings.Contains(s, "val_inv_after") && !strings.Contains(s, "val_inv_after_error") {
				hasAfter = true
			}
			if strings.Contains(s, "val_inv_after_error") {
				hasAfterErr = true
			}
		}

		if !hasBefore {
			t.Errorf("expected beforeValidate to execute")
		}
		if !hasAfterErr {
			t.Errorf("expected afterValidateError to execute on invalid state")
		}
		if hasAfter {
			t.Errorf("afterValidate MUST NOT execute on invalid state")
		}
	})

	t.Run("Invalid state (missing applied migration) fires afterValidateError", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/beforeValidate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_miss_before (v INT64);"),
			},
			"migrations/afterValidate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_miss_after (v INT64);"),
			},
			"migrations/afterValidateError.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_miss_after_error (v INT64);"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = mockFS
		cfg.Locations = []string{"migrations"}
		db := mock.NewMockDatabase()

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		// Apply V1
		_, err = m.Migrate(ctx)
		if err != nil {
			t.Fatalf("migrate failed: %v", err)
		}

		// Delete V1 from local FS
		delete(mockFS, "migrations/V1__init.sql")

		initialCount := len(db.ExecutedStatements())
		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate returned unexpected err: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected invalid state due to missing applied migration")
		}

		stmts := db.ExecutedStatements()[initialCount:]
		hasBefore := false
		hasAfter := false
		hasAfterErr := false

		for _, s := range stmts {
			if strings.Contains(s, "val_miss_before") {
				hasBefore = true
			}
			if strings.Contains(s, "val_miss_after") && !strings.Contains(s, "val_miss_after_error") {
				hasAfter = true
			}
			if strings.Contains(s, "val_miss_after_error") {
				hasAfterErr = true
			}
		}

		if !hasBefore {
			t.Errorf("expected beforeValidate to execute")
		}
		if !hasAfterErr {
			t.Errorf("expected afterValidateError to execute on missing migration")
		}
		if hasAfter {
			t.Errorf("afterValidate MUST NOT execute on invalid state")
		}
	})

	t.Run("Failure in beforeValidate halts and fires afterValidateError", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/beforeValidate.sql": &fstest.MapFile{
				Data: []byte("FAIL_VALIDATE_CALLBACK;"),
			},
			"migrations/afterValidate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_fail_after (v INT64);"),
			},
			"migrations/afterValidateError.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE val_fail_after_error (v INT64);"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = mockFS
		cfg.Locations = []string{"migrations"}
		db := mock.NewMockDatabase()
		db.SetFailOnExecute("FAIL_VALIDATE_CALLBACK")

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		_, err = m.Validate(ctx)
		if err == nil {
			t.Fatalf("expected error from failing beforeValidate")
		}

		stmts := db.ExecutedStatements()
		hasAfter := false
		hasAfterErr := false

		for _, s := range stmts {
			if strings.Contains(s, "val_fail_after") && !strings.Contains(s, "val_fail_after_error") {
				hasAfter = true
			}
			if strings.Contains(s, "val_fail_after_error") {
				hasAfterErr = true
			}
		}

		if !hasAfterErr {
			t.Errorf("expected afterValidateError to execute when beforeValidate fails")
		}
		if hasAfter {
			t.Errorf("afterValidate MUST NOT execute when beforeValidate fails")
		}
	})
}

// 6. Info() lifecycle: confirm beforeInfo and afterInfo execute on success,
// and afterInfoError executes on failure.
func TestChallenger_InfoLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("Successful Info fires beforeInfo and afterInfo in description order", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64);"),
			},
			"migrations/beforeInfo.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE info_before_plain (v INT64);"),
			},
			"migrations/beforeInfo__z.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE info_before_z (v INT64);"),
			},
			"migrations/beforeInfo__a.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE info_before_a (v INT64);"),
			},
			"migrations/afterInfo.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE info_after (v INT64);"),
			},
			"migrations/afterInfoError.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE info_after_error (v INT64);"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = mockFS
		cfg.Locations = []string{"migrations"}
		db := mock.NewMockDatabase()

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		infoRes, err := m.Info(ctx)
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		if infoRes == nil {
			t.Fatalf("expected non-nil InfoResult")
		}

		stmts := db.ExecutedStatements()

		expectedSequence := []string{
			"info_before_plain", // desc: ""
			"info_before_a",     // desc: "a"
			"info_before_z",     // desc: "z"
			"info_after",        // afterInfo
		}

		var matched []string
		for _, s := range stmts {
			for _, exp := range expectedSequence {
				if strings.Contains(s, exp) {
					matched = append(matched, exp)
					break
				}
			}
		}

		if len(matched) != len(expectedSequence) {
			t.Fatalf("expected %d info callback statements, got %d: %v", len(expectedSequence), len(matched), matched)
		}

		for i, exp := range expectedSequence {
			if matched[i] != exp {
				t.Errorf("step %d mismatch: expected %s, got %s", i, exp, matched[i])
			}
		}

		// Ensure afterInfoError did not execute
		for _, s := range stmts {
			if strings.Contains(s, "info_after_error") {
				t.Errorf("afterInfoError MUST NOT execute on successful Info")
			}
		}
	})

	t.Run("Failing beforeInfo callback fires afterInfoError", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/beforeInfo.sql": &fstest.MapFile{
				Data: []byte("FAIL_INFO_STATEMENT;"),
			},
			"migrations/afterInfo.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE info_fail_after (v INT64);"),
			},
			"migrations/afterInfoError.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE info_fail_after_error (v INT64);"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = mockFS
		cfg.Locations = []string{"migrations"}
		db := mock.NewMockDatabase()
		db.SetFailOnExecute("FAIL_INFO_STATEMENT")

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		_, err = m.Info(ctx)
		if err == nil {
			t.Fatalf("expected error from failing beforeInfo")
		}

		stmts := db.ExecutedStatements()
		hasAfter := false
		hasAfterErr := false

		for _, s := range stmts {
			if strings.Contains(s, "info_fail_after") && !strings.Contains(s, "info_fail_after_error") {
				hasAfter = true
			}
			if strings.Contains(s, "info_fail_after_error") {
				hasAfterErr = true
			}
		}

		if !hasAfterErr {
			t.Errorf("expected afterInfoError to execute when beforeInfo fails")
		}
		if hasAfter {
			t.Errorf("afterInfo MUST NOT execute when beforeInfo fails")
		}
	})
}

// 7. Complete lifecycle sequence with versioned and repeatables:
// beforeMigrate -> beforeEachMigrate -> migration -> afterEachMigrate ->
// afterVersioned -> beforeRepeatables -> beforeEachMigrate -> repeatable ->
// afterEachMigrate -> afterMigrateApplied -> afterMigrate
func TestChallenger_CompleteLifecycleSequenceWithVersionedAndRepeatables(t *testing.T) {
	ctx := context.Background()

	mockFS := fstest.MapFS{
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE hook_before_migrate (v INT64);"),
		},
		"migrations/beforeEachMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE hook_before_each_migrate (v INT64);"),
		},
		"migrations/V1__first.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE target_v1 (id INT64);"),
		},
		"migrations/afterEachMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE hook_after_each_migrate (v INT64);"),
		},
		"migrations/afterVersioned.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE hook_after_versioned (v INT64);"),
		},
		"migrations/beforeRepeatables.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE hook_before_repeatables (v INT64);"),
		},
		"migrations/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW target_r AS SELECT 1;"),
		},
		"migrations/afterMigrateApplied.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE hook_after_migrate_applied (v INT64);"),
		},
		"migrations/afterMigrate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE hook_after_migrate (v INT64);"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	db := mock.NewMockDatabase()

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if res.MigrationsExecuted != 2 {
		t.Fatalf("expected 2 migrations executed (1 versioned + 1 repeatable), got %d", res.MigrationsExecuted)
	}

	stmts := db.ExecutedStatements()

	expectedOrder := []string{
		"hook_before_migrate",
		"hook_before_each_migrate",
		"CREATE TABLE target_v1",
		"hook_after_each_migrate",
		"hook_after_versioned",
		"hook_before_repeatables",
		"hook_before_each_migrate",
		"CREATE VIEW target_r",
		"hook_after_each_migrate",
		"hook_after_migrate_applied",
		"hook_after_migrate",
	}

	var matched []string
	for _, s := range stmts {
		for _, exp := range expectedOrder {
			if strings.Contains(s, exp) {
				matched = append(matched, exp)
				break
			}
		}
	}

	if len(matched) != len(expectedOrder) {
		t.Fatalf("expected %d lifecycle steps, got %d: %v", len(expectedOrder), len(matched), matched)
	}

	for i, exp := range expectedOrder {
		if matched[i] != exp {
			t.Errorf("lifecycle sequence mismatch at index %d: expected %s, got %s (sequence: %v)",
				i, exp, matched[i], matched)
		}
	}
}

// 8. Error in afterVersioned or beforeRepeatables triggers afterMigrateError
func TestChallenger_PhaseBoundaryCallbackErrorsTriggerAfterMigrateError(t *testing.T) {
	ctx := context.Background()

	t.Run("afterVersioned failure triggers afterMigrateError and halts repeatables", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/V1__first.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1 (id INT64);"),
			},
			"migrations/afterVersioned.sql": &fstest.MapFile{
				Data: []byte("FAIL_AFTER_VERSIONED;"),
			},
			"migrations/R__view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW target_r_never AS SELECT 1;"),
			},
			"migrations/afterMigrateError.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE err_hook (v INT64);"),
			},
			"migrations/afterMigrate.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE success_hook (v INT64);"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = mockFS
		cfg.Locations = []string{"migrations"}
		db := mock.NewMockDatabase()
		db.SetFailOnExecute("FAIL_AFTER_VERSIONED")

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error from failing afterVersioned")
		}
		if res != nil && res.Success {
			t.Fatalf("expected failure result")
		}

		stmts := db.ExecutedStatements()
		hasV1 := false
		hasR := false
		hasErrHook := false
		hasSuccessHook := false

		for _, s := range stmts {
			if strings.Contains(s, "CREATE TABLE t1") {
				hasV1 = true
			}
			if strings.Contains(s, "target_r_never") {
				hasR = true
			}
			if strings.Contains(s, "err_hook") {
				hasErrHook = true
			}
			if strings.Contains(s, "success_hook") {
				hasSuccessHook = true
			}
		}

		if !hasV1 {
			t.Errorf("V1 should have executed before afterVersioned")
		}
		if hasR {
			t.Errorf("Repeatable migration MUST NOT execute after afterVersioned failure")
		}
		if !hasErrHook {
			t.Errorf("afterMigrateError MUST execute on afterVersioned failure")
		}
		if hasSuccessHook {
			t.Errorf("afterMigrate MUST NOT execute on failure")
		}
	})

	t.Run("beforeRepeatables failure triggers afterMigrateError and halts repeatables", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/V1__first.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE t1 (id INT64);"),
			},
			"migrations/beforeRepeatables.sql": &fstest.MapFile{
				Data: []byte("FAIL_BEFORE_REPEATABLES;"),
			},
			"migrations/R__view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW target_r_never AS SELECT 1;"),
			},
			"migrations/afterMigrateError.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE err_hook (v INT64);"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_schema"
		cfg.FS = mockFS
		cfg.Locations = []string{"migrations"}
		db := mock.NewMockDatabase()
		db.SetFailOnExecute("FAIL_BEFORE_REPEATABLES")

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error from failing beforeRepeatables")
		}
		if res != nil && res.Success {
			t.Fatalf("expected failure result")
		}

		stmts := db.ExecutedStatements()
		hasR := false
		hasErrHook := false

		for _, s := range stmts {
			if strings.Contains(s, "target_r_never") {
				hasR = true
			}
			if strings.Contains(s, "err_hook") {
				hasErrHook = true
			}
		}

		if hasR {
			t.Errorf("Repeatable migration MUST NOT execute after beforeRepeatables failure")
		}
		if !hasErrHook {
			t.Errorf("afterMigrateError MUST execute on beforeRepeatables failure")
		}
	})
}
