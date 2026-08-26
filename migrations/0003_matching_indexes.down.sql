ALTER TABLE payments
    DROP INDEX idx_payments_method_captured;

ALTER TABLE exception_log
    DROP INDEX idx_exception_record;
