package bigquery

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/google/uuid"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/resolver"
	"github.com/roryq/noway/pkg/version"
)

var (
	// ErrLockRowMissing indicates that the lock row was deleted from the schema history table during heartbeat.
	ErrLockRowMissing = errors.New("lock row missing during heartbeat")

	// ErrLockOwnershipLost indicates that lock ownership was taken by another process during heartbeat.
	ErrLockOwnershipLost = errors.New("lock ownership lost during heartbeat")
)

// BigQueryDatabase implements database.Database for Google Cloud BigQuery.
type BigQueryDatabase struct {
	client              *bigquery.Client
	projectID           string
	location            string
	locationMu          sync.RWMutex
	config              *config.Configuration
	retryInitialBackoff time.Duration
	retryMaxBackoff     time.Duration

	activeLockMu      sync.Mutex
	activeLockErr     error
	heartbeatInterval time.Duration
	lockTTL           time.Duration
	onHeartbeat       func(err error)

	heartbeatAttemptInterceptor func() error
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

func (db *BigQueryDatabase) getLocation() string {
	db.locationMu.RLock()
	defer db.locationMu.RUnlock()
	return db.location
}

// recordLocation records the discovered dataset or job location to use as a fallback default
// when no explicit GCPLocation is configured in config.Configuration.
// Note: BigQuery requires queries referencing datasets to run in the dataset's region;
// if multiple datasets exist across different GCP regions in the same project, set GCPLocation
// explicitly or ensure dataset-specific queries resolve location per dataset.
func (db *BigQueryDatabase) recordLocation(loc string) {
	if loc == "" {
		return
	}
	db.locationMu.Lock()
	defer db.locationMu.Unlock()
	if db.location == "" {
		db.location = loc
	}
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
		Location: db.getLocation(),
	}
	return db.retry(ctx, func() error {
		err := ds.Create(ctx, meta)
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
	})
}

// SchemaExists checks if the dataset exists.
func (db *BigQueryDatabase) SchemaExists(ctx context.Context, schema string) (bool, error) {
	meta, err := db.client.Dataset(schema).Metadata(ctx)
	if err == nil {
		if meta != nil {
			db.recordLocation(meta.Location)
		}
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

	sql := fmt.Sprintf(`SELECT 
		(SELECT COUNT(table_name) FROM %s.INFORMATION_SCHEMA.TABLES WHERE table_name != @table_name) + 
		(SELECT COUNT(routine_name) FROM %s.INFORMATION_SCHEMA.ROUTINES) as total_count`,
		db.Quote(schema), db.Quote(schema))

	q := db.client.Query(sql)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "table_name", Value: historyTable},
	}
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
// If excludeTables are provided, those tables are preserved and their migration records cleared.
func (db *BigQueryDatabase) CleanSchema(ctx context.Context, schema string, excludeTables ...string) error {
	excludeMap := make(map[string]bool)
	for _, ext := range excludeTables {
		if ext != "" {
			excludeMap[ext] = true
		}
	}

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

	preservedTables := make(map[string]bool)
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

			if excludeMap[row.TableName] {
				preservedTables[row.TableName] = true
				continue
			}

			dropStmt := fmt.Sprintf("DROP %s IF EXISTS %s", t.dropType, db.Quote(schema, row.TableName))
			if err := db.ExecuteStatement(ctx, dropStmt); err != nil {
				return fmt.Errorf("failed to drop %s %s: %w", t.dropType, row.TableName, err)
			}
		}
	}

	for pt := range preservedTables {
		delSQL := fmt.Sprintf("DELETE FROM %s WHERE `installed_rank` >= 0 OR `description` != 'flyway-lock'", db.Quote(schema, pt))
		if err := db.ExecuteStatement(ctx, delSQL); err != nil {
			return fmt.Errorf("failed to clean preserved table %s: %w", pt, err)
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
	return db.retry(ctx, func() error {
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
	})
}

// HistoryTableExists checks if the history table exists.
func (db *BigQueryDatabase) HistoryTableExists(ctx context.Context, schema, table string) (bool, error) {
	meta, err := db.client.Dataset(schema).Table(table).Metadata(ctx)
	if err == nil {
		if meta != nil {
			db.recordLocation(meta.Location)
		}
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

	var records []resolver.AppliedMigration
	err := db.retry(ctx, func() error {
		records = nil
		q := db.client.Query(sql)
		it, err := q.Read(ctx)
		if err != nil {
			return fmt.Errorf("failed to fetch schema history: %w", err)
		}

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
				return fmt.Errorf("error reading schema history row: %w", err)
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
		return nil
	})

	if err != nil {
		return nil, err
	}
	return records, nil
}

// InsertHistory inserts a record into the history table.
func (db *BigQueryDatabase) InsertHistory(ctx context.Context, schema, table string, rec database.HistoryRecord) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

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

	q := db.client.Query(sql)
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

	return db.executeQuery(ctx, q)
}

// UpdateHistory updates an existing migration record.
func (db *BigQueryDatabase) UpdateHistory(ctx context.Context, schema, table string, rec database.HistoryRecord) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

	sql := fmt.Sprintf(`UPDATE %s
	SET `+"`checksum`"+` = @checksum,
	    `+"`description`"+` = @description,
	    `+"`type`"+` = @type
	WHERE `+"`installed_rank`"+` = @installed_rank`, db.Quote(schema, table))

	csNull := bigquery.NullInt64{}
	if rec.Checksum != nil {
		csNull.Int64 = *rec.Checksum
		csNull.Valid = true
	}

	q := db.client.Query(sql)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "checksum", Value: csNull},
		{Name: "description", Value: rec.Description},
		{Name: "type", Value: rec.Type},
		{Name: "installed_rank", Value: int64(rec.InstalledRank)},
	}

	return db.executeQuery(ctx, q)
}

