package migrator

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/RoryQ/noway/pkg/checksum"
	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database/mock"
)

func TestAdversarial_MigratePlaceholderConfOverridesAndEscapes(t *testing.T) {
	mockFS := fstest.MapFS{
		// V1 has placeholderReplacement=false in .conf, and contains an unconfigured placeholder ${unconfigured_token}
		"sql/V1__raw_bq.sql": &fstest.MapFile{
			Data: []byte("SELECT '${unconfigured_token}' AS val;"),
		},
		"sql/V1__raw_bq.sql.conf": &fstest.MapFile{
			Data: []byte("placeholderReplacement=false"),
		},
		// V2 has normal placeholder replacement with both replaced ${tbl} and escaped $${not_a_var}
		"sql/V2__normal.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${tbl} (col STRING); SELECT '$${not_a_var}';"),
		},
		// R1 has placeholderReplacement=false and contains ${another_unconfigured}
		"sql/R__repeatable_raw.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v_raw AS SELECT '${another_unconfigured}';"),
		},
		"sql/R__repeatable_raw.sql.conf": &fstest.MapFile{
			Data: []byte("placeholderReplacement=false"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = mockFS
	cfg.Placeholders = map[string]string{
		"tbl": "real_table",
	}

	db := mock.NewMockDatabase()
	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	ctx := context.Background()
	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("expected Migrate to succeed with .sql.conf placeholderReplacement=false overrides, got: %v", err)
	}
	if res.MigrationsExecuted != 3 {
		t.Fatalf("expected 3 migrations executed, got %d", res.MigrationsExecuted)
	}

	// Verify executed statements
	execStmts := db.ExecutedStatements()
	joinedStmts := strings.Join(execStmts, "\n---\n")

	// V1 must have executed raw SQL containing '${unconfigured_token}'
	if !strings.Contains(joinedStmts, "SELECT '${unconfigured_token}' AS val") {
		t.Errorf("expected V1 to execute raw statement with unconfigured_token, executed statements:\n%s", joinedStmts)
	}

	// V2 must have executed replaced table name 'real_table' and unescaped '$${not_a_var}' to '${not_a_var}'
	if !strings.Contains(joinedStmts, "CREATE TABLE real_table") {
		t.Errorf("expected V2 to execute with 'real_table', executed:\n%s", joinedStmts)
	}
	if !strings.Contains(joinedStmts, "SELECT '${not_a_var}'") {
		t.Errorf("expected V2 to execute with unescaped '${not_a_var}', executed:\n%s", joinedStmts)
	}

	// R1 must have executed raw SQL containing '${another_unconfigured}'
	if !strings.Contains(joinedStmts, "CREATE VIEW v_raw AS SELECT '${another_unconfigured}'") {
		t.Errorf("expected R1 to execute raw statement with '${another_unconfigured}', executed:\n%s", joinedStmts)
	}

	// Verify checksums in history table
	history, err := db.FetchHistory(ctx, "test_schema", "flyway_schema_history")
	if err != nil {
		t.Fatalf("failed to fetch history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history records, got %d", len(history))
	}

	// R1 checksum in history table must be RAW checksum since placeholderReplacement=false
	r1RawChecksum, _ := checksum.CalculateString("CREATE VIEW v_raw AS SELECT '${another_unconfigured}';")
	for _, h := range history {
		if h.Script == "R__repeatable_raw.sql" {
			if h.Checksum == nil || *h.Checksum != r1RawChecksum {
				t.Errorf("expected R1 checksum %d, got %v", r1RawChecksum, h.Checksum)
			}
		}
	}
}

func TestAdversarial_MigrateFailsOnUnconfiguredPlaceholderWithoutConf(t *testing.T) {
	mockFS := fstest.MapFS{
		// V1 has unconfigured placeholder and NO .conf override -> MUST fail at Migrate() time
		"sql/V1__missing.sql": &fstest.MapFile{
			Data: []byte("SELECT '${fatal_missing_var}';"),
		},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_schema"
	cfg.FS = mockFS
	cfg.Placeholders = map[string]string{}

	db := mock.NewMockDatabase()
	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	ctx := context.Background()
	_, err = m.Migrate(ctx)
	if err == nil {
		t.Fatalf("expected Migrate to fail due to missing placeholder, got nil")
	}
	if !strings.Contains(err.Error(), "fatal_missing_var") {
		t.Fatalf("expected error to mention 'fatal_missing_var', got: %v", err)
	}
}

func TestAdversarial_PlaceholderChangeRepeatableRerunVsRawConf(t *testing.T) {
	mockFS := fstest.MapFS{
		// V1 has ${tbl} -> raw checksum
		"sql/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${tbl} (id INT64);"),
		},
		// R1 has ${tbl} -> replaced checksum
		"sql/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT * FROM ${tbl};"),
		},
		// R2 has ${tbl} but placeholderReplacement=false -> raw checksum
		"sql/R__raw_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v_raw AS SELECT * FROM ${tbl};"),
		},
		"sql/R__raw_view.sql.conf": &fstest.MapFile{
			Data: []byte("placeholderReplacement=false"),
		},
	}

	cfg1 := config.NewDefaultConfiguration()
	cfg1.DefaultSchema = "test_schema"
	cfg1.FS = mockFS
	cfg1.Placeholders = map[string]string{
		"tbl": "users_v1",
	}

	db := mock.NewMockDatabase()
	m1, err := New(cfg1, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	ctx := context.Background()
	res1, err := m1.Migrate(ctx)
	if err != nil {
		t.Fatalf("initial Migrate failed: %v", err)
	}
	if res1.MigrationsExecuted != 3 {
		t.Fatalf("expected 3 migrations executed initially, got %d", res1.MigrationsExecuted)
	}

	// Now modify the placeholder tbl in config to users_v2
	cfg2 := config.NewDefaultConfiguration()
	cfg2.DefaultSchema = "test_schema"
	cfg2.FS = mockFS
	cfg2.Placeholders = map[string]string{
		"tbl": "users_v2",
	}

	m2, err := New(cfg2, db)
	if err != nil {
		t.Fatalf("failed to create second migrator: %v", err)
	}

	// Validate must pass: V1 checksum is RAW so it hasn't changed; repeatables do not fail validation on checksum change
	valRes, err := m2.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !valRes.Valid {
		t.Fatalf("expected Validate to succeed after placeholder change, got errors: %s", valRes.Error())
	}

	// Migrate with new placeholder:
	// - V1 does NOT re-run
	// - R1 (with replaced checksum) MUST re-run because its checksum changed from users_v1 to users_v2
	// - R2 (with placeholderReplacement=false) does NOT re-run because its raw checksum is unchanged
	res2, err := m2.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
	if res2.MigrationsExecuted != 1 {
		t.Fatalf("expected exactly 1 migration executed (R__view.sql), got %d", res2.MigrationsExecuted)
	}
	if len(res2.ExecutedMigrations) != 1 || res2.ExecutedMigrations[0].Script != "R__view.sql" {
		t.Fatalf("expected executed migration to be R__view.sql, got: %v", res2.ExecutedMigrations)
	}
}
