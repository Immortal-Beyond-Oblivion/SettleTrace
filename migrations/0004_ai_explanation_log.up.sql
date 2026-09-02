-- The AI explainer logs every invocation here, whether or not the underlying LLM call
-- succeeded (implementation.md section 8 / section 12: "every AI-generated sentence is
-- stored, permanently" -- including the attempts that produced no sentence at all, so a
-- reviewer can see exactly what was asked, what came back, and how long it took, even for a
-- degraded or failed call).
CREATE TABLE ai_explanation_log (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    exception_id BIGINT UNSIGNED NOT NULL,
    prompt_version VARCHAR(16) NOT NULL,
    model VARCHAR(64) NOT NULL,
    input_summary_json JSON NOT NULL,
    output_text TEXT NULL,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    succeeded TINYINT(1) NOT NULL DEFAULT 0,
    error_message VARCHAR(512) NULL,
    created_at DATETIME(6) NOT NULL,
    KEY idx_ai_explanation_exception (exception_id)
);
