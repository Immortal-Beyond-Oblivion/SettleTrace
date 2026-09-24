package qa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/ai"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

// Answer sources reported to the caller, so an operator can always tell a model-phrased answer
// from a plain deterministic summary of the same rows.
const (
	SourceLLM           = "llm"
	SourceDeterministic = "deterministic"
)

// Answer is what POST /v1/qa returns. EvidenceRows is always present and always the raw rows
// the answer was derived from -- implementation.md section 2.4's "return answer + the raw
// rows, together, always" -- so a reader can check any sentence against the data, and so the
// deterministic result stays visible even when the LLM layer degrades to nothing.
type Answer struct {
	Text           string           `json:"answer"`
	Intent         Intent           `json:"intent"`
	EvidenceRows   []map[string]any `json:"evidence_rows"`
	AnswerSource   string           `json:"answer_source"`
	PromptVersion  string           `json:"prompt_version,omitempty"`
	DegradedReason string           `json:"degraded_reason,omitempty"`
}

// Agent answers operator questions from a fixed set of read-only store queries. Every field
// except Store is optional and degrades gracefully when unset -- the same convention as
// ai.Explainer: a nil LLM, Budget, or Breaker means "no AI phrasing / no cap / no breaker,"
// never a failure. Store is the one hard dependency; without it there is nothing to answer from.
type Agent struct {
	Store store.QAStore
	LLM   ai.LLMClient

	Budget  *ai.BudgetTracker
	Breaker *ai.CircuitBreaker

	SystemPrompt  string
	PromptVersion string

	// BudgetKey is the ai.BudgetTracker bucket Q&A calls are charged to. Defaults to "api:qa" --
	// a question isn't part of any matching-engine batch run, so (like the explainer's
	// "api:adhoc") it draws on one fixed, always-on bucket rather than a synthesized batch ID.
	BudgetKey  string
	MaxTokens  int
	Timeout    time.Duration
	EstCostUSD float64

	// Now is overridable in tests; production callers leave it nil and get the real UTC clock.
	Now func() time.Time
}

// unsupportedText is returned verbatim for a question no rule recognizes. It never touches the
// store or the LLM: an unrecognized question gets a fixed, honest answer, not a guess.
const unsupportedText = "I can only answer three kinds of questions: why a specific payment (give its pay_ ID) " +
	"has not settled, how much is currently at risk in unresolved exceptions, and which payment " +
	"methods have the worst match rate recently."

// Answer classifies the question, runs exactly one fixed store query, builds a deterministic
// summary of the rows, and -- only if there are rows and the budget/breaker/LLM allow -- asks the
// LLM to rephrase them. Any LLM-side problem (unconfigured, over budget, breaker open, call
// failure, empty output, or output that references data outside the rows) degrades to the
// deterministic summary with a DegradedReason; only a store failure returns an error.
func (agent *Agent) Answer(ctx context.Context, question string) (Answer, error) {
	if agent == nil || agent.Store == nil {
		return Answer{}, errors.New("qa: agent has no store configured")
	}

	classification := Classify(question, agent.clock())

	var (
		summary  string
		evidence []map[string]any
	)
	switch classification.Intent {
	case IntentWhyNotSettled:
		rows, err := agent.Store.WhyNotSettled(ctx, classification.PaymentID)
		if err != nil {
			return Answer{}, fmt.Errorf("qa: why not settled: %w", err)
		}
		summary, evidence = summarizeWhyNotSettled(classification.PaymentID, rows)
	case IntentAmountAtRisk:
		rows, err := agent.Store.UnresolvedAmountByReason(ctx)
		if err != nil {
			return Answer{}, fmt.Errorf("qa: unresolved amount by reason: %w", err)
		}
		summary, evidence = summarizeAmountAtRisk(rows)
	case IntentWorstMethod:
		rows, err := agent.Store.WorstMethodsByMatchRate(ctx, classification.Since, 5)
		if err != nil {
			return Answer{}, fmt.Errorf("qa: worst methods by match rate: %w", err)
		}
		summary, evidence = summarizeWorstMethods(classification.Since, rows)
	default:
		return Answer{
			Text:         unsupportedText,
			Intent:       IntentUnsupported,
			EvidenceRows: []map[string]any{},
			AnswerSource: SourceDeterministic,
		}, nil
	}

	answer := Answer{
		Text:         summary,
		Intent:       classification.Intent,
		EvidenceRows: evidence,
		AnswerSource: SourceDeterministic,
	}

	// No rows means nothing for a model to phrase -- and the surest way to invite an invented
	// answer is to ask a model to write one from an empty table. The deterministic summary
	// already states plainly that nothing was found.
	if len(evidence) == 0 {
		return answer, nil
	}

	text, reason := agent.compose(ctx, question, classification.Intent, evidence)
	if reason != "" {
		answer.DegradedReason = reason
		return answer, nil
	}
	answer.Text = text
	answer.AnswerSource = SourceLLM
	answer.PromptVersion = agent.PromptVersion
	return answer, nil
}

