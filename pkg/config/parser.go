package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// LoadFromFile loads configuration from a file (.conf, .toml, .yaml, .json).
func LoadFromFile(filePath string) (*Configuration, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error reading config file '%s': %w", filePath, err)
	}

	cfg := NewDefaultConfiguration()
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".conf", ".properties":
		err = parseProperties(string(data), cfg)
	case ".toml":
		err = toml.Unmarshal(data, cfg)
	case ".yaml", ".yml":
		err = yaml.Unmarshal(data, cfg)
	case ".json":
		err = json.Unmarshal(data, cfg)
	default:
		// Default to properties file
		err = parseProperties(string(data), cfg)
	}

	if err != nil {
		return nil, fmt.Errorf("error parsing config file '%s': %w", filePath, err)
	}

	return cfg, nil
}

// LoadFromEnv overrides configuration values from FLYWAY_* and NOWAY_* environment variables.
func LoadFromEnv(cfg *Configuration) {
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := parts[0]
		v := parts[1]

		kUpper := strings.ToUpper(k)
		if strings.HasPrefix(kUpper, "FLYWAY_") {
			key := k[len("FLYWAY_"):]
			applyEnvKey(cfg, key, v)
		} else if strings.HasPrefix(kUpper, "NOWAY_") {
			key := k[len("NOWAY_"):]
			applyEnvKey(cfg, key, v)
		}
	}
}

func applyEnvKey(cfg *Configuration, key, val string) {
	keyUpper := strings.ToUpper(key)
	switch keyUpper {
	case "URL":
		cfg.URL = val
	case "USER":
		cfg.User = val
	case "PASSWORD":
		cfg.Password = val
	case "DRIVER":
		cfg.Driver = val
	case "SCHEMAS":
		cfg.Schemas = splitAndTrim(val, ",")
	case "CREATE_SCHEMAS", "CREATESCHEMAS":
		cfg.CreateSchemas = parseBool(val, true)
	case "DEFAULT_SCHEMA", "DEFAULTSCHEMA":
		cfg.DefaultSchema = val
	case "TABLE":
		cfg.Table = val
	case "LOCATIONS":
		cfg.Locations = splitAndTrim(val, ",")
	case "SQL_MIGRATION_PREFIX", "SQLMIGRATIONPREFIX":
		cfg.SQLMigrationPrefix = val
	case "REPEATABLE_SQL_MIGRATION_PREFIX", "REPEATABLESQLMIGRATIONPREFIX":
		cfg.RepeatableSQLMigrationPrefix = val
	case "SQL_MIGRATION_SEPARATOR", "SQLMIGRATIONSEPARATOR":
		cfg.SQLMigrationSeparator = val
	case "SQL_MIGRATION_SUFFIXES", "SQLMIGRATIONSUFFIXES":
		cfg.SQLMigrationSuffixes = splitAndTrim(val, ",")
	case "ENCODING":
		cfg.Encoding = val
	case "PLACEHOLDER_REPLACEMENT", "PLACEHOLDERREPLACEMENT":
		cfg.PlaceholderReplacement = parseBool(val, true)
	case "PLACEHOLDER_PREFIX", "PLACEHOLDERPREFIX":
		cfg.PlaceholderPrefix = val
	case "PLACEHOLDER_SUFFIX", "PLACEHOLDERSUFFIX":
		cfg.PlaceholderSuffix = val
	case "PLACEHOLDER_SEPARATOR", "PLACEHOLDERSEPARATOR":
		cfg.PlaceholderSeparator = val
	case "TARGET":
		cfg.Target = val
	case "OUT_OF_ORDER", "OUTOFORDER":
		cfg.OutOfOrder = parseBool(val, false)
	case "VALIDATE_ON_MIGRATE", "VALIDATEONMIGRATE":
		cfg.ValidateOnMigrate = parseBool(val, true)
	case "IGNORE_FUTURE_MIGRATIONS", "IGNOREFUTUREMIGRATIONS":
		cfg.IgnoreFutureMigrations = parseBool(val, true)
	case "IGNORE_MISSING_MIGRATIONS", "IGNOREMISSINGMIGRATIONS":
		cfg.IgnoreMissingMigrations = parseBool(val, false)
	case "IGNORE_PENDING_MIGRATIONS", "IGNOREPENDINGMIGRATIONS":
		cfg.IgnorePendingMigrations = parseBool(val, false)
	case "CLEAN_DISABLED", "CLEANDISABLED":
		cfg.CleanDisabled = parseBool(val, true)
	case "CLEAN_ON_VALIDATION_ERROR", "CLEANONVALIDATIONERROR":
		cfg.CleanOnValidationError = parseBool(val, false)
	case "MIXED":
		cfg.Mixed = parseBool(val, false)
	case "GROUP":
		cfg.Group = parseBool(val, false)
	case "BATCH":
		cfg.Batch = parseBool(val, false)
	case "INSTALLED_BY", "INSTALLEDBY":
		cfg.InstalledBy = val
	case "CONNECT_RETRIES", "CONNECTRETRIES":
		if n, err := strconv.Atoi(val); err == nil {
			cfg.ConnectRetries = n
		}
	case "BASELINE_VERSION", "BASELINEVERSION":
		cfg.BaselineVersion = val
	case "BASELINE_DESCRIPTION", "BASELINEDESCRIPTION":
		cfg.BaselineDescription = val
	case "BASELINE_ON_MIGRATE", "BASELINEONMIGRATE":
		cfg.BaselineOnMigrate = parseBool(val, false)
	case "OUTPUT_TYPE", "OUTPUTTYPE":
		cfg.OutputType = val
	case "DRY_RUN_OUTPUT", "DRYRUNOUTPUT":
		cfg.DryRunOutput = val
	case "GCP_PROJECT_ID", "GCPPROJECTID":
		cfg.GCPProjectID = val
	case "GCP_DATASET", "GCPDATASET":
		cfg.GCPDataset = val
	case "GCP_LOCATION", "GCPLOCATION":
		cfg.GCPLocation = val
	case "GCP_CREDENTIALS_FILE", "GCPCREDENTIALSFILE":
		cfg.GCPCredentialsFile = val
	case "GCP_CREDENTIALS_JSON", "GCPCREDENTIALSJSON":
		cfg.GCPCredentialsJSON = val
	case "GCP_BIGQUERY_ENDPOINT", "GCPBIGQUERYENDPOINT":
		cfg.GCPBigQueryEndpoint = val
	case "SPANNER_INSTANCE_ID", "SPANNERINSTANCEID", "GCP_SPANNER_INSTANCE_ID", "GCPSPANNERINSTANCEID":
		cfg.GCPSpannerInstanceID = val
	case "SPANNER_DATABASE_ID", "SPANNERDATABASEID", "GCP_SPANNER_DATABASE_ID", "GCPSPANNERDATABASEID":
		cfg.GCPSpannerDatabaseID = val
	case "SPANNER_ENDPOINT", "SPANNERENDPOINT", "GCP_SPANNER_ENDPOINT", "GCPSPANNERENDPOINT":
		cfg.GCPSpannerEndpoint = val
	default:
		if strings.HasPrefix(keyUpper, "PLACEHOLDERS_") {
			phKey := key[len("PLACEHOLDERS_"):]
			if cfg.Placeholders == nil {
				cfg.Placeholders = make(map[string]string)
			}
			cfg.Placeholders[phKey] = val
		} else if strings.HasPrefix(keyUpper, "PLACEHOLDER_") {
			phKey := key[len("PLACEHOLDER_"):]
			if cfg.Placeholders == nil {
				cfg.Placeholders = make(map[string]string)
			}
			cfg.Placeholders[phKey] = val
		}
	}
}

