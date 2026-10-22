package config

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/roryq/noway/pkg/version"
)

// Clone returns a deep copy that shares no slices, maps, or mutable pointer values with c.
// FS can be shared as it is read-only.
func (c *Configuration) Clone() *Configuration {
	if c == nil {
		return nil
	}
	cp := *c
	cp.Schemas = slices.Clone(c.Schemas)
	cp.Locations = slices.Clone(c.Locations)
	cp.SQLMigrationSuffixes = slices.Clone(c.SQLMigrationSuffixes)
	if c.Placeholders != nil {
		cp.Placeholders = maps.Clone(c.Placeholders)
	}
	cp.IgnoreMigrationPatterns = slices.Clone(c.IgnoreMigrationPatterns)
	cp.CherryPick = slices.Clone(c.CherryPick)
	if c.TargetVersion != nil {
		tv := *c.TargetVersion
		cp.TargetVersion = &tv
	}
	if c.GCPOAuthType != nil {
		ot := *c.GCPOAuthType
		cp.GCPOAuthType = &ot
	}
	return &cp
}

// Configuration represents complete configuration for noway / flyway operations.
type Configuration struct {
	// Connection & Target
	Environment string   `json:"environment" yaml:"environment" toml:"environment"` // default: "default"
	URL         string   `json:"url" yaml:"url" toml:"url"`
	User        string   `json:"user" yaml:"user" toml:"user"`
	Password    string   `json:"password" yaml:"password" toml:"password"`
	Driver   string   `json:"driver" yaml:"driver" toml:"driver"`
	Schemas  []string `json:"schemas" yaml:"schemas" toml:"schemas"`
	DefaultSchema string `json:"defaultSchema" yaml:"defaultSchema" toml:"defaultSchema"`
	Table         string   `json:"table" yaml:"table" toml:"table"`                 // default: flyway_schema_history
	CreateSchemas bool     `json:"createSchemas" yaml:"createSchemas" toml:"createSchemas"` // default: true

	// Migrations Discovery
	Locations                 []string `json:"locations" yaml:"locations" toml:"locations"`
	FS                        fs.FS    `json:"-" yaml:"-" toml:"-"`
	SQLMigrationPrefix        string   `json:"sqlMigrationPrefix" yaml:"sqlMigrationPrefix" toml:"sqlMigrationPrefix"`                   // default: V
	RepeatableSQLMigrationPrefix string `json:"repeatableSqlMigrationPrefix" yaml:"repeatableSqlMigrationPrefix" toml:"repeatableSqlMigrationPrefix"` // default: R
	UndoSQLMigrationPrefix    string   `json:"undoSqlMigrationPrefix" yaml:"undoSqlMigrationPrefix" toml:"undoSqlMigrationPrefix"`             // default: U
	BaselineSQLMigrationPrefix string  `json:"baselineSqlMigrationPrefix" yaml:"baselineSqlMigrationPrefix" toml:"baselineSqlMigrationPrefix"`   // default: B
	SQLMigrationSeparator     string   `json:"sqlMigrationSeparator" yaml:"sqlMigrationSeparator" toml:"sqlMigrationSeparator"`                 // default: __
	SQLMigrationSuffixes      []string `json:"sqlMigrationSuffixes" yaml:"sqlMigrationSuffixes" toml:"sqlMigrationSuffixes"`                   // default: [.sql]
	Encoding                  string   `json:"encoding" yaml:"encoding" toml:"encoding"`                                                 // default: UTF-8
	WorkingDirectory          string   `json:"workingDirectory" yaml:"workingDirectory" toml:"workingDirectory"`

	// Placeholder replacement
	PlaceholderReplacement bool              `json:"placeholderReplacement" yaml:"placeholderReplacement" toml:"placeholderReplacement"` // default: true
	PlaceholderPrefix      string            `json:"placeholderPrefix" yaml:"placeholderPrefix" toml:"placeholderPrefix"`                 // default: ${
	PlaceholderSuffix      string            `json:"placeholderSuffix" yaml:"placeholderSuffix" toml:"placeholderSuffix"`                 // default: }
	PlaceholderSeparator   string            `json:"placeholderSeparator" yaml:"placeholderSeparator" toml:"placeholderSeparator"`       // default: :
	Placeholders           map[string]string `json:"placeholders" yaml:"placeholders" toml:"placeholders"`

	// Execution & Validation Control
	Target                  string           `json:"target" yaml:"target" toml:"target"`                                                 // default: latest
	TargetVersion           *version.Version `json:"-" yaml:"-" toml:"-"`
	OutOfOrder              bool             `json:"outOfOrder" yaml:"outOfOrder" toml:"outOfOrder"`                                     // default: false
	ValidateOnMigrate       bool             `json:"validateOnMigrate" yaml:"validateOnMigrate" toml:"validateOnMigrate"`               // default: true
	IgnoreFutureMigrations  bool             `json:"ignoreFutureMigrations" yaml:"ignoreFutureMigrations" toml:"ignoreFutureMigrations"` // default: true
	IgnoreMissingMigrations bool             `json:"ignoreMissingMigrations" yaml:"ignoreMissingMigrations" toml:"ignoreMissingMigrations"` // default: false
	IgnorePendingMigrations bool             `json:"ignorePendingMigrations" yaml:"ignorePendingMigrations" toml:"ignorePendingMigrations"` // default: false
	IgnoreMigrationPatterns              []string         `json:"ignoreMigrationPatterns" yaml:"ignoreMigrationPatterns" toml:"ignoreMigrationPatterns"` // default: ["*:future"]
	SkipExecutingMigrations              bool             `json:"skipExecutingMigrations" yaml:"skipExecutingMigrations" toml:"skipExecutingMigrations"` // default: false
	CherryPick                           []string         `json:"cherryPick" yaml:"cherryPick" toml:"cherryPick"`
	FailOnMissingTarget                  bool             `json:"failOnMissingTarget" yaml:"failOnMissingTarget" toml:"failOnMissingTarget"`             // default: false
	FailOnMissingLocations               bool             `json:"failOnMissingLocations" yaml:"failOnMissingLocations" toml:"failOnMissingLocations"`   // default: false
	ValidateMigrationNaming              bool             `json:"validateMigrationNaming" yaml:"validateMigrationNaming" toml:"validateMigrationNaming"` // default: false
	IgnoreMigrationPatternsExplicitlySet bool             `json:"-" yaml:"-" toml:"-"`
	CleanDisabled           bool             `json:"cleanDisabled" yaml:"cleanDisabled" toml:"cleanDisabled"`                             // default: true
	CleanOnValidationError  bool             `json:"cleanOnValidationError" yaml:"cleanOnValidationError" toml:"cleanOnValidationError"` // default: false
	Mixed                   bool             `json:"mixed" yaml:"mixed" toml:"mixed"`                                                     // default: false
	Group                   bool             `json:"group" yaml:"group" toml:"group"`                                                     // default: false
	Batch                   bool             `json:"batch" yaml:"batch" toml:"batch"`                                                     // default: false
	InstalledBy             string           `json:"installedBy" yaml:"installedBy" toml:"installedBy"`                                 // default: current user
	ConnectRetries          int              `json:"connectRetries" yaml:"connectRetries" toml:"connectRetries"`                         // default: 0
	LockRetryCount          int              `json:"lockRetryCount" yaml:"lockRetryCount" toml:"lockRetryCount"`                         // default: 50

	// Baseline
	BaselineVersion     string `json:"baselineVersion" yaml:"baselineVersion" toml:"baselineVersion"`             // default: 1
	BaselineDescription string `json:"baselineDescription" yaml:"baselineDescription" toml:"baselineDescription"` // default: << Flyway Baseline >>
	BaselineOnMigrate   bool   `json:"baselineOnMigrate" yaml:"baselineOnMigrate" toml:"baselineOnMigrate"`       // default: false

	// Output & Output formatting
	OutputType   string `json:"outputType" yaml:"outputType" toml:"outputType"`       // "text" or "json"
	OutputFile   string `json:"outputFile" yaml:"outputFile" toml:"outputFile"`
	Color        string `json:"color" yaml:"color" toml:"color"`
	DryRunOutput string `json:"dryRunOutput" yaml:"dryRunOutput" toml:"dryRunOutput"` // Output file for dry run SQL (or - for stdout)
	Quiet        bool   `json:"-" yaml:"-" toml:"-"`
	Debug        bool   `json:"-" yaml:"-" toml:"-"`

	// BigQuery Specific Connection Overrides
	GCPProjectID          string `json:"gcpProjectId" yaml:"gcpProjectId" toml:"gcpProjectId"`
	GCPDataset            string `json:"gcpDataset" yaml:"gcpDataset" toml:"gcpDataset"`
	GCPLocation           string `json:"gcpLocation" yaml:"gcpLocation" toml:"gcpLocation"`
	GCPCredentialsFile    string `json:"gcpCredentialsFile" yaml:"gcpCredentialsFile" toml:"gcpCredentialsFile"`
	GCPCredentialsJSON    string `json:"gcpCredentialsJson" yaml:"gcpCredentialsJson" toml:"gcpCredentialsJson"`
	GCPBigQueryEndpoint   string `json:"gcpBigQueryEndpoint" yaml:"gcpBigQueryEndpoint" toml:"gcpBigQueryEndpoint"`
	GCPOAuthType          *int   `json:"gcpOAuthType" yaml:"gcpOAuthType" toml:"gcpOAuthType"`
	GCPAccessToken        string `json:"gcpAccessToken" yaml:"gcpAccessToken" toml:"gcpAccessToken"`

	// Cloud Spanner Specific Connection Overrides
	GCPSpannerInstanceID  string `json:"gcpSpannerInstanceId" yaml:"gcpSpannerInstanceId" toml:"gcpSpannerInstanceId"`
	GCPSpannerDatabaseID  string `json:"gcpSpannerDatabaseId" yaml:"gcpSpannerDatabaseId" toml:"gcpSpannerDatabaseId"`
	GCPSpannerEndpoint    string `json:"gcpSpannerEndpoint" yaml:"gcpSpannerEndpoint" toml:"gcpSpannerEndpoint"`
}

