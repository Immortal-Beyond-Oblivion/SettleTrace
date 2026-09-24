// Package api exposes the minimal read-only operator HTTP surface.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/ai"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/ai/qa"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/audit"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

// QAAnswerer is the one capability POST /v1/qa needs from the Settlement Q&A agent. It is an
// interface (rather than a concrete *qa.Agent) so handler tests can inject a stub, and so the
// HTTP layer cannot reach anything on the agent beyond answering a question -- kept separate
// from ai.Explainer on purpose, per implementation.md section 5's AIExplainer/QAAnswerer split.
type QAAnswerer interface {
	Answer(ctx context.Context, question string) (qa.Answer, error)
}

// maxQARequestBytes and maxQuestionRunes bound POST /v1/qa input. A question is one short
// sentence; anything larger is rejected before it can reach the classifier or an LLM prompt.
const (
	maxQARequestBytes = 8 << 10
	maxQuestionRunes  = 500
)

// Server serves batch and exception responses from an injected read model.
type Server struct {
	// Exceptions is the zero-configuration fallback GET /v1/exceptions serves when Store is
	// nil or doesn't implement store.ExceptionLister -- kept so the ingestion/matching-only
	// dev smoke test path (implementation.md section 12) still returns something before a
	// database is wired up. Once Store implements ExceptionLister, real exception_log rows
	// take over and this field is ignored by that route.
	Exceptions []recon.Exception

	// Store and Explainer back POST /v1/exceptions/{id}/explain. Both are optional: when
	// either is nil, the route degrades to 503 rather than panicking, matching this repo's
	// established "not configured degrades gracefully" convention (nil BudgetTracker,
	// CircuitBreaker, and LLMClient all behave the same way inside internal/ai itself).
	// This lets the API start and serve every other route even before AI is configured.
	Store     store.ExceptionReader
	Explainer *ai.Explainer

	// AdHocBatchRunID is the ai.BudgetTracker key charged for explain calls made through
	// this HTTP route. An exception explained on demand via the API/dashboard/CLI isn't
	// part of any specific matching-engine batch run, so it is deliberately attributed to
	// one fixed, always-on budget bucket rather than a real batch_run_id. Defaults to
	// "api:adhoc" when empty.
	AdHocBatchRunID string

	// QA backs POST /v1/qa. Optional: when nil the route degrades to 503, same convention as
	// Store/Explainer above, so the API still starts and serves every other route without it.
	QA QAAnswerer

	// Audit backs POST /v1/ingest/verify-chain. Optional: when nil the route degrades to 503,
	// same convention as Store/Explainer/QA above. It is the read-only store.AuditReader, so this
	// route can re-check the hash chain but has no way to write to audit_log.
	Audit store.AuditReader
}

// Routes returns the configured HTTP handler tree.
func (server Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("GET /v1/exceptions", server.listExceptions)
	mux.HandleFunc("POST /v1/exceptions/", server.resolveException)
	mux.HandleFunc("POST /v1/exceptions/{id}/explain", server.explainException)
	mux.HandleFunc("POST /v1/qa", server.askQuestion)
	mux.HandleFunc("POST /v1/ingest/verify-chain", server.verifyChain)
	return mux
}

