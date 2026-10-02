package parser

import (
	"testing"
)

func TestBigQueryParserBasic(t *testing.T) {
	sql := `
-- Comment 1
# Comment 2
CREATE TABLE users (
    id INT64,
    name STRING
);

INSERT INTO users (id, name) VALUES (1, 'John; Doe');
INSERT INTO users (id, name) VALUES (2, "Jane; Smith");
`
	p := NewBigQueryParser()
	stmts := p.SplitStatements(sql)

	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(stmts))
	}
}

func TestBigQueryParserDoubledQuotes(t *testing.T) {
	sql := `
INSERT INTO users (id, name) VALUES (1, 'O''Reilly; and Sons');
INSERT INTO users (id, name) VALUES (2, "Said ""Hello; World""");
`
	p := NewBigQueryParser()
	stmts := p.SplitStatements(sql)

	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(stmts))
	}
}

func TestBigQueryParserCaseExpression(t *testing.T) {
	sql := `
SELECT CASE WHEN id = 1 THEN 'one;' ELSE 'other;' END FROM users;
SELECT 2;
`
	p := NewBigQueryParser()
	stmts := p.SplitStatements(sql)

	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(stmts))
	}
}

func TestBigQueryParserTripleQuotes(t *testing.T) {
	sql := `
CREATE TABLE docs (
    id INT64,
    body STRING
);

INSERT INTO docs VALUES (1, '''This is a multiline
string; with semicolons; and "quotes"''');

INSERT INTO docs VALUES (2, """Another multiline
string; with 'single' and ; semicolons""");
`
	p := NewBigQueryParser()
	stmts := p.SplitStatements(sql)

	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(stmts))
	}
}

func TestBigQueryParserProceduralBlock(t *testing.T) {
	sql := `
CREATE PROCEDURE test_proc()
BEGIN
    DECLARE x INT64 DEFAULT 0;
    SET x = 1;
    IF x = 1 THEN
        SELECT 1;
    END IF;
END;

SELECT 2;
`
	p := NewBigQueryParser()
	stmts := p.SplitStatements(sql)

	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(stmts))
	}
}

func TestBigQueryParserProceduralElseIf(t *testing.T) {
	sql := `
IF x = 1 THEN
    SELECT 1;
ELSEIF x = 2 THEN
    SELECT 2;
ELSE
    SELECT 3;
END IF;

SELECT 4;
`
	p := NewBigQueryParser()
	stmts := p.SplitStatements(sql)

	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(stmts))
	}
}

func TestBigQueryParserCaseInCreateTableIfNotExists(t *testing.T) {
	sql := `
CREATE TABLE IF NOT EXISTS my_table AS
SELECT 
    id,
    CASE 
        WHEN status = 'A' THEN 'Active'
        WHEN status = 'P' THEN 'Pending'
        ELSE 'Unknown'
    END AS status_desc
FROM source_table;

SELECT * FROM my_table;
`
	p := NewBigQueryParser()
	stmts := p.SplitStatements(sql)

	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(stmts))
	}
}

func TestBigQueryParserMultilineComments(t *testing.T) {
	sql := `
/*
  This is a multiline comment
  spanning multiple lines
*/
CREATE TABLE test (id INT64);
/* Another comment at the end */
`
	p := NewBigQueryParser()
	stmts := p.SplitStatements(sql)

	if len(stmts) != 1 {
		t.Fatalf("expected 1 statement, got %d", len(stmts))
	}
}

func TestPlaceholderReplacement(t *testing.T) {
	cfg := PlaceholderConfig{
		Enabled:   true,
		Prefix:    "${",
		Suffix:    "}",
		Separator: ":",
		Values: map[string]string{
			"schema_name": "my_dataset",
			"table_name":  "my_table",
		},
	}

	replacer := NewPlaceholderReplacer(cfg)
	sql := "CREATE TABLE ${schema_name}.${table_name} (id INT64); SELECT '${schema_name}'; DEFAULT=${missing:fallback}; ESCAPED=$${not_replaced};"

	builtins := BuiltinPlaceholders{
		DefaultSchema: "default_ds",
		User:          "service-account@gcp.com",
	}

	res, err := replacer.Replace(sql, builtins)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "CREATE TABLE my_dataset.my_table (id INT64); SELECT 'my_dataset'; DEFAULT=fallback; ESCAPED=${not_replaced};"
	if res != expected {
		t.Errorf("got %q, want %q", res, expected)
	}
}

