package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
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
