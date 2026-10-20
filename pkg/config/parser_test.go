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

func TestFlywayTOML_Hierarchical(t *testing.T) {
	tomlContent := `
[flyway]
locations = ["filesystem:./sql", "filesystem:./custom"]
table = "flyway_history"

[environments.default]
url = "jdbc:bigquery:;ProjectId=test-proj;DefaultDataset=test_ds;"
user = "test_user"
password = "secret_password"

[environments.default.flyway]
cleanDisabled = false

[environments.staging]
url = "jdbc:bigquery:;ProjectId=staging-proj;DefaultDataset=staging_ds;"
user = "staging_user"

[environments.staging.flyway.placeholders]
env = "staging"
tier = "qa"
`
	tmpDir := t.TempDir()
	tomlPath := filepath.Join(tmpDir, "flyway.toml")
	if err := os.WriteFile(tomlPath, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("failed to write flyway.toml: %v", err)
	}

	// 1. Default environment
	cfgDefault, err := LoadFromFile(tomlPath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}
	if err := cfgDefault.Finalize(); err != nil {
		t.Fatalf("cfgDefault.Finalize failed: %v", err)
	}
	if cfgDefault.GCPProjectID != "test-proj" || cfgDefault.GCPDataset != "test_ds" {
		t.Errorf("expected default url loaded, got %s/%s", cfgDefault.GCPProjectID, cfgDefault.GCPDataset)
	}
	if cfgDefault.User != "test_user" {
		t.Errorf("expected default user 'test_user', got %s", cfgDefault.User)
	}
	if cfgDefault.Password != "secret_password" {
		t.Errorf("expected password 'secret_password', got %s", cfgDefault.Password)
	}
	if cfgDefault.Table != "flyway_history" {
		t.Errorf("expected table from [flyway] 'flyway_history', got %s", cfgDefault.Table)
	}
	if cfgDefault.CleanDisabled != false {
		t.Errorf("expected cleanDisabled=false from [environments.default.flyway]")
	}
	if len(cfgDefault.Locations) != 2 || cfgDefault.Locations[1] != "filesystem:./custom" {
		t.Errorf("expected locations from [flyway], got %v", cfgDefault.Locations)
	}

	// 2. Specific environment via cfg.Environment
	cfgStaging := NewDefaultConfiguration()
	cfgStaging.Environment = "staging"
	if err := LoadFromFileInto(tomlPath, cfgStaging); err != nil {
		t.Fatalf("LoadFromFileInto staging failed: %v", err)
	}
	if err := cfgStaging.Finalize(); err != nil {
		t.Fatalf("cfgStaging.Finalize failed: %v", err)
	}
	if cfgStaging.GCPProjectID != "staging-proj" || cfgStaging.GCPDataset != "staging_ds" {
		t.Errorf("expected staging url, got %s/%s", cfgStaging.GCPProjectID, cfgStaging.GCPDataset)
	}
	if cfgStaging.User != "staging_user" {
		t.Errorf("expected staging user, got %s", cfgStaging.User)
	}
	if cfgStaging.Placeholders["env"] != "staging" || cfgStaging.Placeholders["tier"] != "qa" {
		t.Errorf("expected staging placeholders, got %v", cfgStaging.Placeholders)
	}

	// 3. Unknown environment error
	cfgUnknown := NewDefaultConfiguration()
	cfgUnknown.Environment = "production"
	if err := LoadFromFileInto(tomlPath, cfgUnknown); err == nil {
		t.Errorf("expected error for non-existent environment 'production', got nil")
	}
}

func TestFlywayTOML_EnvironmentUnderFlywayTable(t *testing.T) {
	tomlContent := `
[flyway]
environment = "development"

[environments.development]
url = "jdbc:bigquery:;ProjectId=dev-proj;DefaultDataset=dev_ds;"
user = "dev_user"

[environments.production]
url = "jdbc:bigquery:;ProjectId=prod-proj;DefaultDataset=prod_ds;"
user = "prod_user"
`
	tmpDir := t.TempDir()
	tomlPath := filepath.Join(tmpDir, "flyway.toml")
	if err := os.WriteFile(tomlPath, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("failed to write flyway.toml: %v", err)
	}

	// Loading without pre-set Environment should pick up "development" from [flyway]
	cfg, err := LoadFromFile(tomlPath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("cfg.Finalize failed: %v", err)
	}

	if cfg.Environment != "development" {
		t.Errorf("expected Environment 'development' from [flyway], got %s", cfg.Environment)
	}
	if cfg.GCPProjectID != "dev-proj" || cfg.GCPDataset != "dev_ds" {
		t.Errorf("expected dev environment url loaded, got %s/%s", cfg.GCPProjectID, cfg.GCPDataset)
	}
	if cfg.User != "dev_user" {
		t.Errorf("expected dev_user, got %s", cfg.User)
	}
}


