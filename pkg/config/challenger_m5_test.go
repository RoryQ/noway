package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChallengerM5_MultiFormatConfigLoading(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("TOML file parsing", func(t *testing.T) {
		tomlContent := `
url = "jdbc:bigquery:;ProjectId=toml-proj;"
table = "toml_history"
outOfOrder = true
target = "2.0"
schemas = ["ds1", "ds2"]
`
		path := filepath.Join(tmpDir, "flyway.toml")
		if err := os.WriteFile(path, []byte(tomlContent), 0644); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		cfg, err := LoadFromFile(path)
		if err != nil {
			t.Fatalf("LoadFromFile TOML failed: %v", err)
		}
		if cfg.URL != "jdbc:bigquery:;ProjectId=toml-proj;" || cfg.Table != "toml_history" || !cfg.OutOfOrder || cfg.Target != "2.0" {
			t.Errorf("unexpected TOML config values: %+v", cfg)
		}
		if len(cfg.Schemas) != 2 || cfg.Schemas[0] != "ds1" {
			t.Errorf("unexpected Schemas: %v", cfg.Schemas)
		}
	})

	t.Run("YAML file parsing", func(t *testing.T) {
		yamlContent := `
url: "jdbc:bigquery:;ProjectId=yaml-proj;"
table: "yaml_history"
cleanDisabled: false
target: "next"
schemas:
  - "analytics"
`
		path := filepath.Join(tmpDir, "flyway.yaml")
		if err := os.WriteFile(path, []byte(yamlContent), 0644); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		cfg, err := LoadFromFile(path)
		if err != nil {
			t.Fatalf("LoadFromFile YAML failed: %v", err)
		}
		if cfg.Table != "yaml_history" || cfg.CleanDisabled || cfg.Target != "next" {
			t.Errorf("unexpected YAML config values: %+v", cfg)
		}
	})

	t.Run("JSON file parsing", func(t *testing.T) {
		jsonContent := `{
  "url": "jdbc:bigquery:;ProjectId=json-proj;",
  "table": "json_history",
  "baselineOnMigrate": true,
  "baselineVersion": "10.0"
}`
		path := filepath.Join(tmpDir, "flyway.json")
		if err := os.WriteFile(path, []byte(jsonContent), 0644); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		cfg, err := LoadFromFile(path)
		if err != nil {
			t.Fatalf("LoadFromFile JSON failed: %v", err)
		}
		if cfg.Table != "json_history" || !cfg.BaselineOnMigrate || cfg.BaselineVersion != "10.0" {
			t.Errorf("unexpected JSON config values: %+v", cfg)
		}
	})

	t.Run("Empty config file defaults cleanly", func(t *testing.T) {
		path := filepath.Join(tmpDir, "empty.conf")
		if err := os.WriteFile(path, []byte(""), 0644); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		cfg, err := LoadFromFile(path)
		if err != nil {
			t.Fatalf("LoadFromFile empty failed: %v", err)
		}
		if cfg.Table != "flyway_schema_history" || cfg.SQLMigrationPrefix != "V" {
			t.Errorf("expected standard defaults on empty config, got %+v", cfg)
		}
	})

	t.Run("Malformed syntax returns error without panic", func(t *testing.T) {
		badJSON := filepath.Join(tmpDir, "bad.json")
		_ = os.WriteFile(badJSON, []byte("{ invalid json "), 0644)
		_, err := LoadFromFile(badJSON)
		if err == nil {
			t.Errorf("expected error for malformed JSON, got nil")
		}

		badYAML := filepath.Join(tmpDir, "bad.yaml")
		_ = os.WriteFile(badYAML, []byte("key: [unclosed list"), 0644)
		_, err = LoadFromFile(badYAML)
		if err == nil {
			t.Errorf("expected error for malformed YAML, got nil")
		}
	})
}

func TestChallengerM5_BooleanParsingMatrix(t *testing.T) {
	truthy := []string{"true", "TRUE", "True", "yes", "YES", "1", "on", "ON"}
	falsy := []string{"false", "FALSE", "False", "no", "NO", "0", "off", "OFF"}

	for _, s := range truthy {
		if !parseBool(s, false) {
			t.Errorf("expected truthy for %q", s)
		}
	}

	for _, s := range falsy {
		if parseBool(s, true) {
			t.Errorf("expected falsy for %q", s)
		}
	}

	// Unknown string returns default
	if !parseBool("unknown_string", true) {
		t.Errorf("expected default true")
	}
	if parseBool("unknown_string", false) {
		t.Errorf("expected default false")
	}
}

func TestChallengerM5_FinalizeTargetVersions(t *testing.T) {
	t.Run("sentinel targets", func(t *testing.T) {
		for _, target := range []string{"latest", "LATEST", "current", "CURRENT", "next", "NEXT"} {
			cfg := NewDefaultConfiguration()
			cfg.Target = target
			if err := cfg.Finalize(); err != nil {
				t.Errorf("unexpected error for sentinel %q: %v", target, err)
			}
			if cfg.TargetVersion != nil {
				t.Errorf("expected nil TargetVersion for sentinel %q", target)
			}
		}
	})

	t.Run("numeric valid target", func(t *testing.T) {
		cfg := NewDefaultConfiguration()
		cfg.Target = "2.1.0"
		if err := cfg.Finalize(); err != nil {
			t.Fatalf("unexpected error for numeric target: %v", err)
		}
		if cfg.TargetVersion == nil || cfg.TargetVersion.Normalized() != "2.1" {
			t.Errorf("expected TargetVersion normalized 2.1, got %v (normalized: %q)", cfg.TargetVersion, cfg.TargetVersion.Normalized())
		}
	})

	t.Run("invalid target returns error", func(t *testing.T) {
		cfg := NewDefaultConfiguration()
		cfg.Target = "invalid_version_123"
		if err := cfg.Finalize(); err == nil {
			t.Errorf("expected error for invalid target, got nil")
		}
	})
}

func TestChallengerM5_ColonSeparatorAndExclamationCommentProperties(t *testing.T) {
	confContent := `
! Exclamation point comment
# Hash comment
flyway.table: colon_history
flyway.cleanDisabled: false
flyway.schemas: dataset1, dataset2
`
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "colon.properties")
	if err := os.WriteFile(confPath, []byte(confContent), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	cfg, err := LoadFromFile(confPath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if cfg.Table != "colon_history" {
		t.Errorf("expected table 'colon_history', got %q", cfg.Table)
	}
	if cfg.CleanDisabled {
		t.Errorf("expected cleanDisabled false")
	}
	if len(cfg.Schemas) != 2 || cfg.Schemas[0] != "dataset1" || cfg.Schemas[1] != "dataset2" {
		t.Errorf("unexpected schemas: %v", cfg.Schemas)
	}
}
