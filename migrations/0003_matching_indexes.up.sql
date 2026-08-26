ALTER TABLE payments
    ADD KEY idx_payments_method_captured (method, captured_at);

ALTER TABLE exception_log
    ADD KEY idx_exception_record (record_type, record_id);
