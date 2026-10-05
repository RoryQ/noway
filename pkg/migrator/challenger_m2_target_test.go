package migrator

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/database/mock"
	"github.com/roryq/noway/pkg/resolver"
)

func TestChallenger_TargetCurrent(t *testing.T) {
	ctx := context.Background()

	t.Run("CleanDB_TargetCurrent", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"sql/V1__init.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__step.sql":  &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__final.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "current"

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		// Check Info() before migrate
		infoRes, err := m.Info(ctx)
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		for _, item := range infoRes.Migrations {
			if item.State != resolver.StateAboveTarget {
				t.Errorf("expected script %s to be StateAboveTarget on clean DB with target=current, got %s", item.Script, item.State)
			}
		}

		// Migrate() should execute 0 migrations
		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 0 {
			t.Errorf("expected 0 migrations executed, got %d", res.MigrationsExecuted)
		}
		if res.InitialVersion != "<< Empty >>" {
			t.Errorf("expected initial version '<< Empty >>', got %s", res.InitialVersion)
		}
		if res.TargetVersion != "<< Empty >>" {
			t.Errorf("expected target version '<< Empty >>', got %s", res.TargetVersion)
		}
	})

	t.Run("PopulatedDB_TargetCurrent", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
		v1Str := "1"
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1Str,
			Description:   "init",
			Type:          "SQL",
			Script:        "V1__init.sql",
			Success:       true,
		})

		fs := fstest.MapFS{
			"sql/V1__init.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__step.sql":  &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__final.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "current"

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		// Info() should show V1=Success, V2=AboveTarget, V3=AboveTarget
		infoRes, err := m.Info(ctx)
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		stateByScript := make(map[string]resolver.MigrationState)
		for _, item := range infoRes.Migrations {
			stateByScript[item.Script] = item.State
		}
		if stateByScript["V1__init.sql"] != resolver.StateSuccess {
			t.Errorf("expected V1 StateSuccess, got %s", stateByScript["V1__init.sql"])
		}
		if stateByScript["V2__step.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V2 StateAboveTarget, got %s", stateByScript["V2__step.sql"])
		}
		if stateByScript["V3__final.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V3 StateAboveTarget, got %s", stateByScript["V3__final.sql"])
		}

		// Migrate() should execute 0 migrations
		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 0 {
			t.Errorf("expected 0 migrations executed, got %d", res.MigrationsExecuted)
		}
		if res.TargetVersion != "1" {
			t.Errorf("expected target version '1', got %s", res.TargetVersion)
		}
	})

	t.Run("GapInHistory_TargetCurrent_OutOfOrderFalse", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
		v1Str := "1"
		v3Str := "3"
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1Str, Description: "first", Type: "SQL", Script: "V1__first.sql", Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v3Str, Description: "third", Type: "SQL", Script: "V3__third.sql", Success: true,
		})

		fs := fstest.MapFS{
			"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
			"sql/V4__fourth.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "current"
		cfg.OutOfOrder = false
		cfg.ValidateOnMigrate = false

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		infoRes, err := m.Info(ctx)
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		states := make(map[string]resolver.MigrationState)
		for _, item := range infoRes.Migrations {
			states[item.Script] = item.State
		}
		if states["V1__first.sql"] != resolver.StateSuccess {
			t.Errorf("expected V1 Success, got %s", states["V1__first.sql"])
		}
		if states["V2__second.sql"] != resolver.StateIgnored {
			t.Errorf("expected V2 StateIgnored, got %s", states["V2__second.sql"])
		}
		if states["V3__third.sql"] != resolver.StateSuccess {
			t.Errorf("expected V3 Success, got %s", states["V3__third.sql"])
		}
		if states["V4__fourth.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V4 StateAboveTarget, got %s", states["V4__fourth.sql"])
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 0 {
			t.Errorf("expected 0 executed, got %d", res.MigrationsExecuted)
		}
		if res.TargetVersion != "3" {
			t.Errorf("expected target version '3', got %s", res.TargetVersion)
		}
	})

	t.Run("GapInHistory_TargetCurrent_OutOfOrderTrue", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
		v1Str := "1"
		v3Str := "3"
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1Str, Description: "first", Type: "SQL", Script: "V1__first.sql", Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v3Str, Description: "third", Type: "SQL", Script: "V3__third.sql", Success: true,
		})

		fs := fstest.MapFS{
			"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
			"sql/V4__fourth.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "current"
		cfg.OutOfOrder = true

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		infoRes, err := m.Info(ctx)
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		states := make(map[string]resolver.MigrationState)
		for _, item := range infoRes.Migrations {
			states[item.Script] = item.State
		}
		if states["V2__second.sql"] != resolver.StatePending {
			t.Errorf("expected V2 StatePending under OutOfOrder=true, got %s", states["V2__second.sql"])
		}
		if states["V4__fourth.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V4 StateAboveTarget, got %s", states["V4__fourth.sql"])
		}

		// When OutOfOrder=true and target=current, V2 (<= 3) is executed!
		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}
		if res.ExecutedMigrations[0].Version != "2" {
			t.Errorf("expected V2 executed, got %s", res.ExecutedMigrations[0].Version)
		}
		// TargetVersion must NOT downgrade from 3 to 2
		if res.TargetVersion != "3" {
			t.Errorf("expected TargetVersion '3', got %s", res.TargetVersion)
		}
	})
}

