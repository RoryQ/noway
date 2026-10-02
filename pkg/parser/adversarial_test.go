package parser

import (
	"math/rand"
	"strings"
	"testing"
)

// TestAdversarialNestedProceduralBlocks verifies nested procedural blocks
// such as nested BEGIN ... END, IF inside ELSE, and blocks following END;
func TestAdversarialNestedProceduralBlocks(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("nested BEGIN END followed by IF inside CREATE PROCEDURE", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_proc()
BEGIN
    DECLARE x INT64;
    BEGIN
        SET x = 1;
    END;
    IF x = 1 THEN
        SELECT 1;
    END IF;
END;
SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != `CREATE PROCEDURE test_proc()
BEGIN
    DECLARE x INT64;
    BEGIN
        SET x = 1;
    END;
    IF x = 1 THEN
        SELECT 1;
    END IF;
END` {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("nested BEGIN END followed by CASE inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_nested_case()
BEGIN
    BEGIN
        SELECT 1;
    END;
    CASE
        WHEN true THEN SELECT 2;
    END CASE;
END;
SELECT 3;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 3` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("nested BEGIN END followed by WHILE inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_nested_while()
BEGIN
    BEGIN
        SELECT 1;
    END;
    WHILE false DO
        SELECT 2;
    END WHILE;
END;
SELECT 3;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("nested BEGIN END followed by LOOP inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_nested_loop()
BEGIN
    BEGIN
        SELECT 1;
    END;
    LOOP
        LEAVE;
    END LOOP;
END;
SELECT 3;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("nested BEGIN END followed by REPEAT inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_nested_repeat()
BEGIN
    BEGIN
        SELECT 1;
    END;
    REPEAT
        SELECT 2;
    UNTIL true
    END REPEAT;
END;
SELECT 3;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("nested BEGIN END followed by FOR inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_nested_for()
BEGIN
    BEGIN
        SELECT 1;
    END;
    FOR record IN (SELECT 1 AS val) DO
        SELECT record.val;
    END FOR;
END;
SELECT 3;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("deeply nested BEGIN blocks", func(t *testing.T) {
		sql := `BEGIN
    BEGIN
        BEGIN
            SELECT 1;
        END;
    END;
END;
SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("nested IF inside ELSE block", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_else_if()
BEGIN
    IF x = 1 THEN
        SELECT 1;
    ELSE
        IF x = 2 THEN
            SELECT 2;
        END IF;
    END IF;
END;
SELECT 3;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})
}

// TestAdversarialComplexCaseCombinations verifies complex CASE WHEN ... END CASE combinations.
func TestAdversarialComplexCaseCombinations(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("nested procedural CASE inside procedural CASE", func(t *testing.T) {
		sql := `CASE x
    WHEN 1 THEN
        CASE y
            WHEN 2 THEN SELECT 1;
        END CASE;
    ELSE SELECT 3;
END CASE;
SELECT 4;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 4` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("CASE expression ending with END; followed by procedural CASE", func(t *testing.T) {
		sql := `BEGIN
    SET x = CASE WHEN a THEN 1 ELSE 2 END;
    CASE x
        WHEN 1 THEN SELECT 1;
    END CASE;
END;
SELECT 5;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 5` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("multiple CASE expressions in SELECT", func(t *testing.T) {
		sql := `SELECT
    CASE WHEN a THEN 1 END,
    CASE WHEN b THEN 2 END,
    CASE WHEN c THEN 3 END;
SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("nested CASE expression inside procedural IF", func(t *testing.T) {
		sql := `IF status = 1 THEN
    SET desc = CASE WHEN a THEN CASE WHEN b THEN 'b' ELSE 'c' END ELSE 'd' END;
END IF;
SELECT 'after_if';`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})
}

// TestAdversarialDropColumnAndDDL verifies DROP COLUMN IF EXISTS combined with DDL/DML.
func TestAdversarialDropColumnAndDDL(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("statement ending with table keyword followed by procedural IF", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_table_if()
BEGIN
    SELECT * FROM table;
    IF x > 0 THEN
        SELECT 1;
    END IF;
END;
SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("statement ending with column keyword followed by procedural IF", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_column_if()
BEGIN
    SELECT 1 AS column;
    IF x > 0 THEN
        SELECT 1;
    END IF;
END;
SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("ALTER TABLE DROP COLUMN IF EXISTS with DDL and DML sequence", func(t *testing.T) {
		sql := `ALTER TABLE t DROP COLUMN IF EXISTS c1;
ALTER TABLE t DROP COLUMN IF EXISTS c2;
INSERT INTO t (c3) VALUES (1);
SELECT * FROM t;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 4 {
			t.Fatalf("expected 4 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("DROP COLUMN IF EXISTS followed by procedural block", func(t *testing.T) {
		sql := `ALTER TABLE t DROP COLUMN IF EXISTS c1;
BEGIN
    SELECT 1;
END;
SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 3 {
			t.Fatalf("expected 3 statements, got %d: %#v", len(stmts), stmts)
		}
	})
}

// TestAdversarialMultilineStrings verifies multiline strings """ containing semicolons and quotes.
func TestAdversarialMultilineStrings(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("triple quotes with embedded procedural block and semicolons", func(t *testing.T) {
		sql := `INSERT INTO logs VALUES ("""
BEGIN
    SELECT 1;
END;
""");
SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("triple quotes adjacent to semicolon", func(t *testing.T) {
		sql := `SELECT """foo;bar""";SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("raw multiline with semicolons and quotes", func(t *testing.T) {
		sql := `SELECT r"""line1; "quotes"; line2;""";
SELECT rb"""raw bytes; 'single'; line;""";
SELECT 3;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 3 {
			t.Fatalf("expected 3 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("triple single quotes with mixed content", func(t *testing.T) {
		sql := `SELECT '''line 1;
line 2; "double quotes"; ''';
SELECT 4;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})
}

// TestAdversarialEscapesAndIdentifiers verifies escaping and identifiers.
func TestAdversarialEscapesAndIdentifiers(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("identifiers starting with r, b, rb, br", func(t *testing.T) {
		sql := `SELECT r, b, rb, br, real_val, byte_val FROM tbl;
SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("backslash and doubled escaping", func(t *testing.T) {
		sql := `SELECT 'a\'b;c', "d\"e;f", 'g''h;i', "j""k;l";
SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("escaped backslash before quote", func(t *testing.T) {
		sql := `SELECT 'path\\', 'next;';
SELECT 2;`

		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})
}

// TestAdversarialFuzzer tests randomized combinations of statements
// to verify statement splitting behavior across random permutations.
func TestAdversarialFuzzer(t *testing.T) {
	p := NewBigQueryParser()

	// Pool of valid standalone statements (each statement ends with ;)
	atomicStatements := []string{
		`CREATE TABLE users (id INT64, name STRING);`,
		`INSERT INTO users VALUES (1, 'hello; world');`,
		`INSERT INTO users VALUES (2, "quoted; \"semicolon\"");`,
		`SELECT """multiline;
with;
semicolons;""";`,
		`SELECT r"""raw; multiline;""";`,
		`SELECT rb"""raw; bytes;""";`,
		`ALTER TABLE users DROP COLUMN IF EXISTS temp_token;`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS phone STRING;`,
		`DROP ROW ACCESS POLICY IF EXISTS pol ON users;`,
		`SELECT CASE WHEN x = 1 THEN 1 ELSE 2 END;`,
		`CREATE PROCEDURE simple_proc() BEGIN SELECT 1; END;`,
	}

	rng := rand.New(rand.NewSource(42))
	for round := 0; round < 20; round++ {
		count := rng.Intn(5) + 3 // 3 to 7 statements
		var scriptBuilder strings.Builder
		for j := 0; j < count; j++ {
			idx := rng.Intn(len(atomicStatements))
			scriptBuilder.WriteString(atomicStatements[idx])
			scriptBuilder.WriteString("\n")
		}

		script := scriptBuilder.String()
		stmts := p.SplitStatements(script)
		if len(stmts) != count {
			t.Errorf("round %d: expected %d statements, got %d for script:\n%s", round, count, len(stmts), script)
		}
	}
}
