package migrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database/mock"
)

func TestFlywayJSON_MigrateResult(t *testing.T) {
	res := &MigrateResult{
		InitialSchemaVersion: "1.0",
		TargetSchemaVersion:  "2.0",
		SchemaName:           "my_schema",
		Migrations: []ExecutedSummary{
			{
				Category:      "Versioned",
				Version:       "2.0",
				Description:   "add users",
				Type:          "SQL",
				Script:        "V2_0__add_users.sql",
				ExecutionTime: 42,
			},
		},
		MigrationsExecuted: 1,
		Success:            true,
		FlywayVersion:      "1.0.0",
		Database:           "BigQuery",
		Warnings:           []string{},
		Operation:          "migrate",

		// Internal Go fields for backward compatibility
		InitialVersion:     "1.0",
		TargetVersion:      "2.0",
		ExecutedMigrations: []ExecutedSummary{},
		TotalExecutionTime: 42,
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal MigrateResult: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	expectedKeys := []string{
		"initialSchemaVersion",
		"targetSchemaVersion",
		"schemaName",
		"migrations",
		"migrationsExecuted",
		"success",
		"flywayVersion",
		"database",
		"warnings",
		"operation",
	}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("MigrateResult JSON missing expected Flyway key: %s", key)
		}
	}

	disallowedKeys := []string{
		"initialVersion",
		"targetVersion",
		"executedMigrations",
		"totalExecutionTime",
	}
	for _, key := range disallowedKeys {
		if _, ok := m[key]; ok {
			t.Errorf("MigrateResult JSON contains disallowed internal key: %s", key)
		}
	}

	// Verify migrations element keys
	migs, ok := m["migrations"].([]interface{})
	if !ok || len(migs) != 1 {
		t.Fatalf("expected migrations array of len 1, got %v", m["migrations"])
	}
	migItem := migs[0].(map[string]interface{})
	migExpectedKeys := []string{"category", "version", "description", "type", "filepath", "executionTime"}
	for _, key := range migExpectedKeys {
		if _, ok := migItem[key]; !ok {
			t.Errorf("MigrateOutput item missing expected Flyway key: %s", key)
		}
	}
	if migItem["filepath"] != "V2_0__add_users.sql" {
		t.Errorf("expected filepath 'V2_0__add_users.sql', got %v", migItem["filepath"])
	}
}

func TestFlywayJSON_InfoResult(t *testing.T) {
	now := time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)
	res := &InfoResult{
		SchemaVersion:   "1.0",
		SchemaName:      "test_schema",
		AllSchemasEmpty: false,
		FlywayVersion:   "1.0.0",
		Database:        "BigQuery",
		Warnings:        []string{},
		Operation:       "info",
		FlywayFormat: []FlywayInfoItem{
			{
				Category:       "Versioned",
				Version:        "1.0",
				RawVersion:     "1.0",
				Description:    "init",
				Type:           "SQL",
				InstalledOnUTC: now.Format("2006-01-02T15:04:05.000Z"),
				State:          "Success",
				Undoable:       "No",
				Filepath:       "V1_0__init.sql",
				UndoFilepath:   "",
				InstalledBy:    "test_user",
				ExecutionTime:  15,
			},
		},
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal InfoResult: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	expectedKeys := []string{
		"schemaVersion",
		"schemaName",
		"migrations",
		"allSchemasEmpty",
		"flywayVersion",
		"database",
		"warnings",
		"operation",
	}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("InfoResult JSON missing expected Flyway key: %s", key)
		}
	}

	disallowedKeys := []string{
		"schema",
		"table",
		"flywayFormat",
	}
	for _, key := range disallowedKeys {
		if _, ok := m[key]; ok {
			t.Errorf("InfoResult JSON contains disallowed internal key: %s", key)
		}
	}

	migs, ok := m["migrations"].([]interface{})
	if !ok || len(migs) != 1 {
		t.Fatalf("expected migrations array of len 1, got %v", m["migrations"])
	}
	migItem := migs[0].(map[string]interface{})
	migExpectedKeys := []string{
		"category", "version", "rawVersion", "description", "type",
		"installedOnUTC", "state", "undoable", "filepath", "undoFilepath",
		"installedBy", "executionTime",
	}
	for _, key := range migExpectedKeys {
		if _, ok := migItem[key]; !ok {
			t.Errorf("InfoOutput item missing expected Flyway key: %s", key)
		}
	}
	if migItem["installedOnUTC"] != "2026-10-13T14:00:00.000Z" {
		t.Errorf("expected installedOnUTC ISO 8601 UTC timestamp, got %v", migItem["installedOnUTC"])
	}
}

