package parser

import (
	"strings"
	"testing"
)

// TestChallenger5_BeginInCase verifies combinations of BEGIN ... END; blocks inside procedural CASE statements.
func TestChallenger5_BeginInCase(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("BEGIN in all branches of procedural CASE", func(t *testing.T) {
		sql := `CASE action
    WHEN 'insert' THEN
        BEGIN
            INSERT INTO t VALUES (1);
            INSERT INTO audit VALUES ('inserted');
        END;
    WHEN 'update' THEN
        BEGIN
            UPDATE t SET val = 2 WHERE val = 1;
            INSERT INTO audit VALUES ('updated');
        END;
    ELSE
        BEGIN
            DELETE FROM t WHERE val = 1;
            INSERT INTO audit VALUES ('deleted');
        END;
END CASE;
SELECT 'audit_complete';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "CASE action") || !strings.HasSuffix(stmts[0].SQL, "END CASE") {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 'audit_complete'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("nested BEGIN inside BEGIN inside procedural CASE", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_double_begin_in_case()
BEGIN
    CASE status
        WHEN 'INIT' THEN
            BEGIN
                SELECT 'outer_begin';
                BEGIN
                    SELECT 'inner_begin';
                END;
                SELECT 'after_inner';
            END;
        ELSE
            SELECT 'other';
    END CASE;
END;
SELECT 'after_proc';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "CREATE PROCEDURE") || !strings.HasSuffix(stmts[0].SQL, "END") {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 'after_proc'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("alternating BEGIN and CASE blocks (BEGIN -> CASE -> BEGIN -> CASE -> BEGIN)", func(t *testing.T) {
		sql := `BEGIN
    CASE level1
        WHEN 1 THEN
            BEGIN
                CASE level2
                    WHEN 2 THEN
                        BEGIN
                            SELECT 42;
                        END;
                    ELSE
                        SELECT 0;
                END CASE;
            END;
        ELSE
            SELECT -1;
    END CASE;
END;
SELECT 'finished';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'finished'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

// TestChallenger5_IfInCase verifies combinations of IF ... END IF; inside procedural CASE statements.
func TestChallenger5_IfInCase(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("procedural IF with ELSEIF and ELSE inside CASE", func(t *testing.T) {
		sql := `CASE category
    WHEN 'A' THEN
        IF score > 90 THEN
            SELECT 'A+';
        ELSEIF score > 80 THEN
            SELECT 'A';
        ELSE
            SELECT 'A-';
        END IF;
    WHEN 'B' THEN
        IF score > 70 THEN
            SELECT 'B+';
        ELSE
            SELECT 'B';
        END IF;
    ELSE
        SELECT 'C';
END CASE;
SELECT 'grading_done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'grading_done'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE inside IF inside CASE", func(t *testing.T) {
		sql := `CASE mode
    WHEN 1 THEN
        IF submode > 0 THEN
            CASE submode
                WHEN 10 THEN SELECT 10;
                ELSE SELECT 20;
            END CASE;
        END IF;
    ELSE
        SELECT 0;
END CASE;
SELECT 'next_step';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'next_step'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("BEGIN inside IF inside CASE", func(t *testing.T) {
		sql := `CASE operation
    WHEN 'exec' THEN
        IF authorized THEN
            BEGIN
                DECLARE token STRING;
                SET token = 'abc';
                SELECT token;
            END;
        ELSE
            SELECT 'denied';
        END IF;
    ELSE
        SELECT 'noop';
END CASE;
SELECT 'auth_complete';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'auth_complete'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

// TestChallenger5_NestedCaseInCase verifies deeply nested procedural and scalar CASE statements.
func TestChallenger5_NestedCaseInCase(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("5-level deeply nested procedural CASE", func(t *testing.T) {
		sql := `CASE l1
    WHEN 1 THEN
        CASE l2
            WHEN 2 THEN
                CASE l3
                    WHEN 3 THEN
                        CASE l4
                            WHEN 4 THEN
                                CASE l5
                                    WHEN 5 THEN SELECT 5;
                                    ELSE SELECT -5;
                                END CASE;
                            ELSE SELECT -4;
                        END CASE;
                    ELSE SELECT -3;
                END CASE;
            ELSE SELECT -2;
        END CASE;
    ELSE SELECT -1;
END CASE;
SELECT 'deep_case_done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'deep_case_done'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE in both WHEN and ELSE of outer procedural CASE", func(t *testing.T) {
		sql := `CASE opt
    WHEN 'A' THEN
        CASE sub_a
            WHEN 1 THEN SELECT 'A1';
            ELSE SELECT 'A2';
        END CASE;
    ELSE
        CASE sub_b
            WHEN 1 THEN SELECT 'B1';
            ELSE SELECT 'B2';
        END CASE;
END CASE;
SELECT 'both_branches_done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'both_branches_done'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("mixed scalar CASE expressions within nested procedural CASE branches", func(t *testing.T) {
		sql := `CASE val
    WHEN 1 THEN
        CASE subval
            WHEN 2 THEN
                SET res = CASE WHEN x = 1 THEN 'one' WHEN x = 2 THEN 'two' ELSE 'other' END;
            ELSE
                SET res = CASE y WHEN 'a' THEN 10 ELSE 20 END;
        END CASE;
    ELSE
        SET res = CASE WHEN z THEN 't' ELSE 'f' END;
END CASE;
SELECT res;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT res" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

// TestChallenger5_ExceptionInNestedBlocks verifies EXCEPTION handling inside nested CASE and BEGIN blocks.
func TestChallenger5_ExceptionInNestedBlocks(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("EXCEPTION block containing procedural CASE inside procedural CASE", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_case_in_exception()
BEGIN
    CASE outer_flag
        WHEN true THEN
            BEGIN
                SELECT 1;
            EXCEPTION WHEN ERROR THEN
                CASE @@error.statement_type
                    WHEN 'INSERT' THEN
                        SELECT 'insert failed';
                    WHEN 'UPDATE' THEN
                        SELECT 'update failed';
                    ELSE
                        SELECT 'other error';
                END CASE;
            END;
        ELSE
            SELECT 2;
    END CASE;
END;
SELECT 'after_exception_case';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'after_exception_case'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("EXCEPTION block containing IF which contains procedural CASE", func(t *testing.T) {
		sql := `BEGIN
    CASE req_type
        WHEN 'HTTP' THEN
            BEGIN
                SELECT 100;
            EXCEPTION WHEN ERROR THEN
                IF @@error.formatted_stack_trace IS NOT NULL THEN
                    CASE err_level
                        WHEN 'CRITICAL' THEN SELECT 'alert_oncall';
                        ELSE SELECT 'log_warn';
                    END CASE;
                END IF;
            END;
        ELSE
            SELECT 200;
    END CASE;
END;
SELECT 'after_nested_exception_if_case';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'after_nested_exception_if_case'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("EXCEPTION block containing nested BEGIN EXCEPTION block inside CASE", func(t *testing.T) {
		sql := `CASE run_mode
    WHEN 'SAFE' THEN
        BEGIN
            SELECT 1;
        EXCEPTION WHEN ERROR THEN
            BEGIN
                INSERT INTO error_log VALUES (@@error.message);
            EXCEPTION WHEN ERROR THEN
                SELECT 'error log also failed';
            END;
        END;
    ELSE
        SELECT 2;
END CASE;
SELECT 'chained_exception_done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'chained_exception_done'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("CASE with heterogeneous branches (EXCEPTION, nested CASE, IF, plain statement)", func(t *testing.T) {
		sql := `CASE branch_type
    WHEN 'exception_branch' THEN
        BEGIN
            SELECT 1;
        EXCEPTION WHEN ERROR THEN
            SELECT 2;
        END;
    WHEN 'case_branch' THEN
        CASE inner_type
            WHEN 'X' THEN SELECT 3;
            ELSE SELECT 4;
        END CASE;
    WHEN 'if_branch' THEN
        IF inner_flag THEN
            SELECT 5;
        ELSE
            SELECT 6;
        END IF;
    ELSE
        SELECT 7;
END CASE;
SELECT 'heterogeneous_branches_done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'heterogeneous_branches_done'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

// TestChallenger5_ComplexMultiStatementScript verifies that a 10-statement complex script
// mixing every nested structure splits accurately into exactly 10 statements.
func TestChallenger5_ComplexMultiStatementScript(t *testing.T) {
	p := NewBigQueryParser()

	sql := `-- 1. Simple DDL
CREATE TABLE my_table (id INT64, val STRING);

-- 2. Standalone CASE with BEGIN block
CASE mode
    WHEN 1 THEN
        BEGIN
            SELECT 1;
        END;
    ELSE
        SELECT 2;
END CASE;

-- 3. Procedure with CASE containing IF
CREATE PROCEDURE p_case_if()
BEGIN
    CASE status
        WHEN 'OK' THEN
            IF count > 0 THEN
                SELECT count;
            END IF;
        ELSE
            SELECT 0;
    END CASE;
END;

-- 4. Simple query
SELECT 4;

-- 5. Deep nested CASE (3 levels) inside BEGIN
BEGIN
    CASE a
        WHEN 1 THEN
            CASE b
                WHEN 2 THEN
                    CASE c
                        WHEN 3 THEN SELECT 3;
                    END CASE;
            END CASE;
    END CASE;
END;

-- 6. DML
INSERT INTO my_table VALUES (1, 'hello');

-- 7. Standalone CASE with EXCEPTION block
CASE run_env
    WHEN 'PROD' THEN
        BEGIN
            SELECT 1;
        EXCEPTION WHEN ERROR THEN
            SELECT 'prod_error';
        END;
    ELSE
        SELECT 'dev';
END CASE;

-- 8. Loop with CASE containing BEGIN
WHILE i < 3 DO
    CASE i
        WHEN 0 THEN
            BEGIN
                SET i = i + 1;
            END;
        ELSE
            SET i = i + 1;
    END CASE;
END WHILE;

-- 9. DDL with IF NOT EXISTS
CREATE TABLE IF NOT EXISTS backup_table LIKE my_table;

-- 10. Final query
SELECT 'all_10_statements_passed';
`

	stmts := p.SplitStatements(sql)
	if len(stmts) != 10 {
		t.Fatalf("expected exactly 10 statements, got %d:\n", len(stmts))
		for idx, s := range stmts {
			t.Logf("  [%d] line %d: %q", idx, s.LineNumber, s.SQL)
		}
	}

	expectedPrefixes := []string{
		"CREATE TABLE my_table",
		"CASE mode",
		"CREATE PROCEDURE p_case_if()",
		"SELECT 4",
		"BEGIN",
		"INSERT INTO my_table",
		"CASE run_env",
		"WHILE i < 3 DO",
		"CREATE TABLE IF NOT EXISTS backup_table",
		"SELECT 'all_10_statements_passed'",
	}

	for i, expected := range expectedPrefixes {
		if !strings.Contains(stmts[i].SQL, expected) {
			t.Errorf("stmt %d expected containing %q, got: %q", i, expected, stmts[i].SQL)
		}
	}
}

// TestChallenger5_ScalarCaseInProceduralBranches verifies scalar CASE expressions in statements inside procedural CASE branches.
func TestChallenger5_ScalarCaseInProceduralBranches(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("scalar CASE followed by another statement in same WHEN branch", func(t *testing.T) {
		sql := `CASE status
    WHEN 'A' THEN
        SELECT CASE WHEN flag THEN 'yes' ELSE 'no' END;
        SELECT 1;
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

	t.Run("scalar CASE with arithmetic operator after END in SET statement", func(t *testing.T) {
		sql := `CASE status
    WHEN 'A' THEN
        SET x = CASE WHEN y THEN 1 ELSE 0 END + 10;
        SELECT x;
    ELSE
        SELECT 0;
END CASE;
SELECT 'after';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'after'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

// TestChallenger5_ScalarCaseInConditions verifies nested procedural CASE with scalar CASE in condition expressions.
func TestChallenger5_ScalarCaseInConditions(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("scalar CASE in conditions of outer and inner procedural CASE", func(t *testing.T) {
		sql := `CASE (CASE x WHEN 1 THEN 'a' ELSE 'b' END)
    WHEN 'a' THEN
        CASE (CASE y WHEN 2 THEN 'c' ELSE 'd' END)
            WHEN 'c' THEN
                SELECT 1;
            ELSE
                SELECT 2;
        END CASE;
    ELSE
        SELECT 3;
END CASE;
SELECT 4;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 4" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE statements back-to-back without newlines", func(t *testing.T) {
		sql := `CASE x WHEN 1 THEN SELECT 1; END CASE; CASE y WHEN 2 THEN SELECT 2; END CASE;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "CASE x") || !strings.HasSuffix(stmts[0].SQL, "END CASE") {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if !strings.HasPrefix(stmts[1].SQL, "CASE y") || !strings.HasSuffix(stmts[1].SQL, "END CASE") {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

