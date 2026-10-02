package parser

import (
	"testing"
)

func TestScalarVsProceduralEdgeCases(t *testing.T) {
	p := NewBigQueryParser()

	t.Run("scalar IF after ELSE in CASE expression", func(t *testing.T) {
		sql := `SELECT CASE WHEN a THEN 1 ELSE IF(b, 2, 3) END; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != `SELECT CASE WHEN a THEN 1 ELSE IF(b, 2, 3) END` {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("nested scalar IF inside ELSE branch of CASE", func(t *testing.T) {
		sql := `SELECT CASE WHEN a THEN 1 ELSE IF(b, 2, IF(c, 3, 4)) END; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[0].SQL != `SELECT CASE WHEN a THEN 1 ELSE IF(b, 2, IF(c, 3, 4)) END` {
			t.Errorf("stmt 0 mismatch: %q", stmts[0].SQL)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF after THEN in CASE expression", func(t *testing.T) {
		sql := `SELECT CASE WHEN a THEN IF(b, 1, 2) ELSE 3 END; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("scalar IF in WHEN condition of CASE expression", func(t *testing.T) {
		sql := `SELECT CASE WHEN IF(a, true, false) THEN 1 ELSE 2 END; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("scalar IF parenthesized after ELSE", func(t *testing.T) {
		sql := `SELECT CASE WHEN a THEN 1 ELSE (IF(b, 2, 3)) END; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("scalar IF after ELSE with space before paren", func(t *testing.T) {
		sql := `SELECT CASE WHEN a THEN 1 ELSE IF (b, 2, 3) END; SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
	})

	t.Run("procedural ELSE IF in procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_p()
BEGIN
    IF a THEN
        SELECT 1;
    ELSE IF b THEN
        SELECT 2;
    END IF;
    END IF;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("procedural ELSE newline IF in procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_p()
BEGIN
    IF a THEN
        SELECT 1;
    ELSE
        IF b THEN
            SELECT 2;
        END IF;
    END IF;
END;
SELECT 2;`
		stmts := p.SplitStatements(sql)
		if len(stmts) != 2 {
			t.Fatalf("expected 2 statements, got %d: %#v", len(stmts), stmts)
		}
		if stmts[1].SQL != `SELECT 2` {
			t.Errorf("stmt 1 mismatch: %q", stmts[1].SQL)
		}
	})

	t.Run("scalar IF after ELSE in CASE inside procedure", func(t *testing.T) {
		sql := `CREATE PROCEDURE test_p()
BEGIN
    SELECT CASE WHEN a THEN 1 ELSE IF(b, 2, 3) END;
    SELECT 2;
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
}