// DeleteHistory deletes a migration record.
func (db *BigQueryDatabase) DeleteHistory(ctx context.Context, schema, table string, installedRank int) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

	sql := fmt.Sprintf("DELETE FROM %s WHERE `installed_rank` = @installed_rank", db.Quote(schema, table))

	q := db.client.Query(sql)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "installed_rank", Value: int64(installedRank)},
	}

	return db.executeQuery(ctx, q)
}

// Lock acquires a Flyway-compatible row lock in the history table.
func (db *BigQueryDatabase) Lock(ctx context.Context, schema, table string) (database.UnlockFunc, error) {
	lockID := uuid.New().String()
	tableName := db.Quote(schema, table)

	ttl := db.lockTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	var expireInterval string
	if ttl%time.Minute == 0 {
		expireInterval = fmt.Sprintf("INTERVAL %d MINUTE", int(ttl.Minutes()))
	} else if ttl%time.Second == 0 {
		expireInterval = fmt.Sprintf("INTERVAL %d SECOND", int(ttl.Seconds()))
	} else {
		expireInterval = fmt.Sprintf("INTERVAL %d MILLISECOND", ttl.Milliseconds())
	}
	delExpiredSQL := fmt.Sprintf(`DELETE FROM %s 
	WHERE `+"`description`"+` = 'flyway-lock' 
	  AND `+"`installed_on`"+` < TIMESTAMP_SUB(CURRENT_TIMESTAMP(), %s)`, tableName, expireInterval)
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

	maxRetries := 50
	if db.config != nil && db.config.LockRetryCount > 0 {
		maxRetries = db.config.LockRetryCount
	}

	acquired := false
	for attempt := 0; attempt < maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Try insert lock row as a single attempt (0 internal retries)
		// Outer Lock loop manages retries every 1 second, avoiding exponential backoff inflation.
		q := db.client.Query(insertLockSQL)
		q.Parameters = []bigquery.QueryParameter{
			{Name: "lock_id", Value: lockID},
		}
		if err := db.executeQueryWithRetries(ctx, q, 0); err == nil {
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
						_ = db.executeQueryWithRetries(ctx, selfQ, 0)
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
		_ = db.executeQuery(context.Background(), cleanQ)
		return nil, fmt.Errorf("unable to obtain lock on Flyway schema history table %s.%s after %d retries", schema, table, maxRetries)
	}

	// Start background lock heartbeat to update installed_on every 3 minutes (or custom heartbeatInterval)
	interval := 3 * time.Minute
	if db.heartbeatInterval > 0 {
		interval = db.heartbeatInterval
	}
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)

	var heartbeatErrMu sync.Mutex
	var heartbeatErr error
	setHeartbeatErr := func(err error) {
		heartbeatErrMu.Lock()
		if heartbeatErr == nil {
			heartbeatErr = err
		}
		heartbeatErrMu.Unlock()
		db.setActiveLockErr(err)
	}
	getHeartbeatErr := func() error {
		heartbeatErrMu.Lock()
		defer heartbeatErrMu.Unlock()
		return heartbeatErr
	}

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		lastHeartbeatSuccess := time.Now()
		safetyMargin := 2 * time.Minute
		if ttl <= 2*time.Minute {
			safetyMargin = ttl / 4
		}
		deadline := ttl - safetyMargin

		updateSQL := fmt.Sprintf(`UPDATE %s 
		SET `+"`installed_on`"+` = CURRENT_TIMESTAMP() 
		WHERE `+"`version`"+` = @lock_id AND `+"`description`"+` = 'flyway-lock'`, tableName)

		attemptHeartbeat := func() error {
			if db.heartbeatAttemptInterceptor != nil {
				if interceptErr := db.heartbeatAttemptInterceptor(); interceptErr != nil {
					return interceptErr
				}
			}
			tickTimeout := 30 * time.Second
			if interval < tickTimeout {
				tickTimeout = interval / 2
				if tickTimeout < 500*time.Millisecond {
					tickTimeout = 500 * time.Millisecond
				}
			}
			tickCtx, cancelTick := context.WithTimeout(heartbeatCtx, tickTimeout)
			defer cancelTick()

			q := db.client.Query(updateSQL)
			q.Parameters = []bigquery.QueryParameter{
				{Name: "lock_id", Value: lockID},
			}
			status, hErr := db.executeQueryWithStatus(tickCtx, q, 0)
			if heartbeatCtx.Err() != nil {
				return heartbeatCtx.Err()
			}
			if tickCtx.Err() != nil && hErr == nil {
				return tickCtx.Err()
			}
			if hErr == nil {
				// Check if any rows were updated. In BigQuery, an UPDATE that matches 0 rows
				// returns success without error. If 0 rows were updated, the lock was lost!
				if status != nil && status.Statistics != nil {
					if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok {
						if qs.NumDMLAffectedRows == 0 {
							hErr = db.determineLostLockError(tickCtx, tableName, lockID)
						}
					}
				}
			}
			return hErr
		}

		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				hErr := attemptHeartbeat()
				if heartbeatCtx.Err() != nil {
					return
				}
				if hErr == nil {
					lastHeartbeatSuccess = time.Now()
					if db.onHeartbeat != nil {
						db.onHeartbeat(nil)
					}
					continue
				}

				// Check if the lock was lost or missing (non-transient fatal error)
				if errors.Is(hErr, ErrLockOwnershipLost) || errors.Is(hErr, ErrLockRowMissing) {
					setHeartbeatErr(hErr)
					if db.onHeartbeat != nil {
						db.onHeartbeat(hErr)
					}
					return
				}

				// Transient error (e.g. Unavailable, 500/503, network blip, tick timeout): retry until TTL minus safety margin
				retryBackoff := 1 * time.Second
				if interval < 5*time.Second {
					retryBackoff = 20 * time.Millisecond
				}
				success := false
				for time.Since(lastHeartbeatSuccess) < deadline {
					select {
					case <-heartbeatCtx.Done():
						return
					case <-time.After(retryBackoff):
					}

					hErr = attemptHeartbeat()
					if heartbeatCtx.Err() != nil {
						return
					}
					if hErr == nil {
						lastHeartbeatSuccess = time.Now()
						if db.onHeartbeat != nil {
							db.onHeartbeat(nil)
						}
						success = true
						break
					}
					if errors.Is(hErr, ErrLockOwnershipLost) || errors.Is(hErr, ErrLockRowMissing) {
						setHeartbeatErr(hErr)
						if db.onHeartbeat != nil {
							db.onHeartbeat(hErr)
						}
						return
					}
					retryBackoff = min(retryBackoff*2, 10*time.Second)
				}

				if !success {
					finalErr := fmt.Errorf("lock heartbeat failed to refresh within TTL safety margin (last refresh %v ago, TTL %v): %w", time.Since(lastHeartbeatSuccess), ttl, hErr)
					setHeartbeatErr(finalErr)
					if db.onHeartbeat != nil {
						db.onHeartbeat(finalErr)
					}
					return
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

			delLockSQL := fmt.Sprintf(`DELETE FROM %s 
			WHERE `+"`version`"+` = @lock_id AND `+"`description`"+` = 'flyway-lock'`, tableName)

			q := db.client.Query(delLockSQL)
			q.Parameters = []bigquery.QueryParameter{
				{Name: "lock_id", Value: lockID},
			}
			delErr := db.executeQuery(unlockCtx, q)
			if hbErr != nil {
				unlockErr = fmt.Errorf("lock heartbeat failed: %w", hbErr)
				return
			}
			if delErr != nil {
				unlockErr = fmt.Errorf("failed to release lock: %w", delErr)
				return
			}
		})
		return unlockErr
	}

	return unlock, nil
}

