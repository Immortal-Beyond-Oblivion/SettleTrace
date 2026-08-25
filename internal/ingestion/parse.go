package ingestion

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/schema"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

// Record is one validated source event ready for transactional persistence.
type Record struct {
	Source         string
	ExternalID     string
	EventType      string
	IdempotencyKey string
	Payload        json.RawMessage
	Payment        *store.PaymentRow
	Settlement     *store.SettlementRow
	Bank           *store.BankRow
	Ledger         *store.LedgerRow
}

// Parse validates a payload and returns every contained record, or none if the file is malformed.
func Parse(source string, raw []byte) ([]Record, error) {
	if err := schema.Validate(source, raw); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] != '{' && trimmed[0] != '[' {
		return parseCSV(source, raw)
	}
	return parseJSON(source, trimmed)
}

// parseJSON converts one object or an array of objects into ingest records.
func parseJSON(source string, raw []byte) ([]Record, error) {
	var objects []map[string]json.RawMessage
	if raw[0] == '{' {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, err
		}
		objects = []map[string]json.RawMessage{object}
	} else if err := json.Unmarshal(raw, &objects); err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(objects))
	for _, object := range objects {
		record, err := recordFromObject(source, object)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// parseCSV converts a whole table into ingest records after header validation.
func parseCSV(source string, raw []byte) ([]Record, error) {
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		return nil, err
	}
	header := map[string]int{}
	for index, name := range rows[0] {
		header[strings.TrimSpace(name)] = index
	}
	records := make([]Record, 0, len(rows)-1)
	for _, row := range rows[1:] {
		object := map[string]json.RawMessage{}
		for name, index := range header {
			object[name] = jsonStringOrNumber(row[index])
		}
		record, err := recordFromObject(source, object)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// recordFromObject maps a validated object onto the matching normalized row type.
func recordFromObject(source string, object map[string]json.RawMessage) (Record, error) {
	payload, err := json.Marshal(object)
	if err != nil {
		return Record{}, err
	}
	switch source {
	case schema.SourceWebhook:
		return webhookRecord(object, payload)
	case schema.SourceSettlement:
		return settlementRecord(object, payload)
	case schema.SourceBank:
		return bankRecord(object, payload)
	case schema.SourceLedger:
		return ledgerRecord(object, payload)
	default:
		return Record{}, fmt.Errorf("unsupported source %q", source)
	}
}

// webhookRecord builds a payment ingest record from a synthetic webhook payload.
func webhookRecord(object map[string]json.RawMessage, payload json.RawMessage) (Record, error) {
	paymentID := stringField(object, "payment_id")
	status := stringField(object, "status")
	capturedAt, err := timeField(object, "captured_at")
	if err != nil {
		return Record{}, err
	}
	amount, err := intField(object, "amount_paise")
	if err != nil {
		return Record{}, err
	}
	eventType := "payment." + status
	row := store.PaymentRow{
		PaymentID:     paymentID,
		OrderID:       stringField(object, "order_id"),
		AmountPaise:   amount,
		FeePaise:      intFieldDefault(object, "fee_paise", 0),
		TaxPaise:      intFieldDefault(object, "tax_paise", 0),
		Currency:      stringField(object, "currency"),
		Method:        stringField(object, "method"),
		Status:        status,
		CapturedAt:    capturedAt,
		SourceEventAt: time.Time{},
	}
	if capturedAt != nil {
		row.SourceEventAt = *capturedAt
	}
	return Record{
		Source:         schema.SourceWebhook,
		ExternalID:     paymentID,
		EventType:      eventType,
		IdempotencyKey: IdempotencyKey(schema.SourceWebhook, paymentID, eventType),
		Payload:        payload,
		Payment:        &row,
	}, nil
}

// settlementRecord builds a settlement ingest record from synthetic JSON or CSV fields.
func settlementRecord(object map[string]json.RawMessage, payload json.RawMessage) (Record, error) {
	settlementID := stringField(object, "settlement_id")
	entityID := stringField(object, "entity_id")
	lineType := stringField(object, "type")
	if lineType == "" {
		lineType = stringField(object, "line_type")
	}
	if lineType == "" {
		lineType = "payment"
	}
	credit := intFieldDefault(object, "credit_paise", intFieldDefault(object, "credit", 0))
	settledAt, err := timeField(object, "settled_at")
	if err != nil {
		return Record{}, err
	}
	if settledAt == nil {
		return Record{}, fmt.Errorf("settled_at is required")
	}
	row := store.SettlementRow{
		SettlementID:  settlementID,
		EntityID:      entityID,
		LineType:      lineType,
		PaymentMethod: firstNonEmpty(stringField(object, "method"), stringField(object, "payment_method")),
		CreditPaise:   credit,
		DebitPaise:    intFieldDefault(object, "debit_paise", intFieldDefault(object, "debit", 0)),
		FeePaise:      intFieldDefault(object, "fee_paise", intFieldDefault(object, "fee", 0)),
		TaxPaise:      intFieldDefault(object, "tax_paise", intFieldDefault(object, "tax", 0)),
		SettledAt:     *settledAt,
	}
	return Record{
		Source:         schema.SourceSettlement,
		ExternalID:     settlementID + ":" + entityID,
		EventType:      "settlement.line",
		IdempotencyKey: IdempotencyKey(schema.SourceSettlement, settlementID+":"+entityID, "settlement.line"),
		Payload:        payload,
		Settlement:     &row,
	}, nil
}

// bankRecord builds a bank ingest record from synthetic JSON or CSV fields.
func bankRecord(object map[string]json.RawMessage, payload json.RawMessage) (Record, error) {
	reference := firstNonEmpty(stringField(object, "reference_id"), stringField(object, "reference"))
	credit, err := intField(object, "credit_paise")
	if err != nil {
		credit = intFieldDefault(object, "credit", 0)
	}
	bookedAt, err := timeField(object, "booked_at")
	if err != nil || bookedAt == nil {
		return Record{}, fmt.Errorf("booked_at is required")
	}
	row := store.BankRow{ReferenceID: reference, CreditPaise: credit, BookedAt: *bookedAt}
	return Record{
		Source:         schema.SourceBank,
		ExternalID:     reference,
		EventType:      "bank.line",
		IdempotencyKey: IdempotencyKey(schema.SourceBank, reference, "bank.line"),
		Payload:        payload,
		Bank:           &row,
	}, nil
}

// ledgerRecord builds a ledger ingest record from synthetic JSON or CSV fields.
func ledgerRecord(object map[string]json.RawMessage, payload json.RawMessage) (Record, error) {
	reference := firstNonEmpty(stringField(object, "reference_id"), stringField(object, "reference"))
	amount, err := intField(object, "amount_paise")
	if err != nil {
		return Record{}, err
	}
	bookedAt, err := timeField(object, "booked_at")
	if err != nil || bookedAt == nil {
		return Record{}, fmt.Errorf("booked_at is required")
	}
	row := store.LedgerRow{ReferenceID: reference, AmountPaise: amount, BookedAt: *bookedAt}
	return Record{
		Source:         schema.SourceLedger,
		ExternalID:     reference,
		EventType:      "ledger.line",
		IdempotencyKey: IdempotencyKey(schema.SourceLedger, reference, "ledger.line"),
		Payload:        payload,
		Ledger:         &row,
	}, nil
}

// stringField unmarshals a JSON string field or returns empty.
func stringField(object map[string]json.RawMessage, name string) string {
	raw, ok := object[name]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return strings.Trim(string(raw), `"`)
}

// intField unmarshals a required integer field.
func intField(object map[string]json.RawMessage, name string) (int64, error) {
	raw, ok := object[name]
	if !ok {
		return 0, fmt.Errorf("missing %s", name)
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return value, nil
}

// intFieldDefault unmarshals an optional integer field.
func intFieldDefault(object map[string]json.RawMessage, name string, fallback int64) int64 {
	value, err := intField(object, name)
	if err != nil {
		return fallback
	}
	return value
}

// timeField parses RFC3339 timestamps or unix seconds used by gensynth.
func timeField(object map[string]json.RawMessage, name string) (*time.Time, error) {
	raw, ok := object[name]
	if !ok {
		return nil, nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		parsed, err := time.Parse(time.RFC3339, asString)
		if err != nil {
			return nil, fmt.Errorf("invalid %s", name)
		}
		utc := parsed.UTC()
		return &utc, nil
	}
	var asUnix int64
	if err := json.Unmarshal(raw, &asUnix); err != nil {
		return nil, fmt.Errorf("invalid %s", name)
	}
	utc := time.Unix(asUnix, 0).UTC()
	return &utc, nil
}

// jsonStringOrNumber encodes a CSV cell as a JSON number when possible.
func jsonStringOrNumber(value string) json.RawMessage {
	if _, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		return json.RawMessage(strings.TrimSpace(value))
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

// firstNonEmpty returns the first nonempty string.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
