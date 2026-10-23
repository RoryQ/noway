package resolver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/roryq/noway/pkg/checksum"
	"github.com/roryq/noway/pkg/parser"
)

func TestResolverFS(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V1.1__add_col.sql": &fstest.MapFile{
			Data: []byte("ALTER TABLE users ADD COLUMN name STRING;"),
		},
		"migrations/V2.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE logs (id INT64);"),
		},
		"migrations/R__views.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW user_view AS SELECT * FROM users;"),
		},
		"migrations/U1.1__undo_add_col.sql": &fstest.MapFile{
			Data: []byte("ALTER TABLE users DROP COLUMN name;"),
		},
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("-- before migrate hook"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if len(resolved.VersionedMigrations) != 3 {
		t.Errorf("expected 3 versioned migrations, got %d", len(resolved.VersionedMigrations))
	}
	if resolved.VersionedMigrations[0].Version.String() != "1" {
		t.Errorf("expected V1 first, got %s", resolved.VersionedMigrations[0].Version.String())
	}
	if resolved.VersionedMigrations[1].Version.String() != "1.1" {
		t.Errorf("expected V1.1 second, got %s", resolved.VersionedMigrations[1].Version.String())
	}
	if resolved.VersionedMigrations[2].Version.String() != "2" || resolved.VersionedMigrations[2].Description != "" {
		t.Errorf("expected V2 with empty description, got %+v", resolved.VersionedMigrations[2])
	}

	if len(resolved.RepeatableMigrations) != 1 {
		t.Errorf("expected 1 repeatable migration, got %d", len(resolved.RepeatableMigrations))
	}
	if resolved.RepeatableMigrations[0].Description != "views" {
		t.Errorf("expected desc 'views', got %s", resolved.RepeatableMigrations[0].Description)
	}

	if len(resolved.UndoMigrations) != 1 {
		t.Errorf("expected 1 undo migration, got %d", len(resolved.UndoMigrations))
	}

	if len(resolved.Callbacks["beforeMigrate"]) != 1 {
		t.Errorf("expected beforeMigrate callback")
	}
}

func TestDuplicateVersions(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V1_0__duplicate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users2 (id INT64);"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	_, err := res.Resolve()
	if err == nil {
		t.Errorf("expected error for duplicate versions (1 and 1.0), got nil")
	}
}

func TestResolverNonMigrationFiles(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/Views.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT 1;"),
		},
		"migrations/Validate.sql": &fstest.MapFile{
			Data: []byte("SELECT 1;"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("expected Resolve to succeed ignoring non-migration files, got: %v", err)
	}

	if len(resolved.VersionedMigrations) != 1 {
		t.Errorf("expected 1 versioned migration, got %d", len(resolved.VersionedMigrations))
	}
}

func TestResolverScriptMigrationsAndSqlConf(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V1__init.sql.conf": &fstest.MapFile{
			Data: []byte("shouldExecute=${env} == 'production'\nexecuteInTransaction=false"),
		},
		"migrations/V2__load_data.sh": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho 'Loading BigQuery seed data'"),
		},
		"migrations/V2__load_data.sh.conf": &fstest.MapFile{
			Data: []byte("shouldExecute=true"),
		},
		"migrations/R__refresh_cache.bash": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho 'Refreshing cache'"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if len(resolved.VersionedMigrations) != 2 {
		t.Fatalf("expected 2 versioned migrations, got %d", len(resolved.VersionedMigrations))
	}

	// Check V1 (SQL)
	v1 := resolved.VersionedMigrations[0]
	if v1.Type != TypeSQL || v1.IsScript {
		t.Errorf("expected V1 to be TypeSQL, got %s (isScript=%v)", v1.Type, v1.IsScript)
	}
	if v1.Config.ShouldExecute != "${env} == 'production'" {
		t.Errorf("expected V1 shouldExecute '${env} == \\'production\\'', got %q", v1.Config.ShouldExecute)
	}
	if v1.Config.ExecuteInTransaction == nil || *v1.Config.ExecuteInTransaction != false {
		t.Errorf("expected V1 executeInTransaction to be false, got %v", v1.Config.ExecuteInTransaction)
	}

	// Check V2 (Bash Script)
	v2 := resolved.VersionedMigrations[1]
	if v2.Type != TypeScript || !v2.IsScript {
		t.Errorf("expected V2 to be TypeScript, got %s (isScript=%v)", v2.Type, v2.IsScript)
	}
	if v2.Config.ShouldExecute != "true" {
		t.Errorf("expected V2 shouldExecute 'true', got %q", v2.Config.ShouldExecute)
	}

	// Check Repeatable Bash script
	if len(resolved.RepeatableMigrations) != 1 {
		t.Fatalf("expected 1 repeatable migration, got %d", len(resolved.RepeatableMigrations))
	}
	r1 := resolved.RepeatableMigrations[0]
	if r1.Type != TypeScript || !r1.IsScript {
		t.Errorf("expected repeatable to be TypeScript, got %s", r1.Type)
	}
}

