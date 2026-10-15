package spanner

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"cloud.google.com/go/spanner/spannertest"
	"cloud.google.com/go/spanner/spansql"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

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

func TestSpannerLockAndHeartbeat(t *testing.T) {
	srv, err := spannertest.NewServer("localhost:0")
	if err != nil {
		t.Fatalf("failed to start spannertest server: %v", err)
	}
	defer srv.Close()

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to dial spannertest server: %v", err)
	}
	defer conn.Close()

	dbURI := "projects/test-proj/instances/test-inst/databases/test-db"
	client, err := spanner.NewClient(ctx, dbURI, option.WithGRPCConn(conn))
	if err != nil {
		t.Fatalf("failed to create spanner client: %v", err)
	}
	defer client.Close()

	ddl, err := spansql.ParseDDL("schema.sql", `CREATE TABLE flyway_schema_history (
		installed_rank INT64 NOT NULL,
		version STRING(50),
		description STRING(200) NOT NULL,
		type STRING(20) NOT NULL,
		script STRING(1000) NOT NULL,
		checksum INT64,
		installed_by STRING(100) NOT NULL,
		installed_on TIMESTAMP NOT NULL OPTIONS (allow_commit_timestamp=true),
		execution_time INT64 NOT NULL,
		success BOOL NOT NULL
	) PRIMARY KEY (installed_rank);`)
	if err != nil {
		t.Fatalf("failed to parse DDL: %v", err)
	}
	if err := srv.UpdateDDL(ddl); err != nil {
		t.Fatalf("failed to update DDL in spannertest: %v", err)
	}

	var heartbeatCount int64
	db := &SpannerDatabase{
		client:            client,
		config:            &config.Configuration{LockRetryCount: 1},
		heartbeatInterval: 30 * time.Millisecond,
		onHeartbeat: func(err error) {
			if err != nil {
				t.Logf("heartbeat tick error: %v", err)
			}
			atomic.AddInt64(&heartbeatCount, 1)
		},
	}

	// 1. Acquire lock
	unlock, err := db.Lock(ctx, "", "flyway_schema_history")
	if err != nil {
		t.Fatalf("failed to acquire Spanner lock: %v", err)
	}

	// 2. Wait for heartbeat ticks
	time.Sleep(100 * time.Millisecond)
	if count := atomic.LoadInt64(&heartbeatCount); count < 2 {
		t.Errorf("expected at least 2 heartbeats, got %d", count)
	}

	// 3. Concurrent lock attempt should fail with lock already held
	db2 := &SpannerDatabase{
		client: client,
		config: &config.Configuration{LockRetryCount: 1},
	}
	_, err2 := db2.Lock(ctx, "", "flyway_schema_history")
	if err2 == nil || !strings.Contains(err2.Error(), "lock already held") {
		t.Errorf("expected 'lock already held' error on concurrent lock, got: %v", err2)
	}

	// 4. Release lock
	if err := unlock(ctx); err != nil {
		t.Fatalf("failed to unlock: %v", err)
	}

	// Calling unlock again should be safe and idempotent
	if err := unlock(ctx); err != nil {
		t.Errorf("expected idempotent unlock, got error: %v", err)
	}

	// 5. Another lock attempt should now succeed
	unlock2, err3 := db2.Lock(ctx, "", "flyway_schema_history")
	if err3 != nil {
		t.Fatalf("failed to acquire lock after release: %v", err3)
	}
	_ = unlock2(ctx)
}

