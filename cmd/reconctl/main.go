// Command reconctl provides controlled local operator actions.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/audit"
	_ "github.com/go-sql-driver/mysql"
)

// main dispatches the requested operator command.
func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: reconctl verify-chain | help")
		return
	}
	switch os.Args[1] {
	case "help":
		fmt.Println("commands: verify-chain, help")
	case "verify-chain":
		if err := runVerifyChain(); err != nil {
			fmt.Fprintln(os.Stderr, "verify-chain failed:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		os.Exit(2)
	}
}

// runVerifyChain connects to MySQL using DB_DSN, loads every audit_log row in insertion
// order, and reports whether the hash chain audit.Verify computes still holds.
func runVerifyChain() error {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		return fmt.Errorf("DB_DSN is required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	entries, err := loadAuditEntries(ctx, db)
	if err != nil {
		return fmt.Errorf("load audit_log: %w", err)
	}
	if len(entries) == 0 {
		fmt.Println("verified: true (0 rows)")
		return nil
	}
	brokenAt, verifyErr := audit.Verify(entries)
	if verifyErr != nil {
		fmt.Printf("verified: false\nfirst_break_at_row: %d\ndetail: %v\n", brokenAt, verifyErr)
		return nil
	}
	fmt.Printf("verified: true (%d rows)\n", len(entries))
	return nil
}

// loadAuditEntries reads every audit_log row ordered by id into audit.Entry values.
func loadAuditEntries(ctx context.Context, db *sql.DB) ([]audit.Entry, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT event_type, payload_json, COALESCE(previous_hash, ''), row_hash, created_at
		FROM audit_log
		ORDER BY id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]audit.Entry, 0)
	for rows.Next() {
		var eventType, previousHash, rowHash string
		var payloadJSON []byte
		var createdAt time.Time
		if err := rows.Scan(&eventType, &payloadJSON, &previousHash, &rowHash, &createdAt); err != nil {
			return nil, err
		}
		var payload any
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			return nil, fmt.Errorf("decode payload_json: %w", err)
		}
		entries = append(entries, audit.Entry{
			EventType:    eventType,
			Payload:      payload,
			PreviousHash: previousHash,
			CreatedAt:    createdAt.UTC(),
			RowHash:      rowHash,
		})
	}
	return entries, rows.Err()
}
