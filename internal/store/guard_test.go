package store

import "testing"

// TestGuardMutationRejectsHistoryUpdates verifies match and audit history cannot be updated in place.
func TestGuardMutationRejectsHistoryUpdates(t *testing.T) {
	if err := GuardMutation("UPDATE match_results SET confidence='EXACT' WHERE id=1"); err == nil {
		t.Fatal("expected append-only rejection")
	}
	if err := GuardMutation("DELETE FROM raw_events WHERE id=1"); err == nil {
		t.Fatal("expected append-only rejection")
	}
}

// TestGuardMutationAllowsIngestInserts verifies ordinary ingest inserts are permitted.
func TestGuardMutationAllowsIngestInserts(t *testing.T) {
	if err := GuardMutation("INSERT INTO raw_events (source) VALUES ('webhook')"); err != nil {
		t.Fatalf("expected insert to pass: %v", err)
	}
	if err := GuardMutation("UPDATE batch_queue SET status='claimed' WHERE id=1"); err != nil {
		t.Fatalf("expected batch_queue update to pass: %v", err)
	}
}
