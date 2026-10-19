package migrator

import (
	"context"
	"fmt"
)

// CleanResult contains the clean operation summary matching Flyway's output schema.
type CleanResult struct {
	SchemasCleaned []string `json:"schemasCleaned"`
	SchemasDropped []string `json:"schemasDropped"`
	FlywayVersion  string   `json:"flywayVersion"`
	Database       string   `json:"database"`
	Warnings       []string `json:"warnings"`
	Operation      string   `json:"operation"`
}

// Clean drops all objects (tables, views, materialized views, routines) in the configured schemas.
func (m *Migrator) Clean(ctx context.Context) (*CleanResult, error) {
	return m.cleanInternal(ctx, "")
}

// cleanInternal cleans the schemas, optionally preserving a specified table (e.g. the schema history table to retain the active lock).
func (m *Migrator) cleanInternal(ctx context.Context, preserveTable string) (*CleanResult, error) {
	if m.config.CleanDisabled {
		return nil, fmt.Errorf("clean failed: flyway.cleanDisabled is set to true. Set cleanDisabled=false in config or -cleanDisabled=false on CLI to enable clean")
	}

	defaultSchema := m.config.GetDefaultSchema()
	schemas := m.config.Schemas
	if len(schemas) == 0 {
		schemas = []string{defaultSchema}
	}

	m.ensureUser(ctx)

	// 1. Fire beforeClean callbacks
	if err := m.callbackRunner.Fire(ctx, "beforeClean"); err != nil {
		return nil, err
	}

	result := &CleanResult{
		SchemasCleaned: make([]string, 0),
		SchemasDropped: make([]string, 0),
		FlywayVersion:  m.FlywayVersion(),
		Database:       m.DatabaseName(),
		Warnings:       make([]string, 0),
		Operation:      "clean",
	}

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

		var cleanErr error
		if preserveTable != "" && schema == defaultSchema {
			cleanErr = m.db.CleanSchema(ctx, schema, preserveTable)
		} else {
			cleanErr = m.db.CleanSchema(ctx, schema)
		}
		if cleanErr != nil {
			_ = m.callbackRunner.Fire(ctx, "afterCleanError")
			return nil, fmt.Errorf("failed cleaning schema '%s': %w", schema, cleanErr)
		}
		result.SchemasCleaned = append(result.SchemasCleaned, schema)
	}

	// 3. Fire afterClean callbacks
	if err := m.callbackRunner.Fire(ctx, "afterClean"); err != nil {
		return nil, err
	}

	return result, nil
}