func (db *BigQueryDatabase) determineLostLockError(ctx context.Context, tableName, lockID string) error {
	if db.client == nil {
		return ErrLockOwnershipLost
	}
	checkSQL := fmt.Sprintf("SELECT `version` FROM %s WHERE `description` = 'flyway-lock' LIMIT 1", tableName)
	q := db.client.Query(checkSQL)
	it, err := q.Read(ctx)
	if err != nil {
		return err
	}
	var row struct {
		Version string `bigquery:"version"`
	}
	if itErr := it.Next(&row); itErr == nil {
		if row.Version != lockID {
			return fmt.Errorf("%w: lock owned by %q", ErrLockOwnershipLost, row.Version)
		}
		return fmt.Errorf("lock row present but update affected 0 rows")
	} else if errors.Is(itErr, iterator.Done) {
		return fmt.Errorf("%w: row was deleted", ErrLockRowMissing)
	} else {
		return itErr
	}
}

func (db *BigQueryDatabase) setActiveLockErr(err error) {
	db.activeLockMu.Lock()
	defer db.activeLockMu.Unlock()
	db.activeLockErr = err
}

func (db *BigQueryDatabase) checkActiveLockErr() error {
	db.activeLockMu.Lock()
	defer db.activeLockMu.Unlock()
	if db.activeLockErr != nil {
		return fmt.Errorf("bigquery lock heartbeat error: %w", db.activeLockErr)
	}
	return nil
}

