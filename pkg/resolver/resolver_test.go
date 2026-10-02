package resolver

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/RoryQ/noway/pkg/checksum"
	"github.com/RoryQ/noway/pkg/parser"
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

