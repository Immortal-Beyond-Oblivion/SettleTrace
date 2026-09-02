// Package api exposes the minimal read-only operator HTTP surface.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"log"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/ai"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

// Server serves batch and exception responses from an injected read model.
type Server struct {
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
}

// Routes returns the configured HTTP handler tree.
func (server Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("GET /v1/exceptions", server.listExceptions)
	mux.HandleFunc("POST /v1/exceptions/", server.resolveException)
	mux.HandleFunc("POST /v1/exceptions/{id}/explain", server.explainException)
	return mux
}

// health reports process readiness without exposing dependencies or secrets.
func (server Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

// listExceptions returns the current read model in its amount-at-risk ordering.
func (server Server) listExceptions(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"exceptions": server.Exceptions})
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

// writeJSON writes one JSON response with a deterministic content type.
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
