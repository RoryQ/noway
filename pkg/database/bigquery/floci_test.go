package bigquery_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/roryq/noway/pkg/config"
	driver "github.com/roryq/noway/pkg/database/bigquery"
	"github.com/roryq/noway/pkg/migrator"
)

func getFlociEndpoint() string {
	endpoint := os.Getenv("FLOCI_BIGQUERY_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("BIGQUERY_EMULATOR_HOST")
	}
	if endpoint == "" {
		endpoint = "http://localhost:4588/bigquery/v2/"
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	if !strings.HasSuffix(endpoint, "/") {
		endpoint += "/"
	}
	if !strings.Contains(endpoint, "/bigquery/v2/") {
		endpoint += "bigquery/v2/"
	}
	return endpoint
}

func isFlociAvailable(endpoint string) bool {
	baseURL := strings.TrimSuffix(endpoint, "bigquery/v2/")
	healthURL := baseURL + "health"
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(healthURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// TestFlociGCPIntegration validates noway's BigQuery operations against floci-gcp emulator.
func TestFlociGCPIntegration(t *testing.T) {
	endpoint := getFlociEndpoint()
	if !isFlociAvailable(endpoint) {
		t.Skip("floci-gcp emulator is not running; skipping floci integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	projectID := "floci-local"
	datasetName := "floci_noway_test"
	tableName := "flyway_schema_history"

	cfg := config.NewDefaultConfiguration()
	cfg.GCPProjectID = projectID
	cfg.GCPBigQueryEndpoint = endpoint
	cfg.DefaultSchema = datasetName
	cfg.Schemas = []string{datasetName}
	cfg.Table = tableName
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("config finalize failed: %v", err)
	}

	db, err := driver.New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to initialize BigQuery driver for floci-gcp: %v", err)
	}
	defer db.Close()

	t.Run("DatasetLifecycle", func(t *testing.T) {
		// 1. Ensure Schema
		if err := db.EnsureSchema(ctx, datasetName); err != nil {
			t.Fatalf("EnsureSchema failed: %v", err)
		}

		// 2. Schema Exists Check
		exists, err := db.SchemaExists(ctx, datasetName)
		if err != nil {
			t.Fatalf("SchemaExists error: %v", err)
		}
		if !exists {
			t.Fatalf("expected dataset %s to exist", datasetName)
		}

		// 3. Re-running EnsureSchema is idempotent
		if err := db.EnsureSchema(ctx, datasetName); err != nil {
			t.Fatalf("EnsureSchema idempotency check failed: %v", err)
		}
	})

	t.Run("HistoryTableLifecycle", func(t *testing.T) {
		// 1. Ensure History Table
		if err := db.EnsureHistoryTable(ctx, datasetName, tableName); err != nil {
			t.Fatalf("EnsureHistoryTable failed: %v", err)
		}

		// 2. History Table Exists Check
		exists, err := db.HistoryTableExists(ctx, datasetName, tableName)
		if err != nil {
			t.Fatalf("HistoryTableExists error: %v", err)
		}
		if !exists {
			t.Fatalf("expected history table %s.%s to exist", datasetName, tableName)
		}

		// 3. Re-running EnsureHistoryTable is idempotent
		if err := db.EnsureHistoryTable(ctx, datasetName, tableName); err != nil {
			t.Fatalf("EnsureHistoryTable idempotency check failed: %v", err)
		}
	})

	t.Run("StreamingInsertAndQuery", func(t *testing.T) {
		client, err := bigquery.NewClient(
			ctx,
			projectID,
			option.WithEndpoint(endpoint),
			option.WithoutAuthentication(),
		)
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}
		defer client.Close()

		tbl := client.Dataset(datasetName).Table(tableName)
		inserter := tbl.Inserter()

		type TestHistoryRow struct {
			InstalledRank int64                  `bigquery:"installed_rank"`
			Version       bigquery.NullString    `bigquery:"version"`
			Description   string                 `bigquery:"description"`
			Type          string                 `bigquery:"type"`
			Script        string                 `bigquery:"script"`
			Checksum      bigquery.NullInt64     `bigquery:"checksum"`
			InstalledBy   string                 `bigquery:"installed_by"`
			InstalledOn   bigquery.NullTimestamp `bigquery:"installed_on"`
			ExecutionTime int64                  `bigquery:"execution_time"`
			Success       bool                   `bigquery:"success"`
		}

		row := TestHistoryRow{
			InstalledRank: 1,
			Version:       bigquery.NullString{StringVal: "1.0", Valid: true},
			Description:   "initial schema",
			Type:          "SQL",
			Script:        "V1.0__initial_schema.sql",
			Checksum:      bigquery.NullInt64{Int64: 424242, Valid: true},
			InstalledBy:   "floci_tester",
			InstalledOn:   bigquery.NullTimestamp{Timestamp: time.Now().UTC(), Valid: true},
			ExecutionTime: 35,
			Success:       true,
		}

		if err := inserter.Put(ctx, []*TestHistoryRow{&row}); err != nil {
			t.Fatalf("inserter.Put failed on floci-gcp: %v", err)
		}

		// Verify row presence via SELECT query
		sql := "SELECT installed_rank, version, description, type, script, checksum, installed_by, execution_time, success FROM " + db.Quote(datasetName, tableName)
		q := client.Query(sql)
		it, err := q.Read(ctx)
		if err != nil {
			t.Fatalf("query read failed on floci-gcp: %v", err)
		}

		type QueryResultRow struct {
			InstalledRank int64               `bigquery:"installed_rank"`
			Version       bigquery.NullString `bigquery:"version"`
			Description   string              `bigquery:"description"`
			Type          string              `bigquery:"type"`
			Script        string              `bigquery:"script"`
			Checksum      bigquery.NullInt64  `bigquery:"checksum"`
			InstalledBy   string              `bigquery:"installed_by"`
			ExecutionTime int64               `bigquery:"execution_time"`
			Success       bool                `bigquery:"success"`
		}

		var count int
		for {
			var r QueryResultRow
			err := it.Next(&r)
			if err == iterator.Done {
				break
			}
			if err != nil {
				t.Fatalf("it.Next failed: %v", err)
			}
			count++
			if r.InstalledRank != 1 || r.Version.StringVal != "1.0" || r.Description != "initial schema" || r.Success != true {
				t.Errorf("unexpected row data retrieved from floci-gcp: %+v", r)
			}
		}

		if count == 0 {
			t.Fatalf("expected at least 1 history record from floci-gcp, got 0")
		}
	})
}

// TestFlociRepeatableMigrationRename tests the full lifecycle of renaming a repeatable migration against floci-gcp.
func TestFlociRepeatableMigrationRename(t *testing.T) {
	endpoint := getFlociEndpoint()
	if !isFlociAvailable(endpoint) {
		t.Skip("floci-gcp emulator is not running; skipping floci integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	projectID := "floci-local"
	datasetName := "floci_repeatable_test"
	tableName := "flyway_schema_history"

	fs := fstest.MapFS{
		"migrations/V1__init.sql":       &fstest.MapFile{Data: []byte("CREATE TABLE IF NOT EXISTS `" + projectID + "." + datasetName + ".users` (id INT64, name STRING);")},
		"migrations/R__user_view.sql":   &fstest.MapFile{Data: []byte("CREATE VIEW IF NOT EXISTS `" + projectID + "." + datasetName + ".user_view` AS SELECT id, name FROM `" + projectID + "." + datasetName + ".users`;")},
		"migrations/.keep":              &fstest.MapFile{Data: []byte("")},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.GCPProjectID = projectID
	cfg.GCPBigQueryEndpoint = endpoint
	cfg.DefaultSchema = datasetName
	cfg.Schemas = []string{datasetName}
	cfg.Table = tableName
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("config finalize failed: %v", err)
	}

	db, err := driver.New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to initialize BigQuery driver for floci-gcp: %v", err)
	}
	defer db.Close()

	// Clean dataset beforehand if present
	_ = db.DropSchema(ctx, datasetName)

	m, err := migrator.New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	// 1. Initial Migrate: applies V1 and R__user_view
	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("initial Migrate failed: %v", err)
	}
	if res.MigrationsExecuted != 2 {
		t.Fatalf("expected 2 migrations executed, got %d", res.MigrationsExecuted)
	}

	// 2. Validate passes on current state
	valRes, err := m.Validate(ctx)
	if err != nil || !valRes.Valid {
		t.Fatalf("initial Validate failed: %v, errors: %s", err, valRes.Error())
	}

	// 3. Rename repeatable migration: delete R__user_view.sql, add R__customer_view.sql
	delete(fs, "migrations/R__user_view.sql")
	fs["migrations/R__customer_view.sql"] = &fstest.MapFile{
		Data: []byte("CREATE VIEW IF NOT EXISTS `" + projectID + "." + datasetName + ".customer_view` AS SELECT id, name FROM `" + projectID + "." + datasetName + ".users`;"),
	}

	// 4. Validate MUST PASS per Flyway parity (missing/renamed repeatable is treated as DELETED, not error)
	m2, _ := migrator.New(cfg, db)
	valRes2, err := m2.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate after rename returned error: %v", err)
	}
	if !valRes2.Valid {
		t.Fatalf("expected Validate to pass after repeatable migration rename (Flyway parity), got: %s", valRes2.Error())
	}

	// 5. Migrate applies R__customer_view cleanly
	migRes2, err := m2.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate after rename failed: %v", err)
	}
	if migRes2.MigrationsExecuted != 1 {
		t.Fatalf("expected 1 migration executed (R__customer_view), got %d", migRes2.MigrationsExecuted)
	}

	// 6. Verify history has 3 records: V1, R__user_view, R__customer_view
	history, err := db.FetchHistory(ctx, datasetName, tableName)
	if err != nil {
		t.Fatalf("FetchHistory failed: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history records, got %d", len(history))
	}

	// 7. Repair marks R__user_view as DELETE
	repRes, err := m2.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if len(repRes.DeletedMigrations) != 1 || repRes.DeletedMigrations[0] != "R__user_view.sql" {
		t.Fatalf("expected R__user_view.sql marked as DELETE by repair, got: %+v", repRes.DeletedMigrations)
	}

	// 8. Validate continues to pass
	valRes3, err := m2.Validate(ctx)
	if err != nil || !valRes3.Valid {
		t.Fatalf("Validate after repair failed: %v, errors: %s", err, valRes3.Error())
	}

	// Cleanup
	_ = db.DropSchema(ctx, datasetName)
}

