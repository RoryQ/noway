package spanner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/spanner"
	databaseAdmin "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"cloud.google.com/go/spanner/spansql"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/resolver"
	"github.com/roryq/noway/pkg/version"
)

// SpannerDatabase implements database.Database for Google Cloud Spanner.
type SpannerDatabase struct {
	client      *spanner.Client
	adminClient *databaseAdmin.DatabaseAdminClient
	projectID   string
	instanceID  string
	databaseID  string
	config      *config.Configuration
	mu          sync.Mutex

	// heartbeatInterval is the interval between lock heartbeat refreshes.
	// If <= 0, defaults to 3 minutes.
	heartbeatInterval time.Duration

	// lockTTL is the duration after which a lock row is considered expired.
	// If <= 0, defaults to 10 minutes.
	lockTTL time.Duration

	// onHeartbeat is an optional hook called on each heartbeat tick (used for testing).
	onHeartbeat func(err error)

	activeLockMu  sync.Mutex
	activeLockErr error
}

var _ database.Database = (*SpannerDatabase)(nil)

// New creates and initializes a new SpannerDatabase instance.
func New(ctx context.Context, cfg *config.Configuration) (*SpannerDatabase, error) {
	projectID := cfg.GCPProjectID
	instanceID := cfg.GCPSpannerInstanceID
	databaseID := cfg.GCPSpannerDatabaseID

	if projectID == "" {
		return nil, fmt.Errorf("Cloud Spanner project ID is required. Set via config, JDBC URL, or GOOGLE_CLOUD_PROJECT env var")
	}
	if instanceID == "" {
		return nil, fmt.Errorf("Cloud Spanner instance ID is required. Set via config, JDBC URL, or SPANNER_INSTANCE_ID env var")
	}
	if databaseID == "" {
		return nil, fmt.Errorf("Cloud Spanner database ID is required. Set via config, JDBC URL, or SPANNER_DATABASE_ID env var")
	}

	var opts []option.ClientOption

	endpoint := cfg.GCPSpannerEndpoint
	if endpoint == "" {
		endpoint = os.Getenv("SPANNER_EMULATOR_HOST")
	}

	if endpoint != "" {
		// Emulator connection
		_ = os.Setenv("SPANNER_EMULATOR_HOST", endpoint)
		opts = append(opts, option.WithEndpoint(endpoint), option.WithoutAuthentication())
	} else {
		if cfg.GCPCredentialsJSON != "" {
			opts = append(opts, option.WithCredentialsJSON([]byte(cfg.GCPCredentialsJSON)))
		} else if cfg.GCPCredentialsFile != "" {
			opts = append(opts, option.WithCredentialsFile(cfg.GCPCredentialsFile))
		}
	}

	dbPath := fmt.Sprintf("projects/%s/instances/%s/databases/%s", projectID, instanceID, databaseID)

	client, err := spanner.NewClient(ctx, dbPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Spanner client for database %s: %w", dbPath, err)
	}

	adminClient, err := databaseAdmin.NewDatabaseAdminClient(ctx, opts...)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to create Spanner DatabaseAdminClient: %w", err)
	}

	return &SpannerDatabase{
		client:      client,
		adminClient: adminClient,
		projectID:   projectID,
		instanceID:  instanceID,
		databaseID:  databaseID,
		config:      cfg,
	}, nil
}

