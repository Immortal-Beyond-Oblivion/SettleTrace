package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestGeminiClient builds a GeminiLLMClient wired to an httptest.Server instead of the
// real Gemini endpoint, bypassing NewGeminiLLMClient's real-network URL so Complete's
// request/response handling can be exercised without a real API key or a live network call --
// see the baseURL field's doc comment in llm_client.go.
func newTestGeminiClient(t *testing.T, handler http.HandlerFunc) (*GeminiLLMClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &GeminiLLMClient{
		apiKey:     "test-key",
		model:      "gemini-test-model",
		httpClient: server.Client(),
		baseURL:    server.URL,
	}, server
}

// geminiSuccessBody builds a minimal, valid Gemini generateContent success response with the
// given text parts concatenated into a single candidate.
func geminiSuccessBody(parts ...string) string {
	texts := make([]map[string]string, 0, len(parts))
	for _, part := range parts {
		texts = append(texts, map[string]string{"text": part})
	}
	body, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{
			{"content": map[string]any{"parts": texts}},
		},
	})
	return string(body)
}

func TestNewGeminiLLMClient_ReturnsNilWithoutAPIKeyOrModel(t *testing.T) {
	if client := NewGeminiLLMClient("", "gemini-1.5-flash"); client != nil {
		t.Fatalf("expected NewGeminiLLMClient with an empty api key to return nil")
	}
	if client := NewGeminiLLMClient("key", ""); client != nil {
		t.Fatalf("expected NewGeminiLLMClient with an empty model to return nil")
	}
	if client := NewGeminiLLMClient("", ""); client != nil {
		t.Fatalf("expected NewGeminiLLMClient with neither set to return nil")
	}
}

func TestNewGeminiLLMClient_ReturnsConfiguredClientWhenBothSet(t *testing.T) {
	client := NewGeminiLLMClient("key", "gemini-1.5-flash")
	if client == nil {
		t.Fatalf("expected NewGeminiLLMClient to return a configured client when both are set")
	}
}

func TestGeminiLLMClient_Complete_NilReceiverReturnsErrorNotPanic(t *testing.T) {
	// The exact regression this session's earlier debug pass fixed (state.md section 2.1
	// item 2): a nil *GeminiLLMClient stored inside a non-nil LLMClient interface value must
	// degrade to an error, never panic, so "not configured" -> "explanation_skipped" holds.
	var client *GeminiLLMClient
	_, err := client.Complete(context.Background(), CompletionRequest{})
	if err == nil {
		t.Fatalf("expected a nil receiver to return an error instead of panicking")
	}
}

func TestGeminiLLMClient_Complete_SuccessReturnsText(t *testing.T) {
	client, _ := newTestGeminiClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.RawQuery, "key=test-key") {
			t.Errorf("expected the api key in the query string, got %q", r.URL.RawQuery)
		}
		if !strings.Contains(r.URL.Path, "gemini-test-model") {
			t.Errorf("expected the model name in the request path, got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, geminiSuccessBody("no settlement line has arrived within the SLA window."))
	})

	resp, err := client.Complete(context.Background(), CompletionRequest{
		SystemPrompt: "explain only from the evidence provided",
		UserPrompt:   `{"reason_code":"NO_CANDIDATE_IN_WINDOW"}`,
		MaxTokens:    300,
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if resp.Text != "no settlement line has arrived within the SLA window." {
		t.Fatalf("unexpected response text: %q", resp.Text)
	}
}

func TestGeminiLLMClient_Complete_ConcatenatesMultipleParts(t *testing.T) {
	// Gemini can return more than one text part in a single candidate; Complete must not
	// silently truncate to the first one -- the same "don't drop output" reasoning the
	// Anthropic client this replaced applied to multiple content blocks (state.md section
	// 7.1), applied here to parts.
	client, _ := newTestGeminiClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, geminiSuccessBody("first part. ", "second part."))
	})

	resp, err := client.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if resp.Text != "first part. second part." {
		t.Fatalf("expected concatenated parts, got %q", resp.Text)
	}
}

func TestGeminiLLMClient_Complete_NonOKStatusReturnsErrorWithBody(t *testing.T) {
	client, _ := newTestGeminiClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":400,"message":"API key not valid","status":"INVALID_ARGUMENT"}}`)
	})

	_, err := client.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
	if err == nil {
		t.Fatalf("expected a non-200 response to return an error")
	}
	if !strings.Contains(err.Error(), "API key not valid") {
		t.Fatalf("expected the error to surface Gemini's error body, got %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected the error to include the status code, got %v", err)
	}
}

func TestGeminiLLMClient_Complete_EmptyCandidatesReturnsError(t *testing.T) {
	client, _ := newTestGeminiClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"candidates":[]}`)
	})

	_, err := client.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
	if err == nil {
		t.Fatalf("expected an empty candidates array to return an error")
	}
}

func TestGeminiLLMClient_Complete_EmptyContentPartsReturnsError(t *testing.T) {
	client, _ := newTestGeminiClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[]}}]}`)
	})

	_, err := client.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
	if err == nil {
		t.Fatalf("expected empty content parts to return an error")
	}
}

func TestGeminiLLMClient_Complete_MalformedJSONReturnsError(t *testing.T) {
	client, _ := newTestGeminiClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{not valid json`)
	})

	_, err := client.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
	if err == nil {
		t.Fatalf("expected malformed JSON to return a decode error")
	}
}

func TestGeminiLLMClient_Complete_SendsSystemPromptAndUserPrompt(t *testing.T) {
	// Guards against a regression where the system/user prompt split silently collapses --
	// architecture.md section 9's evidence-only boundary depends on the system prompt
	// ("explain only from the evidence provided") actually reaching the model as a distinct
	// field, not folded into the user turn.
	var captured map[string]any
	client, _ := newTestGeminiClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, geminiSuccessBody("ok"))
	})

	_, err := client.Complete(context.Background(), CompletionRequest{
		SystemPrompt: "explain only from the evidence provided",
		UserPrompt:   "the user prompt",
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	systemInstruction, _ := captured["systemInstruction"].(map[string]any)
	if systemInstruction == nil {
		t.Fatalf("expected a systemInstruction field in the request body, got %+v", captured)
	}
	contents, _ := captured["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("expected exactly one content entry, got %+v", captured["contents"])
	}
}
