package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// ApplyMigrations executes versioned *.up.sql files in lexical order, skipping already applied files.
//
// Tracking table: settletrace_schema_migrations, deliberately NOT named "schema_migrations".
// implementation.md section 9/12 documents an alternative setup path using the external
// golang-migrate CLI (`migrate -path ./migrations -database "$DB_DSN" up`), which creates and
// owns its OWN table also named "schema_migrations", shaped (version bigint, dirty boolean) --
// a completely different, incompatible convention from this function's (filename varchar,
// applied_at datetime). Every actual running binary in this repo (cmd/api, cmd/matching-engine)
// calls THIS function, never the CLI, so this function is the one real source of truth for this
// repo's migration state -- but sharing the same table name as the CLI's tracker meant that if
// the CLI's table ever existed first (as it did on the dev DB -- see state.md's migration-table-
// collision writeup), CREATE TABLE IF NOT EXISTS silently kept the CLI's incompatible table and
// every subsequent query against the expected columns failed. Renaming this function's table
// removes the possibility of that collision entirely, without needing to touch or drop whatever
// the CLI may have created -- the two trackers simply no longer share a name. Per the owner's
// explicit decision (state.md), this Go-native function is the one tracker going forward; the
// external golang-migrate CLI path in implementation.md is no longer how this repo's migrations
// are meant to be run (flagged there as a follow-up doc fix, not changed in this same pass).
func ApplyMigrations(ctx context.Context, db *sql.DB, directory string) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS settletrace_schema_migrations (
			filename VARCHAR(255) PRIMARY KEY,
			applied_at DATETIME(6) NOT NULL
		)`); err != nil {
		return fmt.Errorf("create settletrace_schema_migrations: %w", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settletrace_schema_migrations WHERE filename = ?`, name).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		for _, statement := range splitSQL(string(body)) {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				if isAlreadyAppliedError(err) {
					// The object this statement would create/rename/drop already exists on this
					// DB -- the expected bootstrap case the very first run of the renamed
					// settletrace_schema_migrations tracker hits against a DB whose schema was
					// already fully built by an earlier run (whether via this same function
					// under its old "schema_migrations" name, or by hand, or by the
					// golang-migrate CLI implementation.md documents). A fresh tracker table
					// starts with zero rows, so every migration file looks unapplied to it even
					// though the DDL underneath already ran -- without this check, re-running
					// that already-applied DDL fails outright (e.g. Error 1050 "Table
					// 'raw_events' already exists") and ApplyMigrations degrades to "never
					// actually verifies anything," the exact regression this fix closes.
					// Treating it as already-applied and moving on is safe specifically because
					// this is this repo's OWN migration file: if the object already exists, it
					// was built BY this same file at some point in the past (there is no other
					// source of raw_events/payments/etc. in this codebase), so skipping the
					// statement and still recording the file as applied reflects reality rather
					// than papering over a genuine mismatch.
					log.Printf("migration %s: statement already applied, skipping (%v)", name, err)
					continue
				}
				return fmt.Errorf("apply %s: %w", name, err)
			}
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO settletrace_schema_migrations (filename, applied_at) VALUES (?, ?)`, name, time.Now().UTC()); err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
	}
	return nil
}

// isAlreadyAppliedError reports whether err is a MySQL "this object already exists / already
// doesn't exist" error -- the class of error a migration statement returns when its target
// (table, column, index, key, constraint) was already created or already removed by an earlier
// application of the SAME migration file, as opposed to a genuine, novel failure (bad SQL,
// permissions, connection loss) that must still abort the run. The specific codes:
//   - 1050: CREATE TABLE, table already exists
//   - 1060: ALTER TABLE ADD COLUMN, duplicate column name
//   - 1061: ALTER TABLE ADD KEY/INDEX, duplicate key name
//   - 1091: ALTER TABLE DROP INDEX/COLUMN, the thing being dropped doesn't exist (i.e. it was
//     already dropped by an earlier run of this same statement -- 0002_ledger_uniqueness.up.sql's
//     combined DROP INDEX + ADD UNIQUE KEY is exactly this shape)
//   - 1826: ALTER TABLE ADD CONSTRAINT, duplicate foreign key constraint name
func isAlreadyAppliedError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 1050, 1060, 1061, 1091, 1826:
		return true
	default:
		return false
	}
}

// splitSQL splits a migration file into individual statements.
func splitSQL(body string) []string {
	parts := strings.Split(body, ";")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		statement := strings.TrimSpace(part)
		if statement == "" {
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}
