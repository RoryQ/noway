package migrator

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/database/mock"
	"github.com/roryq/noway/pkg/version"
)

// =================================================================================================
// 1. CONCURRENCY STRESS TESTS
// =================================================================================================

// TestChallenger_M5_ConcurrencyStress verifies thread safety across multiple workers concurrently
// executing Migrate(), Validate(), Info(), and Repair() against a shared database.
func TestChallenger_M5_ConcurrencyStress(t *testing.T) {
	ctx := context.Background()

	t.Run("ConcurrentMultiWorker_MigrateValidateInfoRepair", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__init.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
			"migrations/V2__step.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE t2 (id INT64);")},
			"migrations/R__view1.sql":  &fstest.MapFile{Data: []byte("CREATE VIEW v1 AS SELECT 1;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_stress"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		// Seed initial state with V1
		cfgSeed := *cfg
		cfgSeed.Target = "1"
		mSeedTarget, err := New(&cfgSeed, db)
		if err != nil {
			t.Fatalf("failed to init seed migrator: %v", err)
		}
		_, err = mSeedTarget.Migrate(ctx)
		if err != nil {
			t.Fatalf("seed migration failed: %v", err)
		}

		const numGoroutines = 24
		var wg sync.WaitGroup
		var migrateSuccessCount int64
		var migrateLockCount int64
		var repairSuccessCount int64
		var repairLockCount int64
		var validateCount int64
		var infoCount int64

		startSignal := make(chan struct{})

		for i := 0; i < numGoroutines; i++ {
			workerID := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startSignal

				workerCfg := config.NewDefaultConfiguration()
				workerCfg.DefaultSchema = "test_stress"
				workerCfg.FS = fs
				workerCfg.Locations = []string{"migrations"}
				wMigrator, wErr := New(workerCfg, db)
				if wErr != nil {
					t.Errorf("worker %d failed to create migrator: %v", workerID, wErr)
					return
				}

				switch workerID % 4 {
				case 0:
					// Worker calls Migrate
					res, mErr := wMigrator.Migrate(ctx)
					if mErr != nil {
						if strings.Contains(mErr.Error(), "already locked") {
							atomic.AddInt64(&migrateLockCount, 1)
						} else {
							t.Errorf("worker %d migrate unexpected error: %v", workerID, mErr)
						}
					} else if res != nil {
						atomic.AddInt64(&migrateSuccessCount, 1)
					}
				case 1:
					// Worker calls Repair
					rRes, rErr := wMigrator.Repair(ctx)
					if rErr != nil {
						if strings.Contains(rErr.Error(), "already locked") {
							atomic.AddInt64(&repairLockCount, 1)
						} else {
							t.Errorf("worker %d repair unexpected error: %v", workerID, rErr)
						}
					} else if rRes != nil {
						atomic.AddInt64(&repairSuccessCount, 1)
					}
				case 2:
					// Worker calls Validate
					vRes, vErr := wMigrator.Validate(ctx)
					if vErr != nil {
						t.Errorf("worker %d validate returned error: %v", workerID, vErr)
					} else if vRes != nil {
						atomic.AddInt64(&validateCount, 1)
					}
				case 3:
					// Worker calls Info
					iRes, iErr := wMigrator.Info(ctx)
					if iErr != nil {
						t.Errorf("worker %d info returned error: %v", workerID, iErr)
					} else if iRes != nil {
						atomic.AddInt64(&infoCount, 1)
					}
				}
			}()
		}

		close(startSignal)
		wg.Wait()

		// Verify database lock is not leaked
		unlock, lockErr := db.Lock(ctx, "test_stress", "flyway_schema_history")
		if lockErr != nil {
			t.Fatalf("database lock leaked after concurrent workers finished: %v", lockErr)
		}
		_ = unlock(ctx)

		if atomic.LoadInt64(&validateCount) == 0 {
			t.Errorf("expected at least one successful Validate invocation")
		}
		if atomic.LoadInt64(&infoCount) == 0 {
			t.Errorf("expected at least one successful Info invocation")
		}

		// Subsequent single-threaded migration should succeed cleanly
		mFinal, err := New(cfg, db)
		if err != nil {
			t.Fatalf("final migrator init failed: %v", err)
		}
		finalRes, err := mFinal.Migrate(ctx)
		if err != nil {
			t.Fatalf("final Migrate failed: %v", err)
		}
		if !finalRes.Success {
			t.Errorf("final Migrate expected success, got false")
		}

		// Validation should pass
		valFinal, err := mFinal.Validate(ctx)
		if err != nil || !valFinal.Valid {
			t.Errorf("final Validate expected valid=true, got valid=%v, err=%v", valFinal.Valid, err)
		}
	})

	t.Run("ConcurrentMigrateLockContention", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_lock"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		const workers = 16
		var wg sync.WaitGroup
		startSignal := make(chan struct{})
		results := make(chan error, workers)

		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startSignal
				workerCfg := config.NewDefaultConfiguration()
				workerCfg.DefaultSchema = "test_lock"
				workerCfg.FS = fs
				workerCfg.Locations = []string{"migrations"}
				wMigrator, _ := New(workerCfg, db)
				_, err := wMigrator.Migrate(ctx)
				results <- err
			}()
		}

		close(startSignal)
		wg.Wait()
		close(results)

		var successCount int
		var lockContentionCount int
		for err := range results {
			if err == nil {
				successCount++
			} else if strings.Contains(err.Error(), "already locked") {
				lockContentionCount++
			} else {
				t.Errorf("unexpected error under lock contention: %v", err)
			}
		}

		if successCount < 1 {
			t.Errorf("expected at least 1 successful Migrate(), got %d", successCount)
		}
		if lockContentionCount+successCount != workers {
			t.Errorf("all workers must either succeed or encounter lock contention (sum=%d, want %d)",
				lockContentionCount+successCount, workers)
		}
	})

	t.Run("ConcurrentValidateAndInfoNoLockContention", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
			"migrations/V2__step.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t2 (id INT64);")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_read_concurrency"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		mInit, _ := New(cfg, db)
		_, err := mInit.Migrate(ctx)
		if err != nil {
			t.Fatalf("initial migrate failed: %v", err)
		}

		const readWorkers = 20
		var wg sync.WaitGroup
		errs := make(chan error, readWorkers*2)

		for i := 0; i < readWorkers; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				rCfg := config.NewDefaultConfiguration()
				rCfg.DefaultSchema = "test_read_concurrency"
				rCfg.FS = fs
				rCfg.Locations = []string{"migrations"}
				m, _ := New(rCfg, db)
				valRes, err := m.Validate(ctx)
				if err != nil {
					errs <- fmt.Errorf("Validate failed: %w", err)
				} else if !valRes.Valid {
					errs <- fmt.Errorf("Validate invalid: %s", valRes.Error())
				}
			}()
			go func() {
				defer wg.Done()
				rCfg := config.NewDefaultConfiguration()
				rCfg.DefaultSchema = "test_read_concurrency"
				rCfg.FS = fs
				rCfg.Locations = []string{"migrations"}
				m, _ := New(rCfg, db)
				infoRes, err := m.Info(ctx)
				if err != nil {
					errs <- fmt.Errorf("Info failed: %w", err)
				} else if len(infoRes.Migrations) != 2 {
					errs <- fmt.Errorf("expected 2 migrations in Info, got %d", len(infoRes.Migrations))
				}
			}()
		}

		wg.Wait()
		close(errs)

		for err := range errs {
			t.Errorf("concurrent reader error: %v", err)
		}
	})

	t.Run("LockReleasedOnStatementFailure", func(t *testing.T) {
		db := mock.NewMockDatabase()
		db.SetFailOnExecute("FAIL_LOCK_RELEASE")

		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("SELECT FAIL_LOCK_RELEASE;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_cancel"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)

		_, err := m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error on failed statement, got nil")
		}

		// Database lock MUST be unlocked
		unlock, lockErr := db.Lock(ctx, "test_cancel", "flyway_schema_history")
		if lockErr != nil {
			t.Fatalf("lock was not released following statement failure: %v", lockErr)
		}
		_ = unlock(ctx)
	})
}

