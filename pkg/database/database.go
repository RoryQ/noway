package database

import (
	"context"
	"time"

	"github.com/RoryQ/noway/pkg/resolver"
)

// HistoryRecord represents a row in the flyway_schema_history table.
type HistoryRecord struct {
	InstalledRank int
	Version       *string
	Description   string
	Type          string
	Script        string
	Checksum      *int64
	InstalledBy   string
	InstalledOn   time.Time
	ExecutionTime int64
	Success       bool
}

// Database represents the interface for interacting with the database.
type Database interface {
	// Close closes the database connection.
	Close() error

	// Quote quotes identifiers for this database dialect.
	Quote(identifiers ...string) string

	// GetCurrentUser returns the database user session.
	GetCurrentUser(ctx context.Context) (string, error)

	// EnsureSchema creates the schema/dataset if it doesn't exist.
	EnsureSchema(ctx context.Context, schema string) error

	// SchemaExists checks if the schema/dataset exists.
	SchemaExists(ctx context.Context, schema string) (bool, error)

	// SchemaEmpty checks if the schema has no tables, views, or routines.
	SchemaEmpty(ctx context.Context, schema string) (bool, error)

	// DropSchema drops the entire schema/dataset and its contents.
	DropSchema(ctx context.Context, schema string) error

	// CleanSchema drops all tables, views, routines in the schema without dropping the schema itself.
	CleanSchema(ctx context.Context, schema string) error

	// EnsureHistoryTable creates the schema history table if it doesn't exist.
	EnsureHistoryTable(ctx context.Context, schema, table string) error

	// HistoryTableExists checks if the schema history table exists.
	HistoryTableExists(ctx context.Context, schema, table string) (bool, error)

	// FetchHistory retrieves all history records ordered by installed_rank ASC.
	FetchHistory(ctx context.Context, schema, table string) ([]resolver.AppliedMigration, error)

	// InsertHistory inserts a new applied migration record into the history table.
	InsertHistory(ctx context.Context, schema, table string, record HistoryRecord) error

	// UpdateHistory updates an existing migration record (used during repair / baseline alignment).
	UpdateHistory(ctx context.Context, schema, table string, record HistoryRecord) error

	// DeleteHistory removes a history record (used during repair for failed migrations).
	DeleteHistory(ctx context.Context, schema, table string, installedRank int) error

	// Lock acquires an exclusive lock on the schema history table.
	Lock(ctx context.Context, schema, table string) (UnlockFunc, error)

	// ExecuteStatement executes a single SQL statement on the database.
	ExecuteStatement(ctx context.Context, sql string) error
}

// UnlockFunc releases the acquired database lock.
type UnlockFunc func(ctx context.Context) error
