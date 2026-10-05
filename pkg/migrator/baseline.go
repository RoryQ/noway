package migrator

import (
	"context"
	"fmt"
	"time"

	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/version"
)

// BaselineResult contains the baseline operation outcome.
type BaselineResult struct {
	Schema              string `json:"schema"`
	Table               string `json:"table"`
	BaselineVersion     string `json:"baselineVersion"`
	BaselineDescription string `json:"baselineDescription"`
	Success             bool   `json:"success"`
}

// Baseline baselines an existing database, setting the baseline version marker.
func (m *Migrator) Baseline(ctx context.Context) (*BaselineResult, error) {
	defaultSchema := m.config.GetDefaultSchema()
	table := m.config.Table

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
		_ = unlock(context.Background())
	}()

	return m.baselineInternal(ctx, defaultSchema, table)
}

func (m *Migrator) baselineInternal(ctx context.Context, defaultSchema, table string) (*BaselineResult, error) {
	// 1. Check existing history
	applied, err := m.db.FetchHistory(ctx, defaultSchema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch schema history: %w", err)
	}

	nonSchemaApplied := 0
	for _, app := range applied {
		if app.Type == "BASELINE" {
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

	bVer := m.config.BaselineVersion
	if bVer == "" {
		bVer = "1"
	}
	bDesc := m.config.BaselineDescription
	if bDesc == "" {
		bDesc = "<< Flyway Baseline >>"
	}

	// Validate baseline version
	_, err = version.Parse(bVer)
	if err != nil {
		return nil, fmt.Errorf("invalid baseline version '%s': %w", bVer, err)
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
		Description:   bDesc,
		Type:          "BASELINE",
		Script:        bDesc,
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
		Schema:              defaultSchema,
		Table:               table,
		BaselineVersion:     bVer,
		BaselineDescription: bDesc,
		Success:             true,
	}, nil
}
