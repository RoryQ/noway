package migrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database/mock"
	"github.com/roryq/noway/pkg/parser"
	"github.com/roryq/noway/pkg/resolver"
)

func TestCallbackRunner_SQLStatementExecutionAndPlaceholders(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()

	bqParser := parser.NewBigQueryParser()
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled:   true,
		Prefix:    "${",
		Suffix:    "}",
		Separator: ":",
		Values: map[string]string{
			"custom_env": "production",
		},
	})
	builtins := parser.BuiltinPlaceholders{
		DefaultSchema: "my_schema",
		Table:         "flyway_schema_history",
		User:          "dev_user",
		Database:      "my_db",
		Timestamp:     time.Now(),
	}

	callbacks := map[string][]resolver.ResolvedCallback{
		"beforeMigrate": {
			{
				Event:    "beforeMigrate",
				Filename: "beforeMigrate__audit.sql",
				Content:  "CREATE TABLE ${flyway:defaultSchema}.audit (env STRING DEFAULT '${custom_env}');",
			},
		},
	}

	runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)
	runner.SetConfig(&config.Configuration{
		DefaultSchema: "my_schema",
		Placeholders: map[string]string{
			"custom_env": "production",
		},
	})

	if err := runner.Fire(ctx, "beforeMigrate"); err != nil {
		t.Fatalf("Fire(beforeMigrate) unexpected error: %v", err)
	}

	stmts := db.ExecutedStatements()
	if len(stmts) != 1 {
		t.Fatalf("expected 1 statement executed, got %d", len(stmts))
	}
	expected := "CREATE TABLE my_schema.audit (env STRING DEFAULT 'production')"
	if stmts[0] != expected {
		t.Errorf("expected %q, got %q", expected, stmts[0])
	}
}

func TestCallbackRunner_ScriptEnvironmentVariables(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()

	tmpDir := t.TempDir()
	outFile := filepath.Join(tmpDir, "env_out.txt")

	scriptContent := `#!/bin/bash
echo "FLYWAY_OPERATION=$FLYWAY_OPERATION" >> ` + outFile + `
echo "NOWAY_OPERATION=$NOWAY_OPERATION" >> ` + outFile + `
echo "FLYWAY_DATABASE=$FLYWAY_DATABASE" >> ` + outFile + `
echo "NOWAY_DATABASE=$NOWAY_DATABASE" >> ` + outFile + `
echo "FLYWAY_SCHEMAS=$FLYWAY_SCHEMAS" >> ` + outFile + `
echo "NOWAY_SCHEMAS=$NOWAY_SCHEMAS" >> ` + outFile + `
echo "FLYWAY_EVENT=$FLYWAY_EVENT" >> ` + outFile + `
echo "FP__region__=$FP__region__" >> ` + outFile + `
exit 0
`

	bqParser := parser.NewBigQueryParser()
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{Enabled: false})
	builtins := parser.BuiltinPlaceholders{
		DefaultSchema: "dataset_a",
		Table:         "flyway_schema_history",
		User:          "tester",
		Database:      "db_proj",
	}

	callbacks := map[string][]resolver.ResolvedCallback{
		"beforeMigrate": {
			{
				Event:    "beforeMigrate",
				Filename: "beforeMigrate__check_env.sh",
				Content:  scriptContent,
			},
		},
	}

	runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)
	runner.SetConfig(&config.Configuration{
		Schemas: []string{"dataset_a", "dataset_b"},
		Placeholders: map[string]string{
			"region": "us-central1",
		},
	})

	if err := runner.Fire(ctx, "beforeMigrate"); err != nil {
		t.Fatalf("Fire(beforeMigrate) script error: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed reading env output file: %v", err)
	}
	outStr := string(data)

	expectedKeys := []string{
		"FLYWAY_OPERATION=MIGRATE",
		"NOWAY_OPERATION=MIGRATE",
		"FLYWAY_DATABASE=db_proj",
		"NOWAY_DATABASE=db_proj",
		"FLYWAY_SCHEMAS=dataset_a,dataset_b",
		"NOWAY_SCHEMAS=dataset_a,dataset_b",
		"FLYWAY_EVENT=beforeMigrate",
		"FP__region__=us-central1",
	}

	for _, k := range expectedKeys {
		if !strings.Contains(outStr, k) {
			t.Errorf("output missing expected variable: %q\nFull output:\n%s", k, outStr)
		}
	}
}

