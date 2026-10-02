package checksum

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestChallengerM5_LineEndingsMatrix(t *testing.T) {
	// Various permutations of line endings representing the same logical statements
	variations := []struct {
		name    string
		content string
	}{
		{"pure LF", "SELECT 1;\nSELECT 2;\nSELECT 3;\n"},
		{"pure CRLF", "SELECT 1;\r\nSELECT 2;\r\nSELECT 3;\r\n"},
		{"mixed LF and CRLF", "SELECT 1;\nSELECT 2;\r\nSELECT 3;\n"},
		{"mixed with CR at line end", "SELECT 1;\r\nSELECT 2;\nSELECT 3;\r\n"},
		{"no trailing newline LF", "SELECT 1;\nSELECT 2;\nSELECT 3;"},
		{"no trailing newline CRLF", "SELECT 1;\r\nSELECT 2;\r\nSELECT 3;"},
	}

	firstSum, err := CalculateString(variations[0].content)
	if err != nil {
		t.Fatalf("failed to calculate checksum for %s: %v", variations[0].name, err)
	}

	for _, v := range variations[1:] {
		sum, err := CalculateString(v.content)
		if err != nil {
			t.Errorf("failed to calculate checksum for %s: %v", v.name, err)
			continue
		}
		if sum != firstSum {
			t.Errorf("line ending variance between %s (%d) and %s (%d)", variations[0].name, firstSum, v.name, sum)
		}
	}
}

func TestChallengerM5_BOMHandlingAdversarial(t *testing.T) {
	t.Run("BOM on non-empty first line is stripped", func(t *testing.T) {
		plain := "CREATE TABLE users (id INT64);"
		withBOM := "\ufeffCREATE TABLE users (id INT64);"
		s1, _ := CalculateString(plain)
		s2, _ := CalculateString(withBOM)
		if s1 != s2 {
			t.Errorf("expected BOM on first line to be stripped: %d != %d", s1, s2)
		}
	})

	t.Run("BOM on empty first line followed by content", func(t *testing.T) {
		plain := "\nCREATE TABLE users (id INT64);"
		withBOM := "\ufeff\nCREATE TABLE users (id INT64);"
		s1, _ := CalculateString(plain)
		s2, _ := CalculateString(withBOM)
		if s1 != s2 {
			t.Errorf("expected BOM on empty first line to be stripped: %d != %d", s1, s2)
		}
	})

	t.Run("BOM in middle of line is NOT stripped", func(t *testing.T) {
		plain := "SELECT '\ufeff';"
		withoutBOM := "SELECT '';"
		s1, _ := CalculateString(plain)
		s2, _ := CalculateString(withoutBOM)
		if s1 == s2 {
			t.Errorf("BOM inside string literal should not be stripped: s1=%d == s2=%d", s1, s2)
		}
	})

	t.Run("BOM on second line is NOT stripped", func(t *testing.T) {
		line1 := "SELECT 1;\n\ufeffSELECT 2;"
		line2 := "SELECT 1;\nSELECT 2;"
		s1, _ := CalculateString(line1)
		s2, _ := CalculateString(line2)
		if s1 == s2 {
			t.Errorf("BOM on second line should not be stripped: s1=%d == s2=%d", s1, s2)
		}
	})
}

func TestChallengerM5_EmptyAndWhitespaceFiles(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected int64
	}{
		{"empty string", "", 0},
		{"lone BOM", "\ufeff", 0},
		{"lone LF", "\n", 0},
		{"lone CRLF", "\r\n", 0},
		{"multiple blank lines", "\n\n\n", 0},
		{"multiple CRLF blank lines", "\r\n\r\n\r\n", 0},
		{"BOM followed by blank lines", "\ufeff\r\n\r\n", 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sum, err := CalculateString(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sum != tc.expected {
				t.Errorf("expected %d, got %d", tc.expected, sum)
			}
		})
	}
}

func TestChallengerM5_CombineChecksumsAdversarial(t *testing.T) {
	t.Run("empty slice returns 0", func(t *testing.T) {
		if res := CombineChecksums(nil); res != 0 {
			t.Errorf("expected 0 for nil slice, got %d", res)
		}
		if res := CombineChecksums([]int64{}); res != 0 {
			t.Errorf("expected 0 for empty slice, got %d", res)
		}
	})

	t.Run("single element returns itself identity", func(t *testing.T) {
		vals := []int64{0, 1, -1, 42, -999999, 2147483647, -2147483648}
		for _, v := range vals {
			if res := CombineChecksums([]int64{v}); res != v {
				t.Errorf("identity failed for %d: got %d", v, res)
			}
		}
	})

	t.Run("order dependency and determinism", func(t *testing.T) {
		c1 := []int64{100, 200, 300}
		c2 := []int64{300, 200, 100}

		res1 := CombineChecksums(c1)
		res1Repeat := CombineChecksums(c1)
		if res1 != res1Repeat {
			t.Errorf("determinism failed: %d != %d", res1, res1Repeat)
		}

		res2 := CombineChecksums(c2)
		if res1 == res2 {
			t.Errorf("expected different checksums for different order: %d == %d", res1, res2)
		}
	})

	t.Run("negative numbers and 32-bit wrap", func(t *testing.T) {
		c := []int64{-12345678, 87654321, -1}
		res := CombineChecksums(c)
		// Checksum must fit in int32
		if res < -2147483648 || res > 2147483647 {
			t.Errorf("combined checksum out of 32-bit signed range: %d", res)
		}
	})
}

func TestChallengerM5_CalculateBytesParity(t *testing.T) {
	data := []byte("CREATE TABLE bigquery_table (x INT64, y STRING);\n")
	sum1, err1 := Calculate(bytes.NewReader(data))
	sum2, err2 := CalculateBytes(data)
	sum3, err3 := CalculateString(string(data))

	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatalf("unexpected error: %v, %v, %v", err1, err2, err3)
	}
	if sum1 != sum2 || sum2 != sum3 {
		t.Errorf("parity mismatch across methods: %d vs %d vs %d", sum1, sum2, sum3)
	}
}

func TestChallengerM5_LargeFileAndScannerLimits(t *testing.T) {
	t.Run("1MB script computes successfully", func(t *testing.T) {
		var sb strings.Builder
		for i := 0; i < 20000; i++ {
			sb.WriteString("SELECT 1 AS col, 'some text to make it longer' AS col2;\n")
		}
		script := sb.String()
		sum, err := CalculateString(script)
		if err != nil {
			t.Fatalf("failed on 1MB script: %v", err)
		}
		if sum == 0 {
			t.Errorf("expected non-zero checksum for large script")
		}
	})

	t.Run("line exceeding 10MB scanner buffer returns error", func(t *testing.T) {
		// Create a single line exceeding 10MB
		hugeLine := strings.Repeat("A", 11*1024*1024)
		_, err := CalculateString(hugeLine)
		if err == nil {
			t.Errorf("expected error for line exceeding 10MB buffer, got nil")
		} else if err != bufio.ErrTooLong {
			t.Logf("got expected error for oversized line: %v", err)
		}
	})
}
