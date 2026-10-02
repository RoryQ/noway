package migrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database"
	"github.com/RoryQ/noway/pkg/database/mock"
	"github.com/RoryQ/noway/pkg/parser"
	"github.com/RoryQ/noway/pkg/resolver"
)

// -----------------------------------------------------------------------------
// ADVERSARIAL VERIFICATION 1: PYTHON SCRIPT CALLBACK EXECUTION VIA python3
// -----------------------------------------------------------------------------

// TestChallenger_PythonScriptExecution_Python3 verifies that .py callback scripts
// are dispatched via python3, verify Python version, produce side effects, and
// work for both in-memory and physical on-disk files.
func TestChallenger_PythonScriptExecution_Python3(t *testing.T) {
	ctx := context.Background()

	t.Run("Python3_Interpreter_Verification_And_Side_Effect", func(t *testing.T) {
		tmpDir := t.TempDir()
		markerFile := filepath.Join(tmpDir, "python_marker.json")

		pyScript := fmt.Sprintf(`import sys, json, os
assert sys.version_info[0] == 3, "Must be Python 3"
data = {
    "version_major": sys.version_info[0],
    "executable": sys.executable,
    "event": os.environ.get("FLYWAY_EVENT"),
    "operation": os.environ.get("FLYWAY_OPERATION")
}
with open(%q, "w") as f:
    json.dump(data, f)
sys.exit(0)
`, markerFile)

		db := mock.NewMockDatabase()
		bqParser := parser.NewBigQueryParser()
		replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{Enabled: false})
		builtins := parser.BuiltinPlaceholders{DefaultSchema: "ds_test"}

		callbacks := map[string][]resolver.ResolvedCallback{
			"beforeMigrate": {
				{
					Event:    "beforeMigrate",
					Filename: "beforeMigrate__test.py",
					Content:  pyScript,
				},
			},
		}

		runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)
		runner.SetOperation("MIGRATE")

		err := runner.Fire(ctx, "beforeMigrate")
		if err != nil {
			t.Fatalf("Fire(beforeMigrate) python script failed: %v", err)
		}

		raw, err := os.ReadFile(markerFile)
		if err != nil {
			t.Fatalf("failed to read marker file produced by python: %v", err)
		}

		var payload map[string]interface{}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("invalid json payload from python script: %v", err)
		}

		if payload["version_major"] != float64(3) {
			t.Errorf("expected python version_major 3, got %v", payload["version_major"])
		}
		if payload["event"] != "beforeMigrate" {
			t.Errorf("expected FLYWAY_EVENT 'beforeMigrate', got %v", payload["event"])
		}
		if payload["operation"] != "MIGRATE" {
			t.Errorf("expected FLYWAY_OPERATION 'MIGRATE', got %v", payload["operation"])
		}
	})

	t.Run("Python3_Physical_Location_On_Disk", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "afterMigrate__audit.py")
		outPath := filepath.Join(tmpDir, "physical_out.txt")

		scriptContent := fmt.Sprintf(`import sys, os
with open(%q, "w") as f:
    f.write("PHYSICAL_PYTHON_SUCCESS:" + os.environ.get("FLYWAY_CALLBACK"))
sys.exit(0)
`, outPath)

		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
			t.Fatalf("failed writing physical python script: %v", err)
		}

		db := mock.NewMockDatabase()
		bqParser := parser.NewBigQueryParser()
		replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{Enabled: false})
		builtins := parser.BuiltinPlaceholders{DefaultSchema: "ds_test"}

		callbacks := map[string][]resolver.ResolvedCallback{
			"afterMigrate": {
				{
					Event:            "afterMigrate",
					Filename:         "afterMigrate__audit.py",
					PhysicalLocation: scriptPath,
					Content:          scriptContent,
				},
			},
		}

		runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)
		runner.SetOperation("MIGRATE")

		if err := runner.Fire(ctx, "afterMigrate"); err != nil {
			t.Fatalf("Fire(afterMigrate) physical python script failed: %v", err)
		}

		content, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("failed reading output from physical python script: %v", err)
		}

		expected := "PHYSICAL_PYTHON_SUCCESS:afterMigrate__audit.py"
		if string(content) != expected {
			t.Errorf("expected %q, got %q", expected, string(content))
		}
	})

	t.Run("Python3_NonZeroExit_ReturnsErrorAndCapturesStderr", func(t *testing.T) {
		pyScript := `import sys
sys.stderr.write("CRITICAL PYTHON FAILURE: exit code 42\n")
sys.exit(42)
`
		db := mock.NewMockDatabase()
		bqParser := parser.NewBigQueryParser()
		replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{Enabled: false})
		builtins := parser.BuiltinPlaceholders{DefaultSchema: "ds_test"}

		callbacks := map[string][]resolver.ResolvedCallback{
			"beforeMigrate": {
				{
					Event:    "beforeMigrate",
					Filename: "beforeMigrate__fail.py",
					Content:  pyScript,
				},
			},
		}

		runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)
		err := runner.Fire(ctx, "beforeMigrate")
		if err == nil {
			t.Fatalf("expected error from failing python callback, got nil")
		}

		if !strings.Contains(err.Error(), "CRITICAL PYTHON FAILURE: exit code 42") {
			t.Errorf("expected error to contain script stderr output, got: %v", err)
		}
	})
}

// -----------------------------------------------------------------------------
// ADVERSARIAL VERIFICATION 2: ENVIRONMENT VARIABLES IN SCRIPT EXECUTION
// -----------------------------------------------------------------------------

