-- Supports the Settlement Q&A agent's "why didn't payment X settle" lookup
-- (internal/store/qa_mysql.go, MySQLStore.WhyNotSettled):
-- WHERE record_type = 'payment' AND record_id = ? ORDER BY id DESC
-- exception_log's only existing index (idx_exception_risk, 0001) is keyed on
-- (amount_at_risk_paise, id), so without this a per-payment lookup is a full table scan.
-- The trailing id column lets MySQL satisfy ORDER BY id DESC from the index itself.
ALTER TABLE exception_log
    ADD KEY idx_exception_record (record_type, record_id, id);
