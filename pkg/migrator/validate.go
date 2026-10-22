package migrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/earthboundkid/versioninfo/v2"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/resolver"
	"github.com/roryq/noway/pkg/version"
)

// ValidationError represents a discrepancy between local migrations and database history.
type ValidationError struct {
	Version     *version.Version `json:"version"`
	Description string           `json:"description"`
	File        string           `json:"file"`
	Message     string           `json:"message"`
}

// ErrorDetails represents Flyway error information.
type ErrorDetails struct {
	ErrorCode    string `json:"errorCode"`
	ErrorMessage string `json:"errorMessage"`
}

// ValidateOutput describes an invalid migration matching Flyway ValidateOutput.
type ValidateOutput struct {
	Version      string        `json:"version"`
	Description  string        `json:"description"`
	Filepath     string        `json:"filepath"`
	ErrorDetails *ErrorDetails `json:"errorDetails"`
}

// ValidateResult contains validation findings matching Flyway's output schema.
type ValidateResult struct {
	ValidationSuccessful bool             `json:"validationSuccessful"`
	ValidateCount        int              `json:"validateCount"`
	InvalidMigrations    []ValidateOutput `json:"invalidMigrations"`
	ErrorDetails         *ErrorDetails    `json:"errorDetails,omitempty"`
	FlywayVersion        string           `json:"flywayVersion"`
	Database             string           `json:"database"`
	Warnings             []string         `json:"warnings"`
	Operation            string           `json:"operation"`

	// Deprecated: Alias fields for backward-compatibility with Go code and tests
	Valid  bool              `json:"-"`
	Errors []ValidationError `json:"-"`
}

