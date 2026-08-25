package schema

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateRejectsMalformedCSVWholly verifies one bad row fails the entire file.
func TestValidateRejectsMalformedCSVWholly(t *testing.T) {
	payload := "reference_id,credit_paise,booked_at\nref_1,100,2026-08-01T00:00:00Z\nref_2,200"
	if err := Validate(SourceBank, []byte(payload)); err == nil {
		t.Fatal("expected malformed csv rejection")
	}
}

// TestValidateAcceptsSettlementJSON verifies representative synthetic settlement fields.
func TestValidateAcceptsSettlementJSON(t *testing.T) {
	payload := `{"settlement_id":"setl_1","entity_id":"pay_1","credit":97000,"currency":"INR","method":"card","settled_at":1754006400}`
	if err := Validate(SourceSettlement, []byte(payload)); err != nil {
		t.Fatalf("expected valid settlement: %v", err)
	}
}

// TestValidateRejectsNonINR verifies v1 ingestion stays inside the INR-only scope.
func TestValidateRejectsNonINR(t *testing.T) {
	payload := `{"payment_id":"pay_1","amount_paise":100,"currency":"USD","status":"captured","method":"card","captured_at":"2026-08-01T00:00:00Z"}`
	err := Validate(SourceWebhook, []byte(payload))
	if err == nil || !strings.Contains(err.Error(), "INR") {
		t.Fatalf("expected INR scope error, got %v", err)
	}
}

// TestValidateRejectsFractionalPaise verifies money fields cannot be fractional JSON numbers.
func TestValidateRejectsFractionalPaise(t *testing.T) {
	payload := `{"payment_id":"pay_1","amount_paise":100.5,"currency":"INR","status":"captured","method":"card","captured_at":"2026-08-01T00:00:00Z"}`
	if err := Validate(SourceWebhook, []byte(payload)); err == nil {
		t.Fatal("expected fractional paise rejection")
	}
}

// TestValidateRejectsLuhnValidDigitSequence verifies PAN-shaped data is rejected before persistence.
func TestValidateRejectsLuhnValidDigitSequence(t *testing.T) {
	payload := []byte(`{"notes":"customer ref 4532015112830366","reference_id":"ref_1","credit_paise":5000,"booked_at":"2026-08-01T00:00:00Z"}`)
	if err := Validate(SourceBank, payload); !errors.Is(err, ErrPossiblePANDetected) {
		t.Fatalf("expected PAN rejection, got %v", err)
	}
}

// TestValidateDoesNotFalsePositiveOnNonLuhnDigits verifies long non-card numbers remain ingestible.
func TestValidateDoesNotFalsePositiveOnNonLuhnDigits(t *testing.T) {
	payload := []byte(`{"settlement_id":"1234567890123456","entity_id":"pay_1","credit":5000,"currency":"INR","method":"card","settled_at":1754006400}`)
	if err := Validate(SourceSettlement, payload); err != nil {
		t.Fatalf("expected non-Luhn reference to pass, got %v", err)
	}
}
