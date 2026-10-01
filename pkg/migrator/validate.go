package migrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/RoryQ/noway/pkg/resolver"
	"github.com/RoryQ/noway/pkg/version"
)

// ValidationError represents a discrepancy between local migrations and database history.
type ValidationError struct {
	Version     *version.Version `json:"version"`
	Description string           `json:"description"`
	File        string           `json:"file"`
	Message     string           `json:"message"`
}

// ValidateResult contains validation findings.
type ValidateResult struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationError `json:"errors"`
}

// Error formats validation errors into a single error string matching Flyway format.
func (r *ValidateResult) Error() string {
	if r.Valid || len(r.Errors) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Validate failed: %d error(s) found:\n", len(r.Errors)))
	for i, err := range r.Errors {
		sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, err.Message))
	}
	return sb.String()
}

// Validate validates the applied migrations in the database against the resolved local migrations.
func (m *Migrator) Validate(ctx context.Context) (*ValidateResult, error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table

	// 1. Resolve local migrations
	resolved, err := m.resolver.Resolve()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve migrations: %w", err)
	}

	// 2. Fetch applied migrations
	exists, err := m.db.HistoryTableExists(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to check history table existence: %w", err)
	}
	if !exists {
		// No history table yet -> valid (clean state)
		return &ValidateResult{Valid: true}, nil
	}

	applied, err := m.db.FetchHistory(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch schema history: %w", err)
	}

	result := &ValidateResult{Valid: true}

	// Map resolved by canonical version
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

	// Map repeatable resolved by script name
	resolvedRepeatable := make(map[string]resolver.ResolvedMigration)
	for _, res := range resolved.RepeatableMigrations {
		resolvedRepeatable[res.Script] = res
	}

	// Check each applied migration
	for _, app := range applied {
		if !app.Success {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Version:     app.Version,
				Description: app.Description,
				File:        app.Script,
				Message:     fmt.Sprintf("Migration %s failed in database on %s", app.Script, app.InstalledOn.Format("2006-01-02 15:04:05")),
			})
			continue
		}

		if app.Type == "BASELINE" || app.Type == "SCHEMA" {
			continue
		}

		// Versioned migration validation
		if app.Version != nil && !app.Version.IsEmpty() {
			verKey := app.Version.Normalized()
			res, found := resolvedByVersion[verKey]
			if !found {
				// Check if it's future
				if maxResolvedVersion != nil && app.Version.IsNewerThan(*maxResolvedVersion) {
					result.Valid = false
					result.Errors = append(result.Errors, ValidationError{
						Version:     app.Version,
						Description: app.Description,
						File:        app.Script,
						Message:     fmt.Sprintf("Detected applied migration not resolved locally (future): %s", app.Script),
					})
				} else {
					result.Valid = false
					result.Errors = append(result.Errors, ValidationError{
						Version:     app.Version,
						Description: app.Description,
						File:        app.Script,
						Message:     fmt.Sprintf("Detected applied migration not resolved locally: %s", app.Script),
					})
				}
				continue
			}

			// Check checksum
			if app.Checksum != nil && *app.Checksum != res.Checksum {
				result.Valid = false
				result.Errors = append(result.Errors, ValidationError{
					Version:     app.Version,
					Description: app.Description,
					File:        res.Script,
					Message: fmt.Sprintf("Migration checksum mismatch for migration version %s\n-> Applied to database : %d\n-> Resolved locally    : %d\nEither revert the changes to the file, or run repair to update the schema history.",
						app.Version.String(), *app.Checksum, res.Checksum),
				})
			}

			// Check description
			if app.Description != res.Description {
				result.Valid = false
				result.Errors = append(result.Errors, ValidationError{
					Version:     app.Version,
					Description: app.Description,
					File:        res.Script,
					Message: fmt.Sprintf("Migration description mismatch for migration version %s\n-> Applied to database : %s\n-> Resolved locally    : %s",
						app.Version.String(), app.Description, res.Description),
				})
			}

			// Check type
			if app.Type != string(res.Type) {
				result.Valid = false
				result.Errors = append(result.Errors, ValidationError{
					Version:     app.Version,
					Description: app.Description,
					File:        res.Script,
					Message: fmt.Sprintf("Migration type mismatch for migration version %s\n-> Applied to database : %s\n-> Resolved locally    : %s",
						app.Version.String(), app.Type, string(res.Type)),
				})
			}
		} else {
			// Repeatable migration validation
			res, found := resolvedRepeatable[app.Script]
			if !found {
				result.Valid = false
				result.Errors = append(result.Errors, ValidationError{
					Version:     nil,
					Description: app.Description,
					File:        app.Script,
					Message:     fmt.Sprintf("Detected applied repeatable migration not resolved locally: %s", app.Script),
				})
				continue
			}

			// Check description
			if app.Description != res.Description {
				result.Valid = false
				result.Errors = append(result.Errors, ValidationError{
					Version:     nil,
					Description: app.Description,
					File:        res.Script,
					Message: fmt.Sprintf("Repeatable migration description mismatch for %s\n-> Applied to database : %s\n-> Resolved locally    : %s",
						app.Script, app.Description, res.Description),
				})
			}
		}
	}

	return result, nil
}
