// Package recon defines the stable domain types for reconciliation.
package recon

import "time"

// Confidence describes the evidence strength of a proposed reconciliation result.
type Confidence string

const (
	// ConfidenceExact marks an evidence-complete deterministic result.
	ConfidenceExact Confidence = "EXACT"
	// ConfidenceHigh marks a bounded deterministic recovery result.
	ConfidenceHigh Confidence = "HIGH"
	// ConfidenceAdvisory marks a rank that requires human resolution.
	ConfidenceAdvisory Confidence = "ADVISORY_ONLY"
)

const (
	// ReasonNoCandidate records a missing settlement candidate inside the allowed window.
	ReasonNoCandidate = "NO_CANDIDATE_IN_WINDOW"
	// ReasonAmbiguous records multiple equally plausible candidates.
	ReasonAmbiguous = "MULTI_CANDIDATE_AMBIGUOUS"
	// ReasonAmountMismatch records an unexplained financial difference.
	ReasonAmountMismatch = "AMOUNT_MISMATCH_UNEXPLAINED"
	// ReasonLedgerMismatch records a ledger reference with an incorrect gross amount.
	ReasonLedgerMismatch = "LEDGER_AMOUNT_MISMATCH"
	// ReasonLedgerOrphan records a ledger row without a payment candidate.
	ReasonLedgerOrphan = "LEDGER_ORPHAN"
)

// Payment is the normalized gross gateway payment record.
type Payment struct {
	ID            string
	OrderID       string
	AmountPaise   int64
	FeePaise      int64
	TaxPaise      int64
	Method        string
	CapturedAt    time.Time
	SettlementSLA time.Duration
}

// NetAmount returns the expected net settlement credit for a payment.
func (p Payment) NetAmount() int64 { return p.AmountPaise - p.FeePaise - p.TaxPaise }

// SettlementLine is the normalized settlement report line.
type SettlementLine struct {
	ID           string
	SettlementID string
	EntityID     string
	CreditPaise  int64
	Method       string
	SettledAt    time.Time
}

// LedgerLine is the normalized merchant-booked gross payment record.
type LedgerLine struct {
	ID          string
	ReferenceID string
	AmountPaise int64
	BookedAt    time.Time
}

// MatchResult records a deterministic or advisory association and its evidence.
type MatchResult struct {
	PaymentID    string
	CandidateID  string
	Confidence   Confidence
	RuleID       string
	Evidence     map[string]any
	LedgerLineID string
}

// Exception records an unresolved financial record and machine-readable reason.
type Exception struct {
	RecordType        string
	RecordID          string
	ReasonCode        string
	AmountAtRiskPaise int64
	Evidence          map[string]any
}
