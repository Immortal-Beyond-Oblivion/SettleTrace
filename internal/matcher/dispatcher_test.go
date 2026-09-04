package matcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

// fakeReconStore is a hand-rolled in-memory ReconStore, following the same
// no-mocking-library convention store.MemoryStore already uses for ingestion tests.
type fakeReconStore struct {
	payments      []store.PaymentCandidate
	settlements   []store.SettlementCandidate
	ledgerLines   []store.LedgerCandidate
	matches       []store.MatchResultRow
	exceptions    []store.ExceptionRow
	auditEntries  []store.AuditEntryRow
	matchedLedger map[int64]string
}

// newFakeReconStore constructs an empty fake store ready for a single test's fixtures.
func newFakeReconStore() *fakeReconStore {
	return &fakeReconStore{matchedLedger: map[int64]string{}}
}

// GetUnmatchedPaymentsInWindow returns fixture payments not already matched or exceptioned.
func (fake *fakeReconStore) GetUnmatchedPaymentsInWindow(_ context.Context, start, end time.Time) ([]store.PaymentCandidate, error) {
	out := make([]store.PaymentCandidate, 0)
	for _, payment := range fake.payments {
		if payment.CapturedAt.Before(start) || !payment.CapturedAt.Before(end) {
			continue
		}
		if fake.hasOutcome(payment.PaymentID) {
			continue
		}
		out = append(out, payment)
	}
	return out, nil
}

// GetPaymentsInWindow returns every fixture payment captured in the window, regardless of outcome.
func (fake *fakeReconStore) GetPaymentsInWindow(_ context.Context, start, end time.Time) ([]store.PaymentCandidate, error) {
	out := make([]store.PaymentCandidate, 0)
	for _, payment := range fake.payments {
		if payment.CapturedAt.Before(start) || !payment.CapturedAt.Before(end) {
			continue
		}
		out = append(out, payment)
	}
	return out, nil
}

// GetSettlementCandidates returns fixture settlement lines for one method inside the window.
func (fake *fakeReconStore) GetSettlementCandidates(_ context.Context, method string, start, end time.Time) ([]store.SettlementCandidate, error) {
	out := make([]store.SettlementCandidate, 0)
	for _, candidate := range fake.settlements {
		if candidate.Method != method {
			continue
		}
		if candidate.SettledAt.Before(start) || !candidate.SettledAt.Before(end) {
			continue
		}
		out = append(out, candidate)
	}
	return out, nil
}

