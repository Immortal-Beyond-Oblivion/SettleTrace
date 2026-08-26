// Package matcher orchestrates the deterministic recon tiers against a ReconStore.
package matcher

import "time"

// Report summarizes one matching-engine run over a time window.
type Report struct {
	WindowStart         time.Time
	WindowEnd           time.Time
	MatchedByConfidence map[string]int
	ExceptionsByReason  map[string]int
}

// NewReport constructs an empty report for the given window.
func NewReport(start, end time.Time) *Report {
	return &Report{
		WindowStart:         start,
		WindowEnd:           end,
		MatchedByConfidence: map[string]int{},
		ExceptionsByReason:  map[string]int{},
	}
}

// recordMatched increments the count for one confidence tier.
func (report *Report) recordMatched(confidence string) {
	report.MatchedByConfidence[confidence]++
}

// recordException increments the count for one exception reason code.
func (report *Report) recordException(reason string) {
	report.ExceptionsByReason[reason]++
}

// TotalMatched returns the sum of matches across every confidence tier.
func (report *Report) TotalMatched() int {
	total := 0
	for _, count := range report.MatchedByConfidence {
		total += count
	}
	return total
}

// TotalExceptions returns the sum of exceptions across every reason code.
func (report *Report) TotalExceptions() int {
	total := 0
	for _, count := range report.ExceptionsByReason {
		total += count
	}
	return total
}
