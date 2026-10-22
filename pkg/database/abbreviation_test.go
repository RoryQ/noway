package database

import (
	"strings"
	"testing"
)

func TestAbbreviateScript_FlywayParity(t *testing.T) {
	// Under 1000 runes stays unchanged
	s1000 := strings.Repeat("a", 1000)
	if AbbreviateScript(s1000) != s1000 {
		t.Fatalf("expected 1000-char string unchanged")
	}

	// 1200-character script: result must be "..." plus characters 3 to 999 of the input
	// (indices 3 to 1000, 997 characters)
	var sb strings.Builder
	for i := 0; i < 1200; i++ {
		sb.WriteByte(byte('a' + (i % 26)))
	}
	input := sb.String()
	abbr := AbbreviateScript(input)

	if len([]rune(abbr)) != 1000 {
		t.Fatalf("expected length 1000, got %d", len([]rune(abbr)))
	}

	runes := []rune(input)
	expected := "..." + string(runes[3:1000])
	if abbr != expected {
		t.Fatalf("expected %q, got %q", expected, abbr)
	}
}

func TestAbbreviateDescription_FlywayParity(t *testing.T) {
	d200 := strings.Repeat("d", 200)
	if AbbreviateDescription(d200) != d200 {
		t.Fatalf("expected 200-char description unchanged")
	}

	d250 := strings.Repeat("d", 250)
	abbr := AbbreviateDescription(d250)
	expected := strings.Repeat("d", 197) + "..."
	if abbr != expected {
		t.Fatalf("expected %q, got %q", expected, abbr)
	}
}
