package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Compile-time proof that MySQLStore satisfies the Settlement Q&A agent's entire query surface.
// If someone renames or re-signatures a QAStore method, this line fails the build instead of
// letting cmd/api discover it at runtime.
var _ QAStore = (*MySQLStore)(nil)

// maxWhyNotSettledRows caps how many exception_log rows one "why didn't X settle" answer can
// pull. A single payment can legitimately accumulate several rows over time (raised, resolved,
// re-raised), but never so many that an LLM prompt built from them needs to be unbounded.
const maxWhyNotSettledRows = 20

// WhyNotSettled returns every exception_log row on record for one payment, most recent first.
//
// implementation.md section 8's sketch of this template joins exception_log to payments on a
// surrogate payments.id; the real schema (migrations/0001_core.up.sql) has no such column --
// payments.payment_id IS the primary key, and the matching engine writes it directly into
// exception_log.record_id with record_type = 'payment' (see GetUnmatchedPaymentsInWindow above,
// which relies on exactly that pairing). So no join is needed: this is a single indexed lookup
// against idx_exception_record (migrations/0006_exception_record_index.up.sql).
func (store *MySQLStore) WhyNotSettled(ctx context.Context, paymentID string) ([]WhyNotSettledRow, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT reason_code, evidence_json, amount_at_risk_paise, resolved_at
		FROM exception_log
		WHERE record_type = 'payment' AND record_id = ?
		ORDER BY id DESC
		LIMIT ?`,
		paymentID, maxWhyNotSettledRows,
	)
	if err != nil {
		return nil, fmt.Errorf("query why-not-settled: %w", err)
	}
	defer rows.Close()

	result := make([]WhyNotSettledRow, 0)
	for rows.Next() {
		var row WhyNotSettledRow
		var evidence []byte
		var resolvedAt sql.NullTime
		if err := rows.Scan(&row.ReasonCode, &evidence, &row.AmountAtRiskPaise, &resolvedAt); err != nil {
			return nil, fmt.Errorf("scan why-not-settled row: %w", err)
		}
		row.EvidenceJSON = json.RawMessage(evidence)
		if resolvedAt.Valid {
			resolved := resolvedAt.Time.UTC()
			row.ResolvedAt = &resolved
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// UnresolvedAmountByReason returns current unresolved exception totals grouped by reason code,
// highest amount at risk first (reason_code ASC breaks ties so the order is deterministic).
//
// implementation.md section 8's sketch filters on a batch_run_id column that exception_log does
// not have; grouping by reason_code is the closest real-schema equivalent of "how much is at
// risk, and why" -- see the ReasonAggregateRow doc comment in store.go.
func (store *MySQLStore) UnresolvedAmountByReason(ctx context.Context) ([]ReasonAggregateRow, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT reason_code, COALESCE(SUM(amount_at_risk_paise), 0) AS total_at_risk, COUNT(*) AS exception_count
		FROM exception_log
		WHERE resolved_at IS NULL
		GROUP BY reason_code
		ORDER BY total_at_risk DESC, reason_code ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query unresolved amount by reason: %w", err)
	}
	defer rows.Close()

	result := make([]ReasonAggregateRow, 0)
	for rows.Next() {
		var row ReasonAggregateRow
		if err := rows.Scan(&row.ReasonCode, &row.TotalAtRiskPaise, &row.Count); err != nil {
			return nil, fmt.Errorf("scan unresolved amount row: %w", err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// WorstMethodsByMatchRate returns up to limit payment methods with captured payments since the
// given time, ordered by match rate ascending (worst first, method ASC on ties).
//
// A payment counts as matched when at least one match_results row exists for it. EXISTS is used
// instead of implementation.md section 8's LEFT JOIN + COUNT sketch because match_results is
// append-only and a payment can have more than one row over time (a superseded match keeps its
// old row); a plain LEFT JOIN would multiply such a payment's weight in the denominator and
// skew the rate. idx_match_record (record_type, record_id) makes each EXISTS probe an index
// lookup.
func (store *MySQLStore) WorstMethodsByMatchRate(ctx context.Context, since time.Time, limit int) ([]MethodMatchRateRow, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT p.method,
		       AVG(CASE WHEN EXISTS (
		           SELECT 1 FROM match_results mr WHERE mr.record_type = 'payment' AND mr.record_id = p.payment_id
		       ) THEN 1 ELSE 0 END) AS match_rate
		FROM payments p
		WHERE p.captured_at IS NOT NULL AND p.captured_at >= ?
		GROUP BY p.method
		ORDER BY match_rate ASC, p.method ASC
		LIMIT ?`,
		since.UTC(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query worst methods by match rate: %w", err)
	}
	defer rows.Close()

	result := make([]MethodMatchRateRow, 0)
	for rows.Next() {
		var row MethodMatchRateRow
		if err := rows.Scan(&row.Method, &row.MatchRate); err != nil {
			return nil, fmt.Errorf("scan method match rate row: %w", err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
