package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ApplyMigrations executes versioned *.up.sql files in lexical order, skipping already applied files.
func ApplyMigrations(ctx context.Context, db *sql.DB, directory string) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename VARCHAR(255) PRIMARY KEY,
			applied_at DATETIME(6) NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
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
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE filename = ?`, name).Scan(&count); err != nil {
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
				return fmt.Errorf("apply %s: %w", name, err)
			}
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (filename, applied_at) VALUES (?, ?)`, name, time.Now().UTC()); err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
	}
	return nil
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
