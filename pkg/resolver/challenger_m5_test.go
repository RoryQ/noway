package resolver

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/RoryQ/noway/pkg/parser"
)

func TestChallengerM5_DeeplyNestedSubdirectories(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/lvl1/lvl2/lvl3/lvl4/lvl5/lvl6/lvl7/lvl8/lvl9/lvl10/V1__deepest.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
		"migrations/lvl1/lvl2/lvl3/lvl4/lvl5/V2__mid_level.sh": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho mid"),
		},
		"migrations/lvl1/R__shallow_repeatable.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v1 AS SELECT 1;"),
		},
		"migrations/lvl1/lvl2/lvl3/B1__deep_baseline.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE base1 (id INT64);"),
		},
		"migrations/lvl1/lvl2/lvl3/lvl4/U1__deep_undo.sql": &fstest.MapFile{
			Data: []byte("DROP TABLE t1;"),
		},
		"migrations/lvl1/lvl2/beforeMigrate__hook.sql": &fstest.MapFile{
			Data: []byte("-- before migrate hook"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected error resolving deep hierarchy: %v", err)
	}

	if len(resolved.VersionedMigrations) != 2 {
		t.Fatalf("expected 2 versioned migrations, got %d", len(resolved.VersionedMigrations))
	}
	if resolved.VersionedMigrations[0].Version.String() != "1" {
		t.Errorf("expected V1 first, got %s", resolved.VersionedMigrations[0].Version.String())
	}
	if resolved.VersionedMigrations[1].Version.String() != "2" || !resolved.VersionedMigrations[1].IsScript {
		t.Errorf("expected V2 script second, got %+v", resolved.VersionedMigrations[1])
	}

	if len(resolved.RepeatableMigrations) != 1 {
		t.Fatalf("expected 1 repeatable migration, got %d", len(resolved.RepeatableMigrations))
	}
	if len(resolved.BaselineMigrations) != 1 {
		t.Fatalf("expected 1 baseline migration, got %d", len(resolved.BaselineMigrations))
	}
	if len(resolved.UndoMigrations) != 1 {
		t.Fatalf("expected 1 undo migration, got %d", len(resolved.UndoMigrations))
	}
	if len(resolved.Callbacks["beforeMigrate"]) != 1 {
		t.Fatalf("expected 1 beforeMigrate callback, got %d", len(resolved.Callbacks["beforeMigrate"]))
	}
}

func TestChallengerM5_WeirdAndIgnoredExtensions(t *testing.T) {
	mockFS := fstest.MapFS{
		// Valid migration
		"migrations/V1__valid.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		// Invalid / ignored extensions
		"migrations/V2__backup.sql.bak":   &fstest.MapFile{Data: []byte("SELECT 2;")},
		"migrations/V3__temp.sql~":        &fstest.MapFile{Data: []byte("SELECT 3;")},
		"migrations/V4__notes.txt":        &fstest.MapFile{Data: []byte("SELECT 4;")},
		"migrations/V5__doc.md":           &fstest.MapFile{Data: []byte("SELECT 5;")},
		"migrations/V6__config.json":      &fstest.MapFile{Data: []byte("{}")},
		"migrations/V7__noext":            &fstest.MapFile{Data: []byte("SELECT 7;")},
		"migrations/.V8__hidden.sql":      &fstest.MapFile{Data: []byte("SELECT 8;")},
		"migrations/.DS_Store":            &fstest.MapFile{Data: []byte("junk")},
		"migrations/README.txt":           &fstest.MapFile{Data: []byte("docs")},
		"migrations/R__temp.sql.tmp":      &fstest.MapFile{Data: []byte("SELECT 9;")},
		"migrations/U1__undo.sql.old":     &fstest.MapFile{Data: []byte("SELECT 10;")},
		"migrations/beforeMigrate.sql.swp": &fstest.MapFile{Data: []byte("SELECT 11;")},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected error resolving with ignored files: %v", err)
	}

	// Only V1__valid.sql should be resolved
	if len(resolved.VersionedMigrations) != 1 {
		t.Errorf("expected 1 versioned migration, got %d (resolved: %+v)", len(resolved.VersionedMigrations), resolved.VersionedMigrations)
	}
	if len(resolved.RepeatableMigrations) != 0 {
		t.Errorf("expected 0 repeatable migrations, got %d", len(resolved.RepeatableMigrations))
	}
	if len(resolved.UndoMigrations) != 0 {
		t.Errorf("expected 0 undo migrations, got %d", len(resolved.UndoMigrations))
	}
	if len(resolved.Callbacks) != 0 {
		t.Errorf("expected 0 callbacks, got %d", len(resolved.Callbacks))
	}
}

