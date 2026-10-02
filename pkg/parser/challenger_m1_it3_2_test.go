package parser

import (
	"strings"
	"testing"
)

// TestChallenger2_NestedProceduralBlocks verifies deeply nested procedural blocks,
// sequential blocks, labels, exception handling, and transaction blocks.
func TestChallenger2_NestedProceduralBlocks(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("nested BEGIN BEGIN END END without procedure wrapper", func(t *testing.T) {
		sql := `BEGIN
    BEGIN
        SELECT 1;
    END;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if !strings.HasPrefix(stmts[0].SQL, "BEGIN") || !strings.HasSuffix(stmts[0].SQL, "END") {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("triple nested BEGIN blocks inside procedure with statements at each level", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_nested()
BEGIN
    DECLARE a INT64;
    BEGIN
        DECLARE b INT64;
        BEGIN
            DECLARE c INT64;
            SET c = 3;
            SELECT c;
        END;
        SET b = 2;
        SELECT b;
    END;
    SET a = 1;
    SELECT a;
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

	t.Run("nested BEGIN with EXCEPTION inside outer BEGIN", func(t *testing.T) {
		sql := `BEGIN
    BEGIN
        SELECT 1 / 0;
    EXCEPTION WHEN ERROR THEN
        SELECT @@error.message;
    END;
    SELECT 'recovered';
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

	t.Run("nested labeled BEGIN END blocks", func(t *testing.T) {
		sql := `outer_block: BEGIN
    inner_block: BEGIN
        SELECT 1;
    END inner_block;
END outer_block;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("sequential nested blocks within transaction", func(t *testing.T) {
		sql := `BEGIN TRANSACTION;
BEGIN
    SELECT 1;
END;
BEGIN
    SELECT 2;
END;
COMMIT TRANSACTION;
SELECT 3;`
		stmts := p.SplitStatements(sql)
		// BEGIN TRANSACTION is 1 statement, BEGIN END is 1, BEGIN END is 1, COMMIT is 1, SELECT 3 is 1
		if len(stmts) != 5 {
			t.Fatalf("expected 5 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != "BEGIN TRANSACTION" {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[3].SQL != "COMMIT TRANSACTION" {
			t.Errorf("stmt 3 mismatch: %q", stmts[3].SQL)
		}
		if stmts[4].SQL != "SELECT 3" {
			t.Errorf("stmt 4 mismatch: %q", stmts[4].SQL)
		}
	})
}

// TestChallenger2_CaseWhenElseScalarIF verifies scalar IF within CASE expressions across complex query shapes.
func TestChallenger2_CaseWhenElseScalarIF(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("required prompt query: SELECT CASE WHEN a THEN 1 ELSE IF(b, 2, 3) END; SELECT 2;", func(t *testing.T) {
		sql := `SELECT CASE WHEN a THEN 1 ELSE IF(b, 2, 3) END; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != "SELECT CASE WHEN a THEN 1 ELSE IF(b, 2, 3) END" {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("multiple WHEN branches with nested IF in WHEN and THEN and ELSE", func(t *testing.T) {
		sql := `SELECT CASE
    WHEN IF(x > 0, true, false) THEN IF(y > 0, 10, 20)
    WHEN IF(z > 0, true, false) THEN 30
    ELSE IF(w > 0, IF(v > 0, 40, 50), 60)
END AS result;
SELECT 'next';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'next'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("CASE with scalar IF in CTE followed by main query", func(t *testing.T) {
		sql := `WITH cte AS (
    SELECT id, CASE WHEN flag THEN 1 ELSE IF(score > 50, 2, 3) END AS category
    FROM raw_data
)
SELECT id, category FROM cte WHERE category > 1;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("CASE with scalar IF inside WHERE and GROUP BY and HAVING", func(t *testing.T) {
		sql := `SELECT count(*)
FROM t
WHERE CASE WHEN a THEN 1 ELSE IF(b, 2, 3) END > 1
GROUP BY CASE WHEN a THEN 'x' ELSE IF(b, 'y', 'z') END
HAVING count(*) > IF(max_limit > 0, max_limit, 10);
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("nested CASE inside CASE with scalar IF in every leaf branch", func(t *testing.T) {
		sql := `SELECT CASE
    WHEN a THEN
        CASE
            WHEN b THEN IF(c, 1, 2)
            ELSE IF(d, 3, 4)
        END
    ELSE
        CASE
            WHEN e THEN IF(f, 5, 6)
            ELSE IF(g, 7, 8)
        END
END AS compound;
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

// TestChallenger2_ProceduralIFWithParentheses verifies procedural IF statements with parenthesized conditions.
func TestChallenger2_ProceduralIFWithParentheses(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("simple procedural IF (cond) THEN ... END IF", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_proc_if()
BEGIN
    IF (x > 0) THEN
        SELECT 1;
    END IF;
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

	t.Run("standalone procedural IF (cond) THEN ... END IF outside procedure", func(t *testing.T) {
		sql := `IF (x > 0) THEN
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

	t.Run("compound parenthesized conditions IF ((a > 0) AND (b < 10)) THEN", func(t *testing.T) {
		sql := `IF ((a > 0) AND (b < 10)) THEN
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

	t.Run("parenthesized condition with IN clause containing commas: IF (x IN (1, 2, 3)) THEN", func(t *testing.T) {
		sql := `IF (x IN (1, 2, 3)) THEN
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

	t.Run("parenthesized condition calling scalar function with commas: IF (COALESCE(x, y) = 1) THEN", func(t *testing.T) {
		sql := `IF (COALESCE(x, y) = 1) THEN
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

	t.Run("procedural IF with scalar IF in parenthesized condition: IF (IF(a, 1, 0) = 1) THEN", func(t *testing.T) {
		sql := `IF (IF(a, 1, 0) = 1) THEN
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

	t.Run("procedural IF with subquery in condition: IF (SELECT count(*) > 0 FROM t) THEN", func(t *testing.T) {
		sql := `IF (SELECT count(*) > 0 FROM t) THEN
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

	t.Run("procedural IF with comments and newline between ) and THEN", func(t *testing.T) {
		sql := "IF (x > 0) /* check x */\n-- line comment\nTHEN\n    SELECT 1;\nEND IF;\nSELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural IF with ELSEIF and ELSE branches", func(t *testing.T) {
		sql := `IF (x = 1) THEN
    SELECT 1;
ELSEIF (x = 2) THEN
    SELECT 2;
ELSE
    SELECT 3;
END IF;
SELECT 4;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 4" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("nested procedural IF inside another procedural IF with parenthesized conditions", func(t *testing.T) {
		sql := `IF (outer_cond > 0) THEN
    IF (inner_cond > 0) THEN
        SELECT 'inner';
    END IF;
    SELECT 'outer';
END IF;
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

// TestChallenger2_SystemTimeAsOf verifies FOR SYSTEM_TIME AS OF query statement splitting.
func TestChallenger2_SystemTimeAsOf(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("required prompt query: SELECT * FROM t FOR SYSTEM_TIME AS OF ...; SELECT 2;", func(t *testing.T) {
		sql := `SELECT * FROM t FOR SYSTEM_TIME AS OF CURRENT_TIMESTAMP(); SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != "SELECT * FROM t FOR SYSTEM_TIME AS OF CURRENT_TIMESTAMP()" {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("case variations: for system_time as of and FOR System_Time AS OF", func(t *testing.T) {
		sql := `SELECT * FROM t for system_time as of '2023-01-01 00:00:00';
SELECT * FROM t FOR System_Time AS OF '2023-01-02 00:00:00';
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 3 {
			t.Fatalf("expected 3 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[2].SQL != "SELECT 3" {
			t.Errorf("stmt 2 mismatch: %q", stmts[2].SQL)
		}
	})

	t.Run("joined tables with multiple FOR SYSTEM_TIME AS OF clauses", func(t *testing.T) {
		sql := `SELECT a.id, b.val
FROM t1 FOR SYSTEM_TIME AS OF TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 HOUR) AS a
JOIN t2 FOR SYSTEM_TIME AS OF TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 HOUR) AS b
ON a.id = b.id;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("FOR SYSTEM_TIME AS OF in subquery and CTE", func(t *testing.T) {
		sql := `WITH history AS (
    SELECT * FROM audit FOR SYSTEM_TIME AS OF t0
)
SELECT * FROM history WHERE id IN (SELECT id FROM users FOR SYSTEM_TIME AS OF t0);
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural FOR loop with SYSTEM_TIME table scan inside body", func(t *testing.T) {
		sql := `CREATE PROCEDURE process_snapshot()
BEGIN
    FOR rec IN (SELECT * FROM my_table FOR SYSTEM_TIME AS OF snap_ts) DO
        INSERT INTO target VALUES (rec.id, rec.name);
    END FOR;
END;
SELECT 'proc_done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 'proc_done'" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

// TestChallenger2_DropConstraintIfExists verifies DDL statement splitting on DROP CONSTRAINT IF EXISTS.
func TestChallenger2_DropConstraintIfExists(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("required prompt query: ALTER TABLE t DROP CONSTRAINT IF EXISTS c; SELECT 2;", func(t *testing.T) {
		sql := `ALTER TABLE t DROP CONSTRAINT IF EXISTS c; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != "ALTER TABLE t DROP CONSTRAINT IF EXISTS c" {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("multiple DDL constraint operations in sequence", func(t *testing.T) {
		sql := `ALTER TABLE t DROP CONSTRAINT IF EXISTS check_positive;
ALTER TABLE t DROP CONSTRAINT IF EXISTS fk_user;
ALTER TABLE t DROP PRIMARY KEY IF EXISTS;
ALTER TABLE t ADD CONSTRAINT check_positive CHECK (val > 0);
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 5 {
			t.Fatalf("expected 5 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[4].SQL != "SELECT 2" {
			t.Errorf("stmt 4 mismatch: %q", stmts[4].SQL)
		}
	})

	t.Run("DROP CONSTRAINT IF EXISTS inside procedure body", func(t *testing.T) {
		sql := `CREATE PROCEDURE migrate_constraints()
BEGIN
    ALTER TABLE t DROP CONSTRAINT IF EXISTS old_c;
    ALTER TABLE t ADD CONSTRAINT new_c CHECK (x > 0);
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

	t.Run("mixed DDL IF EXISTS clauses: TABLE, VIEW, COLUMN, CONSTRAINT, POLICY", func(t *testing.T) {
		sql := `DROP TABLE IF EXISTS t1;
DROP VIEW IF EXISTS v1;
ALTER TABLE t1 DROP COLUMN IF EXISTS col1;
ALTER TABLE t1 DROP CONSTRAINT IF EXISTS c1;
DROP ROW ACCESS POLICY IF EXISTS pol1 ON t1;
SELECT 'ddl_complete';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 6 {
			t.Fatalf("expected 6 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[5].SQL != "SELECT 'ddl_complete'" {
			t.Errorf("stmt 5 mismatch: %q", stmts[5].SQL)
		}
	})
}

// TestChallenger2_ComprehensiveStressScenario combines all edge case elements into a complex migration script.
func TestChallenger2_ComprehensiveStressScenario(t *testing.T) {
	p := NewBigQueryParser()

	sql := `-- 1. DDL setup
ALTER TABLE dataset.accounts DROP CONSTRAINT IF EXISTS balance_positive;
DROP TABLE IF EXISTS dataset.audit_log;
CREATE TABLE dataset.audit_log (id INT64, msg STRING);

-- 2. Procedure with nested blocks, parenthesized IF, scalar IF, FOR SYSTEM_TIME, and REPEAT
CREATE PROCEDURE dataset.process_batch(IN cutoff TIMESTAMP)
BEGIN
    DECLARE processed INT64 DEFAULT 0;

    -- Time-travel query into variable/temp table
    CREATE TEMP TABLE recent_changes AS
    SELECT * FROM dataset.accounts FOR SYSTEM_TIME AS OF cutoff;

    -- Procedural IF with parenthesized condition containing scalar function
    IF (processed = 0 AND (SELECT count(*) FROM recent_changes) > 0) THEN
        BEGIN
            -- Nested block with scalar IF in CASE
            INSERT INTO dataset.audit_log
            SELECT id, CASE WHEN balance > 0 THEN 'pos' ELSE IF(balance = 0, 'zero', 'neg') END
            FROM recent_changes;
        END;
    END IF;

    -- Procedural WHILE with scalar REPEAT call
    WHILE processed < 3 DO
        INSERT INTO dataset.audit_log VALUES (processed, REPEAT('log_', 2));
        SET processed = processed + 1;
    END WHILE;
END;

-- 3. Procedure call
CALL dataset.process_batch(TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 DAY));

-- 4. Final verification queries
SELECT * FROM dataset.audit_log;
SELECT 42;`

	stmts := p.SplitStatements(sql)
	if len(stmts) != 7 {
		t.Fatalf("expected 7 statements, got %d: %#v", len(stmts), stmts)
	}

	expectedPrefixes := []string{
		"ALTER TABLE dataset.accounts DROP CONSTRAINT IF EXISTS balance_positive",
		"DROP TABLE IF EXISTS dataset.audit_log",
		"CREATE TABLE dataset.audit_log",
		"CREATE PROCEDURE dataset.process_batch",
		"CALL dataset.process_batch",
		"SELECT * FROM dataset.audit_log",
		"SELECT 42",
	}

	for i, exp := range expectedPrefixes {
		if !strings.Contains(stmts[i].SQL, exp) {
			t.Errorf("stmt %d expected containing %q, got: %q", i, exp, stmts[i].SQL)
		}
	}
}

// TestChallenger2_DeepAdversarialCases tests extreme syntax edge cases.
func TestChallenger2_DeepAdversarialCases(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("no space between closing paren and THEN: IF(x>0)THEN", func(t *testing.T) {
		sql := "IF(x>0)THEN SELECT 1; END IF; SELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF with no spaces around parens and commas", func(t *testing.T) {
		sql := "SELECT IF(x,1,0);SELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF with excessive whitespace and newlines", func(t *testing.T) {
		sql := "SELECT IF \n\n ( \n\n x > 0 \n , \n 1 \n , \n 0 \n\n ) \n ; \n SELECT 2 ;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("nested procedural IF with ELSEIF and scalar IF in body", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_nested_ifs()
BEGIN
    IF (x > 0) THEN
        IF(y > 0)THEN
            SET z = IF(w > 0, 1, 0);
        ELSEIF (y = 0) THEN
            SET z = 2;
        ELSE
            SET z = 3;
        END IF;
    END IF;
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

	t.Run("scalar REPEAT with nested function calls", func(t *testing.T) {
		sql := "SELECT REPEAT(CONCAT('a', 'b'), 3); SELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("backticked procedural keywords as aliases", func(t *testing.T) {
		sql := "SELECT 1 AS `repeat`, 2 AS `begin`, 3 AS `for`, 4 AS `loop`, 5 AS `while`; SELECT 2;"
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != "SELECT 2" {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})
}

