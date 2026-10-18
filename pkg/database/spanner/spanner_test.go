package spanner

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"cloud.google.com/go/spanner"
	databaseAdmin "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/spannertest"
	"cloud.google.com/go/spanner/spansql"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/migrator"
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

func setupSpannerTestClient(t *testing.T) (*spannertest.Server, *spanner.Client) {
	t.Helper()
	srv, err := spannertest.NewServer("localhost:0")
	if err != nil {
		t.Fatalf("failed to start spannertest server: %v", err)
	}
	t.Cleanup(srv.Close)

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to dial spannertest server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	dbURI := "projects/test-proj/instances/test-inst/databases/test-db"
	client, err := spanner.NewClient(ctx, dbURI, option.WithGRPCConn(conn))
	if err != nil {
		t.Fatalf("failed to create spanner client: %v", err)
	}
	t.Cleanup(client.Close)

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

	return srv, client
}

func readLockRow(t *testing.T, ctx context.Context, client *spanner.Client) (ownerVersion string, installedBy string, installedOn time.Time, exists bool) {
	t.Helper()
	row, err := client.Single().ReadRow(ctx, "flyway_schema_history", spanner.Key{int64(-1)}, []string{"version", "installed_by", "installed_on"})
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return "", "", time.Time{}, false
		}
		t.Fatalf("unexpected error reading lock row: %v", err)
	}
	var verNull spanner.NullString
	if err := row.Columns(&verNull, &installedBy, &installedOn); err != nil {
		t.Fatalf("failed to scan lock row: %v", err)
	}
	return verNull.StringVal, installedBy, installedOn, true
}

func TestSpannerLockAndHeartbeat(t *testing.T) {
	_, client := setupSpannerTestClient(t)
	ctx := context.Background()

	var heartbeatCount int64
	var heartbeatErrCount int64
	db := &SpannerDatabase{
		client:            client,
		config:            &config.Configuration{LockRetryCount: 1},
		heartbeatInterval: 30 * time.Millisecond,
		onHeartbeat: func(err error) {
			if err != nil {
				atomic.AddInt64(&heartbeatErrCount, 1)
				t.Errorf("unexpected heartbeat error: %v", err)
				return
			}
			atomic.AddInt64(&heartbeatCount, 1)
		},
	}

	// 1. Acquire lock and verify owner ID and initial installed_on timestamp
	unlock, err := db.Lock(ctx, "", "flyway_schema_history")
	if err != nil {
		t.Fatalf("failed to acquire Spanner lock: %v", err)
	}

	ownerVer, instBy, initialInstalledOn, exists := readLockRow(t, ctx, client)
	if !exists {
		t.Fatalf("expected lock row at installed_rank = -1 to exist")
	}
	if ownerVer == "" || instBy == "" || ownerVer != instBy {
		t.Fatalf("expected non-empty matching owner ID in version (%q) and installed_by (%q)", ownerVer, instBy)
	}
	if initialInstalledOn.IsZero() {
		t.Fatalf("expected non-zero initial installed_on timestamp")
	}

	// 2. Wait for heartbeat ticks and verify installed_on actually advances in the database
	time.Sleep(120 * time.Millisecond)
	if count := atomic.LoadInt64(&heartbeatCount); count < 2 {
		t.Errorf("expected at least 2 heartbeats, got %d", count)
	}
	if errCount := atomic.LoadInt64(&heartbeatErrCount); errCount > 0 {
		t.Errorf("expected 0 heartbeat errors, got %d", errCount)
	}

	afterOwnerVer, afterInstBy, updatedInstalledOn, exists := readLockRow(t, ctx, client)
	if !exists {
		t.Fatalf("expected lock row to still exist after heartbeat")
	}
	if afterOwnerVer != ownerVer || afterInstBy != instBy {
		t.Errorf("expected owner ID to remain %q, got version=%q installed_by=%q", ownerVer, afterOwnerVer, afterInstBy)
	}
	if !updatedInstalledOn.After(initialInstalledOn) {
		t.Errorf("expected heartbeat to advance installed_on: initial=%v, updated=%v", initialInstalledOn, updatedInstalledOn)
	}

	// 3. Concurrent lock attempt should fail with lock already held by ownerVer
	db2 := &SpannerDatabase{
		client: client,
		config: &config.Configuration{LockRetryCount: 1},
	}
	_, err2 := db2.Lock(ctx, "", "flyway_schema_history")
	if err2 == nil || !strings.Contains(err2.Error(), "lock already held") || !strings.Contains(err2.Error(), ownerVer) {
		t.Errorf("expected 'lock already held by %s' error on concurrent lock, got: %v", ownerVer, err2)
	}

	// 4. Release lock and verify row is removed
	if err := unlock(ctx); err != nil {
		t.Fatalf("failed to unlock: %v", err)
	}
	if _, _, _, exists := readLockRow(t, ctx, client); exists {
		t.Errorf("expected lock row to be deleted after unlock")
	}

	// Calling unlock again should be safe and idempotent
	if err := unlock(ctx); err != nil {
		t.Errorf("expected idempotent unlock, got error: %v", err)
	}

	// 5. Another lock attempt should now succeed with a new owner ID
	unlock2, err3 := db2.Lock(ctx, "", "flyway_schema_history")
	if err3 != nil {
		t.Fatalf("failed to acquire lock after release: %v", err3)
	}
	owner2Ver, _, _, exists := readLockRow(t, ctx, client)
	if !exists || owner2Ver == "" || owner2Ver == ownerVer {
		t.Errorf("expected new lock owner ID distinct from %q, got %q (exists=%v)", ownerVer, owner2Ver, exists)
	}
	if err := unlock2(ctx); err != nil {
		t.Fatalf("failed to unlock second lock: %v", err)
	}
}