// ExecuteStatement executes a SQL statement against BigQuery.
func (db *BigQueryDatabase) ExecuteStatement(ctx context.Context, sql string) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return nil
	}

	q := db.client.Query(trimmed)
	if err := db.executeQuery(ctx, q); err != nil {
		return fmt.Errorf("%w\nSQL: %s", err, trimmed)
	}
	return nil
}

// executeQuery submits a query job and waits for it to complete.
// Transient submission failures retry with the same job ID (allowing 409 recovery if the server
// created the job), status polling errors never resubmit the job, and completed jobs whose
// status.Err() is retryable retry with a new job ID only if the job has not partially executed (e.g. multi-statement
// script jobs where prior statements succeeded and committed, or DML mutated rows).
func (db *BigQueryDatabase) executeQuery(ctx context.Context, q *bigquery.Query) error {
	maxRetries := 5
	if db.config != nil && db.config.ConnectRetries > 0 {
		maxRetries = db.config.ConnectRetries
	}
	return db.executeQueryWithRetries(ctx, q, maxRetries)
}

func (db *BigQueryDatabase) executeQueryWithRetries(ctx context.Context, q *bigquery.Query, maxRetries int) error {
	_, err := db.executeQueryWithStatus(ctx, q, maxRetries)
	return err
}