func TestChallengerM5_LeadingAndTrailingZerosDuplicates(t *testing.T) {
	t.Run("duplicate versions with different leading zeros rejected", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/V01__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V1__second.sql":  &fstest.MapFile{Data: []byte("SELECT 2;")},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
		})

		_, err := res.Resolve()
		if err == nil {
			t.Fatalf("expected error for duplicate versions 01 and 1, got nil")
		}
		if !strings.Contains(err.Error(), "found more than one migration with version 1") {
			t.Errorf("expected error message mentioning version 1, got: %v", err)
		}
	})

	t.Run("duplicate versions with multi leading zeros 001 and 1 rejected", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/V001__first.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V1__second.sql":  &fstest.MapFile{Data: []byte("SELECT 2;")},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
		})

		_, err := res.Resolve()
		if err == nil {
			t.Fatalf("expected error for duplicate versions 001 and 1, got nil")
		}
	})

	t.Run("duplicate versions with trailing zeros 1.0 and 1.0.0 rejected", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/V1.0__first.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V1.0.0__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
		})

		_, err := res.Resolve()
		if err == nil {
			t.Fatalf("expected error for duplicate versions 1.0 and 1.0.0, got nil")
		}
	})

	t.Run("1.0.1 and 1.001 are DISTINCT and both accepted", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/V1.0.1__three_parts.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V1.001__two_parts.sql":   &fstest.MapFile{Data: []byte("SELECT 2;")},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
		})

		resolved, err := res.Resolve()
		if err != nil {
			t.Fatalf("expected 1.0.1 and 1.001 to be accepted as distinct versions, got: %v", err)
		}
		if len(resolved.VersionedMigrations) != 2 {
			t.Fatalf("expected 2 migrations, got %d", len(resolved.VersionedMigrations))
		}
		// In Flyway order: 1.0.1 ([1, 0, 1]) comes before 1.001 ([1, 1])
		if resolved.VersionedMigrations[0].Version.String() != "1.0.1" {
			t.Errorf("expected 1.0.1 first, got %s", resolved.VersionedMigrations[0].Version.String())
		}
		if resolved.VersionedMigrations[1].Version.String() != "1.001" {
			t.Errorf("expected 1.001 second, got %s", resolved.VersionedMigrations[1].Version.String())
		}
	})
}

func TestChallengerM5_MixedFileTypesCoexistence(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql":            &fstest.MapFile{Data: []byte("CREATE TABLE t (id INT64);")},
		"migrations/U1__init.sql":            &fstest.MapFile{Data: []byte("DROP TABLE t;")},
		"migrations/B1__init.sql":            &fstest.MapFile{Data: []byte("CREATE TABLE t (id INT64);")},
		"migrations/V2__add_col.sql":         &fstest.MapFile{Data: []byte("ALTER TABLE t ADD COLUMN c STRING;")},
		"migrations/U2__add_col.sql":         &fstest.MapFile{Data: []byte("ALTER TABLE t DROP COLUMN c;")},
		"migrations/B2__baseline.sql":        &fstest.MapFile{Data: []byte("CREATE TABLE t (id INT64, c STRING);")},
		"migrations/R__view.sql":             &fstest.MapFile{Data: []byte("CREATE VIEW v AS SELECT * FROM t;")},
		"migrations/beforeMigrate.sql":       &fstest.MapFile{Data: []byte("-- before")},
		"migrations/afterMigrate.sql":        &fstest.MapFile{Data: []byte("-- after")},
		"migrations/afterMigrate__01.sh":     &fstest.MapFile{Data: []byte("#!/bin/bash\n")},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error in mixed environment: %v", err)
	}

	if len(resolved.VersionedMigrations) != 2 {
		t.Errorf("expected 2 versioned migrations, got %d", len(resolved.VersionedMigrations))
	}
	if len(resolved.UndoMigrations) != 2 {
		t.Errorf("expected 2 undo migrations, got %d", len(resolved.UndoMigrations))
	}
	if len(resolved.BaselineMigrations) != 2 {
		t.Errorf("expected 2 baseline migrations, got %d", len(resolved.BaselineMigrations))
	}
	if len(resolved.RepeatableMigrations) != 1 {
		t.Errorf("expected 1 repeatable migration, got %d", len(resolved.RepeatableMigrations))
	}
	if len(resolved.Callbacks["beforeMigrate"]) != 1 {
		t.Errorf("expected 1 beforeMigrate callback, got %d", len(resolved.Callbacks["beforeMigrate"]))
	}
	if len(resolved.Callbacks["afterMigrate"]) != 2 {
		t.Errorf("expected 2 afterMigrate callbacks, got %d", len(resolved.Callbacks["afterMigrate"]))
	}
}

