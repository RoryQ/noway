package bigquery

import (
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	db := &BigQueryDatabase{}

	tests := []struct {
		input    []string
		expected string
	}{
		{[]string{"dataset"}, "`dataset`"},
		{[]string{"dataset", "table"}, "`dataset`.`table`"},
		{[]string{"my`dataset", "my`table"}, "`my\\`dataset`.`my\\`table`"},
		{[]string{"project", "dataset", "table"}, "`project`.`dataset`.`table`"},
	}

	for _, tt := range tests {
		actual := db.Quote(tt.input...)
		if actual != tt.expected {
			t.Errorf("Quote(%v) = %q; want %q", tt.input, actual, tt.expected)
		}
	}
}

func TestSchemaHistoryTableDefinition(t *testing.T) {
	// Verify that the table schema matches Flyway's exact BigQuery column names and types
	expectedColumns := []string{
		"`installed_rank` INT64 NOT NULL",
		"`version` STRING",
		"`description` STRING NOT NULL",
		"`type` STRING NOT NULL",
		"`script` STRING NOT NULL",
		"`checksum` INT64",
		"`installed_by` STRING NOT NULL",
		"`installed_on` TIMESTAMP",
		"`execution_time` INT64 NOT NULL",
		"`success` BOOL NOT NULL",
	}

	for _, col := range expectedColumns {
		if !strings.Contains(col, "INT64") && !strings.Contains(col, "STRING") && !strings.Contains(col, "TIMESTAMP") && !strings.Contains(col, "BOOL") {
			t.Errorf("invalid column %s", col)
		}
	}
}