func TestChallenger_TargetNext(t *testing.T) {
	ctx := context.Background()

	t.Run("CleanDB_TargetNext", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"sql/V1__init.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__step.sql":  &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__final.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "next"

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		infoRes, err := m.Info(ctx)
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		states := make(map[string]resolver.MigrationState)
		for _, item := range infoRes.Migrations {
			states[item.Script] = item.State
		}
		if states["V1__init.sql"] != resolver.StatePending {
			t.Errorf("expected V1 Pending, got %s", states["V1__init.sql"])
		}
		if states["V2__step.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V2 AboveTarget, got %s", states["V2__step.sql"])
		}
		if states["V3__final.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V3 AboveTarget, got %s", states["V3__final.sql"])
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}
		if res.ExecutedMigrations[0].Version != "1" {
			t.Errorf("expected V1 executed, got %s", res.ExecutedMigrations[0].Version)
		}
		if res.TargetVersion != "1" {
			t.Errorf("expected TargetVersion '1', got %s", res.TargetVersion)
		}
	})

	t.Run("GapInHistory_TargetNext_OutOfOrderFalse", func(t *testing.T) {
		// Pre-populate V1 and V3. Local has V1, V2, V3, V4, V5.
		// Under OutOfOrder=false, V2 is skipped. The next valid migration is V4!
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
		v1Str := "1"
		v3Str := "3"
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1Str, Description: "first", Type: "SQL", Script: "V1__first.sql", Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v3Str, Description: "third", Type: "SQL", Script: "V3__third.sql", Success: true,
		})

		fs := fstest.MapFS{
			"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
			"sql/V4__fourth.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
			"sql/V5__fifth.sql":  &fstest.MapFile{Data: []byte("SELECT 5;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "next"
		cfg.OutOfOrder = false
		cfg.ValidateOnMigrate = false

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		infoRes, err := m.Info(ctx)
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		states := make(map[string]resolver.MigrationState)
		for _, item := range infoRes.Migrations {
			states[item.Script] = item.State
		}
		if states["V2__second.sql"] != resolver.StateIgnored {
			t.Errorf("expected V2 StateIgnored, got %s", states["V2__second.sql"])
		}
		if states["V4__fourth.sql"] != resolver.StatePending {
			t.Errorf("expected V4 StatePending, got %s", states["V4__fourth.sql"])
		}
		if states["V5__fifth.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V5 StateAboveTarget, got %s", states["V5__fifth.sql"])
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}
		if res.ExecutedMigrations[0].Version != "4" {
			t.Errorf("expected V4 executed, got %s", res.ExecutedMigrations[0].Version)
		}
		if res.TargetVersion != "4" {
			t.Errorf("expected TargetVersion '4', got %s", res.TargetVersion)
		}
	})

	t.Run("GapInHistory_TargetNext_OutOfOrderTrue", func(t *testing.T) {
		// Pre-populate V1 and V3. Local has V1, V2, V3, V4, V5.
		// Under OutOfOrder=true, V2 IS eligible, so the next migration to run is V2!
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
		v1Str := "1"
		v3Str := "3"
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1Str, Description: "first", Type: "SQL", Script: "V1__first.sql", Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v3Str, Description: "third", Type: "SQL", Script: "V3__third.sql", Success: true,
		})

		fs := fstest.MapFS{
			"sql/V1__first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__third.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
			"sql/V4__fourth.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
			"sql/V5__fifth.sql":  &fstest.MapFile{Data: []byte("SELECT 5;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "next"
		cfg.OutOfOrder = true

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		infoRes, err := m.Info(ctx)
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		states := make(map[string]resolver.MigrationState)
		for _, item := range infoRes.Migrations {
			states[item.Script] = item.State
		}
		if states["V2__second.sql"] != resolver.StatePending {
			t.Errorf("expected V2 StatePending, got %s", states["V2__second.sql"])
		}
		if states["V4__fourth.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V4 StateAboveTarget, got %s", states["V4__fourth.sql"])
		}
		if states["V5__fifth.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V5 StateAboveTarget, got %s", states["V5__fifth.sql"])
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}
		if res.ExecutedMigrations[0].Version != "2" {
			t.Errorf("expected V2 executed, got %s", res.ExecutedMigrations[0].Version)
		}
		// TargetVersion must NOT downgrade from 3 to 2
		if res.TargetVersion != "3" {
			t.Errorf("expected TargetVersion '3', got %s", res.TargetVersion)
		}

		// Second run of target=next should now apply V4!
		res2, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate 2 failed: %v", err)
		}
		if res2.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed on run 2, got %d", res2.MigrationsExecuted)
		}
		if res2.ExecutedMigrations[0].Version != "4" {
			t.Errorf("expected V4 executed on run 2, got %s", res2.ExecutedMigrations[0].Version)
		}
		if res2.TargetVersion != "4" {
			t.Errorf("expected TargetVersion '4' on run 2, got %s", res2.TargetVersion)
		}
	})

	t.Run("MultipleGapsProgression_OutOfOrderTrue", func(t *testing.T) {
		// Pre-populate V1 and V5. Local has V1, V2, V3, V4, V5, V6.
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
		v1Str := "1"
		v5Str := "5"
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1Str, Description: "v1", Type: "SQL", Script: "V1__v1.sql", Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v5Str, Description: "v5", Type: "SQL", Script: "V5__v5.sql", Success: true,
		})

		fs := fstest.MapFS{
			"sql/V1__v1.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__v2.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__v3.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
			"sql/V4__v4.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
			"sql/V5__v5.sql": &fstest.MapFile{Data: []byte("SELECT 5;")},
			"sql/V6__v6.sql": &fstest.MapFile{Data: []byte("SELECT 6;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "next"
		cfg.OutOfOrder = true

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		// Step 1: V2 executes
		res1, err := m.Migrate(ctx)
		if err != nil || res1.MigrationsExecuted != 1 || res1.ExecutedMigrations[0].Version != "2" || res1.TargetVersion != "5" {
			t.Fatalf("Step 1 failed: exec=%d, ver=%s, target=%s", res1.MigrationsExecuted, res1.ExecutedMigrations[0].Version, res1.TargetVersion)
		}

		// Step 2: V3 executes
		res2, err := m.Migrate(ctx)
		if err != nil || res2.MigrationsExecuted != 1 || res2.ExecutedMigrations[0].Version != "3" || res2.TargetVersion != "5" {
			t.Fatalf("Step 2 failed: exec=%d, ver=%s, target=%s", res2.MigrationsExecuted, res2.ExecutedMigrations[0].Version, res2.TargetVersion)
		}

		// Step 3: V4 executes
		res3, err := m.Migrate(ctx)
		if err != nil || res3.MigrationsExecuted != 1 || res3.ExecutedMigrations[0].Version != "4" || res3.TargetVersion != "5" {
			t.Fatalf("Step 3 failed: exec=%d, ver=%s, target=%s", res3.MigrationsExecuted, res3.ExecutedMigrations[0].Version, res3.TargetVersion)
		}

		// Step 4: V6 executes (all gaps filled, now advance to 6)
		res4, err := m.Migrate(ctx)
		if err != nil || res4.MigrationsExecuted != 1 || res4.ExecutedMigrations[0].Version != "6" || res4.TargetVersion != "6" {
			t.Fatalf("Step 4 failed: exec=%d, ver=%s, target=%s", res4.MigrationsExecuted, res4.ExecutedMigrations[0].Version, res4.TargetVersion)
		}

		// Step 5: all applied, 0 executions
		res5, err := m.Migrate(ctx)
		if err != nil || res5.MigrationsExecuted != 0 || res5.TargetVersion != "6" {
			t.Fatalf("Step 5 failed: exec=%d, target=%s", res5.MigrationsExecuted, res5.TargetVersion)
		}
	})

	t.Run("MultipleGapsProgression_OutOfOrderFalse", func(t *testing.T) {
		// Pre-populate V1 and V5. Local has V1, V2, V3, V4, V5, V6.
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
		v1Str := "1"
		v5Str := "5"
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v1Str, Description: "v1", Type: "SQL", Script: "V1__v1.sql", Success: true,
		})
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2, Version: &v5Str, Description: "v5", Type: "SQL", Script: "V5__v5.sql", Success: true,
		})

		fs := fstest.MapFS{
			"sql/V1__v1.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__v2.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__v3.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
			"sql/V4__v4.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
			"sql/V5__v5.sql": &fstest.MapFile{Data: []byte("SELECT 5;")},
			"sql/V6__v6.sql": &fstest.MapFile{Data: []byte("SELECT 6;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "next"
		cfg.OutOfOrder = false
		cfg.ValidateOnMigrate = false

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		// Step 1: V2, V3, V4 skipped, so V6 executes!
		res1, err := m.Migrate(ctx)
		if err != nil || res1.MigrationsExecuted != 1 || res1.ExecutedMigrations[0].Version != "6" || res1.TargetVersion != "6" {
			t.Fatalf("Step 1 failed: exec=%d, ver=%s, target=%s", res1.MigrationsExecuted, res1.ExecutedMigrations[0].Version, res1.TargetVersion)
		}

		// Step 2: 0 executed, V2..V4 remain skipped
		res2, err := m.Migrate(ctx)
		if err != nil || res2.MigrationsExecuted != 0 || res2.TargetVersion != "6" {
			t.Fatalf("Step 2 failed: exec=%d, target=%s", res2.MigrationsExecuted, res2.TargetVersion)
		}
	})
}

