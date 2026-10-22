package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// LoadFromFile loads configuration from a single file (.conf, .properties, .toml, .yaml, .json).
func LoadFromFile(filePath string) (*Configuration, error) {
	return LoadFromFiles([]string{filePath})
}

// LoadFromFiles loads and merges configuration from multiple files into a new Configuration.
// All configs are merged into one map first, and the environment is selected once.
func LoadFromFiles(filePaths []string) (*Configuration, error) {
	cfg := NewDefaultConfiguration()
	if err := LoadFromFilesInto(filePaths, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadFromFileInto overlays configuration from a file onto an existing Configuration struct.
func LoadFromFileInto(filePath string, cfg *Configuration) error {
	return LoadFromFilesInto([]string{filePath}, cfg)
}

// LoadFromFilesInto merges all configs from filePaths into one map first,
// selects the environment once (CLI, FLYWAY_ENVIRONMENT, flyway.environment, root environment, default),
// and applies the merged configuration to cfg.
func LoadFromFilesInto(filePaths []string, cfg *Configuration) error {
	mergedMap := make(map[string]any)

	for _, filePath := range filePaths {
		filePath = strings.TrimSpace(filePath)
		if filePath == "" {
			continue
		}
		fileMap, err := readFileToMap(filePath)
		if err != nil {
			return err
		}
		mergeMaps(mergedMap, fileMap)
	}

	targetEnv := determineEnvironment(mergedMap, cfg.Environment)
	cfg.Environment = targetEnv

	return applyMergedConfig(mergedMap, targetEnv, cfg)
}

func readFileToMap(filePath string) (map[string]any, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error reading config file '%s': %w", filePath, err)
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".conf", ".properties":
		return parsePropertiesToMap(string(data))
	case ".toml":
		var rawMap map[string]any
		if err := toml.Unmarshal(data, &rawMap); err != nil {
			return nil, fmt.Errorf("error parsing TOML config file '%s': %w", filePath, err)
		}
		return rawMap, nil
	case ".yaml", ".yml":
		var anyVal any
		if err := yaml.Unmarshal(data, &anyVal); err != nil {
			return nil, fmt.Errorf("error parsing YAML config file '%s': %w", filePath, err)
		}
		return toStringAnyMap(anyVal), nil
	case ".json":
		var rawMap map[string]any
		if err := json.Unmarshal(data, &rawMap); err != nil {
			return nil, fmt.Errorf("error parsing JSON config file '%s': %w", filePath, err)
		}
		return rawMap, nil
	default:
		return parsePropertiesToMap(string(data))
	}
}

func parsePropertiesToMap(content string) (map[string]any, error) {
	result := make(map[string]any)
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "--") {
			continue
		}

		var key, val string
		if idx := strings.IndexAny(line, "=:"); idx != -1 {
			key = strings.TrimSpace(line[:idx])
			val = strings.TrimSpace(line[idx+1:])
		} else {
			continue
		}

		if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) ||
			(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
			val = val[1 : len(val)-1]
		}

		cleanKey := key
		lowerKey := strings.ToLower(cleanKey)
		if strings.HasPrefix(lowerKey, "noway.") {
			cleanKey = "flyway." + cleanKey[len("noway."):]
			lowerKey = strings.ToLower(cleanKey)
		}

		parts := strings.Split(cleanKey, ".")
		phIdx := -1
		for i, part := range parts {
			partLower := strings.ToLower(part)
			if partLower == "placeholders" {
				phIdx = i
				break
			}
			if partLower == "placeholder" {
				if i+1 < len(parts) {
					nextLower := strings.ToLower(parts[i+1])
					if nextLower == "prefix" || nextLower == "suffix" || nextLower == "separator" || nextLower == "replacement" {
						continue
					}
				}
				phIdx = i
				break
			}
		}
		if phIdx != -1 && phIdx < len(parts)-1 {
			phName := strings.Join(parts[phIdx+1:], ".")
			setPlaceholderInMap(result, parts[:phIdx+1], phName, val)
			continue
		}

		setDottedKey(result, cleanKey, val)
	}
	return result, scanner.Err()
}

