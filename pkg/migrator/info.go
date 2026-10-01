package migrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/RoryQ/noway/pkg/resolver"
	"github.com/RoryQ/noway/pkg/version"
)

// InfoResult contains the migration info report.
type InfoResult struct {
	Schema       string                   `json:"schema"`
	Table        string                   `json:"table"`
	Migrations   []resolver.MigrationInfo `json:"migrations"`
	FlywayFormat []FlywayInfoItem         `json:"flywayFormat"`
}

// FlywayInfoItem matches Flyway's JSON info schema.
type FlywayInfoItem struct {
	Category      string `json:"category"`
	Version       string `json:"version"`
	Description   string `json:"description"`
	Type          string `json:"type"`
	InstalledOn   string `json:"installedOn"`
	State         string `json:"state"`
	Undoable      string `json:"undoable"`
	File          string `json:"filepath"`
	InstalledBy   string `json:"installedBy"`
	ExecutionTime int64  `json:"executionTime"`
}

// Info gathers info on all resolved and applied migrations.
func (m *Migrator) Info(ctx context.Context) (*InfoResult, error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table

	// 1. Resolve local migrations
	res, err := m.resolver.Resolve()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve migrations: %w", err)
	}

	// 2. Fetch applied migrations
	var applied []resolver.AppliedMigration
	exists, err := m.db.HistoryTableExists(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to check history table existence: %w", err)
	}
	if exists {
		applied, err = m.db.FetchHistory(ctx, defaultSchema, table)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch schema history: %w", err)
		}
	}

	// Determine effective target version
	var effectiveTargetVersion *version.Version
	if m.config.TargetVersion != nil {
		effectiveTargetVersion = m.config.TargetVersion
	} else if strings.EqualFold(m.config.Target, "current") {
		var maxAppliedVersion *version.Version
		for _, app := range applied {
			if app.Version != nil && !app.Version.IsEmpty() {
				if maxAppliedVersion == nil || app.Version.IsNewerThan(*maxAppliedVersion) {
					maxAppliedVersion = app.Version
				}
			}
		}
		if maxAppliedVersion != nil {
			effectiveTargetVersion = maxAppliedVersion
		} else {
			emptyVer := version.Empty
			effectiveTargetVersion = &emptyVer
		}
	}

	// 3. Compute merged migration infos
	infos := computeMigrationInfos(res, applied, effectiveTargetVersion, m.config.OutOfOrder)

	result := &InfoResult{
		Schema:     defaultSchema,
		Table:      table,
		Migrations: infos,
	}

	for _, info := range infos {
		instOn := ""
		if info.InstalledOn != nil {
			instOn = info.InstalledOn.Format("2006-01-02 15:04:05")
		}
		execTime := int64(0)
		if info.ExecutionTime != nil {
			execTime = *info.ExecutionTime
		}
		category := "Versioned"
		if info.Version == nil || info.Version.IsEmpty() {
			category = "Repeatable"
		}
		if info.Type == "BASELINE" {
			category = "Baseline"
		}

		result.FlywayFormat = append(result.FlywayFormat, FlywayInfoItem{
			Category:      category,
			Version:       info.VersionString(),
			Description:   info.Description,
			Type:          info.Type,
			InstalledOn:   instOn,
			State:         string(info.State),
			Undoable:      "No",
			File:          info.Script,
			InstalledBy:   info.InstalledBy,
			ExecutionTime: execTime,
		})
	}

	return result, nil
}

// RenderTable outputs a formatted table matching Flyway CLI output.
func (r *InfoResult) RenderTable(w io.Writer) {
	fmt.Fprintf(w, "Schema version: %s\n\n", r.Schema)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Category\tVersion\tDescription\tType\tInstalled On\tState\tExecution Time\tChecksum")
	fmt.Fprintln(tw, "--------\t-------\t-----------\t----\t------------\t-----\t--------------\t--------")

	for _, item := range r.Migrations {
		cat := "Versioned"
		if item.Version == nil || item.Version.IsEmpty() {
			cat = "Repeatable"
		}
		if item.Type == "BASELINE" {
			cat = "Baseline"
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			cat,
			item.VersionString(),
			item.Description,
			item.Type,
			item.InstalledOnString(),
			string(item.State),
			item.ExecutionTimeString(),
			item.ChecksumString(),
		)
	}

	tw.Flush()
}

