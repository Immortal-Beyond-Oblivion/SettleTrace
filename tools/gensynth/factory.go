package main
import (
	"fmt"
	"time"
)

// SettlementLineRecord represents the generated settlement entity before serialization.
type SettlementLineRecord struct {
	EntityID      string `json:"entity_id"`
	Type          string `json:"type"`
	Debit         int64  `json:"debit"`
	Credit        int64  `json:"credit"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Fee           int64  `json:"fee"`
	Tax           int64  `json:"tax"`
	OnHold        bool   `json:"on_hold"`
	Settled       bool   `json:"settled"`
	CreatedAt     int64  `json:"created_at"`
	SettledAt     int64  `json:"settled_at"`
	SettlementID  string `json:"settlement_id"`
	SettlementUTR string `json:"settlement_utr"`
	OrderID       string `json:"order_id"`
	Method        string `json:"method"`
}

// LedgerLineRecord represents the generated merchant ledger entity before serialization.
type LedgerLineRecord struct {
	Reference   string `json:"reference"`
	AmountPaise int64  `json:"amount_paise"`
	BookedAt    string `json:"booked_at"`
}
// Generated Payment Entry before serialization to JSON for the engine
type PaymentRecord struct {
	PaymentID string `json:"payment_id"`
	OrderID string `json:"order_id"`
	AmountPaise int64 `json:"amount_paise"`
	Currency string `json:"currency"`
	Status string `json:"status"`
	Method string `json:"method"`
	CapturedAt time.Time `json:"captured_at"`
}

// GenerateBasePayment Factory to create valid, pristine payment records before any fault injection
func GenerateBasePayment(seqID int, baseTime time.Time) PaymentRecord {
	return PaymentRecord{
		PaymentID: fmt.Sprintf("pay_synth_%d", seqID),
		OrderID: fmt.Sprintf("order_synth_%d", seqID),
		AmountPaise: 150000, // 1500 INR: Ideally replaced with a log-normal distribution for more realistic amounts later
		Currency: "INR",
		Status: "captured",
		Method: "card",
		CapturedAt: baseTime.Add(time.Duration(seqID) * time.Minute), // Staggered capture times for realism
	}
}

func DeriveSettlement(payment PaymentRecord, feePaise int64, taxPaise int64) SettlementLineRecord {
	return SettlementLineRecord{
		SettlementID: fmt.Sprintf("setl_synth_%s", payment.PaymentID),
		SettlementUTR: fmt.Sprintf("UTR%d", payment.CapturedAt.Unix()),
		EntityID: payment.PaymentID,
		Credit: payment.AmountPaise - feePaise - taxPaise,
		Fee: feePaise,
		Tax: taxPaise,
		Method: payment.Method,
		SettledAt: payment.CapturedAt.Add(24 * time.Hour).Unix(), // Assuming a 1-day settlement period
	}
}