func TestResolverPlaceholderChecksum(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${tbl} (id INT64);"),
		},
		"migrations/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT * FROM ${tbl};"),
		},
		"migrations/R__raw_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v2 AS SELECT * FROM ${tbl};"),
		},
		"migrations/R__raw_view.sql.conf": &fstest.MapFile{
			Data: []byte("placeholderReplacement=false"),
		},
	}

	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"tbl": "users",
		},
	})

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
		Replacer:  replacer,
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if len(resolved.VersionedMigrations) != 1 {
		t.Fatalf("expected 1 versioned migration, got %d", len(resolved.VersionedMigrations))
	}
	if len(resolved.RepeatableMigrations) != 2 {
		t.Fatalf("expected 2 repeatable migrations, got %d", len(resolved.RepeatableMigrations))
	}

	// V1 (versioned) matches Flyway SqlMigrationResolver: checksum is calculated from RAW content
	expectedV1Checksum, _ := checksum.CalculateString("CREATE TABLE ${tbl} (id INT64);")
	if resolved.VersionedMigrations[0].Checksum != expectedV1Checksum {
		t.Errorf("expected V1 raw checksum %d, got %d", expectedV1Checksum, resolved.VersionedMigrations[0].Checksum)
	}

	// Check repeatable migrations
	repeatablesByScript := make(map[string]ResolvedMigration)
	for _, rm := range resolved.RepeatableMigrations {
		repeatablesByScript[rm.Script] = rm
	}

	// Repeatable R__view has checksum calculated after placeholder replacement
	expectedRChecksum, _ := checksum.CalculateString("CREATE VIEW v AS SELECT * FROM users;")
	if repeatablesByScript["R__view.sql"].Checksum != expectedRChecksum {
		t.Errorf("expected R__view replaced checksum %d, got %d", expectedRChecksum, repeatablesByScript["R__view.sql"].Checksum)
	}

	// Repeatable R__raw_view has placeholderReplacement=false, so checksum is calculated from raw content
	expectedRawRChecksum, _ := checksum.CalculateString("CREATE VIEW v2 AS SELECT * FROM ${tbl};")
	if repeatablesByScript["R__raw_view.sql"].Checksum != expectedRawRChecksum {
		t.Errorf("expected R__raw_view raw checksum %d, got %d", expectedRawRChecksum, repeatablesByScript["R__raw_view.sql"].Checksum)
	}
}

// -------------------------------------------------------------------------
// Milestone 1 Resolver Parity & Regression Test Suite
// -------------------------------------------------------------------------

// TestResolverPlaceholderChecksum_ParityAndEscapes verifies:
// 1. Versioned migrations (V) use RAW checksum including $${var}.
// 2. Undo migrations (U) use RAW checksum matching versioned migrations.
// 3. Repeatable migrations (R) use REPLACED checksum where $${var} is unescaped to ${var}.
func TestResolverPlaceholderChecksum_ParityAndEscapes(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE ${tbl} (id INT64); SELECT '$${tbl}';"),
		},
		"migrations/U1__undo.sql": &fstest.MapFile{
			Data: []byte("DROP TABLE ${tbl}; SELECT '$${tbl}';"),
		},
		"migrations/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT * FROM ${tbl}; SELECT '$${tbl}';"),
		},
	}

	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"tbl": "users",
		},
	})

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
		Replacer:  replacer,
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if len(resolved.VersionedMigrations) != 1 {
		t.Fatalf("expected 1 versioned migration, got %d", len(resolved.VersionedMigrations))
	}
	if len(resolved.UndoMigrations) != 1 {
		t.Fatalf("expected 1 undo migration, got %d", len(resolved.UndoMigrations))
	}
	if len(resolved.RepeatableMigrations) != 1 {
		t.Fatalf("expected 1 repeatable migration, got %d", len(resolved.RepeatableMigrations))
	}

	// 1. V1 uses RAW checksum
	rawV1 := "CREATE TABLE ${tbl} (id INT64); SELECT '$${tbl}';"
	expectedV1Checksum, _ := checksum.CalculateString(rawV1)
	if resolved.VersionedMigrations[0].Checksum != expectedV1Checksum {
		t.Errorf("V1 checksum: got %d, want raw checksum %d", resolved.VersionedMigrations[0].Checksum, expectedV1Checksum)
	}

	// 2. U1 uses RAW checksum
	rawU1 := "DROP TABLE ${tbl}; SELECT '$${tbl}';"
	expectedU1Checksum, _ := checksum.CalculateString(rawU1)
	if resolved.UndoMigrations[0].Checksum != expectedU1Checksum {
		t.Errorf("U1 checksum: got %d, want raw checksum %d", resolved.UndoMigrations[0].Checksum, expectedU1Checksum)
	}

	// 3. Repeatable R__view uses REPLACED checksum:
	// ${tbl} -> users, $${tbl} -> ${tbl}
	replacedR := "CREATE VIEW v AS SELECT * FROM users; SELECT '${tbl}';"
	expectedRChecksum, _ := checksum.CalculateString(replacedR)
	if resolved.RepeatableMigrations[0].Checksum != expectedRChecksum {
		t.Errorf("R__view checksum: got %d, want replaced checksum %d", resolved.RepeatableMigrations[0].Checksum, expectedRChecksum)
	}
}

