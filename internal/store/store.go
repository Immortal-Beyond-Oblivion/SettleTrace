// Package store persists reconciliation records and enforces append-only history.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrDuplicate is returned when an idempotency or uniqueness constraint already holds the row.
var ErrDuplicate = errors.New("duplicate ingest record")

// ErrAppendOnly is returned when a caller attempts to mutate protected history tables.
var ErrAppendOnly = errors.New("append-only table cannot be updated or deleted")

// RawEvent is the immutable landing record for a source payload.
type RawEvent struct {
	Source         string
	ExternalID     string
	EventType      string
	IdempotencyKey string
	Payload        json.RawMessage
	ReceivedAt     time.Time
}

// PaymentRow is the normalized gateway payment written during ingestion.
type PaymentRow struct {
	PaymentID     string
	OrderID       string
	AmountPaise   int64
	FeePaise      int64
	TaxPaise      int64
	Currency      string
	Method        string
	Status        string
	CapturedAt    *time.Time
	SourceEventAt time.Time
}

// SettlementRow is the normalized settlement line written during ingestion.
type SettlementRow struct {
	SettlementID  string
	EntityID      string
	LineType      string
	PaymentMethod string
	CreditPaise   int64
	DebitPaise    int64
	FeePaise      int64
	TaxPaise      int64
	SettledAt     time.Time
}

// BankRow is the normalized bank statement line written during ingestion.
type BankRow struct {
	ReferenceID string
	CreditPaise int64
	BookedAt    time.Time
}

// LedgerRow is the normalized merchant ledger line written during ingestion.
type LedgerRow struct {
	ReferenceID string
	AmountPaise int64
	BookedAt    time.Time
}

// Counts is a snapshot of ingested row totals used by tests and local verification.
type Counts struct {
	RawEvents   int
	Payments    int
	Settlements int
	BankLines   int
	LedgerLines int
}

// IngestStore writes raw events and normalized records inside one transaction.
type IngestStore interface {
	InTx(ctx context.Context, fn func(tx IngestStore) error) error
	InsertRawEvent(ctx context.Context, event RawEvent) error
	InsertPayment(ctx context.Context, payment PaymentRow) error
	InsertSettlement(ctx context.Context, line SettlementRow) error
	InsertBankLine(ctx context.Context, line BankRow) error
	InsertLedgerLine(ctx context.Context, line LedgerRow) error
	Count(ctx context.Context) (Counts, error)
}