func TestLoadFromFile_Overlay(t *testing.T) {
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "base.conf")
	overridePath := filepath.Join(tmpDir, "override.toml")

	baseContent := `
flyway.url=jdbc:bigquery:;ProjectId=base-proj;DefaultDataset=base_ds;
flyway.user=base_user
flyway.cleanDisabled=true
flyway.placeholders.p1=v1
`
	overrideContent := `
[flyway]
cleanDisabled = false

[flyway.placeholders]
p2 = "v2"

[environments.default]
url = "jdbc:bigquery:;ProjectId=override-proj;DefaultDataset=override_ds;"
`
	if err := os.WriteFile(basePath, []byte(baseContent), 0644); err != nil {
		t.Fatalf("failed to write base.conf: %v", err)
	}
	if err := os.WriteFile(overridePath, []byte(overrideContent), 0644); err != nil {
		t.Fatalf("failed to write override.toml: %v", err)
	}

	cfg := NewDefaultConfiguration()
	if err := LoadFromFileInto(basePath, cfg); err != nil {
		t.Fatalf("failed loading base: %v", err)
	}
	if err := LoadFromFileInto(overridePath, cfg); err != nil {
		t.Fatalf("failed loading override: %v", err)
	}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("cfg.Finalize failed: %v", err)
	}

	if cfg.GCPProjectID != "override-proj" {
		t.Errorf("expected url from override, got %s", cfg.GCPProjectID)
	}
	if cfg.User != "base_user" {
		t.Errorf("expected user preserved from base 'base_user', got %s", cfg.User)
	}
	if cfg.CleanDisabled != false {
		t.Errorf("expected cleanDisabled overridden to false")
	}
	if cfg.Placeholders["p1"] != "v1" || cfg.Placeholders["p2"] != "v2" {
		t.Errorf("expected merged placeholders p1 and p2, got %v", cfg.Placeholders)
	}
}

func TestLoadFromFiles_MultipleFilesCrossEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	tomlFile := filepath.Join(tmpDir, "environments.toml")
	confFile := filepath.Join(tmpDir, "flyway.conf")

	tomlContent := `
[environments.staging]
url = "jdbc:bigquery:;ProjectId=staging-proj;DefaultDataset=staging_ds;"
user = "staging_user"

[environments.production]
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

	// Order 1: TOML first, then CONF
	cfg1, err := LoadFromFiles([]string{tomlFile, confFile})
	if err != nil {
		t.Fatalf("LoadFromFiles (order 1) failed: %v", err)
	}
	if cfg1.Environment != "staging" {
		t.Errorf("expected Environment='staging', got '%s'", cfg1.Environment)
	}
	if cfg1.URL != "jdbc:bigquery:;ProjectId=staging-proj;DefaultDataset=staging_ds;" {
		t.Errorf("expected staging URL, got '%s'", cfg1.URL)
	}
	if cfg1.User != "staging_user" {
		t.Errorf("expected User='staging_user', got '%s'", cfg1.User)
	}
	if cfg1.Password != "secret_staging" {
		t.Errorf("expected Password='secret_staging', got '%s'", cfg1.Password)
	}

	// Order 2: CONF first, then TOML
	cfg2, err := LoadFromFiles([]string{confFile, tomlFile})
	if err != nil {
		t.Fatalf("LoadFromFiles (order 2) failed: %v", err)
	}
	if cfg2.Environment != "staging" {
		t.Errorf("expected Environment='staging', got '%s'", cfg2.Environment)
	}
	if cfg2.URL != "jdbc:bigquery:;ProjectId=staging-proj;DefaultDataset=staging_ds;" {
		t.Errorf("expected staging URL, got '%s'", cfg2.URL)
	}
	if cfg2.User != "staging_user" {
		t.Errorf("expected User='staging_user', got '%s'", cfg2.User)
	}
	if cfg2.Password != "secret_staging" {
		t.Errorf("expected Password='secret_staging', got '%s'", cfg2.Password)
	}
}

func TestDetermineEnvironment_PriorityOrder(t *testing.T) {
	tmpDir := t.TempDir()

	// Clear environment variables before testing
	origFlywayEnv := os.Getenv("FLYWAY_ENVIRONMENT")
	origNowayEnv := os.Getenv("NOWAY_ENVIRONMENT")
	defer func() {
		os.Setenv("FLYWAY_ENVIRONMENT", origFlywayEnv)
		os.Setenv("NOWAY_ENVIRONMENT", origNowayEnv)
	}()
	os.Unsetenv("FLYWAY_ENVIRONMENT")
	os.Unsetenv("NOWAY_ENVIRONMENT")

	// Priority 5: default
	{
		emptyFile := filepath.Join(tmpDir, "empty.conf")
		_ = os.WriteFile(emptyFile, []byte("flyway.table=history\n"), 0644)
		cfg, err := LoadFromFiles([]string{emptyFile})
		if err != nil {
			t.Fatalf("LoadFromFiles failed: %v", err)
		}
		if cfg.Environment != "default" {
			t.Errorf("priority 5: expected 'default', got '%s'", cfg.Environment)
		}
	}

	// Priority 4: root environment
	{
		rootEnvFile := filepath.Join(tmpDir, "root_env.toml")
		_ = os.WriteFile(rootEnvFile, []byte("environment = \"env4\"\n[environments.env4]\nurl=\"jdbc:bigquery:;ProjectId=p4;DefaultDataset=d4;\"\n"), 0644)
		cfg, err := LoadFromFiles([]string{rootEnvFile})
		if err != nil {
			t.Fatalf("LoadFromFiles failed: %v", err)
		}
		if cfg.Environment != "env4" {
			t.Errorf("priority 4: expected 'env4', got '%s'", cfg.Environment)
		}
	}

	// Priority 3: flyway.environment overrides root environment
	{
		flywayEnvFile := filepath.Join(tmpDir, "flyway_env.toml")
		content := `