// Close closes the Spanner client and admin client.
func (db *SpannerDatabase) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	var firstErr error
	if db.client != nil {
		db.client.Close()
	}
	if db.adminClient != nil {
		if err := db.adminClient.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// databasePath returns the fully qualified database path.
func (db *SpannerDatabase) databasePath() string {
	return fmt.Sprintf("projects/%s/instances/%s/databases/%s", db.projectID, db.instanceID, db.databaseID)
}

// instancePath returns the fully qualified instance path.
func (db *SpannerDatabase) instancePath() string {
	return fmt.Sprintf("projects/%s/instances/%s", db.projectID, db.instanceID)
}

// Quote quotes an identifier with backticks.
func (db *SpannerDatabase) Quote(identifiers ...string) string {
	quoted := make([]string, 0, len(identifiers))
	for _, id := range identifiers {
		if id == "" || id == "default" {
			continue
		}
		escaped := strings.ReplaceAll(id, "`", "\\`")
		quoted = append(quoted, "`"+escaped+"`")
	}
	if len(quoted) == 0 {
		return ""
	}
	return strings.Join(quoted, ".")
}

// GetCurrentUser returns the database user session.
func (db *SpannerDatabase) GetCurrentUser(ctx context.Context) (string, error) {
	if db.config != nil && db.config.InstalledBy != "" {
		return db.config.InstalledBy, nil
	}
	if user := os.Getenv("USER"); user != "" {
		return user, nil
	}
	return "spanner", nil
}

// EnsureSchema creates the database if it does not exist (when CreateSchemas is true).
func (db *SpannerDatabase) EnsureSchema(ctx context.Context, schema string) error {
	exists, err := db.SchemaExists(ctx, schema)
	if err == nil && exists {
		return nil
	}

	req := &databasepb.CreateDatabaseRequest{
		Parent:          db.instancePath(),
		CreateStatement: fmt.Sprintf("CREATE DATABASE `%s`", db.databaseID),
	}

	op, err := db.adminClient.CreateDatabase(ctx, req)
	if err != nil {
		if status.Code(err) == codes.AlreadyExists || strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create Spanner database %s: %w", db.databaseID, err)
	}

	_, err = op.Wait(ctx)
	if err != nil {
		if status.Code(err) == codes.AlreadyExists || strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("failed waiting for database creation: %w", err)
	}
	return nil
}

// SchemaExists checks if the Spanner database exists.
func (db *SpannerDatabase) SchemaExists(ctx context.Context, schema string) (bool, error) {
	_, err := db.adminClient.GetDatabase(ctx, &databasepb.GetDatabaseRequest{
		Name: db.databasePath(),
	})
	if err == nil {
		return true, nil
	}
	if status.Code(err) == codes.NotFound || strings.Contains(strings.ToLower(err.Error()), "not found") {
		return false, nil
	}
	return false, err
}

type spannerDDLConstraint struct {
	table      string
	constraint string
}

type spannerDDLObjects struct {
	tables      []string
	views       []string
	indexes     []string
	constraints []spannerDDLConstraint
}

func (db *SpannerDatabase) inspectDDL(ctx context.Context) (*spannerDDLObjects, error) {
	if db.adminClient == nil {
		return nil, fmt.Errorf("adminClient is nil")
	}
	resp, err := db.adminClient.GetDatabaseDdl(ctx, &databasepb.GetDatabaseDdlRequest{
		Database: db.databasePath(),
	})
	if err != nil {
		return nil, err
	}

	objs := &spannerDDLObjects{}
	for _, stmtStr := range resp.Statements {
		stmt, err := spansql.ParseDDLStmt(stmtStr)
		if err != nil {
			continue
		}
		switch s := stmt.(type) {
		case *spansql.CreateTable:
			objs.tables = append(objs.tables, string(s.Name))
			for _, c := range s.Constraints {
				if _, isFK := c.Constraint.(spansql.ForeignKey); isFK && c.Name != "" {
					objs.constraints = append(objs.constraints, spannerDDLConstraint{
						table:      string(s.Name),
						constraint: string(c.Name),
					})
				}
			}
		case *spansql.CreateIndex:
			objs.indexes = append(objs.indexes, string(s.Name))
		case *spansql.CreateView:
			objs.views = append(objs.views, string(s.Name))
		case *spansql.AlterTable:
			if ac, ok := s.Alteration.(spansql.AddConstraint); ok {
				if _, isFK := ac.Constraint.Constraint.(spansql.ForeignKey); isFK && ac.Constraint.Name != "" {
					objs.constraints = append(objs.constraints, spannerDDLConstraint{
						table:      string(s.Name),
						constraint: string(ac.Constraint.Name),
					})
				}
			}
		}
	}
	return objs, nil
}

// SchemaEmpty checks if the database has no user tables (excluding schema history table).
func (db *SpannerDatabase) SchemaEmpty(ctx context.Context, schema string) (bool, error) {
	exists, err := db.SchemaExists(ctx, schema)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}

	historyTable := "flyway_schema_history"
	if db.config != nil && db.config.Table != "" {
		historyTable = db.config.Table
	}

	sql := `SELECT COUNT(1) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '' AND TABLE_TYPE = 'BASE TABLE' AND TABLE_NAME != @table_name`
	stmt := spanner.Statement{
		SQL: sql,
		Params: map[string]interface{}{
			"table_name": historyTable,
		},
	}
	iter := db.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		if objs, ddlErr := db.inspectDDL(ctx); ddlErr == nil {
			for _, tName := range objs.tables {
				if tName != historyTable {
					return false, nil
				}
			}
			return true, nil
		}
		return false, fmt.Errorf("failed to query tables: %w", err)
	}
	var count int64
	if err := row.Column(0, &count); err != nil {
		return false, err
	}
	return count == 0, nil
}

