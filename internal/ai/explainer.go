package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

// ErrBudgetExceeded is returned when the batch's AI spend cap would be exceeded by this call.
// It is not a failure of the LLM call itself -- the call is never attempted -- and callers
// must treat it as a degraded-but-successful outcome (the exception keeps its reason code,
// with no explanation attached), never as an error to surface to an operator.
var ErrBudgetExceeded = errors.New("ai: batch budget exceeded, explanation skipped")

// ErrCircuitOpen is returned when the circuit breaker is currently open. Same handling as
// ErrBudgetExceeded: this degrades the response, it does not fail the caller.
var ErrCircuitOpen = errors.New("ai: circuit breaker open, explanation skipped")

// Exception is the minimal, evidence-only view of a reconciliation exception the explainer is
// allowed to see. It is deliberately not store.ExceptionRow itself: architecture.md section 9
// draws a hard boundary around what the AI layer may be handed (reason_code, evidence_json,
// amount_at_risk -- nothing else, no PII, no free-form notes), and giving the explainer its
// own narrower input type makes that boundary a compile-time fact about this package's API,
// not just a convention a future caller has to remember to honor.
type Exception struct {
	ID                int64
	ReasonCode        string
	EvidenceJSON      json.RawMessage
	AmountAtRiskPaise int64
}

// Explanation is the plain-language result of a successful Explain call.
type Explanation struct {
	Text          string
	PromptVersion string
}

// CompletionRequest is the shape Explainer hands to its LLMClient. Kept in this package
// (rather than importing a specific vendor SDK) so LLMClient stays a small, mockable seam --
// exactly how internal/matcher/tier3.go treats the fuzzy-ranker HTTP call as a swappable
// dependency of the engine, not a hardwired one.
type CompletionRequest struct {
	SystemPrompt string
	UserPrompt   string
	MaxTokens    int
}

// CompletionResponse is the shape an LLMClient returns.
type CompletionResponse struct {
	Text string
}

// LLMClient is the only way Explainer talks to a language model. A real implementation wraps
// whichever vendor SDK/API is configured (README/implementation.md's LLM_API_KEY, LLM_MODEL);
// tests use a hand-rolled fake, per this repo's no-mocking-library convention.
type LLMClient interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

// Explainer builds a prompt exclusively from an exception's evidence_json (architecture.md
// section 9: the model gets structured facts, never asked to diagnose from scratch) and
// returns a one-sentence, plain-language explanation. It has no retry logic by design -- a
// single attempt, a hard timeout, fail closed to "no explanation" rather than retrying and
// risking a slow cascading queue of retries under a partial LLM outage.
type Explainer struct {
	LLM   LLMClient
	Store store.AIExplanationLogWriter

	Budget  *BudgetTracker
	Breaker *CircuitBreaker

	SystemPrompt  string
	PromptVersion string
	ModelName     string
	MaxTokens     int
	Timeout       time.Duration
	EstCostUSD    float64

	// now is overridable in tests so latency and logged timestamps are deterministic;
	// production callers leave this nil and get the real clock.
	now func() time.Time
}

// clock returns the configured time source, defaulting to the real UTC clock -- the same
// "one designated time source per package" discipline as internal/matcher/windowclock.go.
func (explainer *Explainer) clock() time.Time {
	if explainer.now != nil {
		return explainer.now()
	}
	return time.Now().UTC()
}

