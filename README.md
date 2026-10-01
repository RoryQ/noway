# noway 🚀

**noway** is a pure Go, drop-in replacement for [Flyway](https://flywaydb.org/) and the official [`flyway-gcp-bigquery`](https://github.com/flyway/flyway/tree/main/flyway-database/flyway-gcp-bigquery) driver.

It is designed so that any database previously migrated or managed using Flyway can seamlessly use **noway** instead with **zero migration or database changes required**.

---

## Key Features & Compatibility

- 🎯 **100% Flyway Schema History Compatibility**:
  - Reads and writes to standard `flyway_schema_history` table with exact BigQuery data types:
    ```sql
    CREATE TABLE `flyway_schema_history` (
        `installed_rank` INT64 NOT NULL,
        `version` STRING,
        `description` STRING NOT NULL,
        `type` STRING NOT NULL,
        `script` STRING NOT NULL,
        `checksum` INT64,
        `installed_by` STRING NOT NULL,
        `installed_on` TIMESTAMP,
        `execution_time` INT64 NOT NULL,
        `success` BOOL NOT NULL
    );
    ```
- 🔒 **Matching Insert-Row Concurrency Lock**:
  - Implements Flyway's `installed_rank = -100` row-locking algorithm with automatic heartbeat renewal and expired lock cleanup.
- 🧮 **Exact Flyway Checksum Algorithm**:
  - CRC32 checksum calculation matching Flyway's `ChecksumCalculator` (line-by-line, UTF-8 BOM removal, line-ending agnostic LF/CRLF).
- ⚙️ **Configuration Compatibility**:
  - Reads existing `flyway.conf` / `flyway.toml` / `flyway.yaml` / `flyway.json` files.
  - Understands all standard `FLYWAY_*` environment variables (e.g. `FLYWAY_URL`, `FLYWAY_SCHEMAS`, `FLYWAY_TABLE`, `FLYWAY_LOCATIONS`, `FLYWAY_PLACEHOLDERS_*`).
  - Supports standard Flyway JDBC BigQuery connection strings:
    `jdbc:bigquery://https://www.googleapis.com/bigquery/v2:443;ProjectId=my-project;OAuthType=0;OAuthServiceAcctEmail=...;OAuthPKeyFile=...;DefaultDataset=my_dataset;Location=US;`
- 📜 **Full Migration Script Dialect Support**:
  - BigQuery Standard SQL procedural blocks (`BEGIN ... END`, `IF ... THEN`, `LOOP`, `WHILE`, `CASE`).
  - Single-line comments (`--` and `#`) and multiline comments (`/* ... */`).
  - Multiline string literals (`'''...'''` and `"""..."""`).
  - Versioned migrations (`V1__description.sql`), Repeatable migrations (`R__views.sql`), Undo migrations (`U1__undo.sql`), and Baseline migrations (`B1__baseline.sql`).
  - Lifecycle SQL Callbacks (`beforeMigrate.sql`, `afterMigrate.sql`, `beforeEachMigrate.sql`, etc.).
  - Placeholder substitution (`${placeholder}`, `${placeholder:default}`, `$${escaped}`).
- 📦 **Go Library & CLI**:
  - Use as a standalone CLI tool (`noway`) or embed directly in Go binaries with `embed.FS` support.

---

## Installation

### CLI Binary

```bash
go install github.com/RoryQ/noway/cmd/noway@latest
```

You can also alias or symlink `noway` to `flyway`:
```bash
alias flyway="noway"
```

### Go Library

```bash
go get github.com/RoryQ/noway
```

---

## CLI Usage

All standard Flyway commands are supported:

### 1. `migrate`
Migrates the database to the newest version.
```bash
# Using existing flyway.conf in the directory
noway migrate

# Or with CLI options
noway migrate \
  -url="jdbc:bigquery:;ProjectId=my-project;DefaultDataset=analytics;" \
  -locations=filesystem:./migrations \
  -target=2.0
```

### 2. `info`
Prints status and history of all migrations:
```bash
noway info
```
Output:
```
Schema version: analytics

Category    Version  Description         Type  Installed On         State    Execution Time  Checksum
--------    -------  -----------         ----  ------------         -----    --------------  --------
Versioned   1        create users        SQL   2026-10-01 19:40:00  Success  420ms           -192837482
Versioned   2        add index           SQL   2026-10-01 19:40:05  Success  310ms           847291038
Repeatable           reporting views     SQL   2026-10-01 19:40:08  Success  150ms           128374920
```

JSON output is also supported:
```bash
noway info -outputType=json
```

### 3. `validate`
Validates applied database migrations against local migration files (checks checksums, descriptions, missing files):
```bash
noway validate
```

### 4. `baseline`
Baselines an existing database at a specific version:
```bash
noway baseline -baselineVersion=1.0 -baselineDescription="Initial Baseline"
```

### 5. `repair`
Repairs the schema history table (removes failed migration rows and aligns checksums with local files):
```bash
noway repair
```

### 6. `clean`
Drops all tables, views, materialized views, and routines in the configured schemas:
```bash
noway clean -cleanDisabled=false
```

### 7. `undo`
Reverts the most recently applied versioned migration (if a matching `U...` script exists):
```bash
noway undo
```

---

## Go Library Usage

You can embed `noway` directly into your Go application with `embed.FS`:

```go
package main

import (
	"context"
	"embed"
	"log"

	"github.com/RoryQ/noway"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func main() {
	ctx := context.Background()

	nw, err := noway.New(
		noway.WithProject("my-gcp-project"),
		noway.WithDataset("analytics_dataset"),
		noway.WithLocation("US"),
		noway.WithFS(migrationsFS),
		noway.WithLocations("migrations"),
		noway.WithPlaceholders(map[string]string{
			"env": "production",
		}),
	)
	if err != nil {
		log.Fatalf("Failed to initialize noway: %v", err)
	}
	defer nw.Close()

	// Run migrations
	res, err := nw.Migrate(ctx)
	if err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	log.Printf("Applied %d migration(s), now at version %s", res.MigrationsExecuted, res.TargetVersion)
}
```

---

## Configuration (`flyway.conf`)

`noway` natively loads configuration from `flyway.conf`, `noway.conf`, TOML, YAML, or JSON files.

Example `flyway.conf`:
```properties
# Connection
flyway.url=jdbc:bigquery://https://www.googleapis.com/bigquery/v2:443;ProjectId=my-project;OAuthType=0;OAuthServiceAcctEmail=sa@my-project.iam.gserviceaccount.com;OAuthPKeyFile=/path/to/key.json;DefaultDataset=analytics;Location=US;

# Schema & Table
flyway.schemas=analytics,reporting
flyway.defaultSchema=analytics
flyway.table=flyway_schema_history

# Migrations
flyway.locations=filesystem:./sql
flyway.sqlMigrationPrefix=V
flyway.repeatableSqlMigrationPrefix=R
flyway.sqlMigrationSeparator=__
flyway.sqlMigrationSuffixes=.sql

# Placeholders
flyway.placeholders.environment=production
flyway.placeholders.retention_days=90

# Execution
flyway.validateOnMigrate=true
flyway.outOfOrder=false
flyway.cleanDisabled=true
```

---

## License

MIT License.