// DropSchema drops all tables and objects in the database.
func (db *SpannerDatabase) DropSchema(ctx context.Context, schema string) error {
	return db.CleanSchema(ctx, schema)
}

// CleanSchema drops all user tables, views, and indexes in the database.
// If excludeTables are provided, those tables are preserved and their migration records cleared.
func (db *SpannerDatabase) CleanSchema(ctx context.Context, schema string, excludeTables ...string) error {
	excludeMap := make(map[string]bool)
	for _, ext := range excludeTables {
		if ext != "" {
			excludeMap[ext] = true
		}
	}

	var ddlStatements []string

	// 1. Foreign key constraints
	fkSQL := `SELECT CONSTRAINT_NAME, TABLE_NAME 
	          FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS 
	          WHERE CONSTRAINT_TYPE = 'FOREIGN KEY' AND TABLE_SCHEMA = ''`
	iter := db.client.Single().Query(ctx, spanner.NewStatement(fkSQL))
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			break
		}
		var cName, tName string
		if err := row.Columns(&cName, &tName); err == nil {
			ddlStatements = append(ddlStatements, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", db.Quote(tName), db.Quote(cName)))
		}
	}
	iter.Stop()

	// 2. Secondary indexes
	idxSQL := `SELECT INDEX_NAME, TABLE_NAME 
	           FROM INFORMATION_SCHEMA.INDEXES 
	           WHERE INDEX_TYPE != 'PRIMARY_KEY' AND TABLE_SCHEMA = '' AND NOT IS_MANAGED`
	iter = db.client.Single().Query(ctx, spanner.NewStatement(idxSQL))
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			break
		}
		var idxName, tName string
		if err := row.Columns(&idxName, &tName); err == nil {
			ddlStatements = append(ddlStatements, fmt.Sprintf("DROP INDEX %s", db.Quote(idxName)))
		}
	}
	iter.Stop()

	// 3. Views
	viewsSQL := `SELECT TABLE_NAME FROM INFORMATION_SCHEMA.VIEWS WHERE TABLE_SCHEMA = ''`
	iter = db.client.Single().Query(ctx, spanner.NewStatement(viewsSQL))
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			break
		}
		var vName string
		if err := row.Column(0, &vName); err == nil {
			ddlStatements = append(ddlStatements, fmt.Sprintf("DROP VIEW %s", db.Quote(vName)))
		}
	}
	iter.Stop()

	preservedTables := make(map[string]bool)
	// 4. Base tables
	tblSQL := `SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_TYPE = 'BASE TABLE' AND TABLE_SCHEMA = ''`
	iter = db.client.Single().Query(ctx, spanner.NewStatement(tblSQL))
	var tblQueryErr error
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			tblQueryErr = err
			break
		}
		var tName string
		if err := row.Column(0, &tName); err == nil {
			if excludeMap[tName] {
				preservedTables[tName] = true
				continue
			}
			ddlStatements = append(ddlStatements, fmt.Sprintf("DROP TABLE %s", db.Quote(tName)))
		}
	}
	iter.Stop()

	// Fallback to GetDatabaseDdl if INFORMATION_SCHEMA is unavailable (e.g., spannertest emulator)
	if tblQueryErr != nil {
		if objs, ddlErr := db.inspectDDL(ctx); ddlErr == nil {
			for _, fk := range objs.constraints {
				ddlStatements = append(ddlStatements, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", db.Quote(fk.table), db.Quote(fk.constraint)))
			}
			for _, idxName := range objs.indexes {
				ddlStatements = append(ddlStatements, fmt.Sprintf("DROP INDEX %s", db.Quote(idxName)))
			}
			for _, vName := range objs.views {
				ddlStatements = append(ddlStatements, fmt.Sprintf("DROP VIEW %s", db.Quote(vName)))
			}
			for _, tName := range objs.tables {
				if excludeMap[tName] {
					preservedTables[tName] = true
					continue
				}
				ddlStatements = append(ddlStatements, fmt.Sprintf("DROP TABLE %s", db.Quote(tName)))
			}
		} else {
			return fmt.Errorf("failed to query Spanner tables for clean: %w", tblQueryErr)
		}
	}

	if len(ddlStatements) > 0 {
		op, err := db.adminClient.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
			Database:   db.databasePath(),
			Statements: ddlStatements,
		})
		if err != nil {
			return fmt.Errorf("failed to submit schema clean DDL: %w", err)
		}
		if err := op.Wait(ctx); err != nil {
			return fmt.Errorf("failed executing schema clean DDL: %w", err)
		}
	}

	for pt := range preservedTables {
		delSQL := fmt.Sprintf("DELETE FROM %s WHERE `installed_rank` >= 0 OR `description` != 'flyway-lock'", db.Quote(pt))
		if err := db.ExecuteStatement(ctx, delSQL); err != nil {
			return fmt.Errorf("failed to clean preserved table %s: %w", pt, err)
		}
	}

	return nil
}