// TestChallenger_ScriptEnvironmentVariables_Comprehensive verifies that
// FLYWAY_OPERATION, FLYWAY_DATABASE, FLYWAY_SCHEMAS, FP__*, and FLYWAY_PLACEHOLDER_*
// are fully and accurately populated for script execution.
func TestChallenger_ScriptEnvironmentVariables_Comprehensive(t *testing.T) {
	ctx := context.Background()

	t.Run("All_Required_Environment_Variables_Populated", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonDumpPath := filepath.Join(tmpDir, "env_dump.json")

		pyScript := fmt.Sprintf(`import os, json, sys
data = dict(os.environ)
with open(%q, "w") as f:
    json.dump(data, f)
sys.exit(0)
`, jsonDumpPath)

		db := mock.NewMockDatabase()
		bqParser := parser.NewBigQueryParser()
		replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
			Enabled: true,
			Prefix:  "${",
			Suffix:  "}",
			Values: map[string]string{
				"cluster":     "us-central1-b",
				"app_version": "v2.5.0",
				"MAX_RETRIES": "5",
				"special_val": "a=b,c;d",
			},
		})
		builtins := parser.BuiltinPlaceholders{
			DefaultSchema: "analytics_prod",
			Table:         "custom_schema_history",
			User:          "cicd_deployer@service.iam.gserviceaccount.com",
			Database:      "bigquery_proj_123",
		}

		callbacks := map[string][]resolver.ResolvedCallback{
			"beforeMigrate": {
				{
					Event:    "beforeMigrate",
					Filename: "beforeMigrate__dump_env.py",
					Content:  pyScript,
				},
			},
		}

		cfg := &config.Configuration{
			DefaultSchema:        "analytics_prod",
			Table:                "custom_schema_history",
			Schemas:              []string{"analytics_prod", "analytics_raw", "analytics_staging"},
			GCPProjectID:         "bigquery_proj_123",
			GCPDataset:           "analytics_prod",
			GCPLocation:          "US",
			GCPBigQueryEndpoint:  "https://bigquery.googleapis.com",
			Placeholders: map[string]string{
				"cluster":     "us-central1-b",
				"app_version": "v2.5.0",
				"MAX_RETRIES": "5",
				"special_val": "a=b,c;d",
			},
		}

		runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)
		runner.SetConfig(cfg)
		runner.SetOperation("MIGRATE")

		if err := runner.Fire(ctx, "beforeMigrate"); err != nil {
			t.Fatalf("Fire(beforeMigrate) script callback failed: %v", err)
		}

		raw, err := os.ReadFile(jsonDumpPath)
		if err != nil {
			t.Fatalf("failed reading env dump: %v", err)
		}

		var envVars map[string]string
		if err := json.Unmarshal(raw, &envVars); err != nil {
			t.Fatalf("failed unmarshaling env json: %v", err)
		}

		// 1. Core Flyway variables
		expectedCore := map[string]string{
			"FLYWAY_OPERATION":          "MIGRATE",
			"NOWAY_OPERATION":           "MIGRATE",
			"FLYWAY_DATABASE":           "bigquery_proj_123",
			"NOWAY_DATABASE":            "bigquery_proj_123",
			"FLYWAY_SCHEMAS":            "analytics_prod,analytics_raw,analytics_staging",
			"NOWAY_SCHEMAS":             "analytics_prod,analytics_raw,analytics_staging",
			"FLYWAY_DEFAULT_SCHEMA":     "analytics_prod",
			"NOWAY_DEFAULT_SCHEMA":      "analytics_prod",
			"FLYWAY_TABLE":              "custom_schema_history",
			"NOWAY_TABLE":               "custom_schema_history",
			"FLYWAY_USER":               "cicd_deployer@service.iam.gserviceaccount.com",
			"NOWAY_USER":                "cicd_deployer@service.iam.gserviceaccount.com",
			"FLYWAY_EVENT":              "beforeMigrate",
			"NOWAY_EVENT":               "beforeMigrate",
			"FLYWAY_CALLBACK":           "beforeMigrate__dump_env.py",
			"NOWAY_CALLBACK":            "beforeMigrate__dump_env.py",
			"FLYWAY_GCP_PROJECT_ID":     "bigquery_proj_123",
			"NOWAY_GCP_PROJECT_ID":      "bigquery_proj_123",
			"FLYWAY_GCP_DATASET":        "analytics_prod",
			"NOWAY_GCP_DATASET":         "analytics_prod",
			"FLYWAY_GCP_LOCATION":       "US",
			"NOWAY_GCP_LOCATION":        "US",
			"FLYWAY_BIGQUERY_ENDPOINT":  "https://bigquery.googleapis.com",
			"NOWAY_BIGQUERY_ENDPOINT":   "https://bigquery.googleapis.com",
		}

		for k, expectedVal := range expectedCore {
			actualVal, exists := envVars[k]
			if !exists {
				t.Errorf("missing expected environment variable: %s", k)
			} else if actualVal != expectedVal {
				t.Errorf("environment variable %s = %q, want %q", k, actualVal, expectedVal)
			}
		}

		// 2. Builtin FP__ placeholders
		expectedBuiltinFP := map[string]string{
			"FP__flyway_defaultSchema": "analytics_prod",
			"FP__flyway_table":         "custom_schema_history",
			"FP__flyway_user":          "cicd_deployer@service.iam.gserviceaccount.com",
			"FP__flyway_database":      "bigquery_proj_123",
		}

		for k, expectedVal := range expectedBuiltinFP {
			actualVal, exists := envVars[k]
			if !exists {
				t.Errorf("missing expected builtin placeholder env: %s", k)
			} else if actualVal != expectedVal {
				t.Errorf("placeholder %s = %q, want %q", k, actualVal, expectedVal)
			}
		}

		// 3. Custom placeholders (FP__*, FLYWAY_PLACEHOLDER_*, NOWAY_PLACEHOLDER_*)
		customPlaceholders := map[string]string{
			"cluster":     "us-central1-b",
			"app_version": "v2.5.0",
			"MAX_RETRIES": "5",
			"special_val": "a=b,c;d",
		}

		for k, v := range customPlaceholders {
			fpKey := "FP__" + k
			flywayKey := "FLYWAY_PLACEHOLDER_" + k
			nowayKey := "NOWAY_PLACEHOLDER_" + k

			if envVars[fpKey] != v {
				t.Errorf("expected %s = %q, got %q", fpKey, v, envVars[fpKey])
			}
			if envVars[flywayKey] != v {
				t.Errorf("expected %s = %q, got %q", flywayKey, v, envVars[flywayKey])
			}
			if envVars[nowayKey] != v {
				t.Errorf("expected %s = %q, got %q", nowayKey, v, envVars[nowayKey])
			}
		}
	})

	t.Run("Operations_Set_Accurately_In_Migrate_Validate_Info", func(t *testing.T) {
		operations := []struct {
			name   string
			event  string
			runner func(t *testing.T, dumpPath string)
		}{
			{
				name:  "MIGRATE",
				event: "beforeMigrate",
				runner: func(t *testing.T, dumpPath string) {
					cfg := config.NewDefaultConfiguration()
					cfg.DefaultSchema = "test_ds"
					db := mock.NewMockDatabase()

					migDir := t.TempDir()
					pyScript := fmt.Sprintf(`import os, sys
with open(%q, "w") as f:
    f.write(os.environ.get("FLYWAY_OPERATION", ""))
sys.exit(0)
`, dumpPath)
					if err := os.WriteFile(filepath.Join(migDir, "beforeMigrate__check.py"), []byte(pyScript), 0644); err != nil {
						t.Fatal(err)
					}
					cfg.Locations = []string{"filesystem:" + migDir}

					m, err := New(cfg, db)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := m.Migrate(context.Background()); err != nil {
						t.Fatal(err)
					}
				},
			},
			{
				name:  "VALIDATE",
				event: "beforeValidate",
				runner: func(t *testing.T, dumpPath string) {
					cfg := config.NewDefaultConfiguration()
					cfg.DefaultSchema = "test_ds"
					db := mock.NewMockDatabase()

					migDir := t.TempDir()
					pyScript := fmt.Sprintf(`import os, sys
with open(%q, "w") as f:
    f.write(os.environ.get("FLYWAY_OPERATION", ""))
sys.exit(0)
`, dumpPath)
					if err := os.WriteFile(filepath.Join(migDir, "beforeValidate__check.py"), []byte(pyScript), 0644); err != nil {
						t.Fatal(err)
					}
					cfg.Locations = []string{"filesystem:" + migDir}

					m, err := New(cfg, db)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := m.Validate(context.Background()); err != nil {
						t.Fatal(err)
					}
				},
			},
			{
				name:  "INFO",
				event: "beforeInfo",
				runner: func(t *testing.T, dumpPath string) {
					cfg := config.NewDefaultConfiguration()
					cfg.DefaultSchema = "test_ds"
					db := mock.NewMockDatabase()

					migDir := t.TempDir()
					pyScript := fmt.Sprintf(`import os, sys
with open(%q, "w") as f:
    f.write(os.environ.get("FLYWAY_OPERATION", ""))
sys.exit(0)
`, dumpPath)
					if err := os.WriteFile(filepath.Join(migDir, "beforeInfo__check.py"), []byte(pyScript), 0644); err != nil {
						t.Fatal(err)
					}
					cfg.Locations = []string{"filesystem:" + migDir}

					m, err := New(cfg, db)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := m.Info(context.Background()); err != nil {
						t.Fatal(err)
					}
				},
			},
		}

		for _, op := range operations {
			t.Run(op.name, func(t *testing.T) {
				tmpDir := t.TempDir()
				dumpFile := filepath.Join(tmpDir, "op_dump.txt")
				op.runner(t, dumpFile)

				content, err := os.ReadFile(dumpFile)
				if err != nil {
					t.Fatalf("failed reading op dump for %s: %v", op.name, err)
				}
				if string(content) != op.name {
					t.Errorf("FLYWAY_OPERATION for %s = %q, want %q", op.name, string(content), op.name)
				}
			})
		}
	})
}

