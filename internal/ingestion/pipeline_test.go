package ingestion

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/schema"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
)

type fixedClock struct{ now time.Time }

// Now returns a deterministic UTC timestamp for ingest tests.
func (clock fixedClock) Now() time.Time { return clock.now }

// signBody returns a hex HMAC for a webhook payload.
func signBody(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// sampleWebhook returns a representative captured-payment payload.
func sampleWebhook(paymentID string) []byte {
	return []byte(`{"payment_id":"` + paymentID + `","order_id":"order_1","amount_paise":150000,"currency":"INR","status":"captured","method":"card","captured_at":"2026-08-01T00:01:00Z"}`)
}

// newPipeline builds a test pipeline with a memory store and the requested cache.
func newPipeline(cache DuplicateCache) (Pipeline, *store.MemoryStore) {
	memory := store.NewMemoryStore()
	return Pipeline{
		Store:  memory,
		Cache:  cache,
		Clock:  fixedClock{now: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)},
		Secret: "local-secret",
	}, memory
}

// TestProcessDuplicateWebhookCreatesOnePayment verifies redelivery does not create a second payment.
func TestProcessDuplicateWebhookCreatesOnePayment(t *testing.T) {
	pipeline, memory := newPipeline(NewMemoryCache())
	body := sampleWebhook("pay_dup")
	envelope := Envelope{Source: schema.SourceWebhook, Body: body, SignatureHex: signBody(body, "local-secret")}
	if _, err := pipeline.Process(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Process(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	counts, _ := memory.Count(context.Background())
	if counts.RawEvents != 1 || counts.Payments != 1 {
		t.Fatalf("expected one raw event and one payment, got %+v", counts)
	}
}

// TestProcessDuplicateWithoutRedisStillUsesStoreUniqueness verifies Redis is not the correctness guarantee.
func TestProcessDuplicateWithoutRedisStillUsesStoreUniqueness(t *testing.T) {
	pipeline, memory := newPipeline(NoopCache{})
	body := sampleWebhook("pay_bypass")
	envelope := Envelope{Source: schema.SourceWebhook, Body: body, SignatureHex: signBody(body, "local-secret")}
	if _, err := pipeline.Process(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Process(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	counts, _ := memory.Count(context.Background())
	if counts.RawEvents != 1 || counts.Payments != 1 {
		t.Fatalf("expected uniqueness without redis, got %+v", counts)
	}
}

// TestProcessRejectsInvalidWebhookSignatureBeforePersistence verifies HMAC failure never writes rows.
func TestProcessRejectsInvalidWebhookSignatureBeforePersistence(t *testing.T) {
	pipeline, memory := newPipeline(NewMemoryCache())
	body := sampleWebhook("pay_sig")
	_, err := pipeline.Process(context.Background(), Envelope{Source: schema.SourceWebhook, Body: body, SignatureHex: "00"})
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected invalid signature, got %v", err)
	}
	counts, _ := memory.Count(context.Background())
	if counts.RawEvents != 0 || counts.Payments != 0 {
		t.Fatalf("expected no persistence, got %+v", counts)
	}
}

// TestProcessRejectsMalformedFileWholly verifies a broken CSV does not persist any row.
func TestProcessRejectsMalformedFileWholly(t *testing.T) {
	pipeline, memory := newPipeline(NoopCache{})
	body := []byte("reference_id,credit_paise,booked_at\nref_1,100,2026-08-01T00:00:00Z\nref_2,200")
	if _, err := pipeline.Process(context.Background(), Envelope{Source: schema.SourceBank, Body: body}); err == nil {
		t.Fatal("expected malformed rejection")
	}
	counts, _ := memory.Count(context.Background())
	if counts.RawEvents != 0 || counts.BankLines != 0 {
		t.Fatalf("expected no partial ingest, got %+v", counts)
	}
}

// TestProcessRollsBackWhenNormalizedInsertFails verifies raw and normalized writes share one transaction.
func TestProcessRollsBackWhenNormalizedInsertFails(t *testing.T) {
	memory := store.NewMemoryStore()
	memory.FailAfterNWrites(1)
	pipeline := Pipeline{Store: memory, Cache: NoopCache{}, Clock: recon.UTCClock{}, Secret: "local-secret"}
	body := sampleWebhook("pay_tx")
	_, err := pipeline.Process(context.Background(), Envelope{Source: schema.SourceWebhook, Body: body, SignatureHex: signBody(body, "local-secret")})
	if err == nil {
		t.Fatal("expected forced write failure")
	}
	counts, _ := memory.Count(context.Background())
	if counts.RawEvents != 0 || counts.Payments != 0 {
		t.Fatalf("expected rollback, got %+v", counts)
	}
}

// TestProcessQueueAcksOnlyAfterCommit verifies a failed ingest leaves the message on the queue.
func TestProcessQueueAcksOnlyAfterCommit(t *testing.T) {
	pipeline, _ := newPipeline(NoopCache{})
	queue := &memoryQueue{messages: []QueueMessage{{
		Receipt: "r1",
		Source:  schema.SourceBank,
		Body:    []byte("reference_id,credit_paise,booked_at\nbad"),
	}}}
	if err := pipeline.ProcessQueue(context.Background(), queue, nil); err != nil {
		t.Fatal(err)
	}
	if len(queue.deleted) != 0 {
		t.Fatalf("expected no ack, deleted %v", queue.deleted)
	}
	queue.messages = []QueueMessage{{
		Receipt: "r2",
		Source:  schema.SourceBank,
		Body:    []byte("reference_id,credit_paise,booked_at\nref_ok,500,2026-08-01T00:00:00Z"),
	}}
	if err := pipeline.ProcessQueue(context.Background(), queue, nil); err != nil {
		t.Fatal(err)
	}
	if len(queue.deleted) != 1 || queue.deleted[0] != "r2" {
		t.Fatalf("expected ack after commit, got %v", queue.deleted)
	}
}

// TestWebhookHandlerRejectsBadSignature verifies the HTTP path authenticates the raw body.
func TestWebhookHandlerRejectsBadSignature(t *testing.T) {
	pipeline, memory := newPipeline(NoopCache{})
	request := httptest.NewRequest(http.MethodPost, "/v1/webhooks", bytes.NewReader(sampleWebhook("pay_http")))
	request.Header.Set("X-Webhook-Signature", "deadbeef")
	response := httptest.NewRecorder()
	WebhookHandler(pipeline).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
	counts, _ := memory.Count(context.Background())
	if counts.Payments != 0 {
		t.Fatalf("expected no payment, got %+v", counts)
	}
}

// TestDecodeQueueBodyAcceptsS3Notification verifies LocalStack-style S3 events map onto source prefixes.
func TestDecodeQueueBodyAcceptsS3Notification(t *testing.T) {
	body := []byte(`{"Records":[{"s3":{"bucket":{"name":"landing"},"object":{"key":"settlements/setl_1.json"}}}]}`)
	message, err := DecodeQueueBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if message.Source != schema.SourceSettlement || message.Bucket != "landing" {
		t.Fatalf("unexpected message %#v", message)
	}
}

// TestProcessLandingDirMovesAcknowledgedFiles verifies local file ingest acknowledges only after commit.
func TestProcessLandingDirMovesAcknowledgedFiles(t *testing.T) {
	root := t.TempDir()
	bankDir := filepath.Join(root, "banks")
	if err := os.MkdirAll(bankDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bankDir, "line.csv")
	if err := os.WriteFile(path, []byte("reference_id,credit_paise,booked_at\nref_file,900,2026-08-01T00:00:00Z"), 0o644); err != nil {
		t.Fatal(err)
	}
	pipeline, memory := newPipeline(NoopCache{})
	if _, err := pipeline.ProcessLandingDir(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected landing file to be moved after ack")
	}
	counts, _ := memory.Count(context.Background())
	if counts.BankLines != 1 {
		t.Fatalf("expected one bank line, got %+v", counts)
	}
}

type memoryQueue struct {
	messages []QueueMessage
	deleted  []string
}

// Receive returns the current in-memory queue batch.
func (queue *memoryQueue) Receive(context.Context) ([]QueueMessage, error) {
	batch := queue.messages
	queue.messages = nil
	return batch, nil
}

// Delete records an acknowledgement after a committed ingest.
func (queue *memoryQueue) Delete(_ context.Context, receipt string) error {
	queue.deleted = append(queue.deleted, receipt)
	return nil
}
