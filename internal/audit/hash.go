// Package audit provides append-only hash-chain utilities for reconciliation history.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Entry is a serializable audit event with its predecessor hash.
type Entry struct {
	EventType    string
	Payload      any
	PreviousHash string
	CreatedAt    time.Time
	RowHash      string
}

// Seal calculates and assigns a deterministic hash for an audit entry.
func Seal(entry Entry) (Entry, error) {
	payload, err := json.Marshal(entry.Payload)
	if err != nil {
		return Entry{}, fmt.Errorf("marshal audit payload: %w", err)
	}
	value := entry.PreviousHash + "|" + entry.EventType + "|" + entry.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + string(payload)
	sum := sha256.Sum256([]byte(value))
	entry.RowHash = hex.EncodeToString(sum[:])
	return entry, nil
}

// Verify checks ordering, predecessor linkage, and row hashes for every audit entry.
func Verify(entries []Entry) (int, error) {
	previous := ""
	for index, entry := range entries {
		if entry.PreviousHash != previous {
			return index, fmt.Errorf("previous hash mismatch")
		}
		sealed, err := Seal(Entry{EventType: entry.EventType, Payload: entry.Payload, PreviousHash: entry.PreviousHash, CreatedAt: entry.CreatedAt})
		if err != nil {
			return index, err
		}
		if sealed.RowHash != entry.RowHash {
			return index, fmt.Errorf("row hash mismatch")
		}
		previous = entry.RowHash
	}
	return -1, nil
}
