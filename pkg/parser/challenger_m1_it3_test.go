package parser

import (
	"strings"
	"testing"
)

// TestChallengerEdgeCases_CommentsInLookahead tests lookahead token discrimination
// when whitespace and comments (single-line --, #, and multi-line /* ... */) are embedded.
func TestChallengerEdgeCases_CommentsInLookahead(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("scalar IF with multiline comment between IF and paren", func(t *testing.T) {
		sql := `SELECT IF /* comment */ (x > 0, 1, 0);
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "SELECT IF /* comment */ (x > 0, 1, 0)") {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF with single line dash comment between IF and paren", func(t *testing.T) {
		sql := "SELECT IF -- line comment\n (x > 0, 1, 0);\nSELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF with single line hash comment between IF and paren", func(t *testing.T) {
		sql := "SELECT IF # hash comment\n (x > 0, 1, 0);\nSELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF with multiple sequential comments between IF and paren", func(t *testing.T) {
		sql := `SELECT IF /* c1 */ /* c2 */ (x > 0, 1, 0);
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF with comments embedded inside arguments", func(t *testing.T) {
		sql := `SELECT IF /* c0 */ ( /* c1 */ x > 0 /* c2 */ , /* c3 */ 1 /* c4 */ , /* c5 */ 0 /* c6 */ );
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural IF with multiline comment before paren condition and THEN", func(t *testing.T) {
		sql := `IF /* comment */ (x > 0) /* comment2 */ THEN
    SELECT 1;
END IF;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural IF with multiline comment without paren", func(t *testing.T) {
		sql := `IF /* comment */ x > 0 /* comment2 */ THEN
    SELECT 1;
END IF;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar REPEAT with comment between REPEAT and paren", func(t *testing.T) {
		sql := `SELECT REPEAT /* comment */ ('abc', 3);
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar REPEAT with line comment between REPEAT and paren", func(t *testing.T) {
		sql := "SELECT REPEAT -- comment\n ('abc', 3);\nSELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("FOR SYSTEM_TIME with comment between FOR and SYSTEM_TIME", func(t *testing.T) {
		sql := `SELECT * FROM t FOR /* comment */ SYSTEM_TIME AS OF CURRENT_TIMESTAMP();
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("FOR SYSTEM_TIME with line comment between FOR and SYSTEM_TIME", func(t *testing.T) {
		sql := "SELECT * FROM t FOR -- comment\n SYSTEM_TIME AS OF CURRENT_TIMESTAMP();\nSELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF with comments inside procedure SET statement", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_p()
BEGIN
    DECLARE x INT64;
    SET x = IF /* comment */ (x > 0, 10, 20);
    SELECT x;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural FOR loop with comment between FOR and loop var", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_for_comment()
BEGIN
    FOR /* loop var */ rec IN (SELECT 1 AS v) DO
        SELECT rec.v;
    END FOR;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural REPEAT loop with comment between REPEAT and body", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_repeat_comment()
BEGIN
    REPEAT /* comment */
        SELECT 1;
    UNTIL true
    END REPEAT;
END;
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

// TestChallengerEdgeCases_TripleQuotedStrings tests multiline strings (triple quotes ''' and """)
// containing semicolons, quotes, and procedural keywords in both non-procedural and procedural contexts.
func TestChallengerEdgeCases_TripleQuotedStrings(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("non-procedural triple double quotes with semicolons and mixed quotes", func(t *testing.T) {
		sql := `SELECT """multiline; with ; semicolons and 'single' and "double" quotes""";
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("non-procedural triple single quotes with semicolons and mixed quotes", func(t *testing.T) {
		sql := `SELECT '''multiline; with ; semicolons and 'single' and "double" quotes''';
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("non-procedural raw and byte triple quotes with semicolons and quotes", func(t *testing.T) {
		sql := `SELECT r"""raw multiline with ;;; and \" \' quotes""";
SELECT b"""byte multiline with ;;; and \" \' quotes""";
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 3 {
			t.Fatalf("expected 3 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[2].SQL != "SELECT 3" {
			t.Errorf("stmt 2 mismatch: %q", stmts[2].SQL)
		}
	})

	t.Run("non-procedural triple quotes containing embedded procedural keywords and semicolons", func(t *testing.T) {
		sql := `SELECT """BEGIN; SELECT 1; IF true THEN SELECT 2; END IF; END;""";
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("non-procedural triple quotes with escaped quotes inside", func(t *testing.T) {
		sql := `SELECT """hello \""" world; semicolon;""";
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural block containing triple quotes with semicolons and quotes", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_triple_quote_proc()
BEGIN
    DECLARE s STRING;
    SET s = """
        SELECT * FROM t WHERE col = 'a;b;c' AND col2 = "x;y;z";
        -- embedded comment with ;
        IF true THEN SELECT 1; END IF;
    """;
    SELECT s;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural block containing triple single quotes with semicolons", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_triple_single_quote_proc()
BEGIN
    DECLARE q STRING;
    SET q = '''
        INSERT INTO log VALUES ('val;1', "val;2");
        BEGIN SELECT 1; END;
    ''';
    EXECUTE IMMEDIATE q;
END;
SELECT 'after_proc';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'after_proc'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural IF with triple quoted string containing semicolons in condition", func(t *testing.T) {
		sql := `IF (SELECT col FROM t WHERE col = """val;with;semi;""") IS NOT NULL THEN
    SELECT 1;
END IF;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF containing triple quoted string with semicolons and quotes", func(t *testing.T) {
		sql := `SELECT IF(val = """a;b;c""", """true;val""", """false;val""");
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF with comments and triple quoted string with semicolons", func(t *testing.T) {
		sql := `SELECT IF /* comment */ (val = """a;b;c;""", 1, 0);
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE with triple quoted strings containing semicolons in branches", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_case_triple_quotes()
BEGIN
    CASE status
        WHEN 'raw' THEN
            SET query = """SELECT 1; SELECT 2;""";
        ELSE
            SET query = '''SELECT 3; SELECT 4;''';
    END CASE;
END;
SELECT 'done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'done'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural WHILE loop containing triple quoted strings with semicolons", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_while_triple_quotes()
BEGIN
    DECLARE i INT64 DEFAULT 0;
    WHILE i < 3 DO
        SET msg = """iteration; number: """ || CAST(i AS STRING) || """; done;""";
        SET i = i + 1;
    END WHILE;
END;
SELECT 42;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 42" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("stress combination of comments, lookaheads, triple quotes, and procedural blocks", func(t *testing.T) {
		sql := `-- Script 1:
SELECT IF /* comment */ (x > 0, 1, 0);
-- Script 2:
CREATE PROCEDURE p1()
BEGIN
    DECLARE s STRING;
    SET s = """
    BEGIN
        SELECT 1;
    END;
    """;
    IF /* comment in proc */ (x > 0) THEN
        SET x = REPEAT /* comment */ ('abc', 2);
    END IF;
END;
-- Script 3:
SELECT * FROM t FOR /* comment */ SYSTEM_TIME AS OF CURRENT_TIMESTAMP();
-- Script 4:
SELECT 4;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 4 {
			t.Fatalf("expected 4 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasSuffix(stmts[3].SQL, "SELECT 4") {
			t.Errorf("stmt 3 mismatch: %q", stmts[3].SQL)
		}
	})
}

func TestAdversarialProceduralCaseNesting(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("procedural IF inside procedural CASE inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_if_in_case()
BEGIN
    CASE status
        WHEN 1 THEN
            IF x > 0 THEN
                SELECT 1;
            END IF;
        ELSE
            SELECT 2;
    END CASE;
END;
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 3" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("standalone procedural IF inside procedural CASE", func(t *testing.T) {
		sql := `CASE status
    WHEN 1 THEN
        IF x > 0 THEN
            SELECT 1;
        END IF;
    ELSE
        SELECT 2;
END CASE;
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 3" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("LOOP and IF inside procedural CASE inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_loop_and_if_in_case()
BEGIN
    CASE status
        WHEN 1 THEN
            LOOP
                IF x > 0 THEN
                    LEAVE;
                END IF;
            END LOOP;
        ELSE
            SELECT 2;
    END CASE;
END;
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 3" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}