// parseProperties parses Java-style .properties / flyway.conf files.
func parseProperties(content string, cfg *Configuration) error {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}

		var key, val string
		if idx := strings.IndexAny(line, "=:"); idx != -1 {
			key = strings.TrimSpace(line[:idx])
			val = strings.TrimSpace(line[idx+1:])
		} else {
			continue
		}

		// Strip quotes if present
		if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) ||
			(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
			val = val[1 : len(val)-1]
		}

		// Strip flyway. or noway. prefix case-insensitively
		cleanKey := key
		lowerKey := strings.ToLower(cleanKey)
		if strings.HasPrefix(lowerKey, "flyway.") {
			cleanKey = cleanKey[len("flyway."):]
		} else if strings.HasPrefix(lowerKey, "noway.") {
			cleanKey = cleanKey[len("noway."):]
		}

		applyProperty(cfg, cleanKey, val)
	}

	return scanner.Err()
}

func applyProperty(cfg *Configuration, key, val string) {
	switch strings.ToLower(key) {
	case "url":
		cfg.URL = val
	case "user":
		cfg.User = val
	case "password":
		cfg.Password = val
	case "driver":
		cfg.Driver = val
	case "schemas":
		cfg.Schemas = splitAndTrim(val, ",")
	case "createschemas", "create_schemas":
		cfg.CreateSchemas = parseBool(val, true)
	case "defaultschema":
		cfg.DefaultSchema = val
	case "table":
		cfg.Table = val
	case "locations":
		cfg.Locations = splitAndTrim(val, ",")
	case "sqlmigrationprefix":
		cfg.SQLMigrationPrefix = val
	case "repeatablesqlmigrationprefix":
		cfg.RepeatableSQLMigrationPrefix = val
	case "undosqlmigrationprefix":
		cfg.UndoSQLMigrationPrefix = val
	case "baselinesqlmigrationprefix":
		cfg.BaselineSQLMigrationPrefix = val
	case "sqlmigrationseparator":
		cfg.SQLMigrationSeparator = val
	case "sqlmigrationsuffixes":
		cfg.SQLMigrationSuffixes = splitAndTrim(val, ",")
	case "encoding":
		cfg.Encoding = val
	case "placeholderreplacement":
		cfg.PlaceholderReplacement = parseBool(val, true)
	case "placeholderprefix":
		cfg.PlaceholderPrefix = val
	case "placeholdersuffix":
		cfg.PlaceholderSuffix = val
	case "placeholderseparator":
		cfg.PlaceholderSeparator = val
	case "target":
		cfg.Target = val
	case "outoforder":
		cfg.OutOfOrder = parseBool(val, false)
	case "validateonmigrate":
		cfg.ValidateOnMigrate = parseBool(val, true)
	case "ignorefuturemigrations", "ignore_future_migrations":
		cfg.IgnoreFutureMigrations = parseBool(val, true)
	case "ignoremissingmigrations", "ignore_missing_migrations":
		cfg.IgnoreMissingMigrations = parseBool(val, false)
	case "ignorependingmigrations", "ignore_pending_migrations":
		cfg.IgnorePendingMigrations = parseBool(val, false)
	case "cleandisabled":
		cfg.CleanDisabled = parseBool(val, true)
	case "cleanonvalidationerror":
		cfg.CleanOnValidationError = parseBool(val, false)
	case "mixed":
		cfg.Mixed = parseBool(val, false)
	case "group":
		cfg.Group = parseBool(val, false)
	case "batch":
		cfg.Batch = parseBool(val, false)
	case "installedby":
		cfg.InstalledBy = val
	case "connectretries":
		if n, err := strconv.Atoi(val); err == nil {
			cfg.ConnectRetries = n
		}
	case "baselineversion":
		cfg.BaselineVersion = val
	case "baselinedescription":
		cfg.BaselineDescription = val
	case "baselineonmigrate":
		cfg.BaselineOnMigrate = parseBool(val, false)
	case "outputtype":
		cfg.OutputType = val
	case "dryrunoutput", "dry_run_output":
		cfg.DryRunOutput = val
	case "gcpprojectid":
		cfg.GCPProjectID = val
	case "gcpdataset":
		cfg.GCPDataset = val
	case "gcplocation":
		cfg.GCPLocation = val
	case "gcpcredentialsfile":
		cfg.GCPCredentialsFile = val
	case "gcpcredentialsjson":
		cfg.GCPCredentialsJSON = val
	case "gcpbigqueryendpoint", "bigqueryendpoint":
		cfg.GCPBigQueryEndpoint = val
	case "spannerinstanceid", "spanner_instance_id", "gcpspannerinstanceid", "instanceid", "instance_id":
		cfg.GCPSpannerInstanceID = val
	case "spannerdatabaseid", "spanner_database_id", "gcpspannerdatabaseid", "databaseid", "database_id":
		cfg.GCPSpannerDatabaseID = val
	case "spannerendpoint", "spanner_endpoint", "gcpspannerendpoint":
		cfg.GCPSpannerEndpoint = val
	default:
		keyLower := strings.ToLower(key)
		if strings.HasPrefix(keyLower, "placeholders.") {
			phKey := key[len("placeholders."):]
			if cfg.Placeholders == nil {
				cfg.Placeholders = make(map[string]string)
			}
			cfg.Placeholders[phKey] = val
		} else if strings.HasPrefix(keyLower, "placeholder.") {
			phKey := key[len("placeholder."):]
			if cfg.Placeholders == nil {
				cfg.Placeholders = make(map[string]string)
			}
			cfg.Placeholders[phKey] = val
		}
	}
}

func splitAndTrim(s, sep string) []string {
	raw := strings.Split(s, sep)
	res := make([]string, 0, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

func parseBool(s string, defaultVal bool) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "true" || lower == "yes" || lower == "1" || lower == "on" {
		return true
	}
	if lower == "false" || lower == "no" || lower == "0" || lower == "off" {
		return false
	}
	return defaultVal
}