// TestResolverRepeatableMissingPlaceholderError verifies that when a repeatable
// migration contains an unresolvable placeholder without a default value,
// Resolve() returns an error rather than silently swallowing the error.
func TestResolverRepeatableMissingPlaceholderError(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/R__broken_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT * FROM ${missing_var};"),
		},
	}

	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values:  map[string]string{}, // missing_var not supplied
	})

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
		Replacer:  replacer,
	})

	_, err := res.Resolve()
	if err == nil {
		t.Fatalf("expected error from Resolve() when repeatable migration contains missing placeholder, got nil")
	}
}

// TestResolverGlobalPlaceholderReplacementOverride_Regression verifies that
// when global ResolverConfig.PlaceholderReplacement is disabled (*false),
// a migration with .conf placeholderReplacement=true is still substituted,
// while migrations without the override compute raw checksum.
func TestResolverGlobalPlaceholderReplacementOverride_Regression(t *testing.T) {
	disabled := false
	mockFS := fstest.MapFS{
		"migrations/R__custom.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v1 AS SELECT * FROM ${tbl};"),
		},
		"migrations/R__custom.sql.conf": &fstest.MapFile{
			Data: []byte("placeholderReplacement=true"),
		},
		"migrations/R__standard.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v2 AS SELECT * FROM ${tbl};"),
		},
	}

	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: false,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"tbl": "users",
		},
	})

	res := NewResolver(ResolverConfig{
		FS:                     mockFS,
		Locations:              []string{"migrations"},
		PlaceholderReplacement: &disabled,
		Replacer:               replacer,
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	repeatablesByScript := make(map[string]ResolvedMigration)
	for _, rm := range resolved.RepeatableMigrations {
		repeatablesByScript[rm.Script] = rm
	}

	// R__custom has placeholderReplacement=true override, so checksum is calculated from replaced content
	expectedCustomChecksum, _ := checksum.CalculateString("CREATE VIEW v1 AS SELECT * FROM users;")
	if repeatablesByScript["R__custom.sql"].Checksum != expectedCustomChecksum {
		t.Errorf("expected R__custom replaced checksum %d, got %d", expectedCustomChecksum, repeatablesByScript["R__custom.sql"].Checksum)
	}

	// R__standard respects global disabled, so checksum is calculated from raw content
	expectedStandardChecksum, _ := checksum.CalculateString("CREATE VIEW v2 AS SELECT * FROM ${tbl};")
	if repeatablesByScript["R__standard.sql"].Checksum != expectedStandardChecksum {
		t.Errorf("expected R__standard raw checksum %d, got %d", expectedStandardChecksum, repeatablesByScript["R__standard.sql"].Checksum)
	}
}

func TestDuplicateRepeatableDescriptions(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT 1;"),
		},
		"migrations/extra/R__view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT 2;"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	_, err := res.Resolve()
	if err == nil {
		t.Fatalf("expected error for duplicate repeatable description, got nil")
	}

	expectedPrefix := "found more than one repeatable migration with description 'view'\nOffenders:\n->"
	if !strings.Contains(err.Error(), expectedPrefix) {
		t.Errorf("expected error containing %q, got %q", expectedPrefix, err.Error())
	}
}

func TestRepeatablesOrdering(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/R__zebra.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW zebra AS SELECT 1;"),
		},
		"migrations/R__alpha.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW alpha AS SELECT 1;"),
		},
		"migrations/R__beta.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW beta AS SELECT 1;"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if len(resolved.RepeatableMigrations) != 3 {
		t.Fatalf("expected 3 repeatable migrations, got %d", len(resolved.RepeatableMigrations))
	}

	if resolved.RepeatableMigrations[0].Description != "alpha" {
		t.Errorf("expected alpha first, got %s", resolved.RepeatableMigrations[0].Description)
	}
	if resolved.RepeatableMigrations[1].Description != "beta" {
		t.Errorf("expected beta second, got %s", resolved.RepeatableMigrations[1].Description)
	}
	if resolved.RepeatableMigrations[2].Description != "zebra" {
		t.Errorf("expected zebra third, got %s", resolved.RepeatableMigrations[2].Description)
	}
}

func TestCumulativeBaselineDiscoveryAndSorting(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/B__2.0__second_base.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE base2 (id INT64);"),
		},
		"migrations/B1__first_base.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE base1 (id INT64);"),
		},
		"migrations/B__1.5__mid_base.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE base1_5 (id INT64);"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if len(resolved.BaselineMigrations) != 3 {
		t.Fatalf("expected 3 baseline migrations, got %d", len(resolved.BaselineMigrations))
	}

	// Verify ascending version sort: 1, 1.5, 2.0
	if resolved.BaselineMigrations[0].Version.String() != "1" {
		t.Errorf("expected version 1 first, got %s", resolved.BaselineMigrations[0].Version.String())
	}
	if resolved.BaselineMigrations[1].Version.String() != "1.5" {
		t.Errorf("expected version 1.5 second, got %s", resolved.BaselineMigrations[1].Version.String())
	}
	if resolved.BaselineMigrations[2].Version.String() != "2.0" {
		t.Errorf("expected version 2.0 third, got %s", resolved.BaselineMigrations[2].Version.String())
	}

	for _, bm := range resolved.BaselineMigrations {
		if bm.Type != TypeBaseline || !bm.IsBaseline {
			t.Errorf("expected TypeBaseline and IsBaseline=true, got type=%s isBaseline=%v", bm.Type, bm.IsBaseline)
		}
	}
}

