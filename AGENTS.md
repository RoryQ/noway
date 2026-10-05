# Project Instructions & Verification Rules

## 1. Flyway Schema History & Parity Rules
- **Repeatable Migrations Type**: Repeatable SQL migrations (`R__*.sql`) MUST resolve with `Type: "SQL"` (and script migrations with `Type: "SCRIPT"`). Repeatable migrations are represented in Flyway by `Version: nil`, **never** by a distinct `REPEATABLE` type.
- **Allowed History Record Types**: The `Type` column in `flyway_schema_history` must strictly adhere to Flyway's `CoreMigrationType` enum values:
  - `SCHEMA`
  - `BASELINE`
  - `SQL` (used for both versioned and repeatable SQL migrations)
  - `JDBC`
  - `CUSTOM`
  - `SCRIPT` (used for both versioned and repeatable script migrations)
  - `UNDO_SQL`, `UNDO_JDBC`, `UNDO_SCRIPT`
  - `DELETE`
- **Checksum Calculation Rules**:
  - `Versioned`, `Undo`, and `Baseline` migrations calculate CRC32 on raw file content (without placeholder replacement) to ensure cross-environment consistency.
  - `Repeatable` migrations calculate CRC32 on placeholder-replaced content (unless `placeholderReplacement=false`) so changes in placeholder values trigger re-execution.

## 2. Parity Testing & Adversarial Verification Invariants
- **No Self-Referential Mocking**: Never generate test assertions or mock databases using internal assumptions. When testing against Flyway behavior, verify against the official Flyway Java source code (`SqlMigrationResolver.java`, `ResolvedMigrationImpl.java`) or raw database exports.
- **Reference Inversion Rule**: When writing challenger or adversarial test cases, if a standard Flyway output (such as `Type: "SQL"` on a repeatable migration) causes a validation or checksum mismatch, the mismatch is a **bug in the implementation under test**, not an expected negative test case.
- **Enum Invariant Checks**: Any new migration or history type added to `noway` must be tested against the allowed Flyway `CoreMigrationType` constants.