func TestCallbackRunner_PythonScriptExecution(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()

	pyContent := `import os, sys

if os.environ.get("FLYWAY_OPERATION") != "VALIDATE":
    sys.exit(10)
if os.environ.get("FLYWAY_DATABASE") != "py_dataset":
    sys.exit(11)
if os.environ.get("FP__cluster__") != "prod-cluster-1":
    sys.exit(12)
sys.exit(0)
`

	bqParser := parser.NewBigQueryParser()
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{Enabled: false})
	builtins := parser.BuiltinPlaceholders{
		DefaultSchema: "py_dataset",
		Table:         "flyway_schema_history",
		User:          "py_runner",
	}

	callbacks := map[string][]resolver.ResolvedCallback{
		"beforeValidate": {
			{
				Event:    "beforeValidate",
				Filename: "beforeValidate__check.py",
				Content:  pyContent,
			},
		},
	}

	runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)
	runner.SetConfig(&config.Configuration{
		Placeholders: map[string]string{
			"cluster": "prod-cluster-1",
		},
	})
	runner.SetOperation("VALIDATE")

	if err := runner.Fire(ctx, "beforeValidate"); err != nil {
		t.Fatalf("python callback execution failed: %v", err)
	}
}

func TestCallbackRunner_FailureHaltsChain(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()
	db.SetFailOnExecute("FAIL_STMT")

	bqParser := parser.NewBigQueryParser()
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{Enabled: false})
	builtins := parser.BuiltinPlaceholders{DefaultSchema: "public"}

	callbacks := map[string][]resolver.ResolvedCallback{
		"beforeMigrate": {
			{
				Event:    "beforeMigrate",
				Filename: "beforeMigrate__01_fail.sql",
				Content:  "FAIL_STMT;",
			},
			{
				Event:    "beforeMigrate",
				Filename: "beforeMigrate__02_never.sql",
				Content:  "CREATE TABLE never_run (id INT64);",
			},
		},
	}

	runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)
	err := runner.Fire(ctx, "beforeMigrate")
	if err == nil {
		t.Fatalf("expected error from failing callback, got nil")
	}

	for _, s := range db.ExecutedStatements() {
		if strings.Contains(s, "never_run") {
			t.Errorf("second callback should not have executed, but found: %s", s)
		}
	}
}

func TestCallbackRunner_DetermineOperation(t *testing.T) {
	tests := []struct {
		event string
		want  string
	}{
		{"beforeMigrate", "MIGRATE"},
		{"afterMigrate", "MIGRATE"},
		{"beforeEachMigrate", "MIGRATE"},
		{"beforeValidate", "VALIDATE"},
		{"afterValidate", "VALIDATE"},
		{"beforeInfo", "INFO"},
		{"afterInfo", "INFO"},
		{"beforeClean", "CLEAN"},
		{"afterClean", "CLEAN"},
		{"beforeUndo", "UNDO"},
		{"afterUndo", "UNDO"},
		{"beforeBaseline", "BASELINE"},
		{"afterBaseline", "BASELINE"},
		{"beforeRepair", "REPAIR"},
		{"afterRepair", "REPAIR"},
		{"unknownEvent", "COMMAND"},
	}

	for _, tt := range tests {
		got := determineOperation(tt.event)
		if got != tt.want {
			t.Errorf("determineOperation(%q) = %q, want %q", tt.event, got, tt.want)
		}
	}
}

