ALTER TABLE ledger_lines
    DROP INDEX uq_ledger_reference,
    ADD KEY idx_ledger_reference (reference_id);
