package qa

import (
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func TestClassify(t *testing.T) {
	cases := []struct {
		name          string
		question      string
		wantIntent    Intent
		wantPaymentID string
		wantDays      int
	}{
		{name: "payment id question", question: "why didn't pay_H8x92k4Rt settle this week?", wantIntent: IntentWhyNotSettled, wantPaymentID: "pay_H8x92k4Rt"},
		{name: "payment id wins over aggregate wording", question: "how much is pay_abc123 at risk?", wantIntent: IntentWhyNotSettled, wantPaymentID: "pay_abc123"},
		{name: "payment id keeps its case", question: "status of pay_AbC9?", wantIntent: IntentWhyNotSettled, wantPaymentID: "pay_AbC9"},
		{name: "worst method default window", question: "which payment method has the worst match rate?", wantIntent: IntentWorstMethod, wantDays: 7},
		{name: "worst method today", question: "worst matching method today", wantIntent: IntentWorstMethod, wantDays: 1},
		{name: "worst method this month", question: "which method has the lowest match rate this month", wantIntent: IntentWorstMethod, wantDays: 30},
		{name: "worst method explicit days", question: "worst method match rate over the last 14 days", wantIntent: IntentWorstMethod, wantDays: 14},
		{name: "worst method days clamped to max", question: "worst method match rate last 400 days", wantIntent: IntentWorstMethod, wantDays: 90},
		{name: "worst without method or matching wording is not a method question", question: "what is my worst exception?", wantIntent: IntentUnsupported},
		{name: "how much", question: "how much money is at risk?", wantIntent: IntentAmountAtRisk},
		{name: "how many", question: "How many exceptions are unresolved", wantIntent: IntentAmountAtRisk},
		{name: "unsupported", question: "what's the weather like", wantIntent: IntentUnsupported},
		{name: "empty", question: "", wantIntent: IntentUnsupported},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.question, fixedNow)
			if got.Intent != tc.wantIntent {
				t.Fatalf("intent = %q, want %q", got.Intent, tc.wantIntent)
			}
			if got.PaymentID != tc.wantPaymentID {
				t.Errorf("payment id = %q, want %q", got.PaymentID, tc.wantPaymentID)
			}
			if got.WindowDays != tc.wantDays {
				t.Errorf("window days = %d, want %d", got.WindowDays, tc.wantDays)
			}
			if tc.wantDays > 0 {
				wantSince := fixedNow.AddDate(0, 0, -tc.wantDays)
				if !got.Since.Equal(wantSince) {
					t.Errorf("since = %s, want %s", got.Since, wantSince)
				}
			}
		})
	}
}
