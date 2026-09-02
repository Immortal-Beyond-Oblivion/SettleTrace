package ai

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

// fakeLLMClient is a hand-rolled LLMClient fake, following this repo's no-mocking-library
// convention (see internal/matcher/dispatcher_test.go's fakeReconStore).
type fakeLLMClient struct {
	response CompletionResponse
	err      error
	calls    []CompletionRequest
}

func (fake *fakeLLMClient) Complete(_ context.Context, req CompletionRequest) (CompletionResponse, error) {
	fake.calls = append(fake.calls, req)
	if fake.err != nil {
		return CompletionResponse{}, fake.err
	}
	return fake.response, nil
}

// fakeExplanationLogStore records every ai_explanation_log write in memory.
type fakeExplanationLogStore struct {
	entries []store.AIExplanationLogRow
}

func (fake *fakeExplanationLogStore) WriteAIExplanationLog(_ context.Context, log store.AIExplanationLogRow) error {
	fake.entries = append(fake.entries, log)
	return nil
}

func TestExplainer_Explain_SuccessLogsAndReturnsText(t *testing.T) {
	llm := &fakeLLMClient{response: CompletionResponse{Text: "No settlement line has arrived within the card SLA window yet."}}
	logStore := &fakeExplanationLogStore{}
	explainer := &Explainer{
		LLM:           llm,
		Store:         logStore,
		Budget:        newTestBudgetTracker(newFakeBudgetClient(), 2.00),
		Breaker:       NewCircuitBreaker(3, 30*time.Second),
		SystemPrompt:  "explain only from the evidence provided",
		PromptVersion: "v1",
		ModelName:     "claude-sonnet-4-6",
	}

	exc := Exception{ID: 42, ReasonCode: "NO_CANDIDATE_IN_WINDOW", EvidenceJSON: json.RawMessage(`{"window_days":5}`), AmountAtRiskPaise: 50000}
	explanation, err := explainer.Explain(context.Background(), "batch_1", exc)
	if err != nil {
		t.Fatalf("Explain returned error: %v", err)
	}
	if explanation.Text != llm.response.Text {
		t.Fatalf("expected the explanation text to match the LLM response, got %q", explanation.Text)
	}
	if explanation.PromptVersion != "v1" {
		t.Fatalf("expected prompt version v1, got %q", explanation.PromptVersion)
	}
	if len(logStore.entries) != 1 {
		t.Fatalf("expected exactly one ai_explanation_log row, got %d", len(logStore.entries))
	}
	entry := logStore.entries[0]
	if !entry.Succeeded || entry.ExceptionID != 42 || entry.OutputText != llm.response.Text {
		t.Fatalf("expected a succeeded log entry for exception 42 with the LLM output, got %+v", entry)
	}
}

func TestExplainer_Explain_LLMFailureDegradesAndLogsFailure(t *testing.T) {
	llm := &fakeLLMClient{err: errors.New("upstream timeout")}
	logStore := &fakeExplanationLogStore{}
	explainer := &Explainer{
		LLM:           llm,
		Store:         logStore,
		Budget:        newTestBudgetTracker(newFakeBudgetClient(), 2.00),
		Breaker:       NewCircuitBreaker(3, 30*time.Second),
		PromptVersion: "v1",
		ModelName:     "claude-sonnet-4-6",
	}

	exc := Exception{ID: 7, ReasonCode: "AMOUNT_MISMATCH_UNEXPLAINED", AmountAtRiskPaise: 1000}
	_, err := explainer.Explain(context.Background(), "batch_1", exc)
	if err == nil {
		t.Fatalf("expected Explain to return an error when the LLM call fails")
	}
	if len(logStore.entries) != 1 || logStore.entries[0].Succeeded {
		t.Fatalf("expected exactly one non-succeeded log entry, got %+v", logStore.entries)
	}
	if logStore.entries[0].ErrorMessage == "" {
		t.Fatalf("expected the failed log entry to carry the LLM error message")
	}
}

func TestExplainer_Explain_BudgetExceededSkipsLLMCallEntirely(t *testing.T) {
	llm := &fakeLLMClient{response: CompletionResponse{Text: "should never be reached"}}
	logStore := &fakeExplanationLogStore{}
	// Cap is 0.01 and every call costs 0.01, so the second call must be rejected.
	budgetClient := newFakeBudgetClient()
	tracker := newTestBudgetTracker(budgetClient, 0.01)
	explainer := &Explainer{
		LLM: llm, Store: logStore, Budget: tracker, Breaker: NewCircuitBreaker(3, 30*time.Second),
		PromptVersion: "v1", ModelName: "m", EstCostUSD: 0.01,
	}

	exc := Exception{ID: 1, ReasonCode: "X"}
	if _, err := explainer.Explain(context.Background(), "batch_1", exc); err != nil {
		t.Fatalf("expected the first call to succeed, got %v", err)
	}
	_, err := explainer.Explain(context.Background(), "batch_1", exc)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded on the second call, got %v", err)
	}
	if len(llm.calls) != 1 {
		t.Fatalf("expected the LLM to be called exactly once (never on the over-budget call), got %d calls", len(llm.calls))
	}
	if len(logStore.entries) != 2 || logStore.entries[1].Succeeded {
		t.Fatalf("expected a second, non-succeeded log entry recording the budget skip, got %+v", logStore.entries)
	}
}

func TestExplainer_Explain_OpenCircuitSkipsLLMCallEntirely(t *testing.T) {
	llm := &fakeLLMClient{response: CompletionResponse{Text: "should never be reached"}}
	logStore := &fakeExplanationLogStore{}
	breaker := NewCircuitBreaker(1, time.Hour)
	breaker.RecordFailure() // trip it before Explain is ever called
	explainer := &Explainer{
		LLM: llm, Store: logStore, Budget: newTestBudgetTracker(newFakeBudgetClient(), 2.00), Breaker: breaker,
		PromptVersion: "v1", ModelName: "m",
	}

	_, err := explainer.Explain(context.Background(), "batch_1", Exception{ID: 9, ReasonCode: "X"})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if len(llm.calls) != 0 {
		t.Fatalf("expected the LLM to never be called while the breaker is open, got %d calls", len(llm.calls))
	}
	if len(logStore.entries) != 1 || logStore.entries[0].Succeeded {
		t.Fatalf("expected one non-succeeded log entry for the breaker skip, got %+v", logStore.entries)
	}
}

func TestExplainer_Explain_NilBudgetAndBreakerStillWorks(t *testing.T) {
	// Explainer must remain usable with no budget tracker and no circuit breaker configured
	// at all (e.g. a local dev run with LLM_API_KEY set but no Redis) -- both guardrails are
	// optional add-ons, not hard dependencies of the explain path itself.
	llm := &fakeLLMClient{response: CompletionResponse{Text: "ok"}}
	logStore := &fakeExplanationLogStore{}
	explainer := &Explainer{LLM: llm, Store: logStore, PromptVersion: "v1", ModelName: "m"}

	explanation, err := explainer.Explain(context.Background(), "batch_1", Exception{ID: 1, ReasonCode: "X"})
	if err != nil {
		t.Fatalf("expected Explain to succeed with nil Budget/Breaker, got %v", err)
	}
	if explanation.Text != "ok" {
		t.Fatalf("expected the explanation text 'ok', got %q", explanation.Text)
	}
}