func TestChallenger_TargetLatest(t *testing.T) {
	ctx := context.Background()

	db := mock.NewMockDatabase()
	_ = db.EnsureSchema(ctx, "test_ds")
	_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
	v1Str := "1"
	_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
		InstalledRank: 1, Version: &v1Str, Description: "init", Type: "SQL", Script: "V1__init.sql", Success: true,
	})

	fs := fstest.MapFS{
		"sql/V1__init.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
		"sql/V2__step.sql":  &fstest.MapFile{Data: []byte("SELECT 2;")},
		"sql/V3__final.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
	}

	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "test_ds"
	cfg.FS = fs
	cfg.Target = "latest"

	m, err := New(cfg, db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}

	infoRes, err := m.Info(ctx)
	if err != nil {
		t.Fatalf("Info failed: %v", err)
	}
	states := make(map[string]resolver.MigrationState)
	for _, item := range infoRes.Migrations {
		states[item.Script] = item.State
	}
	if states["V1__init.sql"] != resolver.StateSuccess {
		t.Errorf("expected V1 Success, got %s", states["V1__init.sql"])
	}
	if states["V2__step.sql"] != resolver.StatePending {
		t.Errorf("expected V2 Pending under target=latest, got %s", states["V2__step.sql"])
	}
	if states["V3__final.sql"] != resolver.StatePending {
		t.Errorf("expected V3 Pending under target=latest, got %s", states["V3__final.sql"])
	}

	res, err := m.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if res.MigrationsExecuted != 2 {
		t.Errorf("expected 2 migrations executed, got %d", res.MigrationsExecuted)
	}
	if res.TargetVersion != "3" {
		t.Errorf("expected TargetVersion '3', got %s", res.TargetVersion)
	}
}

