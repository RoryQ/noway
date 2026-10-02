package resolver

import (
	"testing"
	"testing/fstest"
)

// ============================================================================
// CHALLENGER M3: ADVERSARIAL RESOLVER CALLBACK DISCOVERY & SORTING SUITE
// ============================================================================

// TestChallenger_ResolverCallbackEdgeCases tests complex callback naming, sorting,
// case sensitivity, and non-callback exclusion.
func TestChallenger_ResolverCallbackEdgeCases(t *testing.T) {
	mockFS := fstest.MapFS{
		// Valid callbacks for beforeMigrate:
		"migrations/beforeMigrate__z.sql":            &fstest.MapFile{Data: []byte("-- z")},
		"migrations/beforeMigrate.sql":                &fstest.MapFile{Data: []byte("-- plain sql")},
		"migrations/beforeMigrate.sh":                 &fstest.MapFile{Data: []byte("#!/bin/bash\nexit 0")},
		"migrations/beforeMigrate__a.py":              &fstest.MapFile{Data: []byte("import sys; sys.exit(0)")},
		"migrations/beforeMigrate__01_init.sql":       &fstest.MapFile{Data: []byte("-- 01 init")},
		"migrations/beforeMigrate__UPPER.sql":         &fstest.MapFile{Data: []byte("-- UPPER")},
		"migrations/beforeMigrate__multi_part_desc.sql": &fstest.MapFile{Data: []byte("-- multi part")},

		// Files that should NOT be recognized as beforeMigrate callbacks:
		"migrations/beforeMigrateNotEvent.sql":        &fstest.MapFile{Data: []byte("-- not event")},
		"migrations/random_beforeMigrate.sql":         &fstest.MapFile{Data: []byte("-- prefix not event")},
		"migrations/beforeMigrate.txt":                &fstest.MapFile{Data: []byte("-- unsupported suffix")},

		// Callbacks for other lifecycle events:
		"migrations/afterVersioned__1.sql":            &fstest.MapFile{Data: []byte("-- av 1")},
		"migrations/beforeRepeatables.sql":            &fstest.MapFile{Data: []byte("-- br plain")},
		"migrations/afterMigrateApplied.sql":          &fstest.MapFile{Data: []byte("-- ama plain")},
		"migrations/afterValidateError__notify.sh":    &fstest.MapFile{Data: []byte("#!/bin/bash\nexit 0")},
		"migrations/afterInfoError.py":                &fstest.MapFile{Data: []byte("import sys; sys.exit(0)")},
	}

	cfg := DefaultResolverConfig()
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	r := NewResolver(cfg)

	result, err := r.Resolve()
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	cbs := result.Callbacks["beforeMigrate"]
	// Expected beforeMigrate valid callbacks: 7 items:
	// 1. beforeMigrate.sh (desc: "", filename: beforeMigrate.sh)
	// 2. beforeMigrate.sql (desc: "", filename: beforeMigrate.sql)
	// 3. beforeMigrate__01_init.sql (desc: "01 init")
	// 4. beforeMigrate__UPPER.sql (desc: "UPPER")
	// 5. beforeMigrate__a.py (desc: "a")
	// 6. beforeMigrate__multi_part_desc.sql (desc: "multi part desc")
	// 7. beforeMigrate__z.sql (desc: "z")
	if len(cbs) != 7 {
		t.Fatalf("expected 7 beforeMigrate callbacks, got %d", len(cbs))
	}

	// Verify descriptions are in strict lexicographical ascending order
	for i := 0; i < len(cbs)-1; i++ {
		descA := cbs[i].Description
		descB := cbs[i+1].Description
		if descA > descB {
			t.Errorf("sorting inversion at %d: %q > %q (%s vs %s)",
				i, descA, descB, cbs[i].Filename, cbs[i+1].Filename)
		}
	}

	// Verify exact order
	expectedFilenames := []string{
		"beforeMigrate.sh",
		"beforeMigrate.sql",
		"beforeMigrate__01_init.sql",
		"beforeMigrate__UPPER.sql",
		"beforeMigrate__a.py",
		"beforeMigrate__multi_part_desc.sql",
		"beforeMigrate__z.sql",
	}

	for i, exp := range expectedFilenames {
		if cbs[i].Filename != exp {
			t.Errorf("at index %d: expected %s, got %s", i, exp, cbs[i].Filename)
		}
	}

	// Verify script classification
	for _, cb := range cbs {
		if cb.Filename == "beforeMigrate.sh" {
			if !cb.IsScript || cb.Type != TypeScript {
				t.Errorf("expected beforeMigrate.sh to be script, got IsScript=%v, Type=%v", cb.IsScript, cb.Type)
			}
		}
		if cb.Filename == "beforeMigrate__a.py" {
			if !cb.IsScript || cb.Type != TypeScript {
				t.Errorf("expected beforeMigrate__a.py to be script, got IsScript=%v, Type=%v", cb.IsScript, cb.Type)
			}
		}
		if cb.Filename == "beforeMigrate.sql" {
			if cb.IsScript || cb.Type != TypeSQL {
				t.Errorf("expected beforeMigrate.sql to be SQL, got IsScript=%v, Type=%v", cb.IsScript, cb.Type)
			}
		}
	}

	// Verify other lifecycle events
	if len(result.Callbacks["afterVersioned"]) != 1 {
		t.Errorf("expected 1 afterVersioned callback, got %d", len(result.Callbacks["afterVersioned"]))
	}
	if len(result.Callbacks["beforeRepeatables"]) != 1 {
		t.Errorf("expected 1 beforeRepeatables callback, got %d", len(result.Callbacks["beforeRepeatables"]))
	}
	if len(result.Callbacks["afterMigrateApplied"]) != 1 {
		t.Errorf("expected 1 afterMigrateApplied callback, got %d", len(result.Callbacks["afterMigrateApplied"]))
	}
	if len(result.Callbacks["afterValidateError"]) != 1 {
		t.Errorf("expected 1 afterValidateError callback, got %d", len(result.Callbacks["afterValidateError"]))
	}
	if len(result.Callbacks["afterInfoError"]) != 1 {
		t.Errorf("expected 1 afterInfoError callback, got %d", len(result.Callbacks["afterInfoError"]))
	}
}

// TestChallenger_ResolverCustomSeparatorCallbacks tests callback resolution with custom separator
func TestChallenger_ResolverCustomSeparatorCallbacks(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/beforeMigrate-step1.sql": &fstest.MapFile{Data: []byte("-- step 1")},
		"migrations/beforeMigrate-step2.sql": &fstest.MapFile{Data: []byte("-- step 2")},
		"migrations/beforeMigrate.sql":       &fstest.MapFile{Data: []byte("-- plain")},
	}

	cfg := DefaultResolverConfig()
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}
	cfg.Separator = "-"
	r := NewResolver(cfg)

	result, err := r.Resolve()
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	cbs := result.Callbacks["beforeMigrate"]
	if len(cbs) != 3 {
		t.Fatalf("expected 3 callbacks with '-' separator, got %d", len(cbs))
	}

	if cbs[0].Description != "" || cbs[0].Filename != "beforeMigrate.sql" {
		t.Errorf("expected plain first, got %s", cbs[0].Filename)
	}
	if cbs[1].Description != "step1" || cbs[1].Filename != "beforeMigrate-step1.sql" {
		t.Errorf("expected step1 second, got %s", cbs[1].Filename)
	}
	if cbs[2].Description != "step2" || cbs[2].Filename != "beforeMigrate-step2.sql" {
		t.Errorf("expected step2 third, got %s", cbs[2].Filename)
	}
}