// -----------------------------------------------------------------------------
// ADVERSARIAL VERIFICATION 3: ERROR SEMANTICS AND ERROR MASKING HARDENING
// -----------------------------------------------------------------------------

// TestChallenger_ErrorSemantics_RootErrorNotMasked tests that:
// 1. A failing callback halts execution immediately.
// 2. Error hooks (afterMigrateError, afterValidateError, afterInfoError) execute on error.
// 3. When an error hook executes, even if it ALSO fails, the ROOT error is returned intact.
func TestChallenger_ErrorSemantics_RootErrorNotMasked(t *testing.T) {
	ctx := context.Background()

	t.Run("beforeMigrate_Fails_And_afterMigrateError_Succeeds_ReturnsRootError", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		db := mock.NewMockDatabase()

		migDir := t.TempDir()
		errorFlagFile := filepath.Join(migDir, "after_error_ran.txt")

		// 1. Failing beforeMigrate callback
		pyFail := `import sys
sys.stderr.write("ROOT_ERROR_BEFORE_MIGRATE_CRASH\n")
sys.exit(101)
`
		if err := os.WriteFile(filepath.Join(migDir, "beforeMigrate__crash.py"), []byte(pyFail), 0644); err != nil {
			t.Fatal(err)
		}

		// 2. afterMigrateError callback that succeeds and writes marker
		pyAfterError := fmt.Sprintf(`import sys
with open(%q, "w") as f:
    f.write("AFTER_MIGRATE_ERROR_RECORDED")
sys.exit(0)
`, errorFlagFile)
		if err := os.WriteFile(filepath.Join(migDir, "afterMigrateError__notify.py"), []byte(pyAfterError), 0644); err != nil {
			t.Fatal(err)
		}

		// 3. A versioned migration that must NEVER run
		sqlMig := "CREATE TABLE never_run (id INT64);"
		if err := os.WriteFile(filepath.Join(migDir, "V1__never.sql"), []byte(sqlMig), 0644); err != nil {
			t.Fatal(err)
		}

		cfg.Locations = []string{"filesystem:" + migDir}
		m, err := New(cfg, db)
		if err != nil {
			t.Fatal(err)
		}

		result, err := m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error from Migrate(), got nil")
		}
		if result != nil && result.Success {
			t.Fatalf("expected result.Success to be false or result nil, got true")
		}

		// Verify root error is returned
		if !strings.Contains(err.Error(), "ROOT_ERROR_BEFORE_MIGRATE_CRASH") {
			t.Errorf("expected root error in returned err, got: %v", err)
		}

		// Verify afterMigrateError fired
		if _, err := os.Stat(errorFlagFile); os.IsNotExist(err) {
			t.Errorf("afterMigrateError was not triggered!")
		}

		// Verify versioned migration did NOT execute
		for _, stmt := range db.ExecutedStatements() {
			if strings.Contains(stmt, "never_run") {
				t.Errorf("migration statement was executed despite beforeMigrate failure!")
			}
		}
	})

	t.Run("beforeMigrate_Fails_And_afterMigrateError_ALSO_Fails_RootErrorNotMasked", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		db := mock.NewMockDatabase()

		migDir := t.TempDir()

		// 1. Root failure in beforeMigrate
		pyFail := `import sys
sys.stderr.write("ROOT_ERROR_PRIMARY_CAUSE\n")
sys.exit(102)
`
		if err := os.WriteFile(filepath.Join(migDir, "beforeMigrate__root_fail.py"), []byte(pyFail), 0644); err != nil {
			t.Fatal(err)
		}

		// 2. afterMigrateError ALSO fails with a distinct exit code
		pyAfterErrorFail := `import sys
sys.stderr.write("SECONDARY_ERROR_HOOK_CRASH\n")
sys.exit(103)
`
		if err := os.WriteFile(filepath.Join(migDir, "afterMigrateError__secondary_fail.py"), []byte(pyAfterErrorFail), 0644); err != nil {
			t.Fatal(err)
		}

		cfg.Locations = []string{"filesystem:" + migDir}
		m, err := New(cfg, db)
		if err != nil {
			t.Fatal(err)
		}

		_, err = m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error, got nil")
		}

		// The root error MUST NOT BE MASKED by afterMigrateError!
		if !strings.Contains(err.Error(), "ROOT_ERROR_PRIMARY_CAUSE") {
			t.Errorf("root error was masked! Expected ROOT_ERROR_PRIMARY_CAUSE, got: %v", err)
		}
	})

	t.Run("Migration_SQL_Failure_Fires_afterMigrateError_And_DoesNotMaskRootSQLError", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		db := mock.NewMockDatabase()
		db.SetFailOnExecute("BAD_SYNTAX_TRIGGER")

		migDir := t.TempDir()
		hookFiredFile := filepath.Join(migDir, "after_error_sql_hook.txt")

		pyHook := fmt.Sprintf(`import sys
with open(%q, "w") as f:
    f.write("ERROR_HOOK_SUCCESS")
sys.exit(0)
`, hookFiredFile)
		if err := os.WriteFile(filepath.Join(migDir, "afterMigrateError__notify.py"), []byte(pyHook), 0644); err != nil {
			t.Fatal(err)
		}

		sqlMig := "BAD_SYNTAX_TRIGGER;"
		if err := os.WriteFile(filepath.Join(migDir, "V1__bad_sql.sql"), []byte(sqlMig), 0644); err != nil {
			t.Fatal(err)
		}

		cfg.Locations = []string{"filesystem:" + migDir}
		m, err := New(cfg, db)
		if err != nil {
			t.Fatal(err)
		}

		result, err := m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error from Migrate() with bad SQL, got nil")
		}
		if result != nil && result.Success {
			t.Errorf("expected result.Success = false")
		}

		if !strings.Contains(err.Error(), "BAD_SYNTAX_TRIGGER") {
			t.Errorf("expected returned error to contain root SQL error, got: %v", err)
		}

		if _, err := os.Stat(hookFiredFile); os.IsNotExist(err) {
			t.Errorf("afterMigrateError hook was not fired on SQL failure!")
		}
	})

	t.Run("Validate_Fails_And_afterValidateError_Runs_RootErrorPreserved", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		db := mock.NewMockDatabase()

		migDir := t.TempDir()

		// Failing beforeValidate
		pyFail := `import sys
sys.stderr.write("ROOT_ERROR_BEFORE_VALIDATE_CRASH\n")
sys.exit(104)
`
		if err := os.WriteFile(filepath.Join(migDir, "beforeValidate__fail.py"), []byte(pyFail), 0644); err != nil {
			t.Fatal(err)
		}

		// Failing afterValidateError
		pyAfterErrorFail := `import sys
sys.stderr.write("AFTER_VALIDATE_ERROR_CRASH\n")
sys.exit(105)
`
		if err := os.WriteFile(filepath.Join(migDir, "afterValidateError__fail.py"), []byte(pyAfterErrorFail), 0644); err != nil {
			t.Fatal(err)
		}

		cfg.Locations = []string{"filesystem:" + migDir}
		m, err := New(cfg, db)
		if err != nil {
			t.Fatal(err)
		}

		_, err = m.Validate(ctx)
		if err == nil {
			t.Fatalf("expected error from Validate(), got nil")
		}

		if !strings.Contains(err.Error(), "ROOT_ERROR_BEFORE_VALIDATE_CRASH") {
			t.Errorf("root error was masked in Validate()! Got: %v", err)
		}
	})

	t.Run("Info_Fails_And_afterInfoError_Runs_RootErrorPreserved", func(t *testing.T) {
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		db := mock.NewMockDatabase()

		migDir := t.TempDir()

		// Failing beforeInfo
		pyFail := `import sys
sys.stderr.write("ROOT_ERROR_BEFORE_INFO_CRASH\n")
sys.exit(106)
`
		if err := os.WriteFile(filepath.Join(migDir, "beforeInfo__fail.py"), []byte(pyFail), 0644); err != nil {
			t.Fatal(err)
		}

		// Failing afterInfoError
		pyAfterErrorFail := `import sys
sys.stderr.write("AFTER_INFO_ERROR_CRASH\n")
sys.exit(107)
`
		if err := os.WriteFile(filepath.Join(migDir, "afterInfoError__fail.py"), []byte(pyAfterErrorFail), 0644); err != nil {
			t.Fatal(err)
		}

		cfg.Locations = []string{"filesystem:" + migDir}
		m, err := New(cfg, db)
		if err != nil {
			t.Fatal(err)
		}

		_, err = m.Info(ctx)
		if err == nil {
			t.Fatalf("expected error from Info(), got nil")
		}

		if !strings.Contains(err.Error(), "ROOT_ERROR_BEFORE_INFO_CRASH") {
			t.Errorf("root error was masked in Info()! Got: %v", err)
		}
	})

	t.Run("Mid_Lifecycle_Failures_Halt_Subsequent_Phases", func(t *testing.T) {
		// Test afterVersioned failure halting beforeRepeatables and repeatables
		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		db := mock.NewMockDatabase()

		migDir := t.TempDir()

		// V1 migration
		if err := os.WriteFile(filepath.Join(migDir, "V1__init.sql"), []byte("CREATE TABLE t1 (id INT64);"), 0644); err != nil {
			t.Fatal(err)
		}

		// Failing afterVersioned callback
		pyFail := `import sys
sys.stderr.write("AFTER_VERSIONED_HALT\n")
sys.exit(108)
`
		if err := os.WriteFile(filepath.Join(migDir, "afterVersioned__stop.py"), []byte(pyFail), 0644); err != nil {
			t.Fatal(err)
		}

		// Repeatable migration that should NOT execute
		if err := os.WriteFile(filepath.Join(migDir, "R__repeat.sql"), []byte("CREATE TABLE r_never (id INT64);"), 0644); err != nil {
			t.Fatal(err)
		}

		cfg.Locations = []string{"filesystem:" + migDir}
		m, err := New(cfg, db)
		if err != nil {
			t.Fatal(err)
		}

		_, err = m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error from afterVersioned failure, got nil")
		}

		if !strings.Contains(err.Error(), "AFTER_VERSIONED_HALT") {
			t.Errorf("expected AFTER_VERSIONED_HALT error, got: %v", err)
		}

		for _, stmt := range db.ExecutedStatements() {
			if strings.Contains(stmt, "r_never") {
				t.Errorf("repeatable migration was executed despite afterVersioned failure!")
			}
		}
	})
}