func TestDuplicateBaselineVersions(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/B1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE base (id INT64);"),
		},
		"migrations/B__1.0__dup.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE base_dup (id INT64);"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	_, err := res.Resolve()
	if err == nil {
		t.Fatalf("expected error for duplicate baseline version, got nil")
	}

	expectedPrefix := "found more than one baseline migration with version 1\nOffenders:\n->"
	if !strings.Contains(err.Error(), expectedPrefix) {
		t.Errorf("expected error containing %q, got %q", expectedPrefix, err.Error())
	}
}

func TestResolver_NamedCallbackDiscovery(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("-- plain callback"),
		},
		"migrations/beforeMigrate__first_step.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE audit_first (id INT64);"),
		},
		"migrations/afterMigrate__01_clean.sh": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho clean\n"),
		},
		"migrations/afterVersioned.sql": &fstest.MapFile{
			Data: []byte("-- after versioned"),
		},
		"migrations/beforeRepeatables__setup.sql": &fstest.MapFile{
			Data: []byte("-- before repeatables"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	beforeMigrate := resolved.Callbacks["beforeMigrate"]
	if len(beforeMigrate) != 2 {
		t.Fatalf("expected 2 beforeMigrate callbacks, got %d", len(beforeMigrate))
	}
	// plain callback should sort first (empty description)
	if beforeMigrate[0].Filename != "beforeMigrate.sql" || beforeMigrate[0].Description != "" {
		t.Errorf("expected beforeMigrate.sql first with empty desc, got %+v", beforeMigrate[0])
	}
	if beforeMigrate[1].Filename != "beforeMigrate__first_step.sql" || beforeMigrate[1].Description != "first step" {
		t.Errorf("expected beforeMigrate__first_step.sql with 'first step' desc, got %+v", beforeMigrate[1])
	}

	afterMigrate := resolved.Callbacks["afterMigrate"]
	if len(afterMigrate) != 1 {
		t.Fatalf("expected 1 afterMigrate callback, got %d", len(afterMigrate))
	}
	if afterMigrate[0].Description != "01 clean" || !afterMigrate[0].IsScript {
		t.Errorf("expected 01 clean script callback, got %+v", afterMigrate[0])
	}

	if len(resolved.Callbacks["afterVersioned"]) != 1 {
		t.Errorf("expected 1 afterVersioned callback, got %d", len(resolved.Callbacks["afterVersioned"]))
	}
	if len(resolved.Callbacks["beforeRepeatables"]) != 1 {
		t.Errorf("expected 1 beforeRepeatables callback, got %d", len(resolved.Callbacks["beforeRepeatables"]))
	}
}

func TestResolver_CallbackDescriptionSorting(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/beforeMigrate__zebra.sql": &fstest.MapFile{
			Data: []byte("-- zebra"),
		},
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("-- plain"),
		},
		"migrations/beforeMigrate__alpha.sql": &fstest.MapFile{
			Data: []byte("-- alpha"),
		},
		"migrations/beforeMigrate__01_init.sql": &fstest.MapFile{
			Data: []byte("-- 01 init"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	cbs := resolved.Callbacks["beforeMigrate"]
	if len(cbs) != 4 {
		t.Fatalf("expected 4 callbacks, got %d", len(cbs))
	}

	expectedOrder := []struct {
		filename string
		desc     string
	}{
		{"beforeMigrate.sql", ""},
		{"beforeMigrate__01_init.sql", "01 init"},
		{"beforeMigrate__alpha.sql", "alpha"},
		{"beforeMigrate__zebra.sql", "zebra"},
	}

	for i, exp := range expectedOrder {
		if cbs[i].Filename != exp.filename || cbs[i].Description != exp.desc {
			t.Errorf("at index %d: expected %s (desc: %q), got %s (desc: %q)",
				i, exp.filename, exp.desc, cbs[i].Filename, cbs[i].Description)
		}
	}
}

func TestResolver_CallbackDeterministicTieBreaking(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/b/beforeMigrate__same.sql": &fstest.MapFile{
			Data: []byte("-- location b"),
		},
		"migrations/a/beforeMigrate__same.sql": &fstest.MapFile{
			Data: []byte("-- location a"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	cbs := resolved.Callbacks["beforeMigrate"]
	if len(cbs) != 2 {
		t.Fatalf("expected 2 callbacks, got %d", len(cbs))
	}

	if cbs[0].PhysicalLocation >= cbs[1].PhysicalLocation {
		t.Errorf("expected tie-breaking by physical location: %s < %s", cbs[0].PhysicalLocation, cbs[1].PhysicalLocation)
	}
}

func TestResolverRepeatableMigrationTypes(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/R__sql_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT 1;"),
		},
		"migrations/R__script_view.sh": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho 'running script'"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if len(resolved.RepeatableMigrations) != 2 {
		t.Fatalf("expected 2 repeatable migrations, got %d", len(resolved.RepeatableMigrations))
	}

	for _, rm := range resolved.RepeatableMigrations {
		if rm.Script == "R__sql_view.sql" {
			if rm.Type != TypeSQL {
				t.Errorf("expected R__sql_view.sql to resolve with Type %q (Flyway parity), got %q", TypeSQL, rm.Type)
			}
			if !rm.IsRepeatable {
				t.Errorf("expected IsRepeatable to be true")
			}
			if rm.IsScript {
				t.Errorf("expected IsScript to be false for .sql")
			}
		} else if rm.Script == "R__script_view.sh" {
			if rm.Type != TypeScript {
				t.Errorf("expected R__script_view.sh to resolve with Type %q, got %q", TypeScript, rm.Type)
			}
			if !rm.IsRepeatable {
				t.Errorf("expected IsRepeatable to be true")
			}
			if !rm.IsScript {
				t.Errorf("expected IsScript to be true for .sh")
			}
		} else {
			t.Errorf("unexpected repeatable migration: %s", rm.Script)
		}
	}
}

