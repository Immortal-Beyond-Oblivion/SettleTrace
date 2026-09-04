package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/audit"
	"github.com/go-sql-driver/mysql"
)

// MySQLStore is the production ingest store backed by InnoDB uniqueness constraints.
type MySQLStore struct {
	db interface {
		ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	}
	beginner beginner
}

type beginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// OpenMySQLStore wraps a database handle with append-only mutation guards.
func OpenMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: guardedDB{db: db}, beginner: db}
}

// InTx runs ingest writes in one transaction and commits only if fn returns nil.
func (store *MySQLStore) InTx(ctx context.Context, fn func(tx IngestStore) error) error {
	if store.beginner == nil {
		return fn(store)
	}
	tx, err := store.beginner.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin ingest tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	nested := &MySQLStore{db: guardedTx{tx: tx}}
	if err := fn(nested); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ingest tx: %w", err)
	}
	return nil
}

// InsertRawEvent writes an immutable landing row and maps uniqueness conflicts to ErrDuplicate.
func (store *MySQLStore) InsertRawEvent(ctx context.Context, event RawEvent) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO raw_events (source, external_id, event_type, idempotency_key, payload_json, received_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		event.Source, event.ExternalID, event.EventType, event.IdempotencyKey, []byte(event.Payload), event.ReceivedAt.UTC(),
	)
	return mapDuplicate(err)
}

// InsertPayment writes a normalized payment and treats a duplicate payment_id as a no-op.
func (store *MySQLStore) InsertPayment(ctx context.Context, payment PaymentRow) error {
	capturedAt := sql.NullTime{}
	if payment.CapturedAt != nil {
		capturedAt = sql.NullTime{Time: payment.CapturedAt.UTC(), Valid: true}
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO payments (
			payment_id, order_id, amount_paise, fee_paise, tax_paise, currency, method, status, captured_at, source_event_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE payment_id = payment_id`,
		payment.PaymentID, nullString(payment.OrderID), payment.AmountPaise, payment.FeePaise, payment.TaxPaise,
		payment.Currency, payment.Method, payment.Status, capturedAt, payment.SourceEventAt.UTC(),
	)
	return err
}

// InsertSettlement writes a normalized settlement line and ignores an exact uniqueness conflict.
func (store *MySQLStore) InsertSettlement(ctx context.Context, line SettlementRow) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO settlement_lines (
			settlement_id, entity_id, line_type, payment_method, credit_paise, debit_paise, fee_paise, tax_paise, settled_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE settlement_id = settlement_id`,
		line.SettlementID, line.EntityID, line.LineType, line.PaymentMethod, line.CreditPaise, line.DebitPaise, line.FeePaise, line.TaxPaise, line.SettledAt.UTC(),
	)
	return err
}

// InsertBankLine writes a normalized bank line and ignores a duplicate reference.
func (store *MySQLStore) InsertBankLine(ctx context.Context, line BankRow) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO bank_lines (reference_id, credit_paise, booked_at)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE reference_id = reference_id`,
		line.ReferenceID, line.CreditPaise, line.BookedAt.UTC(),
	)
	return err
}

// InsertLedgerLine writes a normalized ledger line and ignores a duplicate reference.
func (store *MySQLStore) InsertLedgerLine(ctx context.Context, line LedgerRow) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO ledger_lines (reference_id, amount_paise, booked_at)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE reference_id = reference_id`,
		line.ReferenceID, line.AmountPaise, line.BookedAt.UTC(),
	)
	return err
}