func TestPlaceholderMissingError(t *testing.T) {
	cfg := PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values:  map[string]string{},
	}
	replacer := NewPlaceholderReplacer(cfg)
	_, err := replacer.Replace("SELECT * FROM ${missing_table}", BuiltinPlaceholders{})
	if err == nil {
		t.Errorf("expected error for missing placeholder, got nil")
	}
}

func TestPlaceholderCaseInsensitive(t *testing.T) {
	cfg := PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"DATASET": "my_dataset",
			"myTable": "users",
		},
	}
	replacer := NewPlaceholderReplacer(cfg)
	sql := "SELECT * FROM ${dataset}.${MYTABLE} WHERE ds = '${DATASET}' AND schema = '${flyway:defaultschema}' AND ts = '${MISSING:fallback}';"
	builtins := BuiltinPlaceholders{
		DefaultSchema: "analytics",
	}

	res, err := replacer.Replace(sql, builtins)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "SELECT * FROM my_dataset.users WHERE ds = 'my_dataset' AND schema = 'analytics' AND ts = 'fallback';"
	if res != expected {
		t.Errorf("got %q, want %q", res, expected)
	}
}

// -------------------------------------------------------------------------
// Milestone 1 Parser Regression Test Suite
// -------------------------------------------------------------------------

// TestBigQueryParserMultilineStrings_Regression verifies triple-quoted strings
// (''' and """) with embedded semicolons, newlines, adjacent semicolons,
// escaped quotes, and raw/byte prefixes.
func TestBigQueryParserMultilineStrings_Regression(t *testing.T) {
	tests := []struct {
		name          string
		sql           string
		expectedStmts []string
		expectedLines []int
	}{
		{
			name: "triple double quotes with multiple internal semicolons and newlines",
			sql:  "SELECT \"\"\"line 1;\nline 2;\nline 3;\"\"\" AS text;\nSELECT 1;",
			expectedStmts: []string{
				"SELECT \"\"\"line 1;\nline 2;\nline 3;\"\"\" AS text",
				"SELECT 1",
			},
			expectedLines: []int{1, 4},
		},
		{
			name: "triple single quotes with single and double quotes inside",
			sql:  "INSERT INTO docs VALUES (1, '''First 'single' and \"double\"; quotes; inside''');\nSELECT 2;",
			expectedStmts: []string{
				"INSERT INTO docs VALUES (1, '''First 'single' and \"double\"; quotes; inside''')",
				"SELECT 2",
			},
			expectedLines: []int{1, 2},
		},
		{
			name: "semicolon immediately adjacent to closing triple quotes",
			sql:  "SELECT \"\"\"content;\"\"\";\nSELECT 'next';",
			expectedStmts: []string{
				"SELECT \"\"\"content;\"\"\"",
				"SELECT 'next'",
			},
			expectedLines: []int{1, 2},
		},
		{
			name: "raw and byte prefix triple quotes",
			sql:  "SELECT r\"\"\"raw \\n string;\"\"\";\nSELECT b\"\"\"byte data;\"\"\";\nSELECT 42;",
			expectedStmts: []string{
				"SELECT r\"\"\"raw \\n string;\"\"\"",
				"SELECT b\"\"\"byte data;\"\"\"",
				"SELECT 42",
			},
			expectedLines: []int{1, 2, 3},
		},
		{
			name: "escaped triple quote inside triple quoted string",
			sql:  "SELECT \"\"\"escaped \\\"\"\" quote; still in string;\"\"\";\nSELECT 1;",
			expectedStmts: []string{
				"SELECT \"\"\"escaped \\\"\"\" quote; still in string;\"\"\"",
				"SELECT 1",
			},
			expectedLines: []int{1, 2},
		},
	}

	p := NewBigQueryParser()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmts := p.SplitStatements(tt.sql)
			if len(stmts) != len(tt.expectedStmts) {
				t.Fatalf("expected %d statements, got %d", len(tt.expectedStmts), len(stmts))
			}
			for i, stmt := range stmts {
				if stmt.SQL != tt.expectedStmts[i] {
					t.Errorf("stmt %d SQL mismatch:\ngot:  %q\nwant: %q", i, stmt.SQL, tt.expectedStmts[i])
				}
				if tt.expectedLines != nil && stmt.LineNumber != tt.expectedLines[i] {
					t.Errorf("stmt %d LineNumber mismatch: got %d, want %d", i, stmt.LineNumber, tt.expectedLines[i])
				}
			}
		})
	}
}

