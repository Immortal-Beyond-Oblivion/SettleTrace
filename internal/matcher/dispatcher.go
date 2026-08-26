package matcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

// defaultSLA is the fallback per-method settlement SLA used when a method has no override.
var defaultSLA = map[string]time.Duration{
	"card":       4 * 24 * time.Hour,
	"netbanking": 3 * 24 * time.Hour,
	"upi":        1 * 24 * time.Hour,
	"wallet":     2 * 24 * time.Hour,
}

// fallbackSLA is used for any payment method not present in defaultSLA.
const fallbackSLA = 5 * 24 * time.Hour

// ledgerBookingLagWindow mirrors TierL's own +/-3 day booking-lag window.
const ledgerBookingLagWindow = 3 * 24 * time.Hour

// slaFor returns the configured settlement SLA window for a payment method.
func slaFor(method string) time.Duration {
	if window, ok := defaultSLA[method]; ok {
		return window
	}
	return fallbackSLA
}

// Engine runs the deterministic recon tiers against candidates loaded from a ReconStore.
type Engine struct {
	Store store.ReconStore
	Clock recon.Clock
	// Tier3 is optional. When nil (the default, and what every existing test still gets),
	// a payment that survives Tier 1 and Tier 2 falls straight through to
	// recon.ClassifyPayment's plain exception, exactly as before this field existed. When
	// set, it is tried first, and only a successful, non-empty rank changes the exception
	// written; a nil client, an error, or an empty rank all degrade to the same plain
	// exception path, never fail the batch.
	Tier3 *Tier3Client
}

// NewEngine constructs a matching Engine, defaulting to a UTC clock when none is supplied.
func NewEngine(reconStore store.ReconStore, clock recon.Clock) *Engine {
	if clock == nil {
		clock = recon.UTCClock{}
	}
	return &Engine{Store: reconStore, Clock: clock}
}

// RunWindow matches every unmatched payment captured in [start, end) against settlement
// candidates, then separately matches every unmatched ledger line against payments in a
// booking-lag-widened window, and returns a summary report.
func (engine *Engine) RunWindow(ctx context.Context, start, end time.Time) (*Report, error) {
	report := NewReport(start, end)
	if err := engine.runSettlementSide(ctx, start, end, report); err != nil {
		return report, fmt.Errorf("settlement side: %w", err)
	}
	if err := engine.runLedgerSide(ctx, start, end, report); err != nil {
		return report, fmt.Errorf("ledger side: %w", err)
	}
	return report, nil
}

// runSettlementSide loads unmatched payments once and settlement candidates once per
// method, then runs Tier1 -> Tier2 -> the deterministic exception classifier over the
// in-memory slices, matching implementation.md's load-once-per-window reasoning.
func (engine *Engine) runSettlementSide(ctx context.Context, start, end time.Time, report *Report) error {
	payments, err := engine.Store.GetUnmatchedPaymentsInWindow(ctx, start, end)
	if err != nil {
		return fmt.Errorf("load unmatched payments: %w", err)
	}
	byMethod := groupByMethod(payments)
	for method, group := range byMethod {
		sla := slaFor(method)
		candidateRows, err := engine.Store.GetSettlementCandidates(ctx, method, start, end.Add(sla))
		if err != nil {
			return fmt.Errorf("load settlement candidates for %s: %w", method, err)
		}
		candidates := toSettlementLines(candidateRows)
		for _, paymentRow := range group {
			payment := toReconPayment(paymentRow, sla)
			if match, ok := (recon.Tier1{}).TryMatch(payment, candidates); ok {
				if err := engine.recordPaymentMatch(ctx, match, report); err != nil {
					return fmt.Errorf("record tier1 match for %s: %w", payment.ID, err)
				}
				continue
			}
			if match, ok := (recon.Tier2{}).TryMatch(payment, candidates); ok {
				if err := engine.recordPaymentMatch(ctx, match, report); err != nil {
					return fmt.Errorf("record tier2 match for %s: %w", payment.ID, err)
				}
				continue
			}
			exception := engine.classifyAfterTier2(ctx, payment, candidates)
			if err := engine.recordException(ctx, exception, report); err != nil {
				return fmt.Errorf("record exception for %s: %w", payment.ID, err)
			}
		}
	}
	return nil
}

