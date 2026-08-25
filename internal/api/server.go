// Package api exposes the minimal read-only operator HTTP surface.
package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
)

// Server serves batch and exception responses from an injected read model.
type Server struct{ Exceptions []recon.Exception }

// Routes returns the configured HTTP handler tree.
func (server Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("GET /v1/exceptions", server.listExceptions)
	mux.HandleFunc("POST /v1/exceptions/", server.resolveException)
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

// writeJSON writes one JSON response with a deterministic content type.
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