// health reports process readiness without exposing dependencies or secrets.
func (server Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

// listExceptions returns unresolved exceptions worst-amount-at-risk first, reading real
// exception_log rows via keyset (cursor) pagination when Store supports it -- implementation.md
// section 26's documented shape ("sorted by amount_at_risk_paise DESC by default", a
// next_cursor for paging). Falls back to the injected Server.Exceptions slice when Store is
// nil or doesn't implement store.ExceptionLister, matching this file's established
// "not configured degrades gracefully, never panics" convention for explainException above.
func (server Server) listExceptions(writer http.ResponseWriter, request *http.Request) {
	lister, ok := server.Store.(store.ExceptionLister)
	if !ok {
		writeJSON(writer, http.StatusOK, map[string]any{"exceptions": server.Exceptions})
		return
	}

	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}

	var cursor *store.ExceptionCursor
	if raw := request.URL.Query().Get("cursor"); raw != "" {
		decoded, err := decodeExceptionCursor(raw)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid cursor"})
			return
		}
		cursor = decoded
	}

	// Ask the store for one row more than the page size. That extra row is never returned to the
	// caller; it exists only to prove another page follows. Deciding "is there a next page" from
	// len(records) == limit alone is wrong whenever the total row count is an exact multiple of
	// the page size: the final, full page would still carry a next_cursor, and following it
	// would cost the caller one more round trip that returns zero rows.
	records, err := lister.ListUnresolvedExceptions(request.Context(), limit+1, cursor)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "failed to load exceptions"})
		return
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}

	exceptions := make([]map[string]any, 0, len(records))
	for _, record := range records {
		exceptions = append(exceptions, map[string]any{
			"id":                   record.ID,
			"reason_code":          record.ReasonCode,
			"amount_at_risk_paise": record.AmountAtRiskPaise,
			"evidence":             record.EvidenceJSON,
			"resolved_at":          record.ResolvedAt,
		})
	}

	response := map[string]any{"exceptions": exceptions}
	if hasMore {
		last := records[len(records)-1]
		response["next_cursor"] = encodeExceptionCursor(store.ExceptionCursor{AmountAtRiskPaise: last.AmountAtRiskPaise, ID: last.ID})
	}
	writeJSON(writer, http.StatusOK, response)
}

// exceptionCursorPayload is the JSON shape base64-encoded into a next_cursor value --
// implementation.md section 26's example ("next_cursor": "eyJpZCI6NDgyMX0=", which decodes to
// {"id":4821}) is a plain base64'd JSON object, not an opaque token with a bespoke encoding;
// this keeps that same inspectable shape, with amount_at_risk_paise added since this listing's
// keyset needs both halves of the (amount_at_risk_paise, id) ordering tuple to resume
// correctly, not just id alone.
type exceptionCursorPayload struct {
	AmountAtRiskPaise int64 `json:"amount_at_risk_paise"`
	ID                int64 `json:"id"`
}

func encodeExceptionCursor(cursor store.ExceptionCursor) string {
	payload, _ := json.Marshal(exceptionCursorPayload{AmountAtRiskPaise: cursor.AmountAtRiskPaise, ID: cursor.ID})
	return base64.StdEncoding.EncodeToString(payload)
}

func decodeExceptionCursor(raw string) (*store.ExceptionCursor, error) {
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var payload exceptionCursorPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return nil, err
	}
	return &store.ExceptionCursor{AmountAtRiskPaise: payload.AmountAtRiskPaise, ID: payload.ID}, nil
}