// RenderJSON returns JSON bytes.
func (r *InfoResult) RenderJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func computeMigrationInfos(
	resolved *resolver.ResolveResult,
	applied []resolver.AppliedMigration,
	targetVer *version.Version,
	outOfOrder bool,
) []resolver.MigrationInfo {
	var infos []resolver.MigrationInfo

	appliedRepeatables := make(map[string][]resolver.AppliedMigration) // script -> applied runs

	var maxAppliedVersion *version.Version
	var baselineVersion *version.Version

	for _, app := range applied {
		if app.Type == "BASELINE" && app.Version != nil {
			baselineVersion = app.Version
		}
		if app.Version != nil && !app.Version.IsEmpty() {
			if maxAppliedVersion == nil || app.Version.IsNewerThan(*maxAppliedVersion) {
				maxAppliedVersion = app.Version
			}
		} else {
			appliedRepeatables[app.Script] = append(appliedRepeatables[app.Script], app)
		}
	}

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

	// 1. Process all applied versioned migrations
	seenResolved := make(map[string]bool)
	var prevMaxVer *version.Version

	for _, app := range applied {
		if app.Version == nil || app.Version.IsEmpty() {
			continue
		}

		appVer := app.Version
		verKey := appVer.Normalized()
		res, exists := resolvedByVersion[verKey]

		state := resolver.StateSuccess
		if !app.Success {
			if !exists {
				if maxResolvedVersion != nil && appVer.IsNewerThan(*maxResolvedVersion) {
					state = resolver.StateFutureFailed
				} else {
					state = resolver.StateMissingFailed
				}
			} else {
				state = resolver.StateFailed
			}
		} else if app.Type == "BASELINE" {
			state = resolver.StateBaseline
		} else if !exists {
			if maxResolvedVersion != nil && appVer.IsNewerThan(*maxResolvedVersion) {
				state = resolver.StateFutureSuccess
			} else {
				state = resolver.StateMissingSuccess
			}
		} else if prevMaxVer != nil && prevMaxVer.IsNewerThan(*appVer) {
			state = resolver.StateOutOfOrder
		}

		if prevMaxVer == nil || appVer.IsNewerThan(*prevMaxVer) {
			prevMaxVer = appVer
		}

		var cs *int64 = app.Checksum
		var resPtr *resolver.ResolvedMigration
		if exists {
			seenResolved[verKey] = true
			rCopy := res
			resPtr = &rCopy
		}

		appCopy := app
		instOn := app.InstalledOn
		execTime := app.ExecutionTime

		infos = append(infos, resolver.MigrationInfo{
			Version:       appVer,
			Description:   app.Description,
			Type:          app.Type,
			Script:        app.Script,
			Checksum:      cs,
			InstalledBy:   app.InstalledBy,
			InstalledOn:   &instOn,
			ExecutionTime: &execTime,
			State:         state,
			Resolved:      resPtr,
			Applied:       &appCopy,
		})
	}

	// 2. Process pending resolved versioned migrations
	for _, res := range resolved.VersionedMigrations {
		verKey := res.Version.Normalized()
		if seenResolved[verKey] {
			continue
		}

		state := resolver.StatePending
		if baselineVersion != nil && baselineVersion.IsAtLeast(*res.Version) {
			state = resolver.StateBelowBaseline
		} else if targetVer != nil && res.Version.IsNewerThan(*targetVer) {
			state = resolver.StateAboveTarget
		} else if maxAppliedVersion != nil && maxAppliedVersion.IsNewerThan(*res.Version) {
			if !outOfOrder {
				state = resolver.StateIgnored
			} else {
				state = resolver.StatePending
			}
		}

		rCopy := res
		cs := res.Checksum
		infos = append(infos, resolver.MigrationInfo{
			Version:     res.Version,
			Description: res.Description,
			Type:        string(res.Type),
			Script:      res.Script,
			Checksum:    &cs,
			State:       state,
			Resolved:    &rCopy,
		})
	}

	// 3. Process Repeatable migrations
	for _, res := range resolved.RepeatableMigrations {
		runs, wasApplied := appliedRepeatables[res.Script]
		rCopy := res
		cs := res.Checksum

		if !wasApplied {
			infos = append(infos, resolver.MigrationInfo{
				Version:     nil,
				Description: res.Description,
				Type:        string(res.Type),
				Script:      res.Script,
				Checksum:    &cs,
				State:       resolver.StatePending,
				Resolved:    &rCopy,
			})
		} else {
			// Process previous runs (marked as Superseded)
			for i := 0; i < len(runs)-1; i++ {
				run := runs[i]
				instOn := run.InstalledOn
				execTime := run.ExecutionTime
				appCopy := run
				infos = append(infos, resolver.MigrationInfo{
					Version:       nil,
					Description:   res.Description,
					Type:          string(res.Type),
					Script:        res.Script,
					Checksum:      run.Checksum,
					InstalledBy:   run.InstalledBy,
					InstalledOn:   &instOn,
					ExecutionTime: &execTime,
					State:         resolver.StateSuperseded,
					Resolved:      &rCopy,
					Applied:       &appCopy,
				})
			}

			// Process latest run
			lastRun := runs[len(runs)-1]
			state := resolver.StateSuccess
			if !lastRun.Success {
				state = resolver.StateFailed
			} else if lastRun.Checksum != nil && *lastRun.Checksum != res.Checksum {
				state = resolver.StateOutdated
			}

			instOn := lastRun.InstalledOn
			execTime := lastRun.ExecutionTime
			appCopy := lastRun

			infos = append(infos, resolver.MigrationInfo{
				Version:       nil,
				Description:   res.Description,
				Type:          string(res.Type),
				Script:        res.Script,
				Checksum:      &cs,
				InstalledBy:   lastRun.InstalledBy,
				InstalledOn:   &instOn,
				ExecutionTime: &execTime,
				State:         state,
				Resolved:      &rCopy,
				Applied:       &appCopy,
			})
		}
	}

	return infos
}

// StringTable returns table representation as string.
func (r *InfoResult) StringTable() string {
	var buf bytes.Buffer
	r.RenderTable(&buf)
	return buf.String()
}