// NewDefaultConfiguration returns standard Flyway defaults.
func NewDefaultConfiguration() *Configuration {
	return &Configuration{
		Table:                        "flyway_schema_history",
		CreateSchemas:                true,
		Locations:                    []string{"filesystem:sql"},
		SQLMigrationPrefix:           "V",
		RepeatableSQLMigrationPrefix: "R",
		UndoSQLMigrationPrefix:       "U",
		BaselineSQLMigrationPrefix:   "B",
		SQLMigrationSeparator:        "__",
		SQLMigrationSuffixes:         []string{".sql", ".sh", ".bash", ".cmd", ".ps1", ".bat", ".py"},
		Encoding:                     "UTF-8",
		PlaceholderReplacement:       true,
		PlaceholderPrefix:            "${",
		PlaceholderSuffix:            "}",
		PlaceholderSeparator:         ":",
		Placeholders:                 make(map[string]string),
		Target:                       "latest",
		OutOfOrder:                   false,
		ValidateOnMigrate:            true,
		IgnoreFutureMigrations:       true,
		IgnoreMissingMigrations:      false,
		IgnorePendingMigrations:      false,
		IgnoreMigrationPatterns:      []string{"*:future"},
		CleanDisabled:                true,
		CleanOnValidationError:       false,
		BaselineVersion:              "1",
		BaselineDescription:          "<< Flyway Baseline >>",
		BaselineOnMigrate:            false,
		OutputType:                   "text",
		LockRetryCount:               50,
	}
}