// Count returns current ingest table totals.
func (store *MySQLStore) Count(ctx context.Context) (Counts, error) {
	var counts Counts
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM raw_events`).Scan(&counts.RawEvents); err != nil {
		return Counts{}, err
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payments`).Scan(&counts.Payments); err != nil {
		return Counts{}, err
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settlement_lines`).Scan(&counts.Settlements); err != nil {
		return Counts{}, err
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bank_lines`).Scan(&counts.BankLines); err != nil {
		return Counts{}, err
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ledger_lines`).Scan(&counts.LedgerLines); err != nil {
		return Counts{}, err
	}
	return counts, nil
}

// mapDuplicate converts a MySQL duplicate-key error into ErrDuplicate.
func mapDuplicate(err error) error {
	if err == nil {
		return nil
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return ErrDuplicate
	}
	if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		return ErrDuplicate
	}
	return err
}

// nullString converts empty strings to SQL NULL.
func nullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

type guardedDB struct{ db *sql.DB }

// ExecContext rejects append-only mutations before executing SQL.
func (g guardedDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if err := GuardMutation(query); err != nil {
		return nil, err
	}
	return g.db.ExecContext(ctx, query, args...)
}

// QueryRowContext reads a single row without mutation checks beyond SELECT usage.
func (g guardedDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return g.db.QueryRowContext(ctx, query, args...)
}

// QueryContext reads multiple rows without mutation checks beyond SELECT usage.
func (g guardedDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return g.db.QueryContext(ctx, query, args...)
}

type guardedTx struct{ tx *sql.Tx }

// ExecContext rejects append-only mutations before executing transactional SQL.
func (g guardedTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if err := GuardMutation(query); err != nil {
		return nil, err
	}
	return g.tx.ExecContext(ctx, query, args...)
}

// QueryRowContext reads a single transactional row.
func (g guardedTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return g.tx.QueryRowContext(ctx, query, args...)
}

// QueryContext reads multiple transactional rows.
func (g guardedTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return g.tx.QueryContext(ctx, query, args...)
}

// GetUnmatchedPaymentsInWindow returns captured payments with no match or open exception yet.
func (store *MySQLStore) GetUnmatchedPaymentsInWindow(ctx context.Context, start, end time.Time) ([]PaymentCandidate, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT p.payment_id, COALESCE(p.order_id, ''), p.amount_paise, p.fee_paise, p.tax_paise, p.method, p.captured_at
		FROM payments p
		WHERE p.captured_at IS NOT NULL AND p.captured_at >= ? AND p.captured_at < ?
			AND NOT EXISTS (
				SELECT 1 FROM match_results mr WHERE mr.record_type = 'payment' AND mr.record_id = p.payment_id
			)
			AND NOT EXISTS (
				SELECT 1 FROM exception_log e WHERE e.record_type = 'payment' AND e.record_id = p.payment_id AND e.resolved_at IS NULL
			)
		ORDER BY p.captured_at ASC`,
		start.UTC(), end.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("query unmatched payments: %w", err)
	}
	defer rows.Close()
	candidates := make([]PaymentCandidate, 0)
	for rows.Next() {
		var candidate PaymentCandidate
		if err := rows.Scan(&candidate.PaymentID, &candidate.OrderID, &candidate.AmountPaise, &candidate.FeePaise, &candidate.TaxPaise, &candidate.Method, &candidate.CapturedAt); err != nil {
			return nil, fmt.Errorf("scan unmatched payment: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// GetPaymentsInWindow returns all captured payments in the window, for Tier L candidates.
func (store *MySQLStore) GetPaymentsInWindow(ctx context.Context, start, end time.Time) ([]PaymentCandidate, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT payment_id, COALESCE(order_id, ''), amount_paise, fee_paise, tax_paise, method, captured_at
		FROM payments
		WHERE captured_at IS NOT NULL AND captured_at >= ? AND captured_at < ?
		ORDER BY captured_at ASC`,
		start.UTC(), end.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("query payments in window: %w", err)
	}
	defer rows.Close()
	candidates := make([]PaymentCandidate, 0)
	for rows.Next() {
		var candidate PaymentCandidate
		if err := rows.Scan(&candidate.PaymentID, &candidate.OrderID, &candidate.AmountPaise, &candidate.FeePaise, &candidate.TaxPaise, &candidate.Method, &candidate.CapturedAt); err != nil {
			return nil, fmt.Errorf("scan payment in window: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// GetSettlementCandidates returns settlement lines for one method settled inside the window.
func (store *MySQLStore) GetSettlementCandidates(ctx context.Context, method string, start, end time.Time) ([]SettlementCandidate, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT id, COALESCE(entity_id, ''), credit_paise, payment_method, settled_at
		FROM settlement_lines
		WHERE payment_method = ? AND settled_at >= ? AND settled_at < ?
		ORDER BY settled_at ASC`,
		method, start.UTC(), end.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("query settlement candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]SettlementCandidate, 0)
	for rows.Next() {
		var candidate SettlementCandidate
		if err := rows.Scan(&candidate.ID, &candidate.EntityID, &candidate.CreditPaise, &candidate.Method, &candidate.SettledAt); err != nil {
			return nil, fmt.Errorf("scan settlement candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// GetUnmatchedLedgerLines returns ledger lines that have not yet resolved to a payment and
// were booked in [start, end). Bounding by booked_at keeps this a targeted index range scan
// instead of a full-table scan of every unmatched ledger row ever ingested; callers pass an
// already-widened window (see matcher.ledgerBookingLagWindow) so no in-window match is missed.
func (store *MySQLStore) GetUnmatchedLedgerLines(ctx context.Context, start, end time.Time) ([]LedgerCandidate, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT id, reference_id, amount_paise, booked_at
		FROM ledger_lines
		WHERE matched_payment_id IS NULL AND booked_at >= ? AND booked_at < ?
		ORDER BY booked_at ASC`,
		start.UTC(), end.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("query unmatched ledger lines: %w", err)
	}
	defer rows.Close()
	candidates := make([]LedgerCandidate, 0)
	for rows.Next() {
		var candidate LedgerCandidate
		if err := rows.Scan(&candidate.ID, &candidate.ReferenceID, &candidate.AmountPaise, &candidate.BookedAt); err != nil {
			return nil, fmt.Errorf("scan unmatched ledger line: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// WriteMatchResult appends a reconciliation match; the guard rejects any later update or delete.
func (store *MySQLStore) WriteMatchResult(ctx context.Context, match MatchResultRow) error {
	evidence := match.EvidenceJSON
	if evidence == nil {
		evidence = json.RawMessage("{}")
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO match_results (match_group_id, record_type, record_id, confidence, rule_id, evidence_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		match.MatchGroupID, match.RecordType, match.RecordID, match.Confidence, match.RuleID, []byte(evidence), match.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("write match result: %w", err)
	}
	return nil
}

// WriteExceptionLog appends an unresolved reconciliation exception.
func (store *MySQLStore) WriteExceptionLog(ctx context.Context, exception ExceptionRow) error {
	evidence := exception.EvidenceJSON
	if evidence == nil {
		evidence = json.RawMessage("{}")
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO exception_log (record_type, record_id, reason_code, amount_at_risk_paise, evidence_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		exception.RecordType, exception.RecordID, exception.ReasonCode, exception.AmountAtRiskPaise, []byte(evidence), exception.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("write exception log: %w", err)
	}
	return nil
}

// WriteAuditEntry reads the previous audit_log row hash, seals the new entry against it, and
// appends it inside one transaction, so the hash chain cannot fork under concurrent writers.
func (store *MySQLStore) WriteAuditEntry(ctx context.Context, entry AuditEntryRow) error {
	if store.beginner == nil {
		return errors.New("audit entry requires a MySQLStore opened with a database handle")
	}
	tx, err := store.beginner.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin audit tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var previousHash sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT row_hash FROM audit_log ORDER BY id DESC LIMIT 1 FOR UPDATE`).Scan(&previousHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read previous audit hash: %w", err)
	}

	payload := entry.PayloadJSON
	if payload == nil {
		payload = json.RawMessage("{}")
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return fmt.Errorf("decode audit payload: %w", err)
	}
	// MySQL's DATETIME(6) only keeps microsecond precision, so the hash must be computed
	// on the same truncated timestamp that will actually be stored and later re-read by
	// reconctl's verify-chain, or a nanosecond-precision Go time.Time would seal a hash
	// that a later, DB-round-tripped Verify() could never reproduce.
	createdAt := entry.CreatedAt.UTC().Truncate(time.Microsecond)
	sealed, err := audit.Seal(audit.Entry{
		EventType:    entry.EventType,
		Payload:      decoded,
		PreviousHash: previousHash.String,
		CreatedAt:    createdAt,
	})
	if err != nil {
		return fmt.Errorf("seal audit entry: %w", err)
	}

	var previousHashArg any
	if previousHash.Valid {
		previousHashArg = previousHash.String
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (event_type, payload_json, previous_hash, row_hash, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		sealed.EventType, []byte(payload), previousHashArg, sealed.RowHash, sealed.CreatedAt,
	); err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return tx.Commit()
}

// SetLedgerMatchedPayment records which payment a ledger line resolved to, exactly once.
func (store *MySQLStore) SetLedgerMatchedPayment(ctx context.Context, ledgerLineID int64, paymentID string) error {
	_, err := store.db.ExecContext(ctx, `
		UPDATE ledger_lines SET matched_payment_id = ? WHERE id = ? AND matched_payment_id IS NULL`,
		paymentID, ledgerLineID,
	)
	if err != nil {
		return fmt.Errorf("set ledger matched payment: %w", err)
	}
	return nil
}

// GetExceptionByID reads one exception_log row by primary key for the AI explainer's read
// path (internal/api's explain handler). Returns ErrExceptionNotFound when no row matches,
// so the caller can respond 404 rather than a generic 500.
func (store *MySQLStore) GetExceptionByID(ctx context.Context, id int64) (ExceptionRecord, error) {
	row := store.db.QueryRowContext(ctx, `
		SELECT id, record_type, record_id, reason_code, amount_at_risk_paise, evidence_json, resolved_at, created_at
		FROM exception_log
		WHERE id = ?`,
		id,
	)
	var record ExceptionRecord
	var evidence []byte
	var resolvedAt sql.NullTime
	if err := row.Scan(&record.ID, &record.RecordType, &record.RecordID, &record.ReasonCode, &record.AmountAtRiskPaise, &evidence, &resolvedAt, &record.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ExceptionRecord{}, ErrExceptionNotFound
		}
		return ExceptionRecord{}, fmt.Errorf("get exception by id: %w", err)
	}
	record.EvidenceJSON = json.RawMessage(evidence)
	record.CreatedAt = record.CreatedAt.UTC()
	if resolvedAt.Valid {
		resolved := resolvedAt.Time.UTC()
		record.ResolvedAt = &resolved
	}
	return record, nil
}

// WriteAIExplanationLog appends one ai_explanation_log row. This is a plain INSERT, called
// unconditionally by internal/ai's Explainer whether the underlying LLM call succeeded,
// failed, or was skipped by the budget cap or circuit breaker -- implementation.md section 8's
// "every AI-generated sentence is stored, permanently" claim is only checkable if this write
// happens on every path, not just the success path.
func (store *MySQLStore) WriteAIExplanationLog(ctx context.Context, log AIExplanationLogRow) error {
	input := log.InputSummaryJSON
	if input == nil {
		input = json.RawMessage("{}")
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO ai_explanation_log (
			exception_id, prompt_version, model, input_summary_json, output_text, latency_ms, succeeded, error_message, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ExceptionID, log.PromptVersion, log.Model, []byte(input), log.OutputText, log.LatencyMS, log.Succeeded, nullString(log.ErrorMessage), log.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("write ai explanation log: %w", err)
	}
	return nil
}
