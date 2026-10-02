package parser

import (
	"testing"
)

// TestEmpiricalProceduralSplitting_Passing verifies statement splitting across
// deeply nested procedures, procedural CASE statements, loops, labels, transactions, and exception blocks.
func TestEmpiricalProceduralSplitting_Passing(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("quadruple nested BEGIN END blocks in procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE p_deep()
BEGIN
    BEGIN
        BEGIN
            BEGIN
                SELECT 1;
            END;
        END;
    END;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("sequential nested blocks within procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE p_seq()
BEGIN
    BEGIN
        SELECT 1;
    END;
    BEGIN
        SELECT 2;
    END;
    BEGIN
        SELECT 3;
    END;
END;
SELECT 4;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 4` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("nested BEGIN with EXCEPTION block inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE p_exc()
BEGIN
    DECLARE x INT64;
    BEGIN
        SET x = 1;
    EXCEPTION WHEN ERROR THEN
        SELECT @@error.message;
    END;
    SELECT x;
END;
SELECT 'after_proc';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 'after_proc'` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural CASE with multiple statements per branch", func(t *testing.T) {
		sql := `CREATE PROCEDURE p_case_multistmt()
BEGIN
    CASE status
        WHEN 'A' THEN
            INSERT INTO audit VALUES ('active');
            SELECT 1;
        WHEN 'B' THEN
            INSERT INTO audit VALUES ('pending');
            SELECT 2;
        ELSE
            INSERT INTO audit VALUES ('other');
            SELECT 3;
    END CASE;
END;
SELECT 'done';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 'done'` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("nested procedural CASE statements in WHEN and ELSE", func(t *testing.T) {
		sql := `CREATE PROCEDURE p_nested_case_deep()
BEGIN
    CASE x
        WHEN 1 THEN
            CASE y
                WHEN 2 THEN SELECT 2;
                ELSE SELECT 3;
            END CASE;
        ELSE
            CASE z
                WHEN 4 THEN SELECT 4;
                ELSE SELECT 5;
            END CASE;
    END CASE;
END;
SELECT 'after_nested_case';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 'after_nested_case'` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("sequential procedural CASE statements in procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE p_seq_case()
BEGIN
    CASE WHEN true THEN SELECT 1; END CASE;
    CASE WHEN false THEN SELECT 2; END CASE;
END;
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 3` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("nested loops: LOOP containing FOR containing WHILE containing REPEAT", func(t *testing.T) {
		sql := `CREATE PROCEDURE p_loop_nest()
BEGIN
    LOOP
        FOR rec IN (SELECT 1 AS val) DO
            WHILE rec.val > 0 DO
                REPEAT
                    SET rec.val = rec.val - 1;
                UNTIL rec.val = 0
                END REPEAT;
            END WHILE;
        END FOR;
        LEAVE;
    END LOOP;
END;
SELECT 'loops_complete';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 'loops_complete'` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("labeled loops and blocks with LEAVE and ITERATE", func(t *testing.T) {
		sql := `CREATE PROCEDURE p_labels()
BEGIN
    outer_lbl: LOOP
        inner_lbl: WHILE true DO
            IF cond THEN
                ITERATE inner_lbl;
            ELSE
                LEAVE outer_lbl;
            END IF;
        END WHILE inner_lbl;
    END LOOP outer_lbl;
END;
SELECT 'after_labels';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 'after_labels'` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural statements following intra-block statements ending in reserved words", func(t *testing.T) {
		sql := `CREATE PROCEDURE p_reserved_words()
BEGIN
    SELECT 1 AS ` + "`" + `table` + "`" + `;
    IF a > 0 THEN SELECT 1; END IF;
    SELECT 1 AS ` + "`" + `column` + "`" + `;
    WHILE b > 0 DO SET b = b - 1; END WHILE;
    SELECT 1 AS ` + "`" + `view` + "`" + `;
    LOOP LEAVE; END LOOP;
    SELECT 1 AS ` + "`" + `procedure` + "`" + `;
    REPEAT SELECT 1; UNTIL true END REPEAT;
    SELECT 1 AS ` + "`" + `schema` + "`" + `;
    FOR r IN (SELECT 1) DO SELECT r; END FOR;
    SELECT 1 AS ` + "`" + `index` + "`" + `;
    CASE WHEN true THEN SELECT 1; END CASE;
END;
SELECT 'finished';`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 'finished'` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("comprehensive script with mixed DDL, procedural definitions, CALL, and DML", func(t *testing.T) {
		sql := `DROP TABLE IF EXISTS accounts;
CREATE TABLE accounts (id INT64, balance NUMERIC);
CREATE OR REPLACE PROCEDURE transfer(IN from_id INT64, IN to_id INT64, IN amt NUMERIC)
BEGIN
    DECLARE cur_bal NUMERIC;
    SET cur_bal = (SELECT balance FROM accounts WHERE id = from_id);
    IF cur_bal >= amt THEN
        UPDATE accounts SET balance = balance - amt WHERE id = from_id;
        UPDATE accounts SET balance = balance + amt WHERE id = to_id;
    ELSE
        SELECT 'insufficient funds';
    END IF;
END;
CALL transfer(1, 2, 100.50);
SELECT * FROM accounts;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 5 {
			t.Fatalf("expected 5 statements, got %d: %#v", len(stmts), stmts)
		}
		expectedPrefixes := []string{
			"DROP TABLE IF EXISTS accounts",
			"CREATE TABLE accounts",
			"CREATE OR REPLACE PROCEDURE transfer",
			"CALL transfer(1, 2, 100.50)",
			"SELECT * FROM accounts",
		}
		for i, exp := range expectedPrefixes {
			if !hasPrefix(stmts[i].SQL, exp) {
				t.Errorf("stmt %d expected prefix %q, got: %q", i, exp, stmts[i].SQL)
			}
		}
	})

	t.Run("keywords in strings and comments inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_keywords_in_strings()
BEGIN
    SELECT 'BEGIN', 'END', 'CASE', 'END CASE', 'LOOP', 'END LOOP';
    -- END;
    # END IF;
    /* END WHILE; */
    IF x > 0 THEN
        SELECT "WHILE DO END WHILE";
    END IF;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("transactions inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_multiple_tx()
BEGIN
    BEGIN TRANSACTION;
    INSERT INTO audit VALUES (1);
    COMMIT TRANSACTION;
    BEGIN TRANSACTION;
    INSERT INTO audit VALUES (2);
    COMMIT TRANSACTION;
END;
SELECT 3;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 3` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("consecutive empty BEGIN END blocks and multiple semicolons", func(t *testing.T) {
		sql := `BEGIN END;
BEGIN END;
CREATE PROCEDURE test_semis()
BEGIN
    SELECT 1;;;
    SELECT 2;
END;
SELECT 4;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 4 {
			t.Fatalf("expected 4 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != `BEGIN END` {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != `BEGIN END` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
		if stmts[3].SQL != `SELECT 4` {
			t.Errorf("stmt 3 mismatch: %q", stmts[3].SQL)
		}
	})

	t.Run("CASE statement with subqueries in WHEN condition", func(t *testing.T) {
		sql := `CREATE PROCEDURE p_subquery_case()
BEGIN
    CASE
        WHEN (SELECT count(*) FROM t) > 0 THEN
            SELECT 1;
        WHEN (SELECT count(*) FROM t) = 0 THEN
            SELECT 2;
        ELSE
            SELECT 3;
    END CASE;
END;
SELECT 4;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 4` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("nested REPEAT with parenthesized complex condition in UNTIL", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_repeat()
BEGIN
    REPEAT
        SELECT 1;
    UNTIL (SELECT count(*) FROM t) > 0 AND (x > 10 OR y < 2)
    END REPEAT;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})
}

// TestEmpiricalProceduralSplitting_FailureModes highlights failure modes where
// scalar IF functions inside procedures or loops corrupt statement splitting.
func TestEmpiricalProceduralSplitting_FailureModes(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("scalar IF function call inside procedure SET statement", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_scalar_if_in_proc()
BEGIN
    DECLARE x INT64;
    SET x = IF(1 > 0, 10, 20);
    SELECT x;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 SQL mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF function call inside procedure WHILE loop", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_while_scalar_if()
BEGIN
    DECLARE x INT64 DEFAULT 5;
    WHILE x > 0 DO
        SET x = x - IF(x > 2, 2, 1);
    END WHILE;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("scalar REPEAT string function call", func(t *testing.T) {
		sql := `SELECT REPEAT('abc', 3);
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("time travel query with FOR SYSTEM_TIME AS OF", func(t *testing.T) {
		sql := `SELECT * FROM t FOR SYSTEM_TIME AS OF CURRENT_TIMESTAMP();
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("ALTER TABLE DROP CONSTRAINT IF EXISTS", func(t *testing.T) {
		sql := `ALTER TABLE t DROP CONSTRAINT IF EXISTS c;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
