// Package schema validates synthetic source records before persistence.
package schema

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// RequiredColumns defines the minimum CSV contract for each supported synthetic source.
var RequiredColumns = map[string][]string{"settlement": {"settlement_id", "entity_id", "credit_paise", "settled_at"}, "bank": {"reference_id", "credit_paise", "booked_at"}, "ledger": {"reference_id", "amount_paise", "booked_at"}}

// ValidateCSV rejects a source when its header is incomplete or any data row has the wrong width.
func ValidateCSV(source string, reader io.Reader) error {
	required, ok := RequiredColumns[source]
	if !ok {
		return fmt.Errorf("unsupported source %q", source)
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
