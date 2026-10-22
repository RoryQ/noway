package migrator

import (
	"strings"

	"github.com/roryq/noway/pkg/resolver"
	"github.com/roryq/noway/pkg/version"
)

// ProcessedHistory represents the parsed schema history state, properly interpreting
// DELETE audit rows, UNDO rows, and failed migrations.
type ProcessedHistory struct {
	// Records is the complete history in chronological order with Deleted and Undone flags populated.
	Records []resolver.AppliedMigration

	// AppliedVersioned contains active, successfully applied versioned migrations (non-deleted, non-undone).
	AppliedVersioned map[string]resolver.AppliedMigration

	// AppliedRepeatables contains active, successfully applied repeatable migrations (non-deleted).
	AppliedRepeatables map[string]resolver.AppliedMigration

	// FailedMigrations contains active failed migrations (not deleted and not undone).
	FailedMigrations []resolver.AppliedMigration

	// FailedVersions contains versions that failed in the database (not deleted and not undone).
	FailedVersions map[string]bool

	// DeletedVersions contains versions marked deleted.
	DeletedVersions map[string]bool

	// UndoneVersions contains versions marked undone.
	UndoneVersions map[string]bool

	// MaxAppliedVersion is the highest version among active, successfully applied versioned migrations.
	MaxAppliedVersion *version.Version

	// BaselineVersion is the version from a successful BASELINE record, if any.
	BaselineVersion *version.Version

	// NextInstalledRank is max(installed_rank) + 1 across all records in history.
	NextInstalledRank int
}

// processAppliedHistory analyzes applied records in chronological order and resolves
// the status of each migration (e.g. marking previous records deleted or undone).
func processAppliedHistory(records []resolver.AppliedMigration) ProcessedHistory {
	result := ProcessedHistory{
		Records:            make([]resolver.AppliedMigration, len(records)),
		AppliedVersioned:   make(map[string]resolver.AppliedMigration),
		AppliedRepeatables: make(map[string]resolver.AppliedMigration),
		FailedVersions:     make(map[string]bool),
		DeletedVersions:    make(map[string]bool),
		UndoneVersions:     make(map[string]bool),
		NextInstalledRank:  1,
	}

	copy(result.Records, records)

	// Step 1: Resolve Deleted and Undone status in chronological order
	for i := 0; i < len(result.Records); i++ {
		rec := &result.Records[i]
		if rec.InstalledRank >= result.NextInstalledRank {
			result.NextInstalledRank = rec.InstalledRank + 1
		}

		if !rec.Success {
			continue
		}

		// When a DELETE row is encountered, mark all prior matching applied rows as Deleted
		if rec.Type == "DELETE" {
			if rec.Version != nil && !rec.Version.IsEmpty() {
				result.DeletedVersions[rec.Version.Normalized()] = true
				for j := i - 1; j >= 0; j-- {
					prev := &result.Records[j]
					if prev.Type != "DELETE" && prev.Type != "SCHEMA" && prev.Version != nil && prev.Version.Equals(*rec.Version) {
						prev.Deleted = true
					}
				}
			} else {
				for j := i - 1; j >= 0; j-- {
					prev := &result.Records[j]
					if prev.Type != "DELETE" && prev.Type != "SCHEMA" && (prev.Version == nil || prev.Version.IsEmpty()) && prev.Description == rec.Description {
						prev.Deleted = true
					}
				}
			}
		} else if strings.HasPrefix(rec.Type, "UNDO") {
			// When an UNDO row is encountered, mark prior matching applied rows as Undone
			if rec.Version != nil && !rec.Version.IsEmpty() {
				result.UndoneVersions[rec.Version.Normalized()] = true
				for j := i - 1; j >= 0; j-- {
					prev := &result.Records[j]
					if !strings.HasPrefix(prev.Type, "UNDO") && prev.Type != "SCHEMA" && prev.Version != nil && prev.Version.Equals(*rec.Version) {
						prev.Undone = true
					}
				}
			}
		}
	}

	// Step 2: Categorize active applied, baseline, and failed migrations
	for _, rec := range result.Records {
		if rec.Type == "SCHEMA" {
			continue
		}

		if rec.Type == "BASELINE" && rec.Version != nil && rec.Success {
			result.BaselineVersion = rec.Version
		}

		if !rec.Success {
			if !rec.Deleted && !rec.Undone {
				result.FailedMigrations = append(result.FailedMigrations, rec)
				if rec.Version != nil && !rec.Version.IsEmpty() {
					result.FailedVersions[rec.Version.Normalized()] = true
				}
			}
			continue
		}

		// Synthetic DELETE rows, deleted migrations, and undone migrations are not active applied migrations
		if rec.Type == "DELETE" || rec.Deleted || strings.HasPrefix(rec.Type, "UNDO") || rec.Undone {
			continue
		}

		if rec.Version != nil && !rec.Version.IsEmpty() {
			result.AppliedVersioned[rec.Version.Normalized()] = rec
			if result.MaxAppliedVersion == nil || rec.Version.IsNewerThan(*result.MaxAppliedVersion) {
				result.MaxAppliedVersion = rec.Version
			}
		} else {
			key := rec.Description
			if key == "" {
				key = rec.Script
			}
			result.AppliedRepeatables[key] = rec
		}
	}

	return result
}