// classifyAfterTier2 is the Tier 3 decision point: a payment that Tier 1 and Tier 2 both
// rejected either gets an advisory-ranked MULTI_CANDIDATE_AMBIGUOUS exception from the
// fuzzy-ranker service, or the same plain recon.ClassifyPayment exception this engine wrote
// before Tier 3 existed. Tier 3 never produces a match_results row, only different evidence
// on an exception_log row, matching the advisory-only contract in implementation.md section
// 2.2 and services/fuzzy-ranker/app.py's own response shape.
func (engine *Engine) classifyAfterTier2(ctx context.Context, payment recon.Payment, candidates []recon.SettlementLine) recon.Exception {
	if engine.Tier3 == nil || len(candidates) == 0 {
		return recon.ClassifyPayment(payment, candidates)
	}
	ranked, err := engine.Tier3.Rank(ctx, payment, candidates)
	if err != nil {
		log.Printf("tier3 fuzzy-ranker unavailable for %s, degrading to plain exception: %v", payment.ID, err)
		return recon.ClassifyPayment(payment, candidates)
	}
	if len(ranked) == 0 {
		return recon.ClassifyPayment(payment, candidates)
	}
	return recon.Exception{
		RecordType:        "payment",
		RecordID:          payment.ID,
		ReasonCode:        recon.ReasonAmbiguous,
		AmountAtRiskPaise: payment.AmountPaise,
		Evidence: map[string]any{
			"source":           "tier3_fuzzy_ranker",
			"expected_net_paise": payment.NetAmount(),
			"ranked_candidates": ranked,
		},
	}
}

// runLedgerSide loads every currently unmatched ledger line (not window-bounded; see
// state.md section 5.4) and matches it against payments captured in a booking-lag-widened
// window around [start, end).
func (engine *Engine) runLedgerSide(ctx context.Context, start, end time.Time, report *Report) error {
	ledgerRows, err := engine.Store.GetUnmatchedLedgerLines(ctx)
	if err != nil {
		return fmt.Errorf("load unmatched ledger lines: %w", err)
	}
	if len(ledgerRows) == 0 {
		return nil
	}
	paymentRows, err := engine.Store.GetPaymentsInWindow(ctx, start.Add(-ledgerBookingLagWindow), end.Add(ledgerBookingLagWindow))
	if err != nil {
		return fmt.Errorf("load payments for ledger tier: %w", err)
	}
	payments := make([]recon.Payment, 0, len(paymentRows))
	for _, paymentRow := range paymentRows {
		payments = append(payments, toReconPayment(paymentRow, 0))
	}
	for _, ledgerRow := range ledgerRows {
		ledger := toReconLedgerLine(ledgerRow)
		if match, ok := (recon.TierL{}).TryExactMatch(ledger, payments); ok {
			if err := engine.recordLedgerMatch(ctx, match, report); err != nil {
				return fmt.Errorf("record ledger match for %s: %w", ledger.ID, err)
			}
			continue
		}
		exception := (recon.TierL{}).Classify(ledger, payments)
		if err := engine.recordException(ctx, exception, report); err != nil {
			return fmt.Errorf("record ledger exception for %s: %w", ledger.ID, err)
		}
	}
	return nil
}

// recordPaymentMatch persists a deterministic payment match and its audit trail entry.
func (engine *Engine) recordPaymentMatch(ctx context.Context, match recon.MatchResult, report *Report) error {
	evidence, err := json.Marshal(match.Evidence)
	if err != nil {
		return fmt.Errorf("marshal match evidence: %w", err)
	}
	groupID, err := newMatchGroupID()
	if err != nil {
		return fmt.Errorf("generate match group id: %w", err)
	}
	now := engine.Clock.Now().UTC()
	if err := engine.Store.WriteMatchResult(ctx, store.MatchResultRow{
		MatchGroupID: groupID,
		RecordType:   "payment",
		RecordID:     match.PaymentID,
		Confidence:   string(match.Confidence),
		RuleID:       match.RuleID,
		EvidenceJSON: evidence,
		CreatedAt:    now,
	}); err != nil {
		return err
	}
	if err := engine.writeAudit(ctx, "MATCH_RESULT_WRITTEN", map[string]any{
		"match_group_id": groupID, "record_type": "payment", "record_id": match.PaymentID,
		"confidence": match.Confidence, "rule_id": match.RuleID,
	}, now); err != nil {
		return err
	}
	report.recordMatched(string(match.Confidence))
	return nil
}

