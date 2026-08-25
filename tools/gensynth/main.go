package main
import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

// EntryPoint
func main() {
	recordCount := 10000 // initial target benchmark size for synthetic data generation
	outputDir := "data/landing"
	faultConfig := DefaultFaults()
	var groundTruth []GroundTruthRecord

	// Ensure output directory exists
	os.MkdirAll(filepath.Join(outputDir, "webhooks"), 0755)
	os.MkdirAll(filepath.Join(outputDir, "settlements"), 0755)
	os.MkdirAll(filepath.Join(outputDir, "ledgers"), 0755)

	baseTime := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	rand.Seed(42)
	
	for i:= 1; i <= recordCount; i++ {
		payment := GenerateBasePayment(i, baseTime)
		fee, tax := int64(1500), int64(300) // Fixed fee and tax for synthetic baseline
		settlement := DeriveSettlement(payment, fee, tax)
		ledger := LedgerLineRecord{
			Reference: payment.OrderID,
			AmountPaise: payment.AmountPaise,
			BookedAt: payment.CapturedAt.Format(time.RFC3339),
		}

		// Fault Injection Rules :
		// if the roll falls within the 3% UTRCorruptionRate, call CorruptUTR and log ID_CORRUPTED_NO_MATCH to the ground truth ledger.  
		// If the roll falls within the 2% DelayPastSLARate, call DelayPastSLA and log NO_CANDIDATE_IN_WINDOW.  
		// If it rolls clean, log EXACT and emit the pristine records. 

		roll := rand.Float64()
		outcome := "EXACT"
		faultName := "NONE"
		// Structural Webhook Faults
		webhookFilename := payment.PaymentID + ".json"
		if roll < faultConfig.DuplicateWebhookRate {
			// Duplicate Webhook: Write exact same payload twice[cite: 5]
			writeJSON(filepath.Join(outputDir, "webhooks", payment.PaymentID+"_dup1.json"), payment)
			writeJSON(filepath.Join(outputDir, "webhooks", payment.PaymentID+"_dup2.json"), payment)
			faultName = "DUPLICATE_WEBHOOK"
			// Outcome remains EXACT because idempotency should handle this cleanly[cite: 5]
		} else if roll < faultConfig.DuplicateWebhookRate+faultConfig.OutOfOrderWebhookRate {
			// Out-of-Order Webhook: Emit a 'failed' event that arrives AFTER 'captured'[cite: 5]
			failedPayment := payment
			failedPayment.Status = "failed"
			failedPayment.CapturedAt = payment.CapturedAt.Add(-1 * time.Hour) // Happened earlier

			writeJSON(filepath.Join(outputDir, "webhooks", payment.PaymentID+"_1_captured.json"), payment)
			writeJSON(filepath.Join(outputDir, "webhooks", payment.PaymentID+"_2_failed.json"), failedPayment)
			faultName = "OUT_OF_ORDER_WEBHOOK"
		} else {
			// Standard pristine webhook delivery
			writeJSON(filepath.Join(outputDir, "webhooks", webhookFilename), payment)
		}

		// Mutator Faults (Only apply if a structural fault wasn't triggered)
		if faultName == "NONE" {
			rollMutator := rand.Float64()
			if rollMutator < faultConfig.UTRCorruptionRate {
				settlement.SettlementUTR = CorruptUTR(settlement.SettlementUTR)
				faultName = "UTR_CORRUPTION"
				outcome = "ID_CORRUPTED_NO_MATCH"
			} else if rollMutator < faultConfig.UTRCorruptionRate+faultConfig.DelayPastSLARate {
				settlement.SettledAt = DelayPastSLA(payment.CapturedAt).Unix()
				faultName = "DELAY_PAST_SLA"
				outcome = "NO_CANDIDATE_IN_WINDOW"
			} else if rollMutator < faultConfig.UTRCorruptionRate+faultConfig.DelayPastSLARate+faultConfig.LedgerFatFingerRate {
				ledger.AmountPaise = FatFingerAmount(ledger.AmountPaise)
				faultName = "LEDGER_FAT_FINGER"
				outcome = "LEDGER_AMOUNT_MISMATCH"
			}
		}

		RecordFault(&groundTruth, payment.PaymentID, faultName, outcome)

		writeJSON(filepath.Join(outputDir, "webhooks", payment.PaymentID+".json"), payment)
		writeJSON(filepath.Join(outputDir, "settlements", settlement.SettlementID+".json"), settlement)
		writeJSON(filepath.Join(outputDir, "ledgers", fmt.Sprintf("ledger_%d.json", i)), ledger)
	}

	// Write ground truth for the benchmark harness to grade against
	writeJSON(filepath.Join(outputDir, "ground_truth.jsonl"), groundTruth)
	fmt.Printf("Successfully generated %d records with injected faults.\n", recordCount)
}

// writeJSON serializes a struct to a file at the specified path.
func writeJSON(path string, data interface{}) {
	file, _ := os.Create(path)
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.Encode(data)
}