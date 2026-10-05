# noway 🚀

**noway** is a pure Go, drop-in replacement for [Flyway](https://flywaydb.org/), supporting Google Cloud BigQuery and Google Cloud Spanner.

It is designed so that any database previously migrated or managed using Flyway can seamlessly use **noway** instead with **zero migration or database changes required**.

---

## Supported Databases & Dialects

- 📊 **Google Cloud BigQuery**: Full support for BigQuery Standard SQL, datasets, routines, views, and BigQuery JDBC URLs.
- ⚡ **Google Cloud Spanner**: Full support for Spanner GoogleSQL DDL/DML, instances, databases, emulator support, and Cloud Spanner JDBC URLs (`jdbc:cloudspanner:/projects/.../instances/.../databases/...`).

---

## Key Features & Compatibility

- 🎯 **100% Flyway Schema History Compatibility**:
  - Reads and writes to standard `flyway_schema_history` table with exact BigQuery and Cloud Spanner data types and commit timestamp options.
- 🔒 **Matching Concurrency Row Locks**:
  - BigQuery: `installed_rank = -100` row lock with heartbeat renewal.
  - Cloud Spanner: `installed_rank = -1` row lock within read-write transactions with automatic 10-minute stale lock reclamation.
- 🧮 **Exact Flyway Checksum Algorithm**:
  - CRC32 checksum calculation matching Flyway's `ChecksumCalculator` (line-by-line, UTF-8 BOM removal, line-ending agnostic LF/CRLF).
  - Versioned / Undo / Baseline migrations use raw file content checksums.
  - Repeatable migrations calculate checksums after placeholder replacement (matching Flyway specification).
- ⚙️ **Configuration Compatibility**:
  - Reads existing `flyway.conf` / `flyway.toml` / `flyway.yaml` / `flyway.json` files.
  - Understands all standard `FLYWAY_*`, `SPANNER_*`, and `BIGQUERY_*` environment variables.
  - Supports standard Flyway JDBC BigQuery and Cloud Spanner connection strings.
- 📜 **Full Migration Script Dialect Support**:
  - BigQuery Standard SQL procedural blocks (`BEGIN ... END`, `IF ... THEN`, `LOOP`, `WHILE`, `CASE`).
  - Spanner DDL routing via `DatabaseAdminClient.UpdateDatabaseDdl` and DML routing via `ReadWriteTransaction`.
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
go install github.com/roryq/noway/cmd/noway@latest
```

You can also alias or symlink `noway` to `flyway`:
```bash
alias flyway="noway"
```

### Go Library

```bash
go get github.com/roryq/noway
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

	"github.com/roryq/noway"
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
