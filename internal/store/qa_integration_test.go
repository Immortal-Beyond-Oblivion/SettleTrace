//go:build integration

package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// insertQAException inserts one exception_log row via raw SQL. resolvedAt may be nil.
func insertQAException(t *testing.T, db *sql.DB, recordID, reason string, amountPaise int64, resolvedAt *time.Time) {
	t.Helper()
	var resolved any
	if resolvedAt != nil {
		resolved = resolvedAt.UTC()
	}
	if _, err := db.Exec(`
		INSERT INTO exception_log (record_type, record_id, reason_code, amount_at_risk_paise, evidence_json, resolved_at, created_at)
		VALUES ('payment', ?, ?, ?, '{"candidates_checked":0}', ?, ?)`,
		recordID, reason, amountPaise, resolved, time.Now().UTC().Truncate(time.Microsecond),
	); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLStore_WhyNotSettled_ReturnsOnlyThatPaymentMostRecentFirst(t *testing.T) {
	db := openExceptionIntegrationDB(t)
	resolvedAt := time.Now().UTC().Truncate(time.Microsecond)
	insertQAException(t, db, "pay_QA1", "NO_CANDIDATE_IN_WINDOW", 50000, &resolvedAt)
	insertQAException(t, db, "pay_QA1", "AMOUNT_MISMATCH", 70000, nil)
	insertQAException(t, db, "pay_QA2", "NO_CANDIDATE_IN_WINDOW", 999, nil)

	rows, err := OpenMySQLStore(db).WhyNotSettled(context.Background(), "pay_QA1")
	if err != nil {
		t.Fatalf("WhyNotSettled: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows for pay_QA1, got %d: %+v", len(rows), rows)
	}
	// Most recent (highest id) first: the AMOUNT_MISMATCH row was inserted second.
	if rows[0].ReasonCode != "AMOUNT_MISMATCH" || rows[0].AmountAtRiskPaise != 70000 || rows[0].ResolvedAt != nil {
		t.Errorf("first row = %+v", rows[0])
	}
	if rows[1].ReasonCode != "NO_CANDIDATE_IN_WINDOW" || rows[1].ResolvedAt == nil {
		t.Errorf("second row = %+v", rows[1])
	}
	if string(rows[0].EvidenceJSON) != `{"candidates_checked": 0}` && string(rows[0].EvidenceJSON) != `{"candidates_checked":0}` {
		t.Errorf("evidence_json did not round-trip: %s", rows[0].EvidenceJSON)
	}

	none, err := OpenMySQLStore(db).WhyNotSettled(context.Background(), "pay_DOES_NOT_EXIST")
	if err != nil {
		t.Fatalf("WhyNotSettled(unknown): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("expected no rows for an unknown payment, got %d", len(none))
	}
}

func TestMySQLStore_UnresolvedAmountByReason_GroupsAndExcludesResolved(t *testing.T) {
	db := openExceptionIntegrationDB(t)
	resolvedAt := time.Now().UTC().Truncate(time.Microsecond)
	insertQAException(t, db, "pay_A", "NO_CANDIDATE_IN_WINDOW", 100, nil)
	insertQAException(t, db, "pay_B", "NO_CANDIDATE_IN_WINDOW", 300, nil)
	insertQAException(t, db, "pay_C", "AMOUNT_MISMATCH", 500, nil)
	insertQAException(t, db, "pay_D", "AMOUNT_MISMATCH", 9999, &resolvedAt) // resolved: must not count

	rows, err := OpenMySQLStore(db).UnresolvedAmountByReason(context.Background())
	if err != nil {
		t.Fatalf("UnresolvedAmountByReason: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 reason groups, got %d: %+v", len(rows), rows)
	}
	// Highest total first: AMOUNT_MISMATCH (500, resolved 9999 row excluded) leads
	// NO_CANDIDATE_IN_WINDOW (100 + 300 = 400).
	if rows[0].ReasonCode != "AMOUNT_MISMATCH" || rows[0].TotalAtRiskPaise != 500 || rows[0].Count != 1 {
		t.Errorf("first group = %+v", rows[0])
	}
	if rows[1].ReasonCode != "NO_CANDIDATE_IN_WINDOW" || rows[1].TotalAtRiskPaise != 400 || rows[1].Count != 2 {
		t.Errorf("second group = %+v", rows[1])
	}
}

func TestMySQLStore_WorstMethodsByMatchRate_OrdersWorstFirst(t *testing.T) {
	db := openExceptionIntegrationDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Unique method names keep this test independent of whatever else is in payments.
	// qa_low: 1 of 2 payments matched (0.5). qa_high: 2 of 2 matched (1.0).
	payments := []struct {
		id      string
		method  string
		matched bool
	}{
		{"pay_QATEST_L1", "qa_low", true},
		{"pay_QATEST_L2", "qa_low", false},
		{"pay_QATEST_H1", "qa_high", true},
		{"pay_QATEST_H2", "qa_high", true},
	}
	cleanup := func() {
		for _, p := range payments {
			_, _ = db.Exec(`DELETE FROM match_results WHERE record_type = 'payment' AND record_id = ?`, p.id)
			_, _ = db.Exec(`DELETE FROM payments WHERE payment_id = ?`, p.id)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	for i, p := range payments {
		if _, err := db.Exec(`
			INSERT INTO payments (payment_id, amount_paise, method, status, captured_at, source_event_at)
			VALUES (?, 1000, ?, 'captured', ?, ?)`, p.id, p.method, now, now); err != nil {
			t.Fatal(err)
		}
		if p.matched {
			if _, err := db.Exec(`
				INSERT INTO match_results (match_group_id, record_type, record_id, confidence, rule_id, evidence_json, created_at)
				VALUES (?, 'payment', ?, 'EXACT', 'tier1_exact', '{}', ?)`,
				fmt.Sprintf("00000000-0000-0000-0000-%012d", i), p.id, now); err != nil {
				t.Fatal(err)
			}
		}
	}

	rows, err := OpenMySQLStore(db).WorstMethodsByMatchRate(context.Background(), now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("WorstMethodsByMatchRate: %v", err)
	}
	rates := map[string]float64{}
	order := []string{}
	for _, row := range rows {
		if row.Method == "qa_low" || row.Method == "qa_high" {
			rates[row.Method] = row.MatchRate
			order = append(order, row.Method)
		}
	}
	if rates["qa_low"] != 0.5 || rates["qa_high"] != 1.0 {
		t.Fatalf("rates = %v, want qa_low=0.5 qa_high=1.0", rates)
	}
	if len(order) != 2 || order[0] != "qa_low" {
		t.Errorf("order = %v, want qa_low (worst) first", order)
	}
}