func (db *BigQueryDatabase) executeQueryWithStatus(ctx context.Context, q *bigquery.Query, maxRetries int) (*bigquery.JobStatus, error) {
	initialJobID := q.JobIDConfig.JobID
	if initialJobID == "" {
		initialJobID = "noway-" + uuid.New().String()
	}

	return submitAndPollJob(
		ctx,
		initialJobID,
		func(retryCtx context.Context, op func() error) error {
			return db.retryWithLimit(retryCtx, maxRetries, op)
		},
		func(submitCtx context.Context, jobID string) (*bigquery.Job, error) {
			q.JobIDConfig.JobID = jobID
			job, err := q.Run(submitCtx)
			if err != nil {
				errLower := strings.ToLower(err.Error())
				if strings.Contains(errLower, "already exists") || strings.Contains(errLower, "duplicate") || strings.Contains(err.Error(), "409") {
					if existingJob, getErr := db.recoverExistingJob(submitCtx, q, jobID, err); getErr == nil {
						return existingJob, nil
					}
				}
				return nil, err
			}
			if job != nil {
				db.recordLocation(job.Location())
			}
			return job, nil
		},
		db.waitForJob,
		db.hasJobPartiallyExecuted,
	)
}

func (db *BigQueryDatabase) recoverExistingJob(ctx context.Context, q *bigquery.Query, jobID string, submitErr error) (*bigquery.Job, error) {
	if db.client == nil {
		return nil, fmt.Errorf("bigquery client is nil")
	}

	projectID := db.projectID
	if q != nil && q.JobIDConfig.ProjectID != "" {
		projectID = q.JobIDConfig.ProjectID
	}

	loc := ""
	if q != nil && q.JobIDConfig.Location != "" {
		loc = q.JobIDConfig.Location
	}
	if loc == "" {
		loc = db.getLocation()
	}
	if loc == "" && db.client.Location != "" {
		loc = db.client.Location
	}
	if loc == "" {
		loc = extractJobLocationFromError(submitErr, jobID)
		if loc != "" {
			db.recordLocation(loc)
		}
	}
	if loc == "" {
		loc = db.resolveDatasetLocation(ctx, q)
	}

	if loc != "" {
		if existingJob, err := db.client.JobFromProject(ctx, projectID, jobID, loc); err == nil {
			db.recordLocation(existingJob.Location())
			return existingJob, nil
		}
	}

	// Fallback: list jobs in the project (which does not require a location parameter and returns
	// jobs across all regions, including outside US and EU) to discover the job and its location.
	it := db.client.Jobs(ctx)
	if projectID != "" {
		it.ProjectID = projectID
	}
	for i := 0; i < 100; i++ {
		j, iterErr := it.Next()
		if iterErr == iterator.Done {
			break
		}
		if iterErr != nil {
			break
		}
		if j != nil && j.ID() == jobID {
			if j.Location() != "" {
				db.recordLocation(j.Location())
				if existingJob, getErr := db.client.JobFromProject(ctx, projectID, jobID, j.Location()); getErr == nil {
					return existingJob, nil
				}
			}
			return j, nil
		}
	}

	return db.client.JobFromProject(ctx, projectID, jobID, loc)
}

func (db *BigQueryDatabase) resolveDatasetLocation(ctx context.Context, q *bigquery.Query) string {
	if db.client == nil {
		return ""
	}

	var candidates []string
	seen := make(map[string]bool)
	addCandidate := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			candidates = append(candidates, s)
		}
	}

	if q != nil && q.DefaultDatasetID != "" {
		addCandidate(q.DefaultDatasetID)
	}
	if db.config != nil {
		addCandidate(db.config.GetDefaultSchema())
		for _, s := range db.config.Schemas {
			addCandidate(s)
		}
	}

	for _, schema := range candidates {
		meta, err := db.client.Dataset(schema).Metadata(ctx)
		if err == nil && meta != nil && meta.Location != "" {
			db.recordLocation(meta.Location)
			return meta.Location
		}
	}

	return ""
}

