package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/schema"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

// Status is the caller-visible outcome of one ingest attempt.
type Status string

const (
	// StatusApplied means new rows were committed.
	StatusApplied Status = "applied"
	// StatusDuplicate means the payload was already persisted.
	StatusDuplicate Status = "duplicate"
	// StatusRejected means the payload must not be persisted or retried as valid data.
	StatusRejected Status = "rejected"
)

// Envelope is one source payload plus optional webhook authentication material.
type Envelope struct {
	Source       string
	Body         []byte
	SignatureHex string
	HMACSecret   string
	ObjectKey    string
}

// Result reports whether the caller should acknowledge a queue message.
type Result struct {
	Status  Status
	Reason  string
	Applied int
}

// ShouldAck reports whether a queue consumer may delete the message.
func (result Result) ShouldAck() bool {
	return result.Status == StatusApplied || result.Status == StatusDuplicate
}

// Pipeline validates, authenticates, and transactionally persists source payloads.
type Pipeline struct {
	Store  store.IngestStore
	Cache  DuplicateCache
	Clock  recon.Clock
	Secret string
}

// Process validates a payload, rejects invalid webhooks before writes, and commits atomically.
func (pipeline Pipeline) Process(ctx context.Context, envelope Envelope) (Result, error) {
	if pipeline.Cache == nil {
		pipeline.Cache = NoopCache{}
	}
	if pipeline.Clock == nil {
		pipeline.Clock = recon.UTCClock{}
	}
	if envelope.Source == schema.SourceWebhook {
		secret := envelope.HMACSecret
		if secret == "" {
			secret = pipeline.Secret
		}
		if !VerifyHMAC(envelope.Body, envelope.SignatureHex, secret) {
			return Result{Status: StatusRejected, Reason: ErrInvalidSignature.Error()}, ErrInvalidSignature
		}
	}
	records, err := Parse(envelope.Source, envelope.Body)
	if err != nil {
		return Result{Status: StatusRejected, Reason: err.Error()}, err
	}
	applied := 0
	duplicates := 0
	for _, record := range records {
		outcome, err := pipeline.persist(ctx, record)
		if err != nil {
			return Result{Status: StatusRejected, Reason: err.Error()}, err
		}
		if outcome == StatusApplied {
			applied++
		} else {
			duplicates++
		}
	}
	if applied == 0 && duplicates > 0 {
		return Result{Status: StatusDuplicate, Applied: 0}, nil
	}
	return Result{Status: StatusApplied, Applied: applied}, nil
}

// persist writes one record in a single transaction and marks the cache only after commit.
func (pipeline Pipeline) persist(ctx context.Context, record Record) (Status, error) {
	seen, err := pipeline.Cache.Seen(ctx, record.IdempotencyKey)
	if err == nil && seen {
		return StatusDuplicate, nil
	}
	err = pipeline.Store.InTx(ctx, func(tx store.IngestStore) error {
		rawErr := tx.InsertRawEvent(ctx, store.RawEvent{
			Source:         record.Source,
			ExternalID:     record.ExternalID,
			EventType:      record.EventType,
			IdempotencyKey: record.IdempotencyKey,
			Payload:        record.Payload,
			ReceivedAt:     pipeline.Clock.Now().UTC(),
		})
		if rawErr != nil {
			return rawErr
		}
		return insertNormalized(ctx, tx, record)
	})
	if errors.Is(err, store.ErrDuplicate) {
		_ = pipeline.Cache.Mark(ctx, record.IdempotencyKey)
		return StatusDuplicate, nil
	}
	if err != nil {
		return "", err
	}
	if auditErr := pipeline.writeIngestAudit(ctx, record); auditErr != nil {
		return "", fmt.Errorf("write ingest audit entry: %w", auditErr)
	}
	_ = pipeline.Cache.Mark(ctx, record.IdempotencyKey)
	return StatusApplied, nil
}

// writeIngestAudit appends a hash-chained audit_log entry for one committed raw event, closing
// the gap state.md previously flagged: the matching engine's writes were audited (via
// ReconStore.WriteAuditEntry), but ingestion's writes were not, so the append-only audit
// guarantee did not actually cover the point where data first enters the system. This is a
// no-op, not an error, when the configured store does not support audit writes (for example
// MemoryStore, the in-process fake used by ingestion's unit tests), since audit coverage is a
// MySQLStore capability, not a contract every IngestStore implementation must satisfy.
func (pipeline Pipeline) writeIngestAudit(ctx context.Context, record Record) error {
	auditor, ok := pipeline.Store.(store.AuditWriter)
	if !ok {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"source":          record.Source,
		"external_id":     record.ExternalID,
		"event_type":      record.EventType,
		"idempotency_key": record.IdempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("marshal ingest audit payload: %w", err)
	}
	return auditor.WriteAuditEntry(ctx, store.AuditEntryRow{
		EventType:   "RAW_EVENT_INGESTED",
		PayloadJSON: payload,
		CreatedAt:   pipeline.Clock.Now().UTC(),
	})
}

// insertNormalized writes the typed row that belongs to a validated record.
func insertNormalized(ctx context.Context, tx store.IngestStore, record Record) error {
	switch {
	case record.Payment != nil:
		return tx.InsertPayment(ctx, *record.Payment)
	case record.Settlement != nil:
		return tx.InsertSettlement(ctx, *record.Settlement)
	case record.Bank != nil:
		return tx.InsertBankLine(ctx, *record.Bank)
	case record.Ledger != nil:
		return tx.InsertLedgerLine(ctx, *record.Ledger)
	default:
		return errors.New("record produced no normalized row")
	}
}
