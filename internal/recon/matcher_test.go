package recon

import (
	"testing"
	"time"
)

// TestTier1MatchesDerivedNetAmount verifies Tier 1 uses net settlement credit rather than gross amount.
func TestTier1MatchesDerivedNetAmount(t *testing.T) {
	capturedAt := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	payment := Payment{ID: "pay_1", AmountPaise: 100000, FeePaise: 2000, TaxPaise: 360, Method: "card", CapturedAt: capturedAt, SettlementSLA: 5 * 24 * time.Hour}
	candidate := SettlementLine{ID: "line_1", EntityID: "pay_1", CreditPaise: 97640, SettledAt: capturedAt.Add(24 * time.Hour)}
	if result, ok := (Tier1{}).TryMatch(payment, []SettlementLine{candidate}); !ok || result.Confidence != ConfidenceExact {
		t.Fatalf("expected exact match, got %#v ok=%v", result, ok)
	}
}

// TestTier1RejectsLateCandidate verifies Tier 1 respects the configured SLA window.
func TestTier1RejectsLateCandidate(t *testing.T) {
	capturedAt := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	payment := Payment{ID: "pay_1", AmountPaise: 100, CapturedAt: capturedAt, SettlementSLA: 24 * time.Hour}
	candidate := SettlementLine{ID: "line_1", EntityID: "pay_1", CreditPaise: 100, SettledAt: capturedAt.Add(48 * time.Hour)}
	if _, ok := (Tier1{}).TryMatch(payment, []SettlementLine{candidate}); ok {
		t.Fatal("expected late candidate rejection")
	}
}

// TestTier2RejectsAmbiguousCandidates verifies bounded recovery never chooses between competing candidates.
func TestTier2RejectsAmbiguousCandidates(t *testing.T) {
	capturedAt := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	payment := Payment{ID: "pay_123", AmountPaise: 100, CapturedAt: capturedAt, SettlementSLA: 24 * time.Hour}
	candidates := []SettlementLine{{ID: "one", EntityID: "pay_124", CreditPaise: 100, SettledAt: capturedAt}, {ID: "two", EntityID: "pay_125", CreditPaise: 100, SettledAt: capturedAt}}
	if _, ok := (Tier2{}).TryMatch(payment, candidates); ok {
		t.Fatal("expected ambiguous candidate rejection")
	}
}

// TestTierLClassifiesGrossMismatch verifies ledger matching does not reuse net settlement logic.
func TestTierLClassifiesGrossMismatch(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	ledger := LedgerLine{ID: "ledger_1", ReferenceID: "order_1", AmountPaise: 999, BookedAt: now}
	payment := Payment{ID: "pay_1", OrderID: "order_1", AmountPaise: 1000, CapturedAt: now}
	if exception := (TierL{}).Classify(ledger, []Payment{payment}); exception.ReasonCode != ReasonLedgerMismatch {
		t.Fatalf("expected ledger mismatch, got %s", exception.ReasonCode)
	}
}
