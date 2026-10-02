package parser

import (
	"fmt"
	"testing"
)

func TestChallengerStress(t *testing.T) {
	p := NewBigQueryParser()

	cases := []struct {
		name     string
		sql      string
		expected int
	}{
		{
			name:     "case when then IF func",
			sql:      "SELECT CASE WHEN a THEN IF(x > 0, 1, 0) ELSE 2 END;\nSELECT 2;",
			expected: 2,
		},
		{
			name:     "standalone select IF func",
			sql:      "SELECT IF(x > 0, 1, 0);\nSELECT 2;",
			expected: 2,
		},
		{
			name:     "where IF func",
			sql:      "SELECT 1 WHERE IF(x > 0, true, false);\nSELECT 2;",
			expected: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts := p.SplitStatements(tc.sql)
			fmt.Printf("DEBUG [%s]: got %d statements\n", tc.name, len(stmts))
			for idx, s := range stmts {
				fmt.Printf("  stmt %d (line %d): %q\n", idx, s.LineNumber, s.SQL)
			}
			if len(stmts) != tc.expected {
				t.Errorf("expected %d stmts, got %d", tc.expected, len(stmts))
			}
		})
	}
}
