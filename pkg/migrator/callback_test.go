package migrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database/mock"
	"github.com/RoryQ/noway/pkg/parser"
	"github.com/RoryQ/noway/pkg/resolver"
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
echo "FP__region=$FP__region" >> ` + outFile + `
echo "FLYWAY_PLACEHOLDER_region=$FLYWAY_PLACEHOLDER_region" >> ` + outFile + `
echo "NOWAY_PLACEHOLDER_region=$NOWAY_PLACEHOLDER_region" >> ` + outFile + `
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
		"FP__region=us-central1",
		"FLYWAY_PLACEHOLDER_region=us-central1",
		"NOWAY_PLACEHOLDER_region=us-central1",
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
if os.environ.get("FP__cluster") != "prod-cluster-1":
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