func TestFlywayJSON_ValidateResult(t *testing.T) {
	t.Run("Valid", func(t *testing.T) {
		res := &ValidateResult{
			Valid: true,
		}
		res.finalize("BigQuery", 3)

		data, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("failed to marshal ValidateResult: %v", err)
		}

		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}

		expectedKeys := []string{
			"validationSuccessful",
			"validateCount",
			"invalidMigrations",
			"flywayVersion",
			"database",
			"warnings",
			"operation",
		}
		for _, key := range expectedKeys {
			if _, ok := m[key]; !ok {
				t.Errorf("ValidateResult JSON missing key: %s", key)
			}
		}

		if m["validationSuccessful"] != true {
			t.Errorf("expected validationSuccessful true, got %v", m["validationSuccessful"])
		}
		if _, ok := m["valid"]; ok {
			t.Errorf("ValidateResult JSON should not have legacy 'valid' key")
		}
		if _, ok := m["errors"]; ok {
			t.Errorf("ValidateResult JSON should not have legacy 'errors' key")
		}
		if _, ok := m["errorDetails"]; ok {
			t.Errorf("ValidateResult JSON should omit errorDetails when successful")
		}
	})

	t.Run("Invalid", func(t *testing.T) {
		res := &ValidateResult{
			Valid: false,
			Errors: []ValidationError{
				{
					Description: "missing file",
					File:        "V1__missing.sql",
					Message:     "Migration V1__missing.sql not found",
				},
			},
		}
		res.finalize("BigQuery", 1)

		data, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("failed to marshal ValidateResult: %v", err)
		}

		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}

		if m["validationSuccessful"] != false {
			t.Errorf("expected validationSuccessful false, got %v", m["validationSuccessful"])
		}

		errDet, ok := m["errorDetails"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected errorDetails object, got %v", m["errorDetails"])
		}
		if errDet["errorCode"] != "FAULT" {
			t.Errorf("expected errorCode 'FAULT', got %v", errDet["errorCode"])
		}

		invalids, ok := m["invalidMigrations"].([]interface{})
		if !ok || len(invalids) != 1 {
			t.Fatalf("expected 1 invalid migration, got %v", m["invalidMigrations"])
		}
		inv := invalids[0].(map[string]interface{})
		if inv["filepath"] != "V1__missing.sql" {
			t.Errorf("expected filepath 'V1__missing.sql', got %v", inv["filepath"])
		}
	})
}

func TestFlywayJSON_BaselineResult(t *testing.T) {
	res := &BaselineResult{
		BaselineVersion:       "1.0",
		SuccessfullyBaselined: true,
		FlywayVersion:         "1.0.0",
		Database:              "BigQuery",
		Warnings:              []string{},
		Operation:             "baseline",

		Schema:              "analytics",
		Table:               "flyway_schema_history",
		BaselineDescription: "<< Flyway Baseline >>",
		Success:             true,
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal BaselineResult: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	expectedKeys := []string{
		"baselineVersion",
		"successfullyBaselined",
		"flywayVersion",
		"database",
		"warnings",
		"operation",
	}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("BaselineResult missing Flyway key: %s", key)
		}
	}

	disallowedKeys := []string{"schema", "table", "baselineDescription", "success"}
	for _, key := range disallowedKeys {
		if _, ok := m[key]; ok {
			t.Errorf("BaselineResult contains disallowed internal key: %s", key)
		}
	}
}