func TestResolver_RelativeScriptPath(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/V1__root.sql": &fstest.MapFile{
			Data: []byte("SELECT 1;"),
		},
		"sql/views/R__users_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW users_v AS SELECT 1;"),
		},
		"sql/sub/deep/V2__deep.sql": &fstest.MapFile{
			Data: []byte("SELECT 2;"),
		},
		"sql/sub/deep/U2__undo_deep.sql": &fstest.MapFile{
			Data: []byte("SELECT 3;"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"sql"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resolved.RepeatableMigrations) != 1 || resolved.RepeatableMigrations[0].Script != "views/R__users_view.sql" {
		t.Errorf("expected repeatable script 'views/R__users_view.sql', got %s", resolved.RepeatableMigrations[0].Script)
	}

	if len(resolved.VersionedMigrations) != 2 {
		t.Fatalf("expected 2 versioned migrations, got %d", len(resolved.VersionedMigrations))
	}
	if resolved.VersionedMigrations[0].Script != "V1__root.sql" {
		t.Errorf("expected root script 'V1__root.sql', got %s", resolved.VersionedMigrations[0].Script)
	}
	if resolved.VersionedMigrations[1].Script != "sub/deep/V2__deep.sql" {
		t.Errorf("expected deep script 'sub/deep/V2__deep.sql', got %s", resolved.VersionedMigrations[1].Script)
	}

	if len(resolved.UndoMigrations) != 1 || resolved.UndoMigrations[0].Script != "sub/deep/U2__undo_deep.sql" {
		t.Errorf("expected undo script 'sub/deep/U2__undo_deep.sql', got %s", resolved.UndoMigrations[0].Script)
	}
}

func TestRepeatableScriptMigrationChecksum(t *testing.T) {
	scriptContent := "#!/bin/bash\necho ${HOME}\necho FP__cluster__\n"

	mockFS := fstest.MapFS{
		"migrations/R__deploy.sh": &fstest.MapFile{
			Data: []byte(scriptContent),
		},
	}

	replacer1 := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"cluster": "cluster-alpha",
		},
	})

	res1 := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
		Replacer:  replacer1,
	})

	resolved1, err := res1.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error with ${HOME}: %v", err)
	}
	if len(resolved1.RepeatableMigrations) != 1 {
		t.Fatalf("expected 1 repeatable script migration, got %d", len(resolved1.RepeatableMigrations))
	}

	expectedContent1 := "#!/bin/bash\necho ${HOME}\necho cluster-alpha\n"
	expectedCs1, _ := checksum.CalculateString(expectedContent1)
	if resolved1.RepeatableMigrations[0].Checksum != expectedCs1 {
		t.Errorf("got checksum %d, want checksum for cluster-alpha %d", resolved1.RepeatableMigrations[0].Checksum, expectedCs1)
	}

	// Change placeholder value -> checksum changes
	replacer2 := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"cluster": "cluster-beta",
		},
	})

	res2 := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
		Replacer:  replacer2,
	})

	resolved2, err := res2.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	expectedContent2 := "#!/bin/bash\necho ${HOME}\necho cluster-beta\n"
	expectedCs2, _ := checksum.CalculateString(expectedContent2)
	if resolved2.RepeatableMigrations[0].Checksum != expectedCs2 {
		t.Errorf("got checksum %d, want checksum for cluster-beta %d", resolved2.RepeatableMigrations[0].Checksum, expectedCs2)
	}
	if resolved1.RepeatableMigrations[0].Checksum == resolved2.RepeatableMigrations[0].Checksum {
		t.Errorf("expected different checksums when placeholder changes")
	}
}

