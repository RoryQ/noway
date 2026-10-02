package version

import (
	"testing"
)

func TestVersionParsingAndComparison(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected int // -1 (v1 < v2), 0 (v1 == v2), 1 (v1 > v2)
	}{
		{"1", "1", 0},
		{"1.0", "1", 0},
		{"1.0.0", "1.0", 0},
		{"1_0_0", "1.0", 0},
		{"01", "1", 0},
		{"1.1", "1.2", -1},
		{"1.2", "1.1", 1},
		{"1.10", "1.2", 1},
		{"1.2", "1.10", -1},
		{"2", "1.99.99", 1},
		{"2.0.1", "2.0.0.1", 1},
		{"20231015120000", "20231015120001", -1},
		{"1.0.0.0.1", "1", 1},
	}

	for _, tt := range tests {
		ver1, err := Parse(tt.v1)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", tt.v1, err)
		}
		ver2, err := Parse(tt.v2)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", tt.v2, err)
		}

		cmp := ver1.Compare(ver2)
		if cmp != tt.expected {
			t.Errorf("Compare(%q, %q) = %d; want %d", tt.v1, tt.v2, cmp, tt.expected)
		}

		if tt.expected == 0 && !ver1.Equals(ver2) {
			t.Errorf("Equals(%q, %q) = false; want true", tt.v1, tt.v2)
		}
		if tt.expected > 0 && !ver1.IsNewerThan(ver2) {
			t.Errorf("IsNewerThan(%q, %q) = false; want true", tt.v1, tt.v2)
		}
		if tt.expected >= 0 && !ver1.IsAtLeast(ver2) {
			t.Errorf("IsAtLeast(%q, %q) = false; want true", tt.v1, tt.v2)
		}
	}
}

func TestVersionSpecialMarkers(t *testing.T) {
	v1, _ := Parse("1.0")
	v0, _ := Parse("0")
	current, _ := Parse("current")
	next, _ := Parse("next")
	latest, _ := Parse("latest")
	empty, _ := Parse("")

	if !latest.IsNewerThan(v1) {
		t.Errorf("expected latest > 1.0")
	}
	if !v1.IsNewerThan(empty) {
		t.Errorf("expected 1.0 > empty")
	}
	if !empty.IsEmpty() {
		t.Errorf("expected empty.IsEmpty() == true")
	}

	// 5-tier rank ordering tests:
	// Empty (1) < Current (2) < Next (3) < Numeric Versions (4) < Latest (5)
	if v0.Compare(current) != 1 {
		t.Errorf("expected v0 > current, got cmp=%d", v0.Compare(current))
	}
	if current.Compare(v0) != -1 {
		t.Errorf("expected current < v0, got cmp=%d", current.Compare(v0))
	}
	if v0.Compare(next) != 1 {
		t.Errorf("expected v0 > next, got cmp=%d", v0.Compare(next))
	}
	if next.Compare(v0) != -1 {
		t.Errorf("expected next < v0, got cmp=%d", next.Compare(v0))
	}
	if current.Compare(next) != -1 {
		t.Errorf("expected current < next, got cmp=%d", current.Compare(next))
	}
	if next.Compare(current) != 1 {
		t.Errorf("expected next > current, got cmp=%d", next.Compare(current))
	}
	if current.Compare(current) != 0 {
		t.Errorf("expected current == current, got cmp=%d", current.Compare(current))
	}
	if next.Compare(next) != 0 {
		t.Errorf("expected next == next, got cmp=%d", next.Compare(next))
	}
	if empty.Compare(current) != -1 {
		t.Errorf("expected empty < current, got cmp=%d", empty.Compare(current))
	}
	if latest.Compare(v0) != 1 {
		t.Errorf("expected latest > v0, got cmp=%d", latest.Compare(v0))
	}
}

func TestInvalidVersions(t *testing.T) {
	invalid := []string{"1..0", "1.a", "v1.0", "-1", "1.-2"}
	for _, inv := range invalid {
		_, err := Parse(inv)
		if err == nil {
			t.Errorf("expected error for invalid version %q, got nil", inv)
		}
	}
}
