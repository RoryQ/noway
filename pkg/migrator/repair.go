package migrator

import (
	"context"
	"fmt"

	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/resolver"
	"github.com/roryq/noway/pkg/version"
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
func (m *Migrator) Repair(ctx context.Context) (res *RepairResult, retErr error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table

	m.ensureUser(ctx)

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
		if uErr := unlock(context.Background()); uErr != nil && retErr == nil {
			retErr = uErr
		}
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
	var maxResolvedVersion *version.Version
	for _, res := range resolved.VersionedMigrations {
		if res.Version != nil {
			resolvedByVersion[res.Version.Normalized()] = res
			if maxResolvedVersion == nil || res.Version.IsNewerThan(*maxResolvedVersion) {
				maxResolvedVersion = res.Version
			}
		}
	}
	resolvedRepeatableByDesc := make(map[string]resolver.ResolvedMigration)
	for _, res := range resolved.RepeatableMigrations {
		resolvedRepeatableByDesc[res.Description] = res
		resolvedRepeatableByDesc[database.AbbreviateDescription(res.Description)] = res
	}

	user := m.config.InstalledBy
	if user == "" {
		currentUser, _ := m.db.GetCurrentUser(ctx)
		if currentUser != "" {
			user = currentUser
		} else {
			user = "flyway"
		}
	}

	history := processAppliedHistory(applied)
	nextRank := history.NextInstalledRank

	// Track versions/descriptions deleted during this repair run so multiple active runs
	// of the same migration only produce one new DELETE row. Prior DELETE rows have already
	// marked earlier runs with app.Deleted = true via processAppliedHistory.
	deletedVersions := make(map[string]bool)
	deletedRepeatableDescs := make(map[string]bool)

	for _, app := range history.Records {
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

		if app.Type == "BASELINE" || app.Type == "SCHEMA" || app.Type == "DELETE" || app.Deleted || app.Undone {
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
				if (app.Description != res.Description && app.Description != database.AbbreviateDescription(res.Description)) || app.Type != string(res.Type) {
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
						Description:   database.AbbreviateDescription(res.Description),
						Type:          string(res.Type),
						Script:        database.AbbreviateScript(res.Script),
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
				// Feature 18: Missing versioned migration on disk -> Insert separate DELETE record
				if m.isIgnored("missing", app) {
					continue
				}
				if !deletedVersions[verKey] {
					deletedVersions[verKey] = true
					var verStr *string
					if app.Version != nil {
						s := app.Version.String()
						verStr = &s
					}
					rec := database.HistoryRecord{
						InstalledRank: nextRank,
						Version:       verStr,
						Description:   app.Description,
						Type:          "DELETE",
						Script:        app.Script,
						Checksum:      app.Checksum,
						InstalledBy:   user,
						ExecutionTime: 0,
						Success:       true,
					}
					if err := m.db.InsertHistory(ctx, defaultSchema, table, rec); err != nil {
						_ = m.callbackRunner.Fire(ctx, "afterRepairError")
						return nil, fmt.Errorf("failed to insert delete record for migration %s: %w", app.Script, err)
					}
					nextRank++
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
			if found {
				// Flyway parity: Do NOT align checksums for edited repeatable migrations!
				// Only align if equivalent checksum matches (raw content matches, replaced differs)
				if res.ChecksumMatchesWithoutBeingIdentical(app.Checksum) {
					rec := database.HistoryRecord{
						InstalledRank: app.InstalledRank,
						Version:       nil,
						Description:   database.AbbreviateDescription(res.Description),
						Type:          string(res.Type),
						Script:        database.AbbreviateScript(res.Script),
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
				// Feature 18: Missing repeatable migration on disk -> Insert separate DELETE record
				if m.isIgnored("missing", app) {
					continue
				}
				if !deletedRepeatableDescs[app.Description] {
					deletedRepeatableDescs[app.Description] = true
					rec := database.HistoryRecord{
						InstalledRank: nextRank,
						Version:       nil,
						Description:   app.Description,
						Type:          "DELETE",
						Script:        app.Script,
						Checksum:      app.Checksum,
						InstalledBy:   user,
						ExecutionTime: 0,
						Success:       true,
					}
					if err := m.db.InsertHistory(ctx, defaultSchema, table, rec); err != nil {
						_ = m.callbackRunner.Fire(ctx, "afterRepairError")
						return nil, fmt.Errorf("failed to insert delete record for repeatable migration %s: %w", app.Script, err)
					}
					nextRank++
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