func TestCallbackRunner_FlywayUserPlaceholder(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()

	bqParser := parser.NewBigQueryParser()
	replacer := parser.NewPlaceholderReplacer(parser.DefaultPlaceholderConfig())
	builtins := parser.BuiltinPlaceholders{
		DefaultSchema: "test_schema",
		Table:         "flyway_schema_history",
		User:          "initial_user",
	}

	callbacks := map[string][]resolver.ResolvedCallback{
		"beforeMigrate": {
			{
				Event:    "beforeMigrate",
				Filename: "beforeMigrate__user.sql",
				Content:  "INSERT INTO audit (executed_by) VALUES ('${flyway:user}');",
			},
		},
	}

	runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)

	// 1. Initial fire replaces with initial_user
	if err := runner.Fire(ctx, "beforeMigrate"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stmts := db.ExecutedStatements()
	if len(stmts) != 1 || !strings.Contains(stmts[0], "'initial_user'") {
		t.Errorf("expected statement to contain 'initial_user', got %v", stmts)
	}

	// 2. SetBuiltins updates user dynamically
	builtins.User = "updated_admin"
	runner.SetBuiltins(builtins)
	db.ClearExecutedStatements()

	if err := runner.Fire(ctx, "beforeMigrate"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stmts = db.ExecutedStatements()
	if len(stmts) != 1 || !strings.Contains(stmts[0], "'updated_admin'") {
		t.Errorf("expected statement to contain 'updated_admin', got %v", stmts)
	}
}

func TestCallbackRunner_ScriptPhysicalFilePreservesExecBit(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()
	tmpDir := t.TempDir()

	scriptFile := filepath.Join(tmpDir, "beforeMigrate__hook.sh")
	if err := os.WriteFile(scriptFile, []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	callbacks := map[string][]resolver.ResolvedCallback{
		"beforeMigrate": {
			{
				Event:            "beforeMigrate",
				Filename:         "beforeMigrate__hook.sh",
				PhysicalLocation: scriptFile,
				IsOnDisk:         true,
				Content:          "#!/bin/bash\nexit 0\n",
			},
		},
	}

	runner := NewCallbackRunner(db, callbacks, parser.NewBigQueryParser(), parser.NewPlaceholderReplacer(parser.PlaceholderConfig{}), parser.BuiltinPlaceholders{})
	if err := runner.Fire(ctx, "beforeMigrate"); err != nil {
		t.Fatalf("Fire failed: %v", err)
	}

	fi, err := os.Stat(scriptFile)
	if err != nil {
		t.Fatalf("failed stat script: %v", err)
	}
	if fi.Mode().Perm()&0111 == 0 {
		t.Errorf("expected exec bit preserved on user file, got mode: %v", fi.Mode().Perm())
	}
}

func TestCallbackRunner_ScriptFilesystemUsesReplacedPlaceholders(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()
	tmpDir := t.TempDir()

	outFile := filepath.Join(tmpDir, "ph_out.txt")
	scriptFile := filepath.Join(tmpDir, "beforeMigrate__replace.sh")
	scriptContent := fmt.Sprintf("#!/bin/bash\necho \"VAL=$FP__custom_val__\" > %s\n", outFile)
	if err := os.WriteFile(scriptFile, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	callbacks := map[string][]resolver.ResolvedCallback{
		"beforeMigrate": {
			{
				Event:            "beforeMigrate",
				Filename:         "beforeMigrate__replace.sh",
				PhysicalLocation: scriptFile,
				IsOnDisk:         true,
				Content:          scriptContent,
			},
		},
	}

	runner := NewCallbackRunner(db, callbacks, parser.NewBigQueryParser(), parser.NewPlaceholderReplacer(parser.PlaceholderConfig{}), parser.BuiltinPlaceholders{})
	runner.SetConfig(&config.Configuration{
		Placeholders: map[string]string{
			"custom_val": "hello_from_placeholder",
		},
	})
	if err := runner.Fire(ctx, "beforeMigrate"); err != nil {
		t.Fatalf("Fire failed: %v", err)
	}

	outData, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	if !strings.Contains(string(outData), "VAL=hello_from_placeholder") {
		t.Errorf("expected placeholder replaced in executed script, got: %s", string(outData))
	}

	// Also verify the physical file on disk was NOT mutated or stripped of permissions
	fi, _ := os.Stat(scriptFile)
	if fi.Mode().Perm()&0111 == 0 {
		t.Errorf("expected exec bit preserved on user file, got mode: %v", fi.Mode().Perm())
	}
	diskContent, _ := os.ReadFile(scriptFile)
	if !strings.Contains(string(diskContent), "$FP__custom_val__") {
		t.Errorf("expected original file on disk to remain unchanged, got: %s", string(diskContent))
	}
}

func TestCallbackRunner_ScriptShellSyntaxExecutesWithoutPlaceholderError(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()
	tmpDir := t.TempDir()

	outFile := filepath.Join(tmpDir, "shell_out.txt")
	scriptFile := filepath.Join(tmpDir, "beforeMigrate__shell.sh")
	scriptContent := fmt.Sprintf("#!/bin/bash\necho \"SHELL_VAR=${UNKNOWN_SHELL_VAR:-my_default}\" > %s\nexit 0\n", outFile)
	_ = os.WriteFile(scriptFile, []byte(scriptContent), 0755)

	callbacks := map[string][]resolver.ResolvedCallback{
		"beforeMigrate": {
			{
				Event:            "beforeMigrate",
				Filename:         "beforeMigrate__shell.sh",
				PhysicalLocation: scriptFile,
				IsOnDisk:         true,
				Content:          scriptContent,
			},
		},
	}

	runner := NewCallbackRunner(db, callbacks, parser.NewBigQueryParser(), parser.NewPlaceholderReplacer(parser.PlaceholderConfig{}), parser.BuiltinPlaceholders{})
	if err := runner.Fire(ctx, "beforeMigrate"); err != nil {
		t.Fatalf("expected script with shell syntax to execute without error, got: %v", err)
	}

	outData, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	if !strings.Contains(string(outData), "SHELL_VAR=my_default") {
		t.Errorf("expected shell variable default expanded by bash, got: %s", string(outData))
	}
}

func TestMigrate_ValidateOnMigrate_PreservesMigrateOperation(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()
	tmpDir := t.TempDir()

	outFile := filepath.Join(tmpDir, "op_out.txt")
	scriptFile := filepath.Join(tmpDir, "beforeEachMigrate__check_op.sh")
	scriptContent := fmt.Sprintf("#!/bin/bash\necho \"FLYWAY_OPERATION=$FLYWAY_OPERATION\" > %s\nexit 0\n", outFile)
	_ = os.WriteFile(scriptFile, []byte(scriptContent), 0755)

	sqlMigration := filepath.Join(tmpDir, "V1__init.sql")
	_ = os.WriteFile(sqlMigration, []byte("CREATE TABLE t (id INT64);"), 0644)

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.Locations = []string{tmpDir}
	cfg.ValidateOnMigrate = true

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	_, err = m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	outData, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	if !strings.Contains(string(outData), "FLYWAY_OPERATION=MIGRATE") {
		t.Errorf("expected FLYWAY_OPERATION=MIGRATE during migrate callbacks, got: %s", string(outData))
	}
}

func TestMigrate_ValidateOnMigrate_FiresValidateCallbacksWithMigrateOperation(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()
	tmpDir := t.TempDir()

	beforeFile := filepath.Join(tmpDir, "before_val.txt")
	afterFile := filepath.Join(tmpDir, "after_val.txt")

	beforeScript := filepath.Join(tmpDir, "beforeValidate.sh")
	_ = os.WriteFile(beforeScript, []byte(fmt.Sprintf("#!/bin/bash\necho \"FLYWAY_OPERATION=$FLYWAY_OPERATION\" > %s\nexit 0\n", beforeFile)), 0755)

	afterScript := filepath.Join(tmpDir, "afterValidate.sh")
	_ = os.WriteFile(afterScript, []byte(fmt.Sprintf("#!/bin/bash\necho \"FLYWAY_OPERATION=$FLYWAY_OPERATION\" > %s\nexit 0\n", afterFile)), 0755)

	sqlMigration := filepath.Join(tmpDir, "V1__init.sql")
	_ = os.WriteFile(sqlMigration, []byte("CREATE TABLE t (id INT64);"), 0644)

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.Locations = []string{tmpDir}
	cfg.ValidateOnMigrate = true

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	_, err = m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	bData, err := os.ReadFile(beforeFile)
	if err != nil {
		t.Fatalf("expected beforeValidate.sh to run: %v", err)
	}
	if !strings.Contains(string(bData), "FLYWAY_OPERATION=MIGRATE") {
		t.Errorf("expected beforeValidate to get FLYWAY_OPERATION=MIGRATE, got %s", string(bData))
	}

	aData, err := os.ReadFile(afterFile)
	if err != nil {
		t.Fatalf("expected afterValidate.sh to run: %v", err)
	}
	if !strings.Contains(string(aData), "FLYWAY_OPERATION=MIGRATE") {
		t.Errorf("expected afterValidate to get FLYWAY_OPERATION=MIGRATE, got %s", string(aData))
	}
}

func TestScriptMigration_EnvironmentVariables(t *testing.T) {
	ctx := context.Background()
	db := mock.NewMockDatabase()
	tmpDir := t.TempDir()

	outFile := filepath.Join(tmpDir, "script_env.txt")
	scriptFile := filepath.Join(tmpDir, "V1__script.sh")
	scriptContent := fmt.Sprintf(`#!/bin/bash
echo "URL=$FLYWAY_URL" > %s
echo "PWD=$FLYWAY_PASSWORD" >> %s
echo "USER=$FLYWAY_USER" >> %s
echo "APP=$MY_PFX_app_id_MY_SFX" >> %s
echo "ENV=$MY_PFX_flyway_environment_MY_SFX" >> %s
exit 0
`, outFile, outFile, outFile, outFile, outFile)
	_ = os.WriteFile(scriptFile, []byte(scriptContent), 0755)

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.Locations = []string{tmpDir}
	cfg.URL = "jdbc:spanner://localhost/projects/p/instances/i/databases/d"
	cfg.Password = "secretpass"
	cfg.User = "custom_user"
	cfg.Environment = "staging"
	cfg.ScriptPlaceholderPrefix = "MY_PFX_"
	cfg.ScriptPlaceholderSuffix = "_MY_SFX"
	cfg.Placeholders = map[string]string{
		"app_id": "12345",
	}

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	_, err = m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	outData, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	content := string(outData)
	if !strings.Contains(content, "URL=jdbc:spanner://localhost/projects/p/instances/i/databases/d") {
		t.Errorf("expected FLYWAY_URL in script env, got: %s", content)
	}
	if !strings.Contains(content, "PWD=secretpass") {
		t.Errorf("expected FLYWAY_PASSWORD in script env, got: %s", content)
	}
	if !strings.Contains(content, "USER=custom_user") {
		t.Errorf("expected FLYWAY_USER=custom_user in script env, got: %s", content)
	}
	if !strings.Contains(content, "APP=12345") {
		t.Errorf("expected custom script placeholder in script env, got: %s", content)
	}
	if !strings.Contains(content, "ENV=staging") {
		t.Errorf("expected flyway_environment in script env, got: %s", content)
	}

	// Also verify that when User is not set, FLYWAY_USER does NOT fall back to "flyway"
	outFileNoUser := filepath.Join(tmpDir, "script_nouser.txt")
	scriptFile2 := filepath.Join(tmpDir, "V2__nouser.sh")
	scriptContent2 := fmt.Sprintf("#!/bin/bash\necho \"USER=${FLYWAY_USER:-none}\" > %s\nexit 0\n", outFileNoUser)
	_ = os.WriteFile(scriptFile2, []byte(scriptContent2), 0755)

	cfg2 := config.NewDefaultConfiguration()
	cfg2.DefaultSchema = "test_ds"
	cfg2.Locations = []string{tmpDir}
	m2, err := New(cfg2, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}
	_, err = m2.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
	outData2, _ := os.ReadFile(outFileNoUser)
	if strings.Contains(string(outData2), "USER=flyway") {
		t.Errorf("expected FLYWAY_USER not to default to 'flyway' when unknown, got: %s", string(outData2))
	}
}




