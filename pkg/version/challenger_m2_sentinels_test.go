package version

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestChallenger_SentinelsVsNumerics(t *testing.T) {
	empty := Empty
	emptyParsed, _ := Parse("")
	zeroVal := Version{}

	current := Current
	currentParsed, _ := Parse("current")
	currentUpper, _ := Parse("CURRENT")

	next := Next
	nextParsed, _ := Parse("next")
	nextUpper, _ := Parse("NEXT")

	latest := Latest
	latestParsed, _ := Parse("latest")
	latestUpper, _ := Parse("LATEST")

	v0, _ := Parse("0")
	v0_0, _ := Parse("0.0")
	v0_0_0, _ := Parse("0.0.0")
	v001, _ := Parse("001")
	v0_1, _ := Parse("0.1")
	v1_0, _ := Parse("1.0")
	v1_0_0, _ := Parse("1.0.0")
	v1_0_1, _ := Parse("1.0.1")
	v1_001, _ := Parse("1.001") // Note: 1.001 is parsed as 1.1 in Flyway because 001 == 1
	v2_0, _ := Parse("2.0")
	v999, _ := Parse("999.999.999")

	// 1. Sentinel equivalence tests
	t.Run("SentinelSelfEquivalence", func(t *testing.T) {
		equivalents := [][]Version{
			{empty, emptyParsed, zeroVal},
			{current, currentParsed, currentUpper},
			{next, nextParsed, nextUpper},
			{latest, latestParsed, latestUpper},
			{v0, v0_0, v0_0_0},
			{v001, v1_0, v1_0_0},
		}

		for groupIdx, group := range equivalents {
			for i := 0; i < len(group); i++ {
				for j := 0; j < len(group); j++ {
					v1 := group[i]
					v2 := group[j]
					if cmp := v1.Compare(v2); cmp != 0 {
						t.Errorf("Group %d [%d vs %d]: expected Compare == 0, got %d", groupIdx, i, j, cmp)
					}
					if !v1.Equals(v2) {
						t.Errorf("Group %d [%d vs %d]: expected Equals == true", groupIdx, i, j)
					}
				}
			}
		}
	})

	// 2. Strict Chain Order: Empty < Current < Next < 0 == 0.0 == 0.0.0 < 0.1 < 001 == 1.0 < 1.0.1 < 1.001 < 2.0 < 999.999.999 < Latest
	t.Run("StrictRankAndNumericalChain", func(t *testing.T) {
		chain := []struct {
			name string
			ver  Version
		}{
			{"Empty", empty},
			{"Current", current},
			{"Next", next},
			{"0", v0},
			{"0.1", v0_1},
			{"1.0", v1_0},
			{"1.0.1", v1_0_1},
			{"1.001 (1.1)", v1_001},
			{"2.0", v2_0},
			{"999.999.999", v999},
			{"Latest", latest},
		}

		for i := 0; i < len(chain)-1; i++ {
			v1 := chain[i].ver
			v2 := chain[i+1].ver
			name1 := chain[i].name
			name2 := chain[i+1].name

			if cmp := v1.Compare(v2); cmp != -1 {
				t.Errorf("Expected %s < %s, got cmp=%d", name1, name2, cmp)
			}
			if cmp := v2.Compare(v1); cmp != 1 {
				t.Errorf("Expected %s > %s, got cmp=%d", name2, name1, cmp)
			}
			if v1.IsNewerThan(v2) {
				t.Errorf("Expected !(%s > %s)", name1, name2)
			}
			if !v2.IsNewerThan(v1) {
				t.Errorf("Expected %s > %s", name2, name1)
			}
		}
	})
}

