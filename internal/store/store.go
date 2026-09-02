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

// AuditWriter is satisfied by any store that can append a hash-chained audit_log row.
// It is kept separate from IngestStore on purpose: MySQLStore implements it because it
// also implements ReconStore's WriteAuditEntry, but MemoryStore (the in-process fake used
// by ingestion's unit tests) does not, and should not be forced to. A caller holding only
// an IngestStore can type-assert against this interface and treat a miss as "no audit
// coverage available," never as an error.
type AuditWriter interface {
	WriteAuditEntry(ctx context.Context, entry AuditEntryRow) error
}

// PaymentCandidate is a payment loaded for reconciliation matching.
type PaymentCandidate struct {
	PaymentID   string
	OrderID     string
	AmountPaise int64
	FeePaise    int64
	TaxPaise    int64
	Method      string
	CapturedAt  time.Time
}

// SettlementCandidate is a settlement line loaded as a possible match target.
type SettlementCandidate struct {
	ID          int64
	EntityID    string
	CreditPaise int64
	Method      string
	SettledAt   time.Time
}

// LedgerCandidate is an unmatched ledger line loaded for Tier L matching.
type LedgerCandidate struct {
	ID          int64
	ReferenceID string
	AmountPaise int64
	BookedAt    time.Time
}

// MatchResultRow is an append-only reconciliation match ready for persistence.
type MatchResultRow struct {
	MatchGroupID string
	RecordType   string
	RecordID     string
	Confidence   string
	RuleID       string
	EvidenceJSON json.RawMessage
	CreatedAt    time.Time
}

// ExceptionRow is an unresolved reconciliation exception ready for persistence.
type ExceptionRow struct {
	RecordType        string
	RecordID          string
	ReasonCode        string
	AmountAtRiskPaise int64
	EvidenceJSON      json.RawMessage
	CreatedAt         time.Time
}

// AuditEntryRow is one hash-chained audit event awaiting its predecessor hash.
type AuditEntryRow struct {
	EventType   string
	PayloadJSON json.RawMessage
	CreatedAt   time.Time
}

// AIExplanationLogRow is one persisted record of an AI explainer invocation, written whether
// or not the underlying LLM call succeeded. implementation.md section 8/section 24 requires
// this: "every AI-generated sentence is stored, permanently," including a failed or
// budget/circuit-skipped attempt, so a reviewer can always see exactly what was asked, what
// (if anything) came back, and why.
type AIExplanationLogRow struct {
	ExceptionID      int64
	PromptVersion    string
	Model            string
	InputSummaryJSON json.RawMessage
	OutputText       string
	LatencyMS        int64
	Succeeded        bool
	ErrorMessage     string
	CreatedAt        time.Time
}

// ErrExceptionNotFound is returned by GetExceptionByID when no exception_log row matches --
// callers (internal/api's explain handler) map this to a 404, distinct from a genuine query
// failure, which maps to a 500.
var ErrExceptionNotFound = errors.New("exception not found")

// ExceptionRecord is one persisted exception_log row, returned by ID. Kept separate from
// ExceptionRow (the write-side shape the matching engine appends) because a read needs the
// row's own primary key and resolution state, neither of which the writer ever supplies, and
// giving the read path its own type keeps ExceptionRow's "this is an insert-only shape"
// contract honest.
type ExceptionRecord struct {
	ID                int64
	RecordType        string
	RecordID          string
	ReasonCode        string
	AmountAtRiskPaise int64
	EvidenceJSON      json.RawMessage
	ResolvedAt        *time.Time
	CreatedAt         time.Time
}

// ExceptionReader is satisfied by any store that can look up one exception_log row by its
// primary key. Kept separate from ReconStore, mirroring AIExplanationLogWriter's reasoning
// below: the API's explain handler only ever needs this one narrow read and should not be
// handed the rest of ReconStore's write surface (WriteMatchResult, SetLedgerMatchedPayment,
// etc.) -- there is no code path through this interface that could let the AI-adjacent HTTP
// layer mutate reconciliation state, matching architecture.md section 9's read-only boundary
// at the Go-interface level, not just the database-grant level (README section 11/23).
type ExceptionReader interface {
	GetExceptionByID(ctx context.Context, id int64) (ExceptionRecord, error)
}

// AIExplanationLogWriter is satisfied by any store that can persist an ai_explanation_log
// row. Kept separate from ReconStore, mirroring AuditWriter's reasoning above: the AI layer
// only ever needs this one narrow write capability and should not be handed the rest of
// ReconStore's surface -- in particular, nothing in internal/ai can call WriteMatchResult or
// SetLedgerMatchedPayment. The read-only-DB-grant boundary implementation.md section 11/23
// describes is enforced at the database-user level in prod, and mirrored here at the
// Go-interface level so a caller can't even compile a path that hands the AI layer more than
// this.
type AIExplanationLogWriter interface {
	WriteAIExplanationLog(ctx context.Context, log AIExplanationLogRow) error
}

// ReconStore reads matching candidates and persists deterministic reconciliation outcomes.
// It is kept separate from IngestStore because ingestion and matching have different
// failure domains and different callers: the matching engine never ingests, and the
// ingestion worker never decides a match.
type ReconStore interface {
	// GetUnmatchedPaymentsInWindow returns captured payments in [start, end) that have
	// neither a match_results row nor an unresolved exception_log row yet.
	GetUnmatchedPaymentsInWindow(ctx context.Context, start, end time.Time) ([]PaymentCandidate, error)
	// GetPaymentsInWindow returns all captured payments in [start, end), regardless of
	// match status, for use as Tier L candidates on the ledger side.
	GetPaymentsInWindow(ctx context.Context, start, end time.Time) ([]PaymentCandidate, error)
	// GetSettlementCandidates returns settlement lines for one method settled in [start, end).
	GetSettlementCandidates(ctx context.Context, method string, start, end time.Time) ([]SettlementCandidate, error)
	// GetUnmatchedLedgerLines returns ledger lines with no matched_payment_id yet.
	GetUnmatchedLedgerLines(ctx context.Context) ([]LedgerCandidate, error)
	// WriteMatchResult appends a reconciliation match; there is no corresponding update method.
	WriteMatchResult(ctx context.Context, match MatchResultRow) error
	// WriteExceptionLog appends an unresolved reconciliation exception.
	WriteExceptionLog(ctx context.Context, exception ExceptionRow) error
	// WriteAuditEntry chains and appends one audit_log row from the previous row's hash.
	WriteAuditEntry(ctx context.Context, entry AuditEntryRow) error
	// SetLedgerMatchedPayment records the payment a ledger line resolved to, once, idempotently.
	SetLedgerMatchedPayment(ctx context.Context, ledgerLineID int64, paymentID string) error
}