// EnsureHistoryTable creates the flyway_schema_history table if it doesn't exist.
func (db *SpannerDatabase) EnsureHistoryTable(ctx context.Context, schema, table string) error {
	exists, err := db.HistoryTableExists(ctx, schema, table)
	if err == nil && exists {
		return nil
	}

	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	ddl := fmt.Sprintf(`CREATE TABLE %s (
		`+"`installed_rank`"+` INT64 NOT NULL,
		`+"`version`"+` STRING(50),
		`+"`description`"+` STRING(200) NOT NULL,
		`+"`type`"+` STRING(20) NOT NULL,
		`+"`script`"+` STRING(1000) NOT NULL,
		`+"`checksum`"+` INT64,
		`+"`installed_by`"+` STRING(100) NOT NULL,
		`+"`installed_on`"+` TIMESTAMP NOT NULL OPTIONS (allow_commit_timestamp=true),
		`+"`execution_time`"+` INT64 NOT NULL,
		`+"`success`"+` BOOL NOT NULL
	) PRIMARY KEY (`+"`installed_rank`"+`)`, db.Quote(tableName))

	return db.ExecuteStatement(ctx, ddl)
}

// HistoryTableExists checks if the history table exists in Spanner.
func (db *SpannerDatabase) HistoryTableExists(ctx context.Context, schema, table string) (bool, error) {
	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	sql := "SELECT COUNT(1) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_NAME = @tableName AND TABLE_SCHEMA = ''"
	stmt := spanner.Statement{
		SQL: sql,
		Params: map[string]interface{}{
			"tableName": tableName,
		},
	}

	iter := db.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		if objs, ddlErr := db.inspectDDL(ctx); ddlErr == nil {
			for _, tName := range objs.tables {
				if tName == tableName {
					return true, nil
				}
			}
			return false, nil
		}
		return false, err
	}
	var count int64
	if err := row.Column(0, &count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// FetchHistory retrieves all history records ordered by installed_rank ASC.
func (db *SpannerDatabase) FetchHistory(ctx context.Context, schema, table string) ([]resolver.AppliedMigration, error) {
	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	sql := fmt.Sprintf(`SELECT 
		`+"`installed_rank`"+`,
		`+"`version`"+`,
		`+"`description`"+`,
		`+"`type`"+`,
		`+"`script`"+`,
		`+"`checksum`"+`,
		`+"`installed_by`"+`,
		`+"`installed_on`"+`,
		`+"`execution_time`"+`,
		`+"`success`"+`
	FROM %s
	ORDER BY `+"`installed_rank`"+` ASC`, db.Quote(tableName))

	iter := db.client.Single().Query(ctx, spanner.NewStatement(sql))
	defer iter.Stop()

	var records []resolver.AppliedMigration
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read schema history row: %w", err)
		}

		var (
			rank        int64
			verNull     spanner.NullString
			description string
			recType     string
			script      string
			csNull      spanner.NullInt64
			installedBy string
			installedOn time.Time
			execTime    int64
			success     bool
		)

		if err := row.Columns(&rank, &verNull, &description, &recType, &script, &csNull, &installedBy, &installedOn, &execTime, &success); err != nil {
			return nil, fmt.Errorf("failed to scan history row: %w", err)
		}

		// Skip internal lock rows (installed_rank = -1)
		if rank < 0 {
			continue
		}

		var ver *version.Version
		if verNull.Valid && verNull.StringVal != "" {
			v, err := version.Parse(verNull.StringVal)
			if err == nil {
				ver = &v
			}
		}

		var cs *int64
		if csNull.Valid {
			c := csNull.Int64
			cs = &c
		}

		records = append(records, resolver.AppliedMigration{
			InstalledRank: int(rank),
			Version:       ver,
			Description:   description,
			Type:          recType,
			Script:        script,
			Checksum:      cs,
			InstalledBy:   installedBy,
			InstalledOn:   installedOn,
			ExecutionTime: execTime,
			Success:       success,
		})
	}

	return records, nil
}

