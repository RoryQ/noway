package migrator

import (
	"context"
	"fmt"

	"github.com/RoryQ/noway/pkg/checksum"
	"github.com/RoryQ/noway/pkg/database"
	"github.com/RoryQ/noway/pkg/resolver"
)

// RepairResult contains the repaired migrations outcome.
type RepairResult struct {
	Schema           string   `json:"schema"`
	Table            string   `json:"table"`
	RemovedFailed    []string `json:"removedFailed"`
	AlignedChecksums []string `json:"alignedChecksums"`
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
	resolvedRepeatable := make(map[string]resolver.ResolvedMigration)
	for _, res := range resolved.RepeatableMigrations {
		resolvedRepeatable[res.Script] = res
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
				effectiveCs := res.Checksum
				shouldReplace := m.config.PlaceholderReplacement
				if res.Config.PlaceholderReplacement != nil {
					shouldReplace = *res.Config.PlaceholderReplacement
				}
				if shouldReplace {
					if replaced, err := m.replacer.Replace(res.Content, m.builtins); err == nil {
						if newCs, err := checksum.CalculateString(replaced); err == nil {
							effectiveCs = newCs
						}
					}
				}

				needsUpdate := false
				if app.Checksum == nil || *app.Checksum != effectiveCs {
					needsUpdate = true
				}
				if app.Description != res.Description || app.Type != string(res.Type) {
					needsUpdate = true
				}

				if needsUpdate {
					cs := effectiveCs
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
			}
		} else {
			// Repeatable migration alignment
			if res, found := resolvedRepeatable[app.Script]; found {
				if app.Description != res.Description || app.Type != string(res.Type) {
					rec := database.HistoryRecord{
						InstalledRank: app.InstalledRank,
						Version:       nil,
						Description:   res.Description,
						Type:          string(res.Type),
						Script:        res.Script,
						Checksum:      app.Checksum,
					}
					if err := m.db.UpdateHistory(ctx, defaultSchema, table, rec); err != nil {
						_ = m.callbackRunner.Fire(ctx, "afterRepairError")
						return nil, fmt.Errorf("failed to align metadata for %s: %w", app.Script, err)
					}
					result.AlignedChecksums = append(result.AlignedChecksums, res.Script)
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
