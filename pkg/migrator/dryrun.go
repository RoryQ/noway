package migrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/resolver"
	"github.com/roryq/noway/pkg/version"
)

// DryRunBuilder generates formatted, executable SQL output representing all actions in a dry run.
type DryRunBuilder struct {
	db       database.Database
	schema   string
	table    string
	database string
	user     string
	sb       strings.Builder
}

// NewDryRunBuilder creates a new DryRunBuilder.
func NewDryRunBuilder(db database.Database, schema, table, dbName, user string) *DryRunBuilder {
	return &DryRunBuilder{
		db:       db,
		schema:   schema,
		table:    table,
		database: dbName,
		user:     user,
	}
}

// sqlStringLiteral returns a BigQuery/Spanner single-quoted string literal.
func sqlStringLiteral(s string) string {
	var sb strings.Builder
	sb.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\\':
			sb.WriteString(`\\`)
		case '\'':
			sb.WriteString(`\'`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&sb, `\x%02x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('\'')
	return sb.String()
}

// sqlComment makes s safe to put after "-- ".
func sqlComment(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == '\u0085' {
			sb.WriteRune(' ')
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// WriteHeader outputs the Dry Run header comment block.
func (b *DryRunBuilder) WriteHeader(operation, targetVersion string) {
	b.sb.WriteString("-- --------------------------------------------------------------------------------\n")
	b.sb.WriteString(fmt.Sprintf("-- Flyway / Noway Dry Run Output (%s)\n", sqlComment(operation)))
	if b.database != "" {
		b.sb.WriteString(fmt.Sprintf("-- Database: %s\n", sqlComment(b.database)))
	}
	b.sb.WriteString(fmt.Sprintf("-- Schema: %s\n", sqlComment(b.schema)))
	if targetVersion != "" {
		b.sb.WriteString(fmt.Sprintf("-- Target Version: %s\n", sqlComment(targetVersion)))
	}
	b.sb.WriteString(fmt.Sprintf("-- Generated: %s\n", time.Now().Format("2006-01-02 15:04:05 MST")))
	b.sb.WriteString("-- --------------------------------------------------------------------------------\n\n")
}

// WriteSchemaCreation outputs schema creation DDL if needed.
func (b *DryRunBuilder) WriteSchemaCreation(schema string) {
	quoted := b.db.Quote(schema)
	b.sb.WriteString(fmt.Sprintf("-- Ensure schema exists: %s\n", sqlComment(schema)))
	b.sb.WriteString(fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s;\n\n", quoted))
}

// WriteHistoryTableCreation outputs DDL to create the schema history table.
func (b *DryRunBuilder) WriteHistoryTableCreation() {
	b.sb.WriteString("-- Ensure schema history table exists\n")
	ddl := b.db.HistoryTableDDL(b.schema, b.table)
	b.sb.WriteString(ddl)
	b.sb.WriteString("\n\n")
}

// WriteBaseline outputs the baseline history record insertion.
func (b *DryRunBuilder) WriteBaseline(rank int, version, description string) {
	b.sb.WriteString("-- Baseline schema history\n")
	b.WriteHistoryInsert(rank, version, description, "BASELINE", description, 0, b.user)
}

// WriteCallback outputs callback statements.
func (b *DryRunBuilder) WriteCallback(event string, cb resolver.ResolvedCallback, replacedSQL string) {
	trimmed := strings.TrimSpace(replacedSQL)
	if trimmed == "" {
		return
	}
	b.sb.WriteString(fmt.Sprintf("-- Callback: %s (%s)\n", sqlComment(event), sqlComment(cb.Filename)))
	b.sb.WriteString(trimmed)
	if !strings.HasSuffix(trimmed, ";") {
		b.sb.WriteString(";")
	}
	b.sb.WriteString("\n\n")
}

// WriteMigration outputs a migration's SQL statements followed by history table insert.
func (b *DryRunBuilder) WriteMigration(mig resolver.ResolvedMigration, replacedSQL string, rank int, user string) {
	verStr := ""
	if mig.Version != nil {
		verStr = fmt.Sprintf("Version: %s, ", mig.Version.String())
	}
	recType := string(mig.Type)
	if recType == "" {
		recType = "SQL"
	}

	b.sb.WriteString("-- --------------------------------------------------------------------------------\n")
	b.sb.WriteString(fmt.Sprintf("-- Migration: %s (%sDescription: %s, Type: %s)\n", sqlComment(mig.Script), sqlComment(verStr), sqlComment(mig.Description), sqlComment(recType)))
	b.sb.WriteString("-- --------------------------------------------------------------------------------\n")
	trimmed := strings.TrimSpace(replacedSQL)
	if trimmed != "" {
		b.sb.WriteString(trimmed)
		if !strings.HasSuffix(trimmed, ";") {
			b.sb.WriteString(";")
		}
		b.sb.WriteString("\n\n")
	}

	vStr := ""
	if mig.Version != nil {
		vStr = mig.Version.String()
	}
	b.WriteHistoryInsert(rank, vStr, mig.Description, recType, mig.Script, mig.Checksum, user)
}

// WriteUndo outputs an undo migration's SQL statements and history record management.
func (b *DryRunBuilder) WriteUndo(mig resolver.ResolvedMigration, replacedSQL string, rank int, user string) {
	verStr := ""
	if mig.Version != nil {
		verStr = fmt.Sprintf("Version: %s, ", mig.Version.String())
	}

	b.sb.WriteString("-- --------------------------------------------------------------------------------\n")
	b.sb.WriteString(fmt.Sprintf("-- Undo Migration: %s (%sDescription: %s)\n", sqlComment(mig.Script), sqlComment(verStr), sqlComment(mig.Description)))
	b.sb.WriteString("-- --------------------------------------------------------------------------------\n")
	trimmed := strings.TrimSpace(replacedSQL)
	if trimmed != "" {
		b.sb.WriteString(trimmed)
		if !strings.HasSuffix(trimmed, ";") {
			b.sb.WriteString(";")
		}
		b.sb.WriteString("\n\n")
	}

	recType := "UNDO_SQL"
	if mig.IsScript {
		recType = "UNDO_SCRIPT"
	}
	vStr := ""
	if mig.Version != nil {
		vStr = mig.Version.String()
	}
	b.WriteHistoryInsert(rank, vStr, mig.Description, recType, mig.Script, mig.Checksum, user)
}

// WriteHistoryInsert writes an INSERT statement for flyway_schema_history.
func (b *DryRunBuilder) WriteHistoryInsert(rank int, version, description, recType, script string, checksum int64, user string) {
	quotedTable := b.db.Quote(b.schema, b.table)

	verVal := "NULL"
	if version != "" {
		verVal = sqlStringLiteral(version)
	}
	descVal := sqlStringLiteral(description)
	typeVal := sqlStringLiteral(recType)
	scriptVal := sqlStringLiteral(script)
	userVal := sqlStringLiteral(user)

	csVal := "NULL"
	if recType != "BASELINE" && recType != "SCHEMA" {
		csVal = fmt.Sprintf("%d", checksum)
	}

	b.sb.WriteString("-- Record migration in schema history table\n")
	b.sb.WriteString(fmt.Sprintf("INSERT INTO %s (`installed_rank`, `version`, `description`, `type`, `script`, `checksum`, `installed_by`, `installed_on`, `execution_time`, `success`)\n", quotedTable))
	b.sb.WriteString(fmt.Sprintf("VALUES (%d, %s, %s, %s, %s, %s, %s, CURRENT_TIMESTAMP(), 0, TRUE);\n\n", rank, verVal, descVal, typeVal, scriptVal, csVal, userVal))
}

// String returns the generated SQL content.
func (b *DryRunBuilder) String() string {
	return b.sb.String()
}

// Save outputs the dry run content to the destination file or stdout.
func (b *DryRunBuilder) Save(destination string) error {
	content := b.String()
	if destination == "-" || strings.EqualFold(destination, "stdout") {
		fmt.Print(content)
		return nil
	}

	dir := filepath.Dir(destination)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("failed to create directory for dryRunOutput '%s': %w", destination, err)
		}
	}

	if err := os.WriteFile(destination, []byte(content), 0o600); err != nil {
		return fmt.Errorf("failed to write dryRunOutput to '%s': %w", destination, err)
	}
	_ = os.Chmod(destination, 0o600)

	return nil
}

func (m *Migrator) migrateDryRun(
	ctx context.Context,
	defaultSchema, table, user string,
	createdSchemas []string,
	historyExists bool,
	nonEmptySchemas []string,
	pendingVersioned, pendingRepeatable []resolver.ResolvedMigration,
	initialVersionStr string,
	effectiveTargetVersion *version.Version,
	maxAppliedVersion *version.Version,
	nextInstalledRank int,
) (*MigrateResult, error) {
	builder := NewDryRunBuilder(m.db, defaultSchema, table, m.builtins.Database, user)
	targetVerStr := ""
	if effectiveTargetVersion != nil {
		targetVerStr = effectiveTargetVersion.String()
	}
	builder.WriteHeader("migrate", targetVerStr)

	// 1. Schema creation DDL
	for _, schema := range createdSchemas {
		builder.WriteSchemaCreation(schema)
	}

	// 2. History table creation DDL
	if !historyExists {
		builder.WriteHistoryTableCreation()
		if len(nonEmptySchemas) > 0 && m.config.BaselineOnMigrate {
			bVer := m.config.BaselineVersion
			if bVer == "" {
				bVer = "1"
			}
			bDesc := m.config.BaselineDescription
			if bDesc == "" {
				bDesc = "<< Flyway Baseline >>"
			}
			builder.WriteBaseline(1, bVer, bDesc)
			if nextInstalledRank <= 1 {
				nextInstalledRank = 2
			}
		}
	}

	// 3. beforeMigrate callbacks
	for _, cb := range m.callbackRunner.callbacks["beforeMigrate"] {
		if !isScriptFile(cb.Filename) {
			sql, err := m.replacer.Replace(cb.Content, m.builtins)
			if err == nil {
				builder.WriteCallback("beforeMigrate", cb, sql)
			}
		}
	}

	totalToApply := len(pendingVersioned) + len(pendingRepeatable)
	result := &MigrateResult{
		InitialSchemaVersion: initialVersionStr,
		InitialVersion:       initialVersionStr,
		SchemaName:           defaultSchema,
		Migrations:           make([]ExecutedSummary, 0, totalToApply),
		ExecutedMigrations:   make([]ExecutedSummary, 0, totalToApply),
		Success:              true,
		FlywayVersion:        m.FlywayVersion(),
		Database:             m.DatabaseName(),
		Warnings:             make([]string, 0),
		Operation:            "migrate",
	}

	currentVersion := maxAppliedVersion

	// 4. Versioned Migrations
	for _, mig := range pendingVersioned {
		sql := mig.Content
		shouldReplace := m.config.PlaceholderReplacement
		if mig.Config.PlaceholderReplacement != nil {
			shouldReplace = *mig.Config.PlaceholderReplacement
		}
		if shouldReplace && m.replacer != nil {
			replaced, err := m.replacer.ReplaceContent(sql, m.builtins)
			if err != nil {
				return nil, fmt.Errorf("error replacing placeholders in %s: %w", mig.Script, err)
			}
			sql = replaced
		}

		builder.WriteMigration(mig, sql, nextInstalledRank, user)

		verStr := ""
		if mig.Version != nil {
			verStr = mig.Version.String()
		}
		exec := ExecutedSummary{
			Category:      "Versioned",
			Version:       verStr,
			Description:   mig.Description,
			Type:          string(mig.Type),
			Script:        mig.Script,
			ExecutionTime: 0,
		}
		result.Migrations = append(result.Migrations, exec)
		result.ExecutedMigrations = result.Migrations
		result.MigrationsExecuted++
		nextInstalledRank++
		if currentVersion == nil || mig.Version.IsNewerThan(*currentVersion) {
			currentVersion = mig.Version
		}
	}

	// 5. afterVersioned callbacks
	for _, cb := range m.callbackRunner.callbacks["afterVersioned"] {
		if !isScriptFile(cb.Filename) {
			sql, _ := m.replacer.Replace(cb.Content, m.builtins)
			builder.WriteCallback("afterVersioned", cb, sql)
		}
	}

	// 6. beforeRepeatables callbacks
	if len(pendingRepeatable) > 0 {
		for _, cb := range m.callbackRunner.callbacks["beforeRepeatables"] {
			if !isScriptFile(cb.Filename) {
				sql, _ := m.replacer.Replace(cb.Content, m.builtins)
				builder.WriteCallback("beforeRepeatables", cb, sql)
			}
		}
	}

	// 7. Repeatable Migrations
	for _, mig := range pendingRepeatable {
		sql := mig.Content
		shouldReplace := m.config.PlaceholderReplacement
		if mig.Config.PlaceholderReplacement != nil {
			shouldReplace = *mig.Config.PlaceholderReplacement
		}
		if shouldReplace && m.replacer != nil {
			replaced, err := m.replacer.ReplaceContent(sql, m.builtins)
			if err != nil {
				return nil, fmt.Errorf("error replacing placeholders in %s: %w", mig.Script, err)
			}
			sql = replaced
		}

		builder.WriteMigration(mig, sql, nextInstalledRank, user)

		exec := ExecutedSummary{
			Category:      "Repeatable",
			Version:       "",
			Description:   mig.Description,
			Type:          string(mig.Type),
			Script:        mig.Script,
			ExecutionTime: 0,
		}
		result.Migrations = append(result.Migrations, exec)
		result.ExecutedMigrations = result.Migrations
		result.MigrationsExecuted++
		nextInstalledRank++
	}

	// 8. afterMigrateApplied callbacks
	if len(result.Migrations) > 0 {
		for _, cb := range m.callbackRunner.callbacks["afterMigrateApplied"] {
			if !isScriptFile(cb.Filename) {
				sql, _ := m.replacer.Replace(cb.Content, m.builtins)
				builder.WriteCallback("afterMigrateApplied", cb, sql)
			}
		}
	}

	// 9. afterMigrate callbacks
	for _, cb := range m.callbackRunner.callbacks["afterMigrate"] {
		if !isScriptFile(cb.Filename) {
			sql, _ := m.replacer.Replace(cb.Content, m.builtins)
			builder.WriteCallback("afterMigrate", cb, sql)
		}
	}

	if currentVersion != nil {
		result.TargetSchemaVersion = currentVersion.String()
		result.TargetVersion = currentVersion.String()
	} else {
		result.TargetSchemaVersion = initialVersionStr
		result.TargetVersion = initialVersionStr
	}

	if err := builder.Save(m.config.DryRunOutput); err != nil {
		return nil, err
	}

	return result, nil
}

func (m *Migrator) undoDryRun(
	ctx context.Context,
	defaultSchema, table, user string,
	items []UndoItem,
	initialVer, targetVer string,
	nextRank int,
) (*UndoResult, error) {
	builder := NewDryRunBuilder(m.db, defaultSchema, table, m.builtins.Database, user)
	builder.WriteHeader("undo", initialVer)

	// Callbacks: beforeUndo
	for _, cb := range m.callbackRunner.callbacks["beforeUndo"] {
		if !isScriptFile(cb.Filename) {
			sql, _ := m.replacer.Replace(cb.Content, m.builtins)
			builder.WriteCallback("beforeUndo", cb, sql)
		}
	}

	var outputs []UndoOutput
	rank := nextRank
	for _, item := range items {
		for _, cb := range m.callbackRunner.callbacks["beforeEachUndo"] {
			if !isScriptFile(cb.Filename) {
				sql, _ := m.replacer.Replace(cb.Content, m.builtins)
				builder.WriteCallback("beforeEachUndo", cb, sql)
			}
		}

		builder.WriteUndo(item.Resolved, item.SQL, rank, user)
		rank++

		for _, cb := range m.callbackRunner.callbacks["afterEachUndo"] {
			if !isScriptFile(cb.Filename) {
				sql, _ := m.replacer.Replace(cb.Content, m.builtins)
				builder.WriteCallback("afterEachUndo", cb, sql)
			}
		}

		outputs = append(outputs, UndoOutput{
			Category:      "Versioned",
			Version:       item.Resolved.Version.String(),
			Description:   item.Resolved.Description,
			Filepath:      item.Resolved.Script,
			ExecutionTime: 0,
		})
	}

	for _, cb := range m.callbackRunner.callbacks["afterUndo"] {
		if !isScriptFile(cb.Filename) {
			sql, _ := m.replacer.Replace(cb.Content, m.builtins)
			builder.WriteCallback("afterUndo", cb, sql)
		}
	}

	if err := builder.Save(m.config.DryRunOutput); err != nil {
		return nil, err
	}

	res := &UndoResult{
		InitialSchemaVersion: initialVer,
		TargetSchemaVersion:  targetVer,
		SchemaName:           defaultSchema,
		Migrations:           outputs,
		MigrationsUndone:     len(outputs),
		Success:              true,
		FlywayVersion:        m.FlywayVersion(),
		Database:             m.DatabaseName(),
		Warnings:             make([]string, 0),
		Operation:            "undo",
	}
	if len(outputs) > 0 {
		res.UndoneVersion = outputs[len(outputs)-1].Version
		res.Script = outputs[len(outputs)-1].Filepath
		res.ExecutionTime = 0
	}
	return res, nil
}
