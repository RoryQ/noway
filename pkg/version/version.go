package version

import (
	"fmt"
	"math/big"
	"strings"
)

// Predefined special versions.
var (
	Empty   = Version{displayText: "<< Empty >>", predefined: true, empty: true}
	Latest  = Version{displayText: "latest", rawVersion: "latest", predefined: true, latest: true}
	Current = Version{displayText: "current", rawVersion: "current", predefined: true, current: true}
	Next    = Version{displayText: "next", rawVersion: "next", predefined: true, next: true}
)

// Version represents a Flyway migration version.
// Format: 1, 1.0, 005, 1.2.3.4, 20231015120000, etc.
type Version struct {
	parts       []*big.Int
	displayText string
	rawVersion  string
	predefined  bool
	empty       bool
	latest      bool
	current     bool
	next        bool
}

// Parse parses a version string into a Version object.
// Follows exact Flyway normalization and parsing rules:
// - Underscores '_' are normalized to dots '.'
// - String is tokenized by '.' into big integers
// - Trailing zeros are removed for comparison purposes
func Parse(versionStr string) (Version, error) {
	if versionStr == "" {
		return Empty, nil
	}
	trimmed := strings.TrimSpace(versionStr)
	if strings.EqualFold(trimmed, "current") {
		return Current, nil
	}
	if strings.EqualFold(trimmed, "next") {
		return Next, nil
	}
	if strings.EqualFold(trimmed, "latest") {
		return Latest, nil
	}

	normalized := strings.ReplaceAll(trimmed, "_", ".")
	rawParts := strings.Split(normalized, ".")
	parts := make([]*big.Int, 0, len(rawParts))

	for _, part := range rawParts {
		if part == "" {
			return Version{}, fmt.Errorf("invalid version '%s': empty segment", versionStr)
		}
		num := new(big.Int)
		_, ok := num.SetString(part, 10)
		if !ok || num.Sign() < 0 {
			return Version{}, fmt.Errorf("invalid version '%s': part '%s' is not a non-negative integer", versionStr, part)
		}
		parts = append(parts, num)
	}

	// Remove trailing zeros for comparison
	comparisonParts := make([]*big.Int, len(parts))
	copy(comparisonParts, parts)
	for i := len(comparisonParts) - 1; i > 0; i-- {
		if comparisonParts[i].Sign() == 0 {
			comparisonParts = comparisonParts[:i]
		} else {
			break
		}
	}

	return Version{
		parts:       comparisonParts,
		displayText: normalized,
		rawVersion:  versionStr,
	}, nil
}

// MustParse parses a version string and panics if invalid.
func MustParse(versionStr string) Version {
	v, err := Parse(versionStr)
	if err != nil {
		panic(err)
	}
	return v
}

// New parses a version string into a Version object.
func New(versionStr string) (Version, error) {
	return Parse(versionStr)
}

// Compare compares two versions.
func Compare(v1, v2 Version) int {
	return v1.Compare(v2)
}

// String returns the normalized display text.
func (v Version) String() string {
	if v.IsEmpty() {
		return ""
	}
	return v.displayText
}

// Normalized returns the canonical string with trailing zeros stripped for equivalence lookup.
func (v Version) Normalized() string {
	if v.IsEmpty() {
		return ""
	}
	if v.predefined {
		return v.displayText
	}
	if len(v.parts) == 0 {
		return "0"
	}
	partsStr := make([]string, len(v.parts))
	for i, p := range v.parts {
		partsStr[i] = p.String()
	}
	return strings.Join(partsStr, ".")
}

// Raw returns the original raw string representation.
func (v Version) Raw() string {
	return v.rawVersion
}

// IsEmpty returns true if version is Empty.
func (v Version) IsEmpty() bool {
	return v.empty || (len(v.parts) == 0 && !v.predefined)
}

// IsPredefined returns true if this is a predefined special version.
func (v Version) IsPredefined() bool {
	return v.predefined
}

// IsCurrent returns true if this is the Current sentinel.
func (v Version) IsCurrent() bool {
	return v.current
}

// IsNext returns true if this is the Next sentinel.
func (v Version) IsNext() bool {
	return v.next
}

// IsLatest returns true if this is the Latest sentinel.
func (v Version) IsLatest() bool {
	return v.latest
}

// rank returns the comparison tier of the version:
// Tier 1: Empty
// Tier 2: Current
// Tier 3: Next
// Tier 4: Numeric versions
// Tier 5: Latest
func (v Version) rank() int {
	if v.IsEmpty() {
		return 1
	}
	if v.current {
		return 2
	}
	if v.next {
		return 3
	}
	if v.latest {
		return 5
	}
	return 4
}

// Compare compares this version to another version.
// 5-tier rank ordering:
// Empty (rank 1) < Current (rank 2) < Next (rank 3) < Numeric Versions (rank 4) < Latest (rank 5)
// Returns:
// -1 if v < other
//  0 if v == other
//  1 if v > other
func (v Version) Compare(other Version) int {
	r1 := v.rank()
	r2 := other.rank()
	if r1 != r2 {
		if r1 < r2 {
			return -1
		}
		return 1
	}

	// Both have the same rank. If not rank 4 (numeric), they are both the same sentinel.
	if r1 != 4 {
		return 0
	}

	maxLen := len(v.parts)
	if len(other.parts) > maxLen {
		maxLen = len(other.parts)
	}

	zero := big.NewInt(0)
	for i := 0; i < maxLen; i++ {
		var p1, p2 *big.Int
		if i < len(v.parts) {
			p1 = v.parts[i]
		} else {
			p1 = zero
		}

		if i < len(other.parts) {
			p2 = other.parts[i]
		} else {
			p2 = zero
		}

		cmp := p1.Cmp(p2)
		if cmp != 0 {
			return cmp
		}
	}

	return 0
}

// Equals returns true if two versions are equal.
func (v Version) Equals(other Version) bool {
	return v.Compare(other) == 0
}

// IsNewerThan returns true if v > other.
func (v Version) IsNewerThan(other Version) bool {
	return v.Compare(other) > 0
}

// IsAtLeast returns true if v >= other.
func (v Version) IsAtLeast(other Version) bool {
	return v.Compare(other) >= 0
}

// Major returns the major version number.
func (v Version) Major() *big.Int {
	if len(v.parts) > 0 {
		return new(big.Int).Set(v.parts[0])
	}
	return big.NewInt(0)
}

// MajorAsString returns the major version number as a string.
func (v Version) MajorAsString() string {
	return v.Major().String()
}