func (db *SpannerDatabase) setActiveLockErr(err error) {
	db.activeLockMu.Lock()
	defer db.activeLockMu.Unlock()
	db.activeLockErr = err
}

func (db *SpannerDatabase) checkActiveLockErr() error {
	db.activeLockMu.Lock()
	defer db.activeLockMu.Unlock()
	if db.activeLockErr != nil {
		return fmt.Errorf("spanner lock heartbeat error: %w", db.activeLockErr)
	}
	return nil
}

func extractLockOwner(verNull spanner.NullString, instBy string) string {
	if verNull.Valid && verNull.StringVal != "" {
		return verNull.StringVal
	}
	return instBy
}

// InsertHistory inserts a new applied migration record into the history table.
func (db *SpannerDatabase) InsertHistory(ctx context.Context, schema, table string, rec database.HistoryRecord) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	var verVal spanner.NullString
	if rec.Version != nil && *rec.Version != "" {
		verVal = spanner.NullString{StringVal: *rec.Version, Valid: true}
	}

	var csVal spanner.NullInt64
	if rec.Checksum != nil {
		csVal = spanner.NullInt64{Int64: *rec.Checksum, Valid: true}
	}

	m := spanner.InsertOrUpdate(tableName,
		[]string{"installed_rank", "version", "description", "type", "script", "checksum", "installed_by", "installed_on", "execution_time", "success"},
		[]interface{}{
			int64(rec.InstalledRank),
			verVal,
			rec.Description,
			rec.Type,
			rec.Script,
			csVal,
			rec.InstalledBy,
			spanner.CommitTimestamp,
			rec.ExecutionTime,
			rec.Success,
		},
	)

	_, err := db.client.Apply(ctx, []*spanner.Mutation{m})
	if err != nil {
		return fmt.Errorf("failed to insert history record: %w", err)
	}
	return nil
}

// UpdateHistory updates an existing migration record.
func (db *SpannerDatabase) UpdateHistory(ctx context.Context, schema, table string, rec database.HistoryRecord) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	var csVal spanner.NullInt64
	if rec.Checksum != nil {
		csVal = spanner.NullInt64{Int64: *rec.Checksum, Valid: true}
	}

	m := spanner.Update(tableName,
		[]string{"installed_rank", "description", "type", "script", "checksum"},
		[]interface{}{
			int64(rec.InstalledRank),
			rec.Description,
			rec.Type,
			rec.Script,
			csVal,
		},
	)

	_, err := db.client.Apply(ctx, []*spanner.Mutation{m})
	if err != nil {
		return fmt.Errorf("failed to update history record: %w", err)
	}
	return nil
}

// DeleteHistory removes a history record.
func (db *SpannerDatabase) DeleteHistory(ctx context.Context, schema, table string, installedRank int) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	m := spanner.Delete(tableName, spanner.Key{int64(installedRank)}.AsPrefix())
	_, err := db.client.Apply(ctx, []*spanner.Mutation{m})
	if err != nil {
		return fmt.Errorf("failed to delete history record: %w", err)
	}
	return nil
}