// =================================================================================================
// 2. EDGE CASE TARGET VERSIONS
// =================================================================================================

// TestChallenger_M5_EdgeCaseTargetVersions stress tests boundary targets: invalid, empty,
// equal to baseline, lower than baseline, next, current, and latest.
func TestChallenger_M5_EdgeCaseTargetVersions(t *testing.T) {
	ctx := context.Background()

	t.Run("TargetCurrent_CleanVsPopulatedDB", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__one.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V2__two.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_target_curr"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.Target = "current"

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to init migrator: %v", err)
		}

		// 1. Clean DB: target=current should execute 0 migrations and report << Empty >>
		resClean, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if resClean.MigrationsExecuted != 0 {
			t.Errorf("expected 0 migrations on clean DB with target=current, got %d", resClean.MigrationsExecuted)
		}
		if resClean.TargetVersion != "<< Empty >>" {
			t.Errorf("expected TargetVersion '<< Empty >>', got %q", resClean.TargetVersion)
		}

		// 2. Apply V1 with target=1
		cfg.Target = "1"
		m1, _ := New(cfg, db)
		res1, err := m1.Migrate(ctx)
		if err != nil || res1.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed, got %d, err=%v", res1.MigrationsExecuted, err)
		}

		// 3. Now with V1 applied, target=current should stop at 1 and execute 0
		cfg.Target = "current"
		mCurr, _ := New(cfg, db)
		resPop, err := mCurr.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate target=current on populated DB failed: %v", err)
		}
		if resPop.MigrationsExecuted != 0 {
			t.Errorf("expected 0 migrations executed, got %d", resPop.MigrationsExecuted)
		}
		if resPop.TargetVersion != "1" {
			t.Errorf("expected TargetVersion '1', got %q", resPop.TargetVersion)
		}
	})

	t.Run("TargetNext_CleanDB_PopulatedDB_OutOfOrderDisabled", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__one.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V2__two.sql":   &fstest.MapFile{Data: []byte("SELECT 2;")},
			"migrations/V3__three.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_target_next"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.Target = "next"

		// Step 1: Clean DB + target=next -> applies V1 only
		m1, _ := New(cfg, db)
		res1, err := m1.Migrate(ctx)
		if err != nil {
			t.Fatalf("first next failed: %v", err)
		}
		if res1.MigrationsExecuted != 1 || res1.TargetVersion != "1" {
			t.Errorf("expected V1 applied with TargetVersion 1, got count=%d, ver=%s", res1.MigrationsExecuted, res1.TargetVersion)
		}

		// Step 2: Next again -> applies V2 only
		m2, _ := New(cfg, db)
		res2, err := m2.Migrate(ctx)
		if err != nil {
			t.Fatalf("second next failed: %v", err)
		}
		if res2.MigrationsExecuted != 1 || res2.TargetVersion != "2" {
			t.Errorf("expected V2 applied with TargetVersion 2, got count=%d, ver=%s", res2.MigrationsExecuted, res2.TargetVersion)
		}

		// Step 3: Next again -> applies V3 only
		m3, _ := New(cfg, db)
		res3, err := m3.Migrate(ctx)
		if err != nil {
			t.Fatalf("third next failed: %v", err)
		}
		if res3.MigrationsExecuted != 1 || res3.TargetVersion != "3" {
			t.Errorf("expected V3 applied with TargetVersion 3, got count=%d, ver=%s", res3.MigrationsExecuted, res3.TargetVersion)
		}

		// Step 4: Next again when all applied -> 0 executed
		m4, _ := New(cfg, db)
		res4, err := m4.Migrate(ctx)
		if err != nil {
			t.Fatalf("exhausted next failed: %v", err)
		}
		if res4.MigrationsExecuted != 0 || res4.TargetVersion != "3" {
			t.Errorf("expected 0 executed when exhausted, got count=%d, ver=%s", res4.MigrationsExecuted, res4.TargetVersion)
		}
	})

	t.Run("TargetLatest_And_EmptyOrWhitespace", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__one.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V2__two.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_target_latest"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.Target = "" // empty string acts as latest

		m, _ := New(cfg, db)
		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed: %v", err)
		}
		if res.MigrationsExecuted != 2 {
			t.Errorf("expected 2 migrations executed with empty target (acting as latest), got %d", res.MigrationsExecuted)
		}
		if res.TargetVersion != "2" {
			t.Errorf("expected TargetVersion '2', got %q", res.TargetVersion)
		}
	})

	t.Run("TargetEqualToBaseline_And_LowerThanBaseline", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__one.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V2__two.sql":   &fstest.MapFile{Data: []byte("SELECT 2;")},
			"migrations/V3__three.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_baseline_target"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.BaselineVersion = "2"
		cfg.BaselineDescription = "Base2"

		// Baseline the DB at version 2
		mBase, _ := New(cfg, db)
		_, err := mBase.Baseline(ctx)
		if err != nil {
			t.Fatalf("Baseline failed: %v", err)
		}

		// Case 1: Target equal to baseline (target = "2")
		cfgTarget2 := *cfg
		cfgTarget2.Target = "2"
		mTarget2, _ := New(&cfgTarget2, db)
		resTarget2, err := mTarget2.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate target=2 failed: %v", err)
		}
		if resTarget2.MigrationsExecuted != 0 {
			t.Errorf("expected 0 executed when target == baseline (2), got %d", resTarget2.MigrationsExecuted)
		}

		// Case 2: Target lower than baseline (target = "1")
		cfgTarget1 := *cfg
		cfgTarget1.Target = "1"
		mTarget1, _ := New(&cfgTarget1, db)
		resTarget1, err := mTarget1.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate target=1 failed: %v", err)
		}
		if resTarget1.MigrationsExecuted != 0 {
			t.Errorf("expected 0 executed when target < baseline (1 < 2), got %d", resTarget1.MigrationsExecuted)
		}

		// Case 3: Target higher than baseline (target = "3") -> executes V3
		cfgTarget3 := *cfg
		cfgTarget3.Target = "3"
		mTarget3, _ := New(&cfgTarget3, db)
		resTarget3, err := mTarget3.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate target=3 failed: %v", err)
		}
		if resTarget3.MigrationsExecuted != 1 {
			t.Errorf("expected 1 executed (V3) when target > baseline (3 > 2), got %d", resTarget3.MigrationsExecuted)
		}
		if resTarget3.TargetVersion != "3" {
			t.Errorf("expected TargetVersion '3', got %q", resTarget3.TargetVersion)
		}
	})

	t.Run("TargetWithQuestionMarkSuffix", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__one.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V2__two.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_target_qmark"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		// Initialize migrator first
		m, _ := New(cfg, db)
		// Set target with '?' directly on config to simulate dynamic resolution
		m.config.Target = "1?"

		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate with '1?' failed: %v", err)
		}
		if res.MigrationsExecuted != 1 || res.TargetVersion != "1" {
			t.Errorf("expected V1 applied with target 1?, got count=%d, ver=%s", res.MigrationsExecuted, res.TargetVersion)
		}
	})

	t.Run("TargetZero_And_ExtremeBigInt", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V0__pre.sql":  &fstest.MapFile{Data: []byte("SELECT 0;")},
			"migrations/V1__one.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V99999999999999999999999999999999999999__future.sql": &fstest.MapFile{Data: []byte("SELECT 999;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_target_bounds"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		// Target = "0": applies V0 only
		cfg.Target = "0"
		m0, _ := New(cfg, db)
		res0, err := m0.Migrate(ctx)
		if err != nil {
			t.Fatalf("target=0 migrate failed: %v", err)
		}
		if res0.MigrationsExecuted != 1 || res0.TargetVersion != "0" {
			t.Errorf("expected V0 applied for target 0, got count=%d, ver=%s", res0.MigrationsExecuted, res0.TargetVersion)
		}

		// Target = extreme big int: applies V1 and huge version
		cfg.Target = "99999999999999999999999999999999999999"
		mHuge, _ := New(cfg, db)
		resHuge, err := mHuge.Migrate(ctx)
		if err != nil {
			t.Fatalf("huge target migrate failed: %v", err)
		}
		if resHuge.MigrationsExecuted != 2 {
			t.Errorf("expected 2 migrations executed for huge target, got %d", resHuge.MigrationsExecuted)
		}
	})
}

