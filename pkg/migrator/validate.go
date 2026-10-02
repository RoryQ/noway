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

	m.callbackRunner.callbacks = resolved.Callbacks
	m.callbackRunner.SetOperation("VALIDATE")

	if err := m.callbackRunner.Fire(ctx, "beforeValidate"); err != nil {
		_ = m.callbackRunner.Fire(ctx, "afterValidateError")
		return nil, err
	}

	// 2. Fetch applied migrations
	exists, err := m.db.HistoryTableExists(ctx, defaultSchema, table)
	if err != nil {
		_ = m.callbackRunner.Fire(ctx, "afterValidateError")
		return nil, fmt.Errorf("failed to check history table existence: %w", err)
	}
	if !exists {
		// No history table yet -> valid (clean state)
		if err := m.callbackRunner.Fire(ctx, "afterValidate"); err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterValidateError")
			return nil, err
		}
		return &ValidateResult{Valid: true}, nil
	}

	applied, err := m.db.FetchHistory(ctx, defaultSchema, table)
	if err != nil {
		_ = m.callbackRunner.Fire(ctx, "afterValidateError")
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

	// Map repeatable resolved by description (primary) and script (fallback)
	resolvedRepeatableByDesc := make(map[string]resolver.ResolvedMigration)
	resolvedRepeatableByScript := make(map[string]resolver.ResolvedMigration)
	for _, res := range resolved.RepeatableMigrations {
		resolvedRepeatableByDesc[res.Description] = res
		resolvedRepeatableByScript[res.Script] = res
	}

	// Extract metadata from history: maxAppliedVersion and baselineVersion
	appliedVersions := make(map[string]bool)
	appliedByVersion := make(map[string]resolver.AppliedMigration)
	var maxAppliedVersion *version.Version
	var baselineVersion *version.Version
	for _, app := range applied {
		if app.Type == "BASELINE" && app.Version != nil {
			baselineVersion = app.Version
		}
		if app.Type != "DELETE" && app.Version != nil && !app.Version.IsEmpty() {
			appliedVersions[app.Version.Normalized()] = true
			appliedByVersion[app.Version.Normalized()] = app
			if app.Success {
				if maxAppliedVersion == nil || app.Version.IsNewerThan(*maxAppliedVersion) {
					maxAppliedVersion = app.Version
				}
			}
		}
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

		// Feature 14: Ignore BASELINE, SCHEMA, and DELETE history records
		if app.Type == "BASELINE" || app.Type == "SCHEMA" || app.Type == "DELETE" {
			continue
		}

		// Versioned migration validation
		if app.Version != nil && !app.Version.IsEmpty() {
			verKey := app.Version.Normalized()

			// Skip mismatch checks if at or below baseline
			if baselineVersion != nil && baselineVersion.IsAtLeast(*app.Version) {
				continue
			}

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
			// Feature 14: Repeatable migration validation
			// Match by description first, fallback to script
			res, found := resolvedRepeatableByDesc[app.Description]
			if !found {
				res, found = resolvedRepeatableByScript[app.Script]
			}
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

			// Check type
			if app.Type != string(res.Type) {
				result.Valid = false
				result.Errors = append(result.Errors, ValidationError{
					Version:     nil,
					Description: app.Description,
					File:        res.Script,
					Message: fmt.Sprintf("Migration type mismatch for repeatable migration %s\n-> Applied to database : %s\n-> Resolved locally    : %s",
						res.Script, app.Type, string(res.Type)),
				})
			}
		}
	}

	// Feature 13: Check for out-of-order pending versioned migrations
	if !m.config.OutOfOrder && maxAppliedVersion != nil {
		effectiveTarget := m.determineEffectiveTarget(
			maxAppliedVersion,
			baselineVersion,
			appliedByVersion,
			resolved.VersionedMigrations,
		)

		for _, res := range resolved.VersionedMigrations {
			if res.Version == nil || res.Version.IsEmpty() {
				continue
			}
			verKey := res.Version.Normalized()
			if appliedVersions[verKey] {
				continue
			}

			// Covered by baseline
			if baselineVersion != nil && baselineVersion.IsAtLeast(*res.Version) {
				continue
			}

			// Above target cutoff
			if effectiveTarget != nil && res.Version.IsNewerThan(*effectiveTarget) {
				continue
			}

			// Conditional shouldExecute check
			if res.Config.ShouldExecute != "" {
				shouldExec, err := resolver.EvaluateShouldExecute(res.Config.ShouldExecute, m.replacer, m.builtins)
				if err == nil && !shouldExec {
					continue
				}
			}

			// If strictly older than maximum applied version: out-of-order pending violation!
			if maxAppliedVersion.IsNewerThan(*res.Version) {
				result.Valid = false
				result.Errors = append(result.Errors, ValidationError{
					Version:     res.Version,
					Description: res.Description,
					File:        res.Script,
					Message:     fmt.Sprintf("Detected resolved migration not applied to database: %s", res.Version),
				})
			}
		}
	}

	if !result.Valid {
		_ = m.callbackRunner.Fire(ctx, "afterValidateError")
		return result, nil
	}

	if err := m.callbackRunner.Fire(ctx, "afterValidate"); err != nil {
		_ = m.callbackRunner.Fire(ctx, "afterValidateError")
		return nil, err
	}

	return result, nil
}