// Lock acquires a Flyway-compatible lock row in the history table.
func (db *SpannerDatabase) Lock(ctx context.Context, schema, table string) (database.UnlockFunc, error) {
	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	lockID := uuid.New().String()

	ttl := db.lockTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	retryCount := 50
	if db.config != nil && db.config.LockRetryCount > 0 {
		retryCount = db.config.LockRetryCount
	}

	var lastErr error
	for attempt := 0; attempt < retryCount; attempt++ {
		acquired := false
		_, err := db.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
			// Read lock row
			stmt := spanner.Statement{
				SQL: fmt.Sprintf("SELECT version, installed_by, installed_on FROM %s WHERE installed_rank = -1", db.Quote(tableName)),
			}
			iter := txn.Query(ctx, stmt)
			defer iter.Stop()

			row, err := iter.Next()
			if err != nil && err != iterator.Done {
				return err
			}
			if err != iterator.Done {
				// Lock row exists; check if it is still within TTL
				var verNull spanner.NullString
				var instBy string
				var instOn time.Time
				if scanErr := row.Columns(&verNull, &instBy, &instOn); scanErr != nil {
					return scanErr
				}
				if time.Since(instOn) < ttl {
					return fmt.Errorf("lock already held by %s", extractLockOwner(verNull, instBy))
				}
			}

			// Insert or take over expired lock row with our unique owner ID
			m := spanner.InsertOrUpdate(tableName,
				[]string{"installed_rank", "version", "description", "type", "script", "checksum", "installed_by", "installed_on", "execution_time", "success"},
				[]interface{}{
					int64(-1),
					spanner.NullString{StringVal: lockID, Valid: true},
					"flyway-lock",
					"SCHEMA",
					"",
					spanner.NullInt64{},
					lockID,
					spanner.CommitTimestamp,
					int64(0),
					true,
				},
			)
			if bufferErr := txn.BufferWrite([]*spanner.Mutation{m}); bufferErr != nil {
				return bufferErr
			}
			acquired = true
			return nil
		})

		if err == nil && acquired {
			db.setActiveLockErr(nil)

			interval := db.heartbeatInterval
			if interval <= 0 {
				interval = 3 * time.Minute
			}

			var lockErrMu sync.Mutex
			var heartbeatErr error
			setHeartbeatErr := func(err error) {
				lockErrMu.Lock()
				if heartbeatErr == nil {
					heartbeatErr = err
				}
				lockErrMu.Unlock()
				db.setActiveLockErr(err)
			}
			getHeartbeatErr := func() error {
				lockErrMu.Lock()
				defer lockErrMu.Unlock()
				return heartbeatErr
			}

			// Start background lock heartbeat to update installed_on every interval
			heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				ticker := time.NewTicker(interval)
				defer ticker.Stop()

				for {
					select {
					case <-heartbeatCtx.Done():
						return
					case <-ticker.C:
						_, hErr := db.client.ReadWriteTransaction(heartbeatCtx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
							stmt := spanner.Statement{
								SQL: fmt.Sprintf("SELECT version, installed_by FROM %s WHERE installed_rank = -1", db.Quote(tableName)),
							}
							iter := txn.Query(ctx, stmt)
							defer iter.Stop()

							row, err := iter.Next()
							if err == iterator.Done {
								return fmt.Errorf("lock row missing during heartbeat")
							}
							if err != nil {
								return err
							}

							var verNull spanner.NullString
							var instBy string
							if err := row.Columns(&verNull, &instBy); err != nil {
								return err
							}
							owner := extractLockOwner(verNull, instBy)
							if owner != lockID {
								return fmt.Errorf("lock ownership lost during heartbeat: owned by %q, expected %q", owner, lockID)
							}

							m := spanner.Update(tableName,
								[]string{"installed_rank", "installed_on"},
								[]interface{}{int64(-1), spanner.CommitTimestamp},
							)
							return txn.BufferWrite([]*spanner.Mutation{m})
						})

						if heartbeatCtx.Err() != nil {
							return
						}
						if hErr != nil {
							setHeartbeatErr(hErr)
							if db.onHeartbeat != nil {
								db.onHeartbeat(hErr)
							}
							return
						}
						if db.onHeartbeat != nil {
							db.onHeartbeat(nil)
						}
					}
				}
			}()

			var unlockOnce sync.Once
			var unlockErr error
			unlock := func(unlockCtx context.Context) error {
				unlockOnce.Do(func() {
					cancelHeartbeat()
					wg.Wait()

					hbErr := getHeartbeatErr()
					db.setActiveLockErr(nil)

					_, delErr := db.client.ReadWriteTransaction(unlockCtx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
						stmt := spanner.Statement{
							SQL: fmt.Sprintf("SELECT version, installed_by FROM %s WHERE installed_rank = -1", db.Quote(tableName)),
						}
						iter := txn.Query(ctx, stmt)
						defer iter.Stop()

						row, err := iter.Next()
						if err == iterator.Done {
							return fmt.Errorf("lock row missing on unlock")
						}
						if err != nil {
							return err
						}

						var verNull spanner.NullString
						var instBy string
						if err := row.Columns(&verNull, &instBy); err != nil {
							return err
						}
						owner := extractLockOwner(verNull, instBy)
						if owner != lockID {
							return fmt.Errorf("cannot unlock: lock owned by %q, expected %q", owner, lockID)
						}

						m := spanner.Delete(tableName, spanner.Key{int64(-1)})
						return txn.BufferWrite([]*spanner.Mutation{m})
					})

					if hbErr != nil {
						unlockErr = fmt.Errorf("lock heartbeat failed: %w", hbErr)
					} else if delErr != nil {
						unlockErr = fmt.Errorf("failed to release Spanner lock: %w", delErr)
					}
				})
				return unlockErr
			}
			return unlock, nil
		}

		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}

	return nil, fmt.Errorf("failed to acquire Spanner lock after %d attempts: %w", retryCount, lastErr)
}

