package resolver

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/RoryQ/noway/pkg/checksum"
	"github.com/RoryQ/noway/pkg/parser"
)

// TestAdversarial_RepeatableMissingPlaceholderErrors verifies checksum error propagation
// for repeatable migrations when placeholders are missing, compared against versioned,
// undo, and baseline migrations which calculate raw checksums.
func TestAdversarial_RepeatableMissingPlaceholderErrors(t *testing.T) {
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"valid_var": "analytics",
		},
	})

	t.Run("repeatable migration fails resolution on missing placeholder", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/R__view.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW v AS SELECT * FROM ${missing_table};"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
			Replacer:  replacer,
		})

		_, err := res.Resolve()
		if err == nil {
			t.Fatalf("expected error from Resolve() when repeatable migration has missing placeholder, got nil")
		}
		if !strings.Contains(err.Error(), "missing_table") {
			t.Fatalf("expected error to mention missing placeholder 'missing_table', got: %v", err)
		}
	})

	t.Run("repeatable migration fails when only one of multiple placeholders is missing", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/R__multi.sql": &fstest.MapFile{
				Data: []byte("SELECT '${valid_var}', '${unresolved_second_var}';"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
			Replacer:  replacer,
		})

		_, err := res.Resolve()
		if err == nil {
			t.Fatalf("expected error from Resolve(), got nil")
		}
		if !strings.Contains(err.Error(), "unresolved_second_var") {
			t.Fatalf("expected error to mention unresolved placeholder, got: %v", err)
		}
	})

	t.Run("versioned, undo, and baseline succeed even with missing placeholder because they use raw checksum", func(t *testing.T) {
		vRaw := "CREATE TABLE ${missing_tbl} (id INT64);"
		uRaw := "DROP TABLE ${missing_tbl};"
		bRaw := "CREATE TABLE ${missing_tbl}_base (id INT64);"

		mockFS := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte(vRaw),
			},
			"migrations/U1__undo.sql": &fstest.MapFile{
				Data: []byte(uRaw),
			},
			"migrations/B1__baseline.sql": &fstest.MapFile{
				Data: []byte(bRaw),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
			Replacer:  replacer,
		})

		resolved, err := res.Resolve()
		if err != nil {
			t.Fatalf("expected versioned, undo, baseline to succeed with raw checksum, got error: %v", err)
		}

		if len(resolved.VersionedMigrations) != 1 {
			t.Fatalf("expected 1 versioned migration, got %d", len(resolved.VersionedMigrations))
		}
		expectedVChecksum, _ := checksum.CalculateString(vRaw)
		if resolved.VersionedMigrations[0].Checksum != expectedVChecksum {
			t.Errorf("versioned checksum: got %d, want raw checksum %d", resolved.VersionedMigrations[0].Checksum, expectedVChecksum)
		}

		if len(resolved.UndoMigrations) != 1 {
			t.Fatalf("expected 1 undo migration, got %d", len(resolved.UndoMigrations))
		}
		expectedUChecksum, _ := checksum.CalculateString(uRaw)
		if resolved.UndoMigrations[0].Checksum != expectedUChecksum {
			t.Errorf("undo checksum: got %d, want raw checksum %d", resolved.UndoMigrations[0].Checksum, expectedUChecksum)
		}

		if len(resolved.BaselineMigrations) != 1 {
			t.Fatalf("expected 1 baseline migration, got %d", len(resolved.BaselineMigrations))
		}
		expectedBChecksum, _ := checksum.CalculateString(bRaw)
		if resolved.BaselineMigrations[0].Checksum != expectedBChecksum {
			t.Errorf("baseline checksum: got %d, want raw checksum %d", resolved.BaselineMigrations[0].Checksum, expectedBChecksum)
		}
	})

	t.Run("repeatable with default value syntax succeeds and uses default for checksum", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/R__fallback.sql": &fstest.MapFile{
				Data: []byte("SELECT '${missing_with_default:fallback_dataset}';"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
			Replacer:  replacer,
		})

		resolved, err := res.Resolve()
		if err != nil {
			t.Fatalf("expected repeatable with default value to succeed, got: %v", err)
		}

		expectedChecksum, _ := checksum.CalculateString("SELECT 'fallback_dataset';")
		if resolved.RepeatableMigrations[0].Checksum != expectedChecksum {
			t.Errorf("got checksum %d, want checksum for fallback_dataset %d", resolved.RepeatableMigrations[0].Checksum, expectedChecksum)
		}
	})

	t.Run("repeatable with escaped placeholder succeeds and unescapes for checksum", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"migrations/R__escaped.sql": &fstest.MapFile{
				Data: []byte("SELECT '$${missing_escaped_var}';"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
			Replacer:  replacer,
		})

		resolved, err := res.Resolve()
		if err != nil {
			t.Fatalf("expected repeatable with escaped placeholder to succeed, got: %v", err)
		}

		expectedChecksum, _ := checksum.CalculateString("SELECT '${missing_escaped_var}';")
		if resolved.RepeatableMigrations[0].Checksum != expectedChecksum {
			t.Errorf("got checksum %d, want checksum for unescaped content %d", resolved.RepeatableMigrations[0].Checksum, expectedChecksum)
		}
	})
}

