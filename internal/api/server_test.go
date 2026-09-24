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
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
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

// fakeExceptionReader is a hand-rolled store.ExceptionReader (+ store.ExceptionLister) fake,
// following this repo's no-mocking-library convention (see internal/ai/explainer_test.go's
// fakeLLMClient). It satisfies both interfaces from one type -- same as MySQLStore does in
// production -- so tests for either explainException or listExceptions can share it without
// a second fake type.
type fakeExceptionReader struct {
	records map[int64]store.ExceptionRecord
	err     error // returned for any id not in records; defaults to store.ErrExceptionNotFound

	// list, listErr back ListUnresolvedExceptions. list is expected pre-sorted by the caller
	// (amount_at_risk_paise DESC, id DESC), matching what MySQLStore's real query guarantees --
	// this fake does no sorting of its own, same as fakeReconStore's fixtures elsewhere in
	// this repo trusting the test author to hand in data already shaped like the real query's
	// contract.
	list    []store.ExceptionRecord
	listErr error
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

func (fake *fakeExceptionReader) ListUnresolvedExceptions(_ context.Context, limit int, cursor *store.ExceptionCursor) ([]store.ExceptionRecord, error) {
	if fake.listErr != nil {
		return nil, fake.listErr
	}
	start := 0
	if cursor != nil {
		for i, record := range fake.list {
			if record.AmountAtRiskPaise == cursor.AmountAtRiskPaise && record.ID == cursor.ID {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(fake.list) {
		end = len(fake.list)
	}
	if start > end {
		start = end
	}
	return fake.list[start:end], nil
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

// TestListExceptions_FallsBackToInMemorySliceWhenStoreNotConfigured covers the zero-config
// dev smoke-test path: Store is nil (the default zero value), so listExceptions must serve
// whatever was injected into Exceptions rather than erroring or panicking on the type
// assertion to store.ExceptionLister.
func TestListExceptions_FallsBackToInMemorySliceWhenStoreNotConfigured(t *testing.T) {
	server := Server{Exceptions: []recon.Exception{{ReasonCode: "NO_CANDIDATE_IN_WINDOW"}}}
	request := httptest.NewRequest(http.MethodGet, "/v1/exceptions", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, response.Code)
	}
	body := decodeJSONBody(t, response)
	exceptions, ok := body["exceptions"].([]any)
	if !ok || len(exceptions) != 1 {
		t.Fatalf("expected the injected Exceptions slice to be served as-is, got %+v", body)
	}
	if _, hasCursor := body["next_cursor"]; hasCursor {
		t.Fatalf("did not expect a next_cursor on the in-memory fallback path, got %+v", body)
	}
}

// TestListExceptions_ReturnsDBBackedResultsWithoutNextCursorOnAShortPage covers the real,
// store-backed path added this session: when Store implements store.ExceptionLister and the
// page comes back shorter than the requested limit, that's the last page, so no next_cursor
// should be emitted.
func TestListExceptions_ReturnsDBBackedResultsWithoutNextCursorOnAShortPage(t *testing.T) {
	server := Server{
		Store: &fakeExceptionReader{list: []store.ExceptionRecord{
			{ID: 2, ReasonCode: "MULTI_CANDIDATE_AMBIGUOUS", AmountAtRiskPaise: 250000, EvidenceJSON: json.RawMessage(`{"candidates":3}`)},
			{ID: 1, ReasonCode: "NO_CANDIDATE_IN_WINDOW", AmountAtRiskPaise: 5000, EvidenceJSON: json.RawMessage(`{}`)},
		}},
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/exceptions", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, response.Code)
	}
	body := decodeJSONBody(t, response)
	exceptions, ok := body["exceptions"].([]any)
	if !ok || len(exceptions) != 2 {
		t.Fatalf("expected both fixture rows, got %+v", body)
	}
	first, ok := exceptions[0].(map[string]any)
	if !ok || first["reason_code"] != "MULTI_CANDIDATE_AMBIGUOUS" {
		t.Fatalf("expected the worst (highest amount-at-risk) row first, got %+v", exceptions)
	}
	if _, hasCursor := body["next_cursor"]; hasCursor {
		t.Fatalf("did not expect a next_cursor when the page is shorter than the limit, got %+v", body)
	}
}

// TestListExceptions_SetsNextCursorOnAFullPageAndItRoundTrips covers pagination end to end:
// a full page (limit=1 against two fixture rows) must carry a next_cursor, and re-requesting
// with that exact cursor must resume from the second row, not repeat or skip it.
func TestListExceptions_SetsNextCursorOnAFullPageAndItRoundTrips(t *testing.T) {
	fake := &fakeExceptionReader{list: []store.ExceptionRecord{
		{ID: 2, ReasonCode: "MULTI_CANDIDATE_AMBIGUOUS", AmountAtRiskPaise: 250000, EvidenceJSON: json.RawMessage(`{}`)},
		{ID: 1, ReasonCode: "NO_CANDIDATE_IN_WINDOW", AmountAtRiskPaise: 5000, EvidenceJSON: json.RawMessage(`{}`)},
	}}
	server := Server{Store: fake}

	firstRequest := httptest.NewRequest(http.MethodGet, "/v1/exceptions?limit=1", nil)
	firstResponse := httptest.NewRecorder()
	server.Routes().ServeHTTP(firstResponse, firstRequest)
	firstBody := decodeJSONBody(t, firstResponse)

	cursor, ok := firstBody["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("expected a next_cursor on a full page, got %+v", firstBody)
	}

	secondRequest := httptest.NewRequest(http.MethodGet, "/v1/exceptions?limit=1&cursor="+cursor, nil)
	secondResponse := httptest.NewRecorder()
	server.Routes().ServeHTTP(secondResponse, secondRequest)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, secondResponse.Code)
	}
	secondBody := decodeJSONBody(t, secondResponse)
	exceptions, ok := secondBody["exceptions"].([]any)
	if !ok || len(exceptions) != 1 {
		t.Fatalf("expected exactly the second fixture row, got %+v", secondBody)
	}
	row, ok := exceptions[0].(map[string]any)
	if !ok || row["reason_code"] != "NO_CANDIDATE_IN_WINDOW" {
		t.Fatalf("expected the cursor to resume at the second row, got %+v", exceptions)
	}
	if _, hasCursor := secondBody["next_cursor"]; hasCursor {
		t.Fatalf("expected no next_cursor once the last row has been returned, got %+v", secondBody)
	}
}

// TestListExceptions_InvalidCursorReturns400 guards against a malformed/tampered cursor value
// being silently treated as "no cursor" (which would restart pagination from the top instead
// of surfacing the caller's mistake) or crashing the handler.
func TestListExceptions_InvalidCursorReturns400(t *testing.T) {
	server := Server{Store: &fakeExceptionReader{}}
	request := httptest.NewRequest(http.MethodGet, "/v1/exceptions?cursor=not-valid-base64!!", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, response.Code)
	}
}

// TestListExceptions_StoreErrorReturns500 covers the real-query-failed path distinctly from
// the not-configured (nil Store) fallback above -- a store that IS configured but returns an
// error must not be silently swallowed into an empty 200.
func TestListExceptions_StoreErrorReturns500(t *testing.T) {
	server := Server{Store: &fakeExceptionReader{listErr: errors.New("connection refused")}}
	request := httptest.NewRequest(http.MethodGet, "/v1/exceptions", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected %d, got %d", http.StatusInternalServerError, response.Code)
	}
}
