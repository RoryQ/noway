package main

import (
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
