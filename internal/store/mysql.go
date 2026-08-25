package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// MySQLStore is the production ingest store backed by InnoDB uniqueness constraints.
type MySQLStore struct {
	db interface {
		ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
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
