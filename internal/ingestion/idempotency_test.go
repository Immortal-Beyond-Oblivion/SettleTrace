package ingestion

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestVerifyHMACAcceptsRawBodySignature verifies raw-body signature checking succeeds for a valid payload.
func TestVerifyHMACAcceptsRawBodySignature(t *testing.T) {
	body := []byte(`{"id":"pay_1","amount":100}`)
	mac := hmac.New(sha256.New, []byte("local-secret"))
	_, _ = mac.Write(body)
	if !VerifyHMAC(body, hex.EncodeToString(mac.Sum(nil)), "local-secret") {
		t.Fatal("expected valid signature")
	}
}

// TestVerifyHMACRejectsChangedBody verifies a signature cannot authenticate modified content.
func TestVerifyHMACRejectsChangedBody(t *testing.T) {
	body := []byte(`{"id":"pay_1"}`)
	if VerifyHMAC(body, "00", "local-secret") {
		t.Fatal("expected invalid signature rejection")
	}
}
