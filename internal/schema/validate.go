// Package schema validates synthetic source records before persistence.
package schema

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

const (
	// SourceWebhook is a signed payment lifecycle event.
	SourceWebhook = "webhook"
	// SourceSettlement is a settlement report file or object.
	SourceSettlement = "settlement"
	// SourceBank is a bank statement file or object.
	SourceBank = "bank"
	// SourceLedger is a merchant ledger file or object.
	SourceLedger = "ledger"
)

// ErrUnsupportedSource is returned when the source name is not in the v1 contract.
var ErrUnsupportedSource = errors.New("unsupported source")

// RequiredCSVColumns defines the minimum CSV contract for each supported synthetic source.
var RequiredCSVColumns = map[string][]string{
	SourceSettlement: {"settlement_id", "entity_id", "credit_paise", "settled_at"},
	SourceBank:       {"reference_id", "credit_paise", "booked_at"},
	SourceLedger:     {"reference_id", "amount_paise", "booked_at"},
}

// RequiredJSONFields defines the minimum JSON object contract for each source.
var RequiredJSONFields = map[string][]string{
	SourceWebhook:    {"payment_id", "amount_paise", "currency", "status", "method", "captured_at"},
	SourceSettlement: {"settlement_id", "entity_id", "currency", "method"},
	SourceBank:       {"reference_id", "credit_paise", "booked_at"},
	SourceLedger:     {"amount_paise", "booked_at"},
}

// Validate rejects a source payload that is malformed, out of currency scope, or PAN-shaped.
func Validate(source string, raw []byte) error {
	source = strings.ToLower(strings.TrimSpace(source))
	if err := RejectPossiblePAN(raw); err != nil {
		return err
	}
	if looksLikeCSV(raw) {
		return ValidateCSV(source, bytes.NewReader(raw))
	}
	return validateJSON(source, raw)
}

// ValidateCSV rejects a source when its header is incomplete or any data row has the wrong width.
func ValidateCSV(source string, reader io.Reader) error {
	required, ok := RequiredCSVColumns[source]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnsupportedSource, source)
	}
	rows, err := csv.NewReader(reader).ReadAll()
	if err != nil {
		return fmt.Errorf("read csv: %w", err)
	}
	if len(rows) < 2 {
		return fmt.Errorf("source %q requires a header and data row", source)
	}
	columns := make(map[string]struct{}, len(rows[0]))
	for _, column := range rows[0] {
		columns[strings.TrimSpace(column)] = struct{}{}
	}
	for _, column := range required {
		if _, exists := columns[column]; !exists {
			return fmt.Errorf("source %q missing required column %q", source, column)
		}
	}
	for index, row := range rows[1:] {
		if len(row) != len(rows[0]) {
			return fmt.Errorf("row %d has %d fields, expected %d", index+2, len(row), len(rows[0]))
		}
	}
	return nil
}

// validateJSON rejects non-objects, missing fields, non-INR currency, and non-integer amounts.
func validateJSON(source string, raw []byte) error {
	required, ok := RequiredJSONFields[source]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnsupportedSource, source)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fmt.Errorf("source %q payload is empty", source)
	}
	var objects []map[string]json.RawMessage
	switch trimmed[0] {
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return fmt.Errorf("source %q invalid json: %w", source, err)
		}
		objects = []map[string]json.RawMessage{object}
	case '[':
		if err := json.Unmarshal(trimmed, &objects); err != nil {
			return fmt.Errorf("source %q invalid json array: %w", source, err)
		}
		if len(objects) == 0 {
			return fmt.Errorf("source %q json array is empty", source)
		}
	default:
		return fmt.Errorf("source %q payload must be json object, json array, or csv", source)
	}
	for index, object := range objects {
		if err := validateJSONObject(source, required, object); err != nil {
			return fmt.Errorf("record %d: %w", index, err)
		}
	}
	return nil
}

// validateJSONObject checks required fields, INR-only currency, and integer paise amounts.
func validateJSONObject(source string, required []string, object map[string]json.RawMessage) error {
	fields := make(map[string]struct{}, len(object))
	for name := range object {
		fields[name] = struct{}{}
	}
	if source == SourceLedger {
		if _, hasReference := fields["reference"]; !hasReference {
			if _, hasReferenceID := fields["reference_id"]; !hasReferenceID {
				return fmt.Errorf("source %q missing required field %q", source, "reference")
			}
		}
	}
	for _, field := range required {
		if field == "currency" {
			continue
		}
		if _, exists := fields[field]; !exists {
			return fmt.Errorf("source %q missing required field %q", source, field)
		}
	}
	if raw, exists := object["currency"]; exists {
		var currency string
		if err := json.Unmarshal(raw, &currency); err != nil {
			return fmt.Errorf("currency must be a string")
		}
		if !strings.EqualFold(currency, "INR") {
			return fmt.Errorf("currency %q is outside INR-only v1 scope", currency)
		}
	} else if source == SourceWebhook || source == SourceSettlement {
		return fmt.Errorf("source %q missing required field %q", source, "currency")
	}
	for _, amountField := range []string{"amount_paise", "credit_paise", "debit_paise", "fee", "tax", "credit", "debit", "amount"} {
		raw, exists := object[amountField]
		if !exists {
			continue
		}
		if err := requireIntegerPaise(amountField, raw); err != nil {
			return err
		}
	}
	return nil
}

// requireIntegerPaise rejects fractional JSON numbers for money fields.
func requireIntegerPaise(field string, raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fmt.Errorf("%s is empty", field)
	}
	if trimmed[0] == '"' {
		return fmt.Errorf("%s must be an integer paise value", field)
	}
	if bytes.Contains(trimmed, []byte{'.'}) {
		return fmt.Errorf("%s must be an integer paise value", field)
	}
	var value int64
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return fmt.Errorf("%s must be an integer paise value", field)
	}
	if value < 0 {
		return fmt.Errorf("%s must be >= 0", field)
	}
	return nil
}

// looksLikeCSV reports whether the payload is a text table rather than JSON.
func looksLikeCSV(raw []byte) bool {
	trimmed := bytes.TrimLeftFunc(raw, unicode.IsSpace)
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return false
	}
	return bytes.Contains(trimmed, []byte{','})
}