func TestResolverHiddenDirectories(t *testing.T) {
	// Test fs.FS scanning skips hidden directories such as Kubernetes ConfigMap ..data/
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/..data/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/.git/V1__git.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected error with hidden directory in FS: %v", err)
	}
	if len(resolved.VersionedMigrations) != 1 {
		t.Fatalf("expected 1 migration, got %d", len(resolved.VersionedMigrations))
	}

	// Test real filesystem scanning skips hidden directories
	tmpDir := t.TempDir()
	if err := os.WriteFile(tmpDir+"/V1__init.sql", []byte("CREATE TABLE users (id INT64);"), 0644); err != nil {
		t.Fatal(err)
	}
	hiddenSubDir := tmpDir + "/..data"
	if err := os.Mkdir(hiddenSubDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hiddenSubDir+"/V1__init.sql", []byte("CREATE TABLE users (id INT64);"), 0644); err != nil {
		t.Fatal(err)
	}

	resDir := NewResolver(ResolverConfig{
		Locations: []string{tmpDir},
	})
	resolvedDir, err := resDir.Resolve()
	if err != nil {
		t.Fatalf("unexpected error with hidden directory on filesystem: %v", err)
	}
	if len(resolvedDir.VersionedMigrations) != 1 {
		t.Fatalf("expected 1 migration from filesystem, got %d", len(resolvedDir.VersionedMigrations))
	}
}

func TestResolverEncodingLatin1(t *testing.T) {
	// Latin-1 byte 0xE9 represents 'é'.
	// Raw bytes in Go string give raw checksum.
	// Flyway decodes Latin-1 to UTF-8 [0xC3, 0xA9] and calculates checksum on UTF-8 bytes.
	latin1Data := []byte{0xE9}
	rawCs, err := checksum.CalculateString(string(latin1Data))
	if err != nil {
		t.Fatal(err)
	}
	utf8Cs, err := checksum.CalculateString("é")
	if err != nil {
		t.Fatal(err)
	}
	if rawCs == utf8Cs {
		t.Fatalf("expected raw Latin-1 checksum to differ from decoded UTF-8 checksum")
	}

	mockFS := fstest.MapFS{
		"migrations/V1__global_latin1.sql": &fstest.MapFile{
			Data: latin1Data,
		},
		"migrations/V2__per_script_latin1.sql": &fstest.MapFile{
			Data: latin1Data,
		},
		"migrations/V2__per_script_latin1.sql.conf": &fstest.MapFile{
			Data: []byte("encoding=Latin-1\n"),
		},
	}

	// 1. Global encoding Latin-1
	resGlobal := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
		Encoding:  "Latin-1",
	})
	resolvedGlobal, err := resGlobal.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}
	if len(resolvedGlobal.VersionedMigrations) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(resolvedGlobal.VersionedMigrations))
	}
	if resolvedGlobal.VersionedMigrations[0].Checksum != utf8Cs {
		t.Errorf("expected checksum %d for Latin-1 'é' under global encoding, got %d", utf8Cs, resolvedGlobal.VersionedMigrations[0].Checksum)
	}
	if resolvedGlobal.VersionedMigrations[0].Content != "é" {
		t.Errorf("expected decoded content 'é', got %q", resolvedGlobal.VersionedMigrations[0].Content)
	}

	// 2. Default UTF-8 with per-script Latin-1 config on V2
	resPerScript := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
		Encoding:  "UTF-8",
	})
	resolvedPerScript, err := resPerScript.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}
	if len(resolvedPerScript.VersionedMigrations) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(resolvedPerScript.VersionedMigrations))
	}
	// V1 without per-script config uses UTF-8, replacing invalid byte 0xE9 with U+FFFD
	utf8ErrorCs, err := checksum.CalculateString("\uFFFD")
	if err != nil {
		t.Fatal(err)
	}
	if resolvedPerScript.VersionedMigrations[0].Checksum != utf8ErrorCs {
		t.Errorf("expected checksum %d for U+FFFD under UTF-8, got %d", utf8ErrorCs, resolvedPerScript.VersionedMigrations[0].Checksum)
	}
	if resolvedPerScript.VersionedMigrations[0].Content != "\uFFFD" {
		t.Errorf("expected decoded content '\\uFFFD', got %q", resolvedPerScript.VersionedMigrations[0].Content)
	}
	// V2 has encoding=Latin-1 in .conf -> decoded to UTF-8 -> utf8Cs
	if resolvedPerScript.VersionedMigrations[1].Checksum != utf8Cs {
		t.Errorf("expected checksum %d for per-script Latin-1 'é', got %d", utf8Cs, resolvedPerScript.VersionedMigrations[1].Checksum)
	}
	if resolvedPerScript.VersionedMigrations[1].Content != "é" {
		t.Errorf("expected decoded content 'é', got %q", resolvedPerScript.VersionedMigrations[1].Content)
	}
}

