package migrator

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/version"
)

// BaselineResult contains the baseline operation outcome matching Flyway's output schema.
type BaselineResult struct {
	BaselineVersion       string   `json:"baselineVersion"`
	SuccessfullyBaselined bool     `json:"successfullyBaselined"`
	FlywayVersion         string   `json:"flywayVersion"`
	Database              string   `json:"database"`
	Warnings              []string `json:"warnings"`
	Operation             string   `json:"operation"`

	// Deprecated: Alias fields for backward-compatibility with Go code and tests
	Schema              string `json:"-"`
	Table               string `json:"-"`
	BaselineDescription string `json:"-"`
	Success             bool   `json:"-"`
}

// Baseline baselines an existing database, setting the baseline version marker.
func (m *Migrator) Baseline(ctx context.Context) (res *BaselineResult, retErr error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table

	m.ensureUser(ctx)

	// 1. Ensure schema exists
	if m.config.CreateSchemas {
		if err := m.db.EnsureSchema(ctx, defaultSchema); err != nil {
			return nil, fmt.Errorf("failed to ensure schema exists: %w", err)
		}
	} else {
		exists, err := m.db.SchemaExists(ctx, defaultSchema)
		if err != nil {
			return nil, fmt.Errorf("failed to check schema '%s' existence: %w", defaultSchema, err)
		}
		if !exists {
			return nil, fmt.Errorf("schema '%s' does not exist and createSchemas is false", defaultSchema)
		}
	}

	// 2. Ensure history table exists
	if err := m.db.EnsureHistoryTable(ctx, defaultSchema, table); err != nil {
		return nil, fmt.Errorf("failed to create schema history table: %w", err)
	}

	// 3. Acquire lock
	unlock, err := m.db.Lock(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire lock for baseline: %w", err)
	}
	defer func() {
		if uErr := unlock(context.Background()); uErr != nil && retErr == nil {
			retErr = uErr
		}
	}()

	return m.baselineInternal(ctx, defaultSchema, table)
}

func (m *Migrator) baselineInternal(ctx context.Context, defaultSchema, table string) (*BaselineResult, error) {
	bVer := m.config.BaselineVersion
	if bVer == "" {
		bVer = "1"
	}
	bDesc := m.config.BaselineDescription
	if bDesc == "" {
		bDesc = "<< Flyway Baseline >>"
	}

	// Validate baseline version
	targetVer, err := version.Parse(bVer)
	if err != nil {
		return nil, fmt.Errorf("invalid baseline version '%s': %w", bVer, err)
	}

	// 1. Check existing history
	applied, err := m.db.FetchHistory(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch schema history: %w", err)
	}

	nonSchemaApplied := 0
	for _, app := range applied {
		if app.Type == "BASELINE" {
			if app.Version != nil && app.Version.Compare(targetVer) == 0 && (app.Description == bDesc || app.Description == database.AbbreviateDescription(bDesc)) {
				log.Printf("INFO: Schema %s already baselined with version %s (%s). Skipping.", defaultSchema, bVer, bDesc)
				return &BaselineResult{
					BaselineVersion:       bVer,
					SuccessfullyBaselined: true,
					FlywayVersion:         m.FlywayVersion(),
					Database:              m.DatabaseName(),
					Warnings:              make([]string, 0),
					Operation:             "baseline",
					Schema:                defaultSchema,
					Table:                 table,
					BaselineDescription:   bDesc,
					Success:               true,
				}, nil
			}
			return nil, fmt.Errorf("unable to baseline database: schema history table already baselined at version %s", app.Version.String())
		}
		if app.Type != "SCHEMA" {
			nonSchemaApplied++
		}
	}

	if nonSchemaApplied > 0 {
		return nil, fmt.Errorf("unable to baseline database: schema history table is not empty and already contains %d applied migration(s)", nonSchemaApplied)
	}

	// 2. Fire beforeBaseline callbacks
	if err := m.callbackRunner.Fire(ctx, "beforeBaseline"); err != nil {
		return nil, err
	}

	// 3. Insert baseline record
	user := m.config.InstalledBy
	if user == "" {
		currentUser, _ := m.db.GetCurrentUser(ctx)
		if currentUser != "" {
			user = currentUser
		} else {
			user = "flyway"
		}
	}

	nextRank := 1
	for _, app := range applied {
		if app.InstalledRank >= nextRank {
			nextRank = app.InstalledRank + 1
		}
	}

	rec := database.HistoryRecord{
		InstalledRank: nextRank,
		Version:       &bVer,
		Description:   database.AbbreviateDescription(bDesc),
		Type:          "BASELINE",
		Script:        database.AbbreviateScript(bDesc),
		Checksum:      nil,
		InstalledBy:   user,
		InstalledOn:   time.Now(),
		ExecutionTime: 0,
		Success:       true,
	}

	if err := m.db.InsertHistory(ctx, defaultSchema, table, rec); err != nil {
		_ = m.callbackRunner.Fire(ctx, "afterBaselineError")
		return nil, fmt.Errorf("failed to insert baseline record: %w", err)
	}

	// 4. Fire afterBaseline callbacks
	if err := m.callbackRunner.Fire(ctx, "afterBaseline"); err != nil {
		return nil, err
	}

	return &BaselineResult{
		BaselineVersion:       bVer,
		SuccessfullyBaselined: true,
		FlywayVersion:         m.FlywayVersion(),
		Database:              m.DatabaseName(),
		Warnings:              make([]string, 0),
		Operation:             "baseline",
		Schema:                defaultSchema,
		Table:                 table,
		BaselineDescription:   bDesc,
		Success:               true,
	}, nil
}