// -----------------------------------------------------------------------------
// ADVERSARIAL VERIFICATION 4: STATEMENT-LEVEL HOOKS ORDER & EXECUTION
// -----------------------------------------------------------------------------

// chronologicalRecorder records the exact interleaved sequence of callback events
// and SQL statement executions.
type chronologicalRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *chronologicalRecorder) Record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *chronologicalRecorder) Events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make([]string, len(r.events))
	copy(copied, r.events)
	return copied
}

// recordingDatabase decorates Database to record statement executions in chronologicalRecorder.
type recordingDatabase struct {
	database.Database
	recorder *chronologicalRecorder
}

func (d *recordingDatabase) ExecuteStatement(ctx context.Context, sql string) error {
	d.recorder.Record("EXECUTE_SQL: " + strings.TrimSpace(sql))
	return d.Database.ExecuteStatement(ctx, sql)
}

// TestChallenger_StatementLevelHooks_SequenceAndErrors verifies that
// beforeEachMigrateStatement, afterEachMigrateStatement, and afterEachMigrateStatementError
// fire in strict statement-by-statement order, and failures halt subsequent statements.
func TestChallenger_StatementLevelHooks_SequenceAndErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("MultiStatement_Migration_Executes_Hooks_In_Strict_Order", func(t *testing.T) {
		recorder := &chronologicalRecorder{}
		mockDB := mock.NewMockDatabase()
		recDB := &recordingDatabase{Database: mockDB, recorder: recorder}

		// Define 3 statements
		stmtsSQL := `
CREATE TABLE s1 (id INT64);
CREATE TABLE s2 (name STRING);
CREATE TABLE s3 (active BOOL);
`
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "V1__multi_stmt.sql"), []byte(stmtsSQL), 0644); err != nil {
			t.Fatal(err)
		}

		// Register hooks that write to recorder via temporary marker scripts
		// Since callbacks run in-process, we can write Python scripts that invoke a local callback
		// Or we can register SQL callbacks that recDB will record!
		// Even simpler: SQL callbacks are executed by recDB.ExecuteStatement!
		// beforeEachMigrateStatement: SELECT 'HOOK_beforeEachMigrateStatement';
		// afterEachMigrateStatement: SELECT 'HOOK_afterEachMigrateStatement';
		if err := os.WriteFile(filepath.Join(tmpDir, "beforeEachMigrate.sql"), []byte("SELECT 'HOOK_beforeEachMigrate';"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "beforeEachMigrateStatement.sql"), []byte("SELECT 'HOOK_beforeEachMigrateStatement';"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "afterEachMigrateStatement.sql"), []byte("SELECT 'HOOK_afterEachMigrateStatement';"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "afterEachMigrate.sql"), []byte("SELECT 'HOOK_afterEachMigrate';"), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.Locations = []string{"filesystem:" + tmpDir}
		m, err := New(cfg, recDB)
		if err != nil {
			t.Fatal(err)
		}

		result, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate() unexpected error: %v", err)
		}
		if result.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed, got %d", result.MigrationsExecuted)
		}

		events := recorder.Events()

		// Filter events of interest (ignore history table DDL if any)
		var sequence []string
		for _, e := range events {
			if strings.Contains(e, "HOOK_") || strings.Contains(e, "CREATE TABLE s") {
				sequence = append(sequence, e)
			}
		}

		expectedSequence := []string{
			"EXECUTE_SQL: SELECT 'HOOK_beforeEachMigrate'",
			"EXECUTE_SQL: SELECT 'HOOK_beforeEachMigrateStatement'",
			"EXECUTE_SQL: CREATE TABLE s1 (id INT64)",
			"EXECUTE_SQL: SELECT 'HOOK_afterEachMigrateStatement'",
			"EXECUTE_SQL: SELECT 'HOOK_beforeEachMigrateStatement'",
			"EXECUTE_SQL: CREATE TABLE s2 (name STRING)",
			"EXECUTE_SQL: SELECT 'HOOK_afterEachMigrateStatement'",
			"EXECUTE_SQL: SELECT 'HOOK_beforeEachMigrateStatement'",
			"EXECUTE_SQL: CREATE TABLE s3 (active BOOL)",
			"EXECUTE_SQL: SELECT 'HOOK_afterEachMigrateStatement'",
			"EXECUTE_SQL: SELECT 'HOOK_afterEachMigrate'",
		}

		if len(sequence) != len(expectedSequence) {
			t.Fatalf("sequence length mismatch: expected %d, got %d\nActual:\n%s",
				len(expectedSequence), len(sequence), strings.Join(sequence, "\n"))
		}

		for i, expected := range expectedSequence {
			if sequence[i] != expected {
				t.Errorf("step %d mismatch: got %q, want %q", i, sequence[i], expected)
			}
		}
	})

	t.Run("Statement_Failure_Triggers_afterEachMigrateStatementError_And_Halts_Remaining_Statements", func(t *testing.T) {
		recorder := &chronologicalRecorder{}
		mockDB := mock.NewMockDatabase()
		// Fail when executing statement 2
		mockDB.SetFailOnExecute("FAIL_STMT_2")
		recDB := &recordingDatabase{Database: mockDB, recorder: recorder}

		tmpDir := t.TempDir()
		stmtsSQL := `
CREATE TABLE ok1 (id INT64);
CREATE TABLE FAIL_STMT_2 (id INT64);
CREATE TABLE ok3 (id INT64);
`
		if err := os.WriteFile(filepath.Join(tmpDir, "V1__fail_stmt.sql"), []byte(stmtsSQL), 0644); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(tmpDir, "beforeEachMigrateStatement.sql"), []byte("SELECT 'HOOK_beforeEachMigrateStatement';"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "afterEachMigrateStatement.sql"), []byte("SELECT 'HOOK_afterEachMigrateStatement';"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "afterEachMigrateStatementError.sql"), []byte("SELECT 'HOOK_afterEachMigrateStatementError';"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "afterEachMigrateError.sql"), []byte("SELECT 'HOOK_afterEachMigrateError';"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "afterMigrateError.sql"), []byte("SELECT 'HOOK_afterMigrateError';"), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.Locations = []string{"filesystem:" + tmpDir}
		m, err := New(cfg, recDB)
		if err != nil {
			t.Fatal(err)
		}

		result, err := m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error on statement failure, got nil")
		}
		if result != nil && result.Success {
			t.Errorf("expected result.Success = false")
		}

		events := recorder.Events()
		var sequence []string
		for _, e := range events {
			if strings.Contains(e, "HOOK_") || strings.Contains(e, "CREATE TABLE") {
				sequence = append(sequence, e)
			}
		}

		// Verify:
		// 1. ok1 executes successfully
		// 2. FAIL_STMT_2 attempts execution
		// 3. afterEachMigrateStatementError fires
		// 4. afterEachMigrateError fires
		// 5. afterMigrateError fires
		// 6. ok3 NEVER executed!
		// 7. afterEachMigrateStatement NEVER fired for FAIL_STMT_2!

		expectedSequence := []string{
			"EXECUTE_SQL: SELECT 'HOOK_beforeEachMigrateStatement'",
			"EXECUTE_SQL: CREATE TABLE ok1 (id INT64)",
			"EXECUTE_SQL: SELECT 'HOOK_afterEachMigrateStatement'",
			"EXECUTE_SQL: SELECT 'HOOK_beforeEachMigrateStatement'",
			"EXECUTE_SQL: CREATE TABLE FAIL_STMT_2 (id INT64)",
			"EXECUTE_SQL: SELECT 'HOOK_afterEachMigrateStatementError'",
			"EXECUTE_SQL: SELECT 'HOOK_afterEachMigrateError'",
			"EXECUTE_SQL: SELECT 'HOOK_afterMigrateError'",
		}

		if len(sequence) != len(expectedSequence) {
			t.Fatalf("sequence mismatch: expected %d events, got %d\nActual:\n%s",
				len(expectedSequence), len(sequence), strings.Join(sequence, "\n"))
		}

		for i, expected := range expectedSequence {
			if sequence[i] != expected {
				t.Errorf("step %d mismatch: got %q, want %q", i, sequence[i], expected)
			}
		}

		for _, e := range events {
			if strings.Contains(e, "ok3") {
				t.Fatalf("statement 3 (ok3) must NOT have executed, but found in events: %s", e)
			}
		}
	})

	t.Run("beforeEachMigrateStatement_Failure_Prevents_Statement_Execution", func(t *testing.T) {
		recorder := &chronologicalRecorder{}
		mockDB := mock.NewMockDatabase()
		recDB := &recordingDatabase{Database: mockDB, recorder: recorder}

		tmpDir := t.TempDir()
		stmtsSQL := "CREATE TABLE stmt_never_run (id INT64);"
		if err := os.WriteFile(filepath.Join(tmpDir, "V1__test.sql"), []byte(stmtsSQL), 0644); err != nil {
			t.Fatal(err)
		}

		// Python hook failing before statement runs
		pyFail := `import sys
sys.stderr.write("BEFORE_STATEMENT_HOOK_FAILED\n")
sys.exit(109)
`
		if err := os.WriteFile(filepath.Join(tmpDir, "beforeEachMigrateStatement__fail.py"), []byte(pyFail), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.Locations = []string{"filesystem:" + tmpDir}
		m, err := New(cfg, recDB)
		if err != nil {
			t.Fatal(err)
		}

		_, err = m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error from failing beforeEachMigrateStatement, got nil")
		}

		if !strings.Contains(err.Error(), "BEFORE_STATEMENT_HOOK_FAILED") {
			t.Errorf("expected root error BEFORE_STATEMENT_HOOK_FAILED, got: %v", err)
		}

		for _, e := range recorder.Events() {
			if strings.Contains(e, "stmt_never_run") {
				t.Fatalf("statement was executed despite beforeEachMigrateStatement failure!")
			}
		}
	})
}

