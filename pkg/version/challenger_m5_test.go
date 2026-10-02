package version

import (
	"fmt"
	"math/big"
	"sort"
	"testing"
)

func TestChallengerM5_ExhaustiveOrderAxioms(t *testing.T) {
	// Build a diverse corpus of 100+ version strings covering all variations
	var versionStrings []string

	// Sentinels
	sentinels := []string{"", "current", "next", "latest"}
	versionStrings = append(versionStrings, sentinels...)

	// Basic numerics and zero variants
	zeros := []string{"0", "0.0", "0.0.0", "00", "00.00", "000"}
	versionStrings = append(versionStrings, zeros...)

	// Integer progressions
	for i := 1; i <= 20; i++ {
		versionStrings = append(versionStrings, fmt.Sprintf("%d", i))
		versionStrings = append(versionStrings, fmt.Sprintf("0%d", i))
		versionStrings = append(versionStrings, fmt.Sprintf("%d.0", i))
		versionStrings = append(versionStrings, fmt.Sprintf("%d.0.0", i))
	}

	// Semver dot progressions
	dots := []string{
		"0.1", "0.01", "0.001", "0.2", "0.10", "0.100",
		"1.0.1", "1.001", "1.01", "1.1", "1.1.0", "1.1.1",
		"1.2", "1.2.0", "1.9", "1.09", "1.10", "1.10.0",
		"1.2.3.4", "1.2.3.4.5", "1.2.3.4.5.6",
		"2.0", "2.0.0", "2.0.1", "2.1", "2.10",
		"9.9.9", "10.0.0", "10.0.1", "10.1.0",
	}
	versionStrings = append(versionStrings, dots...)

	// Underscore notation (Flyway compatibility)
	underscores := []string{
		"1_0", "1_0_0", "1_0_1", "1_1", "2_0_0_1", "2023_10_15_12_00_00",
	}
	versionStrings = append(versionStrings, underscores...)

	// Huge numbers (> 64-bit integers)
	huge := []string{
		"18446744073709551615",                             // 2^64 - 1
		"18446744073709551616",                             // 2^64
		"999999999999999999999999999999999999999999",       // 42 digits
		"999999999999999999999999999999999999999999.1",   // huge.1
		"1.999999999999999999999999999999999999999999",   // 1.huge
	}
	versionStrings = append(versionStrings, huge...)

	// Timestamps
	timestamps := []string{
		"20230101000000", "20230101000001", "20231231235959",
		"20240101000000", "20251003112233",
	}
	versionStrings = append(versionStrings, timestamps...)

	if len(versionStrings) < 100 {
		t.Fatalf("expected at least 100 version strings, got %d", len(versionStrings))
	}

	// Parse all versions
	parsedVersions := make([]Version, len(versionStrings))
	for i, s := range versionStrings {
		v, err := Parse(s)
		if err != nil {
			t.Fatalf("unexpected parse failure for %q: %v", s, err)
		}
		parsedVersions[i] = v
	}

	// 1. Reflexivity: v == v, Compare(v, v) == 0, IsAtLeast(v, v) == true
	t.Run("Reflexivity", func(t *testing.T) {
		for i, v := range parsedVersions {
			if cmp := v.Compare(v); cmp != 0 {
				t.Fatalf("reflexivity failed for %s (raw %q): got %d", v.String(), versionStrings[i], cmp)
			}
			if !v.Equals(v) {
				t.Fatalf("equals reflexivity failed for %s", v.String())
			}
			if !v.IsAtLeast(v) {
				t.Fatalf("isAtLeast reflexivity failed for %s", v.String())
			}
			if v.IsNewerThan(v) {
				t.Fatalf("isNewerThan reflexivity failed for %s", v.String())
			}
		}
	})

	// 2. Anti-symmetry: Compare(v1, v2) == -Compare(v2, v1) across all N x N pairs
	t.Run("AntiSymmetry", func(t *testing.T) {
		for i, v1 := range parsedVersions {
			for j, v2 := range parsedVersions {
				cmp12 := v1.Compare(v2)
				cmp21 := v2.Compare(v1)
				if cmp12 != -cmp21 {
					t.Fatalf("anti-symmetry violation for (%q, %q): cmp12=%d, cmp21=%d",
						versionStrings[i], versionStrings[j], cmp12, cmp21)
				}
				if cmp12 == 0 && !v1.Equals(v2) {
					t.Fatalf("equals inconsistency for (%q, %q): cmp=0 but equals=false",
						versionStrings[i], versionStrings[j])
				}
				if cmp12 > 0 && !v1.IsNewerThan(v2) {
					t.Fatalf("isNewerThan inconsistency for (%q, %q)",
						versionStrings[i], versionStrings[j])
				}
			}
		}
	})

	// 3. Transitivity on sample triplets
	t.Run("Transitivity", func(t *testing.T) {
		// Test across step-sampled combinations
		n := len(parsedVersions)
		for i := 0; i < n; i += 3 {
			for j := 0; j < n; j += 3 {
				for k := 0; k < n; k += 3 {
					v1 := parsedVersions[i]
					v2 := parsedVersions[j]
					v3 := parsedVersions[k]

					cmp12 := v1.Compare(v2)
					cmp23 := v2.Compare(v3)
					cmp13 := v1.Compare(v3)

					if cmp12 <= 0 && cmp23 <= 0 && cmp13 > 0 {
						t.Fatalf("transitivity (<=) violation: %s <= %s <= %s but %s > %s",
							v1.String(), v2.String(), v3.String(), v1.String(), v3.String())
					}
					if cmp12 == 0 && cmp23 == 0 && cmp13 != 0 {
						t.Fatalf("transitivity (==) violation: %s == %s == %s but %s != %s",
							v1.String(), v2.String(), v3.String(), v1.String(), v3.String())
					}
					if cmp12 < 0 && cmp23 < 0 && cmp13 >= 0 {
						t.Fatalf("transitivity (<) violation: %s < %s < %s but %s >= %s",
							v1.String(), v2.String(), v3.String(), v1.String(), v3.String())
					}
				}
			}
		}
	})
}

