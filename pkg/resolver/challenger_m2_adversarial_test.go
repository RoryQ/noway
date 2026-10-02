package resolver

import (
	"strings"
	"testing"
	"testing/fstest"
)

// TestChallengerRepeatableDuplicateDescriptions_Adversarial tests that duplicate descriptions
// for repeatable migrations are rejected across subdirectories, file extensions, and deep hierarchies.
func TestChallengerRepeatableDuplicateDescriptions_Adversarial(t *testing.T) {
	t.Run("duplicate descriptions across different subdirectories", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"sql/views/R__user_summary.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW user_summary AS SELECT 1;"),
			},
			"sql/reports/R__user_summary.sql": &fstest.MapFile{
				Data: []byte("CREATE VIEW user_summary AS SELECT 2;"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"sql"},
		})

		_, err := res.Resolve()
		if err == nil {
			t.Fatalf("expected error for duplicate repeatable description across subdirectories, got nil")
		}

		expectedPrefix := "found more than one repeatable migration with description 'user summary'\nOffenders:\n->"
		if !strings.Contains(err.Error(), expectedPrefix) {
			t.Errorf("expected error containing %q, got %q", expectedPrefix, err.Error())
		}
	})

	t.Run("duplicate descriptions across different file extensions in same directory", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"sql/R__refresh_cache.sql": &fstest.MapFile{
				Data: []byte("SELECT 1;"),
			},
			"sql/R__refresh_cache.sh": &fstest.MapFile{
				Data: []byte("#!/bin/bash\necho 'refreshing'"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"sql"},
		})

		_, err := res.Resolve()
		if err == nil {
			t.Fatalf("expected error for duplicate repeatable description across extensions, got nil")
		}

		expectedPrefix := "found more than one repeatable migration with description 'refresh cache'\nOffenders:\n->"
		if !strings.Contains(err.Error(), expectedPrefix) {
			t.Errorf("expected error containing %q, got %q", expectedPrefix, err.Error())
		}
	})

	t.Run("duplicate descriptions across both different subdirectories and different extensions", func(t *testing.T) {
		mockFS := fstest.MapFS{
			"sql/subA/deep/R__sync_job.sql": &fstest.MapFile{
				Data: []byte("SELECT 1;"),
			},
			"sql/subB/R__sync_job.bash": &fstest.MapFile{
				Data: []byte("#!/bin/bash\necho 'sync'"),
			},
		}

		res := NewResolver(ResolverConfig{
			FS:        mockFS,
			Locations: []string{"sql"},
		})

		_, err := res.Resolve()
		if err == nil {
			t.Fatalf("expected error for duplicate repeatable description across different subdirs and extensions, got nil")
		}

		expectedPrefix := "found more than one repeatable migration with description 'sync job'\nOffenders:\n->"
		if !strings.Contains(err.Error(), expectedPrefix) {
			t.Errorf("expected error containing %q, got %q", expectedPrefix, err.Error())
		}
	})
}

// TestChallengerRepeatableOrdering_Adversarial tests strict alphabetical description ordering
// regardless of directory discovery order, filename prefixes, or extensions.
func TestChallengerRepeatableOrdering_Adversarial(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/z_dir/R__01_zero_first.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
		"sql/a_dir/R__middle_action.sh":    &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
		"sql/m_dir/R__zoo_keeper.sql":      &fstest.MapFile{Data: []byte("SELECT 2;")},
		"sql/a_dir/R__apple_pie.bash":      &fstest.MapFile{Data: []byte("#!/bin/bash\n")},
		"sql/b_dir/R__apple.sql":           &fstest.MapFile{Data: []byte("SELECT 3;")},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"sql"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if len(resolved.RepeatableMigrations) != 5 {
		t.Fatalf("expected 5 repeatable migrations, got %d", len(resolved.RepeatableMigrations))
	}

	expectedOrder := []string{
		"01 zero first",
		"apple",
		"apple pie",
		"middle action",
		"zoo keeper",
	}

	for i, exp := range expectedOrder {
		got := resolved.RepeatableMigrations[i].Description
		if got != exp {
			t.Errorf("index %d: expected description %q, got %q", i, exp, got)
		}
	}
}

// TestChallengerCumulativeBaselineDuplicates_Adversarial tests duplicate baseline version detection
// across different naming conventions (B1 vs B__1.0) and subdirectories.
func TestChallengerCumulativeBaselineDuplicates_Adversarial(t *testing.T) {
	mockFS := fstest.MapFS{
		"sql/legacy/B1.0.0__baseline.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"sql/modern/B__1__baseline.sql":   &fstest.MapFile{Data: []byte("SELECT 2;")},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"sql"},
	})

	_, err := res.Resolve()
	if err == nil {
		t.Fatalf("expected duplicate baseline error for versions 1.0.0 and 1, got nil")
	}

	expectedPrefix := "found more than one baseline migration with version 1\nOffenders:\n->"
	if !strings.Contains(err.Error(), expectedPrefix) {
		t.Errorf("expected error containing %q, got %q", expectedPrefix, err.Error())
	}
}
