package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/ai/qa"
)

// stubQAAnswerer is a hand-rolled QAAnswerer, following this repo's no-mocking-library
// convention. It records the question it was handed so tests can check what reached the agent.
type stubQAAnswerer struct {
	answer qa.Answer
	err    error
	calls  int
	asked  string
}

func (stub *stubQAAnswerer) Answer(_ context.Context, question string) (qa.Answer, error) {
	stub.calls++
	stub.asked = question
	return stub.answer, stub.err
}

func postQA(server Server, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/qa", strings.NewReader(body))
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	return response
}

func TestAskQuestion_NotConfiguredReturns503(t *testing.T) {
	response := postQA(Server{}, `{"question":"how much is at risk?"}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected %d, got %d", http.StatusServiceUnavailable, response.Code)
	}
}

func TestAskQuestion_InvalidJSONReturns400(t *testing.T) {
	stub := &stubQAAnswerer{}
	response := postQA(Server{QA: stub}, `{not json`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, response.Code)
	}
	if stub.calls != 0 {
		t.Errorf("agent called %d times for an invalid body; want 0", stub.calls)
	}
}

func TestAskQuestion_BlankQuestionReturns400(t *testing.T) {
	stub := &stubQAAnswerer{}
	response := postQA(Server{QA: stub}, `{"question":"   "}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, response.Code)
	}
	if stub.calls != 0 {
		t.Errorf("agent called %d times for a blank question; want 0", stub.calls)
	}
}

func TestAskQuestion_OverlongQuestionReturns400(t *testing.T) {
	stub := &stubQAAnswerer{}
	body, err := json.Marshal(map[string]string{"question": strings.Repeat("a", maxQuestionRunes+1)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	response := postQA(Server{QA: stub}, string(body))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, response.Code)
	}
	if stub.calls != 0 {
		t.Errorf("agent called %d times for an overlong question; want 0", stub.calls)
	}
}

func TestAskQuestion_AgentErrorReturns500WithoutLeakingDetail(t *testing.T) {
	stub := &stubQAAnswerer{err: errors.New("dial tcp 10.0.0.5:3306: connection refused")}
	response := postQA(Server{QA: stub}, `{"question":"how much is at risk?"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected %d, got %d", http.StatusInternalServerError, response.Code)
	}
	if strings.Contains(response.Body.String(), "10.0.0.5") {
		t.Errorf("internal error detail leaked to the client: %s", response.Body.String())
	}
}

func TestAskQuestion_SuccessReturnsAnswerAndEvidenceRows(t *testing.T) {
	stub := &stubQAAnswerer{answer: qa.Answer{
		Text:         "pay_H8x92k4Rt is unresolved with reason NO_CANDIDATE_IN_WINDOW.",
		Intent:       qa.IntentWhyNotSettled,
		EvidenceRows: []map[string]any{{"reason_code": "NO_CANDIDATE_IN_WINDOW", "amount_at_risk_paise": 50000}},
		AnswerSource: qa.SourceDeterministic,
	}}
	response := postQA(Server{QA: stub}, `{"question":"  why didn't pay_H8x92k4Rt settle this week?  "}`)
	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
	if stub.asked != "why didn't pay_H8x92k4Rt settle this week?" {
		t.Errorf("agent was asked %q; the question should be trimmed", stub.asked)
	}

	var decoded struct {
		Answer       string           `json:"answer"`
		Intent       string           `json:"intent"`
		EvidenceRows []map[string]any `json:"evidence_rows"`
		AnswerSource string           `json:"answer_source"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Answer == "" || decoded.Intent != "why_not_settled" || decoded.AnswerSource != "deterministic" {
		t.Errorf("decoded = %+v", decoded)
	}
	if len(decoded.EvidenceRows) != 1 || decoded.EvidenceRows[0]["reason_code"] != "NO_CANDIDATE_IN_WINDOW" {
		t.Errorf("evidence rows = %#v", decoded.EvidenceRows)
	}
}