func TestFlywayJSON_RepairResult(t *testing.T) {
	res := &RepairResult{
		RepairActions: []string{"Align checksums", "Remove failed migrations"},
		MigrationsAligned: []RepairOutput{
			{
				Version:     "1.0",
				Description: "init",
				Filepath:    "V1_0__init.sql",
			},
		},
		MigrationsDeleted: []RepairOutput{},
		MigrationsRemoved: []RepairOutput{
			{
				Version:     "2.0",
				Description: "failed mig",
				Filepath:    "V2_0__failed.sql",
			},
		},
		FlywayVersion: "1.0.0",
		Database:      "BigQuery",
		Warnings:      []string{},
		Operation:     "repair",

		Schema:           "analytics",
		Table:            "flyway_schema_history",
		RemovedFailed:    []string{"V2_0__failed.sql"},
		AlignedChecksums: []string{"V1_0__init.sql"},
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal RepairResult: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	expectedKeys := []string{
		"repairActions",
		"migrationsAligned",
		"migrationsDeleted",
		"migrationsRemoved",
		"flywayVersion",
		"database",
		"warnings",
		"operation",
	}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("RepairResult missing Flyway key: %s", key)
		}
	}

	disallowedKeys := []string{"schema", "table", "removedFailed", "alignedChecksums", "deletedMigrations"}
	for _, key := range disallowedKeys {
		if _, ok := m[key]; ok {
			t.Errorf("RepairResult contains disallowed internal key: %s", key)
		}
	}

	aligned := m["migrationsAligned"].([]interface{})
	if len(aligned) != 1 {
		t.Fatalf("expected 1 aligned migration, got %d", len(aligned))
	}
	alItem := aligned[0].(map[string]interface{})
	if alItem["filepath"] != "V1_0__init.sql" || alItem["version"] != "1.0" {
		t.Errorf("unexpected aligned item: %v", alItem)
	}
}

func TestFlywayJSON_CleanResult(t *testing.T) {
	res := &CleanResult{
		SchemasCleaned: []string{"analytics"},
		SchemasDropped: []string{},
		FlywayVersion:  "1.0.0",
		Database:       "BigQuery",
		Warnings:       []string{},
		Operation:      "clean",
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal CleanResult: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	expectedKeys := []string{
		"schemasCleaned",
		"schemasDropped",
		"flywayVersion",
		"database",
		"warnings",
		"operation",
	}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("CleanResult missing Flyway key: %s", key)
		}
	}
}

func TestFlywayJSON_UndoResult(t *testing.T) {
	res := &UndoResult{
		InitialSchemaVersion: "2.0",
		TargetSchemaVersion:  "1.0",
		SchemaName:           "analytics",
		Migrations: []UndoOutput{
			{
				Category:      "Versioned",
				Version:       "2.0",
				Description:   "undo users",
				Filepath:      "U2_0__undo_users.sql",
				ExecutionTime: 20,
			},
		},
		MigrationsUndone: 1,
		Success:          true,
		FlywayVersion:    "1.0.0",
		Database:         "BigQuery",
		Warnings:         []string{},
		Operation:        "undo",

		UndoneVersion: "2.0",
		Script:        "U2_0__undo_users.sql",
		ExecutionTime: 20,
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal UndoResult: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	expectedKeys := []string{
		"initialSchemaVersion",
		"targetSchemaVersion",
		"schemaName",
		"migrations",
		"migrationsUndone",
		"success",
		"flywayVersion",
		"database",
		"warnings",
		"operation",
	}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("UndoResult missing Flyway key: %s", key)
		}
	}

	disallowedKeys := []string{"undoneVersion", "script", "executionTime"}
	for _, key := range disallowedKeys {
		if _, ok := m[key]; ok {
			t.Errorf("UndoResult root contains disallowed key: %s", key)
		}
	}
}