func setDottedKey(m map[string]any, key string, val any) {
	parts := strings.Split(key, ".")
	curr := m
	for i := 0; i < len(parts)-1; i++ {
		p := parts[i]
		matchedKey := p
		isEnvName := (i == 1 && strings.EqualFold(parts[0], "environments"))
		for ek := range curr {
			match := false
			if isEnvName {
				match = (ek == p)
			} else {
				match = strings.EqualFold(ek, p)
			}
			if match {
				matchedKey = ek
				break
			}
		}
		sub, ok := curr[matchedKey].(map[string]any)
		if !ok {
			sub = make(map[string]any)
			curr[matchedKey] = sub
		}
		curr = sub
	}
	leaf := parts[len(parts)-1]
	matchedLeaf := leaf
	isEnvName := (len(parts) == 2 && strings.EqualFold(parts[0], "environments"))
	for ek := range curr {
		match := false
		if isEnvName {
			match = (ek == leaf)
		} else {
			match = strings.EqualFold(ek, leaf)
		}
		if match {
			matchedLeaf = ek
			break
		}
	}
	curr[matchedLeaf] = val
}

func setPlaceholderInMap(m map[string]any, path []string, phName string, val any) {
	curr := m
	for i, p := range path {
		matchedKey := p
		isEnvName := (i == 1 && len(path) > 1 && strings.EqualFold(path[0], "environments"))
		for ek := range curr {
			match := false
			if isEnvName {
				match = (ek == p)
			} else {
				match = strings.EqualFold(ek, p)
			}
			if match {
				matchedKey = ek
				break
			}
		}
		sub, ok := curr[matchedKey].(map[string]any)
		if !ok {
			sub = make(map[string]any)
			curr[matchedKey] = sub
		}
		curr = sub
	}
	matchedLeaf := phName
	for ek := range curr {
		if strings.EqualFold(ek, phName) {
			matchedLeaf = ek
			break
		}
	}
	curr[matchedLeaf] = val
}

func mergeMaps(dst, src map[string]any) {
	mergeMapsInternal(dst, src, false)
}

func mergeMapsInternal(dst, src map[string]any, isEnvsMap bool) {
	for k, srcVal := range src {
		if srcVal == nil {
			continue
		}
		dstKey := k
		for existingK := range dst {
			match := false
			if isEnvsMap {
				match = (existingK == k)
			} else {
				match = strings.EqualFold(existingK, k)
			}
			if match {
				dstKey = existingK
				break
			}
		}

		if srcMap, ok := srcVal.(map[string]any); ok {
			childIsEnvs := !isEnvsMap && strings.EqualFold(dstKey, "environments")
			if dstMap, ok := dst[dstKey].(map[string]any); ok {
				mergeMapsInternal(dstMap, srcMap, childIsEnvs)
			} else {
				newMap := make(map[string]any, len(srcMap))
				mergeMapsInternal(newMap, srcMap, childIsEnvs)
				dst[dstKey] = newMap
			}
		} else {
			dst[dstKey] = srcVal
		}
	}
}

func toStringAnyMap(v any) map[string]any {
	if v == nil {
		return make(map[string]any)
	}
	switch m := v.(type) {
	case map[string]any:
		res := make(map[string]any, len(m))
		for k, val := range m {
			res[k] = toStringAnyMapVal(val)
		}
		return res
	case map[any]any:
		res := make(map[string]any, len(m))
		for k, val := range m {
			res[fmt.Sprintf("%v", k)] = toStringAnyMapVal(val)
		}
		return res
	default:
		return make(map[string]any)
	}
}

func toStringAnyMapVal(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return toStringAnyMap(val)
	case map[any]any:
		return toStringAnyMap(val)
	case []any:
		res := make([]any, len(val))
		for i, item := range val {
			res[i] = toStringAnyMapVal(item)
		}
		return res
	default:
		return v
	}
}

