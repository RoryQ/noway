package noway

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database"
	"github.com/RoryQ/noway/pkg/database/bigquery"
	"github.com/RoryQ/noway/pkg/migrator"
	"github.com/RoryQ/noway/pkg/resolver"
	"github.com/RoryQ/noway/pkg/version"
)

// Re-export common types for ease of use
type (
	Configuration    = config.Configuration
	Database         = database.Database
	MigrateResult    = migrator.MigrateResult
	InfoResult       = migrator.InfoResult
	ValidateResult   = migrator.ValidateResult
	BaselineResult   = migrator.BaselineResult
	RepairResult     = migrator.RepairResult
	CleanResult      = migrator.CleanResult
	UndoResult       = migrator.UndoResult
	MigrationInfo    = resolver.MigrationInfo
	Version          = version.Version
)

// Option configures Noway.
type Option func(*nowayOptions)

type nowayOptions struct {
	cfg        *config.Configuration
	configFile string
	db         database.Database
}

// WithConfig uses the provided Configuration object.
func WithConfig(cfg *config.Configuration) Option {
	return func(o *nowayOptions) {
		o.cfg = cfg
	}
}

// WithConfigFile loads configuration from a file (.conf, .toml, .yaml, .json).
func WithConfigFile(path string) Option {
	return func(o *nowayOptions) {
		o.configFile = path
	}
}

// WithDatabase provides a custom Database implementation (e.g. for testing).
func WithDatabase(db database.Database) Option {
	return func(o *nowayOptions) {
		o.db = db
	}
}

// WithJDBCURL sets the JDBC or custom BigQuery connection string.
func WithJDBCURL(url string) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.URL = url
	}
}

// WithProject sets the GCP Project ID.
func WithProject(projectID string) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.GCPProjectID = projectID
	}
}

// WithDataset sets the default BigQuery dataset (schema).
func WithDataset(dataset string) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.DefaultSchema = dataset
		o.cfg.GCPDataset = dataset
	}
}

// WithLocation sets the BigQuery dataset location/region (e.g. "US", "EU").
func WithLocation(location string) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.GCPLocation = location
	}
}

// WithCredentialsFile sets the path to GCP service account key file.
func WithCredentialsFile(filePath string) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.GCPCredentialsFile = filePath
	}
}

// WithCredentialsJSON sets GCP service account key JSON content.
func WithCredentialsJSON(jsonContent string) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.GCPCredentialsJSON = jsonContent
	}
}

// WithLocations sets migration directories or resources.
func WithLocations(locations ...string) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.Locations = locations
	}
}

// WithFS sets an embedded filesystem (e.g. embed.FS) containing migrations.
func WithFS(fileSys fs.FS) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.FS = fileSys
	}
}

// WithTable sets the schema history table name (default: flyway_schema_history).
func WithTable(table string) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.Table = table
	}
}

// WithTarget sets the target version for migration.
func WithTarget(target string) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.Target = target
	}
}

// WithOutOfOrder enables or disables applying migrations out of order.
func WithOutOfOrder(outOfOrder bool) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.OutOfOrder = outOfOrder
	}
}

// WithBaselineOnMigrate enables auto-baselining when migrating on non-empty schema.
func WithBaselineOnMigrate(baseline bool) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.BaselineOnMigrate = baseline
	}
}

// WithCleanDisabled enables or disables schema clean operations.
func WithCleanDisabled(disabled bool) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.CleanDisabled = disabled
	}
}

// WithPlaceholders configures custom placeholders.
func WithPlaceholders(placeholders map[string]string) Option {
	return func(o *nowayOptions) {
		if o.cfg == nil {
			o.cfg = config.NewDefaultConfiguration()
		}
		o.cfg.Placeholders = placeholders
	}
}

// Noway is the main client for executing database migrations.
type Noway struct {
	config   *config.Configuration
	db       database.Database
	migrator *migrator.Migrator
	ownsDB   bool
}

// New creates and initializes a Noway migration instance.
func New(opts ...Option) (*Noway, error) {
	options := &nowayOptions{}
	for _, opt := range opts {
		opt(options)
	}

	var cfg *config.Configuration
	var err error

	if options.configFile != "" {
		cfg, err = config.LoadFromFile(options.configFile)
		if err != nil {
			return nil, err
		}
	} else if options.cfg != nil {
		cfg = options.cfg
	} else {
		// Try to find auto config file or defaults
		if autoConf := config.FindConfigFile(); autoConf != "" {
			cfg, err = config.LoadFromFile(autoConf)
			if err != nil {
				return nil, err
			}
		} else {
			cfg = config.NewDefaultConfiguration()
		}
	}

	// Apply environment variables
	config.LoadFromEnv(cfg)

	// Finalize config
	if err := cfg.Finalize(); err != nil {
		return nil, fmt.Errorf("configuration error: %w", err)
	}

	var db database.Database
	ownsDB := false

	if options.db != nil {
		db = options.db
	} else {
		// Initialize BigQuery driver
		bqDB, err := bigquery.New(context.Background(), cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize BigQuery driver: %w", err)
		}
		db = bqDB
		ownsDB = true
	}

	m, err := migrator.New(cfg, db)
	if err != nil {
		if ownsDB {
			_ = db.Close()
		}
		return nil, fmt.Errorf("failed to initialize migrator: %w", err)
	}

	return &Noway{
		config:   cfg,
		db:       db,
		migrator: m,
		ownsDB:   ownsDB,
	}, nil
}

// Migrate executes pending migrations.
func (n *Noway) Migrate(ctx context.Context) (*migrator.MigrateResult, error) {
	return n.migrator.Migrate(ctx)
}

// Info retrieves status and details on all migrations.
func (n *Noway) Info(ctx context.Context) (*migrator.InfoResult, error) {
	return n.migrator.Info(ctx)
}

// Validate validates applied migrations against local migration scripts.
func (n *Noway) Validate(ctx context.Context) (*migrator.ValidateResult, error) {
	return n.migrator.Validate(ctx)
}

// Baseline baselines an existing database at a specific version.
func (n *Noway) Baseline(ctx context.Context) (*migrator.BaselineResult, error) {
	return n.migrator.Baseline(ctx)
}

// Repair repairs the schema history table.
func (n *Noway) Repair(ctx context.Context) (*migrator.RepairResult, error) {
	return n.migrator.Repair(ctx)
}

// Clean drops all objects in configured schemas.
func (n *Noway) Clean(ctx context.Context) (*migrator.CleanResult, error) {
	return n.migrator.Clean(ctx)
}

// Undo undoes the latest applied migration.
func (n *Noway) Undo(ctx context.Context) (*migrator.UndoResult, error) {
	return n.migrator.Undo(ctx)
}

// Close closes the underlying database connection.
func (n *Noway) Close() error {
	if n.ownsDB && n.db != nil {
		return n.db.Close()
	}
	return nil
}
