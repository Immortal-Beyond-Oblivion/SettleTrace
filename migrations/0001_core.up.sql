CREATE TABLE raw_events (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    source VARCHAR(32) NOT NULL,
    external_id VARCHAR(128) NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    idempotency_key VARCHAR(256) NOT NULL,
    payload_json JSON NOT NULL,
    received_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_raw_events_idempotency (idempotency_key)
);

CREATE TABLE payments (
    payment_id VARCHAR(128) PRIMARY KEY,
    order_id VARCHAR(128) NULL,
    amount_paise BIGINT NOT NULL,
    fee_paise BIGINT NOT NULL DEFAULT 0,
    tax_paise BIGINT NOT NULL DEFAULT 0,
    currency CHAR(3) NOT NULL DEFAULT 'INR',
    method VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL,
    captured_at DATETIME(6) NULL,
    source_event_at DATETIME(6) NOT NULL,
    CHECK (amount_paise >= 0),
    CHECK (fee_paise >= 0),
    CHECK (tax_paise >= 0)
);

CREATE TABLE settlement_lines (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    settlement_id VARCHAR(128) NOT NULL,
    entity_id VARCHAR(128) NULL,
    line_type VARCHAR(32) NOT NULL,
    payment_method VARCHAR(32) NOT NULL,
    credit_paise BIGINT NOT NULL DEFAULT 0,
    debit_paise BIGINT NOT NULL DEFAULT 0,
    fee_paise BIGINT NOT NULL DEFAULT 0,
    tax_paise BIGINT NOT NULL DEFAULT 0,
    settled_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_settlement_entity (settlement_id, entity_id, line_type),
    KEY idx_settlement_candidates (payment_method, settled_at)
);

CREATE TABLE bank_lines (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    reference_id VARCHAR(128) NOT NULL,
    credit_paise BIGINT NOT NULL,
    booked_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_bank_reference (reference_id)
);

CREATE TABLE ledger_lines (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    reference_id VARCHAR(128) NOT NULL,
    amount_paise BIGINT NOT NULL,
    booked_at DATETIME(6) NOT NULL,
    matched_payment_id VARCHAR(128) NULL,
    KEY idx_ledger_reference (reference_id),
    CONSTRAINT fk_ledger_payment FOREIGN KEY (matched_payment_id) REFERENCES payments(payment_id)
);

CREATE TABLE batch_queue (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    batch_id VARCHAR(128) NOT NULL UNIQUE,
    status ENUM('pending', 'claimed', 'done', 'failed') NOT NULL DEFAULT 'pending',
    claimed_by VARCHAR(128) NULL,
    claimed_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL
);

CREATE TABLE match_results (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    match_group_id CHAR(36) NOT NULL,
    record_type VARCHAR(32) NOT NULL,
    record_id VARCHAR(128) NOT NULL,
    confidence ENUM('EXACT', 'HIGH', 'ADVISORY_ONLY') NOT NULL,
    rule_id VARCHAR(64) NOT NULL,
    evidence_json JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    KEY idx_match_record (record_type, record_id),
    KEY idx_match_group (match_group_id)
);

CREATE TABLE exception_log (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    record_type VARCHAR(32) NOT NULL,
    record_id VARCHAR(128) NOT NULL,
    reason_code VARCHAR(64) NOT NULL,
    amount_at_risk_paise BIGINT NOT NULL,
    evidence_json JSON NOT NULL,
    resolved_at DATETIME(6) NULL,
    resolved_by VARCHAR(128) NULL,
    created_at DATETIME(6) NOT NULL,
    KEY idx_exception_risk (amount_at_risk_paise DESC, id DESC)
);

CREATE TABLE audit_log (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    event_type VARCHAR(64) NOT NULL,
    payload_json JSON NOT NULL,
    previous_hash CHAR(64) NULL,
    row_hash CHAR(64) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_audit_hash (row_hash)
);