// TestBigQueryParserCommentsWithSemicolons_Regression verifies line comments (--, #)
// and block comments (/* ... */) containing semicolons and procedural keywords.
func TestBigQueryParserCommentsWithSemicolons_Regression(t *testing.T) {
	tests := []struct {
		name          string
		sql           string
		expectedStmts []string
	}{
		{
			name: "single line comments containing semicolons",
			sql: `-- Comment with; multiple; semicolons;
CREATE TABLE t1 (id INT64);
# Hash comment; with; semicolons;
SELECT * FROM t1;`,
			expectedStmts: []string{
				"-- Comment with; multiple; semicolons;\nCREATE TABLE t1 (id INT64)",
				"# Hash comment; with; semicolons;\nSELECT * FROM t1",
			},
		},
		{
			name: "block comments containing semicolons and newlines",
			sql: `/*
  SELECT 1;
  SELECT 2;
  -- nested line comment;
*/
CREATE TABLE t2 (id INT64);
/* trailing; comment; */`,
			expectedStmts: []string{
				"/*\n  SELECT 1;\n  SELECT 2;\n  -- nested line comment;\n*/\nCREATE TABLE t2 (id INT64)",
			},
		},
		{
			name: "inline block comment with semicolons between tokens",
			sql:  `SELECT /* inline; comment; with; semicolons */ 1 AS val; SELECT 2;`,
			expectedStmts: []string{
				"SELECT /* inline; comment; with; semicolons */ 1 AS val",
				"SELECT 2",
			},
		},
		{
			name: "comments containing procedural keywords must not corrupt depth",
			sql: `-- BEGIN;
-- IF condition THEN;
-- END IF;
CREATE TABLE t3 (id INT64);
/* BEGIN; CASE; END; */
SELECT 3;`,
			expectedStmts: []string{
				"-- BEGIN;\n-- IF condition THEN;\n-- END IF;\nCREATE TABLE t3 (id INT64)",
				"/* BEGIN; CASE; END; */\nSELECT 3",
			},
		},
		{
			name: "trailing comment at EOF without newline",
			sql:  `SELECT 1; -- comment at eof;`,
			expectedStmts: []string{
				"SELECT 1",
			},
		},
		{
			name: "script containing only comments with semicolons returns no statements",
			sql: `-- only comments;
# hash comments;
/* block comment; with; semicolons; */`,
			expectedStmts: []string{},
		},
	}

	p := NewBigQueryParser()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmts := p.SplitStatements(tt.sql)
			if len(stmts) != len(tt.expectedStmts) {
				t.Fatalf("expected %d statements, got %d: %+v", len(tt.expectedStmts), len(stmts), stmts)
			}
			for i, stmt := range stmts {
				if stmt.SQL != tt.expectedStmts[i] {
					t.Errorf("stmt %d SQL mismatch:\ngot:  %q\nwant: %q", i, stmt.SQL, tt.expectedStmts[i])
				}
			}
		})
	}
}

// TestBigQueryParserEscapedQuotes_Regression verifies backslash-escaped quotes (\', \"),
// doubled quotes, and escaped backslashes (\\).
func TestBigQueryParserEscapedQuotes_Regression(t *testing.T) {
	tests := []struct {
		name          string
		sql           string
		expectedStmts []string
	}{
		{
			name: "backslash escaped single quotes with semicolons",
			sql:  `INSERT INTO users VALUES ('O\'Reilly; and Sons'); SELECT 1;`,
			expectedStmts: []string{
				`INSERT INTO users VALUES ('O\'Reilly; and Sons')`,
				`SELECT 1`,
			},
		},
		{
			name: "backslash escaped double quotes with semicolons",
			sql:  `INSERT INTO users VALUES ("She said \"Hello; World\""); SELECT 2;`,
			expectedStmts: []string{
				`INSERT INTO users VALUES ("She said \"Hello; World\"")`,
				`SELECT 2`,
			},
		},
		{
			name: "escaped backslash followed by closing quote",
			sql:  `SELECT 'C:\\path\\', 'next;'; SELECT 3;`,
			expectedStmts: []string{
				`SELECT 'C:\\path\\', 'next;'`,
				`SELECT 3`,
			},
		},
		{
			name: "mixed backslash and doubled quotes in single statement",
			sql:  `SELECT 'don\'t', 'won''t; test', "he said \"yes;\"", "she said ""no;"""; SELECT 4;`,
			expectedStmts: []string{
				`SELECT 'don\'t', 'won''t; test', "he said \"yes;\"", "she said ""no;"""`,
				`SELECT 4`,
			},
		},
	}

	p := NewBigQueryParser()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmts := p.SplitStatements(tt.sql)
			if len(stmts) != len(tt.expectedStmts) {
				t.Fatalf("expected %d statements, got %d", len(tt.expectedStmts), len(stmts))
			}
			for i, stmt := range stmts {
				if stmt.SQL != tt.expectedStmts[i] {
					t.Errorf("stmt %d SQL mismatch:\ngot:  %q\nwant: %q", i, stmt.SQL, tt.expectedStmts[i])
				}
			}
		})
	}
}