// extractJobLocationFromError extracts the BigQuery location from a 409 duplicate job error message
// when formatted as "Already Exists: Job <project>:<location>.<jobID>".
func extractJobLocationFromError(err error, jobID string) string {
	if err == nil || jobID == "" {
		return ""
	}
	errStr := err.Error()
	idx := strings.Index(errStr, "."+jobID)
	if idx <= 0 {
		return ""
	}
	prefix := errStr[:idx]
	colonIdx := strings.LastIndex(prefix, ":")
	if colonIdx < 0 || colonIdx+1 >= len(prefix) {
		return ""
	}
	loc := strings.TrimSpace(prefix[colonIdx+1:])
	if loc == "" || strings.ContainsAny(loc, " \t\n\r/\"'") {
		return ""
	}
	return loc
}

type nonRetryableError struct {
	err error
}

func (e *nonRetryableError) Error() string {
	return e.err.Error()
}

func (e *nonRetryableError) Unwrap() error {
	return e.err
}

// submitAndPollJob submits a job and polls for its completion.
//   - Transient submission errors retry with the same job ID.
//   - Status polling errors (waitForJob) never resubmit the job.
//   - Completed jobs with a retryable status.Err() (e.g. rateLimitExceeded or backendError)
//     retry with a newly generated job ID ONLY if the job has not partially executed.
//     If the job has partially executed (e.g. multi-statement script jobs where prior statements
//     succeeded and committed, or DML mutated rows), retry is refused to prevent duplicate execution.
func submitAndPollJob(
	ctx context.Context,
	initialJobID string,
	retryOp func(context.Context, func() error) error,
	submitJob func(ctx context.Context, jobID string) (*bigquery.Job, error),
	waitForJob func(context.Context, *bigquery.Job) (*bigquery.JobStatus, error),
	isPartiallyExecuted func(context.Context, *bigquery.Job, *bigquery.JobStatus) bool,
) (*bigquery.JobStatus, error) {
	currentJobID := initialJobID
	if currentJobID == "" {
		currentJobID = "noway-" + uuid.New().String()
	}

	var status *bigquery.JobStatus
	err := retryOp(ctx, func() error {
		job, submitErr := submitJob(ctx, currentJobID)
		if submitErr != nil {
			return fmt.Errorf("failed to run query: %w", submitErr)
		}

		var waitErr error
		status, waitErr = waitForJob(ctx, job)
		if waitErr != nil {
			return &nonRetryableError{err: fmt.Errorf("query execution wait failed: %w", waitErr)}
		}

		if jobErr := status.Err(); jobErr != nil {
			execErr := fmt.Errorf("query execution failed: %w", jobErr)
			partiallyExecuted := defaultIsPartiallyExecuted(status)
			if !partiallyExecuted && isPartiallyExecuted != nil {
				partiallyExecuted = isPartiallyExecuted(ctx, job, status)
			} else if !partiallyExecuted && isPartiallyExecuted == nil && isScriptJob(status) {
				// Safer rule: If the code cannot read the child jobs of a script job, do not retry.
				partiallyExecuted = true
			}
			if !partiallyExecuted && isRetryableBigQueryError(jobErr) {
				currentJobID = "noway-" + uuid.New().String()
				return execErr
			}
			return &nonRetryableError{err: execErr}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return status, nil
}

// isScriptJob reports whether a job status indicates the job was a multi-statement script.
func isScriptJob(status *bigquery.JobStatus) bool {
	if status == nil || status.Statistics == nil {
		return false
	}
	if status.Statistics.NumChildJobs > 0 || status.Statistics.ScriptStatistics != nil {
		return true
	}
	if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok && qs.StatementType == "SCRIPT" {
		return true
	}
	return false
}

// hasJobPartiallyExecuted checks whether a BigQuery job (especially a script / multi-statement query)
// has already executed any part of its workload (e.g. one or more child statements completed,
// or rows were mutated). If so, retrying the entire job from the beginning is unsafe because
// already-committed child statements or partial mutations would run a second time.
//
// Safer rule: If the code cannot read the child jobs of a script job, it fails closed (returns true,
// preventing retry).
func (db *BigQueryDatabase) hasJobPartiallyExecuted(ctx context.Context, job *bigquery.Job, status *bigquery.JobStatus) bool {
	if status == nil {
		return false
	}
	if defaultIsPartiallyExecuted(status) {
		return true
	}

	if isScriptJob(status) {
		// Safer rule: If the code cannot read the child jobs of a script job, do not retry.
		if db == nil || db.client == nil || job == nil {
			return true
		}

		it := db.client.Jobs(ctx)
		it.ParentJobID = job.ID()
		if job.ProjectID() != "" {
			it.ProjectID = job.ProjectID()
		} else if db.projectID != "" {
			it.ProjectID = db.projectID
		}

		inspectedCount := 0
		for {
			if ctx.Err() != nil {
				return true // Context canceled/timed out during inspection -> fail closed
			}
			childJob, err := it.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				// Failed to list/read child jobs -> fail closed
				return true
			}
			inspectedCount++
			childStatus, err := childJob.Status(ctx)
			if err != nil || childStatus == nil {
				// Failed to read child job status -> fail closed
				return true
			}
			if childStatus.Done() && childStatus.Err() == nil {
				return true
			}
			if childStatus.Statistics != nil {
				if qs, ok := childStatus.Statistics.Details.(*bigquery.QueryStatistics); ok {
					if qs.NumDMLAffectedRows > 0 {
						return true
					}
					if qs.DMLStats != nil && (qs.DMLStats.InsertedRowCount > 0 || qs.DMLStats.UpdatedRowCount > 0 || qs.DMLStats.DeletedRowCount > 0) {
						return true
					}
					if qs.DDLTargetTable != nil || qs.DDLOperationPerformed != "" || qs.DDLTargetRoutine != nil {
						return true
					}
				}
			}
		}

		if inspectedCount == 0 && status.Statistics.NumChildJobs > 0 {
			// NumChildJobs indicated child jobs existed, but we were unable to inspect any -> fail closed
			return true
		}
	}

	return false
}

func defaultIsPartiallyExecuted(status *bigquery.JobStatus) bool {
	if status == nil || status.Statistics == nil {
		return false
	}
	if status.Statistics.NumChildJobs > 1 {
		return true
	}
	if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok {
		if qs.NumDMLAffectedRows > 0 {
			return true
		}
		if qs.DMLStats != nil && (qs.DMLStats.InsertedRowCount > 0 || qs.DMLStats.UpdatedRowCount > 0 || qs.DMLStats.DeletedRowCount > 0) {
			return true
		}
		if qs.DDLTargetTable != nil || qs.DDLOperationPerformed != "" || qs.DDLTargetRoutine != nil {
			return true
		}
	}
	return false
}

// waitForJob polls job.Status until completion, with backoff and retry on transient status errors.
func (db *BigQueryDatabase) waitForJob(ctx context.Context, job *bigquery.Job) (*bigquery.JobStatus, error) {
	initialBackoff := 500 * time.Millisecond
	if db.retryInitialBackoff > 0 {
		initialBackoff = db.retryInitialBackoff
	}
	maxBackoff := 5 * time.Second
	if db.retryMaxBackoff > 0 {
		maxBackoff = db.retryMaxBackoff
	}

	return pollJobStatus(ctx, initialBackoff, maxBackoff, job.Status)
}

func pollJobStatus(
	ctx context.Context,
	initialBackoff time.Duration,
	maxBackoff time.Duration,
	getStatus func(context.Context) (*bigquery.JobStatus, error),
) (*bigquery.JobStatus, error) {
	backoff := initialBackoff
	maxStatusRetries := 10
	statusRetries := 0
	var lastStatusErr error

	for {
		status, err := getStatus(ctx)
		if err != nil {
			if isRetryableBigQueryError(err) {
				statusRetries++
				lastStatusErr = err
				if statusRetries > maxStatusRetries {
					return nil, fmt.Errorf("failed to get job status after %d retries: %w", maxStatusRetries, lastStatusErr)
				}
				jitterFactor := 0.8 + 0.4*rand.Float64()
				sleepDuration := time.Duration(float64(backoff) * jitterFactor)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(sleepDuration):
				}
				backoff = min(backoff*2, maxBackoff)
				continue
			}
			return nil, err
		}

		// Reset status retries on successful call
		statusRetries = 0

		if status.Done() {
			return status, nil
		}

		// Job is running / pending, wait before next poll
		jitterFactor := 0.8 + 0.4*rand.Float64()
		sleepDuration := time.Duration(float64(backoff) * jitterFactor)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleepDuration):
		}

		backoff = min(backoff*2, maxBackoff)
	}
}

