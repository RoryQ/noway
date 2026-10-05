package mock

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/roryq/noway/pkg/database"
)

func TestChallengerM5_MockDatabaseHistoryCRUDAndNilFields(t *testing.T) {
	ctx := context.Background()
	db := NewMockDatabase()
	schema := "test_ds"
	table := "schema_history"

	// 1. Initial table setup
	if err := db.EnsureHistoryTable(ctx, schema, table); err != nil {
		t.Fatalf("EnsureHistoryTable failed: %v", err)
	}

	exists, err := db.HistoryTableExists(ctx, schema, table)
	if err != nil || !exists {
		t.Fatalf("expected HistoryTableExists true, got %v (err: %v)", exists, err)
	}

	// 2. Insert records with nil version (repeatable) and nil checksum
	ver1 := "1.0"
	cs1 := int64(123456)
	rec1 := database.HistoryRecord{
		InstalledRank: 1,
		Version:       &ver1,
		Description:   "initial",
		Type:          "SQL",
		Script:        "V1.0__initial.sql",
		Checksum:      &cs1,
		InstalledBy:   "tester",
		InstalledOn:   time.Now(),
		ExecutionTime: 10,
		Success:       true,
	}

	// Repeatable: Version is nil
	csRep := int64(789012)
	recRep := database.HistoryRecord{
		InstalledRank: 2,
		Version:       nil,
		Description:   "views",
		Type:          "SQL",
		Script:        "R__views.sql",
		Checksum:      &csRep,
		InstalledBy:   "tester",
		InstalledOn:   time.Now(),
		ExecutionTime: 15,
		Success:       true,
	}

	// Failed record: Checksum is nil
	ver3 := "2.0"
	recFailed := database.HistoryRecord{
		InstalledRank: 3,
		Version:       &ver3,
		Description:   "bad migration",
		Type:          "SQL",
		Script:        "V2.0__bad.sql",
		Checksum:      nil,
		InstalledBy:   "tester",
		InstalledOn:   time.Now(),
		ExecutionTime: 5,
		Success:       false,
	}

	// Lock record with negative installed rank
	recLock := database.HistoryRecord{
		InstalledRank: -100,
		Version:       nil,
		Description:   "flyway-lock",
		Type:          "LOCK",
		Script:        "",
		Checksum:      nil,
		InstalledBy:   "",
		InstalledOn:   time.Now(),
		ExecutionTime: 0,
		Success:       true,
	}

	for _, r := range []database.HistoryRecord{rec1, recRep, recFailed, recLock} {
		if err := db.InsertHistory(ctx, schema, table, r); err != nil {
			t.Fatalf("InsertHistory failed for rank %d: %v", r.InstalledRank, err)
		}
	}

	// 3. Fetch history
	history, err := db.FetchHistory(ctx, schema, table)
	if err != nil {
		t.Fatalf("FetchHistory failed: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("expected 4 history records, got %d", len(history))
	}

	// Check rank 2 (repeatable) has nil version
	if history[1].Version != nil {
		t.Errorf("expected rank 2 version to be nil, got %v", history[1].Version)
	}

	// Check rank 3 (failed) has nil checksum
	if history[2].Checksum != nil {
		t.Errorf("expected rank 3 checksum to be nil, got %v", history[2].Checksum)
	}

	// 4. Update history (e.g. during repair)
	newCs := int64(999999)
	updateRec := database.HistoryRecord{
		InstalledRank: 2,
		Description:   "views updated",
		Type:          "SQL",
		Checksum:      &newCs,
	}
	if err := db.UpdateHistory(ctx, schema, table, updateRec); err != nil {
		t.Fatalf("UpdateHistory failed: %v", err)
	}

	historyAfterUpdate, _ := db.FetchHistory(ctx, schema, table)
	if historyAfterUpdate[1].Checksum == nil || *historyAfterUpdate[1].Checksum != newCs {
		t.Errorf("expected updated checksum %d, got %v", newCs, historyAfterUpdate[1].Checksum)
	}
	if historyAfterUpdate[1].Description != "views updated" {
		t.Errorf("expected updated description 'views updated', got %q", historyAfterUpdate[1].Description)
	}

	// 5. Delete history (e.g. during undo or repair)
	if err := db.DeleteHistory(ctx, schema, table, 3); err != nil {
		t.Fatalf("DeleteHistory failed: %v", err)
	}

	historyAfterDelete, _ := db.FetchHistory(ctx, schema, table)
	if len(historyAfterDelete) != 3 {
		t.Errorf("expected 3 history records after deletion, got %d", len(historyAfterDelete))
	}
	for _, h := range historyAfterDelete {
		if h.InstalledRank == 3 {
			t.Errorf("record with rank 3 should have been deleted")
		}
	}
}

func TestChallengerM5_MockDatabaseLockingAndConcurrency(t *testing.T) {
	ctx := context.Background()
	db := NewMockDatabase()
	schema := "test_ds"
	table := "schema_history"

	// 1. Basic lock and unlock
	unlock, err := db.Lock(ctx, schema, table)
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}

	// Second lock attempt must fail while held
	_, err2 := db.Lock(ctx, schema, table)
	if err2 == nil {
		t.Fatalf("expected second lock attempt to fail, got nil")
	}

	// Release lock
	if err := unlock(ctx); err != nil {
		t.Fatalf("unlock failed: %v", err)
	}

	// Now lock can be acquired again
	unlock2, err3 := db.Lock(ctx, schema, table)
	if err3 != nil {
		t.Fatalf("re-acquiring lock failed: %v", err3)
	}
	_ = unlock2(ctx)

	// 2. High-concurrency race test
	const goroutines = 25
	const iterations = 50
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for it := 0; it < iterations; it++ {
				rank := gid*1000 + it
				ver := fmt.Sprintf("%d.%d", gid, it)
				cs := int64(rank * 42)

				// Concurrent insert
				_ = db.InsertHistory(ctx, schema, table, database.HistoryRecord{
					InstalledRank: rank,
					Version:       &ver,
					Description:   "concurrent test",
					Type:          "SQL",
					Script:        fmt.Sprintf("V%s__concurrent.sql", ver),
					Checksum:      &cs,
					Success:       true,
				})

				// Concurrent fetch
				_, _ = db.FetchHistory(ctx, schema, table)

				// Concurrent statement execute
				_ = db.ExecuteStatement(ctx, fmt.Sprintf("SELECT %d", rank))
			}
		}(g)
	}

	wg.Wait()

	records, err := db.FetchHistory(ctx, schema, table)
	if err != nil {
		t.Fatalf("FetchHistory after concurrent runs failed: %v", err)
	}
	if len(records) != goroutines*iterations {
		t.Errorf("expected %d records, got %d", goroutines*iterations, len(records))
	}
}