// Finalize validates and applies derived properties (e.g. schemas, target version, JDBC parsing).
func (c *Configuration) Finalize() error {
	// Parse JDBC / GCP parameters if URL is provided
	if c.URL != "" {
		if IsSpannerURL(c.URL) {
			sParams, err := ParseJDBCSpannerURL(c.URL)
			if err != nil {
				return fmt.Errorf("invalid Cloud Spanner JDBC URL: %w", err)
			}
			if c.GCPProjectID == "" && sParams.ProjectID != "" {
				c.GCPProjectID = sParams.ProjectID
			}
			if c.GCPSpannerInstanceID == "" && sParams.InstanceID != "" {
				c.GCPSpannerInstanceID = sParams.InstanceID
			}
			if c.GCPSpannerDatabaseID == "" && sParams.DatabaseID != "" {
				c.GCPSpannerDatabaseID = sParams.DatabaseID
			}
			if c.GCPSpannerEndpoint == "" && sParams.Endpoint != "" {
				c.GCPSpannerEndpoint = sParams.Endpoint
			}
			if c.GCPCredentialsFile == "" && sParams.CredentialsFile != "" {
				c.GCPCredentialsFile = sParams.CredentialsFile
			}
			if c.Driver == "" {
				c.Driver = "cloudspanner"
			}
		} else {
			params, err := ParseJDBCBigQueryURL(c.URL)
			if err != nil {
				return fmt.Errorf("invalid JDBC URL: %w", err)
			}
			if c.GCPProjectID == "" && params.ProjectID != "" {
				c.GCPProjectID = params.ProjectID
			}
			if c.GCPDataset == "" && params.DefaultDataset != "" {
				c.GCPDataset = params.DefaultDataset
			}
			if c.GCPLocation == "" && params.Location != "" {
				c.GCPLocation = params.Location
			}
			if c.GCPCredentialsFile == "" && params.ServiceAccountFile != "" {
				c.GCPCredentialsFile = params.ServiceAccountFile
			}
			if c.DefaultSchema == "" && params.DefaultDataset != "" {
				c.DefaultSchema = params.DefaultDataset
			}
			if c.GCPOAuthType == nil && params.OAuthTypeSet {
				c.GCPOAuthType = &params.OAuthType
			}
			if c.GCPAccessToken == "" && params.AccessToken != "" {
				c.GCPAccessToken = params.AccessToken
			}
		}
	}

	// Environment variable fallback for GCP credentials & endpoints
	if c.GCPCredentialsFile == "" {
		if envKey := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); envKey != "" {
			c.GCPCredentialsFile = envKey
		}
	}
	if c.GCPBigQueryEndpoint == "" {
		if envEndpoint := os.Getenv("BIGQUERY_EMULATOR_HOST"); envEndpoint != "" {
			c.GCPBigQueryEndpoint = envEndpoint
		}
	}
	if c.GCPSpannerEndpoint == "" {
		if envEndpoint := os.Getenv("SPANNER_EMULATOR_HOST"); envEndpoint != "" {
			c.GCPSpannerEndpoint = envEndpoint
		}
	}
	if c.GCPSpannerInstanceID == "" {
		if envInst := os.Getenv("SPANNER_INSTANCE_ID"); envInst != "" {
			c.GCPSpannerInstanceID = envInst
		}
	}
	if c.GCPSpannerDatabaseID == "" {
		if envDb := os.Getenv("SPANNER_DATABASE_ID"); envDb != "" {
			c.GCPSpannerDatabaseID = envDb
		}
	}
	if c.GCPProjectID == "" {
		if envProj := os.Getenv("GOOGLE_CLOUD_PROJECT"); envProj != "" {
			c.GCPProjectID = envProj
		} else if envProj := os.Getenv("GCP_PROJECT"); envProj != "" {
			c.GCPProjectID = envProj
		}
	}

	// Default schema logic
	if len(c.Schemas) > 0 {
		if c.DefaultSchema == "" {
			c.DefaultSchema = c.Schemas[0]
		}
	} else if c.DefaultSchema != "" {
		c.Schemas = []string{c.DefaultSchema}
	} else if c.GCPDataset != "" {
		c.DefaultSchema = c.GCPDataset
		c.Schemas = []string{c.GCPDataset}
	}

	// Target version parsing
	if c.Target != "" && !strings.EqualFold(c.Target, "latest") && !strings.EqualFold(c.Target, "current") && !strings.EqualFold(c.Target, "next") {
		v, err := version.Parse(c.Target)
		if err != nil {
			return fmt.Errorf("invalid target version '%s': %w", c.Target, err)
		}
		c.TargetVersion = &v
	}

	// Clean locations: if user passed relative paths or filesystem: prefixes, normalize
	if len(c.Locations) == 0 {
		c.Locations = []string{"filesystem:sql"}
	}

	// Map old booleans onto IgnoreMigrationPatterns
	if len(c.IgnoreMigrationPatterns) == 0 {
		c.IgnoreMigrationPatterns = []string{"*:future"}
	}
	if !c.IgnoreFutureMigrations {
		c.IgnoreMigrationPatterns = removeStateFromPatterns(c.IgnoreMigrationPatterns, "future")
	} else if !c.IsIgnored("future", "", nil) && !c.IgnoreMigrationPatternsExplicitlySet {
		c.IgnoreMigrationPatterns = append(c.IgnoreMigrationPatterns, "*:future")
	}
	if c.IgnoreMissingMigrations && !c.IsIgnored("missing", "", nil) {
		c.IgnoreMigrationPatterns = append(c.IgnoreMigrationPatterns, "*:missing")
	}
	if c.IgnorePendingMigrations && !c.IsIgnored("pending", "", nil) {
		c.IgnoreMigrationPatterns = append(c.IgnoreMigrationPatterns, "*:pending", "*:outdated")
	}

	c.OutputType = strings.ToLower(strings.TrimSpace(c.OutputType))
	if c.OutputType == "" {
		c.OutputType = "text"
	} else if c.OutputType != "text" && c.OutputType != "json" {
		return fmt.Errorf("invalid outputType '%s': must be 'text' or 'json'", c.OutputType)
	}

	c.Color = strings.ToLower(strings.TrimSpace(c.Color))
	if c.Color != "" && c.Color != "always" && c.Color != "never" && c.Color != "auto" {
		return fmt.Errorf("invalid color '%s': must be 'always', 'never' or 'auto'", c.Color)
	}

	// Apply workingDirectory to relative locations if set
	if c.WorkingDirectory != "" {
		for i, loc := range c.Locations {
			prefix := ""
			p := loc
			if strings.HasPrefix(loc, "filesystem:") {
				prefix = "filesystem:"
				p = strings.TrimPrefix(loc, "filesystem:")
			}
			if !filepath.IsAbs(p) {
				c.Locations[i] = prefix + filepath.Join(c.WorkingDirectory, p)
			}
		}
	}

	return nil
}