func (db *BigQueryDatabase) retry(ctx context.Context, op func() error) error {
	maxRetries := 5
	if db.config != nil && db.config.ConnectRetries > 0 {
		maxRetries = db.config.ConnectRetries
	}
	return db.retryWithLimit(ctx, maxRetries, op)
}

func (db *BigQueryDatabase) retryWithLimit(ctx context.Context, maxRetries int, op func() error) error {
	if maxRetries <= 0 {
		return op()
	}

	initialBackoff := 2 * time.Second
	if db.retryInitialBackoff > 0 {
		initialBackoff = db.retryInitialBackoff
	}

	maxBackoff := 30 * time.Second
	if db.retryMaxBackoff > 0 {
		maxBackoff = db.retryMaxBackoff
	}

	backoff := initialBackoff
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Calculate jittered backoff: ±20%
			jitterFactor := 0.8 + 0.4*rand.Float64()
			sleepDuration := time.Duration(float64(backoff) * jitterFactor)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleepDuration):
			}

			backoff = time.Duration(float64(backoff) * 2.0)
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		err := op()
		if err == nil {
			return nil
		}

		if isRetryableBigQueryError(err) {
			lastErr = err
			continue
		}

		var nonRetryable *nonRetryableError
		if errors.As(err, &nonRetryable) {
			return nonRetryable.err
		}
		return err
	}

	return fmt.Errorf("operation failed after %d retries: %w", maxRetries, lastErr)
}

