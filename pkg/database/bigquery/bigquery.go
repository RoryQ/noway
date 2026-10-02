package bigquery

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database"
	"github.com/RoryQ/noway/pkg/resolver"
	"github.com/RoryQ/noway/pkg/version"
)

// BigQueryDatabase implements database.Database for Google Cloud BigQuery.
type BigQueryDatabase struct {
	client    *bigquery.Client
	projectID string
	location  string
	config    *config.Configuration
}

// New creates and initializes a new BigQueryDatabase instance.
func New(ctx context.Context, cfg *config.Configuration) (*BigQueryDatabase, error) {
	if cfg.GCPProjectID == "" {
		return nil, fmt.Errorf("BigQuery project ID is required. Set via config (gcpProjectId or ProjectId in JDBC URL) or GOOGLE_CLOUD_PROJECT env var")
	}

	var opts []option.ClientOption

	endpoint := cfg.GCPBigQueryEndpoint
	if endpoint == "" {
		endpoint = os.Getenv("BIGQUERY_EMULATOR_HOST")
	}
	if endpoint != "" {
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = "http://" + endpoint
		}
		if !strings.HasSuffix(endpoint, "/") {
			endpoint += "/"
		}
		if !strings.Contains(endpoint, "/bigquery/v2/") {
			endpoint += "bigquery/v2/"
		}
		opts = append(opts, option.WithEndpoint(endpoint), option.WithoutAuthentication())
	} else {
		if cfg.GCPCredentialsJSON != "" {
			opts = append(opts, option.WithCredentialsJSON([]byte(cfg.GCPCredentialsJSON)))
		} else if cfg.GCPCredentialsFile != "" {
			opts = append(opts, option.WithCredentialsFile(cfg.GCPCredentialsFile))
		}
	}

	client, err := bigquery.NewClient(ctx, cfg.GCPProjectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create BigQuery client for project %s: %w", cfg.GCPProjectID, err)
	}

	if cfg.GCPLocation != "" {
		client.Location = cfg.GCPLocation
	}

	return &BigQueryDatabase{
		client:    client,
		projectID: cfg.GCPProjectID,
		location:  cfg.GCPLocation,
		config:    cfg,
	}, nil
}

// Close closes the BigQuery client.
func (db *BigQueryDatabase) Close() error {
	if db.client != nil {
		return db.client.Close()
	}
	return nil
}

// Quote quotes an identifier with backticks, escaping internal backticks with \`.
func (db *BigQueryDatabase) Quote(identifiers ...string) string {
	quoted := make([]string, len(identifiers))
	for i, id := range identifiers {
		escaped := strings.ReplaceAll(id, "`", "\\`")
		quoted[i] = "`" + escaped + "`"
	}
	return strings.Join(quoted, ".")
}

// GetCurrentUser returns the current session user.
func (db *BigQueryDatabase) GetCurrentUser(ctx context.Context) (string, error) {
	q := db.client.Query("SELECT SESSION_USER() as user")
	it, err := q.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get current user: %w", err)
	}

	var row struct {
		User string `bigquery:"user"`
	}
	err = it.Next(&row)
	if err != nil && err != iterator.Done {
		return "", err
	}
	if row.User != "" {
		return row.User, nil
	}
	return "unknown", nil
}

// EnsureSchema creates the dataset if it does not exist.
func (db *BigQueryDatabase) EnsureSchema(ctx context.Context, schema string) error {
	exists, err := db.SchemaExists(ctx, schema)
	if err == nil && exists {
		return nil
	}

	ds := db.client.Dataset(schema)
	meta := &bigquery.DatasetMetadata{
		Location: db.location,
	}
	err = ds.Create(ctx, meta)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "already exists") || strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "duplicate") {
			return nil
		}
		// Fallback to SQL DDL
		sql := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", db.Quote(schema))
		return db.ExecuteStatement(ctx, sql)
	}
	return nil
}

// SchemaExists checks if the dataset exists.
func (db *BigQueryDatabase) SchemaExists(ctx context.Context, schema string) (bool, error) {
	_, err := db.client.Dataset(schema).Metadata(ctx)
	if err == nil {
		return true, nil
	}
	errLower := strings.ToLower(err.Error())
	if strings.Contains(errLower, "not found") || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NOT_FOUND") {
		return false, nil
	}

	// Fallback to INFORMATION_SCHEMA query
	sql := fmt.Sprintf("SELECT COUNT(table_name) as count FROM %s.INFORMATION_SCHEMA.TABLES", db.Quote(schema))
	q := db.client.Query(sql)
	it, err := q.Read(ctx)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "not found") || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NOT_FOUND") {
			return false, nil
		}
		return false, err
	}

	var row struct {
		Count int64 `bigquery:"count"`
	}
	err = it.Next(&row)
	if err != nil && err != iterator.Done {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "not found") || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NOT_FOUND") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// SchemaEmpty checks if the dataset has 0 tables and 0 routines.
