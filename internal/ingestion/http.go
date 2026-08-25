package ingestion

import (
	"io"
	"net/http"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/schema"
)

// WebhookHandler verifies the raw body HMAC before any persistence attempt.
func WebhookHandler(pipeline Pipeline) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, "unable to read body", http.StatusBadRequest)
			return
		}
		result, processErr := pipeline.Process(request.Context(), Envelope{
			Source:       schema.SourceWebhook,
			Body:         body,
			SignatureHex: request.Header.Get("X-Webhook-Signature"),
			HMACSecret:   pipeline.Secret,
		})
		if processErr != nil {
			status := http.StatusBadRequest
			if result.Status == StatusRejected && processErr == ErrInvalidSignature {
				status = http.StatusUnauthorized
			}
			http.Error(writer, processErr.Error(), status)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"status":"` + string(result.Status) + `"}`))
	})
}
