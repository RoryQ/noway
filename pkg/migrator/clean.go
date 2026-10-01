package migrator

import (
	"context"
	"fmt"
)

// CleanResult contains the clean operation summary.
type CleanResult struct {
	SchemasCleaned []string `json:"schemasCleaned"`
}

// Clean drops all objects (tables, views, materialized views, routines) in the configured schemas.
func (m *Migrator) Clean(ctx context.Context) (*CleanResult, error) {
	if m.config.CleanDisabled {
		return nil, fmt.Errorf("clean failed: flyway.cleanDisabled is set to true. Set cleanDisabled=false in config or -cleanDisabled=false on CLI to enable clean")
	}

	schemas := m.config.Schemas
	if len(schemas) == 0 {
		defaultSchema := m.config.GetDefaultSchema()
		if defaultSchema != "" {
			schemas = []string{defaultSchema}
		}
	}

	// 1. Fire beforeClean callbacks
	if err := m.callbackRunner.Fire(ctx, "beforeClean"); err != nil {
		return nil, err
	}

	result := &CleanResult{}

	// 2. Clean each schema
	for _, schema := range schemas {
		exists, err := m.db.SchemaExists(ctx, schema)
		if err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterCleanError")
			return nil, fmt.Errorf("failed checking schema '%s': %w", schema, err)
		}
		if !exists {
			continue
		}

		if err := m.db.CleanSchema(ctx, schema); err != nil {
			_ = m.callbackRunner.Fire(ctx, "afterCleanError")
			return nil, fmt.Errorf("failed cleaning schema '%s': %w", schema, err)
		}
		result.SchemasCleaned = append(result.SchemasCleaned, schema)
	}

	// 3. Fire afterClean callbacks
	if err := m.callbackRunner.Fire(ctx, "afterClean"); err != nil {
		return nil, err
	}

	return result, nil
}
