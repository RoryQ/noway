package migrator

import (
	"context"
	"fmt"

	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/resolver"
)

// RepairResult contains the repaired migrations outcome.
type RepairResult struct {
	Schema            string   `json:"schema"`
	Table             string   `json:"table"`
	RemovedFailed     []string `json:"removedFailed"`
	AlignedChecksums  []string `json:"alignedChecksums"`
	DeletedMigrations []string `json:"deletedMigrations,omitempty"`
}

// Repair repairs the schema history table by removing failed migrations and aligning checksums/descriptions.
func (m *Migrator) Repair(ctx context.Context) (*RepairResult, error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table

	// 1. Ensure history table exists
	exists, err := m.db.HistoryTableExists(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to check history table existence: %w", err)
	}
	if !exists {
		return &RepairResult{Schema: defaultSchema, Table: table}, nil
	}

	// 2. Lock table
	unlock, err := m.db.Lock(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire lock for repair: %w", err)
	}
	defer func() {
		_ = unlock(context.Background())
	}()

	// 3. Resolve local migrations
	resolved, err := m.resolver.Resolve()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve migrations: %w", err)
	}

	// 4. Fetch applied migrations
	applied, err := m.db.FetchHistory(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch schema history: %w", err)
	}

	// 5. Fire beforeRepair callbacks
	if err := m.callbackRunner.Fire(ctx, "beforeRepair"); err != nil {
		return nil, err
	}

	result := &RepairResult{
		Schema: defaultSchema,
		Table:  table,
	}

	// Map resolved by version
	resolvedByVersion := make(map[string]resolver.ResolvedMigration)
	for _, res := range resolved.VersionedMigrations {
		if res.Version != nil {
			resolvedByVersion[res.Version.Normalized()] = res
		}
	}
	resolvedRepeatableByDesc := make(map[string]resolver.ResolvedMigration)
	resolvedRepeatableByScript := make(map[string]resolver.ResolvedMigration)
	for _, res := range resolved.RepeatableMigrations {
		resolvedRepeatableByDesc[res.Description] = res
		resolvedRepeatableByScript[res.Script] = res
	}

	for _, app := range applied {
		// 1. Remove failed migration
		if !app.Success {
			if err := m.db.DeleteHistory(ctx, defaultSchema, table, app.InstalledRank); err != nil {
				_ = m.callbackRunner.Fire(ctx, "afterRepairError")
				return nil, fmt.Errorf("failed to remove failed migration %s: %w", app.Script, err)
			}
			result.RemovedFailed = append(result.RemovedFailed, app.Script)
			continue
		}

		if app.Type == "BASELINE" || app.Type == "SCHEMA" {
			continue
		}

		// 2. Align versioned migration checksum & metadata
		if app.Version != nil && !app.Version.IsEmpty() {
			verKey := app.Version.Normalized()
			if res, found := resolvedByVersion[verKey]; found {
				needsUpdate := false
				if app.Checksum == nil || *app.Checksum != res.Checksum {
					needsUpdate = true
				}
				if app.Description != res.Description || app.Type != string(res.Type) {
					needsUpdate = true
				}

				if needsUpdate {
					cs := res.Checksum
					var verStr *string
					if app.Version != nil {
						s := app.Version.String()
						verStr = &s
					}
					rec := database.HistoryRecord{
						InstalledRank: app.InstalledRank,
						Version:       verStr,
						Description:   res.Description,
						Type:          string(res.Type),
						Script:        res.Script,
						Checksum:      &cs,
					}
					if err := m.db.UpdateHistory(ctx, defaultSchema, table, rec); err != nil {
						_ = m.callbackRunner.Fire(ctx, "afterRepairError")
						return nil, fmt.Errorf("failed to align checksum for %s: %w", app.Script, err)
					}
					result.AlignedChecksums = append(result.AlignedChecksums, fmt.Sprintf("%s (checksum -> %d)", res.Script, cs))
				}
			} else {
				// Feature 18: Missing versioned migration on disk -> Mark as DELETE
				if app.Type != "DELETE" {
					var verStr *string
					if app.Version != nil {
						s := app.Version.String()
						verStr = &s
					}
					rec := database.HistoryRecord{
						InstalledRank: app.InstalledRank,
						Version:       verStr,
						Description:   app.Description,
						Type:          "DELETE",
						Script:        app.Script,
						Checksum:      app.Checksum,
					}
					if err := m.db.UpdateHistory(ctx, defaultSchema, table, rec); err != nil {
						_ = m.callbackRunner.Fire(ctx, "afterRepairError")
						return nil, fmt.Errorf("failed to mark deleted migration %s: %w", app.Script, err)
					}
					result.DeletedMigrations = append(result.DeletedMigrations, app.Script)
				}
			}
		} else {
			// Repeatable migration alignment
			res, found := resolvedRepeatableByDesc[app.Description]
			if !found {
				res, found = resolvedRepeatableByScript[app.Script]
			}
			if found {
				if app.Checksum == nil || *app.Checksum != res.Checksum || app.Description != res.Description || app.Type != string(res.Type) {
					rec := database.HistoryRecord{
						InstalledRank: app.InstalledRank,
						Version:       nil,
						Description:   res.Description,
						Type:          string(res.Type),
						Script:        res.Script,
						Checksum:      &res.Checksum,
					}
					if err := m.db.UpdateHistory(ctx, defaultSchema, table, rec); err != nil {
						_ = m.callbackRunner.Fire(ctx, "afterRepairError")
						return nil, fmt.Errorf("failed to align metadata for %s: %w", app.Script, err)
					}
					result.AlignedChecksums = append(result.AlignedChecksums, res.Script)
				}
			} else {
				// Feature 18: Missing repeatable migration on disk -> Mark as DELETE
				if app.Type != "DELETE" {
					rec := database.HistoryRecord{
						InstalledRank: app.InstalledRank,
						Version:       nil,
						Description:   app.Description,
						Type:          "DELETE",
						Script:        app.Script,
						Checksum:      app.Checksum,
					}
					if err := m.db.UpdateHistory(ctx, defaultSchema, table, rec); err != nil {
						_ = m.callbackRunner.Fire(ctx, "afterRepairError")
						return nil, fmt.Errorf("failed to mark deleted migration %s: %w", app.Script, err)
					}
					result.DeletedMigrations = append(result.DeletedMigrations, app.Script)
				}
			}
		}
	}

	// 6. Fire afterRepair callbacks
	if err := m.callbackRunner.Fire(ctx, "afterRepair"); err != nil {
		return nil, err
	}

	return result, nil
}