// TestFlociValidationParity validates Flyway validation parity features against floci-gcp.
func TestFlociValidationParity(t *testing.T) {
	endpoint := getFlociEndpoint()
	if !isFlociAvailable(endpoint) {
		t.Skip("floci-gcp emulator is not running; skipping floci integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	projectID := "floci-local"
	datasetName := "floci_val_parity_test"
	tableName := "flyway_schema_history"

	baseCfg := func(fs fstest.MapFS) *config.Configuration {
		cfg := config.NewDefaultConfiguration()
		cfg.GCPProjectID = projectID
		cfg.GCPBigQueryEndpoint = endpoint
		cfg.DefaultSchema = datasetName
		cfg.Schemas = []string{datasetName}
		cfg.Table = tableName
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		_ = cfg.Finalize()
		return cfg
	}

	cfgInitial := baseCfg(nil)
	db, err := driver.New(ctx, cfgInitial)
	if err != nil {
		t.Fatalf("failed to initialize BigQuery driver for floci-gcp: %v", err)
	}
	defer db.Close()

	// Clean dataset before starting
	_ = db.DropSchema(ctx, datasetName)
	if err := db.EnsureSchema(ctx, datasetName); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}
	defer func() {
		_ = db.DropSchema(ctx, datasetName)
	}()

	v1SQL := "CREATE TABLE IF NOT EXISTS `" + projectID + "." + datasetName + ".t1` (id INT64);"
	v2SQL := "CREATE TABLE IF NOT EXISTS `" + projectID + "." + datasetName + ".t2` (id INT64);"

	// -------------------------------------------------------------------------
	// 1. Non-empty dataset without history table check
	// -------------------------------------------------------------------------
	t.Run("NonEmptyDatasetWithoutHistoryTable", func(t *testing.T) {
		// Create an unmanaged table directly in the dataset
		unmanagedTableSQL := "CREATE TABLE IF NOT EXISTS `" + projectID + "." + datasetName + ".unmanaged_table` (id INT64);"
		if err := db.ExecuteStatement(ctx, unmanagedTableSQL); err != nil {
			t.Fatalf("failed creating unmanaged table: %v", err)
		}

		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte(v1SQL)},
			"migrations/.keep":        &fstest.MapFile{Data: []byte("")},
		}

		// Validate with BaselineOnMigrate=false -> Fails
		cfgNoBaseline := baseCfg(fs)
		cfgNoBaseline.BaselineOnMigrate = false
		m1, _ := migrator.New(cfgNoBaseline, db)
		valRes1, err := m1.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if valRes1.Valid {
			t.Fatalf("expected Validate to fail on non-empty dataset without schema history table")
		}
		if !strings.Contains(valRes1.Error(), "Found non-empty schema(s)") {
			t.Errorf("expected non-empty schema error message, got: %s", valRes1.Error())
		}

		// Validate with BaselineOnMigrate=true -> Passes
		cfgWithBaseline := baseCfg(fs)
		cfgWithBaseline.BaselineOnMigrate = true
		m2, _ := migrator.New(cfgWithBaseline, db)
		valRes2, err := m2.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valRes2.Valid {
			t.Fatalf("expected Validate to pass with BaselineOnMigrate=true, got: %s", valRes2.Error())
		}
	})

	// -------------------------------------------------------------------------
	// 2. Setup History: Apply V1 and V2
	// -------------------------------------------------------------------------
	fsBoth := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{Data: []byte(v1SQL)},
		"migrations/V2__t2.sql":   &fstest.MapFile{Data: []byte(v2SQL)},
		"migrations/.keep":        &fstest.MapFile{Data: []byte("")},
	}
	cfgBoth := baseCfg(fsBoth)
	cfgBoth.BaselineOnMigrate = true
	mMigrate, err := migrator.New(cfgBoth, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	migRes, err := mMigrate.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate V1+V2 failed: %v", err)
	}
	if migRes.MigrationsExecuted != 2 {
		t.Fatalf("expected 2 migrations executed, got %d", migRes.MigrationsExecuted)
	}

	// -------------------------------------------------------------------------
	// 3. IgnoreMissingMigrations Parity
	// -------------------------------------------------------------------------
	t.Run("IgnoreMissingMigrations", func(t *testing.T) {
		// Local filesystem only has V2 (V1 is missing from local files)
		fsOnlyV2 := fstest.MapFS{
			"migrations/V2__t2.sql": &fstest.MapFile{Data: []byte(v2SQL)},
			"migrations/.keep":      &fstest.MapFile{Data: []byte("")},
		}

		// Default IgnoreMissingMigrations=false -> Fails
		cfgDefault := baseCfg(fsOnlyV2)
		mDefault, _ := migrator.New(cfgDefault, db)
		valResDefault, err := mDefault.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if valResDefault.Valid {
			t.Fatalf("expected Validate to fail when applied V1 is missing locally")
		}
		if !strings.Contains(valResDefault.Error(), "Detected applied migration not resolved locally: V1__init.sql") {
			t.Errorf("unexpected error message: %s", valResDefault.Error())
		}

		// IgnoreMissingMigrations=true -> Passes
		cfgIgnoreMissing := baseCfg(fsOnlyV2)
		cfgIgnoreMissing.IgnoreMissingMigrations = true
		mIgnore, _ := migrator.New(cfgIgnoreMissing, db)
		valResIgnore, err := mIgnore.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valResIgnore.Valid {
			t.Fatalf("expected Validate to pass with IgnoreMissingMigrations=true, got: %s", valResIgnore.Error())
		}
	})

	// -------------------------------------------------------------------------
	// 4. IgnoreFutureMigrations Parity
	// -------------------------------------------------------------------------
	t.Run("IgnoreFutureMigrations", func(t *testing.T) {
		// Local filesystem only has V1 (V2 in database is a future migration)
		fsOnlyV1 := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte(v1SQL)},
			"migrations/.keep":        &fstest.MapFile{Data: []byte("")},
		}

		// Default IgnoreFutureMigrations=true -> Passes
		cfgDefault := baseCfg(fsOnlyV1)
		mDefault, _ := migrator.New(cfgDefault, db)
		valResDefault, err := mDefault.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if !valResDefault.Valid {
			t.Fatalf("expected Validate to pass with IgnoreFutureMigrations=true (default), got: %s", valResDefault.Error())
		}

		// IgnoreFutureMigrations=false -> Fails
		cfgStrict := baseCfg(fsOnlyV1)
		cfgStrict.IgnoreFutureMigrations = false
		mStrict, _ := migrator.New(cfgStrict, db)
		valResStrict, err := mStrict.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate error: %v", err)
		}
		if valResStrict.Valid {
			t.Fatalf("expected Validate to fail with IgnoreFutureMigrations=false")
		}
		if !strings.Contains(valResStrict.Error(), "(future)") {
			t.Errorf("expected (future) error message, got: %s", valResStrict.Error())
		}
	})
}

