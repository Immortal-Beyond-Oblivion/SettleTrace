// Stores fault rates and structure for our ground truth tracker
package main
import (
	"time"
)


// FaultConfig holds the target injection rates for each adversarial case.
type FaultConfig struct {
	UTRCorruptionRate float64 `json:"utr_corruption_rate"`
	DelayPastSLARate   float64 `json:"delay_past_sla_rate"`
	DuplicateWebhookRate float64 `json:"duplicate_webhook_rate"`
	AmbigiousCandidateRate float64 `json:"ambigious_candidate_rate"`
	LedgerFatFingerRate float64 `json:"ledger_fat_finger_rate"`
	OutOfOrderWebhookRate float64 `json:"out_of_order_webhook_rate"`
}

// DefaultFaults provides the baseline fault injection percentages
func DefaultFaults() FaultConfig {
	return FaultConfig{
		UTRCorruptionRate: 0.03,
		DelayPastSLARate: 0.02,
		DuplicateWebhookRate: 0.01,
		AmbigiousCandidateRate: 0.015,
		LedgerFatFingerRate: 0.01,
		OutOfOrderWebhookRate: 0.005,
	}
}

// Ground Truth maps a generated payment to its expected engine outcome
type GroundTruthRecord struct {
	PaymentID string `json:"payment_id"`
	InjectionFault string `json:"injection_fault"`
	ExpectedOutcome string `json:"expected_outcome"`
}

// RecordFault logs an injected fault to the ground truth ledger
func RecordFault(ledger *[]GroundTruthRecord, payID, fault, outcome string) {
	*ledger = append(*ledger, GroundTruthRecord{
		PaymentID: payID,
		InjectionFault: fault,
		ExpectedOutcome: outcome,
	})
}

// CorruptUTR simulates a bank routing loss by truncating the UTR
func CorruptUTR(utr string) string {
	if len(utr) > 4 {
		// Simulate corruption by truncating the UTR if it's too short : returning substring
		return utr[:len(utr)/2]
	}
	return utr
}

// DelayPastSLA simulates a delay in processing that exceeds the SLA past the expected time	
func DelayPastSLA(captureAt time.Time) time.Time {
	// A 10 day delay is added to gurantee NO_CANDIDATE_IN_WINDOW exception
	return captureAt.Add(10 * 24 * time.Hour)
}

// FatFingerAmount alters an amount to simulate a human data entry typo on the ledger.
func FatFingerAmount(amount int64) int64 {
	// Simulates a transposed digit or dropped zero.
	return amount - 1000
}