// -----------------------------------------------------------------------------
// ADVERSARIAL VERIFICATION 5: MULTI-CALLBACK SHORT-CIRCUIT & STRESS TESTS
// -----------------------------------------------------------------------------

// TestChallenger_MultipleCallbacksPerEvent_DescriptionOrderAndShortCircuit tests that
// multiple callbacks for the same event execute in alphabetical description order,
// and if one fails, subsequent callbacks in that same event chain NEVER execute.
func TestChallenger_MultipleCallbacksPerEvent_DescriptionOrderAndShortCircuit(t *testing.T) {
	ctx := context.Background()

	t.Run("Description_Order_Execution_Across_Mixed_Script_And_SQL", func(t *testing.T) {
		tmpDir := t.TempDir()
		traceFile := filepath.Join(tmpDir, "trace.txt")

		// 1. First: beforeMigrate__01_first.py
		py1 := fmt.Sprintf(`import sys
with open(%q, "a") as f:
    f.write("01_FIRST\n")
sys.exit(0)
`, traceFile)
		if err := os.WriteFile(filepath.Join(tmpDir, "beforeMigrate__01_first.py"), []byte(py1), 0644); err != nil {
			t.Fatal(err)
		}

		// 2. Second: beforeMigrate__02_second.sh
		sh2 := fmt.Sprintf(`#!/bin/bash
echo "02_SECOND" >> %s
exit 0
`, traceFile)
		if err := os.WriteFile(filepath.Join(tmpDir, "beforeMigrate__02_second.sh"), []byte(sh2), 0644); err != nil {
			t.Fatal(err)
		}

		// 3. Third: beforeMigrate__03_third.py
		py3 := fmt.Sprintf(`import sys
with open(%q, "a") as f:
    f.write("03_THIRD\n")
sys.exit(0)
`, traceFile)
		if err := os.WriteFile(filepath.Join(tmpDir, "beforeMigrate__03_third.py"), []byte(py3), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.Locations = []string{"filesystem:" + tmpDir}
		db := mock.NewMockDatabase()

		m, err := New(cfg, db)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := m.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() unexpected error: %v", err)
		}

		raw, err := os.ReadFile(traceFile)
		if err != nil {
			t.Fatalf("failed reading trace file: %v", err)
		}

		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		expected := []string{"01_FIRST", "02_SECOND", "03_THIRD"}
		if len(lines) != len(expected) {
			t.Fatalf("expected 3 trace lines, got %d: %v", len(lines), lines)
		}
		for i, exp := range expected {
			if lines[i] != exp {
				t.Errorf("step %d: got %q, want %q", i, lines[i], exp)
			}
		}
	})

	t.Run("Middle_Callback_Failure_Short_Circuits_Subsequent_Callbacks_In_Same_Event", func(t *testing.T) {
		tmpDir := t.TempDir()
		traceFile := filepath.Join(tmpDir, "short_circuit_trace.txt")

		// Callback 1 succeeds
		py1 := fmt.Sprintf(`import sys
with open(%q, "a") as f:
    f.write("STEP_1_RAN\n")
sys.exit(0)
`, traceFile)
		if err := os.WriteFile(filepath.Join(tmpDir, "beforeMigrate__01.py"), []byte(py1), 0644); err != nil {
			t.Fatal(err)
		}

		// Callback 2 fails
		py2 := `import sys
sys.stderr.write("CALLBACK_2_BLOWUP\n")
sys.exit(88)
`
		if err := os.WriteFile(filepath.Join(tmpDir, "beforeMigrate__02.py"), []byte(py2), 0644); err != nil {
			t.Fatal(err)
		}

		// Callback 3 must NEVER run
		py3 := fmt.Sprintf(`import sys
with open(%q, "a") as f:
    f.write("STEP_3_NEVER\n")
sys.exit(0)
`, traceFile)
		if err := os.WriteFile(filepath.Join(tmpDir, "beforeMigrate__03.py"), []byte(py3), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.Locations = []string{"filesystem:" + tmpDir}
		db := mock.NewMockDatabase()

		m, err := New(cfg, db)
		if err != nil {
			t.Fatal(err)
		}

		_, err = m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error from failing callback 2, got nil")
		}

		if !strings.Contains(err.Error(), "CALLBACK_2_BLOWUP") {
			t.Errorf("expected CALLBACK_2_BLOWUP in error, got: %v", err)
		}

		raw, _ := os.ReadFile(traceFile)
		content := string(raw)

		if !strings.Contains(content, "STEP_1_RAN") {
			t.Errorf("expected callback 1 to have run")
		}
		if strings.Contains(content, "STEP_3_NEVER") {
			t.Fatalf("callback 3 executed despite callback 2 failure! Short-circuit failed!")
		}
	})
}