func TestChallenger_TargetSpecificVersion(t *testing.T) {
	ctx := context.Background()

	t.Run("Target2_0_DottedVersions", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"sql/V1.0__base.sql":     &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V1.5__half.sql":     &fstest.MapFile{Data: []byte("SELECT 1.5;")},
			"sql/V2.0__target.sql":   &fstest.MapFile{Data: []byte("SELECT 2.0;")},
			"sql/V2.1__exceeded.sql": &fstest.MapFile{Data: []byte("SELECT 2.1;")},
			"sql/V3.0__beyond.sql":   &fstest.MapFile{Data: []byte("SELECT 3.0;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "2.0"

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		infoRes, err := m.Info(ctx)
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		states := make(map[string]resolver.MigrationState)
		for _, item := range infoRes.Migrations {
			states[item.Script] = item.State
		}
		if states["V1.0__base.sql"] != resolver.StatePending {
			t.Errorf("expected V1.0 Pending, got %s", states["V1.0__base.sql"])
		}
		if states["V1.5__half.sql"] != resolver.StatePending {
			t.Errorf("expected V1.5 Pending, got %s", states["V1.5__half.sql"])
		}
		if states["V2.0__target.sql"] != resolver.StatePending {
			t.Errorf("expected V2.0 Pending, got %s", states["V2.0__target.sql"])
		}
		if states["V2.1__exceeded.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V2.1 AboveTarget, got %s", states["V2.1__exceeded.sql"])
		}
		if states["V3.0__beyond.sql"] != resolver.StateAboveTarget {
			t.Errorf("expected V3.0 AboveTarget, got %s", states["V3.0__beyond.sql"])
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 3 {
			t.Fatalf("expected 3 migrations executed, got %d", res.MigrationsExecuted)
		}
		if res.TargetVersion != "2.0" {
			t.Errorf("expected TargetVersion '2.0', got %s", res.TargetVersion)
		}
	})

	t.Run("DBPastTarget_NoRegression", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ds")
		_ = db.EnsureHistoryTable(ctx, "test_ds", "flyway_schema_history")
		v3Str := "3"
		_ = db.InsertHistory(ctx, "test_ds", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1, Version: &v3Str, Description: "v3", Type: "SQL", Script: "V3__v3.sql", Success: true,
		})

		fs := fstest.MapFS{
			"sql/V1__v1.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__v2.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/V3__v3.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "2.0"
		cfg.OutOfOrder = false
		cfg.ValidateOnMigrate = false

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 0 {
			t.Errorf("expected 0 executed, got %d", res.MigrationsExecuted)
		}
		// DB was at 3, target was 2.0: schema version must not regress to 2.0
		if res.TargetVersion != "3" {
			t.Errorf("expected TargetVersion '3', got %s", res.TargetVersion)
		}
	})

	t.Run("RepeatablesNotCutOffByTarget", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"sql/V1__init.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"sql/V2__step.sql":  &fstest.MapFile{Data: []byte("SELECT 2;")},
			"sql/R__view.sql":   &fstest.MapFile{Data: []byte("SELECT 'repeatable';")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ds"
		cfg.FS = fs
		cfg.Target = "1"

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to create migrator: %v", err)
		}

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		// V1 and R__view should execute (total 2). V2 is above target.
		if res.MigrationsExecuted != 2 {
			t.Fatalf("expected 2 migrations executed, got %d", res.MigrationsExecuted)
		}
		if res.ExecutedMigrations[0].Version != "1" {
			t.Errorf("expected V1 executed first, got %s", res.ExecutedMigrations[0].Version)
		}
		if res.ExecutedMigrations[1].Category != "Repeatable" {
			t.Errorf("expected Repeatable executed second, got %s", res.ExecutedMigrations[1].Category)
		}
		if res.TargetVersion != "1" {
			t.Errorf("expected TargetVersion '1', got %s", res.TargetVersion)
		}
	})
}
