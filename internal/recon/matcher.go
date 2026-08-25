package recon

import (
	"strings"
	"time"
)

// Tier1 matches only exact identity, exact net amount, and valid settlement windows.
type Tier1 struct{}

// TryMatch returns an exact result when exactly one candidate satisfies all Tier 1 evidence.
func (Tier1) TryMatch(payment Payment, candidates []SettlementLine) (MatchResult, bool) {
	matches := make([]SettlementLine, 0, 1)
	for _, candidate := range candidates {
		if candidate.EntityID != payment.ID || candidate.CreditPaise != payment.NetAmount() {
			continue
		}
		if !inFutureWindow(payment.CapturedAt, candidate.SettledAt, payment.SettlementSLA) {
			continue
		}
		matches = append(matches, candidate)
	}
	if len(matches) != 1 {
		return MatchResult{}, false
	}
	return MatchResult{PaymentID: payment.ID, CandidateID: matches[0].ID, Confidence: ConfidenceExact, RuleID: "TIER1_EXACT", Evidence: map[string]any{"expected_net_paise": payment.NetAmount()}}, true
}

// Tier2 performs deterministic identifier recovery only inside a narrowed candidate set.
type Tier2 struct{}

// TryMatch returns a high-confidence result only when one candidate is uniquely bounded by evidence.
func (Tier2) TryMatch(payment Payment, candidates []SettlementLine) (MatchResult, bool) {
	matches := make([]SettlementLine, 0, 1)
	for _, candidate := range candidates {
		if candidate.CreditPaise != payment.NetAmount() || !inFutureWindow(payment.CapturedAt, candidate.SettledAt, payment.SettlementSLA) {
			continue
		}
		if levenshtein(normalize(payment.ID), normalize(candidate.EntityID)) <= 2 {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return MatchResult{}, false
	}
	return MatchResult{PaymentID: payment.ID, CandidateID: matches[0].ID, Confidence: ConfidenceHigh, RuleID: "TIER2_BOUNDED_ID", Evidence: map[string]any{"distance_lte": 2}}, true
}

// TierL matches merchant ledger rows to gross payment amounts.
type TierL struct{}

// TryExactMatch returns a ledger result when one payment shares reference, gross amount, and booking window.
func (TierL) TryExactMatch(ledger LedgerLine, payments []Payment) (MatchResult, bool) {
	matches := make([]Payment, 0, 1)
	for _, payment := range payments {
		if ledger.ReferenceID == payment.OrderID && ledger.AmountPaise == payment.AmountPaise && withinDays(ledger.BookedAt, payment.CapturedAt, 3) {
			matches = append(matches, payment)
		}
	}
	if len(matches) != 1 {
		return MatchResult{}, false
	}
	return MatchResult{PaymentID: matches[0].ID, LedgerLineID: ledger.ID, Confidence: ConfidenceExact, RuleID: "TIERL_GROSS_EXACT", Evidence: map[string]any{"gross_amount_paise": ledger.AmountPaise}}, true
}

// Classify returns the most specific ledger exception that available evidence supports.
func (TierL) Classify(ledger LedgerLine, payments []Payment) Exception {
	for _, payment := range payments {
		if ledger.ReferenceID == payment.OrderID && withinDays(ledger.BookedAt, payment.CapturedAt, 3) {
			return Exception{RecordType: "ledger", RecordID: ledger.ID, ReasonCode: ReasonLedgerMismatch, AmountAtRiskPaise: ledger.AmountPaise, Evidence: map[string]any{"payment_id": payment.ID, "expected_gross_paise": payment.AmountPaise}}
		}
	}
	return Exception{RecordType: "ledger", RecordID: ledger.ID, ReasonCode: ReasonLedgerOrphan, AmountAtRiskPaise: ledger.AmountPaise, Evidence: map[string]any{}}
}

// ClassifyPayment returns a transparent payment exception after deterministic tiers reject it.
func ClassifyPayment(payment Payment, candidates []SettlementLine) Exception {
	count := 0
	for _, candidate := range candidates {
		if candidate.CreditPaise == payment.NetAmount() && inFutureWindow(payment.CapturedAt, candidate.SettledAt, payment.SettlementSLA) {
			count++
		}
	}
	if count > 1 {
		return Exception{RecordType: "payment", RecordID: payment.ID, ReasonCode: ReasonAmbiguous, AmountAtRiskPaise: payment.AmountPaise, Evidence: map[string]any{"candidate_count": count}}
	}
	if len(candidates) == 0 {
		return Exception{RecordType: "payment", RecordID: payment.ID, ReasonCode: ReasonNoCandidate, AmountAtRiskPaise: payment.AmountPaise, Evidence: map[string]any{}}
	}
	return Exception{RecordType: "payment", RecordID: payment.ID, ReasonCode: ReasonAmountMismatch, AmountAtRiskPaise: payment.AmountPaise, Evidence: map[string]any{"expected_net_paise": payment.NetAmount()}}
}

// inFutureWindow checks a settlement timestamp against the payment-specific future SLA.
func inFutureWindow(capturedAt, settledAt time.Time, sla time.Duration) bool {
	return !settledAt.Before(capturedAt) && !settledAt.After(capturedAt.Add(sla))
}

// withinDays checks an absolute timestamp difference using whole calendar-duration days.
func withinDays(left, right time.Time, days int) bool {
	difference := left.Sub(right)
	if difference < 0 {
		difference = -difference
	}
	return difference <= time.Duration(days)*24*time.Hour
}

// normalize removes formatting differences before bounded identifier comparison.
func normalize(value string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(value))
}

// levenshtein calculates edit distance for short, already narrowed identifiers.
func levenshtein(left, right string) int {
	previous := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex, leftRune := range left {
		current := make([]int, len(right)+1)
		current[0] = leftIndex + 1
		for rightIndex, rightRune := range right {
			cost := 0
			if leftRune != rightRune {
				cost = 1
			}
			current[rightIndex+1] = min(current[rightIndex]+1, min(previous[rightIndex+1]+1, previous[rightIndex]+cost))
		}
		previous = current
	}
	return previous[len(right)]
}

// min returns the smaller integer value.
func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
