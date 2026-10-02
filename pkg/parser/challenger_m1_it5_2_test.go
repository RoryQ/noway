package parser

import (
	"strings"
	"testing"
)

// TestChallenger5_2_CommentsEdgeCases tests challenging edge cases with comments:
// - Single-line comments (-- and #) with CRLF, trailing EOF without newline
// - Semicolons and keywords inside comments
// - Comments directly abutting semicolons and keywords
// - Block comments /* ... */ with embedded quotes, keywords, newlines
func TestChallenger5_2_CommentsEdgeCases(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("hash comment with embedded semicolon", func(t *testing.T) {
		sql := "SELECT 1; # this is a comment with ; inside\nSELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != "SELECT 1" {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if !strings.HasPrefix(stmts[1].SQL, "# this is a comment") || !strings.HasSuffix(stmts[1].SQL, "SELECT 2") {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("double dash comment with embedded keywords and CRLF", func(t *testing.T) {
		sql := "SELECT 1;\r\n-- BEGIN\r\n--   CASE WHEN foo THEN 1 END CASE;\r\n-- END;\r\nSELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != "SELECT 1" {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if !strings.HasSuffix(stmts[1].SQL, "SELECT 2") {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("block comments containing semicolons and procedural keywords", func(t *testing.T) {
		sql := "SELECT /* ; BEGIN LOOP WHILE REPEAT FOR ; */ 1; SELECT /* ; END CASE; END IF; */ 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.Contains(stmts[0].SQL, "SELECT /* ; BEGIN") {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if !strings.Contains(stmts[1].SQL, "SELECT /* ; END CASE") {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("semicolon abutting comments directly", func(t *testing.T) {
		sql := "SELECT 1/*comment*/;/*comment*/SELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("script with only comments and trailing semicolons", func(t *testing.T) {
		sql := "-- only comment\n; /* another comment */ ;\n# hash comment\n;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 0 {
			t.Fatalf("expected 0 statements for comments-only script, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("comment with quotes inside does not trigger string mode", func(t *testing.T) {
		sql := "SELECT 1; /* 'single' \"double\" `backtick` '''triple''' \"\"\"triple\"\"\" */ SELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != "SELECT 1" {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
	})
}

// TestChallenger5_2_MultilineStringsAndRawStrings tests triple-quoted strings, raw strings,
// byte strings, and escaped quotes.
func TestChallenger5_2_MultilineStringsAndRawStrings(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("triple double quotes with semicolons and keywords", func(t *testing.T) {
		sql := `SELECT """
BEGIN
    SELECT 1;
    CASE WHEN true THEN 2 END CASE;
END;
""" AS query_text;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "SELECT \"\"\"") || !strings.HasSuffix(stmts[0].SQL, "AS query_text") {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("triple single quotes with semicolons and escaped triple quotes", func(t *testing.T) {
		sql := `SELECT '''line 1; line 2; \'\'\' still in string; line 3;''' AS val;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("raw triple quotes r\"\"\" and R'''", func(t *testing.T) {
		sql := `SELECT r"""SELECT 1; BEGIN; END;""" AS r1;
SELECT R'''SELECT 2; FOR SYSTEM_TIME;''' AS r2;
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 3 {
			t.Fatalf("expected 3 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[2].SQL != "SELECT 3" {
			t.Errorf("stmt 2 mismatch: %q", stmts[2].SQL)
		}
	})

	t.Run("byte strings and raw byte strings b\"\"\" and rb\"\"\"", func(t *testing.T) {
		sql := `SELECT b"""bytes; with; semicolons;""" AS b1;
SELECT rb"""raw; bytes; with; semicolons;""" AS rb1;
SELECT br'''bytes; raw; single;''' AS br1;
SELECT 4;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 4 {
			t.Fatalf("expected 4 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[3].SQL != "SELECT 4" {
			t.Errorf("stmt 3 mismatch: %q", stmts[3].SQL)
		}
	})

	t.Run("escaped backslashes preceding quote \\\\'", func(t *testing.T) {
		// In 'test\\'; SELECT 2; the \\ is an escaped backslash, so ' closes the string!
		sql := `SELECT 'path\\'; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != `SELECT 'path\\'` {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("doubled quotes escaping in standard string literals", func(t *testing.T) {
		sql := `SELECT 'It''s a semicolon; in string'; SELECT "He said ""Hello; world"""; SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 3 {
			t.Fatalf("expected 3 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[2].SQL != "SELECT 3" {
			t.Errorf("stmt 2 mismatch: %q", stmts[2].SQL)
		}
	})
}

// TestChallenger5_2_TimeTravelSystemTime tests FOR SYSTEM_TIME AS OF across multiple shapes.
func TestChallenger5_2_TimeTravelSystemTime(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("FOR SYSTEM_TIME with comment between FOR and SYSTEM_TIME", func(t *testing.T) {
		sql := `SELECT * FROM t FOR /* time travel comment */ SYSTEM_TIME AS OF CURRENT_TIMESTAMP(); SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("FOR SYSTEM_TIME with line comment and newline between FOR and SYSTEM_TIME", func(t *testing.T) {
		sql := "SELECT * FROM t FOR -- line comment\nSYSTEM_TIME AS OF CURRENT_TIMESTAMP();\nSELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("multiple joins with FOR SYSTEM_TIME in complex query", func(t *testing.T) {
		sql := `SELECT a.id, b.name, c.status
FROM orders FOR SYSTEM_TIME AS OF t1 AS a
JOIN customers FOR SYSTEM_TIME AS OF t1 AS b ON a.cust_id = b.id
LEFT JOIN inventory FOR SYSTEM_TIME AS OF t1 AS c ON a.prod_id = c.id
WHERE a.created_at > '2023-01-01';
SELECT 'done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'done'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural FOR loop with embedded FOR SYSTEM_TIME query", func(t *testing.T) {
		sql := `FOR r IN (SELECT * FROM my_table FOR SYSTEM_TIME AS OF t0) DO
    INSERT INTO target VALUES (r.x);
END FOR;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

// TestChallenger5_2_ScalarFunctionsDisambiguation tests IF(...) and REPEAT(...) disambiguation.
func TestChallenger5_2_ScalarFunctionsDisambiguation(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("scalar IF with comments between IF and paren", func(t *testing.T) {
		sql := `SELECT IF /* comment */ (x > 0, 1, 0); SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar REPEAT with whitespace and comments", func(t *testing.T) {
		sql := `SELECT REPEAT   /* repeat count */   ('abc', 5); SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural REPEAT loop containing scalar REPEAT function", func(t *testing.T) {
		sql := `REPEAT
    SET str = REPEAT(str, 2);
    SET count = count + 1;
UNTIL count >= 5 END REPEAT;
SELECT 'completed';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'completed'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural IF with scalar IF inside THEN block and scalar IF inside ELSE block", func(t *testing.T) {
		sql := `IF (flag = true) THEN
    SELECT IF(val > 10, 'high', 'low');
ELSEIF (flag IS NULL) THEN
    SELECT IF(default_val, 1, 0);
ELSE
    SELECT IF(fallback, 2, -1);
END IF;
SELECT 'after_if';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'after_if'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF nested 4 levels deep", func(t *testing.T) {
		sql := `SELECT IF(a > 0, IF(b > 0, IF(c > 0, IF(d > 0, 1, 2), 3), 4), 5) AS deep_if; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

// TestChallenger5_2_ProceduralNestingInvariants tests LIFO block stack invariants:
// Deep nesting permutations of BEGIN, CASE, IF, LOOP, WHILE, REPEAT, FOR.
func TestChallenger5_2_ProceduralNestingInvariants(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("deep 7-level procedural nesting across all keyword types", func(t *testing.T) {
		sql := `CREATE PROCEDURE deep_nesting()
BEGIN
    CASE status
        WHEN 'ACTIVE' THEN
            IF (counter < 10) THEN
                WHILE counter < 5 DO
                    LOOP
                        REPEAT
                            SET counter = counter + 1;
                        UNTIL counter > 2 END REPEAT;
                        LEAVE;
                    END LOOP;
                END WHILE;
            END IF;
        ELSE
            SELECT 'other';
    END CASE;
END;
SELECT 'done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "CREATE PROCEDURE") || !strings.HasSuffix(stmts[0].SQL, "END") {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 'done'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("sequential BEGIN blocks inside procedural CASE inside BEGIN", func(t *testing.T) {
		sql := `BEGIN
    CASE mode
        WHEN 1 THEN
            BEGIN
                SELECT 1;
            END;
            BEGIN
                SELECT 2;
            END;
        WHEN 2 THEN
            BEGIN
                SELECT 3;
            END;
    END CASE;
END;
SELECT 4;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 4" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("labeled procedural loops and blocks with matching END labels", func(t *testing.T) {
		sql := `label_outer: BEGIN
    label_loop: LOOP
        LEAVE label_loop;
    END LOOP label_loop;
END label_outer;
SELECT 'after_labels';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'after_labels'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("multiple consecutive empty semicolons and spacing", func(t *testing.T) {
		sql := `;;; SELECT 1; ;;; SELECT 2; ;;;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != "SELECT 1" {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}