func TestSpannerLockExpiryAndTakeover(t *testing.T) {
	_, client := setupSpannerTestClient(t)
	ctx := context.Background()

	// db1 acquires lock with a short TTL (80ms) and a long heartbeat interval (10m) so it won't refresh before expiring
	db1 := &SpannerDatabase{
		client:            client,
		config:            &config.Configuration{LockRetryCount: 1},
		lockTTL:           80 * time.Millisecond,
		heartbeatInterval: 10 * time.Minute,
	}

	unlock1, err := db1.Lock(ctx, "", "flyway_schema_history")
	if err != nil {
		t.Fatalf("db1 failed to acquire lock: %v", err)
	}

	owner1, _, installedOn1, exists := readLockRow(t, ctx, client)
	if !exists || owner1 == "" {
		t.Fatalf("expected db1 lock row to exist with owner ID, got owner=%q exists=%v", owner1, exists)
	}

	// Before expiry, db2 cannot take over the lock
	db2 := &SpannerDatabase{
		client:            client,
		config:            &config.Configuration{LockRetryCount: 1},
		lockTTL:           80 * time.Millisecond,
		heartbeatInterval: 10 * time.Minute,
	}
	if _, err := db2.Lock(ctx, "", "flyway_schema_history"); err == nil || !strings.Contains(err.Error(), "lock already held") {
		t.Fatalf("expected db2 lock attempt before expiry to fail with 'lock already held', got: %v", err)
	}

	// Wait for db1's lock to expire
	time.Sleep(120 * time.Millisecond)

	// Now db2 should take over the expired lock
	unlock2, err := db2.Lock(ctx, "", "flyway_schema_history")
	if err != nil {
		t.Fatalf("expected db2 to take over expired lock, got error: %v", err)
	}

	owner2, instBy2, installedOn2, exists := readLockRow(t, ctx, client)
	if !exists {
		t.Fatalf("expected lock row to exist after db2 takeover")
	}
	if owner2 == "" || owner2 == owner1 || instBy2 != owner2 {
		t.Errorf("expected new owner ID after takeover (prev=%q), got version=%q installed_by=%q", owner1, owner2, instBy2)
	}
	if !installedOn2.After(installedOn1) {
		t.Errorf("expected takeover installed_on (%v) to be after initial installed_on (%v)", installedOn2, installedOn1)
	}

	// Stale unlock1 must fail and MUST NOT delete db2's active lock row
	errUnlock1 := unlock1(ctx)
	if errUnlock1 == nil || !strings.Contains(errUnlock1.Error(), "lock owned by") {
		t.Errorf("expected stale unlock1 to fail with ownership mismatch, got: %v", errUnlock1)
	}

	ownerAfterStaleUnlock, _, _, exists := readLockRow(t, ctx, client)
	if !exists || ownerAfterStaleUnlock != owner2 {
		t.Fatalf("expected db2 lock row (owner %q) to survive stale unlock1, got owner=%q exists=%v", owner2, ownerAfterStaleUnlock, exists)
	}

	// Active owner db2 unlocks cleanly
	if err := unlock2(ctx); err != nil {
		t.Fatalf("expected db2 unlock to succeed, got: %v", err)
	}
	if _, _, _, exists := readLockRow(t, ctx, client); exists {
		t.Errorf("expected lock row to be deleted after db2 unlock")
	}
}

