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

func TestParseCLIArgs_Task10Flags(t *testing.T) {
	args := []string{
		"-url=jdbc:bigquery:;ProjectId=test-proj;DefaultDataset=analytics;",
		"-ignoreMigrationPatterns=*:missing,repeatable:missing",
		"-skipExecutingMigrations=true",
		"-cherryPick=1.0,2.0",
		"-failOnMissingTarget=true",
		"-oauthType=2",
		"-oauthAccessToken=secret-token",
		"-oauthPvtKeyPath=/custom/key.json",
	}

	cfg, err := parseCLIArgs(args)
	if err != nil {
		t.Fatalf("parseCLIArgs failed: %v", err)
	}

	if len(cfg.IgnoreMigrationPatterns) != 2 || cfg.IgnoreMigrationPatterns[0] != "*:missing" {
		t.Errorf("unexpected IgnoreMigrationPatterns: %v", cfg.IgnoreMigrationPatterns)
	}
	if !cfg.SkipExecutingMigrations {
		t.Errorf("expected SkipExecutingMigrations true")
	}
	if len(cfg.CherryPick) != 2 || cfg.CherryPick[0] != "1.0" || cfg.CherryPick[1] != "2.0" {
		t.Errorf("unexpected CherryPick: %v", cfg.CherryPick)
	}
	if !cfg.FailOnMissingTarget {
		t.Errorf("expected FailOnMissingTarget true")
	}
	if cfg.GCPOAuthType == nil || *cfg.GCPOAuthType != 2 {
		t.Errorf("expected GCPOAuthType 2, got %v", cfg.GCPOAuthType)
	}
	if cfg.GCPAccessToken != "secret-token" {
		t.Errorf("expected GCPAccessToken 'secret-token', got %q", cfg.GCPAccessToken)
	}
	if cfg.GCPCredentialsFile != "/custom/key.json" {
		t.Errorf("expected GCPCredentialsFile '/custom/key.json', got %q", cfg.GCPCredentialsFile)
	}
}

func TestParseCLIArgs_FlagsAndOptions(t *testing.T) {
	args := []string{
		"-q",
		"-X",
		"-skipCheckForUpdate",
		"-communityFallback",
		"-workingDirectory=/tmp/myworkdir",
		"-outputFile=/tmp/out.log",
		"-color=always",
		"-outputLogsInJson",
		"-configFileEncoding=UTF-8",
		"-infoSinceDate=2026-01-01",
	}

	cfg, err := parseCLIArgs(args)
	if err != nil {
		t.Fatalf("parseCLIArgs failed: %v", err)
	}

	if !cfg.Quiet {
		t.Errorf("expected Quiet true")
	}
	if !cfg.Debug {
		t.Errorf("expected Debug true")
	}
	if cfg.WorkingDirectory != "/tmp/myworkdir" {
		t.Errorf("expected WorkingDirectory '/tmp/myworkdir', got %q", cfg.WorkingDirectory)
	}
	if cfg.OutputFile != "/tmp/out.log" {
		t.Errorf("expected OutputFile '/tmp/out.log', got %q", cfg.OutputFile)
	}
	if cfg.Color != "always" {
		t.Errorf("expected Color 'always', got %q", cfg.Color)
	}

	// Invalid color fails
	_, err = parseCLIArgs([]string{"-color=bogus"})
	if err == nil || !strings.Contains(err.Error(), "invalid color") {
		t.Errorf("expected error for invalid color, got: %v", err)
	}
}

func TestParseCLIArgs_InvalidIntegersAndOAuthValidation(t *testing.T) {
	// 1. Invalid connectRetries
	_, err := parseCLIArgs([]string{"-connectRetries=abc"})
	if err == nil || !strings.Contains(err.Error(), "invalid integer value") {
		t.Errorf("expected error for invalid connectRetries, got: %v", err)
	}

	// 2. Invalid lockRetryCount
	_, err = parseCLIArgs([]string{"-lockRetryCount=xyz"})
	if err == nil || !strings.Contains(err.Error(), "invalid integer value") {
		t.Errorf("expected error for invalid lockRetryCount, got: %v", err)
	}

	// 3. Invalid oauthType
	_, err = parseCLIArgs([]string{"-oauthType=notanumber"})
	if err == nil || !strings.Contains(err.Error(), "invalid integer value") {
		t.Errorf("expected error for invalid oauthType, got: %v", err)
	}

	// 4. OAuthAccessToken set when OAuthType is not 2
	_, err = parseCLIArgs([]string{"-oauthAccessToken=mytoken", "-oauthType=0"})
	if err == nil || !strings.Contains(err.Error(), "OAuthAccessToken is only valid when OAuthType is 2") {
		t.Errorf("expected error when OAuthAccessToken is set with OAuthType != 2, got: %v", err)
	}
}

func TestParseCLIArgs_WorkingDirectoryAndLocations(t *testing.T) {
	cfg, err := parseCLIArgs([]string{"-workingDirectory=db", "-locations=filesystem:sql"})
	if err != nil {
		t.Fatalf("parseCLIArgs error: %v", err)
	}
	if cfg.WorkingDirectory != "db" {
		t.Errorf("expected WorkingDirectory 'db', got %q", cfg.WorkingDirectory)
	}
	if len(cfg.Locations) != 1 || cfg.Locations[0] != "filesystem:sql" {
		t.Errorf("expected user Locations ['filesystem:sql'], got %v", cfg.Locations)
	}
	resolved := cfg.ResolvedLocations()
	expected := "filesystem:" + filepath.Join("db", "sql")
	if len(resolved) != 1 || resolved[0] != expected {
		t.Errorf("expected ResolvedLocations [%s], got %v", expected, resolved)
	}
}

func TestParseCLIArgs_FailOnMissingLocationsAndValidateMigrationNaming(t *testing.T) {
	cfg, err := parseCLIArgs([]string{
		"-failOnMissingLocations=true",
		"-validateMigrationNaming=true",
	})
	if err != nil {
		t.Fatalf("parseCLIArgs failed: %v", err)
	}
	if !cfg.FailOnMissingLocations {
		t.Errorf("expected FailOnMissingLocations to be true")
	}
	if !cfg.ValidateMigrationNaming {
		t.Errorf("expected ValidateMigrationNaming to be true")
	}
}

func TestExtractCommandAndOptions(t *testing.T) {
	// 1. -schemas info migrate -> command is "migrate", options are ["-schemas", "info"]
	cmd, opts := extractCommandAndOptions([]string{"-schemas", "info", "migrate"})
	if cmd != "migrate" {
		t.Errorf("expected command 'migrate', got %q", cmd)
	}
	if len(opts) != 2 || opts[0] != "-schemas" || opts[1] != "info" {
		t.Errorf("expected options ['-schemas', 'info'], got %v", opts)
	}

	// 2. migrate -schemas info -> command is "migrate"
	cmd, opts = extractCommandAndOptions([]string{"migrate", "-schemas", "info"})
	if cmd != "migrate" {
		t.Errorf("expected command 'migrate', got %q", cmd)
	}
	if len(opts) != 2 || opts[0] != "-schemas" || opts[1] != "info" {
		t.Errorf("expected options ['-schemas', 'info'], got %v", opts)
	}

	// 3. -v -> command is "version"
	cmd, opts = extractCommandAndOptions([]string{"-v"})
	if cmd != "version" {
		t.Errorf("expected command 'version', got %q", cmd)
	}
	if len(opts) != 1 || opts[0] != "-v" {
		t.Errorf("expected options ['-v'], got %v", opts)
	}
}




