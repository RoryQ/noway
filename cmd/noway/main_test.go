package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roryq/noway"
)

func TestParseCLIArgs(t *testing.T) {
	args := []string{
		"-url=jdbc:bigquery:;ProjectId=test-proj;DefaultDataset=analytics;",
		"-schemas=analytics, reporting",
		"-locations=filesystem:./sql, filesystem:./migrations",
		"-target=2.0",
		"-outOfOrder=true",
		"-placeholders.env=staging",
		"-placeholder.cluster=c1",
	}

	cfg, err := parseCLIArgs(args)
	if err != nil {
		t.Fatalf("parseCLIArgs error: %v", err)
	}

	if cfg.GCPProjectID != "test-proj" {
		t.Errorf("expected project 'test-proj', got %s", cfg.GCPProjectID)
	}
	if len(cfg.Schemas) != 2 || cfg.Schemas[1] != "reporting" {
		t.Errorf("expected trimmed schemas, got %v", cfg.Schemas)
	}
	if len(cfg.Locations) != 2 || cfg.Locations[1] != "filesystem:./migrations" {
		t.Errorf("expected trimmed locations, got %v", cfg.Locations)
	}
	if !cfg.OutOfOrder {
		t.Errorf("expected OutOfOrder true")
	}
	if cfg.Placeholders["env"] != "staging" {
		t.Errorf("expected placeholder env=staging, got %s", cfg.Placeholders["env"])
	}
	if cfg.Placeholders["cluster"] != "c1" {
		t.Errorf("expected placeholder cluster=c1, got %s", cfg.Placeholders["cluster"])
	}
}

func TestParseCLIArgs_OutputType(t *testing.T) {
	args := []string{
		"-url=jdbc:bigquery:;ProjectId=test-proj;DefaultDataset=analytics;",
		"-outputType=json",
	}

	cfg, err := parseCLIArgs(args)
	if err != nil {
		t.Fatalf("parseCLIArgs error: %v", err)
	}

	if cfg.OutputType != "json" {
		t.Errorf("expected OutputType 'json', got '%s'", cfg.OutputType)
	}
}

func TestParseCLIArgs_ConfigFilesMerge(t *testing.T) {
	tmpDir := t.TempDir()
	conf1 := tmpDir + "/conf1.properties"
	conf2 := tmpDir + "/conf2.toml"

	if err := os.WriteFile(conf1, []byte("flyway.url=jdbc:bigquery:;ProjectId=p1;DefaultDataset=d1;\nflyway.user=user1\n"), 0644); err != nil {
		t.Fatalf("failed to write conf1: %v", err)
	}
	if err := os.WriteFile(conf2, []byte("[environments.default]\nurl = \"jdbc:bigquery:;ProjectId=p2;DefaultDataset=d2;\"\n"), 0644); err != nil {
		t.Fatalf("failed to write conf2: %v", err)
	}

	args := []string{
		"-configFiles=" + conf1 + "," + conf2,
	}

	cfg, err := parseCLIArgs(args)
	if err != nil {
		t.Fatalf("parseCLIArgs failed: %v", err)
	}

	// conf2 overrides url, but preserves user from conf1
	if cfg.GCPProjectID != "p2" || cfg.GCPDataset != "d2" {
		t.Errorf("expected url from conf2 (p2/d2), got project=%s, dataset=%s", cfg.GCPProjectID, cfg.GCPDataset)
	}
	if cfg.User != "user1" {
		t.Errorf("expected user preserved from conf1 'user1', got %s", cfg.User)
	}
}

func TestParseCLIArgs_Environment(t *testing.T) {
	tmpDir := t.TempDir()
	tomlFile := tmpDir + "/flyway.toml"
	tomlContent := `
[environments.default]
url = "jdbc:bigquery:;ProjectId=dev-proj;DefaultDataset=dev_ds;"
user = "dev_user"

[environments.prod]
url = "jdbc:bigquery:;ProjectId=prod-proj;DefaultDataset=prod_ds;"
user = "prod_user"
`
	if err := os.WriteFile(tomlFile, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("failed to write toml: %v", err)
	}

	args := []string{
		"-configFile=" + tomlFile,
		"-environment=prod",
	}

	cfg, err := parseCLIArgs(args)
	if err != nil {
		t.Fatalf("parseCLIArgs failed: %v", err)
	}

	if cfg.GCPProjectID != "prod-proj" || cfg.GCPDataset != "prod_ds" {
		t.Errorf("expected prod environment url, got project=%s, dataset=%s", cfg.GCPProjectID, cfg.GCPDataset)
	}
	if cfg.User != "prod_user" {
		t.Errorf("expected prod user 'prod_user', got %s", cfg.User)
	}
}