func isRetryableBigQueryError(err error) bool {
	if err == nil {
		return false
	}

	var nonRetryable *nonRetryableError
	if errors.As(err, &nonRetryable) {
		return false
	}

	// Direct check for googleapi.Error
	var gErr *googleapi.Error
	if errors.As(err, &gErr) {
		if gErr.Code == 429 || gErr.Code == 500 || gErr.Code == 502 || gErr.Code == 503 || gErr.Code == 504 {
			return true
		}
		for _, item := range gErr.Errors {
			if isRetryableReason(item.Reason) {
				return true
			}
		}
	}

	// Direct check for bigquery.Error
	var bqErr *bigquery.Error
	if errors.As(err, &bqErr) {
		if isRetryableReason(bqErr.Reason) {
			return true
		}
	}

	// Fallback string matching on error message
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "ratelimitexceeded") ||
		strings.Contains(errStr, "jobratelimitexceeded") ||
		strings.Contains(errStr, "exceeded rate limits") ||
		strings.Contains(errStr, "too many metadata update operations") ||
		strings.Contains(errStr, "backenderror") ||
		strings.Contains(errStr, "internalerror") ||
		strings.Contains(errStr, "concurrentmodification") ||
		strings.Contains(errStr, "status code 429") ||
		strings.Contains(errStr, "status code 503") ||
		strings.Contains(errStr, "status code 500") ||
		strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "broken pipe") {
		return true
	}

	return false
}

func isRetryableReason(reason string) bool {
	r := strings.ToLower(reason)
	return r == "ratelimitexceeded" ||
		r == "jobratelimitexceeded" ||
		r == "backenderror" ||
		r == "internalerror" ||
		r == "concurrentmodification" ||
		r == "rate_limit_exceeded"
}
