package resolver

import (
	"fmt"
	"time"

	"github.com/RoryQ/noway/pkg/version"
)

// MigrationType represents the type of a migration.
type MigrationType string

const (
	TypeSQL        MigrationType = "SQL"
	TypeBaseline   MigrationType = "BASELINE"
	TypeSchema     MigrationType = "SCHEMA"
	TypeDelete     MigrationType = "DELETE"
	TypeRepeatable MigrationType = "REPEATABLE"
	TypeUndo       MigrationType = "UNDO"
)

// MigrationState represents the current state of a migration.
type MigrationState string

const (
	StatePending         MigrationState = "Pending"
	StateSuccess         MigrationState = "Success"
	StateFailed          MigrationState = "Failed"
	StateIgnored         MigrationState = "Ignored"
	StateBaseline        MigrationState = "Baseline"
	StateBaselineIgnored MigrationState = "Ignored (Baseline)"
	StateBelowBaseline   MigrationState = "Below Baseline"
	StateAboveTarget     MigrationState = "Above Target"
	StateOutOfOrder      MigrationState = "Out of Order"
	StateFutureSuccess   MigrationState = "Future"
	StateFutureFailed    MigrationState = "Failed (Future)"
	StateMissingSuccess  MigrationState = "Missing"
	StateMissingFailed   MigrationState = "Failed (Missing)"
	StateOutdated        MigrationState = "Outdated"
	StateSuperseded      MigrationState = "Superseded"
	StateUndone          MigrationState = "Undone"
	StateAvailable       MigrationState = "Available"
	StateDeleted         MigrationState = "Deleted"
)

// ResolvedMigration is a migration found on disk or classpath.
type ResolvedMigration struct {
	Version          *version.Version
	Description      string
	Script           string
	Checksum         int64
	Type             MigrationType
	Content          string
	PhysicalLocation string
	IsRepeatable     bool
	IsUndo           bool
	IsBaseline       bool
}

// AppliedMigration is a migration read from the schema history table.
type AppliedMigration struct {
	InstalledRank int
	Version       *version.Version
	Description   string
	Type          string
	Script        string
	Checksum      *int64
	InstalledBy   string
	InstalledOn   time.Time
	ExecutionTime int64
	Success       bool
}

// MigrationInfo provides comprehensive state information about a resolved or applied migration.
type MigrationInfo struct {
	Version       *version.Version
	Description   string
	Type          string
	Script        string
	Checksum      *int64
	InstalledBy   string
	InstalledOn   *time.Time
	ExecutionTime *int64
	State         MigrationState
	Resolved      *ResolvedMigration
	Applied       *AppliedMigration
}

// VersionString returns string version or empty string for repeatable.
func (m *MigrationInfo) VersionString() string {
	if m.Version != nil && !m.Version.IsEmpty() {
		return m.Version.String()
	}
	return ""
}

// ChecksumString returns formatted checksum string.
func (m *MigrationInfo) ChecksumString() string {
	if m.Checksum != nil {
		return fmt.Sprintf("%d", *m.Checksum)
	}
	return ""
}

// ExecutionTimeString returns formatted execution time.
func (m *MigrationInfo) ExecutionTimeString() string {
	if m.ExecutionTime != nil {
		return fmt.Sprintf("%dms", *m.ExecutionTime)
	}
	return ""
}

// InstalledOnString returns formatted installed_on timestamp.
func (m *MigrationInfo) InstalledOnString() string {
	if m.InstalledOn != nil {
		return m.InstalledOn.Format("2006-01-02 15:04:05")
	}
	return ""
}