// =================================================================================================
// 3. CORRUPTED HISTORY RECORDS
// =================================================================================================

// TestChallenger_M5_CorruptedHistoryRecords stress tests behavior when history table rows are corrupted
// (invalid timestamps, missing descriptions, extreme installed ranks, corrupted checksums).
func TestChallenger_M5_CorruptedHistoryRecords(t *testing.T) {
	ctx := context.Background()

	t.Run("ExtremeInstalledRanks_NegativeAndLarge", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_ranks")
		_ = db.EnsureHistoryTable(ctx, "test_ranks", "flyway_schema_history")

		vNegStr := "1"
		vLargeStr := "2"
		cs := int64(12345)

		// Record 1: Negative installed rank (-100)
		_ = db.InsertHistory(ctx, "test_ranks", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: -100,
			Version:       &vNegStr,
			Description:   "neg rank",
			Type:          "SQL",
			Script:        "V1__one.sql",
			Checksum:      &cs,
			Success:       true,
		})

		// Record 2: Large positive installed rank (1000000)
		_ = db.InsertHistory(ctx, "test_ranks", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1000000,
			Version:       &vLargeStr,
			Description:   "large rank",
			Type:          "SQL",
			Script:        "V2__two.sql",
			Checksum:      &cs,
			Success:       true,
		})

		fs := fstest.MapFS{
			"migrations/V1__one.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V2__two.sql":   &fstest.MapFile{Data: []byte("SELECT 2;")},
			"migrations/V3__three.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_ranks"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}
		cfg.ValidateOnMigrate = false

		m, err := New(cfg, db)
		if err != nil {
			t.Fatalf("failed to init migrator: %v", err)
		}

		// Migrate V3: nextInstalledRank should be 1000001
		res, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate failed with extreme ranks: %v", err)
		}
		if res.MigrationsExecuted != 1 {
			t.Fatalf("expected 1 migration executed, got %d", res.MigrationsExecuted)
		}

		history, err := db.FetchHistory(ctx, "test_ranks", "flyway_schema_history")
		if err != nil {
			t.Fatalf("failed to fetch history: %v", err)
		}

		var v3Rank int
		for _, h := range history {
			if h.Script == "V3__three.sql" {
				v3Rank = h.InstalledRank
			}
		}

		if v3Rank != 1000001 {
			t.Errorf("expected V3 installed rank 1000001, got %d", v3Rank)
		}
	})

	t.Run("MissingDescription_DetectedByValidate_RepairedByRepair", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_desc")
		_ = db.EnsureHistoryTable(ctx, "test_desc", "flyway_schema_history")

		v1Str := "1"
		fs := fstest.MapFS{
			"migrations/V1__initial_schema.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t (id INT64);")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_desc"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		mTemp, _ := New(cfg, db)
		resResolved, _ := mTemp.resolver.Resolve()
		cs := resResolved.VersionedMigrations[0].Checksum

		// Insert history record with empty description
		_ = db.InsertHistory(ctx, "test_desc", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1Str,
			Description:   "", // missing description!
			Type:          "SQL",
			Script:        "V1__initial_schema.sql",
			Checksum:      &cs,
			Success:       true,
		})

		m, _ := New(cfg, db)

		// 1. Validate should report description mismatch
		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if valRes.Valid {
			t.Errorf("expected Validate to fail on missing description, got valid=true")
		}

		// 2. Repair should align description to match resolved migration ("initial schema")
		repRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repRes.AlignedChecksums) != 1 {
			t.Errorf("expected 1 aligned record in repair, got %d", len(repRes.AlignedChecksums))
		}

		history, _ := db.FetchHistory(ctx, "test_desc", "flyway_schema_history")
		if len(history) != 1 || history[0].Description != "initial schema" {
			t.Errorf("expected description aligned to 'initial schema', got %q", history[0].Description)
		}

		// 3. Subsequent Validate should pass
		valAfter, err := m.Validate(ctx)
		if err != nil || !valAfter.Valid {
			t.Errorf("expected Validate to pass after Repair, valid=%v, err=%v", valAfter.Valid, err)
		}
	})

	t.Run("NilChecksum_IgnoredByValidate_AlignedByRepair", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_nil_cs")
		_ = db.EnsureHistoryTable(ctx, "test_nil_cs", "flyway_schema_history")

		v1Str := "1"
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		}

		// Insert history record with nil checksum
		_ = db.InsertHistory(ctx, "test_nil_cs", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1Str,
			Description:   "init",
			Type:          "SQL",
			Script:        "V1__init.sql",
			Checksum:      nil, // nil checksum
			Success:       true,
		})

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_nil_cs"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)

		// 1. Validate passes when checksum is nil (Feature 14 parity)
		valRes, err := m.Validate(ctx)
		if err != nil || !valRes.Valid {
			t.Errorf("expected Validate to ignore nil checksum, valid=%v, err=%v", valRes.Valid, err)
		}

		// 2. Repair should align nil checksum to resolved file checksum
		repRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repRes.AlignedChecksums) != 1 {
			t.Errorf("expected Repair to align nil checksum, got aligned: %+v", repRes.AlignedChecksums)
		}

		history, _ := db.FetchHistory(ctx, "test_nil_cs", "flyway_schema_history")
		if len(history) != 1 || history[0].Checksum == nil {
			t.Fatalf("expected non-nil checksum after repair")
		}
	})

	t.Run("NilVersionOnSQLRecord_TreatedAsRepeatable_RepairedToDelete", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_nil_ver")
		_ = db.EnsureHistoryTable(ctx, "test_nil_ver", "flyway_schema_history")

		cs := int64(9999)
		// Corrupted history: Type is "SQL" but Version is nil
		_ = db.InsertHistory(ctx, "test_nil_ver", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       nil,
			Description:   "orphaned",
			Type:          "SQL",
			Script:        "orphaned.sql",
			Checksum:      &cs,
			Success:       true,
		})

		fs := fstest.MapFS{
			"migrations/V1__valid.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_nil_ver"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)

		// 1. In Flyway, Validate does not fail on orphaned/deleted repeatable migration
		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if !valRes.Valid {
			t.Errorf("expected Validate to pass on orphaned repeatable record (Flyway parity), got: %s", valRes.Error())
		}

		// 2. Repair should mark missing repeatable migration as DELETE
		repRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repRes.DeletedMigrations) != 1 || repRes.DeletedMigrations[0] != "orphaned.sql" {
			t.Errorf("expected orphaned.sql marked as DELETE, got %+v", repRes.DeletedMigrations)
		}

		// 3. Subsequent Validate should pass because DELETE records are ignored
		valAfter, err := m.Validate(ctx)
		if err != nil || !valAfter.Valid {
			t.Errorf("expected Validate to pass after Repair marked orphaned as DELETE, got: %s", valAfter.Error())
		}
	})

	t.Run("CorruptedTimestampsAndExecutionTimes_RenderCleanly", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_corrupt_times")
		_ = db.EnsureHistoryTable(ctx, "test_corrupt_times", "flyway_schema_history")

		v1Str := "1"
		cs := int64(111)

		// Zero timestamp and negative execution time
		_ = db.InsertHistory(ctx, "test_corrupt_times", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1Str,
			Description:   "init",
			Type:          "SQL",
			Script:        "V1__init.sql",
			Checksum:      &cs,
			InstalledOn:   time.Time{}, // zero timestamp
			ExecutionTime: -9999,       // negative execution time
			Success:       true,
		})

		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_corrupt_times"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)

		infoRes, err := m.Info(ctx)
		if err != nil {
			t.Fatalf("Info failed on corrupted times: %v", err)
		}

		var tableBuf bytes.Buffer
		infoRes.RenderTable(&tableBuf)
		tableStr := tableBuf.String()
		if len(tableStr) == 0 {
			t.Errorf("expected rendered table string, got empty")
		}

		jsonBytes, err := infoRes.RenderJSON()
		if err != nil || len(jsonBytes) == 0 {
			t.Errorf("expected valid JSON bytes from RenderJSON, err=%v", err)
		}
	})

	t.Run("UnknownRecordTypes_FlaggedByValidate_AlignedByRepair", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_unknown_type")
		_ = db.EnsureHistoryTable(ctx, "test_unknown_type", "flyway_schema_history")

		v1Str := "1"
		cs := int64(123)

		_ = db.InsertHistory(ctx, "test_unknown_type", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1Str,
			Description:   "init",
			Type:          "UNKNOWN_TYPE", // unknown type!
			Script:        "V1__init.sql",
			Checksum:      &cs,
			Success:       true,
		})

		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_unknown_type"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)

		// 1. Validate should report type mismatch
		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if valRes.Valid {
			t.Errorf("expected Validate to flag type mismatch, got valid=true")
		}

		// 2. Repair should align type to "SQL"
		repRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repRes.AlignedChecksums) != 1 {
			t.Errorf("expected Repair to align unknown type to SQL, got %d aligned", len(repRes.AlignedChecksums))
		}

		history, _ := db.FetchHistory(ctx, "test_unknown_type", "flyway_schema_history")
		if len(history) != 1 || history[0].Type != "SQL" {
			t.Errorf("expected record type aligned to 'SQL', got %q", history[0].Type)
		}

		// 3. Subsequent Validate passes
		valAfter, err := m.Validate(ctx)
		if err != nil || !valAfter.Valid {
			t.Errorf("expected Validate to pass after Repair aligned type, got: %s", valAfter.Error())
		}
	})

	t.Run("MultipleFailedRecords_CleanedByRepair_RetrySucceeds", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_multi_fail")
		_ = db.EnsureHistoryTable(ctx, "test_multi_fail", "flyway_schema_history")

		v1Str := "1"
		v2Str := "2"
		cs := int64(10)

		// Insert multiple failed records at ranks 1 and 2
		_ = db.InsertHistory(ctx, "test_multi_fail", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1Str,
			Description:   "one",
			Type:          "SQL",
			Script:        "V1__one.sql",
			Checksum:      &cs,
			Success:       false,
		})
		_ = db.InsertHistory(ctx, "test_multi_fail", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 2,
			Version:       &v2Str,
			Description:   "two",
			Type:          "SQL",
			Script:        "V2__two.sql",
			Checksum:      &cs,
			Success:       false,
		})

		fs := fstest.MapFS{
			"migrations/V1__one.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V2__two.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_multi_fail"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)

		// Validate fails with 2 errors
		valRes, _ := m.Validate(ctx)
		if valRes.Valid || len(valRes.Errors) != 2 {
			t.Errorf("expected 2 validation errors for failed migrations, got %d", len(valRes.Errors))
		}

		// Repair removes both failed records
		repRes, err := m.Repair(ctx)
		if err != nil {
			t.Fatalf("Repair failed: %v", err)
		}
		if len(repRes.RemovedFailed) != 2 {
			t.Errorf("expected 2 failed migrations removed by Repair, got %d", len(repRes.RemovedFailed))
		}

		// History is now clean
		history, _ := db.FetchHistory(ctx, "test_multi_fail", "flyway_schema_history")
		if len(history) != 0 {
			t.Errorf("expected history table to be empty after removing all failed records, got %d", len(history))
		}

		// Retry Migrate cleanly succeeds
		migRes, err := m.Migrate(ctx)
		if err != nil || migRes.MigrationsExecuted != 2 {
			t.Fatalf("expected successful retry of 2 migrations after Repair, got count=%d, err=%v", migRes.MigrationsExecuted, err)
		}
	})

	t.Run("RestoredDeletedVersionedMigration_RealignedAndValidated", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__one.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/V2__two.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_restore_v"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, err := m.Migrate(ctx)
		if err != nil {
			t.Fatalf("initial migrate failed: %v", err)
		}

		// 1. Delete V1 from disk and run Repair -> V1 marked as DELETE
		delete(fs, "migrations/V1__one.sql")
		mRep1, _ := New(cfg, db)
		repRes1, err := mRep1.Repair(ctx)
		if err != nil || len(repRes1.DeletedMigrations) != 1 {
			t.Fatalf("expected V1 marked as DELETE, got %+v, err=%v", repRes1.DeletedMigrations, err)
		}

		valDeleted, err := mRep1.Validate(ctx)
		if err != nil || !valDeleted.Valid {
			t.Fatalf("expected Validate to pass after V1 marked DELETE, got: %s", valDeleted.Error())
		}

		// 2. Restore V1 to disk with new content
		fs["migrations/V1__one.sql"] = &fstest.MapFile{Data: []byte("SELECT 100;") }

		// 3. Repair again -> aligns V1 and restores Type to SQL
		mRep2, _ := New(cfg, db)
		repRes2, err := mRep2.Repair(ctx)
		if err != nil {
			t.Fatalf("second Repair failed: %v", err)
		}
		if len(repRes2.AlignedChecksums) != 1 {
			t.Errorf("expected 1 aligned checksum for restored V1, got %+v", repRes2.AlignedChecksums)
		}

		history, _ := db.FetchHistory(ctx, "test_restore_v", "flyway_schema_history")
		var v1Type string
		for _, h := range history {
			if h.Script == "V1__one.sql" {
				v1Type = h.Type
			}
		}
		if v1Type != "SQL" {
			t.Errorf("expected restored V1 Type to be 'SQL', got %q", v1Type)
		}

		// 4. Validate passes
		valRestored, err := mRep2.Validate(ctx)
		if err != nil || !valRestored.Valid {
			t.Errorf("expected Validate to pass after restoring V1, got errors: %s", valRestored.Error())
		}
	})
}