// recordLedgerMatch persists a deterministic ledger match, points the ledger line at the
// resolved payment, and writes the audit trail entry.
func (engine *Engine) recordLedgerMatch(ctx context.Context, match recon.MatchResult, report *Report) error {
	evidence, err := json.Marshal(match.Evidence)
	if err != nil {
		return fmt.Errorf("marshal ledger match evidence: %w", err)
	}
	groupID, err := newMatchGroupID()
	if err != nil {
		return fmt.Errorf("generate match group id: %w", err)
	}
	now := engine.Clock.Now().UTC()
	if err := engine.Store.WriteMatchResult(ctx, store.MatchResultRow{
		MatchGroupID: groupID,
		RecordType:   "ledger",
		RecordID:     match.LedgerLineID,
		Confidence:   string(match.Confidence),
		RuleID:       match.RuleID,
		EvidenceJSON: evidence,
		CreatedAt:    now,
	}); err != nil {
		return err
	}
	ledgerLineID, err := strconv.ParseInt(match.LedgerLineID, 10, 64)
	if err != nil {
		return fmt.Errorf("parse ledger line id: %w", err)
	}
	if err := engine.Store.SetLedgerMatchedPayment(ctx, ledgerLineID, match.PaymentID); err != nil {
		return err
	}
	if err := engine.writeAudit(ctx, "LEDGER_MATCH_WRITTEN", map[string]any{
		"match_group_id": groupID, "record_type": "ledger", "record_id": match.LedgerLineID,
		"payment_id": match.PaymentID, "rule_id": match.RuleID,
	}, now); err != nil {
		return err
	}
	report.recordMatched(string(match.Confidence))
	return nil
}

// recordException persists a reconciliation exception (payment or ledger) and its audit entry.
func (engine *Engine) recordException(ctx context.Context, exception recon.Exception, report *Report) error {
	evidence, err := json.Marshal(exception.Evidence)
	if err != nil {
		return fmt.Errorf("marshal exception evidence: %w", err)
	}
	now := engine.Clock.Now().UTC()
	if err := engine.Store.WriteExceptionLog(ctx, store.ExceptionRow{
		RecordType:        exception.RecordType,
		RecordID:          exception.RecordID,
		ReasonCode:        exception.ReasonCode,
		AmountAtRiskPaise: exception.AmountAtRiskPaise,
		EvidenceJSON:      evidence,
		CreatedAt:         now,
	}); err != nil {
		return err
	}
	if err := engine.writeAudit(ctx, "EXCEPTION_LOGGED", map[string]any{
		"record_type": exception.RecordType, "record_id": exception.RecordID, "reason_code": exception.ReasonCode,
	}, now); err != nil {
		return err
	}
	report.recordException(exception.ReasonCode)
	return nil
}

// writeAudit marshals a payload and appends one hash-chained audit_log row.
func (engine *Engine) writeAudit(ctx context.Context, eventType string, payload map[string]any, at time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal audit payload: %w", err)
	}
	if err := engine.Store.WriteAuditEntry(ctx, store.AuditEntryRow{EventType: eventType, PayloadJSON: encoded, CreatedAt: at}); err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}

// toReconPayment converts a stored payment candidate into the pure recon.Payment the tiers use.
func toReconPayment(candidate store.PaymentCandidate, sla time.Duration) recon.Payment {
	return recon.Payment{
		ID:            candidate.PaymentID,
		OrderID:       candidate.OrderID,
		AmountPaise:   candidate.AmountPaise,
		FeePaise:      candidate.FeePaise,
		TaxPaise:      candidate.TaxPaise,
		Method:        candidate.Method,
		CapturedAt:    candidate.CapturedAt,
		SettlementSLA: sla,
	}
}

// toSettlementLines converts stored settlement candidates into the pure recon.SettlementLine type.
func toSettlementLines(candidates []store.SettlementCandidate) []recon.SettlementLine {
	lines := make([]recon.SettlementLine, 0, len(candidates))
	for _, candidate := range candidates {
		lines = append(lines, recon.SettlementLine{
			ID:          strconv.FormatInt(candidate.ID, 10),
			EntityID:    candidate.EntityID,
			CreditPaise: candidate.CreditPaise,
			Method:      candidate.Method,
			SettledAt:   candidate.SettledAt,
		})
	}
	return lines
}

// toReconLedgerLine converts a stored ledger candidate into the pure recon.LedgerLine type.
func toReconLedgerLine(candidate store.LedgerCandidate) recon.LedgerLine {
	return recon.LedgerLine{
		ID:          strconv.FormatInt(candidate.ID, 10),
		ReferenceID: candidate.ReferenceID,
		AmountPaise: candidate.AmountPaise,
		BookedAt:    candidate.BookedAt,
	}
}

// groupByMethod buckets payment candidates by payment method for per-method window queries.
func groupByMethod(payments []store.PaymentCandidate) map[string][]store.PaymentCandidate {
	groups := make(map[string][]store.PaymentCandidate)
	for _, payment := range payments {
		groups[payment.Method] = append(groups[payment.Method], payment)
	}
	return groups
}
