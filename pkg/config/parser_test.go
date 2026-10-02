package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigSingularPlaceholderAndCase_Regression verifies that:
// 1. Singular FLYWAY_PLACEHOLDER_<KEY> and NOWAY_PLACEHOLDER_<KEY> work alongside plural.
// 2. Properties files support both placeholder. and placeholders. case-insensitively.
// 3. Prefix stripping for flyway. and noway. is case-insensitive.
func TestConfigSingularPlaceholderAndCase_Regression(t *testing.T) {
	// 1. Test Env vars
	t.Setenv("FLYWAY_PLACEHOLDER_SINGLE_VAR", "single_val")
	t.Setenv("FLYWAY_PLACEHOLDERS_PLURAL_VAR", "plural_val")
	t.Setenv("NOWAY_PLACEHOLDER_NOWAY_VAR", "noway_val")

	cfg := NewDefaultConfiguration()
	LoadFromEnv(cfg)

	if cfg.Placeholders["SINGLE_VAR"] != "single_val" {
		t.Errorf("expected SINGLE_VAR 'single_val', got %q", cfg.Placeholders["SINGLE_VAR"])
	}
	if cfg.Placeholders["PLURAL_VAR"] != "plural_val" {
		t.Errorf("expected PLURAL_VAR 'plural_val', got %q", cfg.Placeholders["PLURAL_VAR"])
	}
	if cfg.Placeholders["NOWAY_VAR"] != "noway_val" {
		t.Errorf("expected NOWAY_VAR 'noway_val', got %q", cfg.Placeholders["NOWAY_VAR"])
	}

	// 2. Test Properties file with mixed casing and singular/plural
	confContent := `
# Properties with mixed casing and singular placeholder
flyway.placeholder.dataset=analytics
Flyway.Placeholders.CustomTable=users
noway.placeholder.EnvName=prod
FLYWAY.placeholders.REGION=us-central1
`
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "test.conf")
	if err := os.WriteFile(confPath, []byte(confContent), 0644); err != nil {
		t.Fatalf("failed to write conf: %v", err)
	}

	loaded, err := LoadFromFile(confPath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if loaded.Placeholders["dataset"] != "analytics" {
		t.Errorf("expected dataset 'analytics', got %q", loaded.Placeholders["dataset"])
	}
	if loaded.Placeholders["CustomTable"] != "users" {
		t.Errorf("expected CustomTable 'users', got %q", loaded.Placeholders["CustomTable"])
	}
	if loaded.Placeholders["EnvName"] != "prod" {
		t.Errorf("expected EnvName 'prod', got %q", loaded.Placeholders["EnvName"])
	}
	if loaded.Placeholders["REGION"] != "us-central1" {
		t.Errorf("expected REGION 'us-central1', got %q", loaded.Placeholders["REGION"])
	}
}