// TestBigQueryParserProceduralExceptionAndNested_Regression verifies
// BEGIN ... EXCEPTION WHEN ERROR THEN ... END; and nested BEGIN ... END; blocks.
func TestBigQueryParserProceduralExceptionAndNested_Regression(t *testing.T) {
	tests := []struct {
		name          string
		sql           string
		expectedStmts []string
	}{
		{
			name: "top-level BEGIN ... EXCEPTION WHEN ERROR THEN ... END;",
			sql: `BEGIN
    DECLARE x INT64 DEFAULT 0;
    SET x = 1 / 0;
EXCEPTION WHEN ERROR THEN
    SELECT @@error.message;
    SELECT @@error.statement_text;
END;
SELECT 'after_exception_block';`,
			expectedStmts: []string{
				"BEGIN\n    DECLARE x INT64 DEFAULT 0;\n    SET x = 1 / 0;\nEXCEPTION WHEN ERROR THEN\n    SELECT @@error.message;\n    SELECT @@error.statement_text;\nEND",
				"SELECT 'after_exception_block'",
			},
		},
		{
			name: "nested BEGIN ... END; blocks inside CREATE PROCEDURE",
			sql: `CREATE PROCEDURE nested_proc()
BEGIN
    DECLARE a INT64;
    BEGIN
        SET a = 1;
        SELECT a;
    END;
    SELECT a + 1;
END;
SELECT 'done';`,
			expectedStmts: []string{
				"CREATE PROCEDURE nested_proc()\nBEGIN\n    DECLARE a INT64;\n    BEGIN\n        SET a = 1;\n        SELECT a;\n    END;\n    SELECT a + 1;\nEND",
				"SELECT 'done'",
			},
		},
		{
			name: "nested BEGIN ... END; inside EXCEPTION handler",
			sql: `BEGIN
    SELECT 1;
EXCEPTION WHEN ERROR THEN
    BEGIN
        SELECT @@error.message;
    END;
END;
SELECT 2;`,
			expectedStmts: []string{
				"BEGIN\n    SELECT 1;\nEXCEPTION WHEN ERROR THEN\n    BEGIN\n        SELECT @@error.message;\n    END;\nEND",
				"SELECT 2",
			},
		},
		{
			name: "BEGIN TRANSACTION versus procedural BEGIN",
			sql: `BEGIN TRANSACTION;
INSERT INTO accounts (id, balance) VALUES (1, 100);
COMMIT TRANSACTION;
SELECT * FROM accounts;`,
			expectedStmts: []string{
				"BEGIN TRANSACTION",
				"INSERT INTO accounts (id, balance) VALUES (1, 100)",
				"COMMIT TRANSACTION",
				"SELECT * FROM accounts",
			},
		},
	}

	p := NewBigQueryParser()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmts := p.SplitStatements(tt.sql)
			if len(stmts) != len(tt.expectedStmts) {
				t.Fatalf("expected %d statements, got %d", len(tt.expectedStmts), len(stmts))
			}
			for i, stmt := range stmts {
				if stmt.SQL != tt.expectedStmts[i] {
					t.Errorf("stmt %d SQL mismatch:\ngot:  %q\nwant: %q", i, stmt.SQL, tt.expectedStmts[i])
				}
			}
		})
	}
}