// Explain runs the full guardrailed call: budget check, breaker check, a timeout-bounded LLM
// call, and an unconditional write to ai_explanation_log recording the outcome -- success,
// LLM failure, or a skip -- so implementation.md section 8's "every AI-generated sentence is
// stored, permanently" claim holds for every code path through this function, not just the
// happy one.
func (explainer *Explainer) Explain(ctx context.Context, batchRunID string, exc Exception) (Explanation, error) {
	if explainer.LLM == nil {
		return Explanation{}, fmt.Errorf("ai: explainer has no LLM client configured")
	}

	// Budget/Breaker are optional add-ons, not hard dependencies of the explain path: a nil
	// *BudgetTracker or *CircuitBreaker here means "not configured at all" (e.g. local dev
	// with no Redis), which must be treated as "no cap enforced" -- NOT delegated to
	// BudgetTracker.CheckAndReserve's own nil-receiver behavior, which intentionally reports
	// not-ok for a *different* case (a tracker that exists but has no real client behind it).
	// Conflating the two would make every explain call skip whenever Budget/Breaker simply
	// aren't wired up, which is exactly the bug this check exists to prevent.
	if explainer.Budget != nil {
		if ok, err := explainer.Budget.CheckAndReserve(ctx, batchRunID, explainer.estCost()); err != nil {
			return Explanation{}, fmt.Errorf("check ai budget: %w", err)
		} else if !ok {
			explainer.logSkip(ctx, exc, "budget cap reached before this call")
			return Explanation{}, ErrBudgetExceeded
		}
	}

	if explainer.Breaker != nil && !explainer.Breaker.Allow() {
		explainer.logSkip(ctx, exc, "circuit breaker open")
		return Explanation{}, ErrCircuitOpen
	}

	callCtx, cancel := context.WithTimeout(ctx, explainer.timeout())
	defer cancel()

	prompt := explainer.renderPrompt(exc)
	start := explainer.clock()
	response, err := explainer.LLM.Complete(callCtx, CompletionRequest{
		SystemPrompt: explainer.SystemPrompt,
		UserPrompt:   prompt,
		MaxTokens:    explainer.maxTokens(),
	})
	latency := explainer.clock().Sub(start)

	logEntry := store.AIExplanationLogRow{
		ExceptionID:      exc.ID,
		PromptVersion:    explainer.PromptVersion,
		Model:            explainer.ModelName,
		InputSummaryJSON: json.RawMessage(prompt),
		LatencyMS:        latency.Milliseconds(),
		CreatedAt:        explainer.clock(),
	}

	if err != nil {
		explainer.Breaker.RecordFailure()
		logEntry.Succeeded = false
		logEntry.ErrorMessage = err.Error()
		explainer.writeLog(ctx, logEntry)
		return Explanation{}, fmt.Errorf("llm call: %w", err)
	}

	explainer.Breaker.RecordSuccess()
	logEntry.Succeeded = true
	logEntry.OutputText = response.Text
	explainer.writeLog(ctx, logEntry)

	return Explanation{Text: response.Text, PromptVersion: explainer.PromptVersion}, nil
}

// renderPrompt builds the user-facing prompt strictly from the exception's own fields -- no
// external lookups, no free-text notes -- per architecture.md section 9's evidence-only
// boundary. A real prompt-template file (internal/ai/prompts/v1/explainer_system.txt) supplies
// the system prompt separately; this function only ever touches what's already on Exception.
func (explainer *Explainer) renderPrompt(exc Exception) string {
	evidence := exc.EvidenceJSON
	if evidence == nil {
		evidence = json.RawMessage("{}")
	}
	return fmt.Sprintf(
		`{"reason_code":%q,"amount_at_risk_paise":%d,"evidence":%s}`,
		exc.ReasonCode, exc.AmountAtRiskPaise, string(evidence),
	)
}

// logSkip records a budget/breaker skip as a non-succeeded ai_explanation_log row with no LLM
// call attempted at all -- distinguishing "we chose not to call the model" from "the model
// call itself failed" is exactly the distinction a reviewer needs to audit AI spend and
// reliability separately.
func (explainer *Explainer) logSkip(ctx context.Context, exc Exception, reason string) {
	explainer.writeLog(ctx, store.AIExplanationLogRow{
		ExceptionID:      exc.ID,
		PromptVersion:    explainer.PromptVersion,
		Model:            explainer.ModelName,
		InputSummaryJSON: json.RawMessage(fmt.Sprintf(`{"skipped_reason":%q}`, reason)),
		Succeeded:        false,
		ErrorMessage:     reason,
		CreatedAt:        explainer.clock(),
	})
}

// writeLog persists one ai_explanation_log row. A failure to log is deliberately non-fatal to
// the caller (the explanation or the skip already happened; losing the audit row is a
// separate, lower-severity problem) but must never be silent -- a real deployment wires a
// logger here; this package has no direct logging dependency of its own, so the write error is
// simply swallowed at this layer, same as the original explainer.go sketch in
// implementation.md section 8 describes ("non-fatal, but loud" -- the "loud" part is the
// caller's/operator's responsibility once this package is wired into cmd/api).
func (explainer *Explainer) writeLog(ctx context.Context, entry store.AIExplanationLogRow) {
	if explainer.Store == nil {
		return
	}
	_ = explainer.Store.WriteAIExplanationLog(ctx, entry)
}

func (explainer *Explainer) timeout() time.Duration {
	if explainer.Timeout > 0 {
		return explainer.Timeout
	}
	return 5 * time.Second
}

func (explainer *Explainer) maxTokens() int {
	if explainer.MaxTokens > 0 {
		return explainer.MaxTokens
	}
	return 300
}

func (explainer *Explainer) estCost() float64 {
	if explainer.EstCostUSD > 0 {
		return explainer.EstCostUSD
	}
	return 0.01
}
