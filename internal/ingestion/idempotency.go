// Package ingestion implements validation and safe ingestion primitives.
package ingestion

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// IdempotencyKey derives a stable key for a source event.
func IdempotencyKey(source, externalID, eventType string) string {
	return source + ":" + externalID + ":" + eventType
}

// VerifyHMAC verifies a hexadecimal SHA-256 signature over the unmodified request body.
func VerifyHMAC(body []byte, signatureHex, secret string) bool {
	if secret == "" || signatureHex == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected, err := hex.DecodeString(signatureHex)
	if err != nil {
		return false
	}
	return hmac.Equal(mac.Sum(nil), expected)
}