func (db *BigQueryDatabase) SchemaEmpty(ctx context.Context, schema string) (bool, error) {
	exists, err := db.SchemaExists(ctx, schema)
	if err != nil || !exists {
		return exists, err
	}

	sql := fmt.Sprintf(`SELECT 
		(SELECT COUNT(table_name) FROM %s.INFORMATION_SCHEMA.TABLES) + 
		(SELECT COUNT(routine_name) FROM %s.INFORMATION_SCHEMA.ROUTINES) as total_count`,
		db.Quote(schema), db.Quote(schema))

	q := db.client.Query(sql)
	it, err := q.Read(ctx)
	if err != nil {
		return false, err
	}

	var row struct {
		TotalCount int64 `bigquery:"total_count"`
	}
	if err := it.Next(&row); err != nil {
		return false, err
	}

	return row.TotalCount == 0, nil
}

// DropSchema drops the entire dataset with CASCADE.
func (db *BigQueryDatabase) DropSchema(ctx context.Context, schema string) error {
	sql := fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", db.Quote(schema))
	return db.ExecuteStatement(ctx, sql)
}

// CleanSchema drops all tables, views, and routines from the dataset.
func (db *BigQueryDatabase) CleanSchema(ctx context.Context, schema string) error {
	// Drop views and materialized views before base tables to avoid dependency issues
	types := []struct {
		tableType string
		dropType  string
	}{
		{"MATERIALIZED VIEW", "MATERIALIZED VIEW"},
		{"VIEW", "VIEW"},
		{"BASE TABLE", "TABLE"},
		{"SNAPSHOT", "SNAPSHOT TABLE"},
		{"CLONE", "TABLE"},
		{"EXTERNAL", "EXTERNAL TABLE"},
	}

	for _, t := range types {
		sql := fmt.Sprintf("SELECT table_name FROM %s.INFORMATION_SCHEMA.TABLES WHERE table_type = @table_type", db.Quote(schema))
		q := db.client.Query(sql)
		q.Parameters = []bigquery.QueryParameter{
			{Name: "table_type", Value: t.tableType},
		}
		it, err := q.Read(ctx)
		if err != nil {
			errLower := strings.ToLower(err.Error())
			if strings.Contains(errLower, "not found") || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NOT_FOUND") {
				return nil
			}
			return err
		}

		for {
			var row struct {
				TableName string `bigquery:"table_name"`
			}
			err := it.Next(&row)
			if err == iterator.Done {
				break
			}
			if err != nil {
				return err
			}

			dropStmt := fmt.Sprintf("DROP %s IF EXISTS %s", t.dropType, db.Quote(schema, row.TableName))
			if err := db.ExecuteStatement(ctx, dropStmt); err != nil {
				return fmt.Errorf("failed to drop %s %s: %w", t.dropType, row.TableName, err)
			}
		}
	}

	routineTypes := []string{"FUNCTION", "PROCEDURE", "TABLE FUNCTION"}
	for _, rt := range routineTypes {
		sql := fmt.Sprintf("SELECT routine_name FROM %s.INFORMATION_SCHEMA.ROUTINES WHERE routine_type = @routine_type", db.Quote(schema))
		q := db.client.Query(sql)
		q.Parameters = []bigquery.QueryParameter{
			{Name: "routine_type", Value: rt},
		}
		it, err := q.Read(ctx)
		if err != nil {
			errLower := strings.ToLower(err.Error())
			if strings.Contains(errLower, "not found") || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NOT_FOUND") {
				return nil
			}
			return err
		}

		for {
			var row struct {
				RoutineName string `bigquery:"routine_name"`
			}
			err := it.Next(&row)
			if err == iterator.Done {
				break
			}
			if err != nil {
				return err
			}

			dropStmt := fmt.Sprintf("DROP %s IF EXISTS %s", rt, db.Quote(schema, row.RoutineName))
			if err := db.ExecuteStatement(ctx, dropStmt); err != nil {
				return fmt.Errorf("failed to drop %s %s: %w", rt, row.RoutineName, err)
			}
		}
	}

	return nil
}

