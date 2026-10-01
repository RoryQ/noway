package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseJDBCBigQueryURL(t *testing.T) {
	url := "jdbc:bigquery://https://www.googleapis.com/bigquery/v2:443;ProjectId=my-gcp-project;OAuthType=0;OAuthServiceAcctEmail=sa@my-gcp-project.iam.gserviceaccount.com;OAuthPKeyFile=/path/to/key.json;DefaultDataset=analytics_dataset;Location=australia-southeast1;Timeout=30;"

	params, err := ParseJDBCBigQueryURL(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if params.ProjectID != "my-gcp-project" {
		t.Errorf("expected ProjectID 'my-gcp-project', got %q", params.ProjectID)
	}
	if params.DefaultDataset != "analytics_dataset" {
		t.Errorf("expected DefaultDataset 'analytics_dataset', got %q", params.DefaultDataset)
	}
	if params.Location != "australia-southeast1" {
		t.Errorf("expected Location 'australia-southeast1', got %q", params.Location)
	}
	if params.OAuthType != 0 {
		t.Errorf("expected OAuthType 0, got %d", params.OAuthType)
	}
	if params.ServiceAccountFile != "/path/to/key.json" {
		t.Errorf("expected ServiceAccountFile '/path/to/key.json', got %q", params.ServiceAccountFile)
	}
	if params.TimeoutSeconds != 30 {
		t.Errorf("expected Timeout 30, got %d", params.TimeoutSeconds)
	}
}

func TestFlywayConfPropertiesFile(t *testing.T) {
	confContent := `
# Flyway Configuration File
flyway.url=jdbc:bigquery:;ProjectId=test-proj;DefaultDataset=test_ds;
flyway.schemas=test_ds,secondary_ds
flyway.table=custom_schema_history
flyway.locations=filesystem:./custom_sql,filesystem:./more_sql
flyway.baselineOnMigrate=true
flyway.baselineVersion=2.0
flyway.outOfOrder=true
flyway.placeholders.env=production
flyway.placeholders.retention_days=90
`
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "flyway.conf")
	if err := os.WriteFile(confPath, []byte(confContent), 0644); err != nil {
		t.Fatalf("failed to write tmp conf: %v", err)
	}

	cfg, err := LoadFromFile(confPath)
	if err != nil {
		t.Fatalf("LoadFromFile error: %v", err)
	}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}

	if cfg.GCPProjectID != "test-proj" {
		t.Errorf("expected GCPProjectID 'test-proj', got %q", cfg.GCPProjectID)
	}
	if cfg.Table != "custom_schema_history" {
		t.Errorf("expected Table 'custom_schema_history', got %q", cfg.Table)
	}
	if len(cfg.Schemas) != 2 || cfg.Schemas[0] != "test_ds" {
		t.Errorf("unexpected Schemas: %v", cfg.Schemas)
	}
	if !cfg.BaselineOnMigrate {
		t.Errorf("expected baselineOnMigrate true")
	}
	if !cfg.OutOfOrder {
		t.Errorf("expected outOfOrder true")
	}
	if cfg.Placeholders["env"] != "production" || cfg.Placeholders["retention_days"] != "90" {
		t.Errorf("unexpected placeholders: %v", cfg.Placeholders)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("FLYWAY_URL", "jdbc:bigquery:;ProjectId=env-project;DefaultDataset=env_ds;")
	t.Setenv("FLYWAY_OUT_OF_ORDER", "true")
	t.Setenv("FLYWAY_PLACEHOLDERS_TIER", "premium")

	cfg := NewDefaultConfiguration()
	LoadFromEnv(cfg)
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}

	if cfg.GCPProjectID != "env-project" {
		t.Errorf("expected GCPProjectID 'env-project', got %q", cfg.GCPProjectID)
	}
	if !cfg.OutOfOrder {
		t.Errorf("expected OutOfOrder true from env")
	}
	if cfg.Placeholders["TIER"] != "premium" {
		t.Errorf("expected placeholder TIER=premium, got %v", cfg.Placeholders)
	}
}