// compose runs the guardrailed LLM call. It returns either the model's answer text (reason
// empty) or an empty text plus a human-readable reason the caller should fall back to the
// deterministic summary. It never returns an error: every failure here is a degradation.
func (agent *Agent) compose(ctx context.Context, question string, intent Intent, evidence []map[string]any) (string, string) {
	if agent.LLM == nil {
		return "", "ai answer composition not configured"
	}

	// Same nil semantics as ai.Explainer.Explain: a nil Budget/Breaker pointer means "not
	// configured -> no cap / no breaker," so both checks are guarded on the pointer itself.
	if agent.Budget != nil {
		ok, err := agent.Budget.CheckAndReserve(ctx, agent.budgetKey(), agent.estCost())
		if err != nil {
			return "", "ai budget check failed"
		}
		if !ok {
			return "", ai.ErrBudgetExceeded.Error()
		}
	}
	if !agent.Breaker.Allow() {
		return "", ai.ErrCircuitOpen.Error()
	}

	// The user prompt is one JSON object: the question (as data, not instructions), the
	// classified intent, and the rows. The system prompt tells the model to answer only from
	// "rows" and to treat "question" as text to answer, never as instructions to follow.
	payload, err := json.Marshal(struct {
		Question string           `json:"question"`
		Intent   Intent           `json:"intent"`
		Rows     []map[string]any `json:"rows"`
	}{Question: question, Intent: intent, Rows: evidence})
	if err != nil {
		return "", "could not encode evidence rows for the model"
	}

	callCtx, cancel := context.WithTimeout(ctx, agent.timeout())
	defer cancel()

	response, err := agent.LLM.Complete(callCtx, ai.CompletionRequest{
		SystemPrompt: agent.SystemPrompt,
		UserPrompt:   string(payload),
		MaxTokens:    agent.maxTokens(),
	})
	if err != nil {
		agent.Breaker.RecordFailure()
		return "", "answer composition temporarily unavailable"
	}

	text := strings.TrimSpace(response.Text)
	if text == "" {
		agent.Breaker.RecordFailure()
		return "", "ai returned an empty answer"
	}
	agent.Breaker.RecordSuccess()

	// Grounding check: the model may only mention payment IDs that appear in the question or
	// the rows. A cheap, deterministic backstop for the "cite only these rows" prompt rule --
	// if the answer names a payment we never gave it, we don't show that text.
	if !groundedInEvidence(text, question, payload) {
		return "", "ai answer referenced data outside the evidence rows; showing the deterministic summary instead"
	}
	return text, ""
}

// groundedInEvidence reports whether every pay_ ID in text also appears in the question or in
// the JSON payload the model was given.
func groundedInEvidence(text, question string, payload []byte) bool {
	allowed := question + " " + string(payload)
	for _, id := range paymentIDPattern.FindAllString(text, -1) {
		if !strings.Contains(allowed, id) {
			return false
		}
	}
	return true
}

