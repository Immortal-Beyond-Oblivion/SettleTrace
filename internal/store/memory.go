package store

import (
	"context"
	"encoding/json"
	"sync"
)

// MemoryStore is an in-process ingest store used by unit tests.
type MemoryStore struct {
	mu          sync.Mutex
	raw         map[string]RawEvent
	payments    map[string]PaymentRow
	settlements map[string]SettlementRow
	banks       map[string]BankRow
	ledgers     map[string]LedgerRow
	failAfter   int
	writes      int
}

// NewMemoryStore constructs an empty in-memory ingest store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		raw:         map[string]RawEvent{},
		payments:    map[string]PaymentRow{},
		settlements: map[string]SettlementRow{},
		banks:       map[string]BankRow{},
		ledgers:     map[string]LedgerRow{},
	}
}

// FailAfterNWrites forces the next write after n successful writes to error, then resets.
func (store *MemoryStore) FailAfterNWrites(n int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failAfter = n
	store.writes = 0
}

// InTx clones store state, applies fn, and commits only when fn returns nil.
func (store *MemoryStore) InTx(_ context.Context, fn func(tx IngestStore) error) error {
	store.mu.Lock()
	snapshot := store.cloneLocked()
	working := snapshot
	store.mu.Unlock()
	if err := fn(working); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.raw = working.raw
	store.payments = working.payments
	store.settlements = working.settlements
	store.banks = working.banks
	store.ledgers = working.ledgers
	store.writes = working.writes
	store.failAfter = working.failAfter
	return nil
}

// InsertRawEvent stores an immutable event keyed by idempotency key.
func (store *MemoryStore) InsertRawEvent(_ context.Context, event RawEvent) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.hitFaultLocked(); err != nil {
		return err
	}
	if _, exists := store.raw[event.IdempotencyKey]; exists {
		return ErrDuplicate
	}
	payload := append(json.RawMessage(nil), event.Payload...)
	event.Payload = payload
	store.raw[event.IdempotencyKey] = event
	return nil
}

// InsertPayment stores a payment keyed by payment_id.
func (store *MemoryStore) InsertPayment(_ context.Context, payment PaymentRow) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.hitFaultLocked(); err != nil {
		return err
	}
	if _, exists := store.payments[payment.PaymentID]; exists {
		return nil
	}
	store.payments[payment.PaymentID] = payment
	return nil
}

// InsertSettlement stores a settlement line keyed by settlement identity.
func (store *MemoryStore) InsertSettlement(_ context.Context, line SettlementRow) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.hitFaultLocked(); err != nil {
		return err
	}
	key := line.SettlementID + "|" + line.EntityID + "|" + line.LineType
	if _, exists := store.settlements[key]; exists {
		return nil
	}
	store.settlements[key] = line
	return nil
}

// InsertBankLine stores a bank line keyed by reference.
func (store *MemoryStore) InsertBankLine(_ context.Context, line BankRow) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.hitFaultLocked(); err != nil {
		return err
	}
	if _, exists := store.banks[line.ReferenceID]; exists {
		return nil
	}
	store.banks[line.ReferenceID] = line
	return nil
}

// InsertLedgerLine stores a ledger line keyed by reference.
func (store *MemoryStore) InsertLedgerLine(_ context.Context, line LedgerRow) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.hitFaultLocked(); err != nil {
		return err
	}
	if _, exists := store.ledgers[line.ReferenceID]; exists {
		return nil
	}
	store.ledgers[line.ReferenceID] = line
	return nil
}

// Count returns current in-memory ingest totals.
func (store *MemoryStore) Count(_ context.Context) (Counts, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return Counts{
		RawEvents:   len(store.raw),
		Payments:    len(store.payments),
		Settlements: len(store.settlements),
		BankLines:   len(store.banks),
		LedgerLines: len(store.ledgers),
	}, nil
}

// cloneLocked copies maps while the caller already holds the original lock or owns the instance.
func (store *MemoryStore) cloneLocked() *MemoryStore {
	clone := NewMemoryStore()
	clone.failAfter = store.failAfter
	clone.writes = store.writes
	for key, value := range store.raw {
		clone.raw[key] = value
	}
	for key, value := range store.payments {
		clone.payments[key] = value
	}
	for key, value := range store.settlements {
		clone.settlements[key] = value
	}
	for key, value := range store.banks {
		clone.banks[key] = value
	}
	for key, value := range store.ledgers {
		clone.ledgers[key] = value
	}
	return clone
}

// hitFaultLocked increments the write counter and returns a forced failure when configured.
func (store *MemoryStore) hitFaultLocked() error {
	if store.failAfter == 0 {
		return nil
	}
	store.writes++
	if store.writes > store.failAfter {
		return errForcedWrite
	}
	return nil
}

var errForcedWrite = errString("forced ingest write failure")

type errString string

// Error implements error for deterministic test faults.
func (err errString) Error() string { return string(err) }
