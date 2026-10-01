package noway

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database/mock"
)

func TestNowayEndToEnd(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64, email STRING);"),
		},
		"migrations/V2__add_index.sql": &fstest.MapFile{
			Data: []byte("ALTER TABLE users ADD COLUMN created_at TIMESTAMP;"),
		},
		"migrations/R__reporting_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW users_report AS SELECT id, email FROM users;"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "analytics"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}

	nw, err := New(
		WithConfig(cfg),
		WithDatabase(db),
	)
	if err != nil {
		t.Fatalf("failed to create noway: %v", err)
	}
	defer nw.Close()

	ctx := context.Background()

	// 1. Info before migrate
	infoBefore, err := nw.Info(ctx)
	if err != nil {
		t.Fatalf("Info before migrate error: %v", err)
	}
	if len(infoBefore.Migrations) != 3 {
		t.Errorf("expected 3 pending migrations in info, got %d", len(infoBefore.Migrations))
	}

	// 2. Migrate
	migRes, err := nw.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate error: %v", err)
	}
	if migRes.MigrationsExecuted != 3 {
		t.Errorf("expected 3 migrations applied, got %d", migRes.MigrationsExecuted)
	}

	// 3. Validate
	valRes, err := nw.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if !valRes.Valid {
		t.Errorf("expected Validate to pass, got errors: %v", valRes.Errors)
	}

	// 4. Info after migrate
	infoAfter, err := nw.Info(ctx)
	if err != nil {
		t.Fatalf("Info after migrate error: %v", err)
	}
	for _, m := range infoAfter.Migrations {
		if m.State != "Success" {
			t.Errorf("expected migration %s to be Success, got %s", m.Script, m.State)
		}
	}
}

func TestOptionsOverrideWithConfigFile(t *testing.T) {
	confContent := `
flyway.url=jdbc:bigquery:;ProjectId=file-proj;DefaultDataset=file_ds;
flyway.table=file_history
`
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "flyway.conf")
	if err := os.WriteFile(confPath, []byte(confContent), 0644); err != nil {
		t.Fatalf("failed to write conf: %v", err)
	}

	db := mock.NewMockDatabase()

	nw, err := New(
		WithConfigFile(confPath),
		WithProject("overridden-proj"), // Explicit modifier should override config file
		WithTable("custom_table"),
		WithDatabase(db),
	)
	if err != nil {
		t.Fatalf("failed to create noway: %v", err)
	}
	defer nw.Close()

	if nw.config.GCPProjectID != "overridden-proj" {
		t.Errorf("expected GCPProjectID 'overridden-proj', got %s", nw.config.GCPProjectID)
	}
	if nw.config.Table != "custom_table" {
		t.Errorf("expected Table 'custom_table', got %s", nw.config.Table)
	}
	if nw.config.DefaultSchema != "file_ds" {
		t.Errorf("expected DefaultSchema 'file_ds' preserved from file, got %s", nw.config.DefaultSchema)
	}
}

func TestNowayFlociIntegration(t *testing.T) {
	endpoint := os.Getenv("FLOCI_BIGQUERY_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("BIGQUERY_EMULATOR_HOST")
	}
	if endpoint == "" {
		endpoint = "http://localhost:4588/bigquery/v2/"
	}

	nw, err := New(
		WithProject("floci-local"),
		WithDataset("floci_noway_e2e"),
		WithEndpoint(endpoint),
		WithLocations("migrations"),
		WithFS(fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{
				Data: []byte("SELECT 1;"),
			},
		}),
	)
	if err != nil {
		t.Fatalf("failed to initialize noway with floci endpoint: %v", err)
	}
	defer nw.Close()

	if nw.config.GCPBigQueryEndpoint != endpoint {
		t.Errorf("expected endpoint %s, got %s", endpoint, nw.config.GCPBigQueryEndpoint)
	}
}