// TestAdversarial_PerMigrationConfPlaceholderOverride verifies .sql.conf override mechanics:
// 1. placeholderReplacement=false on repeatable migration suppresses error on missing placeholder and uses raw checksum.
// 2. placeholderReplacement=false on repeatable migration prevents value replacement for defined placeholders.
// 3. placeholderReplacement=true overrides global disabled replacement.
// 4. Case insensitivity and formatting in .conf files (FALSE, 0, etc.).
func TestAdversarial_PerMigrationConfPlaceholderOverride(t *testing.T) {
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"env": "production",
		},
	})

	t.Run("repeatable migration with placeholderReplacement=false avoids error on missing placeholder", func(t *testing.T) {
		rawSQL := "CREATE VIEW v AS SELECT * FROM ${totally_unconfigured_var};"
		mockFS := fstest.MapFS{
			"migrations/R__skip.sql": &fstest.MapFile{
				Data: []byte(rawSQL),
			},
			"migrations/R__skip.sql.conf": &fstest.MapFile{
				Data: []byte("placeholderReplacement=false\n"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
			Replacer:  replacer,
		})

		resolved, err := res.Resolve()
		if err != nil {
			t.Fatalf("expected Resolve() to succeed when .conf has placeholderReplacement=false, got: %v", err)
		}

		expectedRawChecksum, _ := checksum.CalculateString(rawSQL)
		if resolved.RepeatableMigrations[0].Checksum != expectedRawChecksum {
			t.Errorf("got checksum %d, want raw checksum %d", resolved.RepeatableMigrations[0].Checksum, expectedRawChecksum)
		}
		if resolved.RepeatableMigrations[0].Config.PlaceholderReplacement == nil || *resolved.RepeatableMigrations[0].Config.PlaceholderReplacement != false {
			t.Errorf("expected Config.PlaceholderReplacement to be false, got %v", resolved.RepeatableMigrations[0].Config.PlaceholderReplacement)
		}
	})

	t.Run("repeatable migration with placeholderReplacement=false leaves defined placeholder unreplaced in checksum", func(t *testing.T) {
		rawSQL := "SELECT '${env}';"
		mockFS := fstest.MapFS{
			"migrations/R__no_replace.sql": &fstest.MapFile{
				Data: []byte(rawSQL),
			},
			"migrations/R__no_replace.sql.conf": &fstest.MapFile{
				Data: []byte("# Config comment\nplaceholderReplacement = FALSE\n"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"migrations"},
			Replacer:  replacer,
		})

		resolved, err := res.Resolve()
		if err != nil {
			t.Fatalf("unexpected resolve error: %v", err)
		}

		// Must match RAW checksum (${env}), NOT replaced (production)
		expectedRawChecksum, _ := checksum.CalculateString(rawSQL)
		replacedChecksum, _ := checksum.CalculateString("SELECT 'production';")

		if resolved.RepeatableMigrations[0].Checksum == replacedChecksum {
			t.Errorf("repeatable migration checksum was replaced with 'production', but placeholderReplacement was false")
		}
		if resolved.RepeatableMigrations[0].Checksum != expectedRawChecksum {
			t.Errorf("got checksum %d, want raw checksum %d", resolved.RepeatableMigrations[0].Checksum, expectedRawChecksum)
		}
	})

	t.Run("repeatable migration with placeholderReplacement=true overrides global disabled replacement", func(t *testing.T) {
		globalDisabled := false
		mockFS := fstest.MapFS{
			"migrations/R__enabled_override.sql": &fstest.MapFile{
				Data: []byte("SELECT '${env}';"),
			},
			"migrations/R__enabled_override.conf": &fstest.MapFile{
				Data: []byte("placeholderReplacement=1\n"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:                     mockFS,
			Locations:              []string{"migrations"},
			PlaceholderReplacement: &globalDisabled,
			Replacer:               replacer,
		})

		resolved, err := res.Resolve()
		if err != nil {
			t.Fatalf("unexpected resolve error: %v", err)
		}

		expectedReplacedChecksum, _ := checksum.CalculateString("SELECT 'production';")
		if resolved.RepeatableMigrations[0].Checksum != expectedReplacedChecksum {
			t.Errorf("got checksum %d, want replaced checksum %d", resolved.RepeatableMigrations[0].Checksum, expectedReplacedChecksum)
		}
	})

	t.Run("repeatable migration with placeholderReplacement=true fails when placeholder is missing even if globally disabled", func(t *testing.T) {
		globalDisabled := false
		mockFS := fstest.MapFS{
			"migrations/R__override_fails.sql": &fstest.MapFile{
				Data: []byte("SELECT '${missing_override_var}';"),
			},
			"migrations/R__override_fails.sql.conf": &fstest.MapFile{
				Data: []byte("placeholderReplacement=true\n"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:                     mockFS,
			Locations:              []string{"migrations"},
			PlaceholderReplacement: &globalDisabled,
			Replacer:               replacer,
		})

		_, err := res.Resolve()
		if err == nil {
			t.Fatalf("expected Resolve() to fail when .conf has placeholderReplacement=true and placeholder is missing")
		}
		if !strings.Contains(err.Error(), "missing_override_var") {
			t.Fatalf("expected error to mention missing placeholder, got: %v", err)
		}
	})
}
