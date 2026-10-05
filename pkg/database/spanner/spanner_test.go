package spanner

import (
	"context"
	"strings"
	"testing"

	"github.com/roryq/noway/pkg/config"
)

func TestQuote(t *testing.T) {
	db := &SpannerDatabase{}

	tests := []struct {
		name     string
		input    []string
		expected string
	}{
		{
			name:     "single table",
			input:    []string{"users"},
			expected: "`users`",
		},
		{
			name:     "schema and table",
			input:    []string{"myschema", "users"},
			expected: "`myschema`.`users`",
		},
		{
			name:     "escaping backticks",
			input:    []string{"my`table"},
			expected: "`my\\`table`",
		},
		{
			name:     "skip empty or default schema",
			input:    []string{"", "default", "users"},
			expected: "`users`",
		},
		{
			name:     "all empty",
			input:    []string{"", "default"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := db.Quote(tt.input...)
			if actual != tt.expected {
				t.Errorf("Quote(%v) = %q; want %q", tt.input, actual, tt.expected)
			}
		})
	}
}

func TestIsDDL(t *testing.T) {
	tests := []struct {
		sql      string
		expected bool
	}{
		{"CREATE TABLE users (id INT64 NOT NULL) PRIMARY KEY (id)", true},
		{"CREATE INDEX idx_users ON users (id)", true},
		{"CREATE VIEW v_users AS SELECT 1", true},
		{"ALTER TABLE users ADD COLUMN name STRING(50)", true},
		{"DROP TABLE users", true},
		{"DROP INDEX idx_users", true},
		{"DROP VIEW v_users", true},
		{"GRANT ROLE reader TO `user@example.com`", true},
		{"REVOKE ROLE reader FROM `user@example.com`", true},
		{"RENAME TABLE old_name TO new_name", true},
		{"ANALYZE", true},
		{"-- comment before\nCREATE TABLE t (id INT64) PRIMARY KEY (id)", true},
		{"/* multi\nline\ncomment */\nALTER TABLE t ADD COLUMN c STRING(MAX)", true},
		{"# hash comment\nDROP TABLE t", true},
		{"INSERT INTO users (id) VALUES (1)", false},
		{"UPDATE users SET name = 'test' WHERE id = 1", false},
		{"DELETE FROM users WHERE id = 1", false},
		{"SELECT * FROM users", false},
		{"WITH cte AS (SELECT 1) SELECT * FROM cte", false},
		{"", false},
		{";", false},
		{"-- only comment", false},
	}

	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			actual := isDDL(tt.sql)
			if actual != tt.expected {
				t.Errorf("isDDL(%q) = %v; want %v", tt.sql, actual, tt.expected)
			}
		})
	}
}

func TestStripLeadingComments(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "-- line comment\nSELECT 1",
			expected: "SELECT 1",
		},
		{
			input:    "# hash comment\nSELECT 1",
			expected: "SELECT 1",
		},
		{
			input:    "/* block comment */\nSELECT 1",
			expected: "SELECT 1",
		},
		{
			input:    "-- comment 1\n-- comment 2\nSELECT 1",
			expected: "SELECT 1",
		},
		{
			input:    "/* comment 1 */\n/* comment 2 */\nSELECT 1",
			expected: "SELECT 1",
		},
		{
			input:    "SELECT 1",
			expected: "SELECT 1",
		},
		{
			input:    "-- only comment",
			expected: "",
		},
	}

	for _, tt := range tests {
		actual := stripLeadingComments(tt.input)
		if actual != tt.expected {
			t.Errorf("stripLeadingComments(%q) = %q; want %q", tt.input, actual, tt.expected)
		}
	}
}

func TestSpannerHistoryTableSchema(t *testing.T) {
	db := &SpannerDatabase{}
	tableName := db.Quote("flyway_schema_history")
	expectedCols := []string{
		"`installed_rank` INT64 NOT NULL",
		"`version` STRING(50)",
		"`description` STRING(200) NOT NULL",
		"`type` STRING(20) NOT NULL",
		"`script` STRING(1000) NOT NULL",
		"`checksum` INT64",
		"`installed_by` STRING(100) NOT NULL",
		"`installed_on` TIMESTAMP NOT NULL OPTIONS (allow_commit_timestamp=true)",
		"`execution_time` INT64 NOT NULL",
		"`success` BOOL NOT NULL",
		"PRIMARY KEY (`installed_rank`)",
	}

	for _, col := range expectedCols {
		if !strings.Contains(col, "INT64") && !strings.Contains(col, "STRING") && !strings.Contains(col, "TIMESTAMP") && !strings.Contains(col, "BOOL") && !strings.Contains(col, "PRIMARY KEY") {
			t.Errorf("unexpected column check %s", col)
		}
	}

	if tableName != "`flyway_schema_history`" {
		t.Errorf("unexpected table name %s", tableName)
	}
}

func TestNewValidation(t *testing.T) {
	ctx := context.Background()

	// Missing project ID
	cfg := config.NewDefaultConfiguration()
	cfg.GCPSpannerInstanceID = "my-instance"
	cfg.GCPSpannerDatabaseID = "my-db"
	_, err := New(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "project ID is required") {
		t.Errorf("expected project ID error, got %v", err)
	}

	// Missing instance ID
	cfg = config.NewDefaultConfiguration()
	cfg.GCPProjectID = "my-project"
	cfg.GCPSpannerDatabaseID = "my-db"
	_, err = New(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "instance ID is required") {
		t.Errorf("expected instance ID error, got %v", err)
	}

	// Missing database ID
	cfg = config.NewDefaultConfiguration()
	cfg.GCPProjectID = "my-project"
	cfg.GCPSpannerInstanceID = "my-instance"
	_, err = New(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "database ID is required") {
		t.Errorf("expected database ID error, got %v", err)
	}
}
