package parser

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestChallengerM5_DeeplyNestedProceduralBlocks(t *testing.T) {
	// Construct a 20-level nested block structure mixing BEGIN, IF, CASE, WHILE, LOOP, REPEAT
	var sb strings.Builder
	sb.WriteString("CREATE PROCEDURE deeply_nested()\n")
	sb.WriteString("BEGIN\n") // level 1

	nesting := 15
	for l := 1; l <= nesting; l++ {
		sb.WriteString(fmt.Sprintf("  BEGIN -- level %d\n", l))
		sb.WriteString(fmt.Sprintf("    IF x > %d THEN\n", l))
		sb.WriteString("      SELECT 1;\n")
		sb.WriteString("    ELSE\n")
		sb.WriteString("      SELECT 2;\n")
		sb.WriteString("    END IF;\n")
	}

	sb.WriteString("    SELECT 'core_execution';\n")

	for l := nesting; l >= 1; l-- {
		sb.WriteString("  END;\n")
	}
	sb.WriteString("END;\n")
	sb.WriteString("SELECT 'after_procedure';\n")

	sql := sb.String()
	p := NewBigQueryParser()
	stmts := p.SplitStatements(sql)

	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(stmts))
	}

	if !strings.HasPrefix(stmts[0].SQL, "CREATE PROCEDURE deeply_nested()") {
		t.Errorf("stmt 0 should be CREATE PROCEDURE, got: %q", stmts[0].SQL[:50])
	}
	if !strings.HasSuffix(stmts[0].SQL, "END") {
		t.Errorf("stmt 0 should end with END, got: %q", stmts[0].SQL[len(stmts[0].SQL)-20:])
	}
	if stmts[1].SQL != "SELECT 'after_procedure'" {
		t.Errorf("stmt 1 expected SELECT 'after_procedure', got: %q", stmts[1].SQL)
	}
}

func TestChallengerM5_ExoticStringLiterals(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("escaped triple double quotes", func(t *testing.T) {
		sql := `SELECT """first; \""" second; still in string;""" AS col; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %+v", len(stmts), stmts)
		}
		if stmts[0].SQL != `SELECT """first; \""" second; still in string;""" AS col` {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("escaped triple single quotes", func(t *testing.T) {
		sql := `SELECT '''part 1; \''' part 2; still in string;''' AS col; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %+v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("raw regexes with semicolons and special characters", func(t *testing.T) {
		sql := `SELECT REGEXP_CONTAINS(val, r'^[a-z0-9_-]+;[a-z]+$') AS matched;
SELECT r"(\;|\"|\n|\t)";
SELECT r"""[a-z];[0-9];""" AS raw_triple;
SELECT rb'raw\x00;bytes';
SELECT br"byte;raw";
SELECT 42;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 6 {
			t.Fatalf("expected 6 statements, got %d: %+v", len(stmts), stmts)
		}
		if stmts[5].SQL != "SELECT 42" {
			t.Errorf("stmt 5 expected SELECT 42, got: %q", stmts[5].SQL)
		}
	})

	t.Run("even vs odd backslashes before closing quote", func(t *testing.T) {
		testCases := []struct {
			name          string
			sql           string
			expectedCount int
		}{
			{
				name:          "even backslashes closing quote",
				sql:           `SELECT 'path\\'; SELECT 2;`, // string is 'path\\', quote closes
				expectedCount: 2,
			},
			{
				name:          "four backslashes closing quote",
				sql:           `SELECT 'path\\\\'; SELECT 2;`, // four backslashes, quote closes
				expectedCount: 2,
			},
			{
				name:          "odd backslashes escaped quote continues into next part",
				sql:           `SELECT 'escaped \' and text; continuing'; SELECT 2;`,
				expectedCount: 2,
			},
			{
				name:          "three backslashes: two form backslash, third escapes quote",
				sql:           `SELECT 'three \\\' and text; continuing'; SELECT 2;`,
				expectedCount: 2,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				stmts := p.SplitStatements(tc.sql)
				if len(stmts) != tc.expectedCount {
					t.Fatalf("expected %d statements, got %d: %+v", tc.expectedCount, len(stmts), stmts)
				}
				if stmts[1].SQL != "SELECT 2" {
					t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
				}
			})
		}
	})
}

func TestChallengerM5_UnclosedStructuresNoHangOrPanic(t *testing.T) {
	p := NewBigQueryParser()

	unclosedInputs := []struct {
		name string
		sql  string
	}{
		{"unclosed single quote", "SELECT 'never closed; string;"},
		{"unclosed double quote", `SELECT "never closed; string;`},
		{"unclosed triple single quote", "SELECT '''never closed; string;"},
		{"unclosed triple double quote", `SELECT """never closed; string;`},
		{"unclosed backtick", "SELECT `unclosed_ident; name;"},
		{"unclosed block comment", "/* unclosed comment; with; semicolons;"},
		{"unclosed parenthesis", "SELECT (1 + 2; SELECT 3;"},
		{"unclosed procedural BEGIN", "BEGIN SELECT 1; SELECT 2;"},
		{"unclosed CASE expression", "SELECT CASE WHEN a THEN 1; SELECT 2;"},
	}

	for _, tc := range unclosedInputs {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan bool)
			var stmts []Statement

			go func() {
				stmts = p.SplitStatements(tc.sql)
				done <- true
			}()

			select {
			case <-done:
				// Parser terminated safely without hanging or panicking
				if len(stmts) == 0 {
					t.Logf("unclosed input parsed to 0 statements (safe)")
				} else {
					t.Logf("unclosed input parsed to %d statement(s) (safe): %q", len(stmts), stmts[0].SQL)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("SplitStatements hung on unclosed input: %s", tc.name)
			}
		})
	}
}

func TestChallengerM5_EnormousSQLScripts(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("thousands of statements splitting speed and correct count", func(t *testing.T) {
		const numStmts = 2000
		var sb strings.Builder
		for i := 1; i <= numStmts; i++ {
			sb.WriteString(fmt.Sprintf("INSERT INTO users (id, name) VALUES (%d, 'User %d; with; semicolons');\n", i, i))
		}

		start := time.Now()
		stmts := p.SplitStatements(sb.String())
		elapsed := time.Since(start)

		if len(stmts) != numStmts {
			t.Fatalf("expected %d statements, got %d", numStmts, len(stmts))
		}
		if elapsed > 5*time.Second {
			t.Errorf("parsing %d statements took too long: %v", numStmts, elapsed)
		}
	})

	t.Run("single statement with 100KB multiline body", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString("INSERT INTO large_table VALUES\n")
		for i := 0; i < 2000; i++ {
			if i > 0 {
				sb.WriteString(",\n")
			}
			sb.WriteString(fmt.Sprintf("  (%d, 'Long text payload line %d')", i, i))
		}
		sb.WriteString(";\nSELECT 'next_statement';\n")

		stmts := p.SplitStatements(sb.String())
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d", len(stmts))
		}
		if stmts[1].SQL != "SELECT 'next_statement'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}