// TestBigQueryParserProceduralEndCase_Regression verifies procedural
// CASE ... WHEN ... THEN ... END CASE; statement splitting (F19).
func TestBigQueryParserProceduralEndCase_Regression(t *testing.T) {
	tests := []struct {
		name          string
		sql           string
		expectedStmts []string
	}{
		{
			name: "standalone procedural CASE statement ending with END CASE;",
			sql: `CASE status
    WHEN 1 THEN
        SET status_name = 'Pending';
    WHEN 2 THEN
        SET status_name = 'Active';
    ELSE
        SET status_name = 'Unknown';
END CASE;
SELECT status_name;`,
			expectedStmts: []string{
				"CASE status\n    WHEN 1 THEN\n        SET status_name = 'Pending';\n    WHEN 2 THEN\n        SET status_name = 'Active';\n    ELSE\n        SET status_name = 'Unknown';\nEND CASE",
				"SELECT status_name",
			},
		},
		{
			name: "searched procedural CASE statement",
			sql: `CASE
    WHEN score >= 90 THEN
        SET grade = 'A';
    WHEN score >= 80 THEN
        SET grade = 'B';
    ELSE
        SET grade = 'F';
END CASE;
SELECT grade;`,
			expectedStmts: []string{
				"CASE\n    WHEN score >= 90 THEN\n        SET grade = 'A';\n    WHEN score >= 80 THEN\n        SET grade = 'B';\n    ELSE\n        SET grade = 'F';\nEND CASE",
				"SELECT grade",
			},
		},
		{
			name: "procedural CASE inside BEGIN ... END; block",
			sql: `BEGIN
    CASE val
        WHEN 'x' THEN SELECT 1;
        ELSE SELECT 2;
    END CASE;
END;
SELECT 'finished';`,
			expectedStmts: []string{
				"BEGIN\n    CASE val\n        WHEN 'x' THEN SELECT 1;\n        ELSE SELECT 2;\n    END CASE;\nEND",
				"SELECT 'finished'",
			},
		},
		{
			name: "query CASE expression followed by procedural CASE statement",
			sql: `SELECT CASE WHEN 1 = 1 THEN 'yes' ELSE 'no' END;
CASE flag
    WHEN true THEN SELECT 1;
END CASE;
SELECT 2;`,
			expectedStmts: []string{
				"SELECT CASE WHEN 1 = 1 THEN 'yes' ELSE 'no' END",
				"CASE flag\n    WHEN true THEN SELECT 1;\nEND CASE",
				"SELECT 2",
			},
		},
	}

	p := NewBigQueryParser()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmts := p.SplitStatements(tt.sql)
			if len(stmts) != len(tt.expectedStmts) {
				t.Fatalf("expected %d statements, got %d: %+v", len(tt.expectedStmts), len(stmts), stmts)
			}
			for i, stmt := range stmts {
				if stmt.SQL != tt.expectedStmts[i] {
					t.Errorf("stmt %d SQL mismatch:\ngot:  %q\nwant: %q", i, stmt.SQL, tt.expectedStmts[i])
				}
			}
		})
	}
}