// =================================================================================================
// 4. CLEAN ON VALIDATION ERROR
// =================================================================================================

// TestChallenger_M5_CleanOnValidationError tests CleanOnValidationError behavior.
func TestChallenger_M5_CleanOnValidationError(t *testing.T) {
	ctx := context.Background()

	t.Run("CleanOnValidationError_Enabled_WipesAndMigrates", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_clean_on_val"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m1, _ := New(cfg, db)
		_, err := m1.Migrate(ctx)
		if err != nil {
			t.Fatalf("initial migrate failed: %v", err)
		}

		// Tamper with local file content to trigger checksum mismatch
		fs["migrations/V1__init.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64, val STRING);")}

		// Set CleanOnValidationError = true, CleanDisabled = false
		cfgClean := *cfg
		cfgClean.CleanOnValidationError = true
		cfgClean.CleanDisabled = false

		m2, err := New(&cfgClean, db)
		if err != nil {
			t.Fatalf("migrator init failed: %v", err)
		}

		// Migrate should trigger Clean() on validation failure, wipe the database, and re-run V1 successfully
		migRes, err := m2.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate with CleanOnValidationError failed: %v", err)
		}
		if !migRes.Success || migRes.MigrationsExecuted != 1 {
			t.Errorf("expected 1 migration re-executed after cleanOnValidationError, got count=%d, success=%v",
				migRes.MigrationsExecuted, migRes.Success)
		}

		// Validate now passes
		valRes, err := m2.Validate(ctx)
		if err != nil || !valRes.Valid {
			t.Errorf("expected Validate to pass after CleanOnValidationError re-migration, got: %s", valRes.Error())
		}
	})

	t.Run("CleanOnValidationError_BlockedWhenCleanDisabled", func(t *testing.T) {
		db := mock.NewMockDatabase()
		fs := fstest.MapFS{
			"migrations/V1__init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64);")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_clean_disabled"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m1, _ := New(cfg, db)
		_, err := m1.Migrate(ctx)
		if err != nil {
			t.Fatalf("initial migrate failed: %v", err)
		}

		// Tamper with local file content to trigger checksum mismatch
		fs["migrations/V1__init.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE t1 (id INT64, modified STRING);")}

		// Both CleanOnValidationError = true and CleanDisabled = true
		cfgBlocked := *cfg
		cfgBlocked.CleanOnValidationError = true
		cfgBlocked.CleanDisabled = true

		mBlocked, _ := New(&cfgBlocked, db)

		// Migrate MUST fail with validation error because clean is disabled
		_, err = mBlocked.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected Migrate to fail when CleanDisabled=true even if CleanOnValidationError=true")
		}
		if !strings.Contains(err.Error(), "checksum mismatch") {
			t.Errorf("expected checksum mismatch validation error, got: %v", err)
		}
	})
}

