package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/roryq/noway"
	"github.com/roryq/noway/pkg/config"
)

const AppVersion = "1.0.0"

func printBanner() {
	fmt.Printf("Noway v%s by RoryQ - Drop-in Flyway replacement for BigQuery & Cloud Spanner\n", AppVersion)
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	cmd := os.Args[1]
	if cmd == "version" || cmd == "-version" || cmd == "--version" || cmd == "-v" {
		printBanner()
		return
	}

	if cmd == "help" || cmd == "-help" || cmd == "--help" || cmd == "-h" {
		printHelp()
		return
	}

	// Parse command-line flags and properties
	cfg, err := parseCLIArgs(os.Args[2:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputType != "json" {
		printBanner()
	}

	nw, err := noway.New(noway.WithConfig(cfg))
	if err != nil {
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

	// 1. Check for configFile/configFiles in args first
	var configFiles []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		clean := strings.TrimLeft(arg, "-")
		if idx := strings.Index(clean, "="); idx != -1 {
			k := strings.ToLower(clean[:idx])
			v := clean[idx+1:]
			if k == "configfile" || k == "configfiles" || k == "config-file" || k == "config-files" {
				configFiles = append(configFiles, splitAndTrimCLI(v, ",")...)
			}
		} else if clean == "configFile" || clean == "configFiles" || clean == "config-file" || clean == "config-files" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				configFiles = append(configFiles, splitAndTrimCLI(args[i+1], ",")...)
				i++
			}
		}
	}

	if len(configFiles) > 0 {
		for _, cf := range configFiles {
			cf = strings.TrimSpace(cf)
			if cf == "" {
				continue
			}
			loaded, err := config.LoadFromFile(cf)
			if err != nil {
				return nil, err
			}
			cfg = loaded
		}
	} else if autoConf := config.FindConfigFile(); autoConf != "" {
		loaded, err := config.LoadFromFile(autoConf)
		if err == nil {
			cfg = loaded
		}
	}

	// 2. Load from Environment
	config.LoadFromEnv(cfg)

	// 3. Parse individual CLI arguments (supports -flag=val, --flag=val, -flag val, and -placeholders.key=val)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			continue
		}

		cleanArg := strings.TrimLeft(arg, "-")
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
			cfg.CreateSchemas = parseBoolVal(val)
		case "defaultschema":
			cfg.DefaultSchema = val
		case "table":
			cfg.Table = val
		case "locations":
			cfg.Locations = splitAndTrimCLI(val, ",")
		case "target":
			cfg.Target = val
		case "outoforder":
			cfg.OutOfOrder = parseBoolVal(val)
		case "validateonmigrate":
			cfg.ValidateOnMigrate = parseBoolVal(val)
		case "cleandisabled":
			cfg.CleanDisabled = parseBoolVal(val)
		case "baselineversion":
			cfg.BaselineVersion = val
		case "baselinedescription":
			cfg.BaselineDescription = val
		case "baselineonmigrate":
			cfg.BaselineOnMigrate = parseBoolVal(val)
		case "outputtype", "output":
			cfg.OutputType = val
		case "gcpprojectid", "project":
			cfg.GCPProjectID = val
		case "gcpdataset", "dataset":
			cfg.GCPDataset = val
		case "gcplocation", "location":
			cfg.GCPLocation = val
		case "gcpcredentialsfile", "keyfile":
			cfg.GCPCredentialsFile = val
		case "driver":
			cfg.Driver = val
		case "gcpspannerinstanceid", "spannerinstanceid", "spannerinstance", "spanner-instance-id", "spanner-instance":
			cfg.GCPSpannerInstanceID = val
		case "gcpspannerdatabaseid", "spannerdatabaseid", "spannerdatabase", "spanner-database-id", "spanner-database":
			cfg.GCPSpannerDatabaseID = val
		case "gcpspannerendpoint", "spannerendpoint", "spanner-endpoint":
			cfg.GCPSpannerEndpoint = val
		case "configfile", "configfiles", "config-file", "config-files":
			// Handled in step 1
		default:
			if strings.HasPrefix(key, "placeholders.") || strings.HasPrefix(key, "placeholder.") {
				phKey := strings.TrimPrefix(key, "placeholders.")
				phKey = strings.TrimPrefix(phKey, "placeholder.")
				if cfg.Placeholders == nil {
					cfg.Placeholders = make(map[string]string)
				}
				cfg.Placeholders[phKey] = val
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

func parseBoolVal(v string) bool {
	l := strings.ToLower(v)
	return l == "true" || l == "yes" || l == "1" || l == "on"
}

func runMigrate(ctx context.Context, nw *noway.Noway, cfg *config.Configuration) {
	fmt.Printf("Database: %s\n", cfg.GCPProjectID)
	fmt.Printf("Schema: %s\n", cfg.GetDefaultSchema())
	fmt.Println("Executing migrate...")

	res, err := nw.Migrate(ctx)
	if err != nil {
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
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputType == "json" {
		data, _ := json.MarshalIndent(res.FlywayFormat, "", "  ")
		fmt.Println(string(data))
		return
	}

	res.RenderTable(os.Stdout)
}

func runValidate(ctx context.Context, nw *noway.Noway, cfg *config.Configuration) {
	res, err := nw.Validate(ctx)
	if err != nil {
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

Examples:
  noway migrate -url="jdbc:bigquery:;ProjectId=my-project;DefaultDataset=analytics;"
  noway migrate -url="jdbc:cloudspanner:/projects/my-project/instances/my-instance/databases/my-db"
  noway info -locations=filesystem:./migrations
  noway validate -gcpProjectId=my-project -defaultSchema=analytics
`)
}