func TestChallenger_OrderAxioms(t *testing.T) {
	// Comprehensive collection of sentinels and numerics
	testVersions := []Version{
		Empty,
		Current,
		Next,
		Latest,
		MustParse("0"),
		MustParse("0.0"),
		MustParse("0.0.0"),
		MustParse("001"),
		MustParse("0.1"),
		MustParse("1.0"),
		MustParse("1.0.0"),
		MustParse("1.0.1"),
		MustParse("1.001"),
		MustParse("2.0"),
		MustParse("20231015120000"),
		MustParse("999.999.999.999"),
	}

	// 1. Reflexivity: v.Compare(v) == 0, v.Equals(v) == true, v.IsAtLeast(v) == true
	t.Run("Reflexivity", func(t *testing.T) {
		for _, v := range testVersions {
			if cmp := v.Compare(v); cmp != 0 {
				t.Errorf("Reflexivity failed for %s: Compare(v, v) = %d, want 0", v.String(), cmp)
			}
			if !v.Equals(v) {
				t.Errorf("Reflexivity failed for %s: Equals(v, v) = false, want true", v.String())
			}
			if !v.IsAtLeast(v) {
				t.Errorf("Reflexivity failed for %s: IsAtLeast(v, v) = false, want true", v.String())
			}
			if v.IsNewerThan(v) {
				t.Errorf("Reflexivity failed for %s: IsNewerThan(v, v) = true, want false", v.String())
			}
		}
	})

	// 2. Anti-symmetry: v1.Compare(v2) == -v2.Compare(v1)
	t.Run("AntiSymmetry", func(t *testing.T) {
		for i, v1 := range testVersions {
			for j, v2 := range testVersions {
				cmp12 := v1.Compare(v2)
				cmp21 := v2.Compare(v1)
				if cmp12 != -cmp21 {
					t.Errorf("Anti-symmetry failed for (%d:%s, %d:%s): Compare(v1, v2) = %d, Compare(v2, v1) = %d",
						i, v1.String(), j, v2.String(), cmp12, cmp21)
				}
				if cmp12 == 0 && !v1.Equals(v2) {
					t.Errorf("Equals consistency failed for (%s, %s): Compare=0 but Equals=false", v1.String(), v2.String())
				}
				if cmp12 > 0 && !v1.IsNewerThan(v2) {
					t.Errorf("IsNewerThan consistency failed for (%s, %s): Compare>0 but IsNewerThan=false", v1.String(), v2.String())
				}
			}
		}
	})

	// 3. Transitivity:
	// If v1.Compare(v2) <= 0 and v2.Compare(v3) <= 0, then v1.Compare(v3) <= 0
	// If v1.Compare(v2) == 0 and v2.Compare(v3) == 0, then v1.Compare(v3) == 0
	// If v1.Compare(v2) < 0 and v2.Compare(v3) < 0, then v1.Compare(v3) < 0
	t.Run("Transitivity", func(t *testing.T) {
		for _, v1 := range testVersions {
			for _, v2 := range testVersions {
				for _, v3 := range testVersions {
					cmp12 := v1.Compare(v2)
					cmp23 := v2.Compare(v3)
					cmp13 := v1.Compare(v3)

					if cmp12 <= 0 && cmp23 <= 0 && cmp13 > 0 {
						t.Fatalf("Transitivity (<=) violation: %s <= %s and %s <= %s, but %s > %s",
							v1.String(), v2.String(), v2.String(), v3.String(), v1.String(), v3.String())
					}
					if cmp12 == 0 && cmp23 == 0 && cmp13 != 0 {
						t.Fatalf("Transitivity (==) violation: %s == %s and %s == %s, but %s != %s",
							v1.String(), v2.String(), v2.String(), v3.String(), v1.String(), v3.String())
					}
					if cmp12 < 0 && cmp23 < 0 && cmp13 >= 0 {
						t.Fatalf("Transitivity (<) violation: %s < %s and %s < %s, but %s >= %s",
							v1.String(), v2.String(), v2.String(), v3.String(), v1.String(), v3.String())
					}
				}
			}
		}
	})
}

func TestChallenger_StressRandomizedGenerators(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	var randomVersions []Version
	for i := 0; i < 50; i++ {
		numParts := 1 + rng.Intn(6)
		parts := make([]string, numParts)
		for j := 0; j < numParts; j++ {
			// mix of 0s, leading zeros, and large numbers
			switch rng.Intn(4) {
			case 0:
				parts[j] = "0"
			case 1:
				parts[j] = fmt.Sprintf("00%d", rng.Intn(10))
			case 2:
				parts[j] = fmt.Sprintf("%d", rng.Intn(1000))
			case 3:
				parts[j] = fmt.Sprintf("%d", rng.Int63n(1_000_000_000_000))
			}
		}
		verStr := ""
		for j, p := range parts {
			if j > 0 {
				verStr += "."
			}
			verStr += p
		}
		v, err := Parse(verStr)
		if err != nil {
			t.Fatalf("Parse failed for generated string %q: %v", verStr, err)
		}
		randomVersions = append(randomVersions, v)
	}

	// Add sentinels to pool
	pool := append([]Version{Empty, Current, Next, Latest}, randomVersions...)

	// Stress anti-symmetry and transitivity on 10,000 random triples
	for i := 0; i < 10000; i++ {
		v1 := pool[rng.Intn(len(pool))]
		v2 := pool[rng.Intn(len(pool))]
		v3 := pool[rng.Intn(len(pool))]

		// Anti-symmetry
		if v1.Compare(v2) != -v2.Compare(v1) {
			t.Fatalf("Anti-symmetry violation for %s and %s", v1.String(), v2.String())
		}

		// Transitivity
		cmp12 := v1.Compare(v2)
		cmp23 := v2.Compare(v3)
		cmp13 := v1.Compare(v3)

		if cmp12 <= 0 && cmp23 <= 0 && cmp13 > 0 {
			t.Fatalf("Transitivity violation: %s <= %s <= %s but %s > %s",
				v1.String(), v2.String(), v3.String(), v1.String(), v3.String())
		}
	}

	// Verify all numeric versions are sandwiched between Next and Latest:
	// Empty < Current < Next < randomNumeric < Latest
	for _, rv := range randomVersions {
		if Empty.Compare(rv) != -1 {
			t.Errorf("Expected Empty < %s", rv.String())
		}
		if Current.Compare(rv) != -1 {
			t.Errorf("Expected Current < %s", rv.String())
		}
		if Next.Compare(rv) != -1 {
			t.Errorf("Expected Next < %s", rv.String())
		}
		if rv.Compare(Latest) != -1 {
			t.Errorf("Expected %s < Latest", rv.String())
		}
	}
}