func TestFlywayJSON_EndToEndOperations(t *testing.T) {
	fs := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE t1 (id INT64);"),
		},
	}

	mockDB := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.Driver = "mock"
	cfg.DefaultSchema = "test_ds"
	cfg.Table = "flyway_schema_history"
	cfg.FS = fs
	cfg.Locations = []string{"migrations"}

	mig, err := New(cfg, mockDB)
	if err != nil {
		t.Fatalf("NewWithDatabase error: %v", err)
	}

	ctx := context.Background()

	// 1. Baseline operation
	bRes, err := mig.Baseline(ctx)
	if err != nil {
		t.Fatalf("Baseline error: %v", err)
	}
	bData, err := json.Marshal(bRes)
	if err != nil {
		t.Fatalf("Baseline Marshal error: %v", err)
	}
	var bMap map[string]interface{}
	if err := json.Unmarshal(bData, &bMap); err != nil {
		t.Fatalf("Baseline unmarshal error: %v", err)
	}
	if bMap["operation"] != "baseline" || bMap["successfullyBaselined"] != true {
		t.Errorf("unexpected baseline JSON output: %s", string(bData))
	}

	// 2. Info operation
	iRes, err := mig.Info(ctx)
	if err != nil {
		t.Fatalf("Info error: %v", err)
	}
	iData, err := json.Marshal(iRes)
	if err != nil {
		t.Fatalf("Info Marshal error: %v", err)
	}
	var iMap map[string]interface{}
	if err := json.Unmarshal(iData, &iMap); err != nil {
		t.Fatalf("Info unmarshal error: %v", err)
	}
	if iMap["operation"] != "info" || iMap["schemaName"] != "test_ds" {
		t.Errorf("unexpected info JSON output: %s", string(iData))
	}
	if !strings.HasPrefix(string(iData), "{") {
		t.Errorf("Info JSON should be an object, not array: %s", string(iData))
	}

	// 3. Validate operation
	vRes, err := mig.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	vData, err := json.Marshal(vRes)
	if err != nil {
		t.Fatalf("Validate Marshal error: %v", err)
	}
	var vMap map[string]interface{}
	if err := json.Unmarshal(vData, &vMap); err != nil {
		t.Fatalf("Validate unmarshal error: %v", err)
	}
	if vMap["operation"] != "validate" || vMap["validationSuccessful"] != true {
		t.Errorf("unexpected validate JSON output: %s", string(vData))
	}

	// 4. Repair operation
	rRes, err := mig.Repair(ctx)
	if err != nil {
		t.Fatalf("Repair error: %v", err)
	}
	rData, err := json.Marshal(rRes)
	if err != nil {
		t.Fatalf("Repair Marshal error: %v", err)
	}
	var rMap map[string]interface{}
	if err := json.Unmarshal(rData, &rMap); err != nil {
		t.Fatalf("Repair unmarshal error: %v", err)
	}
	if rMap["operation"] != "repair" {
		t.Errorf("unexpected repair JSON output: %s", string(rData))
	}

	// 5. Clean operation
	cfg.CleanDisabled = false
	migClean, err := New(cfg, mockDB)
	if err != nil {
		t.Fatalf("Failed to create migrator for clean: %v", err)
	}
	cRes, err := migClean.Clean(ctx)
	if err != nil {
		t.Fatalf("Clean error: %v", err)
	}
	cData, err := json.Marshal(cRes)
	if err != nil {
		t.Fatalf("Clean Marshal error: %v", err)
	}
	var cMap map[string]interface{}
	if err := json.Unmarshal(cData, &cMap); err != nil {
		t.Fatalf("Clean unmarshal error: %v", err)
	}
	if cMap["operation"] != "clean" {
		t.Errorf("unexpected clean JSON output: %s", string(cData))
	}
}