func summarizeWhyNotSettled(paymentID string, rows []store.WhyNotSettledRow) (string, []map[string]any) {
	evidence := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		evidence = append(evidence, map[string]any{
			"reason_code":          row.ReasonCode,
			"amount_at_risk_paise": row.AmountAtRiskPaise,
			"resolved_at":          row.ResolvedAt,
			"evidence":             row.EvidenceJSON,
		})
	}
	if len(rows) == 0 {
		return fmt.Sprintf("No exceptions are on record for %s, so the exception log has nothing to say about it. "+
			"That does not by itself confirm the payment settled.", paymentID), evidence
	}
	latest := rows[0]
	state := "still unresolved"
	if latest.ResolvedAt != nil {
		state = "resolved at " + latest.ResolvedAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s has %s on record. The most recent has reason %s with %s at risk and is %s.",
		paymentID, plural(len(rows), "exception"), latest.ReasonCode, formatINR(latest.AmountAtRiskPaise), state), evidence
}

func summarizeAmountAtRisk(rows []store.ReasonAggregateRow) (string, []map[string]any) {
	evidence := make([]map[string]any, 0, len(rows))
	var totalPaise int64
	var totalCount int
	for _, row := range rows {
		evidence = append(evidence, map[string]any{
			"reason_code":         row.ReasonCode,
			"total_at_risk_paise": row.TotalAtRiskPaise,
			"count":               row.Count,
		})
		totalPaise += row.TotalAtRiskPaise
		totalCount += row.Count
	}
	if len(rows) == 0 {
		return "There are no unresolved exceptions right now.", evidence
	}
	top := rows[0]
	return fmt.Sprintf("There are %s with %s at risk in total. The largest share is %s: %s across %s.",
		plural(totalCount, "unresolved exception"), formatINR(totalPaise),
		top.ReasonCode, formatINR(top.TotalAtRiskPaise), plural(top.Count, "exception")), evidence
}

func summarizeWorstMethods(since time.Time, rows []store.MethodMatchRateRow) (string, []map[string]any) {
	evidence := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		evidence = append(evidence, map[string]any{
			"method":     row.Method,
			"match_rate": row.MatchRate,
			"since":      since.UTC().Format(time.RFC3339),
		})
	}
	day := since.UTC().Format("2006-01-02")
	if len(rows) == 0 {
		return fmt.Sprintf("No captured payments were found since %s, so no method match rates can be computed.", day), evidence
	}
	worst := rows[0]
	return fmt.Sprintf("Among payments captured since %s, %s has the lowest match rate at %.1f%% (%d method(s) compared).",
		day, worst.Method, worst.MatchRate*100, len(rows)), evidence
}

// formatINR renders paise as a plain INR amount, e.g. 50000 -> "INR 500.00".
func formatINR(paise int64) string {
	sign := ""
	if paise < 0 {
		sign = "-"
		paise = -paise
	}
	return fmt.Sprintf("%sINR %d.%02d", sign, paise/100, paise%100)
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func (agent *Agent) clock() time.Time {
	if agent.Now != nil {
		return agent.Now()
	}
	return time.Now().UTC()
}

func (agent *Agent) budgetKey() string {
	if agent.BudgetKey != "" {
		return agent.BudgetKey
	}
	return "api:qa"
}

func (agent *Agent) timeout() time.Duration {
	if agent.Timeout > 0 {
		return agent.Timeout
	}
	return 5 * time.Second
}

func (agent *Agent) maxTokens() int {
	if agent.MaxTokens > 0 {
		return agent.MaxTokens
	}
	return 400
}

func (agent *Agent) estCost() float64 {
	if agent.EstCostUSD > 0 {
		return agent.EstCostUSD
	}
	return 0.01
}