// =================================================================================================
// 5. LIFECYCLE ERROR CALLBACKS
// =================================================================================================

// TestChallenger_M5_LifecycleErrorCallbacks tests firing of error lifecycle events
// (afterMigrateError, afterValidateError, afterInfoError, afterUndoError).
func TestChallenger_M5_LifecycleErrorCallbacks(t *testing.T) {
	ctx := context.Background()

	t.Run("MigrateStatementError_FiresLifecycleErrorCallbacks", func(t *testing.T) {
		db := mock.NewMockDatabase()
		db.SetFailOnExecute("FAIL_STMT")

		fs := fstest.MapFS{
			"migrations/V1__bad.sql":             &fstest.MapFile{Data: []byte("SELECT FAIL_STMT;")},
			"migrations/afterMigrateError.sql":   &fstest.MapFile{Data: []byte("INSERT INTO callback_audit VALUES ('afterMigrateError');")},
			"migrations/afterEachMigrateError.sql": &fstest.MapFile{Data: []byte("INSERT INTO callback_audit VALUES ('afterEachMigrateError');")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_lifecycle_err"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)

		res, err := m.Migrate(ctx)
		if err == nil {
			t.Fatalf("expected error from failed statement, got nil")
		}
		if res != nil && res.Success {
			t.Errorf("expected MigrateResult.Success to be false")
		}

		// Verify executed statements in MockDatabase include the error callbacks
		stmts := db.ExecutedStatements()
		foundAfterEach := false
		foundAfterMigrate := false
		for _, s := range stmts {
			if strings.Contains(s, "afterEachMigrateError") {
				foundAfterEach = true
			}
			if strings.Contains(s, "afterMigrateError") {
				foundAfterMigrate = true
			}
		}

		if !foundAfterEach {
			t.Errorf("expected afterEachMigrateError callback to be executed")
		}
		if !foundAfterMigrate {
			t.Errorf("expected afterMigrateError callback to be executed")
		}
	})

	t.Run("ValidateDiscrepancy_FiresAfterValidateError", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_val_err")
		_ = db.EnsureHistoryTable(ctx, "test_val_err", "flyway_schema_history")

		v1Str := "1"
		badChecksum := int64(999999)
		_ = db.InsertHistory(ctx, "test_val_err", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1Str,
			Description:   "init",
			Type:          "SQL",
			Script:        "V1__init.sql",
			Checksum:      &badChecksum,
			Success:       true,
		})

		fs := fstest.MapFS{
			"migrations/V1__init.sql":            &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/afterValidateError.sql":  &fstest.MapFile{Data: []byte("INSERT INTO callback_audit VALUES ('afterValidateError');")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_val_err"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		valRes, err := m.Validate(ctx)
		if err != nil {
			t.Fatalf("Validate returned unexpected fatal error: %v", err)
		}
		if valRes.Valid {
			t.Fatalf("expected validation discrepancy")
		}

		stmts := db.ExecutedStatements()
		found := false
		for _, s := range stmts {
			if strings.Contains(s, "afterValidateError") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected afterValidateError callback to be fired on validation failure")
		}
	})

	t.Run("InfoHistoryFailure_FiresAfterInfoError", func(t *testing.T) {
		db := mock.NewMockDatabase()
		// Drop schema to simulate inaccessible table
		fs := fstest.MapFS{
			"migrations/V1__init.sql":        &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/beforeInfo.sql":      &fstest.MapFile{Data: []byte("FAIL_INFO_BEFORE;")},
			"migrations/afterInfoError.sql":  &fstest.MapFile{Data: []byte("INSERT INTO audit VALUES ('afterInfoError');")},
		}
		db.SetFailOnExecute("FAIL_INFO_BEFORE")

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_info_err"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, err := m.Info(ctx)
		if err == nil {
			t.Fatalf("expected Info to return error when beforeInfo fails")
		}

		stmts := db.ExecutedStatements()
		found := false
		for _, s := range stmts {
			if strings.Contains(s, "afterInfoError") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected afterInfoError to be fired on Info failure")
		}
	})

	t.Run("UndoStatementError_FiresAfterUndoError", func(t *testing.T) {
		db := mock.NewMockDatabase()
		_ = db.EnsureSchema(ctx, "test_undo_err")
		_ = db.EnsureHistoryTable(ctx, "test_undo_err", "flyway_schema_history")

		v1Str := "1"
		cs := int64(10)
		_ = db.InsertHistory(ctx, "test_undo_err", "flyway_schema_history", database.HistoryRecord{
			InstalledRank: 1,
			Version:       &v1Str,
			Description:   "init",
			Type:          "SQL",
			Script:        "V1__init.sql",
			Checksum:      &cs,
			Success:       true,
		})

		db.SetFailOnExecute("FAIL_UNDO")

		fs := fstest.MapFS{
			"migrations/V1__init.sql":       &fstest.MapFile{Data: []byte("SELECT 1;")},
			"migrations/U1__undo.sql":       &fstest.MapFile{Data: []byte("DROP TABLE FAIL_UNDO;")},
			"migrations/afterUndoError.sql": &fstest.MapFile{Data: []byte("INSERT INTO audit VALUES ('afterUndoError');")},
		}

		cfg := config.NewDefaultConfiguration()
		cfg.DefaultSchema = "test_undo_err"
		cfg.FS = fs
		cfg.Locations = []string{"migrations"}

		m, _ := New(cfg, db)
		_, err := m.Undo(ctx)
		if err == nil {
			t.Fatalf("expected error from failed undo statement, got nil")
		}

		stmts := db.ExecutedStatements()
		found := false
		for _, s := range stmts {
			if strings.Contains(s, "afterUndoError") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected afterUndoError callback to be fired on undo statement failure")
		}
	})
}

// Helper to construct a Version pointer in tests
func versionPtr(v string) *version.Version {
	parsed, err := version.Parse(v)
	if err != nil {
		return nil
	}
	return &parsed
}
