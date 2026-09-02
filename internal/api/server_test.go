package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/ai"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

// TestResolveExceptionRejectsAI verifies the HTTP boundary rejects AI mutation attempts.
func TestResolveExceptionRejectsAI(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/exceptions/1/resolve", bytes.NewBufferString(`{"actor":"ai:explainer"}`))
	response := httptest.NewRecorder()
	Server{}.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, response.Code)
	}
}

// fakeExceptionReader is a hand-rolled store.ExceptionReader fake, following this repo's
// no-mocking-library convention (see internal/ai/explainer_test.go's fakeLLMClient).
type fakeExceptionReader struct {
	records map[int64]store.ExceptionRecord
	err     error // returned for any id not in records; defaults to store.ErrExceptionNotFound
}

func (fake *fakeExceptionReader) GetExceptionByID(_ context.Context, id int64) (store.ExceptionRecord, error) {
	if record, ok := fake.records[id]; ok {
		return record, nil
	}
	if fake.err != nil {
		return store.ExceptionRecord{}, fake.err
	}
	return store.ExceptionRecord{}, store.ErrExceptionNotFound
}

// fakeLLMClient is a hand-rolled ai.LLMClient fake, mirroring internal/ai/explainer_test.go's
// fakeLLMClient -- redefined here since that one is unexported inside package ai.
type fakeLLMClient struct {
	response ai.CompletionResponse
	err      error
}

func (fake *fakeLLMClient) Complete(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
	if fake.err != nil {
		return ai.CompletionResponse{}, fake.err
	}
	return fake.response, nil
}

func decodeJSONBody(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	return body
}

func TestExplainException_NotConfiguredReturns503(t *testing.T) {
	// Both Store and Explainer nil -- the "not configured" case every other guardrail in
	// this repo degrades to, never a panic (see server.go's doc comment on these fields).
	request := httptest.NewRequest(http.MethodPost, "/v1/exceptions/1/explain", nil)
	response := httptest.NewRecorder()
	Server{}.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected %d, got %d", http.StatusServiceUnavailable, response.Code)
	}
}

func TestExplainException_InvalidIDReturns400(t *testing.T) {
	server := Server{
		Store:     &fakeExceptionReader{},
		Explainer: &ai.Explainer{LLM: &fakeLLMClient{}, PromptVersion: "v1", ModelName: "m"},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/exceptions/not-a-number/explain", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, response.Code)
	}
}

func TestExplainException_UnknownIDReturns404(t *testing.T) {
	server := Server{
		Store:     &fakeExceptionReader{records: map[int64]store.ExceptionRecord{}},
		Explainer: &ai.Explainer{LLM: &fakeLLMClient{}, PromptVersion: "v1", ModelName: "m"},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/exceptions/999/explain", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, response.Code)
	}
}

func TestExplainException_StoreErrorReturns500(t *testing.T) {
	server := Server{
		Store:     &fakeExceptionReader{err: errors.New("connection refused")},
		Explainer: &ai.Explainer{LLM: &fakeLLMClient{}, PromptVersion: "v1", ModelName: "m"},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/exceptions/5/explain", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected %d, got %d", http.StatusInternalServerError, response.Code)
	}
}

func TestExplainException_SuccessReturns200WithTextAndEvidence(t *testing.T) {
	server := Server{
		Store: &fakeExceptionReader{records: map[int64]store.ExceptionRecord{
			1: {ID: 1, ReasonCode: "NO_CANDIDATE_IN_WINDOW", EvidenceJSON: json.RawMessage(`{"candidates_checked":0}`), AmountAtRiskPaise: 5000},
		}},
		Explainer: &ai.Explainer{
			LLM:           &fakeLLMClient{response: ai.CompletionResponse{Text: "no settlement line has arrived yet"}},
			PromptVersion: "v1",
			ModelName:     "m",
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/exceptions/1/explain", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, response.Code)
	}
	body := decodeJSONBody(t, response)
	if body["reason_code"] != "NO_CANDIDATE_IN_WINDOW" {
		t.Fatalf("expected reason_code to be present, got %+v", body)
	}
	if body["text"] != "no settlement line has arrived yet" {
		t.Fatalf("expected the explanation text, got %+v", body)
	}
	if body["prompt_version"] != "v1" {
		t.Fatalf("expected prompt_version v1, got %+v", body)
	}
	if _, skipped := body["explanation_skipped"]; skipped {
		t.Fatalf("did not expect explanation_skipped on a successful call, got %+v", body)
	}
}

func TestExplainException_CircuitOpenDegradesTo200WithExplanationSkipped(t *testing.T) {
	breaker := ai.NewCircuitBreaker(1, time.Hour)
	breaker.RecordFailure() // trips the breaker before Explain is ever called
	server := Server{
		Store: &fakeExceptionReader{records: map[int64]store.ExceptionRecord{
			1: {ID: 1, ReasonCode: "NO_CANDIDATE_IN_WINDOW", EvidenceJSON: json.RawMessage(`{}`)},
		}},
		Explainer: &ai.Explainer{
			LLM:           &fakeLLMClient{response: ai.CompletionResponse{Text: "should never be reached"}},
			Breaker:       breaker,
			PromptVersion: "v1",
			ModelName:     "m",
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/exceptions/1/explain", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected %d even when the breaker is open, got %d", http.StatusOK, response.Code)
	}
	body := decodeJSONBody(t, response)
	if body["reason_code"] != "NO_CANDIDATE_IN_WINDOW" {
		t.Fatalf("expected the deterministic reason_code to still be present, got %+v", body)
	}
	skipped, _ := body["explanation_skipped"].(string)
	if skipped == "" {
		t.Fatalf("expected explanation_skipped to be set, got %+v", body)
	}
	if _, hasText := body["text"]; hasText {
		t.Fatalf("did not expect a text field when the breaker is open, got %+v", body)
	}
}

func TestExplainException_LLMFailureDegradesTo200WithGenericSkipMessage(t *testing.T) {
	server := Server{
		Store: &fakeExceptionReader{records: map[int64]store.ExceptionRecord{
			1: {ID: 1, ReasonCode: "AMOUNT_MISMATCH_UNEXPLAINED", EvidenceJSON: json.RawMessage(`{}`)},
		}},
		Explainer: &ai.Explainer{
			LLM:           &fakeLLMClient{err: errors.New("gemini api error (status 400): API_KEY_INVALID")},
			PromptVersion: "v1",
			ModelName:     "m",
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/exceptions/1/explain", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected %d even when the LLM call fails, got %d", http.StatusOK, response.Code)
	}
	body := decodeJSONBody(t, response)
	// server.go's default branch deliberately reports a generic message here -- the real
	// LLM error is logged server-side (log.Printf), never leaked into the HTTP response.
	if body["explanation_skipped"] != "explanation temporarily unavailable" {
		t.Fatalf("expected the generic skip message, got %+v", body)
	}
}