func TestChallengerM5_SemverVsIntegerEdgeCases(t *testing.T) {
	cases := []struct {
		v1       string
		v2       string
		expected int // -1 (v1 < v2), 0 (v1 == v2), 1 (v1 > v2)
		reason   string
	}{
		{"1.0.1", "1.001", -1, "1.001 has parts [1, 1] while 1.0.1 has parts [1, 0, 1]"},
		{"1.001", "1.1", 0, "1.001 is parsed as parts [1, 1]"},
		{"1", "1.0", 0, "trailing zero stripped"},
		{"1.0", "1.0.0", 0, "trailing zeros stripped"},
		{"1.0.0.0", "1", 0, "all trailing zeros stripped"},
		{"1.2", "1.10", -1, "2 < 10 numerically"},
		{"1.10", "1.2", 1, "10 > 2 numerically"},
		{"0", "0.0", 0, "zeros are equivalent"},
		{"0.0", "00", 0, "leading zero in 00 parses to 0"},
		{"001", "1", 0, "leading zero in 001 parses to 1"},
		{"1.09", "1.9", 0, "09 parses to 9"},
		{"1.09", "1.1", 1, "9 > 1"},
		{"20231015120000", "20231015120001", -1, "timestamp comparison"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s_vs_%s", tc.v1, tc.v2), func(t *testing.T) {
			parsed1, err1 := Parse(tc.v1)
			parsed2, err2 := Parse(tc.v2)
			if err1 != nil || err2 != nil {
				t.Fatalf("parse error: %v, %v", err1, err2)
			}
			cmp := parsed1.Compare(parsed2)
			if cmp != tc.expected {
				t.Errorf("Compare(%s, %s) = %d, want %d (%s)", tc.v1, tc.v2, cmp, tc.expected, tc.reason)
			}
		})
	}
}

func TestChallengerM5_InvalidVersionInputs(t *testing.T) {
	invalids := []string{
		"1..2",
		".1",
		"1.",
		"1__2",
		"_1",
		"1_",
		"1.2.3-beta",
		"v1.0",
		"V1.0",
		"-1",
		"1.-2",
		"1. 2",
		"1.2.foo",
		"NaN",
		"1.2.3.4.5.6.7.8.9.a",
	}

	for _, inv := range invalids {
		t.Run(fmt.Sprintf("invalid_%s", inv), func(t *testing.T) {
			_, err := Parse(inv)
			if err == nil {
				t.Errorf("expected error for invalid version %q, got nil", inv)
			}
		})
	}
}

func TestChallengerM5_SortStability(t *testing.T) {
	input := []string{
		"10.0", "1.0", "2.0", "1.1", "1.0.1", "0.1", "current", "latest", "next", "",
	}

	versions := make([]Version, len(input))
	for i, s := range input {
		versions[i] = MustParse(s)
	}

	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Compare(versions[j]) < 0
	})

	expectedOrder := []string{
		"",        // Empty (rank 1)
		"current", // Current (rank 2)
		"next",    // Next (rank 3)
		"0.1",     // rank 4
		"1.0",     // rank 4
		"1.0.1",   // rank 4
		"1.1",     // rank 4
		"2.0",     // rank 4
		"10.0",    // rank 4
		"latest",  // Latest (rank 5)
	}

	for i, exp := range expectedOrder {
		got := versions[i].Raw()
		if got != exp {
			t.Errorf("index %d: expected %q, got %q", i, exp, got)
		}
	}
}

func TestChallengerM5_MajorAndHugeNumbers(t *testing.T) {
	vHuge := MustParse("999999999999999999999999999999999999999999.1.2")
	expectedMajor, _ := new(big.Int).SetString("999999999999999999999999999999999999999999", 10)

	if vHuge.Major().Cmp(expectedMajor) != 0 {
		t.Errorf("expected huge major %s, got %s", expectedMajor.String(), vHuge.Major().String())
	}
	if vHuge.MajorAsString() != "999999999999999999999999999999999999999999" {
		t.Errorf("MajorAsString failed: %s", vHuge.MajorAsString())
	}

	// Empty major
	if Empty.Major().Sign() != 0 {
		t.Errorf("expected 0 for Empty major")
	}
}
