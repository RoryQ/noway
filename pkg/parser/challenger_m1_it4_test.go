package parser

import (
	"strings"
	"testing"
)

// TestChallengerProceduralCaseNesting_PassingEdgeCases verifies the edge cases requested
// by the orchestrator (CASE inside IF, CASE inside LOOP/WHILE/REPEAT/FOR, CASE inside CASE,
// comments, scalar expressions) which are confirmed passing.
func TestChallengerProceduralCaseNesting_PassingEdgeCases(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("procedural CASE inside standalone procedural IF", func(t *testing.T) {
		sql := `IF x > 0 THEN
    CASE y
        WHEN 1 THEN
            SELECT 1;
        ELSE
            SELECT 2;
    END CASE;
END IF;
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "IF x > 0 THEN") || !strings.HasSuffix(stmts[0].SQL, "END IF") {
			t.Errorf("stmt 0 unexpected: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 3" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE inside procedural IF inside CREATE PROCEDURE", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_case_in_if_in_proc()
BEGIN
    IF x > 0 THEN
        CASE y
            WHEN 1 THEN
                SELECT 1;
            WHEN 2 THEN
                SELECT 2;
            ELSE
                SELECT 3;
        END CASE;
    ELSE
        SELECT 4;
    END IF;
END;
SELECT 5;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "CREATE PROCEDURE") || !strings.HasSuffix(stmts[0].SQL, "END") {
			t.Errorf("stmt 0 unexpected: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 5" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE inside procedural ELSEIF and ELSE branches", func(t *testing.T) {
		sql := `IF x > 0 THEN
    SELECT 1;
ELSEIF x < 0 THEN
    CASE y
        WHEN 1 THEN
            SELECT 2;
        ELSE
            SELECT 3;
    END CASE;
ELSE
    CASE z
        WHEN 10 THEN
            SELECT 4;
    END CASE;
END IF;
SELECT 5;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 5" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE inside procedural LOOP", func(t *testing.T) {
		sql := `LOOP
    CASE status
        WHEN 'DONE' THEN
            LEAVE;
        WHEN 'RETRY' THEN
            ITERATE;
        ELSE
            SELECT 1;
    END CASE;
END LOOP;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "LOOP") || !strings.HasSuffix(stmts[0].SQL, "END LOOP") {
			t.Errorf("stmt 0 unexpected: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE inside procedural WHILE loop", func(t *testing.T) {
		sql := `WHILE i < 10 DO
    CASE i
        WHEN 5 THEN
            SELECT 5;
        ELSE
            SELECT 0;
    END CASE;
    SET i = i + 1;
END WHILE;
SELECT 100;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "WHILE i < 10 DO") || !strings.HasSuffix(stmts[0].SQL, "END WHILE") {
			t.Errorf("stmt 0 unexpected: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 100" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE inside procedural REPEAT loop", func(t *testing.T) {
		sql := `REPEAT
    CASE x
        WHEN 1 THEN
            SELECT 1;
        ELSE
            SELECT 2;
    END CASE;
    SET x = x + 1;
UNTIL x > 5
END REPEAT;
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "REPEAT") || !strings.HasSuffix(stmts[0].SQL, "END REPEAT") {
			t.Errorf("stmt 0 unexpected: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 3" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE inside procedural FOR loop", func(t *testing.T) {
		sql := `FOR rec IN (SELECT val FROM dataset.table) DO
    CASE rec.val
        WHEN 1 THEN
            SELECT 'one';
        ELSE
            SELECT 'other';
    END CASE;
END FOR;
SELECT 'done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "FOR rec IN") || !strings.HasSuffix(stmts[0].SQL, "END FOR") {
			t.Errorf("stmt 0 unexpected: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 'done'" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE nested inside another procedural CASE", func(t *testing.T) {
		sql := `CASE outer_val
    WHEN 1 THEN
        CASE inner_val
            WHEN 10 THEN
                SELECT 10;
            ELSE
                SELECT 20;
        END CASE;
    ELSE
        SELECT 30;
END CASE;
SELECT 40;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "CASE outer_val") || !strings.HasSuffix(stmts[0].SQL, "END CASE") {
			t.Errorf("stmt 0 unexpected: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 40" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("deep nesting: Procedure -> WHILE -> IF -> CASE", func(t *testing.T) {
		sql := `CREATE PROCEDURE nested_deep()
BEGIN
    WHILE count > 0 DO
        IF flag THEN
            CASE status
                WHEN 1 THEN
                    SELECT 1;
                ELSE
                    SELECT 2;
            END CASE;
        END IF;
        SET count = count - 1;
    END WHILE;
END;
SELECT 'finished';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "CREATE PROCEDURE") || !strings.HasSuffix(stmts[0].SQL, "END") {
			t.Errorf("stmt 0 unexpected: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 'finished'" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("comments between END and block keywords", func(t *testing.T) {
		sql := `IF x > 0 THEN
    CASE y
        WHEN 1 THEN
            SELECT 1;
    END /* comment before CASE */ CASE;
END -- comment before IF
IF;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar CASE expression inside procedural CASE condition and branch", func(t *testing.T) {
		sql := `CASE (CASE WHEN a = 1 THEN 'x' ELSE 'y' END)
    WHEN 'x' THEN
        SELECT CASE WHEN b = 2 THEN 20 ELSE 30 END;
    ELSE
        SELECT 40;
END CASE;
SELECT 50;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "CASE (CASE WHEN a = 1") || !strings.HasSuffix(stmts[0].SQL, "END CASE") {
			t.Errorf("stmt 0 unexpected: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 50" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF function call inside procedural CASE inside procedural IF", func(t *testing.T) {
		sql := `IF condition THEN
    CASE status
        WHEN 1 THEN
            SELECT IF(val > 10, 'high', 'low');
    END CASE;
END IF;
SELECT 'next';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'next'" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural IF with paren condition followed by procedural CASE with paren condition", func(t *testing.T) {
		sql := `IF (x > 0) THEN
    CASE (y)
        WHEN 1 THEN
            SELECT 1;
        ELSE
            SELECT 2;
    END CASE;
END IF;
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 3" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("complex multiple statements in single script", func(t *testing.T) {
		sql := `CREATE PROCEDURE p1()
BEGIN
    IF x > 0 THEN
        CASE y
            WHEN 1 THEN SELECT 1;
            ELSE SELECT 2;
        END CASE;
    END IF;
END;
IF x > 0 THEN
    CASE y
        WHEN 1 THEN SELECT 1;
    END CASE;
END IF;
WHILE i < 5 DO
    CASE i
        WHEN 1 THEN SELECT 1;
    END CASE;
    SET i = i + 1;
END WHILE;
LOOP
    CASE z
        WHEN 1 THEN LEAVE;
    END CASE;
END LOOP;
SELECT 42;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 5 {
			t.Fatalf("expected 5 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "CREATE PROCEDURE p1()") {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if !strings.HasPrefix(stmts[1].SQL, "IF x > 0 THEN") {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
		if !strings.HasPrefix(stmts[2].SQL, "WHILE i < 5 DO") {
			t.Errorf("stmt 2 mismatch: %q", stmts[2].SQL)
		}
		if !strings.HasPrefix(stmts[3].SQL, "LOOP") {
			t.Errorf("stmt 3 mismatch: %q", stmts[3].SQL)
		}
		if stmts[4].SQL != "SELECT 42" {
			t.Errorf("stmt 4 mismatch: %q", stmts[4].SQL)
		}
	})

	t.Run("deeply nested procedural CASE (3 levels)", func(t *testing.T) {
		sql := `CASE a
  WHEN 1 THEN
    CASE b
      WHEN 2 THEN
        CASE c
          WHEN 3 THEN
            SELECT 3;
          ELSE
            SELECT 4;
        END CASE;
      ELSE
        SELECT 5;
    END CASE;
  ELSE
    SELECT 6;
END CASE;
SELECT 7;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 7" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

// TestAdversarialProceduralCase_BeginBlockInCase reveals an unhandled failure mode:
// A BEGIN ... END; compound block inside a procedural CASE statement branch causes
// the parser's default END handling to decrement caseDepth instead of blockDepth,
// permanently poisoning blockDepth and preventing statement splitting for subsequent SQL.
func TestAdversarialProceduralCase_BeginBlockInCase(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("standalone BEGIN block inside procedural CASE", func(t *testing.T) {
		sql := `CASE x
    WHEN 1 THEN
        BEGIN
            SELECT 1;
        END;
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

	t.Run("BEGIN block inside procedural CASE inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_begin_in_case()
BEGIN
    CASE x
        WHEN 1 THEN
            BEGIN
                SELECT 1;
            END;
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
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})

	t.Run("BEGIN block with EXCEPTION inside procedural CASE", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_exception_in_case()
BEGIN
    CASE status
        WHEN 'ERROR' THEN
            BEGIN
                SELECT 1;
            EXCEPTION WHEN ERROR THEN
                SELECT 2;
            END;
        ELSE
            SELECT 3;
    END CASE;
END;
SELECT 4;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 4" {
			t.Errorf("stmt 1 unexpected: %q", stmts[1].SQL)
		}
	})
}
