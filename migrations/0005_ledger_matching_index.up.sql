-- Supports the newly window-bounded GetUnmatchedLedgerLines query (internal/store/mysql.go):
-- WHERE matched_payment_id IS NULL AND booked_at >= ? AND booked_at < ?
-- Without this, MySQL falls back to a full table scan of ledger_lines for that predicate,
-- since idx_ledger_reference (0001) only covers reference_id.
ALTER TABLE ledger_lines
    ADD KEY idx_ledger_unmatched_booked (matched_payment_id, booked_at);
