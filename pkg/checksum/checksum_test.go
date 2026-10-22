package checksum

import (
	"testing"
)

func TestChecksumLineEndingIndependence(t *testing.T) {
	unixScript := "CREATE TABLE test (\n  id INT64\n);\n"
	windowsScript := "CREATE TABLE test (\r\n  id INT64\r\n);\r\n"
	noTrailingNewline := "CREATE TABLE test (\n  id INT64\n);"

	sumUnix, err := CalculateString(unixScript)
	if err != nil {
		t.Fatalf("CalculateString unix error: %v", err)
	}

	sumWin, err := CalculateString(windowsScript)
	if err != nil {
		t.Fatalf("CalculateString win error: %v", err)
	}

	if sumUnix != sumWin {
		t.Errorf("Unix checksum %d != Windows checksum %d", sumUnix, sumWin)
	}

	// Note: trailing newline creates an empty line in scanner if there's an empty line at the end,
	// let's check noTrailingNewline vs trailing newline
	_ = noTrailingNewline
}

func TestChecksumBOMHandling(t *testing.T) {
	withoutBOM := "CREATE TABLE users (id INT64);"
	withBOM := "\ufeffCREATE TABLE users (id INT64);"

	sum1, err := CalculateString(withoutBOM)
	if err != nil {
		t.Fatalf("CalculateString error: %v", err)
	}

	sum2, err := CalculateString(withBOM)
	if err != nil {
		t.Fatalf("CalculateString error: %v", err)
	}

	if sum1 != sum2 {
		t.Errorf("Checksum with BOM (%d) != without BOM (%d)", sum2, sum1)
	}
}

func TestChecksumEmpty(t *testing.T) {
	sum, err := CalculateString("")
	if err != nil {
		t.Fatalf("CalculateString empty error: %v", err)
	}
	if sum != 0 {
		t.Errorf("expected 0 for empty, got %d", sum)
	}
}

func TestChecksumLoneCR(t *testing.T) {
	// Flyway parity: a lone \r (CR) ends a line in Java BufferedReader.readLine()
	// "a\rb\n" has lines "a" and "b", yielding -1635563411
	sum, err := CalculateString("a\rb\n")
	if err != nil {
		t.Fatalf("CalculateString error: %v", err)
	}
	if sum != -1635563411 {
		t.Errorf("expected -1635563411 for 'a\\rb\\n', got %d", sum)
	}
}