// GetUnmatchedLedgerLines returns fixture ledger lines with no recorded matched payment,
// booked within [start, end) -- mirroring MySQLStore's new window-bounded query so tests
// exercise the same contract the real store now enforces.
func (fake *fakeReconStore) GetUnmatchedLedgerLines(_ context.Context, start, end time.Time) ([]store.LedgerCandidate, error) {
	out := make([]store.LedgerCandidate, 0)
	for _, line := range fake.ledgerLines {
		if _, matched := fake.matchedLedger[line.ID]; matched {
			continue
		}
		if line.BookedAt.Before(start) || !line.BookedAt.Before(end) {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

// WriteMatchResult records one match result in the fake store's slice.
func (fake *fakeReconStore) WriteMatchResult(_ context.Context, match store.MatchResultRow) error {
	fake.matches = append(fake.matches, match)
	return nil
}

// WriteExceptionLog records one exception in the fake store's slice.
func (fake *fakeReconStore) WriteExceptionLog(_ context.Context, exception store.ExceptionRow) error {
	fake.exceptions = append(fake.exceptions, exception)
	return nil
}

// WriteAuditEntry records one audit entry in the fake store's slice without real hash chaining.
func (fake *fakeReconStore) WriteAuditEntry(_ context.Context, entry store.AuditEntryRow) error {
	fake.auditEntries = append(fake.auditEntries, entry)
	return nil
}

// SetLedgerMatchedPayment records which payment a ledger line resolved to, once.
func (fake *fakeReconStore) SetLedgerMatchedPayment(_ context.Context, ledgerLineID int64, paymentID string) error {
	if _, exists := fake.matchedLedger[ledgerLineID]; exists {
		return nil
	}
	fake.matchedLedger[ledgerLineID] = paymentID
	return nil
}

// hasOutcome reports whether a payment already has a match or an exception recorded.
func (fake *fakeReconStore) hasOutcome(paymentID string) bool {
	for _, match := range fake.matches {
		if match.RecordType == "payment" && match.RecordID == paymentID {
			return true
		}
	}
	for _, exception := range fake.exceptions {
		if exception.RecordType == "payment" && exception.RecordID == paymentID {
			return true
		}
	}
	return false
}

// fixedClock returns a constant timestamp, keeping match/exception timing deterministic in tests.
type fixedClock struct{ at time.Time }

// Now returns the fixed timestamp this clock was constructed with.
func (clock fixedClock) Now() time.Time { return clock.at }


// TestEngine_RunWindow_Tier1ExactMatchIsRecorded verifies that a payment and settlement candidate that match exactly are recorded as a match.
func TestEngine_RunWindow_Tier1ExactMatchIsRecorded(t *testing.T) {
	capturedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	fake := newFakeReconStore()
	fake.payments = []store.PaymentCandidate{
		{PaymentID: "pay_1", AmountPaise: 100000, FeePaise: 2900, TaxPaise: 0, Method: "card", CapturedAt: capturedAt},
	}
	fake.settlements = []store.SettlementCandidate{
		{ID: 1, EntityID: "pay_1", CreditPaise: 97100, Method: "card", SettledAt: capturedAt.Add(24 * time.Hour)},
	}
	engine := NewEngine(fake, fixedClock{at: capturedAt.Add(48 * time.Hour)})

	report, err := engine.RunWindow(context.Background(), capturedAt.Add(-time.Hour), capturedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("RunWindow returned error: %v", err)
	}
	if report.MatchedByConfidence["EXACT"] != 1 {
		t.Fatalf("expected one EXACT match, got report %+v", report.MatchedByConfidence)
	}
	if len(fake.matches) != 1 || fake.matches[0].RecordID != "pay_1" {
		t.Fatalf("expected one persisted match for pay_1, got %+v", fake.matches)
	}
	if len(fake.auditEntries) != 1 {
		t.Fatalf("expected one audit entry for the match, got %d", len(fake.auditEntries))
	}
}


// TestEngine_RunWindow_NoCandidateProducesException verifies that a payment with no settlement candidates produces an exception.
func TestEngine_RunWindow_NoCandidateProducesException(t *testing.T) {
	capturedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	fake := newFakeReconStore()
	fake.payments = []store.PaymentCandidate{
		{PaymentID: "pay_2", AmountPaise: 50000, Method: "upi", CapturedAt: capturedAt},
	}
	engine := NewEngine(fake, fixedClock{at: capturedAt})

	report, err := engine.RunWindow(context.Background(), capturedAt.Add(-time.Hour), capturedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("RunWindow returned error: %v", err)
	}
	if report.ExceptionsByReason["NO_CANDIDATE_IN_WINDOW"] != 1 {
		t.Fatalf("expected one NO_CANDIDATE_IN_WINDOW exception, got %+v", report.ExceptionsByReason)
	}
	if len(fake.exceptions) != 1 || fake.exceptions[0].RecordID != "pay_2" {
		t.Fatalf("expected one persisted exception for pay_2, got %+v", fake.exceptions)
	}
}

// TestEngine_RunWindow_LedgerExactMatchSetsMatchedPayment verifies that a ledger line that matches a payment exactly is recorded as matched, even if the payment has no settlement candidates.
func TestEngine_RunWindow_LedgerExactMatchSetsMatchedPayment(t *testing.T) {
	capturedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	fake := newFakeReconStore()
	fake.payments = []store.PaymentCandidate{
		{PaymentID: "pay_3", OrderID: "order_3", AmountPaise: 75000, Method: "card", CapturedAt: capturedAt},
	}
	fake.ledgerLines = []store.LedgerCandidate{
		{ID: 9, ReferenceID: "order_3", AmountPaise: 75000, BookedAt: capturedAt},
	}
	engine := NewEngine(fake, fixedClock{at: capturedAt})

	report, err := engine.RunWindow(context.Background(), capturedAt.Add(-time.Hour), capturedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("RunWindow returned error: %v", err)
	}
	// pay_3 has no settlement candidates at all, so the settlement side logs a
	// NO_CANDIDATE_IN_WINDOW exception for it; the ledger side separately resolves
	// ledger line 9 against the same payment, since the two sides run independently.
	if report.MatchedByConfidence["EXACT"] != 1 {
		t.Fatalf("expected exactly one EXACT match from the ledger side, got %+v", report.MatchedByConfidence)
	}
	if report.ExceptionsByReason["NO_CANDIDATE_IN_WINDOW"] != 1 {
		t.Fatalf("expected the settlement side to log pay_3 as unresolved, got %+v", report.ExceptionsByReason)
	}
	if fake.matchedLedger[9] != "pay_3" {
		t.Fatalf("expected ledger line 9 to resolve to pay_3, got %q", fake.matchedLedger[9])
	}
}


// TestEngine_RunWindow_Tier3RanksAmbiguous
func TestEngine_RunWindow_Tier3RanksAmbiguousPaymentWhenConfigured(t *testing.T) {
	capturedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	fake := newFakeReconStore()
	fake.payments = []store.PaymentCandidate{
		{PaymentID: "pay_5", AmountPaise: 100000, FeePaise: 2900, TaxPaise: 0, Method: "card", CapturedAt: capturedAt},
	}
	fake.settlements = []store.SettlementCandidate{
		// Deliberately mismatched credit, so Tier1 and Tier2 both reject it and the payment
		// falls through to the Tier 3 decision point this test exists to exercise.
		{ID: 1, EntityID: "pay_5", CreditPaise: 50000, Method: "card", SettledAt: capturedAt.Add(24 * time.Hour)},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode([]map[string]any{
			{"id": "1", "score": 0.02, "features": map[string]any{"amount_distance_paise": 47100}},
		})
	}))
	defer server.Close()

	engine := NewEngine(fake, fixedClock{at: capturedAt.Add(48 * time.Hour)})
	engine.Tier3 = NewTier3Client(server.URL)

	report, err := engine.RunWindow(context.Background(), capturedAt.Add(-time.Hour), capturedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("RunWindow returned error: %v", err)
	}
	if report.ExceptionsByReason["MULTI_CANDIDATE_AMBIGUOUS"] != 1 {
		t.Fatalf("expected one MULTI_CANDIDATE_AMBIGUOUS exception from tier3, got %+v", report.ExceptionsByReason)
	}
	if len(fake.exceptions) != 1 {
		t.Fatalf("expected exactly one persisted exception, got %d", len(fake.exceptions))
	}
	var evidence map[string]any
	if err := json.Unmarshal(fake.exceptions[0].EvidenceJSON, &evidence); err != nil {
		t.Fatalf("decode exception evidence: %v", err)
	}
	if evidence["source"] != "tier3_fuzzy_ranker" {
		t.Fatalf("expected tier3 evidence source, got %+v", evidence)
	}
	if _, ok := evidence["ranked_candidates"]; !ok {
		t.Fatalf("expected ranked_candidates in tier3 evidence, got %+v", evidence)
	}
}


// TestEngine_RunWindow_Tier3FailureDegradesToPlainException verifies that if the Tier3 service is unavailable, the engine still produces a plain exception and does not fail the batch.
func TestEngine_RunWindow_Tier3FailureDegradesToPlainException(t *testing.T) {
	capturedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	fake := newFakeReconStore()
	fake.payments = []store.PaymentCandidate{
		{PaymentID: "pay_6", AmountPaise: 100000, FeePaise: 2900, TaxPaise: 0, Method: "card", CapturedAt: capturedAt},
	}
	fake.settlements = []store.SettlementCandidate{
		{ID: 2, EntityID: "pay_6", CreditPaise: 50000, Method: "card", SettledAt: capturedAt.Add(24 * time.Hour)},
	}
	// A server that always errors stands in for "the fuzzy-ranker is unavailable": the
	// engine must still produce the plain, pre-Tier3 exception, and must not fail the batch.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	engine := NewEngine(fake, fixedClock{at: capturedAt.Add(48 * time.Hour)})
	engine.Tier3 = NewTier3Client(server.URL)

	report, err := engine.RunWindow(context.Background(), capturedAt.Add(-time.Hour), capturedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("RunWindow returned error despite tier3 being unavailable: %v", err)
	}
	if report.ExceptionsByReason["AMOUNT_MISMATCH_UNEXPLAINED"] != 1 {
		t.Fatalf("expected the plain pre-tier3 exception to fire, got %+v", report.ExceptionsByReason)
	}
}