environment = "env4"

[flyway]
environment = "env3"

[environments.env3]
url = "jdbc:bigquery:;ProjectId=p3;DefaultDataset=d3;"

[environments.env4]
url = "jdbc:bigquery:;ProjectId=p4;DefaultDataset=d4;"
`
		_ = os.WriteFile(flywayEnvFile, []byte(content), 0644)
		cfg, err := LoadFromFiles([]string{flywayEnvFile})
		if err != nil {
			t.Fatalf("LoadFromFiles failed: %v", err)
		}
		if cfg.Environment != "env3" {
			t.Errorf("priority 3: expected 'env3', got '%s'", cfg.Environment)
		}
	}

	// Priority 2: FLYWAY_ENVIRONMENT overrides flyway.environment and root environment
	{
		os.Setenv("FLYWAY_ENVIRONMENT", "env2")
		file := filepath.Join(tmpDir, "env2_test.toml")
		content := `
environment = "env4"

[flyway]
environment = "env3"

[environments.env2]
url = "jdbc:bigquery:;ProjectId=p2;DefaultDataset=d2;"

[environments.env3]
url = "jdbc:bigquery:;ProjectId=p3;DefaultDataset=d3;"
`
		_ = os.WriteFile(file, []byte(content), 0644)
		cfg, err := LoadFromFiles([]string{file})
		if err != nil {
			t.Fatalf("LoadFromFiles failed: %v", err)
		}
		if cfg.Environment != "env2" {
			t.Errorf("priority 2: expected 'env2', got '%s'", cfg.Environment)
		}
	}

	// Priority 1: CLI overrides FLYWAY_ENVIRONMENT, flyway.environment, root environment
	{
		os.Setenv("FLYWAY_ENVIRONMENT", "env2")
		file := filepath.Join(tmpDir, "env1_test.toml")
		content := `
environment = "env4"

[flyway]
environment = "env3"

[environments.env1]
url = "jdbc:bigquery:;ProjectId=p1;DefaultDataset=d1;"

[environments.env2]
url = "jdbc:bigquery:;ProjectId=p2;DefaultDataset=d2;"
`
		_ = os.WriteFile(file, []byte(content), 0644)
		cfg := NewDefaultConfiguration()
		cfg.Environment = "env1"
		if err := LoadFromFilesInto([]string{file}, cfg); err != nil {
			t.Fatalf("LoadFromFilesInto failed: %v", err)
		}
		if cfg.Environment != "env1" {
			t.Errorf("priority 1: expected 'env1', got '%s'", cfg.Environment)
		}
		if cfg.URL != "jdbc:bigquery:;ProjectId=p1;DefaultDataset=d1;" {
			t.Errorf("expected URL from env1, got '%s'", cfg.URL)
		}
	}
}

func TestLoadFromFiles_PlaceholderWithDotsAndMultiFile(t *testing.T) {
	tmpDir := t.TempDir()
	conf1 := filepath.Join(tmpDir, "ph1.properties")
	conf2 := filepath.Join(tmpDir, "ph2.conf")

	_ = os.WriteFile(conf1, []byte("flyway.placeholders.my.dotted.var=hello\nflyway.placeholders.shared=val1\n"), 0644)
	_ = os.WriteFile(conf2, []byte("flyway.placeholders.shared=val2\nplaceholders.root.dotted.key=world\n"), 0644)

	cfg, err := LoadFromFiles([]string{conf1, conf2})
	if err != nil {
		t.Fatalf("LoadFromFiles failed: %v", err)
	}

	if cfg.Placeholders["my.dotted.var"] != "hello" {
		t.Errorf("expected my.dotted.var='hello', got '%s'", cfg.Placeholders["my.dotted.var"])
	}
	if cfg.Placeholders["shared"] != "val2" {
		t.Errorf("expected shared='val2', got '%s'", cfg.Placeholders["shared"])
	}
	if cfg.Placeholders["root.dotted.key"] != "world" {
		t.Errorf("expected root.dotted.key='world', got '%s'", cfg.Placeholders["root.dotted.key"])
	}
}