// TestBigQueryParserNonProceduralIfExists_Regression verifies non-procedural
// DDL statements containing IF EXISTS and IF NOT EXISTS (F20).
func TestBigQueryParserNonProceduralIfExists_Regression(t *testing.T) {
	tests := []struct {
		name          string
		sql           string
		expectedStmts []string
	}{
		{
			name: "ALTER TABLE DROP COLUMN IF EXISTS",
			sql: `ALTER TABLE users DROP COLUMN IF EXISTS temp_token;
SELECT 1;`,
			expectedStmts: []string{
				"ALTER TABLE users DROP COLUMN IF EXISTS temp_token",
				"SELECT 1",
			},
		},
		{
			name: "ALTER TABLE ADD COLUMN IF NOT EXISTS",
			sql: `ALTER TABLE users ADD COLUMN IF NOT EXISTS phone_number STRING;
SELECT * FROM users;`,
			expectedStmts: []string{
				"ALTER TABLE users ADD COLUMN IF NOT EXISTS phone_number STRING",
				"SELECT * FROM users",
			},
		},
		{
			name: "DROP ROW ACCESS POLICY IF EXISTS",
			sql: `DROP ROW ACCESS POLICY IF EXISTS filter_eu ON dataset.users;
SELECT 'policy_dropped';`,
			expectedStmts: []string{
				"DROP ROW ACCESS POLICY IF EXISTS filter_eu ON dataset.users",
				"SELECT 'policy_dropped'",
			},
		},
		{
			name: "CREATE ROW ACCESS POLICY IF NOT EXISTS",
			sql: `CREATE ROW ACCESS POLICY IF NOT EXISTS filter_us ON dataset.users GRANT TO ('user:alice@example.com') FILTER USING (region = 'US');
SELECT 2;`,
			expectedStmts: []string{
				"CREATE ROW ACCESS POLICY IF NOT EXISTS filter_us ON dataset.users GRANT TO ('user:alice@example.com') FILTER USING (region = 'US')",
				"SELECT 2",
			},
		},
		{
			name: "DROP TABLE / VIEW / SCHEMA IF EXISTS",
			sql: `DROP TABLE IF EXISTS old_table;
DROP VIEW IF EXISTS old_view;
DROP SCHEMA IF EXISTS old_dataset;
SELECT 3;`,
			expectedStmts: []string{
				"DROP TABLE IF EXISTS old_table",
				"DROP VIEW IF EXISTS old_view",
				"DROP SCHEMA IF EXISTS old_dataset",
				"SELECT 3",
			},
		},
		{
			name: "multiple mixed DDL statements in single script",
			sql: `ALTER TABLE orders DROP COLUMN IF EXISTS legacy_notes;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS modern_notes STRING;
DROP ROW ACCESS POLICY IF EXISTS orders_policy ON orders;
SELECT count(*) FROM orders;`,
			expectedStmts: []string{
				"ALTER TABLE orders DROP COLUMN IF EXISTS legacy_notes",
				"ALTER TABLE orders ADD COLUMN IF NOT EXISTS modern_notes STRING",
				"DROP ROW ACCESS POLICY IF EXISTS orders_policy ON orders",
				"SELECT count(*) FROM orders",
			},
		},
	}

	p := NewBigQueryParser()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmts := p.SplitStatements(tt.sql)
			if len(stmts) != len(tt.expectedStmts) {
				t.Fatalf("expected %d statements, got %d: %+v", len(tt.expectedStmts), len(stmts), stmts)
			}
			for i, stmt := range stmts {
				if stmt.SQL != tt.expectedStmts[i] {
					t.Errorf("stmt %d SQL mismatch:\ngot:  %q\nwant: %q", i, stmt.SQL, tt.expectedStmts[i])
				}
			}
		})
	}
}

// TestBigQueryParserRawByteStrings_Regression verifies composite raw/byte string
// prefixes rb, br, rB, etc.
func TestBigQueryParserRawByteStrings_Regression(t *testing.T) {
	tests := []struct {
		name          string
		sql           string
		expectedStmts []string
	}{
		{
			name: "composite rb and br prefixes with semicolons inside literals",
			sql:  `SELECT rb"""raw; bytes;"""; SELECT br'byte; string'; SELECT 3;`,
			expectedStmts: []string{
				`SELECT rb"""raw; bytes;"""`,
				`SELECT br'byte; string'`,
				`SELECT 3`,
			},
		},
		{
			name: "case-insensitive composite prefixes RB and BR",
			sql:  `SELECT RB"""more; raw; bytes;"""; SELECT Br"another; byte; string"; SELECT 4;`,
			expectedStmts: []string{
				`SELECT RB"""more; raw; bytes;"""`,
				`SELECT Br"another; byte; string"`,
				`SELECT 4`,
			},
		},
	}

	p := NewBigQueryParser()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmts := p.SplitStatements(tt.sql)
			if len(stmts) != len(tt.expectedStmts) {
				t.Fatalf("expected %d statements, got %d: %+v", len(tt.expectedStmts), len(stmts), stmts)
			}
			for i, stmt := range stmts {
				if stmt.SQL != tt.expectedStmts[i] {
					t.Errorf("stmt %d SQL mismatch:\ngot:  %q\nwant: %q", i, stmt.SQL, tt.expectedStmts[i])
				}
			}
		})
	}
}

// TestBigQueryParserConsecutiveCaseExpressions_Regression verifies punctuation
// resetting prevKeyword so consecutive CASE expressions are recognized.
func TestBigQueryParserConsecutiveCaseExpressions_Regression(t *testing.T) {
	sql := `SELECT CASE WHEN x = 1 THEN 1 END, CASE WHEN y = 2 THEN 2 END FROM tbl;
SELECT 2;`
	p := NewBigQueryParser()
	stmts := p.SplitStatements(sql)
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(stmts))
	}
}