// EnsureHistoryTable creates the Flyway schema history table.
func (db *BigQueryDatabase) EnsureHistoryTable(ctx context.Context, schema, table string) error {
	tbl := db.client.Dataset(schema).Table(table)
	historySchema := bigquery.Schema{
		{Name: "installed_rank", Type: bigquery.IntegerFieldType, Required: true},
		{Name: "version", Type: bigquery.StringFieldType},
		{Name: "description", Type: bigquery.StringFieldType, Required: true},
		{Name: "type", Type: bigquery.StringFieldType, Required: true},
		{Name: "script", Type: bigquery.StringFieldType, Required: true},
		{Name: "checksum", Type: bigquery.IntegerFieldType},
		{Name: "installed_by", Type: bigquery.StringFieldType, Required: true},
		{Name: "installed_on", Type: bigquery.TimestampFieldType},
		{Name: "execution_time", Type: bigquery.IntegerFieldType, Required: true},
		{Name: "success", Type: bigquery.BooleanFieldType, Required: true},
	}
	err := tbl.Create(ctx, &bigquery.TableMetadata{
		Schema: historySchema,
	})
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "already exists") || strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "duplicate") {
			return nil
		}
		// Fallback to SQL DDL
		sql := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    `+"`installed_rank`"+` INT64 NOT NULL,
    `+"`version`"+` STRING,
    `+"`description`"+` STRING NOT NULL,
    `+"`type`"+` STRING NOT NULL,
    `+"`script`"+` STRING NOT NULL,
    `+"`checksum`"+` INT64,
    `+"`installed_by`"+` STRING NOT NULL,
    `+"`installed_on`"+` TIMESTAMP,
    `+"`execution_time`"+` INT64 NOT NULL,
    `+"`success`"+` BOOL NOT NULL
)`, db.Quote(schema, table))
		return db.ExecuteStatement(ctx, sql)
	}
	return nil
}

// HistoryTableExists checks if the history table exists.
func (db *BigQueryDatabase) HistoryTableExists(ctx context.Context, schema, table string) (bool, error) {
	_, err := db.client.Dataset(schema).Table(table).Metadata(ctx)
	if err == nil {
		return true, nil
	}
	errLower := strings.ToLower(err.Error())
	if strings.Contains(errLower, "not found") || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NOT_FOUND") {
		return false, nil
	}

	// Fallback to INFORMATION_SCHEMA query
	sql := fmt.Sprintf("SELECT COUNT(table_name) as count FROM %s.INFORMATION_SCHEMA.TABLES WHERE table_name = @table_name", db.Quote(schema))
	q := db.client.Query(sql)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "table_name", Value: table},
	}
	it, err := q.Read(ctx)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "not found") || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NOT_FOUND") {
			return false, nil
		}
		return false, err
	}

	var row struct {
		Count int64 `bigquery:"count"`
	}
	if err := it.Next(&row); err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "not found") || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NOT_FOUND") {
			return false, nil
		}
		return false, err
	}
	return row.Count > 0, nil
}

// FetchHistory reads all migration records from the history table.
func (db *BigQueryDatabase) FetchHistory(ctx context.Context, schema, table string) ([]resolver.AppliedMigration, error) {
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
	WHERE `+"`installed_rank`"+` > 0 
	ORDER BY `+"`installed_rank`"+` ASC`, db.Quote(schema, table))

	q := db.client.Query(sql)
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch schema history: %w", err)
	}

	var records []resolver.AppliedMigration
	for {
		var row struct {
			InstalledRank int64                   `bigquery:"installed_rank"`
			Version       bigquery.NullString     `bigquery:"version"`
			Description   string                  `bigquery:"description"`
			Type          string                  `bigquery:"type"`
			Script        string                  `bigquery:"script"`
			Checksum      bigquery.NullInt64      `bigquery:"checksum"`
			InstalledBy   string                  `bigquery:"installed_by"`
			InstalledOn   bigquery.NullTimestamp  `bigquery:"installed_on"`
			ExecutionTime int64                   `bigquery:"execution_time"`
			Success       bool                    `bigquery:"success"`
		}

		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading schema history row: %w", err)
		}

		var vPtr *version.Version
		if row.Version.Valid && row.Version.StringVal != "" {
			v, err := version.Parse(row.Version.StringVal)
			if err == nil {
				vPtr = &v
			}
		}

		var csPtr *int64
		if row.Checksum.Valid {
			c := row.Checksum.Int64
			csPtr = &c
		}

		var instOn time.Time
		if row.InstalledOn.Valid {
			instOn = row.InstalledOn.Timestamp
		}

		records = append(records, resolver.AppliedMigration{
			InstalledRank: int(row.InstalledRank),
			Version:       vPtr,
			Description:   row.Description,
			Type:          row.Type,
			Script:        row.Script,
			Checksum:      csPtr,
			InstalledBy:   row.InstalledBy,
			InstalledOn:   instOn,
			ExecutionTime: row.ExecutionTime,
			Success:       row.Success,
		})
	}

	return records, nil
}