func TestChallengerM5_CustomPrefixAndSeparator(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/M1.0--init.sql":      &fstest.MapFile{Data: []byte("CREATE TABLE custom (id INT64);")},
		"migrations/M2.0--add_col.sql":   &fstest.MapFile{Data: []byte("ALTER TABLE custom ADD COLUMN x INT64;")},
		"migrations/REP--views.sql":      &fstest.MapFile{Data: []byte("CREATE VIEW v AS SELECT 1;")},
	}

	res := NewResolver(ResolverConfig{
		FS:               mockFS,
		Locations:        []string{"migrations"},
		Prefix:           "M",
		RepeatablePrefix: "REP",
		Separator:        "--",
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error with custom prefix/sep: %v", err)
	}

	if len(resolved.VersionedMigrations) != 2 {
		t.Fatalf("expected 2 versioned migrations with M prefix and -- sep, got %d", len(resolved.VersionedMigrations))
	}
	if resolved.VersionedMigrations[0].Version.String() != "1.0" {
		t.Errorf("expected version 1.0, got %s", resolved.VersionedMigrations[0].Version.String())
	}
	if resolved.VersionedMigrations[0].Description != "init" {
		t.Errorf("expected desc 'init', got %q", resolved.VersionedMigrations[0].Description)
	}

	if len(resolved.RepeatableMigrations) != 1 {
		t.Fatalf("expected 1 repeatable migration with REP prefix, got %d", len(resolved.RepeatableMigrations))
	}
	if resolved.RepeatableMigrations[0].Description != "views" {
		t.Errorf("expected desc 'views', got %q", resolved.RepeatableMigrations[0].Description)
	}
}

func TestChallengerM5_MissingFilesHandling(t *testing.T) {
	t.Run("error on non-existent directory when IgnoreMissingFiles is false", func(t *testing.T) {
		res := NewResolver(ResolverConfig{
			Locations:          []string{"/nonexistent/directory/path/here"},
			IgnoreMissingFiles: false,
		})
		_, err := res.Resolve()
		if err == nil {
			t.Fatalf("expected error for non-existent directory, got nil")
		}
	})

	t.Run("succeeds on non-existent directory when IgnoreMissingFiles is true", func(t *testing.T) {
		res := NewResolver(ResolverConfig{
			Locations:          []string{"/nonexistent/directory/path/here"},
			IgnoreMissingFiles: true,
		})
		resolved, err := res.Resolve()
		if err != nil {
			t.Fatalf("expected success with IgnoreMissingFiles=true, got: %v", err)
		}
		if len(resolved.VersionedMigrations) != 0 {
			t.Errorf("expected 0 migrations, got %d", len(resolved.VersionedMigrations))
		}
	})
}

func TestChallengerM5_RepeatablePlaceholderBuiltinsChecksum(t *testing.T) {
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"custom_table": "my_table",
		},
	})

	mockFS := fstest.MapFS{
		"migrations/R__builtin_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT '${flyway:defaultSchema}', '${custom_table}';"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
		Replacer:  replacer,
		Builtins: parser.BuiltinPlaceholders{
			DefaultSchema: "analytics_ds",
		},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if len(resolved.RepeatableMigrations) != 1 {
		t.Fatalf("expected 1 repeatable migration, got %d", len(resolved.RepeatableMigrations))
	}

	// Verify checksum was computed with replaced builtins and values
	if resolved.RepeatableMigrations[0].Checksum == 0 {
		t.Errorf("expected non-zero checksum for repeatable migration")
	}
}