func (r *ValidateResult) finalize(db string, validateCount int) {
	r.ValidationSuccessful = r.Valid
	if versioninfo.Version != "" && versioninfo.Version != "unknown" && versioninfo.Version != "(devel)" {
		r.FlywayVersion = versioninfo.Version
	} else {
		r.FlywayVersion = versioninfo.Short()
	}
	r.Database = db
	r.Operation = "validate"
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	r.ValidateCount = validateCount
	if r.InvalidMigrations == nil {
		r.InvalidMigrations = []ValidateOutput{}
	}
	if !r.Valid {
		r.ErrorDetails = &ErrorDetails{
			ErrorCode:    "FAULT",
			ErrorMessage: r.Error(),
		}
		for _, err := range r.Errors {
			verStr := ""
			if err.Version != nil {
				verStr = err.Version.String()
			}
			r.InvalidMigrations = append(r.InvalidMigrations, ValidateOutput{
				Version:     verStr,
				Description: err.Description,
				Filepath:    err.File,
				ErrorDetails: &ErrorDetails{
					ErrorCode:    "FAULT",
					ErrorMessage: err.Message,
				},
			})
		}
	}
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
func (m *Migrator) Validate(ctx context.Context, ignorePending ...bool) (*ValidateResult, error) {
	ignorePendingArg := len(ignorePending) > 0 && ignorePending[0]
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table

	m.ensureUser(ctx)

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
		// If history table doesn't exist, check if any configured schemas are non-empty
		var nonEmptySchemas []string
		for _, schema := range m.config.Schemas {
			empty, err := m.db.SchemaEmpty(ctx, schema)
			if err != nil {
				_ = m.callbackRunner.Fire(ctx, "afterValidateError")
				return nil, fmt.Errorf("failed to check if schema '%s' is empty: %w", schema, err)
			}
			if !empty {
				nonEmptySchemas = append(nonEmptySchemas, schema)
			}
		}

		if len(nonEmptySchemas) > 0 && !m.config.BaselineOnMigrate {
			if err := m.callbackRunner.Fire(ctx, "afterValidateError"); err != nil {
				return nil, err
			}
			res := &ValidateResult{
				Valid: false,
				Errors: []ValidationError{
					{
						Message: fmt.Sprintf("Found non-empty schema(s) %s but no schema history table. Use baseline() or set baselineOnMigrate to true to initialize the schema history table.", strings.Join(nonEmptySchemas, ", ")),
					},
				},
			}
			res.finalize(m.DatabaseName(), 0)
			return res, nil
		}

		// No history table yet & schemas empty (or baselineOnMigrate) -> valid (clean state)
		if err := m.callbackRunner.Fire(ctx, "afterValidate"); err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterValidateError")
			return nil, err
		}
		res := &ValidateResult{Valid: true}
		res.finalize(m.DatabaseName(), 0)
		return res, nil
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

	resolvedUndoByVersion := make(map[string]resolver.ResolvedMigration)
	for _, res := range resolved.UndoMigrations {
		if res.Version != nil {
			resolvedUndoByVersion[res.Version.Normalized()] = res
		}
	}

	// Map repeatable resolved by description (primary) and script (fallback)
	resolvedRepeatableByDesc := make(map[string]resolver.ResolvedMigration)
	resolvedRepeatableByScript := make(map[string]resolver.ResolvedMigration)
	for _, res := range resolved.RepeatableMigrations {
		resolvedRepeatableByDesc[res.Description] = res
		resolvedRepeatableByDesc[database.AbbreviateDescription(res.Description)] = res
		resolvedRepeatableByScript[res.Script] = res
		resolvedRepeatableByScript[database.AbbreviateScript(res.Script)] = res
	}

	// Extract metadata from history: maxAppliedVersion and baselineVersion
	history := processAppliedHistory(applied)
	maxAppliedVersion := history.MaxAppliedVersion
	baselineVersion := history.BaselineVersion
	appliedByVersion := history.AppliedVersioned
	appliedVersions := make(map[string]bool)
	for verKey := range appliedByVersion {
		appliedVersions[verKey] = true
	}

	// Check each applied migration
	for _, app := range history.Records {
		if !app.Success {
			if app.Deleted || app.Undone {
				continue
			}
			// In Flyway, if app.Version is a future version and IgnoreFutureMigrations=true (or future is ignored),
			// this is FUTURE_FAILED, so it does not fail validation (Flyway only warns on future failed migrations).
			if app.Version != nil && !app.Version.IsEmpty() && (maxResolvedVersion == nil || app.Version.IsNewerThan(*maxResolvedVersion)) && m.isIgnored("future", app) {
				continue
			}
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Version:     app.Version,
				Description: app.Description,
				File:        app.Script,
				Message:     fmt.Sprintf("Migration %s failed in database on %s", app.Script, app.InstalledOn.Format("2006-01-02 15:04:05")),
			})
			continue
		}

		// Ignore BASELINE, SCHEMA, and DELETE history records, records marked Deleted or Undone, and UNDO records
		if app.Type == "BASELINE" || app.Type == "SCHEMA" || app.Type == "DELETE" || app.Deleted || app.Undone || strings.HasPrefix(app.Type, "UNDO") {
			continue
		}

		if len(m.config.CherryPick) > 0 && !m.config.IsPicked(app.Version, app.Description) {
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
					if !m.isIgnored("future", app) {
						result.Valid = false
						result.Errors = append(result.Errors, ValidationError{
							Version:     app.Version,
							Description: app.Description,
							File:        app.Script,
							Message:     fmt.Sprintf("Detected applied migration not resolved locally (future): %s", app.Script),
						})
					}
				} else {
					if !m.isIgnored("missing", app) {
						result.Valid = false
						result.Errors = append(result.Errors, ValidationError{
							Version:     app.Version,
							Description: app.Description,
							File:        app.Script,
							Message:     fmt.Sprintf("Detected applied migration not resolved locally: %s", app.Script),
						})
					}
				}
				continue
			}

			// Check checksum
			if app.Checksum != nil && !res.ChecksumMatches(app.Checksum) {
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
			if app.Description != res.Description && app.Description != database.AbbreviateDescription(res.Description) {
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
				// Repeatable migration deleted or renamed on disk -> report unless missing migrations are ignored
				if !m.isIgnored("missing", app) {
					result.Valid = false
					result.Errors = append(result.Errors, ValidationError{
						Version:     nil,
						Description: app.Description,
						File:        app.Script,
						Message:     fmt.Sprintf("Detected applied migration not resolved locally: %s", app.Script),
					})
				}
				continue
			}

			// Check description
			if app.Description != res.Description && app.Description != database.AbbreviateDescription(res.Description) {
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

	// Check for unapplied (pending / outdated) versioned migrations
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
		if len(m.config.CherryPick) > 0 && !m.config.IsPicked(res.Version, res.Description) {
			continue
		}
		verKey := res.Version.Normalized()
		if appliedVersions[verKey] || history.UndoneVersions[verKey] || history.DeletedVersions[verKey] || history.FailedVersions[verKey] {
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

		// Check if it's outdated or pending
		isOutdated := maxAppliedVersion != nil && maxAppliedVersion.IsNewerThan(*res.Version)
		if isOutdated {
			if m.config.OutOfOrder {
				// With OutOfOrder enabled, in Flyway its state becomes PENDING
				if !ignorePendingArg && !m.isIgnored("pending", res) {
					result.Valid = false
					result.Errors = append(result.Errors, ValidationError{
						Version:     res.Version,
						Description: res.Description,
						File:        res.Script,
						Message:     fmt.Sprintf("Detected resolved migration not applied to database: %s", res.Version),
					})
				}
			} else {
				// OutOfOrder disabled: state is OUTDATED
				if !m.isIgnored("outdated", res) && !m.isIgnored("pending", res) {
					result.Valid = false
					result.Errors = append(result.Errors, ValidationError{
						Version:     res.Version,
						Description: res.Description,
						File:        res.Script,
						Message:     fmt.Sprintf("Detected resolved migration not applied to database: %s", res.Version),
					})
				}
			}
		} else {
			// Normal pending migration
			if !ignorePendingArg && !m.isIgnored("pending", res) {
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

	validateCount := len(resolved.VersionedMigrations) + len(resolved.RepeatableMigrations)
	result.finalize(m.DatabaseName(), validateCount)

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

// isIgnored checks whether a given migration state is ignored according to IgnoreMigrationPatterns.
// State may be "future", "missing", "pending", "outdated", or "ignored".
func (m *Migrator) isIgnored(state string, migDetails ...any) bool {
	if m.config == nil {
		return false
	}
	var migType string
	var ver *version.Version
	for _, d := range migDetails {
		switch v := d.(type) {
		case string:
			migType = v
		case *version.Version:
			ver = v
		case version.Version:
			ver = &v
		case resolver.ResolvedMigration:
			migType = string(v.Type)
			ver = v.Version
		case resolver.AppliedMigration:
			migType = v.Type
			ver = v.Version
		}
	}
	return m.config.IsIgnored(state, migType, ver)
}