// resolveException rejects AI actors because AI is forbidden from resolving financial outcomes.
func (server Server) resolveException(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Actor string `json:"actor"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if strings.HasPrefix(strings.ToLower(payload.Actor), "ai:") {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "actor must be human or deterministic rule"})
		return
	}
	writeJSON(writer, http.StatusNotImplemented, map[string]string{"error": "resolution persistence is pending store integration"})
}

// explainException calls the AI explainer for one exception by ID, behind the same
// budget/circuit-breaker guardrails ai.Explainer.Explain always enforces. A budget-exceeded,
// breaker-open, or LLM-call failure never surfaces as an HTTP error -- it still returns 200
// with the exception's reason code and evidence, just without an explanation attached, per
// architecture.md section 9's boundary: the deterministic result must always be visible even
// when the AI layer degrades to nothing.
func (server Server) explainException(writer http.ResponseWriter, request *http.Request) {
	if server.Store == nil || server.Explainer == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "ai explainer not configured"})
		return
	}

	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid exception id"})
		return
	}

	record, err := server.Store.GetExceptionByID(request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrExceptionNotFound) {
			writeJSON(writer, http.StatusNotFound, map[string]string{"error": "exception not found"})
			return
		}
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "failed to load exception"})
		return
	}

	batchRunID := server.AdHocBatchRunID
	if batchRunID == "" {
		batchRunID = "api:adhoc"
	}

	explanation, explainErr := server.Explainer.Explain(request.Context(), batchRunID, ai.Exception{
		ID:                record.ID,
		ReasonCode:        record.ReasonCode,
		EvidenceJSON:      record.EvidenceJSON,
		AmountAtRiskPaise: record.AmountAtRiskPaise,
	})

	response := map[string]any{
		"reason_code": record.ReasonCode,
		"evidence":    record.EvidenceJSON,
	}
	switch {
	case explainErr == nil:
		response["text"] = explanation.Text
		response["prompt_version"] = explanation.PromptVersion
	case errors.Is(explainErr, ai.ErrBudgetExceeded), errors.Is(explainErr, ai.ErrCircuitOpen):
		response["explanation_skipped"] = explainErr.Error()
	default:
		log.Printf("AI Explainer failed: %v", explainErr)
		// A genuine LLM-call failure (including "not configured") still degrades to a 200
		// with the reason code only -- implementation.md section 24: an AI failure must
		// never surface as an operator-facing error, since the deterministic exception is
		// still fully usable without an explanation.
		response["explanation_skipped"] = "explanation temporarily unavailable"
	}
	writeJSON(writer, http.StatusOK, response)
}

// askQuestion answers one operator question through the Settlement Q&A agent. The request body
// is {"question": "..."}; the response is qa.Answer -- the answer text plus the raw evidence rows
// it came from, always together (implementation.md section 26). An LLM problem never surfaces
// here as an HTTP error: the agent degrades to a deterministic summary of the same rows, so the
// only 5xx paths are "not configured" (503) and a genuine store failure (500).
func (server Server) askQuestion(writer http.ResponseWriter, request *http.Request) {
	if server.QA == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "qa agent not configured"})
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, maxQARequestBytes)
	var payload struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	question := strings.TrimSpace(payload.Question)
	if question == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "question is required"})
		return
	}
	if len([]rune(question)) > maxQuestionRunes {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "question is too long"})
		return
	}

	answer, err := server.QA.Answer(request.Context(), question)
	if err != nil {
		log.Printf("qa agent failed: %v", err)
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "failed to answer question"})
		return
	}
	writeJSON(writer, http.StatusOK, answer)
}

// verifyChainTimeout bounds one full audit_log read plus hash verification.
const verifyChainTimeout = 30 * time.Second

// verifyChain re-verifies the audit_log hash chain end to end, the HTTP twin of
// `reconctl verify-chain` (implementation.md section 26). Both read the same rows the same way
// and call the same audit.Verify, so they always agree. A broken chain is a successful
// verification with a negative result, so it answers 200 with verified=false plus the first
// bad position -- only "not configured" (503) and a failure to read the table (500) are errors.
// first_break_at_row is the 0-based position in insertion order (id ASC), same as reconctl.
func (server Server) verifyChain(writer http.ResponseWriter, request *http.Request) {
	if server.Audit == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "audit log not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), verifyChainTimeout)
	defer cancel()

	entries, err := server.Audit.LoadAuditEntries(ctx)
	if err != nil {
		log.Printf("verify-chain: load audit_log failed: %v", err)
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "failed to read audit log"})
		return
	}

	brokenAt, verifyErr := audit.Verify(entries)
	if verifyErr != nil {
		writeJSON(writer, http.StatusOK, map[string]any{
			"verified":           false,
			"first_break_at_row": brokenAt,
			"detail":             verifyErr.Error(),
		})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"verified": true, "rows_checked": len(entries)})
}

// writeJSON writes one JSON response with a deterministic content type.
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
