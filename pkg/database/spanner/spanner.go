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

// SchemaEmpty checks if the database has no user tables.
func (db *SpannerDatabase) SchemaEmpty(ctx context.Context, schema string) (bool, error) {
	sql := `SELECT COUNT(1) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '' AND TABLE_TYPE = 'BASE TABLE'`
	iter := db.client.Single().Query(ctx, spanner.NewStatement(sql))
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
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
func (db *SpannerDatabase) CleanSchema(ctx context.Context, schema string) error {
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

	// 4. Base tables
	tblSQL := `SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_TYPE = 'BASE TABLE' AND TABLE_SCHEMA = ''`
	iter = db.client.Single().Query(ctx, spanner.NewStatement(tblSQL))
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			break
		}
		var tName string
		if err := row.Column(0, &tName); err == nil {
			ddlStatements = append(ddlStatements, fmt.Sprintf("DROP TABLE %s", db.Quote(tName)))
		}
	}
	iter.Stop()

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

// InsertHistory inserts a new applied migration record into the history table.
func (db *SpannerDatabase) InsertHistory(ctx context.Context, schema, table string, rec database.HistoryRecord) error {
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

	// Clean expired locks older than 10 minutes
	delExpiredSQL := fmt.Sprintf(`DELETE FROM %s 
	WHERE `+"`installed_rank`"+` = -1 
	  AND `+"`installed_on`"+` < TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 10 MINUTE)`, db.Quote(tableName))
	_ = db.ExecuteStatement(ctx, delExpiredSQL)

	retryCount := db.config.LockRetryCount
	if retryCount <= 0 {
		retryCount = 50
	}

	var lastErr error
	for attempt := 0; attempt < retryCount; attempt++ {
		acquired := false
		_, err := db.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
			// Read lock row
			stmt := spanner.Statement{
				SQL: fmt.Sprintf("SELECT installed_on FROM %s WHERE installed_rank = -1", db.Quote(tableName)),
			}
			iter := txn.Query(ctx, stmt)
			defer iter.Stop()

			row, err := iter.Next()
			if err != nil && err != iterator.Done {
				return err
			}
			if err != iterator.Done {
				// Lock row exists
				var instOn time.Time
				if scanErr := row.Column(0, &instOn); scanErr == nil {
					if time.Since(instOn) < 10*time.Minute {
						return fmt.Errorf("lock already held")
					}
				}
			}

			// Insert or update lock row
			m := spanner.InsertOrUpdate(tableName,
				[]string{"installed_rank", "version", "description", "type", "script", "checksum", "installed_by", "installed_on", "execution_time", "success"},
				[]interface{}{
					int64(-1),
					spanner.NullString{},
					"flyway-lock",
					"SCHEMA",
					"",
					spanner.NullInt64{},
					"noway-lock",
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
			unlock := func(unlockCtx context.Context) error {
				m := spanner.Delete(tableName, spanner.Key{int64(-1)}.AsPrefix())
				_, uErr := db.client.Apply(unlockCtx, []*spanner.Mutation{m})
				return uErr
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