// ExecuteStatement executes a SQL statement, routing DDL to UpdateDatabaseDdl and DML/Queries to the client.
func (db *SpannerDatabase) ExecuteStatement(ctx context.Context, sql string) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

	trimmed := strings.TrimSpace(sql)
	if trimmed == "" || trimmed == ";" {
		return nil
	}

	// Trim trailing semicolons for Spanner DDL/DML
	trimmed = strings.TrimRight(trimmed, ";")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return nil
	}

	if isDDL(trimmed) {
		op, err := db.adminClient.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
			Database:   db.databasePath(),
			Statements: []string{trimmed},
		})
		if err != nil {
			return fmt.Errorf("failed to submit Spanner DDL statement: %w", err)
		}
		if err := op.Wait(ctx); err != nil {
			return fmt.Errorf("Spanner DDL execution failed: %w", err)
		}
		return nil
	}

	// Query or DML
	upper := strings.ToUpper(stripLeadingComments(trimmed))
	if strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH") || strings.HasPrefix(upper, "SHOW") {
		iter := db.client.Single().Query(ctx, spanner.NewStatement(trimmed))
		defer iter.Stop()
		for {
			_, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return fmt.Errorf("query execution failed: %w", err)
			}
		}
		return nil
	}

	// DML in ReadWriteTransaction
	_, err := db.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		_, execErr := txn.Update(ctx, spanner.NewStatement(trimmed))
		return execErr
	})
	if err != nil {
		return fmt.Errorf("Spanner DML execution failed: %w", err)
	}
	return nil
}

func isDDL(sql string) bool {
	clean := stripLeadingComments(sql)
	fields := strings.Fields(clean)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToUpper(fields[0]) {
	case "CREATE", "ALTER", "DROP", "GRANT", "REVOKE", "RENAME", "ANALYZE":
		return true
	}
	return false
}

func stripLeadingComments(sql string) string {
	s := strings.TrimSpace(sql)
	for {
		if strings.HasPrefix(s, "--") {
			if idx := strings.Index(s, "\n"); idx != -1 {
				s = strings.TrimSpace(s[idx+1:])
			} else {
				return ""
			}
		} else if strings.HasPrefix(s, "#") {
			if idx := strings.Index(s, "\n"); idx != -1 {
				s = strings.TrimSpace(s[idx+1:])
			} else {
				return ""
			}
		} else if strings.HasPrefix(s, "/*") {
			if idx := strings.Index(s, "*/"); idx != -1 {
				s = strings.TrimSpace(s[idx+2:])
			} else {
				return ""
			}
		} else {
			break
		}
	}
	return s
}
