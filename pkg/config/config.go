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
	cp.ParsedIgnorePatterns = slices.Clone(c.ParsedIgnorePatterns)
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
	PlaceholderReplacement  bool              `json:"placeholderReplacement" yaml:"placeholderReplacement" toml:"placeholderReplacement"` // default: true
	PlaceholderPrefix       string            `json:"placeholderPrefix" yaml:"placeholderPrefix" toml:"placeholderPrefix"`                 // default: ${
	PlaceholderSuffix       string            `json:"placeholderSuffix" yaml:"placeholderSuffix" toml:"placeholderSuffix"`                 // default: }
	PlaceholderSeparator    string            `json:"placeholderSeparator" yaml:"placeholderSeparator" toml:"placeholderSeparator"`       // default: :
	ScriptPlaceholderPrefix string            `json:"scriptPlaceholderPrefix" yaml:"scriptPlaceholderPrefix" toml:"scriptPlaceholderPrefix"` // default: FP__
	ScriptPlaceholderSuffix string            `json:"scriptPlaceholderSuffix" yaml:"scriptPlaceholderSuffix" toml:"scriptPlaceholderSuffix"` // default: __
	Placeholders            map[string]string `json:"placeholders" yaml:"placeholders" toml:"placeholders"`

	// Execution & Validation Control
	Target                  string           `json:"target" yaml:"target" toml:"target"`                                                 // default: latest
	TargetVersion           *version.Version `json:"-" yaml:"-" toml:"-"`
	OutOfOrder              bool             `json:"outOfOrder" yaml:"outOfOrder" toml:"outOfOrder"`                                     // default: false
	ValidateOnMigrate       bool             `json:"validateOnMigrate" yaml:"validateOnMigrate" toml:"validateOnMigrate"`               // default: true
	IgnoreFutureMigrations  bool             `json:"ignoreFutureMigrations" yaml:"ignoreFutureMigrations" toml:"ignoreFutureMigrations"` // default: true
	IgnoreMissingMigrations bool             `json:"ignoreMissingMigrations" yaml:"ignoreMissingMigrations" toml:"ignoreMissingMigrations"` // default: false
	IgnorePendingMigrations bool             `json:"ignorePendingMigrations" yaml:"ignorePendingMigrations" toml:"ignorePendingMigrations"` // default: false
	IgnoreIgnoredMigrations bool             `json:"ignoreIgnoredMigrations" yaml:"ignoreIgnoredMigrations" toml:"ignoreIgnoredMigrations"` // default: false
	IgnoreMigrationPatterns              []string         `json:"ignoreMigrationPatterns" yaml:"ignoreMigrationPatterns" toml:"ignoreMigrationPatterns"` // default: ["*:future"]
	ParsedIgnorePatterns                 []ValidatePattern `json:"-" yaml:"-" toml:"-"`
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
		SQLMigrationSuffixes:         []string{".sql"},
		Encoding:                     "UTF-8",
		PlaceholderReplacement:       true,
		PlaceholderPrefix:            "${",
		PlaceholderSuffix:            "}",
		PlaceholderSeparator:         ":",
		ScriptPlaceholderPrefix:      "FP__",
		ScriptPlaceholderSuffix:      "__",
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
		if c.IgnoreFutureMigrations {
			c.IgnoreMigrationPatterns = []string{"*:future"}
		}
	} else if !c.IgnoreMigrationPatternsExplicitlySet && !c.IgnoreFutureMigrations {
		var filtered []string
		for _, p := range c.IgnoreMigrationPatterns {
			if strings.ToLower(strings.TrimSpace(p)) != "*:future" {
				filtered = append(filtered, p)
			}
		}
		c.IgnoreMigrationPatterns = filtered
	}

	norm := make([]string, len(c.IgnoreMigrationPatterns))
	for i, p := range c.IgnoreMigrationPatterns {
		norm[i] = strings.ToLower(strings.TrimSpace(p))
	}
	if c.IgnoreMissingMigrations && !slices.Contains(norm, "*:missing") {
		c.IgnoreMigrationPatterns = append(c.IgnoreMigrationPatterns, "*:missing")
		norm = append(norm, "*:missing")
	}
	if c.IgnoreIgnoredMigrations && !slices.Contains(norm, "*:ignored") {
		c.IgnoreMigrationPatterns = append(c.IgnoreMigrationPatterns, "*:ignored")
		norm = append(norm, "*:ignored")
	}
	if c.IgnorePendingMigrations && !slices.Contains(norm, "*:pending") {
		c.IgnoreMigrationPatterns = append(c.IgnoreMigrationPatterns, "*:pending")
		norm = append(norm, "*:pending")
	}

	// Parse and validate patterns
	c.ParsedIgnorePatterns = make([]ValidatePattern, 0, len(c.IgnoreMigrationPatterns))
	for _, p := range c.IgnoreMigrationPatterns {
		vp, err := ParseValidatePattern(p)
		if err != nil {
			return err
		}
		c.ParsedIgnorePatterns = append(c.ParsedIgnorePatterns, vp)
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

	if c.ScriptPlaceholderPrefix == "" {
		c.ScriptPlaceholderPrefix = "FP__"
	}
	if c.ScriptPlaceholderSuffix == "" {
		c.ScriptPlaceholderSuffix = "__"
	}

	if c.GCPAccessToken != "" && (c.GCPOAuthType == nil || *c.GCPOAuthType != 2) {
		return fmt.Errorf("OAuthAccessToken is only valid when OAuthType is 2")
	}

	return nil
}