// TestChallenger_ScriptEnv_ComplexPlaceholderValues tests that placeholders
// containing special characters (spaces, dollar signs, quotes) are passed intact.
func TestChallenger_ScriptEnv_ComplexPlaceholderValues(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dumpPath := filepath.Join(tmpDir, "complex_env.json")

	pyScript := fmt.Sprintf(`import os, json, sys
data = {
    "spaced": os.environ.get("FP__spaced_var"),
    "dollar": os.environ.get("FP__dollar_var"),
    "quoted": os.environ.get("FP__quoted_var"),
}
with open(%q, "w") as f:
    json.dump(data, f)
sys.exit(0)
`, dumpPath)

	db := mock.NewMockDatabase()
	bqParser := parser.NewBigQueryParser()
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{Enabled: false})
	builtins := parser.BuiltinPlaceholders{DefaultSchema: "ds_test"}

	callbacks := map[string][]resolver.ResolvedCallback{
		"beforeMigrate": {
			{
				Event:    "beforeMigrate",
				Filename: "beforeMigrate__complex.py",
				Content:  pyScript,
			},
		},
	}

	cfg := &config.Configuration{
		Placeholders: map[string]string{
			"spaced_var": "hello world from test",
			"dollar_var": "$VAR_LITERAL$$100",
			"quoted_var": `"single'and"double"quotes`,
		},
	}

	runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)
	runner.SetConfig(cfg)

	if err := runner.Fire(ctx, "beforeMigrate"); err != nil {
		t.Fatalf("Fire(beforeMigrate) failed: %v", err)
	}

	raw, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("failed reading dump: %v", err)
	}

	var results map[string]string
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}

	if results["spaced"] != "hello world from test" {
		t.Errorf("spaced = %q, want %q", results["spaced"], "hello world from test")
	}
	if results["dollar"] != "$VAR_LITERAL$$100" {
		t.Errorf("dollar = %q, want %q", results["dollar"], "$VAR_LITERAL$$100")
	}
	if results["quoted"] != `"single'and"double"quotes` {
		t.Errorf("quoted = %q, want %q", results["quoted"], `"single'and"double"quotes`)
	}
}