func findEnvString(m map[string]any) string {
	if m == nil {
		return ""
	}
	for k, v := range m {
		kLower := strings.ToLower(k)
		if kLower == "environment" || kLower == "env" {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// determineEnvironment selects the active environment using the priority:
// 1. CLI (cliEnv)
// 2. FLYWAY_ENVIRONMENT (and NOWAY_ENVIRONMENT)
// 3. flyway.environment (from [flyway] table or flyway.environment property)
// 4. root environment (from root table or property)
// 5. default
func determineEnvironment(mergedMap map[string]any, cliEnv string) string {
	// 1. CLI
	if cliEnv != "" {
		return cliEnv
	}

	// 2. FLYWAY_ENVIRONMENT / NOWAY_ENVIRONMENT
	if envVal := os.Getenv("FLYWAY_ENVIRONMENT"); envVal != "" {
		return envVal
	}
	if envVal := os.Getenv("NOWAY_ENVIRONMENT"); envVal != "" {
		return envVal
	}

	var flywayMap map[string]any
	for k, v := range mergedMap {
		if strings.EqualFold(k, "flyway") {
			if m, ok := v.(map[string]any); ok {
				flywayMap = m
				break
			}
		}
	}

	// 3. flyway.environment
	if flywayMap != nil {
		if envVal := findEnvString(flywayMap); envVal != "" {
			return envVal
		}
	}
	for k, v := range mergedMap {
		kLower := strings.ToLower(k)
		if kLower == "flyway.environment" || kLower == "flyway.env" {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}

	// 4. root environment
	if envVal := findEnvString(mergedMap); envVal != "" {
		return envVal
	}

	// 5. default
	return "default"
}

func applyMergedConfig(mergedMap map[string]any, targetEnv string, cfg *Configuration) error {
	var flywayMap map[string]any
	var envsMap map[string]any

	for k, v := range mergedMap {
		if strings.EqualFold(k, "flyway") {
			if m, ok := v.(map[string]any); ok {
				flywayMap = m
			}
		} else if strings.EqualFold(k, "environments") {
			if m, ok := v.(map[string]any); ok {
				envsMap = m
			}
		}
	}

	// 1. Apply root-level properties (skipping "flyway", "environments", and environment selectors)
	for k, v := range mergedMap {
		kLower := strings.ToLower(k)
		if kLower == "flyway" || kLower == "environments" || kLower == "environment" || kLower == "env" || kLower == "flyway.environment" || kLower == "flyway.env" {
			continue
		}
		if err := applyAnyValue(cfg, k, v); err != nil {
			return err
		}
	}

	// 2. Apply [flyway] table
	if flywayMap != nil {
		for k, v := range flywayMap {
			kLower := strings.ToLower(k)
			if kLower == "environment" || kLower == "env" {
				continue
			}
			if err := applyAnyValue(cfg, k, v); err != nil {
				return err
			}
		}
	}

	// 3. Apply [environments] table
	var envObj map[string]any
	if envsMap != nil {
		for ek, ev := range envsMap {
			if ek == targetEnv {
				if m, ok := ev.(map[string]any); ok {
					envObj = m
					break
				}
			}
		}
	}

	if envObj == nil {
		if targetEnv != "default" {
			return fmt.Errorf("environment '%s' not found in config", targetEnv)
		}
	} else {
		// Apply environment properties (Flyway only warns for unknown keys inside environments)
		for k, v := range envObj {
			if strings.EqualFold(k, "flyway") {
				continue
			}
			if err := applyAnyValue(cfg, k, v); err != nil {
				log.Printf("WARNING: %v", err)
				continue
			}
		}
		// Apply nested [environments.<env>.flyway]
		for k, v := range envObj {
			if strings.EqualFold(k, "flyway") {
				if envFlywayMap, ok := v.(map[string]any); ok {
					for fk, fv := range envFlywayMap {
						if err := applyAnyValue(cfg, fk, fv); err != nil {
							log.Printf("WARNING: %v", err)
							continue
						}
					}
				}
			}
		}
	}

	return nil
}

func applyAnyValue(cfg *Configuration, key string, val any) error {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case string:
		return applyProperty(cfg, key, v)
	case bool:
		return applyProperty(cfg, key, strconv.FormatBool(v))
	case int:
		return applyProperty(cfg, key, strconv.Itoa(v))
	case int64:
		return applyProperty(cfg, key, strconv.FormatInt(v, 10))
	case float64:
		if v == float64(int64(v)) {
			return applyProperty(cfg, key, strconv.FormatInt(int64(v), 10))
		}
		return applyProperty(cfg, key, fmt.Sprintf("%v", v))
	case []any:
		var items []string
		for _, item := range v {
			items = append(items, fmt.Sprintf("%v", item))
		}
		switch strings.ToLower(key) {
		case "locations":
			cfg.Locations = items
			return nil
		case "schemas":
			cfg.Schemas = items
			return nil
		case "sqlmigrationsuffixes", "sql_migration_suffixes":
			cfg.SQLMigrationSuffixes = items
			return nil
		case "ignoremigrationpatterns", "ignore_migration_patterns", "ignore-migration-patterns":
			cfg.IgnoreMigrationPatterns = items
			cfg.IgnoreMigrationPatternsExplicitlySet = true
			return nil
		case "cherrypick", "cherry_pick", "cherry-pick":
			cfg.CherryPick = items
			return nil
		default:
			return applyProperty(cfg, key, strings.Join(items, ","))
		}
	case []string:
		switch strings.ToLower(key) {
		case "locations":
			cfg.Locations = v
			return nil
		case "schemas":
			cfg.Schemas = v
			return nil
		case "sqlmigrationsuffixes", "sql_migration_suffixes":
			cfg.SQLMigrationSuffixes = v
			return nil
		case "ignoremigrationpatterns", "ignore_migration_patterns", "ignore-migration-patterns":
			cfg.IgnoreMigrationPatterns = v
			cfg.IgnoreMigrationPatternsExplicitlySet = true
			return nil
		case "cherrypick", "cherry_pick", "cherry-pick":
			cfg.CherryPick = v
			return nil
		default:
			return applyProperty(cfg, key, strings.Join(v, ","))
		}
	case map[string]any:
		keyLower := strings.ToLower(key)
		if keyLower == "placeholders" {
			if cfg.Placeholders == nil {
				cfg.Placeholders = make(map[string]string)
			}
			for pk, pv := range v {
				cfg.Placeholders[pk] = fmt.Sprintf("%v", pv)
			}
			return nil
		}
		for subK, subV := range v {
			if err := applyAnyValue(cfg, key+"."+subK, subV); err != nil {
				return err
			}
		}
		return nil
	case map[string]string:
		keyLower := strings.ToLower(key)
		if keyLower == "placeholders" {
			if cfg.Placeholders == nil {
				cfg.Placeholders = make(map[string]string)
			}
			for pk, pv := range v {
				cfg.Placeholders[pk] = pv
			}
			return nil
		}
		for subK, subV := range v {
			if err := applyProperty(cfg, key+"."+subK, subV); err != nil {
				return err
			}
		}
		return nil
	default:
		return applyProperty(cfg, key, fmt.Sprintf("%v", v))
	}
}

// LoadFromEnv overrides configuration values from FLYWAY_* and NOWAY_* environment variables.
func LoadFromEnv(cfg *Configuration) error {
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
			if err := applyEnvKey(cfg, key, v); err != nil {
				return err
			}
		} else if strings.HasPrefix(kUpper, "NOWAY_") {
			key := k[len("NOWAY_"):]
			if err := applyEnvKey(cfg, key, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyEnvKey(cfg *Configuration, key, val string) error {
	keyUpper := strings.ToUpper(key)
	switch keyUpper {
	case "ENVIRONMENT", "ENV":
		cfg.Environment = val
	case "URL":
		cfg.URL = val
	case "USER", "USERNAME":
		cfg.User = val
	case "PASSWORD":
		cfg.Password = val
	case "DRIVER":
		cfg.Driver = val
	case "LOCK_RETRY_COUNT", "LOCKRETRYCOUNT":
		if n, err := strconv.Atoi(val); err == nil {
			cfg.LockRetryCount = n
		}
	case "SCHEMAS":
		cfg.Schemas = splitAndTrim(val, ",")
	case "CREATE_SCHEMAS", "CREATESCHEMAS":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.CreateSchemas = b
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
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.PlaceholderReplacement = b
	case "PLACEHOLDER_PREFIX", "PLACEHOLDERPREFIX":
		cfg.PlaceholderPrefix = val
	case "PLACEHOLDER_SUFFIX", "PLACEHOLDERSUFFIX":
		cfg.PlaceholderSuffix = val
	case "PLACEHOLDER_SEPARATOR", "PLACEHOLDERSEPARATOR":
		cfg.PlaceholderSeparator = val
	case "TARGET":
		cfg.Target = val
	case "OUT_OF_ORDER", "OUTOFORDER":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.OutOfOrder = b
	case "VALIDATE_ON_MIGRATE", "VALIDATEONMIGRATE":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.ValidateOnMigrate = b
	case "IGNORE_FUTURE_MIGRATIONS", "IGNOREFUTUREMIGRATIONS":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.IgnoreFutureMigrations = b
	case "IGNORE_MISSING_MIGRATIONS", "IGNOREMISSINGMIGRATIONS":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.IgnoreMissingMigrations = b
	case "IGNORE_PENDING_MIGRATIONS", "IGNOREPENDINGMIGRATIONS":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.IgnorePendingMigrations = b
	case "IGNORE_MIGRATION_PATTERNS", "IGNOREMIGRATIONPATTERNS":
		cfg.IgnoreMigrationPatterns = splitAndTrim(val, ",")
		cfg.IgnoreMigrationPatternsExplicitlySet = true
	case "SKIP_EXECUTING_MIGRATIONS", "SKIPEXECUTINGMIGRATIONS":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.SkipExecutingMigrations = b
	case "CHERRY_PICK", "CHERRYPICK":
		cfg.CherryPick = splitAndTrim(val, ",")
	case "FAIL_ON_MISSING_TARGET", "FAILONMISSINGTARGET":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.FailOnMissingTarget = b
	case "FAIL_ON_MISSING_LOCATIONS", "FAILONMISSINGLOCATIONS":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.FailOnMissingLocations = b
	case "VALIDATE_MIGRATION_NAMING", "VALIDATEMIGRATIONNAMING":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.ValidateMigrationNaming = b
	case "CLEAN_DISABLED", "CLEANDISABLED":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.CleanDisabled = b
	case "CLEAN_ON_VALIDATION_ERROR", "CLEANONVALIDATIONERROR":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.CleanOnValidationError = b
	case "MIXED":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.Mixed = b
	case "GROUP":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.Group = b
	case "BATCH":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.Batch = b
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
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.BaselineOnMigrate = b
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
	case "OAUTH_TYPE", "OAUTHTYPE", "GCP_OAUTH_TYPE", "GCPOAUTHTYPE":
		if num, err := strconv.Atoi(val); err == nil {
			cfg.GCPOAuthType = &num
		}
	case "OAUTH_ACCESS_TOKEN", "OAUTHACCESSTOKEN", "GCP_ACCESS_TOKEN", "GCPACCESSTOKEN":
		cfg.GCPAccessToken = val
	case "OAUTH_PVT_KEY_PATH", "OAUTHPVTKEYPATH":
		cfg.GCPCredentialsFile = val
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
	return nil
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

		if err := applyProperty(cfg, cleanKey, val); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func applyProperty(cfg *Configuration, key, val string) error {
	cleanKey := key
	lowerKey := strings.ToLower(cleanKey)
	if strings.HasPrefix(lowerKey, "flyway.") {
		cleanKey = cleanKey[len("flyway."):]
	} else if strings.HasPrefix(lowerKey, "noway.") {
		cleanKey = cleanKey[len("noway."):]
	}
	cleanKeyLower := strings.ToLower(cleanKey)

	switch cleanKeyLower {
	case "environment", "env":
		cfg.Environment = val
	case "url":
		cfg.URL = val
	case "user", "username":
		cfg.User = val
	case "password":
		cfg.Password = val
	case "driver":
		cfg.Driver = val
	case "lockretrycount", "lock_retry_count", "lock-retry-count":
		if n, err := strconv.Atoi(val); err == nil {
			cfg.LockRetryCount = n
		}
	case "schemas":
		cfg.Schemas = splitAndTrim(val, ",")
	case "createschemas", "create_schemas", "create-schemas":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.CreateSchemas = b
	case "defaultschema", "default_schema", "default-schema":
		cfg.DefaultSchema = val
	case "table":
		cfg.Table = val
	case "locations":
		cfg.Locations = splitAndTrim(val, ",")
	case "sqlmigrationprefix", "sql_migration_prefix", "sql-migration-prefix":
		cfg.SQLMigrationPrefix = val
	case "repeatablesqlmigrationprefix", "repeatable_sql_migration_prefix", "repeatable-sql-migration-prefix":
		cfg.RepeatableSQLMigrationPrefix = val
	case "undosqlmigrationprefix", "undo_sql_migration_prefix", "undo-sql-migration-prefix":
		cfg.UndoSQLMigrationPrefix = val
	case "baselinesqlmigrationprefix", "baseline_sql_migration_prefix", "baseline-sql-migration-prefix":
		cfg.BaselineSQLMigrationPrefix = val
	case "sqlmigrationseparator", "sql_migration_separator", "sql-migration-separator":
		cfg.SQLMigrationSeparator = val
	case "sqlmigrationsuffixes", "sql_migration_suffixes", "sql-migration-suffixes":
		cfg.SQLMigrationSuffixes = splitAndTrim(val, ",")
	case "encoding":
		cfg.Encoding = val
	case "placeholderreplacement", "placeholder.replacement", "placeholder_replacement", "placeholder-replacement":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.PlaceholderReplacement = b
	case "placeholderprefix", "placeholder.prefix", "placeholder_prefix", "placeholder-prefix":
		cfg.PlaceholderPrefix = val
	case "placeholdersuffix", "placeholder.suffix", "placeholder_suffix", "placeholder-suffix":
		cfg.PlaceholderSuffix = val
	case "placeholderseparator", "placeholder.separator", "placeholder_separator", "placeholder-separator":
		cfg.PlaceholderSeparator = val
	case "target":
		cfg.Target = val
	case "outoforder", "out_of_order", "out-of-order":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.OutOfOrder = b
	case "validateonmigrate", "validate_on_migrate", "validate-on-migrate":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.ValidateOnMigrate = b
	case "ignorefuturemigrations", "ignore_future_migrations", "ignore-future-migrations":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.IgnoreFutureMigrations = b
	case "ignoremissingmigrations", "ignore_missing_migrations", "ignore-missing-migrations":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.IgnoreMissingMigrations = b
	case "ignorependingmigrations", "ignore_pending_migrations", "ignore-pending-migrations":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.IgnorePendingMigrations = b
	case "ignoremigrationpatterns", "ignore_migration_patterns", "ignore-migration-patterns":
		cfg.IgnoreMigrationPatterns = splitAndTrim(val, ",")
		cfg.IgnoreMigrationPatternsExplicitlySet = true
	case "skipexecutingmigrations", "skip_executing_migrations", "skip-executing-migrations":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.SkipExecutingMigrations = b
	case "cherrypick", "cherry_pick", "cherry-pick":
		cfg.CherryPick = splitAndTrim(val, ",")
	case "failonmissingtarget", "fail_on_missing_target", "fail-on-missing-target":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.FailOnMissingTarget = b
	case "failonmissinglocations", "fail_on_missing_locations", "fail-on-missing-locations":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.FailOnMissingLocations = b
	case "validatemigrationnaming", "validate_migration_naming", "validate-migration-naming":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.ValidateMigrationNaming = b
	case "cleandisabled", "clean_disabled", "clean-disabled":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.CleanDisabled = b
	case "cleanonvalidationerror", "clean_on_validation_error", "clean-on-validation-error":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.CleanOnValidationError = b
	case "mixed":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.Mixed = b
	case "group":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.Group = b
	case "batch":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.Batch = b
	case "installedby", "installed_by", "installed-by":
		cfg.InstalledBy = val
	case "connectretries", "connect_retries", "connect-retries":
		if n, err := strconv.Atoi(val); err == nil {
			cfg.ConnectRetries = n
		}
	case "baselineversion", "baseline_version", "baseline-version":
		cfg.BaselineVersion = val
	case "baselinedescription", "baseline_description", "baseline-description":
		cfg.BaselineDescription = val
	case "baselineonmigrate", "baseline_on_migrate", "baseline-on-migrate":
		b, err := ParseBool(val)
		if err != nil {
			return err
		}
		cfg.BaselineOnMigrate = b
	case "outputtype", "output_type", "output-type", "output":
		cfg.OutputType = val
	case "dryrunoutput", "dry_run_output", "dry-run-output":
		cfg.DryRunOutput = val
	case "gcpprojectid", "project", "projectid", "project_id", "project-id":
		cfg.GCPProjectID = val
	case "gcpdataset", "dataset":
		cfg.GCPDataset = val
	case "gcplocation", "location":
		cfg.GCPLocation = val
	case "gcpcredentialsfile", "keyfile", "credentials_file", "credentials-file":
		cfg.GCPCredentialsFile = val
	case "gcpcredentialsjson", "credentials_json", "credentials-json":
		cfg.GCPCredentialsJSON = val
	case "gcpbigqueryendpoint", "bigqueryendpoint", "bigquery_endpoint", "bigquery-endpoint":
		cfg.GCPBigQueryEndpoint = val
	case "oauthtype", "oauth_type", "oauth-type", "gcpoauthtype":
		if num, err := strconv.Atoi(val); err == nil {
			cfg.GCPOAuthType = &num
		}
	case "oauthaccesstoken", "oauth_access_token", "oauth-access-token", "gcpaccesstoken":
		cfg.GCPAccessToken = val
	case "oauthpvtkeypath", "oauth_pvt_key_path", "oauth-pvt-key-path":
		cfg.GCPCredentialsFile = val
	case "spannerinstanceid", "spanner_instance_id", "spanner-instance-id", "gcpspannerinstanceid", "instanceid", "instance_id":
		cfg.GCPSpannerInstanceID = val
	case "spannerdatabaseid", "spanner_database_id", "spanner-database-id", "gcpspannerdatabaseid", "databaseid", "database_id":
		cfg.GCPSpannerDatabaseID = val
	case "spannerendpoint", "spanner_endpoint", "spanner-endpoint", "gcpspannerendpoint":
		cfg.GCPSpannerEndpoint = val
	case "configfile", "configfiles", "config-file", "config-files":
		// skip
	default:
		if strings.HasPrefix(cleanKeyLower, "placeholders.") {
			phKey := cleanKey[len("placeholders."):]
			if cfg.Placeholders == nil {
				cfg.Placeholders = make(map[string]string)
			}
			cfg.Placeholders[phKey] = val
			return nil
		} else if strings.HasPrefix(cleanKeyLower, "placeholder.") {
			phKey := cleanKey[len("placeholder."):]
			if cfg.Placeholders == nil {
				cfg.Placeholders = make(map[string]string)
			}
			cfg.Placeholders[phKey] = val
			return nil
		}
		if strings.HasPrefix(cleanKeyLower, "jdbcproperties.") || strings.HasPrefix(cleanKeyLower, "plugins.") || strings.HasPrefix(cleanKeyLower, "configurations.") {
			log.Printf("WARNING: %s is not supported by noway and is ignored", key)
			return nil
		}
		normKey := strings.ReplaceAll(strings.ReplaceAll(cleanKeyLower, "-", ""), "_", "")
		if unsupportedFlywayKeys[normKey] {
			log.Printf("WARNING: %s is not supported by noway and is ignored", key)
			return nil
		}
		return UnknownPropertyError(cleanKey)
	}
	return nil
}

// unsupportedFlywayKeys lists known Flyway keys that noway does not currently support.
// When present in configuration, noway logs a warning and ignores them rather than failing.
var unsupportedFlywayKeys = map[string]bool{
	"callbacks":                 true,
	"callbacklocations":         true,
	"skipdefaultcallbacks":      true,
	"resolvers":                 true,
	"skipdefaultresolvers":      true,
	"erroroverrides":            true,
	"outputqueryresults":        true,
	"outputlogsinjson":          true,
	"outputprogress":            true,
	"kerberosconfigfile":        true,
	"executeintransaction":      true,
	"detectencoding":            true,
	"configfileencoding":        true,
	"reportenabled":             true,
	"reportfilename":            true,
	"communitydbsupportenabled": true,
	"connectretriesinterval":    true,
	"initsql":                   true,
	"jardirs":                   true,
	"licensekey":                true,
	"loggers":                   true,
	"powershellexecutable":      true,
	"stream":                    true,
	"tablespace":                true,
	"workingdirectory":          true,
	"ignoreignoredmigrations":   true,
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

// ParseBool parses a string into a boolean. It strictly accepts "true" and "false" (case-insensitive).
// Any other value returns an error.
func ParseBool(s string) (bool, error) {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "true" {
		return true, nil
	}
	if lower == "false" {
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean value %q: must be 'true' or 'false'", s)
}

// KnownProperties lists canonical Flyway and Noway configuration property names.
var KnownProperties = []string{
	"url",
	"user",
	"password",
	"driver",
	"schemas",
	"defaultSchema",
	"table",
	"locations",
	"target",
	"outOfOrder",
	"validateOnMigrate",
	"cleanDisabled",
	"cleanOnValidationError",
	"ignoreFutureMigrations",
	"ignoreMissingMigrations",
	"ignoreIgnoredMigrations",
	"ignorePendingMigrations",
	"ignoreMigrationPatterns",
	"skipExecutingMigrations",
	"cherryPick",
	"failOnMissingTarget",
	"failOnMissingLocations",
	"validateMigrationNaming",
	"baselineVersion",
	"baselineDescription",
	"baselineOnMigrate",
	"installedBy",
	"encoding",
	"placeholderReplacement",
	"placeholderPrefix",
	"placeholderSuffix",
	"placeholderSeparator",
	"scriptPlaceholderPrefix",
	"scriptPlaceholderSuffix",
	"sqlMigrationPrefix",
	"repeatableSqlMigrationPrefix",
	"undoSqlMigrationPrefix",
	"baselineSqlMigrationPrefix",
	"sqlMigrationSeparator",
	"sqlMigrationSuffixes",
	"mixed",
	"group",
	"batch",
	"connectRetries",
	"connectRetriesInterval",
	"lockRetryCount",
	"createSchemas",
	"environment",
	"configFile",
	"configFiles",
	"configFileEncoding",
	"outputType",
	"dryRunOutput",
	"callbacks",
	"callbackLocations",
	"skipDefaultCallbacks",
	"resolvers",
	"skipDefaultResolvers",
	"errorOverrides",
	"outputQueryResults",
	"outputLogsInJson",
	"outputProgress",
	"kerberosConfigFile",
	"executeInTransaction",
	"detectEncoding",
	"reportEnabled",
	"reportFilename",
	"communityDBSupportEnabled",
	"initSql",
	"jarDirs",
	"licenseKey",
	"loggers",
	"powershellExecutable",
	"stream",
	"tablespace",
	"workingDirectory",
	"oauthType",
	"oauthAccessToken",
	"oauthPvtKeyPath",
	"gcpOAuthType",
	"gcpAccessToken",
	"gcpProjectId",
	"gcpDataset",
	"gcpLocation",
	"gcpCredentialsFile",
	"gcpCredentialsJson",
	"gcpBigQueryEndpoint",
	"spannerInstanceId",
	"spannerDatabaseId",
	"spannerEndpoint",
}

// LevenshteinDistance computes the Levenshtein edit distance between two strings (case-insensitive).
func LevenshteinDistance(s1, s2 string) int {
	s1 = strings.ToLower(s1)
	s2 = strings.ToLower(s2)
	r1 := []rune(s1)
	r2 := []rune(s2)
	n := len(r1)
	m := len(r2)

	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := 0; j <= m; j++ {
		d[0][j] = j
	}

	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if r1[i-1] == r2[j-1] {
				cost = 0
			}
			d[i][j] = min(
				d[i-1][j]+1,      // deletion
				d[i][j-1]+1,      // insertion
				d[i-1][j-1]+cost, // substitution
			)
		}
	}
	return d[n][m]
}

// SuggestNearest finds the closest known property name to key if within edit distance threshold.
func SuggestNearest(key string) string {
	clean := strings.TrimPrefix(key, "flyway.")
	clean = strings.TrimPrefix(clean, "noway.")
	cleanNorm := strings.ReplaceAll(clean, "-", "")
	cleanNorm = strings.ReplaceAll(cleanNorm, "_", "")

	bestMatch := ""
	bestDist := 999
	for _, prop := range KnownProperties {
		propNorm := strings.ReplaceAll(prop, "-", "")
		propNorm = strings.ReplaceAll(propNorm, "_", "")
		dist := LevenshteinDistance(cleanNorm, propNorm)
		if dist < bestDist {
			bestDist = dist
			bestMatch = prop
		}
	}

	maxDist := 3
	if len(clean) < 4 {
		maxDist = 1
	} else if len(clean) < 7 {
		maxDist = 2
	}
	if bestDist <= maxDist {
		return bestMatch
	}
	return ""
}

// UnknownPropertyError formats an unknown configuration property error, including a nearest suggestion if available.
func UnknownPropertyError(key string) error {
	clean := strings.TrimPrefix(key, "flyway.")
	clean = strings.TrimPrefix(clean, "noway.")
	suggestion := SuggestNearest(clean)
	if suggestion != "" {
		return fmt.Errorf("Unknown configuration property: %s. Did you mean '%s'?", clean, suggestion)
	}
	return fmt.Errorf("Unknown configuration property: %s", clean)
}