func TestChallengerM5_MockDatabaseSchemaLifecycle(t *testing.T) {
	ctx := context.Background()
	db := NewMockDatabase()
	schema := "analytics"

	// Ensure schema
	if err := db.EnsureSchema(ctx, schema); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	exists, err := db.SchemaExists(ctx, schema)
	if err != nil || !exists {
		t.Fatalf("expected schema to exist")
	}

	empty, err := db.SchemaEmpty(ctx, schema)
	if err != nil || !empty {
		t.Fatalf("expected initial schema to be empty")
	}

	// Add table to schema
	_ = db.EnsureHistoryTable(ctx, schema, "history")
	emptyAfterTable, err := db.SchemaEmpty(ctx, schema)
	if err != nil || emptyAfterTable {
		t.Errorf("expected schema with table to not be empty")
	}

	// Clean schema
	if err := db.CleanSchema(ctx, schema); err != nil {
		t.Fatalf("CleanSchema failed: %v", err)
	}

	existsAfterClean, _ := db.SchemaExists(ctx, schema)
	if !existsAfterClean {
		t.Errorf("schema itself should still exist after CleanSchema")
	}

	emptyAfterClean, _ := db.SchemaEmpty(ctx, schema)
	if !emptyAfterClean {
		t.Errorf("schema should be empty after CleanSchema")
	}

	// Drop schema
	if err := db.DropSchema(ctx, schema); err != nil {
		t.Fatalf("DropSchema failed: %v", err)
	}

	existsAfterDrop, _ := db.SchemaExists(ctx, schema)
	if existsAfterDrop {
		t.Errorf("schema should not exist after DropSchema")
	}
}

func TestChallengerM5_MockDatabaseDialectQuotingAndFailOnExecute(t *testing.T) {
	db := NewMockDatabase()

	// Dialect quoting with backticks
	quoted := db.Quote("my`dataset", "table`name")
	expectedQuoted := "`my\\`dataset`.`table\\`name`"
	if quoted != expectedQuoted {
		t.Errorf("Quote got %q, want %q", quoted, expectedQuoted)
	}

	// FailOnExecute trigger
	db.SetFailOnExecute("FAIL_TRIGGER")
	ctx := context.Background()

	if err := db.ExecuteStatement(ctx, "SELECT 1"); err != nil {
		t.Errorf("unexpected error on benign statement: %v", err)
	}

	err := db.ExecuteStatement(ctx, "SELECT FAIL_TRIGGER FROM t")
	if err == nil {
		t.Errorf("expected error when executing SQL matching failOnExecute, got nil")
	}
}
