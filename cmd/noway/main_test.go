package main

import (
	"os"
	"testing"
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

