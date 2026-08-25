package audit

import (
	"testing"
	"time"
)

// TestVerifyDetectsTampering verifies a mutated audit payload breaks the hash chain.
func TestVerifyDetectsTampering(t *testing.T) {
	first, err := Seal(Entry{EventType: "MATCH", Payload: map[string]string{"id": "one"}, CreatedAt: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Seal(Entry{EventType: "MATCH", Payload: map[string]string{"id": "two"}, PreviousHash: first.RowHash, CreatedAt: time.Date(2026, 8, 25, 0, 1, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	second.Payload = map[string]string{"id": "tampered"}
	if index, err := Verify([]Entry{first, second}); err == nil || index != 1 {
		t.Fatalf("expected tampering at index 1, got index=%d err=%v", index, err)
	}
}