// IsPicked checks whether a migration is selected by the cherryPick configuration.
// If cherryPick is empty, all migrations are considered picked.
// For versioned migrations (v != nil), it matches against version strings.
// For repeatable migrations (v == nil), it matches against the migration description.
func (c *Configuration) IsPicked(v *version.Version, description string) bool {
	if len(c.CherryPick) == 0 {
		return true
	}
	if v != nil {
		vStr := v.String()
		vNorm := v.Normalized()
		for _, cp := range c.CherryPick {
			cpTrim := strings.TrimSpace(cp)
			if cpTrim == vStr || cpTrim == vNorm {
				return true
			}
			if parsed, err := version.Parse(cpTrim); err == nil {
				if parsed.Normalized() == vNorm {
					return true
				}
			}
		}
		return false
	}
	for _, cp := range c.CherryPick {
		cpTrim := strings.TrimSpace(cp)
		if strings.EqualFold(cpTrim, description) {
			return true
		}
	}
	return false
}

// IsIgnored checks whether a given migration state matches IgnoreMigrationPatterns.
// Supported states include: "future", "missing", "pending", "outdated", "ignored".
// Pattern format is [type_or_version:]<state>, e.g.: "*:future", "repeatable:missing", "*:pending".
func (c *Configuration) IsIgnored(state string, migType string, ver *version.Version) bool {
	targetState := strings.ToLower(strings.TrimSpace(state))
	if targetState == "" {
		return false
	}

	for _, rawPattern := range c.IgnoreMigrationPatterns {
		pattern := strings.ToLower(strings.TrimSpace(rawPattern))
		if pattern == "" {
			continue
		}

		rulePattern := "*"
		ruleState := pattern
		if idx := strings.Index(pattern, ":"); idx != -1 {
			rulePattern = strings.TrimSpace(pattern[:idx])
			ruleState = strings.TrimSpace(pattern[idx+1:])
		}

		matchedState := false
		if ruleState == targetState || ruleState == "*" {
			matchedState = true
		} else if ruleState == "pending" && targetState == "outdated" {
			matchedState = true
		}

		if !matchedState {
			continue
		}

		if rulePattern == "*" {
			return true
		}
		if migType != "" && strings.EqualFold(rulePattern, migType) {
			return true
		}
		if ver == nil && strings.EqualFold(rulePattern, "repeatable") {
			return true
		}
		if ver != nil && (rulePattern == strings.ToLower(ver.String()) || rulePattern == strings.ToLower(ver.Normalized())) {
			return true
		}
	}

	return false
}

