package store

import (
	"fmt"
	"strings"
)

var appendOnlyTables = []string{"raw_events", "match_results", "audit_log"}

// GuardMutation rejects ordinary UPDATE or DELETE statements against append-only history tables.
func GuardMutation(query string) error {
	normalized := strings.ToLower(strings.TrimSpace(query))
	normalized = strings.ReplaceAll(normalized, "`", "")
	isUpdate := strings.HasPrefix(normalized, "update")
	isDelete := strings.HasPrefix(normalized, "delete")
	if !isUpdate && !isDelete {
		return nil
	}
	for _, table := range appendOnlyTables {
		if mutationTargetsTable(normalized, table) {
			return fmt.Errorf("%w: %s", ErrAppendOnly, table)
		}
	}
	return nil
}

// mutationTargetsTable reports whether a mutating statement names a protected table.
func mutationTargetsTable(normalizedQuery, table string) bool {
	tokens := []string{
		"update " + table + " ",
		"update " + table,
		"from " + table + " ",
		"from " + table,
	}
	for _, token := range tokens {
		if strings.Contains(normalizedQuery, token) {
			return true
		}
	}
	return strings.Contains(normalizedQuery, " "+table+" ") || strings.HasSuffix(normalizedQuery, " "+table)
}
