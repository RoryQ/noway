package migrator

import (
	"context"
	"fmt"

	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/resolver"
)

// RepairOutput describes a repaired migration matching Flyway RepairOutput.
type RepairOutput struct {
	Version     string `json:"version"`
	Description string `json:"description"`
	Filepath    string `json:"filepath"`
}

// RepairResult contains the repaired migrations outcome matching Flyway's output schema.
type RepairResult struct {
	RepairActions     []string       `json:"repairActions"`
	MigrationsAligned []RepairOutput `json:"migrationsAligned"`
	MigrationsDeleted []RepairOutput `json:"migrationsDeleted"`
	MigrationsRemoved []RepairOutput `json:"migrationsRemoved"`
	FlywayVersion     string         `json:"flywayVersion"`
	Database          string         `json:"database"`
	Warnings          []string       `json:"warnings"`
	Operation         string         `json:"operation"`

	// Deprecated: Alias fields for backward-compatibility with Go code and tests
	Schema            string   `json:"-"`
	Table             string   `json:"-"`
	RemovedFailed     []string `json:"-"`
	AlignedChecksums  []string `json:"-"`
	DeletedMigrations []string `json:"-"`
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
		return &RepairResult{
			RepairActions:     []string{},
			MigrationsAligned: []RepairOutput{},
			MigrationsDeleted: []RepairOutput{},
			MigrationsRemoved: []RepairOutput{},
			FlywayVersion:     m.FlywayVersion(),
			Database:          m.DatabaseName(),
			Warnings:          []string{},
			Operation:         "repair",
			Schema:            defaultSchema,
			Table:             table,
		}, nil
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
		RepairActions:     make([]string, 0),
		MigrationsAligned: make([]RepairOutput, 0),
		MigrationsDeleted: make([]RepairOutput, 0),
		MigrationsRemoved: make([]RepairOutput, 0),
		FlywayVersion:     m.FlywayVersion(),
		Database:          m.DatabaseName(),
		Warnings:          make([]string, 0),
		Operation:         "repair",
		Schema:            defaultSchema,
		Table:             table,
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
			vStr := ""
			if app.Version != nil {
				vStr = app.Version.String()
			}
			result.MigrationsRemoved = append(result.MigrationsRemoved, RepairOutput{
				Version:     vStr,
				Description: app.Description,
				Filepath:    app.Script,
			})
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
					result.MigrationsAligned = append(result.MigrationsAligned, RepairOutput{
						Version:     res.Version.String(),
						Description: res.Description,
						Filepath:    res.Script,
					})
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
					vStr := ""
					if app.Version != nil {
						vStr = app.Version.String()
					}
					result.MigrationsDeleted = append(result.MigrationsDeleted, RepairOutput{
						Version:     vStr,
						Description: app.Description,
						Filepath:    app.Script,
					})
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
					result.MigrationsAligned = append(result.MigrationsAligned, RepairOutput{
						Version:     "",
						Description: res.Description,
						Filepath:    res.Script,
					})
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
					result.MigrationsDeleted = append(result.MigrationsDeleted, RepairOutput{
						Version:     "",
						Description: app.Description,
						Filepath:    app.Script,
					})
				}
			}
		}
	}

	if len(result.MigrationsRemoved) > 0 {
		result.RepairActions = append(result.RepairActions, "Remove failed migrations")
	}
	if len(result.MigrationsAligned) > 0 {
		result.RepairActions = append(result.RepairActions, "Align checksums")
	}
	if len(result.MigrationsDeleted) > 0 {
		result.RepairActions = append(result.RepairActions, "Delete missing migrations")
	}

	// 6. Fire afterRepair callbacks
	if err := m.callbackRunner.Fire(ctx, "afterRepair"); err != nil {
		return nil, err
	}

	return result, nil
}