// InsertHistory inserts a record into the history table.
func (db *BigQueryDatabase) InsertHistory(ctx context.Context, schema, table string, rec database.HistoryRecord) error {
	sql := fmt.Sprintf(`INSERT INTO %s (
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
	) VALUES (
		@installed_rank,
		@version,
		@description,
		@type,
		@script,
		@checksum,
		@installed_by,
		CURRENT_TIMESTAMP(),
		@execution_time,
		@success
	)`, db.Quote(schema, table))

	q := db.client.Query(sql)

	verNull := bigquery.NullString{}
	if rec.Version != nil {
		verNull.StringVal = *rec.Version
		verNull.Valid = true
	}

	csNull := bigquery.NullInt64{}
	if rec.Checksum != nil {
		csNull.Int64 = *rec.Checksum
		csNull.Valid = true
	}

	q.Parameters = []bigquery.QueryParameter{
		{Name: "installed_rank", Value: int64(rec.InstalledRank)},
		{Name: "version", Value: verNull},
		{Name: "description", Value: rec.Description},
		{Name: "type", Value: rec.Type},
		{Name: "script", Value: rec.Script},
		{Name: "checksum", Value: csNull},
		{Name: "installed_by", Value: rec.InstalledBy},
		{Name: "execution_time", Value: rec.ExecutionTime},
		{Name: "success", Value: rec.Success},
	}

	job, err := q.Run(ctx)
	if err != nil {
		return fmt.Errorf("failed to insert history record: %w", err)
	}

	status, err := job.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed waiting for history insert job: %w", err)
	}
	if err := status.Err(); err != nil {
		return fmt.Errorf("history insert failed: %w", err)
	}
	return nil
}

// UpdateHistory updates an existing migration record.
func (db *BigQueryDatabase) UpdateHistory(ctx context.Context, schema, table string, rec database.HistoryRecord) error {
	sql := fmt.Sprintf(`UPDATE %s
	SET `+"`checksum`"+` = @checksum,
	    `+"`description`"+` = @description,
	    `+"`type`"+` = @type
	WHERE `+"`installed_rank`"+` = @installed_rank`, db.Quote(schema, table))

	q := db.client.Query(sql)
	csNull := bigquery.NullInt64{}
	if rec.Checksum != nil {
		csNull.Int64 = *rec.Checksum
		csNull.Valid = true
	}

	q.Parameters = []bigquery.QueryParameter{
		{Name: "checksum", Value: csNull},
		{Name: "description", Value: rec.Description},
		{Name: "type", Value: rec.Type},
		{Name: "installed_rank", Value: int64(rec.InstalledRank)},
	}

	job, err := q.Run(ctx)
	if err != nil {
		return fmt.Errorf("failed to update history record: %w", err)
	}

	status, err := job.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed waiting for history update job: %w", err)
	}
	return status.Err()
}

// DeleteHistory deletes a migration record.
func (db *BigQueryDatabase) DeleteHistory(ctx context.Context, schema, table string, installedRank int) error {
	sql := fmt.Sprintf("DELETE FROM %s WHERE `installed_rank` = @installed_rank", db.Quote(schema, table))
	q := db.client.Query(sql)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "installed_rank", Value: int64(installedRank)},
	}

	job, err := q.Run(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete history record: %w", err)
	}

	status, err := job.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed waiting for history delete job: %w", err)
	}
	return status.Err()
}

