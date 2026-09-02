package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)



// defaultGeminiBaseURL is the real Gemini API base, used whenever baseURL is left empty.
const defaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta/models"

// GeminiLLMClient implements LLMClient for the Google Gemini API.
type GeminiLLMClient struct {
	apiKey     string
	model      string
	httpClient *http.Client

	// baseURL is the Gemini API base (defaultGeminiBaseURL when empty). Tests construct a
	// GeminiLLMClient literal with this set to an httptest.Server's URL so Complete's
	// request/response handling can be exercised without a real network call or a real API
	// key -- see llm_client_test.go. NewGeminiLLMClient leaves this empty for real callers,
	// which is what keeps cmd/api's production wiring unaffected by this field's existence.
	baseURL string
}

// NewGeminiLLMClient returns a configured client, or nil if unconfigured.
func NewGeminiLLMClient(apiKey, model string) *GeminiLLMClient {
	if apiKey == "" || model == "" {
		return nil
	}
	return &GeminiLLMClient{
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{},
	}
}

func (c *GeminiLLMClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	// nil-receiver guard: a nil *GeminiLLMClient can still reach here when it's stored inside
	// a non-nil LLMClient interface value (e.g. Explainer.LLM = ai.NewGeminiLLMClient("", "")).
	// Without this check, c.model/c.apiKey below would panic instead of degrading gracefully,
	// which breaks the "not configured -> explanation_skipped, never a crash" guarantee that
	// cmd/api/main.go and internal/api/server.go both rely on.
	if c == nil {
		return CompletionResponse{}, fmt.Errorf("gemini llm client not configured")
	}

	// Gemini uses the key in the query string
	base := c.baseURL
	if base == "" {
		base = defaultGeminiBaseURL
	}
	url := fmt.Sprintf("%s/%s:generateContent?key=%s", base, c.model, c.apiKey)

	// Format payload exactly as Gemini expects
	payload := map[string]interface{}{
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]interface{}{{"text": req.SystemPrompt}},
		},
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{{"text": req.UserPrompt}},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": req.MaxTokens,
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal payload: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return CompletionResponse{}, fmt.Errorf("gemini api error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse the Gemini response structure
	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return CompletionResponse{}, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return CompletionResponse{}, fmt.Errorf("empty response from gemini")
	}

	// Concatenate every text part of the first candidate -- Gemini can split its response
	// across multiple parts, and reading only Parts[0] silently truncates output whenever
	// that happens (this was the exact bug TestGeminiLLMClient_Complete_ConcatenatesMultipleParts
	// caught: two parts in, only the first part's text came back out). Only candidates[0] is
	// read -- Explain never requests more than one candidate, mirroring the previous Anthropic
	// client's "don't silently truncate multi-part output" handling of content blocks.
	var text strings.Builder
	for _, part := range result.Candidates[0].Content.Parts {
		text.WriteString(part.Text)
	}

	return CompletionResponse{Text: text.String()}, nil
}