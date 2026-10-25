package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/earthboundkid/versioninfo/v2"
	"github.com/roryq/noway"
	"github.com/roryq/noway/pkg/config"
)

var version string

func init() {
	if version != "" && versioninfo.Version == "unknown" {
		versioninfo.Version = version
	}
}

func printBanner() {
	ver := noway.AppVersion()
	if !strings.HasPrefix(ver, "v") && ver != "devel" && ver != "unknown" {
		ver = "v" + ver
	}
	fmt.Printf("Noway %s by RoryQ - Drop-in Flyway replacement for BigQuery & Cloud Spanner\n", ver)
}

func isJSONRequested(args []string) bool {
	for _, arg := range args {
		clean := strings.ToLower(strings.TrimLeft(arg, "-"))
		if clean == "outputtype=json" || clean == "output=json" {
			return true
		}
	}
	return false
}

var validCommands = map[string]bool{
	"migrate":  true,
	"info":     true,
	"validate": true,
	"baseline": true,
	"repair":   true,
	"clean":    true,
	"undo":     true,
	"version":  true,
	"help":     true,
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	var cmd string
	var options []string

	// First pass: identify any known command name
	for _, arg := range os.Args[1:] {
		if cmd == "" && validCommands[strings.ToLower(arg)] {
			cmd = strings.ToLower(arg)
		} else {
			options = append(options, arg)
		}
	}

	if cmd == "" {
		for _, arg := range options {
			clean := strings.ToLower(strings.TrimLeft(arg, "-"))
			if clean == "version" || clean == "v" {
				cmd = "version"
				break
			}
			if clean == "help" || clean == "h" || clean == "?" {
				cmd = "help"
				break
			}
		}
	}

	if cmd == "" {
		newOptions := make([]string, 0, len(options))
		for _, arg := range options {
			if cmd == "" && !strings.HasPrefix(arg, "-") {
				cmd = arg
			} else {
				newOptions = append(newOptions, arg)
			}
		}
		options = newOptions
	}

	if cmd == "" {
		printHelp()
		os.Exit(1)
	}

	if cmd == "version" || cmd == "-version" || cmd == "--version" || cmd == "-v" {
		if isJSONRequested(options) {
			out := struct {
				Command        string   `json:"command"`
				Edition        string   `json:"edition"`
				PluginVersions []string `json:"pluginVersions"`
				ProductVersion string   `json:"productVersion"`
			}{
				Command:        "version",
				Edition:        "COMMUNITY",
				PluginVersions: []string{},
				ProductVersion: strings.TrimPrefix(noway.AppVersion(), "v"),
			}
			data, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(data))
			return
		}
		printBanner()
		return
	}

	if cmd == "help" || cmd == "-help" || cmd == "--help" || cmd == "-h" || cmd == "-?" {
		printHelp()
		return
	}

	// Parse command-line flags and properties
	cfg, err := parseCLIArgs(options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputFile != "" {
		f, err := os.Create(cfg.OutputFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to open output file '%s': %v\n", cfg.OutputFile, err)
			os.Exit(1)
		}
		defer f.Close()
		os.Stdout = f
	}

	if cfg.Quiet {
		log.SetOutput(io.Discard)
	}

	if err := cfg.Finalize(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if !cfg.Quiet && cfg.OutputType != "json" {
		printBanner()
	}

	nw, err := noway.New(noway.WithConfig(cfg))
	if err != nil {
		if cfg.OutputType == "json" {
			outputJSONError(err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	defer nw.Close()

	ctx := context.Background()

	switch strings.ToLower(cmd) {
	case "migrate":
		runMigrate(ctx, nw, cfg)
	case "info":
		runInfo(ctx, nw, cfg)
	case "validate":
		runValidate(ctx, nw, cfg)
	case "baseline":
		runBaseline(ctx, nw, cfg)
	case "repair":
		runRepair(ctx, nw, cfg)
	case "clean":
		runClean(ctx, nw, cfg)
	case "undo":
		runUndo(ctx, nw, cfg)
	default:
		fmt.Fprintf(os.Stderr, "ERROR: Unknown command '%s'. Valid commands: migrate, info, validate, baseline, repair, clean, undo, version\n", cmd)
		os.Exit(1)
	}
}

func parseCLIArgs(args []string) (*config.Configuration, error) {
	cfg := config.NewDefaultConfiguration()

	// 1. Check for workingDirectory, configFile/configFiles and environment in args first
	var configFiles []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		clean := strings.TrimLeft(arg, "-")
		if idx := strings.Index(clean, "="); idx != -1 {
			k := strings.ToLower(clean[:idx])
			v := clean[idx+1:]
			if k == "configfile" || k == "configfiles" || k == "config-file" || k == "config-files" {
				configFiles = append(configFiles, splitAndTrimCLI(v, ",")...)
			} else if k == "environment" || k == "env" {
				cfg.Environment = v
			} else if k == "workingdirectory" || k == "working-directory" || k == "working_directory" {
				cfg.WorkingDirectory = v
			}
		} else if cleanLower := strings.ToLower(clean); cleanLower == "configfile" || cleanLower == "configfiles" || cleanLower == "config-file" || cleanLower == "config-files" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				configFiles = append(configFiles, splitAndTrimCLI(args[i+1], ",")...)
				i++
			}
		} else if strings.ToLower(clean) == "environment" || strings.ToLower(clean) == "env" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				cfg.Environment = args[i+1]
				i++
			}
		} else if cleanLower == "workingdirectory" || cleanLower == "working-directory" || cleanLower == "working_directory" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				cfg.WorkingDirectory = args[i+1]
				i++
			}
		}
	}

	if cfg.WorkingDirectory == "" {
		if envWD := os.Getenv("FLYWAY_WORKING_DIRECTORY"); envWD != "" {
			cfg.WorkingDirectory = envWD
		} else if envWD := os.Getenv("NOWAY_WORKING_DIRECTORY"); envWD != "" {
			cfg.WorkingDirectory = envWD
		}
	}

	if len(configFiles) == 0 {
		if envConfigFiles := os.Getenv("FLYWAY_CONFIG_FILES"); envConfigFiles != "" {
			configFiles = append(configFiles, splitAndTrimCLI(envConfigFiles, ",")...)
		} else if envConfigFiles := os.Getenv("NOWAY_CONFIG_FILES"); envConfigFiles != "" {
			configFiles = append(configFiles, splitAndTrimCLI(envConfigFiles, ",")...)
		}
	}

	if len(configFiles) > 0 {
		resolvedConfigFiles := make([]string, len(configFiles))
		for idx, cf := range configFiles {
			if cfg.WorkingDirectory != "" && !filepath.IsAbs(cf) {
				resolvedConfigFiles[idx] = filepath.Join(cfg.WorkingDirectory, cf)
			} else {
				resolvedConfigFiles[idx] = cf
			}
		}
		if err := config.LoadFromFilesInto(resolvedConfigFiles, cfg); err != nil {
			return nil, err
		}
	} else if autoConf := config.FindConfigFileIn(cfg.WorkingDirectory); autoConf != "" {
		if err := config.LoadFromFilesInto([]string{autoConf}, cfg); err != nil {
			return nil, err
		}
	} else {
		if err := config.LoadFromFilesInto(nil, cfg); err != nil {
			return nil, err
		}
	}

	// 2. Load from Environment
	if err := config.LoadFromEnv(cfg); err != nil {
		return nil, err
	}

	// 3. Parse individual CLI arguments (supports -flag=val, --flag=val, -flag val, and -placeholders.key=val)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			continue
		}

		cleanArg := strings.TrimLeft(arg, "-")

		// Handle flags with no value before checking for values
		switch strings.ToLower(cleanArg) {
		case "x":
			cfg.Debug = true
			continue
		case "q":
			cfg.Quiet = true
			continue
		case "skipcheckforupdate", "communityfallback":
			continue
		}

		var key, val string

		if idx := strings.Index(cleanArg, "="); idx != -1 {
			key = cleanArg[:idx]
			val = cleanArg[idx+1:]
		} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			key = cleanArg
			val = args[i+1]
			i++
		} else {
			key = cleanArg
			val = "true"
		}

		// Strip flyway. or noway. prefix if user wrote -flyway.url=...
		key = strings.TrimPrefix(key, "flyway.")
		key = strings.TrimPrefix(key, "noway.")

		switch strings.ToLower(key) {
		case "url":
			cfg.URL = val
		case "user":
			cfg.User = val
		case "password":
			cfg.Password = val
		case "schemas":
			cfg.Schemas = splitAndTrimCLI(val, ",")
		case "createschemas", "create-schemas", "create_schemas":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.CreateSchemas = b
		case "defaultschema", "default-schema", "default_schema":
			cfg.DefaultSchema = val
		case "table":
			cfg.Table = val
		case "locations":
			cfg.Locations = splitAndTrimCLI(val, ",")
		case "target":
			cfg.Target = val
		case "outoforder", "out-of-order", "out_of_order":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.OutOfOrder = b
		case "validateonmigrate", "validate-on-migrate", "validate_on_migrate":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.ValidateOnMigrate = b
		case "ignorefuturemigrations", "ignore-future-migrations", "ignore_future_migrations":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.IgnoreFutureMigrations = b
		case "ignoremissingmigrations", "ignore-missing-migrations", "ignore_missing_migrations":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.IgnoreMissingMigrations = b
		case "ignorependingmigrations", "ignore-pending-migrations", "ignore_pending_migrations":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.IgnorePendingMigrations = b
		case "ignoreignoredmigrations", "ignore-ignored-migrations", "ignore_ignored_migrations":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.IgnoreIgnoredMigrations = b
		case "ignoremigrationpatterns", "ignore-migration-patterns", "ignore_migration_patterns":
			cfg.IgnoreMigrationPatterns = splitAndTrimCLI(val, ",")
			cfg.IgnoreMigrationPatternsExplicitlySet = true
		case "skipexecutingmigrations", "skip-executing-migrations", "skip_executing_migrations":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.SkipExecutingMigrations = b
		case "cherrypick", "cherry-pick", "cherry_pick":
			cfg.CherryPick = splitAndTrimCLI(val, ",")
		case "failonmissingtarget", "fail-on-missing-target", "fail_on_missing_target":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.FailOnMissingTarget = b
		case "cleandisabled", "clean-disabled", "clean_disabled":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.CleanDisabled = b
		case "cleanonvalidationerror", "clean-on-validation-error", "clean_on_validation_error":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.CleanOnValidationError = b
		case "mixed":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.Mixed = b
		case "group":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.Group = b
		case "batch":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.Batch = b
		case "baselineversion", "baseline-version", "baseline_version":
			cfg.BaselineVersion = val
		case "baselinedescription", "baseline-description", "baseline_description":
			cfg.BaselineDescription = val
		case "baselineonmigrate", "baseline-on-migrate", "baseline_on_migrate":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.BaselineOnMigrate = b
		case "placeholderreplacement", "placeholder-replacement", "placeholder_replacement":
			b, err := parseBoolVal(val)
			if err != nil {
				return nil, err
			}
			cfg.PlaceholderReplacement = b
		case "placeholderprefix", "placeholder-prefix", "placeholder_prefix":
			cfg.PlaceholderPrefix = val
		case "placeholdersuffix", "placeholder-suffix", "placeholder_suffix":
			cfg.PlaceholderSuffix = val
		case "placeholderseparator", "placeholder-separator", "placeholder_separator":
			cfg.PlaceholderSeparator = val
		case "scriptplaceholderprefix", "script-placeholder-prefix", "script_placeholder_prefix":
			cfg.ScriptPlaceholderPrefix = val
		case "scriptplaceholdersuffix", "script-placeholder-suffix", "script_placeholder_suffix":
			cfg.ScriptPlaceholderSuffix = val
		case "sqlmigrationprefix", "sql-migration-prefix", "sql_migration_prefix":
			cfg.SQLMigrationPrefix = val
		case "repeatablesqlmigrationprefix", "repeatable-sql-migration-prefix", "repeatable_sql_migration_prefix":
			cfg.RepeatableSQLMigrationPrefix = val
		case "undosqlmigrationprefix", "undo-sql-migration-prefix", "undo_sql_migration_prefix":
			cfg.UndoSQLMigrationPrefix = val
		case "baselinesqlmigrationprefix", "baseline-sql-migration-prefix", "baseline_sql_migration_prefix":
			cfg.BaselineSQLMigrationPrefix = val
		case "sqlmigrationseparator", "sql-migration-separator", "sql_migration_separator":
			cfg.SQLMigrationSeparator = val
		case "sqlmigrationsuffixes", "sql-migration-suffixes", "sql_migration_suffixes":
			cfg.SQLMigrationSuffixes = splitAndTrimCLI(val, ",")
		case "encoding":
			cfg.Encoding = val
		case "connectretries", "connect-retries", "connect_retries":
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return nil, fmt.Errorf("invalid integer value %q for %s", val, key)
			}
			cfg.ConnectRetries = n
		case "lockretrycount", "lock-retry-count", "lock_retry_count":
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return nil, fmt.Errorf("invalid integer value %q for %s", val, key)
			}
			cfg.LockRetryCount = n
		case "outputtype", "output-type", "output_type", "output":
			cfg.OutputType = val
		case "dryrunoutput", "dry-run-output", "dry_run_output":
			cfg.DryRunOutput = val
		case "gcpprojectid", "project", "projectid", "project-id", "project_id":
			cfg.GCPProjectID = val
		case "gcpdataset", "dataset":
			cfg.GCPDataset = val
		case "gcplocation", "location":
			cfg.GCPLocation = val
		case "gcpcredentialsfile", "keyfile", "credentials-file", "credentials_file":
			cfg.GCPCredentialsFile = val
		case "gcpcredentialsjson", "credentials-json", "credentials_json":
			cfg.GCPCredentialsJSON = val
		case "gcpbigqueryendpoint", "bigqueryendpoint", "bigquery-endpoint":
			cfg.GCPBigQueryEndpoint = val
		case "oauthtype", "oauth-type", "oauth_type", "gcpoauthtype":
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return nil, fmt.Errorf("invalid integer value %q for %s", val, key)
			}
			cfg.GCPOAuthType = &n
		case "oauthaccesstoken", "oauth-access-token", "oauth_access_token", "gcpaccesstoken":
			cfg.GCPAccessToken = val
		case "oauthpvtkeypath", "oauth-pvt-key-path", "oauth_pvt_key_path":
			cfg.GCPCredentialsFile = val
		case "driver":
			cfg.Driver = val
		case "gcpspannerinstanceid", "spannerinstanceid", "spannerinstance", "spanner-instance-id", "spanner-instance", "spanner_instance_id":
			cfg.GCPSpannerInstanceID = val
		case "gcpspannerdatabaseid", "spannerdatabaseid", "spannerdatabase", "spanner-database-id", "spanner-database", "spanner_database_id":
			cfg.GCPSpannerDatabaseID = val
		case "gcpspannerendpoint", "spannerendpoint", "spanner-endpoint", "spanner_endpoint":
			cfg.GCPSpannerEndpoint = val
		case "installedby", "installed-by", "installed_by":
			cfg.InstalledBy = val
		case "environment", "env":
			cfg.Environment = val
		case "configfile", "configfiles", "config-file", "config-files":
			// Handled in step 1
		case "outputfile", "output-file", "output_file":
			cfg.OutputFile = val
		case "workingdirectory", "working-directory", "working_directory":
			cfg.WorkingDirectory = val
		case "color":
			cVal := strings.ToLower(strings.TrimSpace(val))
			if cVal != "always" && cVal != "never" && cVal != "auto" {
				return nil, fmt.Errorf("invalid color '%s': must be 'always', 'never' or 'auto'", val)
			}
			cfg.Color = cVal
		case "outputlogsinjson", "output-logs-in-json", "output_logs_in_json":
			log.Printf("WARNING: -outputLogsInJson is not supported by noway and is ignored")
		case "configfileencoding", "config-file-encoding", "config_file_encoding":
			log.Printf("WARNING: -configFileEncoding is not supported by noway and is ignored")
		case "infosincedate", "infountildate", "infosinceversion", "infountilversion", "infoofstate",
			"info-since-date", "info-until-date", "info-since-version", "info-until-version", "info-of-state",
			"info_since_date", "info_until_date", "info_since_version", "info_until_version", "info_of_state":
			log.Printf("WARNING: -%s is not supported by noway and is ignored", key)
		default:
			if strings.HasPrefix(strings.ToLower(key), "placeholders.") || strings.HasPrefix(strings.ToLower(key), "placeholder.") {
				phKey := strings.TrimPrefix(key, "placeholders.")
				phKey = strings.TrimPrefix(phKey, "placeholder.")
				if cfg.Placeholders == nil {
					cfg.Placeholders = make(map[string]string)
				}
				cfg.Placeholders[phKey] = val
			} else if strings.HasPrefix(strings.ToLower(key), "jdbcproperties.") || strings.HasPrefix(strings.ToLower(key), "plugins.") || strings.HasPrefix(strings.ToLower(key), "configurations.") {
				log.Printf("WARNING: %s is not supported by noway and is ignored", key)
			} else if config.IsUnsupportedFlywayKey(key) {
				log.Printf("WARNING: -%s is not supported by noway and is ignored", key)
			} else {
				return nil, config.UnknownPropertyError(key)
			}
		}
	}

	if err := cfg.Finalize(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func splitAndTrimCLI(s, sep string) []string {
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

func parseBoolVal(v string) (bool, error) {
	return config.ParseBool(v)
}

func outputJSONError(err error) {
	type ErrorDetails struct {
		ErrorCode string `json:"errorCode"`
		Message   string `json:"message"`
	}
	type ErrorResult struct {
		Error ErrorDetails `json:"error"`
	}
	data, _ := json.MarshalIndent(ErrorResult{
		Error: ErrorDetails{
			ErrorCode: "FAULT",
			Message:   err.Error(),
		},
	}, "", "  ")
	fmt.Println(string(data))
}

func runMigrate(ctx context.Context, nw *noway.Noway, cfg *config.Configuration) {
	if cfg.OutputType != "json" {
		fmt.Printf("Database: %s\n", cfg.GCPProjectID)
		fmt.Printf("Schema: %s\n", cfg.GetDefaultSchema())
		fmt.Println("Executing migrate...")
	}

	res, err := nw.Migrate(ctx)
	if err != nil {
		if cfg.OutputType == "json" {
			outputJSONError(err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputType == "json" {
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		return
	}

	if res.MigrationsExecuted == 0 {
		fmt.Println("Schema is up to date. No migrations needed.")
	} else {
		for _, m := range res.ExecutedMigrations {
			fmt.Printf("Successfully applied 1 migration to schema `%s` (execution time %dms): %s - %s\n",
				cfg.GetDefaultSchema(), m.ExecutionTime, m.Version, m.Description)
		}
		fmt.Printf("Successfully applied %d migration(s) to schema `%s` (total time: %dms), now at version v%s\n",
			res.MigrationsExecuted, cfg.GetDefaultSchema(), res.TotalExecutionTime, res.TargetVersion)
	}
}

func runInfo(ctx context.Context, nw *noway.Noway, cfg *config.Configuration) {
	res, err := nw.Info(ctx)
	if err != nil {
		if cfg.OutputType == "json" {
			outputJSONError(err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputType == "json" {
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		return
	}

	res.RenderTable(os.Stdout)
}

func runValidate(ctx context.Context, nw *noway.Noway, cfg *config.Configuration) {
	res, err := nw.Validate(ctx)
	if err != nil {
		if cfg.OutputType == "json" {
			outputJSONError(err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputType == "json" {
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		if !res.Valid {
			os.Exit(1)
		}
		return
	}

	if res.Valid {
		fmt.Println("Successfully validated all applied migrations (0 errors).")
	} else {
		fmt.Fprintln(os.Stderr, res.Error())
		os.Exit(1)
	}
}

func runBaseline(ctx context.Context, nw *noway.Noway, cfg *config.Configuration) {
	res, err := nw.Baseline(ctx)
	if err != nil {
		if cfg.OutputType == "json" {
			outputJSONError(err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputType == "json" {
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		return
	}

	fmt.Printf("Successfully baselined schema `%s` with version %s (%s)\n", res.Schema, res.BaselineVersion, res.BaselineDescription)
}

func runRepair(ctx context.Context, nw *noway.Noway, cfg *config.Configuration) {
	res, err := nw.Repair(ctx)
	if err != nil {
		if cfg.OutputType == "json" {
			outputJSONError(err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputType == "json" {
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		return
	}

	if len(res.RemovedFailed) > 0 {
		for _, rem := range res.RemovedFailed {
			fmt.Printf("Removed failed migration entry: %s\n", rem)
		}
	}
	if len(res.AlignedChecksums) > 0 {
		for _, al := range res.AlignedChecksums {
			fmt.Printf("Aligned metadata for: %s\n", al)
		}
	}
	fmt.Println("Successfully repaired schema history table.")
}

func runClean(ctx context.Context, nw *noway.Noway, cfg *config.Configuration) {
	res, err := nw.Clean(ctx)
	if err != nil {
		if cfg.OutputType == "json" {
			outputJSONError(err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputType == "json" {
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		return
	}

	for _, s := range res.SchemasCleaned {
		fmt.Printf("Successfully cleaned schema `%s` (all objects dropped).\n", s)
	}
}

func runUndo(ctx context.Context, nw *noway.Noway, cfg *config.Configuration) {
	res, err := nw.Undo(ctx)
	if err != nil {
		if cfg.OutputType == "json" {
			outputJSONError(err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputType == "json" {
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		return
	}

	fmt.Printf("Successfully undone migration version %s (%s) in %dms\n", res.UndoneVersion, res.Script, res.ExecutionTime)
}

func printHelp() {
	printBanner()
	fmt.Print(`
Usage:
  noway [command] [flags/options]

Commands:
  migrate    Migrates the database to the newest version
  clean      Drops all objects in the configured schemas
  info       Prints details and status information about all migrations
  validate   Validates applied migrations against local migration files
  baseline   Baselines an existing database at a specific version
  repair     Repairs the schema history table (aligns checksums, removes failed entries)
  undo       Undoes the most recently applied versioned migration
  version    Prints version information

Configuration & Flags:
  -url=<url>                  JDBC URL (BigQuery or Cloud Spanner)
  -driver=<driver>            Driver (bigquery or cloudspanner)
  -gcpProjectId=<id>          Google Cloud Project ID
  -defaultSchema=<schema>     Default BigQuery dataset or Spanner schema
  -schemas=<s1,s2>            Comma-separated list of schemas / datasets
  -table=<table>              Schema history table name (default: flyway_schema_history)
  -locations=<loc1,loc2>      Migration script locations (default: filesystem:sql)
  -target=<version>           Target version to migrate up to (default: latest)
  -outOfOrder=<bool>          Allow applying migrations out of order (default: false)
  -validateOnMigrate=<bool>   Validate migrations before applying (default: true)
  -cleanDisabled=<bool>       Disable clean command for safety (default: true)
  -baselineVersion=<version>  Baseline version (default: 1)
  -baselineDescription=<desc> Baseline description (default: << Flyway Baseline >>)
  -baselineOnMigrate=<bool>   Auto-baseline on non-empty schema (default: false)
  -spannerInstanceId=<id>     Cloud Spanner instance ID
  -spannerDatabaseId=<id>     Cloud Spanner database ID
  -spannerEndpoint=<host:port> Cloud Spanner endpoint (e.g. localhost:9010)
  -placeholders.<key>=<val>   Set custom placeholder values
  -configFile=<file>          Load custom config file (.conf, .toml, .yaml, .json)
  -outputType=<text|json>     Output format (text or json)
  -dryRunOutput=<file>        Output file for dry run SQL script (or - for stdout)

Examples:
  noway migrate -url="jdbc:bigquery:;ProjectId=my-project;DefaultDataset=analytics;"
  noway migrate -url="jdbc:cloudspanner:/projects/my-project/instances/my-instance/databases/my-db"
  noway info -locations=filesystem:./migrations
  noway validate -gcpProjectId=my-project -defaultSchema=analytics
`)
}
