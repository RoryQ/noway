package migrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roryq/noway/pkg/database/mock"
)

func TestDryRunBuilder_EscapesLiteralsAndComments(t *testing.T) {
	db := mock.NewMockDatabase()
	builder := NewDryRunBuilder(db, "analytics", "flyway_schema_history", "mydb\nDROP DATABASE mydb;", "alice\nadmin")

	builder.WriteHeader("MIGRATE", "2.0")

	// Verify header comments replaced newlines with spaces
	headerOutput := builder.String()
	if strings.Contains(headerOutput, "\nDROP DATABASE mydb;") {
		t.Fatalf("expected newline in database name to be sanitized in comments, got: %s", headerOutput)
	}

	// Verify history insert escaping
	maliciousDesc := `u\' OR 1=1 --`
	maliciousScript := "V1__test\n.sql"
	builder.WriteHistoryInsert(1, "1.0", maliciousDesc, "SQL", maliciousScript, 12345, "user'--")

	sqlOutput := builder.String()
	// Must contain escaped backslash and escaped quote
	expectedDescLiteral := `'u\\\' OR 1=1 --'`
	if !strings.Contains(sqlOutput, expectedDescLiteral) {
		t.Errorf("expected history insert to contain %s, got: %s", expectedDescLiteral, sqlOutput)
	}
	expectedScriptLiteral := `'V1__test\n.sql'`
	if !strings.Contains(sqlOutput, expectedScriptLiteral) {
		t.Errorf("expected history insert to contain %s, got: %s", expectedScriptLiteral, sqlOutput)
	}
}

func TestDryRunBuilder_SaveFilePermissions(t *testing.T) {
	db := mock.NewMockDatabase()
	builder := NewDryRunBuilder(db, "analytics", "flyway_schema_history", "testdb", "user")
	builder.WriteHeader("MIGRATE", "1.0")

	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "nested", "dryrun")
	outFile := filepath.Join(outDir, "dryrun.sql")

	if err := builder.Save(outFile); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	fi, err := os.Stat(outFile)
	if err != nil {
		t.Fatalf("failed to stat output file: %v", err)
	}

	// Permissions should be 0600 (not world readable)
	perm := fi.Mode().Perm()
	if perm&0077 != 0 {
		t.Errorf("expected file permissions 0600 without group/other access, got: %o", perm)
	}
}