func removeStateFromPatterns(patterns []string, state string) []string {
	stateLower := strings.ToLower(strings.TrimSpace(state))
	var res []string
	for _, p := range patterns {
		clean := strings.ToLower(strings.TrimSpace(p))
		ruleState := clean
		if idx := strings.Index(clean, ":"); idx != -1 {
			ruleState = strings.TrimSpace(clean[idx+1:])
		}
		if ruleState != stateLower {
			res = append(res, p)
		}
	}
	return res
}

// GetDefaultSchema returns the effective default schema name.
func (c *Configuration) GetDefaultSchema() string {
	if c.DefaultSchema != "" {
		return c.DefaultSchema
	}
	if len(c.Schemas) > 0 {
		return c.Schemas[0]
	}
	return ""
}

// FindConfigFile looks for standard config files in current directory.
func FindConfigFile() string {
	candidates := []string{
		"flyway.conf",
		"noway.conf",
		"flyway.toml",
		"noway.toml",
		"flyway.yaml",
		"flyway.yml",
		"noway.yaml",
		"noway.yml",
		"flyway.json",
		"noway.json",
	}
	for _, f := range candidates {
		if _, err := os.Stat(f); err == nil {
			return f
		}
	}
	// Also check ~/.flyway/flyway.conf
	if home, err := os.UserHomeDir(); err == nil {
		userConf := filepath.Join(home, ".flyway", "flyway.conf")
		if _, err := os.Stat(userConf); err == nil {
			return userConf
		}
	}
	return ""
}