func TestParseCLIArgs_ConfigFilesMultiFileEnvironmentSelection(t *testing.T) {
	tmpDir := t.TempDir()
	tomlFile := tmpDir + "/environments.toml"
	confFile := tmpDir + "/flyway.conf"

	tomlContent := `
[environments.staging]
url = "jdbc:bigquery:;ProjectId=staging-proj;DefaultDataset=staging_ds;"
user = "staging_user"

[environments.prod]
url = "jdbc:bigquery:;ProjectId=prod-proj;DefaultDataset=prod_ds;"
user = "prod_user"
`
	confContent := `
flyway.environment=staging
flyway.password=secret_staging
`

	if err := os.WriteFile(tomlFile, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("failed to write toml: %v", err)
	}
	if err := os.WriteFile(confFile, []byte(confContent), 0644); err != nil {
		t.Fatalf("failed to write conf: %v", err)
	}

	args := []string{
		"-configFiles=" + tomlFile + "," + confFile,
	}

	cfg, err := parseCLIArgs(args)
	if err != nil {
		t.Fatalf("parseCLIArgs failed: %v", err)
	}

	if cfg.Environment != "staging" {
		t.Errorf("expected staging environment, got '%s'", cfg.Environment)
	}
	if cfg.GCPProjectID != "staging-proj" || cfg.GCPDataset != "staging_ds" {
		t.Errorf("expected staging url, got %s/%s", cfg.GCPProjectID, cfg.GCPDataset)
	}
	if cfg.User != "staging_user" {
		t.Errorf("expected staging user 'staging_user', got %s", cfg.User)
	}
	if cfg.Password != "secret_staging" {
		t.Errorf("expected staging password 'secret_staging', got %s", cfg.Password)
	}
}

func TestParseCLIArgs_UnknownEnvironmentWithoutEnvironmentsTable_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	confFile := filepath.Join(tmpDir, "flyway.conf")
	_ = os.WriteFile(confFile, []byte("flyway.url=jdbc:bigquery:;ProjectId=p;DefaultDataset=d;\n"), 0644)

	args := []string{
		"-configFile=" + confFile,
		"-environment=production",
	}

	_, err := parseCLIArgs(args)
	if err == nil {
		t.Fatalf("expected error for unknown environment with no environments table, got nil")
	}
	if !strings.Contains(err.Error(), "environment 'production' not found in config") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseCLIArgs_EnvironmentExactCaseMismatch_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	tomlFile := filepath.Join(tmpDir, "flyway.toml")
	content := `
[environments.production]
url = "jdbc:bigquery:;ProjectId=p;DefaultDataset=d;"
`
	_ = os.WriteFile(tomlFile, []byte(content), 0644)

	args := []string{
		"-configFile=" + tomlFile,
		"-environment=Production",
	}

	_, err := parseCLIArgs(args)
	if err == nil {
		t.Fatalf("expected error for case mismatched environment, got nil")
	}
	if !strings.Contains(err.Error(), "environment 'Production' not found in config") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseCLIArgs_InvalidBooleanFails(t *testing.T) {
	args := []string{
		"-cleanDisabled=flase",
	}

	_, err := parseCLIArgs(args)
	if err == nil {
		t.Fatalf("expected error for -cleanDisabled=flase, got nil")
	}
	if !strings.Contains(err.Error(), "invalid boolean value") {
		t.Errorf("expected invalid boolean error, got: %v", err)
	}
}

func TestParseCLIArgs_UnknownPropertySuggestsNearest(t *testing.T) {
	args := []string{
		"-cleanDisable=false",
	}

	_, err := parseCLIArgs(args)
	if err == nil {
		t.Fatalf("expected error for -cleanDisable=false, got nil")
	}
	expected := "Unknown configuration property: cleanDisable. Did you mean 'cleanDisabled'?"
	if err.Error() != expected {
		t.Errorf("got error %q, want %q", err.Error(), expected)
	}
}

func TestCLIOverridesEnvironmentVariables(t *testing.T) {
	t.Setenv("FLYWAY_TARGET", "1")
	t.Setenv("FLYWAY_URL", "jdbc:bigquery:;ProjectId=env-proj;DefaultDataset=env_ds;")

	args := []string{
		"-target=5",
	}

	cfg, err := parseCLIArgs(args)
	if err != nil {
		t.Fatalf("parseCLIArgs failed: %v", err)
	}
	if cfg.Target != "5" {
		t.Fatalf("expected CLI flag -target=5 to override FLYWAY_TARGET=1, got: %s", cfg.Target)
	}

	nw, err := noway.New(noway.WithConfig(cfg))
	if err != nil {
		t.Fatalf("noway.New failed: %v", err)
	}
	defer nw.Close()

	if cfg.Target != "5" {
		t.Errorf("expected noway.New to preserve Target=5 and not overwrite with FLYWAY_TARGET=1, got: %s", cfg.Target)
	}
}