// Lock acquires a Flyway-compatible row lock in the history table.
func (db *BigQueryDatabase) Lock(ctx context.Context, schema, table string) (database.UnlockFunc, error) {
	lockID := uuid.New().String()
	tableName := db.Quote(schema, table)

	// Clean expired locks older than 10 minutes
	delExpiredSQL := fmt.Sprintf(`DELETE FROM %s 
	WHERE `+"`description`"+` = 'flyway-lock' 
	  AND `+"`installed_on`"+` < TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 10 MINUTE)`, tableName)
	_ = db.ExecuteStatement(ctx, delExpiredSQL)

	insertLockSQL := fmt.Sprintf(`INSERT INTO %s (
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
	) VALUES (-100, @lock_id, 'flyway-lock', '', '', 0, '', CURRENT_TIMESTAMP(), 0, TRUE)`, tableName)

	checkActiveLockSQL := fmt.Sprintf(`SELECT version FROM %s 
	WHERE `+"`description`"+` = 'flyway-lock' 
	ORDER BY `+"`installed_on`"+` ASC LIMIT 1`, tableName)

	delSelfSQL := fmt.Sprintf("DELETE FROM %s WHERE `version` = @lock_id AND `description` = 'flyway-lock'", tableName)

	maxRetries := db.config.LockRetryCount
	if maxRetries <= 0 {
		maxRetries = 50
	}

	acquired := false
	for attempt := 0; attempt < maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Try insert lock row
		q := db.client.Query(insertLockSQL)
		q.Parameters = []bigquery.QueryParameter{
			{Name: "lock_id", Value: lockID},
		}
		job, err := q.Run(ctx)
		if err == nil {
			status, err := job.Wait(ctx)
			if err == nil && status.Err() == nil {
				// Verify if our lock is the active lock holder
				checkQ := db.client.Query(checkActiveLockSQL)
				it, err := checkQ.Read(ctx)
				if err == nil {
					var row struct {
						Version string `bigquery:"version"`
					}
					if it.Next(&row) == nil {
						if row.Version == lockID {
							acquired = true
							break
						} else {
							// Another process holds the lock; delete our extra attempt row
							selfQ := db.client.Query(delSelfSQL)
							selfQ.Parameters = []bigquery.QueryParameter{
								{Name: "lock_id", Value: lockID},
							}
							if delJob, err := selfQ.Run(ctx); err == nil {
								_, _ = delJob.Wait(ctx)
							}
						}
					}
				}
			}
		}

		time.Sleep(1 * time.Second)
	}

	if !acquired {
		// Clean up any stray row for this lockID
		cleanQ := db.client.Query(delSelfSQL)
		cleanQ.Parameters = []bigquery.QueryParameter{
			{Name: "lock_id", Value: lockID},
		}
		if cleanJob, err := cleanQ.Run(context.Background()); err == nil {
			_, _ = cleanJob.Wait(context.Background())
		}
		return nil, fmt.Errorf("unable to obtain lock on Flyway schema history table %s.%s after %d retries", schema, table, maxRetries)
	}

	// Start background lock heartbeat to update installed_on every 3 minutes
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(3 * time.Minute)
		defer ticker.Stop()

		updateSQL := fmt.Sprintf(`UPDATE %s 
		SET `+"`installed_on`"+` = CURRENT_TIMESTAMP() 
		WHERE `+"`version`"+` = @lock_id AND `+"`description`"+` = 'flyway-lock'`, tableName)

		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				q := db.client.Query(updateSQL)
				q.Parameters = []bigquery.QueryParameter{
					{Name: "lock_id", Value: lockID},
				}
				job, err := q.Run(heartbeatCtx)
				if err == nil {
					_, _ = job.Wait(heartbeatCtx)
				}
			}
		}
	}()

	unlock := func(unlockCtx context.Context) error {
		cancelHeartbeat()
		wg.Wait()

		delLockSQL := fmt.Sprintf(`DELETE FROM %s 
		WHERE `+"`version`"+` = @lock_id AND `+"`description`"+` = 'flyway-lock'`, tableName)

		q := db.client.Query(delLockSQL)
		q.Parameters = []bigquery.QueryParameter{
			{Name: "lock_id", Value: lockID},
		}
		job, err := q.Run(unlockCtx)
		if err != nil {
			return fmt.Errorf("failed to release lock: %w", err)
		}
		status, err := job.Wait(unlockCtx)
		if err != nil {
			return fmt.Errorf("failed waiting for lock release job: %w", err)
		}
		return status.Err()
	}

	return unlock, nil
}

// ExecuteStatement executes a SQL statement against BigQuery.
func (db *BigQueryDatabase) ExecuteStatement(ctx context.Context, sql string) error {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return nil
	}

	q := db.client.Query(trimmed)
	job, err := q.Run(ctx)
	if err != nil {
		return fmt.Errorf("failed to run query: %w\nSQL: %s", err, trimmed)
	}

	status, err := job.Wait(ctx)
	if err != nil {
		return fmt.Errorf("query execution wait failed: %w\nSQL: %s", err, trimmed)
	}
	if err := status.Err(); err != nil {
		return fmt.Errorf("query execution failed: %w\nSQL: %s", err, trimmed)
	}

	return nil
}