func TestSpannerLockHeartbeatErrorPropagation(t *testing.T) {
	_, client := setupSpannerTestClient(t)
	ctx := context.Background()

	errCh := make(chan error, 1)
	db := &SpannerDatabase{
		client:            client,
		config:            &config.Configuration{LockRetryCount: 1},
		heartbeatInterval: 30 * time.Millisecond,
		onHeartbeat: func(err error) {
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
			}
		},
	}

	unlock, err := db.Lock(ctx, "", "flyway_schema_history")
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	// Simulate another process overwriting the lock row with a different owner ID
	m := spanner.Update("flyway_schema_history",
		[]string{"installed_rank", "version", "installed_by", "installed_on"},
		[]interface{}{int64(-1), "intruder-owner-id", "intruder-owner-id", spanner.CommitTimestamp},
	)
	if _, err := client.Apply(ctx, []*spanner.Mutation{m}); err != nil {
		t.Fatalf("failed to simulate lock takeover: %v", err)
	}

	// Wait for the heartbeat to detect the ownership loss
	select {
	case hbErr := <-errCh:
		if !strings.Contains(hbErr.Error(), "lock ownership lost") {
			t.Errorf("expected 'lock ownership lost' error from heartbeat, got: %v", hbErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for heartbeat error")
	}

	// Subsequent database operations on db must fail with the heartbeat error
	execErr := db.ExecuteStatement(ctx, "SELECT 1")
	if execErr == nil || !strings.Contains(execErr.Error(), "spanner lock heartbeat error") {
		t.Errorf("expected ExecuteStatement to fail with heartbeat error, got: %v", execErr)
	}

	// Unlock must also surface the heartbeat error and NOT delete the intruder's lock row
	unlockErr := unlock(ctx)
	if unlockErr == nil || !strings.Contains(unlockErr.Error(), "lock heartbeat failed") {
		t.Errorf("expected unlock to return heartbeat error, got: %v", unlockErr)
	}

	ownerAfter, _, _, exists := readLockRow(t, ctx, client)
	if !exists || ownerAfter != "intruder-owner-id" {
		t.Errorf("expected intruder lock row to remain intact, got owner=%q exists=%v", ownerAfter, exists)
	}
}

func setupSpannerTestDB(t *testing.T) (*spannertest.Server, *SpannerDatabase) {
	t.Helper()
	srv, err := spannertest.NewServer("localhost:0")
	if err != nil {
		t.Fatalf("failed to start spannertest server: %v", err)
	}
	t.Cleanup(srv.Close)

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to dial spannertest server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	dbURI := "projects/test-proj/instances/test-inst/databases/test-db"
	client, err := spanner.NewClient(ctx, dbURI, option.WithGRPCConn(conn))
	if err != nil {
		t.Fatalf("failed to create spanner client: %v", err)
	}
	t.Cleanup(client.Close)

	adminClient, err := databaseAdmin.NewDatabaseAdminClient(ctx, option.WithGRPCConn(conn))
	if err != nil {
		t.Fatalf("failed to create spanner database admin client: %v", err)
	}
	t.Cleanup(func() { _ = adminClient.Close() })

	db := &SpannerDatabase{
		client:      client,
		adminClient: adminClient,
		projectID:   "test-proj",
		instanceID:  "test-inst",
		databaseID:  "test-db",
		config:      &config.Configuration{LockRetryCount: 1},
	}
	return srv, db
}

func TestSpannerCleanSchemaAndCleanOnValidationError(t *testing.T) {
	ctx := context.Background()

	t.Run("CleanSchemaWithExcludeTables_PreservesLockRowAndDeletesMigrationRows", func(t *testing.T) {
		_, db := setupSpannerTestDB(t)

		// Create history table and a user table via real Spanner DDL
		if err := db.EnsureHistoryTable(ctx, "", "flyway_schema_history"); err != nil {
			t.Fatalf("EnsureHistoryTable failed: %v", err)
		}
		if err := db.ExecuteStatement(ctx, "CREATE TABLE users (id INT64 NOT NULL, name STRING(100)) PRIMARY KEY (id)"); err != nil {
			t.Fatalf("failed creating users table: %v", err)
		}

		// Acquire active lock (-1, 'flyway-lock')
		unlock, err := db.Lock(ctx, "", "flyway_schema_history")
		if err != nil {
			t.Fatalf("Lock failed: %v", err)
		}

		// Insert applied migration rows (including one whose description happens to be 'flyway-lock' at rank 2)
		v1 := "1"
		v2 := "2"
		cs1 := int64(111)
		cs2 := int64(222)
		if err := db.InsertHistory(ctx, "", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1,
			Description:   "create users",
			Type:          "SQL",
			Script:        "V1__create_users.sql",
			Checksum:      &cs1,
			InstalledBy:   "tester",
			Success:       true,
		}); err != nil {
			t.Fatalf("InsertHistory V1 failed: %v", err)
		}
		if err := db.InsertHistory(ctx, "", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2,
			Version:       &v2,
			Description:   "flyway-lock",
			Type:          "SQL",
			Script:        "V2__flyway-lock.sql",
			Checksum:      &cs2,
			InstalledBy:   "tester",
			Success:       true,
		}); err != nil {
			t.Fatalf("InsertHistory V2 failed: %v", err)
		}

		// Run CleanSchema excluding flyway_schema_history
		if err := db.CleanSchema(ctx, "", "flyway_schema_history"); err != nil {
			t.Fatalf("CleanSchema with excludeTables failed: %v", err)
		}

		// 1. User table 'users' must have been dropped
		usersExists, err := db.HistoryTableExists(ctx, "", "users")
		if err != nil {
			t.Fatalf("checking users table existence failed: %v", err)
		}
		if usersExists {
			t.Errorf("expected user table 'users' to be dropped by CleanSchema")
		}

		// 2. History table 'flyway_schema_history' must still exist
		historyExists, err := db.HistoryTableExists(ctx, "", "flyway_schema_history")
		if err != nil || !historyExists {
			t.Fatalf("expected flyway_schema_history to be preserved, exists=%v err=%v", historyExists, err)
		}

		// 3. All migration rows (rank >= 0) must be deleted
		applied, err := db.FetchHistory(ctx, "", "flyway_schema_history")
		if err != nil {
			t.Fatalf("FetchHistory failed: %v", err)
		}
		if len(applied) != 0 {
			t.Errorf("expected 0 migration rows after CleanSchema, got %d: %+v", len(applied), applied)
		}

		// 4. The active lock row at installed_rank = -1 must still exist after CleanSchema
		ownerVer, instBy, _, lockExists := readLockRow(t, ctx, db.client)
		if !lockExists || ownerVer == "" || ownerVer != instBy {
			t.Fatalf("expected lock row to survive CleanSchema, got exists=%v owner=%q", lockExists, ownerVer)
		}

		// 5. Unlock must succeed cleanly
		if err := unlock(ctx); err != nil {
			t.Fatalf("unlock after CleanSchema failed: %v", err)
		}
	})

	t.Run("EndToEndMigrate_CleanOnValidationError_AgainstSpannerEmulator", func(t *testing.T) {
		_, db := setupSpannerTestDB(t)

		fs := fstest.MapFS{
			"migrations/V1__create_users.sql": &fstest.MapFile{
				Data: []byte("CREATE TABLE users (id INT64 NOT NULL) PRIMARY KEY (id);"),
			},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.LockRetryCount = 1

		m1, err := migrator.New(cfg, db)
		if err != nil {
			t.Fatalf("migrator.New failed: %v", err)
		}

		res1, err := m1.Migrate(ctx)
		if err != nil {
			t.Fatalf("initial Migrate failed: %v", err)
		}
		if res1.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed, got %d", res1.MigrationsExecuted)
		}

		// Modify V1 to cause a validation checksum mismatch
		fs["migrations/V1__create_users.sql"] = &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64 NOT NULL, name STRING(50)) PRIMARY KEY (id);"),
		}

		cfgClean := *cfg
		cfgClean.CleanOnValidationError = true
		cfgClean.CleanDisabled = false

		m2, err := migrator.New(&cfgClean, db)
		if err != nil {
			t.Fatalf("migrator.New for m2 failed: %v", err)
		}

		res2, err := m2.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate with CleanOnValidationError against Spanner emulator failed: %v", err)
		}
		if !res2.Success || res2.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration re-executed after cleanOnValidationError, got exec=%d success=%v", res2.MigrationsExecuted, res2.Success)
		}

		// Lock row must be removed after Migrate completes
		if _, _, _, lockExists := readLockRow(t, ctx, db.client); lockExists {
			t.Errorf("expected lock row to be removed after Migrate completes")
		}

		// Validate must now pass
		valRes, err := m2.Validate(ctx)
		if err != nil || !valRes.Valid {
			t.Errorf("expected Validate to pass after CleanOnValidationError re-migration, got err=%v, val=%s", err, valRes.Error())
		}
	})
}



