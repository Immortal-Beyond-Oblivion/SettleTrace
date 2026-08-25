ALTER TABLE ledger_lines
    DROP INDEX idx_ledger_reference,
    ADD UNIQUE KEY uq_ledger_reference (reference_id);
