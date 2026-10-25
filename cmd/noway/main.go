package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
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
		case "x":
			cfg.Debug = true
		case "q":
			cfg.Quiet = true
		case "skipcheckforupdate", "communityfallback":
			// Flyway CLI compatibility flags
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
		default:
			if err := config.ApplyProperty(cfg, key, val); err != nil {
				return nil, err
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