func TestResolverJavaCharsetsAndBOM(t *testing.T) {
	// 1. ISO-8859-1 vs Windows-1252 on byte 0x80
	isoText, err := decodeContent([]byte{0x80, 0xE9}, "ISO-8859-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isoText != "\u0080é" {
		t.Errorf("expected ISO-8859-1 to decode 0x80 to \\u0080 and 0xE9 to é, got %q", isoText)
	}

	winText, err := decodeContent([]byte{0x80, 0xE9}, "WINDOWS-1252")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if winText != "€é" {
		t.Errorf("expected Windows-1252 to decode 0x80 to € and 0xE9 to é, got %q", winText)
	}

	// 2. US-ASCII on bytes > 0x7F
	asciiText, err := decodeContent([]byte{0x41, 0x80, 0xE9}, "US-ASCII")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if asciiText != "A\uFFFD\uFFFD" {
		t.Errorf("expected US-ASCII to replace bytes >0x7F with \\uFFFD, got %q", asciiText)
	}

	// 3. UTF-16 with Big-Endian BOM
	utf16BEBOM := []byte{0xFE, 0xFF, 0x00, 0x41, 0x00, 0x42}
	utf16Text, err := decodeContent(utf16BEBOM, "UTF-16")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if utf16Text != "AB" {
		t.Errorf("expected UTF-16 with BOM to strip BOM and return 'AB', got %q", utf16Text)
	}

	// 4. UTF-16 without BOM (Java defaults to big-endian)
	utf16NoBOM := []byte{0x00, 0x41, 0x00, 0x42}
	utf16NoBOMText, err := decodeContent(utf16NoBOM, "UTF-16")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if utf16NoBOMText != "AB" {
		t.Errorf("expected UTF-16 without BOM to decode as big-endian 'AB', got %q", utf16NoBOMText)
	}

	// 5. UTF-8 with BOM
	utf8WithBOM := []byte{0xEF, 0xBB, 0xBF, 'S', 'E', 'L', 'E', 'C', 'T'}
	utf8Text, err := decodeContent(utf8WithBOM, "UTF-8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if utf8Text != "SELECT" {
		t.Errorf("expected UTF-8 BOM to be stripped, got %q", utf8Text)
	}

	// 6. UTF-8 invalid bytes replaced with U+FFFD (finding 9)
	utf8Invalid := []byte{0x61, 0xE9}
	utf8InvalidText, err := decodeContent(utf8Invalid, "UTF-8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if utf8InvalidText != "a\uFFFD" {
		t.Errorf("expected invalid UTF-8 byte to be replaced with \\uFFFD, got %q", utf8InvalidText)
	}

	// 7. Unsupported encoding fails
	_, err = decodeContent([]byte("test"), "foo_bar_encoding")
	if err == nil {
		t.Errorf("expected error for unsupported encoding, got nil")
	}
}

func TestResolverCaseInsensitiveSuffix(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.SQL": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V1__init.SQL.conf": &fstest.MapFile{
			Data: []byte("shouldExecute=true\n"),
		},
		"migrations/R__view.SQL": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT 1;"),
		},
		"migrations/U1__undo.SQL": &fstest.MapFile{
			Data: []byte("DROP TABLE users;"),
		},
		"migrations/B1__base.SQL": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/beforeMigrate.SQL": &fstest.MapFile{
			Data: []byte("-- before migrate"),
		},
		"migrations/V2__batch.SH": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho done\n"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error with uppercase suffixes: %v", err)
	}

	if len(resolved.VersionedMigrations) != 2 {
		t.Fatalf("expected 2 versioned migrations, got %d", len(resolved.VersionedMigrations))
	}
	v1 := resolved.VersionedMigrations[0]
	if v1.Version.String() != "1" || v1.Description != "init" {
		t.Errorf("expected V1 with desc 'init', got version %s, desc %s", v1.Version.String(), v1.Description)
	}
	if v1.Config.ShouldExecute != "true" {
		t.Errorf("expected shouldExecute=true from V1__init.SQL.conf, got %q", v1.Config.ShouldExecute)
	}

	v2 := resolved.VersionedMigrations[1]
	if v2.Version.String() != "2" || v2.Description != "batch" || !v2.IsScript {
		t.Errorf("expected V2 script with desc 'batch', got version %s, desc %s, isScript %v", v2.Version.String(), v2.Description, v2.IsScript)
	}

	if len(resolved.RepeatableMigrations) != 1 {
		t.Fatalf("expected 1 repeatable migration, got %d", len(resolved.RepeatableMigrations))
	}
	if resolved.RepeatableMigrations[0].Description != "view" {
		t.Errorf("expected repeatable desc 'view', got %s", resolved.RepeatableMigrations[0].Description)
	}

	if len(resolved.UndoMigrations) != 1 {
		t.Fatalf("expected 1 undo migration, got %d", len(resolved.UndoMigrations))
	}
	if resolved.UndoMigrations[0].Description != "undo" {
		t.Errorf("expected undo desc 'undo', got %s", resolved.UndoMigrations[0].Description)
	}

	if len(resolved.BaselineMigrations) != 1 {
		t.Fatalf("expected 1 baseline migration, got %d", len(resolved.BaselineMigrations))
	}
	if resolved.BaselineMigrations[0].Description != "base" {
		t.Errorf("expected baseline desc 'base', got %s", resolved.BaselineMigrations[0].Description)
	}

	if len(resolved.Callbacks["beforeMigrate"]) != 1 {
		t.Fatalf("expected 1 beforeMigrate callback, got %d", len(resolved.Callbacks["beforeMigrate"]))
	}
}