// TestChallenger_ZeroMigrations_LifecycleEventPrecision verifies that when 0 migrations
// are pending, beforeMigrate and afterMigrate fire, but afterMigrateApplied, afterVersioned,
// and beforeRepeatables do NOT fire.
func TestChallenger_ZeroMigrations_LifecycleEventPrecision(t *testing.T) {
	ctx := context.Background()
	recorder := &chronologicalRecorder{}
	mockDB := mock.NewMockDatabase()
	recDB := &recordingDatabase{Database: mockDB, recorder: recorder}

	tmpDir := t.TempDir()

	// Register hooks for all events
	eventsToRegister := []string{
		"beforeMigrate",
		"afterVersioned",
		"beforeRepeatables",
		"afterMigrateApplied",
		"afterMigrate",
	}

	for _, evt := range eventsToRegister {
		sql := fmt.Sprintf("SELECT 'EVENT_%s';", evt)
		if err := os.WriteFile(filepath.Join(tmpDir, evt+".sql"), []byte(sql), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.Locations = []string{"filesystem:" + tmpDir}

	m, err := New(cfg, recDB)
	if err != nil {
		t.Fatal(err)
	}

	// Initial migration with 0 pending migrations
	result, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate() with 0 migrations failed: %v", err)
	}
	if result.MigrationsExecuted != 0 {
		t.Fatalf("expected 0 migrations executed, got %d", result.MigrationsExecuted)
	}

	events := recorder.Events()
	var executedHooks []string
	for _, e := range events {
		if strings.Contains(e, "EVENT_") {
			executedHooks = append(executedHooks, e)
		}
	}

	expectedHooks := []string{
		"EXECUTE_SQL: SELECT 'EVENT_beforeMigrate'",
		"EXECUTE_SQL: SELECT 'EVENT_afterMigrate'",
	}

	if len(executedHooks) != len(expectedHooks) {
		t.Fatalf("expected %d hooks executed for 0-migration run, got %d:\n%s",
			len(expectedHooks), len(executedHooks), strings.Join(executedHooks, "\n"))
	}

	for i, exp := range expectedHooks {
		if executedHooks[i] != exp {
			t.Errorf("step %d: got %q, want %q", i, executedHooks[i], exp)
		}
	}
}

// TestChallenger_ScriptCallback_LargeOutput tests that script callbacks
// generating substantial output (50KB+) execute and terminate cleanly.
func TestChallenger_ScriptCallback_LargeOutput(t *testing.T) {
	ctx := context.Background()

	pyScript := `import sys
# Generate 1000 lines of output (approx 50KB)
for i in range(1000):
    sys.stdout.write(f"Line {i}: padding output for buffer stress test verification\n")
sys.exit(0)
`
	db := mock.NewMockDatabase()
	bqParser := parser.NewBigQueryParser()
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{Enabled: false})
	builtins := parser.BuiltinPlaceholders{DefaultSchema: "ds_test"}

	callbacks := map[string][]resolver.ResolvedCallback{
		"beforeMigrate": {
			{
				Event:    "beforeMigrate",
				Filename: "beforeMigrate__large_output.py",
				Content:  pyScript,
			},
		},
	}

	runner := NewCallbackRunner(db, callbacks, bqParser, replacer, builtins)
	err := runner.Fire(ctx, "beforeMigrate")
	if err != nil {
		t.Fatalf("large output script callback failed: %v", err)
	}
}

