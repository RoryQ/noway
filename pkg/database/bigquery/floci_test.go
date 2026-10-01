package bigquery_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/RoryQ/noway/pkg/config"
	driver "github.com/RoryQ/noway/pkg/database/bigquery"
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