func TestResolver_MissingLocationsAndFileLocations(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);\n"),
		},
		"some_file.sql": &fstest.MapFile{
			Data: []byte("SELECT 1;\n"),
		},
	}

	// 1. Missing location with FailOnMissingLocations = false succeeds
	resMissing := NewResolver(ResolverConfig{
		FS:                      mockFS,
		Locations:               []string{"non_existent_folder"},
		FailOnMissingLocations:  false,
	})
	resolved, err := resMissing.Resolve()
	if err != nil {
		t.Fatalf("expected missing location to be skipped when FailOnMissingLocations=false, got: %v", err)
	}
	if len(resolved.VersionedMigrations) != 0 {
		t.Errorf("expected 0 migrations, got %d", len(resolved.VersionedMigrations))
	}

	// 2. Missing location with FailOnMissingLocations = true fails
	resMissingFail := NewResolver(ResolverConfig{
		FS:                      mockFS,
		Locations:               []string{"non_existent_folder"},
		FailOnMissingLocations:  true,
	})
	_, err = resMissingFail.Resolve()
	if err == nil {
		t.Fatalf("expected error when FailOnMissingLocations=true, got nil")
	}

	// 3. Location pointing to a regular file is skipped by Flyway
	resFileLoc := NewResolver(ResolverConfig{
		FS:                      mockFS,
		Locations:               []string{"some_file.sql"},
		FailOnMissingLocations:  true,
	})
	resolvedFile, err := resFileLoc.Resolve()
	if err != nil {
		t.Fatalf("expected file location to be skipped, got error: %v", err)
	}
	if len(resolvedFile.VersionedMigrations) != 0 {
		t.Errorf("expected 0 migrations when location is a file, got %d", len(resolvedFile.VersionedMigrations))
	}
}

func TestResolver_ValidateMigrationNaming_IgnoresHelperScripts(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);\n"),
		},
		"migrations/helper.sh": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho helper\n"),
		},
	}

	// 1. With helper.sh, ValidateMigrationNaming=true should succeed because helper.sh is not a SQL migration
	res := NewResolver(ResolverConfig{
		FS:                      mockFS,
		Locations:               []string{"migrations"},
		ValidateMigrationNaming: true,
		SQLMigrationSuffixes:    []string{".sql"},
	})
	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("expected helper.sh to be ignored by ValidateMigrationNaming, got: %v", err)
	}
	if len(resolved.VersionedMigrations) != 1 {
		t.Errorf("expected 1 migration, got %d", len(resolved.VersionedMigrations))
	}

	// 2. An invalid SQL migration name with ValidateMigrationNaming=true must fail
	mockFS["migrations/invalid.sql"] = &fstest.MapFile{
		Data: []byte("SELECT 1;\n"),
	}
	res2 := NewResolver(ResolverConfig{
		FS:                      mockFS,
		Locations:               []string{"migrations"},
		ValidateMigrationNaming: true,
		SQLMigrationSuffixes:    []string{".sql"},
	})
	_, err = res2.Resolve()
	if err == nil || !strings.Contains(err.Error(), "invalid migration name") {
		t.Fatalf("expected error for invalid.sql, got: %v", err)
	}
}

func TestResolver_DirectorySymlink(t *testing.T) {
	tmpDir := t.TempDir()

	// Create real directory with a migration outside scanned location
	realDir := filepath.Join(tmpDir, "real_target")
	if err := os.MkdirAll(realDir, 0755); err != nil {
		t.Fatalf("failed to create real dir: %v", err)
	}
	v1File := filepath.Join(realDir, "V1__init.sql")
	if err := os.WriteFile(v1File, []byte("CREATE TABLE t1 (id INT64);\n"), 0644); err != nil {
		t.Fatalf("failed to write V1 file: %v", err)
	}

	// Create scan directory with a symlink to realDir and a hidden symlink
	scanDir := filepath.Join(tmpDir, "scan_root")
	if err := os.MkdirAll(scanDir, 0755); err != nil {
		t.Fatalf("failed to create scan dir: %v", err)
	}

	linkPath := filepath.Join(scanDir, "linked_migrations")
	if err := os.Symlink(realDir, linkPath); err != nil {
		t.Skipf("symlinks not supported on this OS/filesystem: %v", err)
	}

	hiddenLinkPath := filepath.Join(scanDir, ".hidden_linked_migrations")
	_ = os.Symlink(realDir, hiddenLinkPath)

	res := NewResolver(ResolverConfig{
		Locations: []string{scanDir},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("failed to resolve directory with symlink: %v", err)
	}

	if len(resolved.VersionedMigrations) != 1 {
		t.Fatalf("expected exactly 1 versioned migration from symlinked dir, got %d", len(resolved.VersionedMigrations))
	}
	if resolved.VersionedMigrations[0].Version.String() != "1" {
		t.Errorf("expected version 1, got %s", resolved.VersionedMigrations[0].Version.String())
	}
}