// ResolvedLocations returns Locations with WorkingDirectory applied.
// It does not change c, so it is safe to call any number of times.
func (c *Configuration) ResolvedLocations() []string {
	out := make([]string, len(c.Locations))
	for i, loc := range c.Locations {
		prefix, p := "", loc
		if strings.HasPrefix(loc, "filesystem:") {
			prefix, p = "filesystem:", strings.TrimPrefix(loc, "filesystem:")
		}
		if c.WorkingDirectory != "" && !filepath.IsAbs(p) {
			p = filepath.Join(c.WorkingDirectory, p)
		}
		out[i] = prefix + p
	}
	return out
}

// IsPicked checks whether a migration is selected by the cherryPick configuration.
// If cherryPick is empty, all migrations are considered picked.
// For versioned migrations (v != nil), it matches against version strings.
// For repeatable migrations (v == nil), it matches against the migration description or script name.
func (c *Configuration) IsPicked(v *version.Version, description string, script ...string) bool {
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
		for _, s := range script {
			if strings.EqualFold(cpTrim, s) || strings.EqualFold(cpTrim, filepath.Base(s)) {
				return true
			}
		}
	}
	return false
}

// IsIgnoredIgnored returns true if ignored migrations should be ignored during validation.
// In Flyway, this is true if cherryPick is set, or if ignoreMigrationPatterns ignores "ignored" migrations.
func (c *Configuration) IsIgnoredIgnored() bool {
	return len(c.CherryPick) > 0 || c.MatchesPattern(true, "ignored")
}

// ValidatePattern represents a pattern for ignoring migrations during validation.
type ValidatePattern struct {
	Type  string
	State string
}

var validTypes = map[string]bool{"*": true, "versioned": true, "repeatable": true}
var validStates = map[string]bool{"*": true, "missing": true, "pending": true, "ignored": true, "future": true}

// ParseValidatePattern parses a pattern string of the form <migration_type>:<migration_state>.
func ParseValidatePattern(p string) (ValidatePattern, error) {
	parts := strings.Split(p, ":")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) != 2 {
		return ValidatePattern{}, fmt.Errorf("invalid pattern '%s': pattern must be of the form <migration_type>:<migration_state>", p)
	}
	t := strings.ToLower(strings.TrimSpace(parts[0]))
	s := strings.ToLower(strings.TrimSpace(parts[1]))
	if !validTypes[t] {
		return ValidatePattern{}, fmt.Errorf("invalid migration type '%s': valid types are *, repeatable, versioned", parts[0])
	}
	if !validStates[s] {
		return ValidatePattern{}, fmt.Errorf("invalid migration state '%s': valid states are *, missing, pending, ignored, future", parts[1])
	}
	return ValidatePattern{Type: t, State: s}, nil
}

// Matches returns whether the pattern matches the given migration type and state.
func (v ValidatePattern) Matches(isVersioned bool, state string) bool {
	matchedState := v.State == "*" || v.State == state
	if !matchedState {
		return false
	}
	return v.Type == "*" || (isVersioned && v.Type == "versioned") || (!isVersioned && v.Type == "repeatable")
}

// MatchesPattern checks whether any configured ValidatePattern matches the given migration type and state.
func (c *Configuration) MatchesPattern(isVersioned bool, state string) bool {
	patterns := c.ParsedIgnorePatterns
	if len(patterns) == 0 && len(c.IgnoreMigrationPatterns) > 0 {
		for _, p := range c.IgnoreMigrationPatterns {
			if vp, err := ParseValidatePattern(p); err == nil {
				patterns = append(patterns, vp)
			}
		}
	}
	for _, p := range patterns {
		if p.Matches(isVersioned, state) {
			return true
		}
	}
	return false
}

// IsIgnored checks whether a given migration state matches IgnoreMigrationPatterns.
// Supported states include: "future", "missing", "pending", "outdated", "ignored".
func (c *Configuration) IsIgnored(state string, migType string, ver *version.Version) bool {
	isVersioned := (ver != nil && !ver.IsEmpty()) || strings.EqualFold(migType, "versioned")
	return c.MatchesPattern(isVersioned, strings.ToLower(strings.TrimSpace(state)))
}

// IsPendingIgnored returns true if pending migrations should be ignored.
// In Flyway, this requires either *:pending (or *:*) or both versioned:pending and repeatable:pending.
func (c *Configuration) IsPendingIgnored() bool {
	patterns := c.ParsedIgnorePatterns
	if len(patterns) == 0 && len(c.IgnoreMigrationPatterns) > 0 {
		for _, p := range c.IgnoreMigrationPatterns {
			if vp, err := ParseValidatePattern(p); err == nil {
				patterns = append(patterns, vp)
			}
		}
	}
	hasVersioned := false
	hasRepeatable := false
	for _, p := range patterns {
		if p.State == "*" || p.State == "pending" {
			if p.Type == "*" {
				return true
			}
			if p.Type == "versioned" {
				hasVersioned = true
			}
			if p.Type == "repeatable" {
				hasRepeatable = true
			}
		}
	}
	return hasVersioned && hasRepeatable
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
	return FindConfigFileIn("")
}

// FindConfigFileIn looks for standard config files in the specified directory.
func FindConfigFileIn(dir string) string {
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
		target := f
		if dir != "" {
			target = filepath.Join(dir, f)
		}
		if _, err := os.Stat(target); err == nil {
			return target
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
