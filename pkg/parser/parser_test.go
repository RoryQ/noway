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
