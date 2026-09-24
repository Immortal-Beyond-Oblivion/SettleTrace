package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/audit"
)

// Compile-time proof that MySQLStore satisfies AuditReader. cmd/api assigns a *MySQLStore to
// api.Server.Audit; this line makes a rename or re-signature of LoadAuditEntries fail inside
// this package, next to the code, instead of surfacing as an assignment error in cmd/api.
var _ AuditReader = (*MySQLStore)(nil)

// LoadAuditEntries reads every audit_log row in insertion order (id ASC) and returns them as
// audit.Entry values ready for audit.Verify. It is the read-side twin of WriteAuditEntry.
//
// The query and the field mapping deliberately match cmd/reconctl's loadAuditEntries exactly
// (COALESCE(previous_hash, '') so the first row's NULL becomes the "" that audit.Verify expects,
// created_at converted to UTC), so `reconctl verify-chain` and POST /v1/ingest/verify-chain can
// never disagree about the same table. The whole table is loaded into memory, same as the CLI.
//
// created_at scans into time.Time, which needs parseTime=true in the DSN, the same requirement
// GetExceptionByID and reconctl already have.
func (store *MySQLStore) LoadAuditEntries(ctx context.Context) ([]audit.Entry, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT event_type, payload_json, COALESCE(previous_hash, ''), row_hash, created_at
		FROM audit_log
		ORDER BY id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query audit_log: %w", err)
	}
	defer rows.Close()

	entries := make([]audit.Entry, 0)
	for rows.Next() {
		var eventType, previousHash, rowHash string
		var payloadJSON []byte
		var createdAt time.Time
		if err := rows.Scan(&eventType, &payloadJSON, &previousHash, &rowHash, &createdAt); err != nil {
			return nil, fmt.Errorf("scan audit_log row: %w", err)
		}
		var payload any
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			return nil, fmt.Errorf("decode audit_log payload_json: %w", err)
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
