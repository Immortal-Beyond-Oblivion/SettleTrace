package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/audit"
)

// fakeAuditReader is a hand-rolled store.AuditReader, following this repo's no-mocking-library
// convention. It returns whatever entries/err the test hands it and counts calls.
type fakeAuditReader struct {
	entries []audit.Entry
	err     error
	calls   int
}

func (fake *fakeAuditReader) LoadAuditEntries(_ context.Context) ([]audit.Entry, error) {
	fake.calls++
	return fake.entries, fake.err
}

// sealedChain builds a genuinely valid hash chain of n entries with the real audit.Seal, so the
// handler tests exercise the real audit.Verify rather than a stand-in for it.
func sealedChain(t *testing.T, n int) []audit.Entry {
	t.Helper()
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	entries := make([]audit.Entry, 0, n)
	previous := ""
	for i := 0; i < n; i++ {
		sealed, err := audit.Seal(audit.Entry{
			EventType:    "test_event",
			Payload:      map[string]any{"seq": i},
			PreviousHash: previous,
			CreatedAt:    base.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("seal entry %d: %v", i, err)
		}
		entries = append(entries, sealed)
		previous = sealed.RowHash
	}
	return entries
}

func postVerifyChain(server Server) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/ingest/verify-chain", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	return response
}

func decodeVerifyChainBody(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return body
}

func TestVerifyChain_NotConfiguredReturns503(t *testing.T) {
	response := postVerifyChain(Server{})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected %d, got %d", http.StatusServiceUnavailable, response.Code)
	}
}

func TestVerifyChain_EmptyLogIsVerifiedWithZeroRows(t *testing.T) {
	reader := &fakeAuditReader{}
	response := postVerifyChain(Server{Audit: reader})
	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, response.Code)
	}
	body := decodeVerifyChainBody(t, response)
	if body["verified"] != true {
		t.Errorf("verified = %v, want true", body["verified"])
	}
	if body["rows_checked"] != float64(0) {
		t.Errorf("rows_checked = %v, want 0", body["rows_checked"])
	}
}

func TestVerifyChain_ValidChainIsVerified(t *testing.T) {
	reader := &fakeAuditReader{entries: sealedChain(t, 3)}
	response := postVerifyChain(Server{Audit: reader})
	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, response.Code)
	}
	body := decodeVerifyChainBody(t, response)
	if body["verified"] != true {
		t.Errorf("verified = %v, want true", body["verified"])
	}
	if body["rows_checked"] != float64(3) {
		t.Errorf("rows_checked = %v, want 3", body["rows_checked"])
	}
	if reader.calls != 1 {
		t.Errorf("LoadAuditEntries called %d times; want 1", reader.calls)
	}
}

func TestVerifyChain_TamperedPayloadReportsRowHashMismatch(t *testing.T) {
	entries := sealedChain(t, 3)
	entries[1].Payload = map[string]any{"seq": 999}
	response := postVerifyChain(Server{Audit: &fakeAuditReader{entries: entries}})
	if response.Code != http.StatusOK {
		t.Fatalf("a broken chain is a successful verification; expected %d, got %d", http.StatusOK, response.Code)
	}
	body := decodeVerifyChainBody(t, response)
	if body["verified"] != false {
		t.Errorf("verified = %v, want false", body["verified"])
	}
	if body["first_break_at_row"] != float64(1) {
		t.Errorf("first_break_at_row = %v, want 1", body["first_break_at_row"])
	}
	if body["detail"] != "row hash mismatch" {
		t.Errorf("detail = %v, want %q", body["detail"], "row hash mismatch")
	}
}

func TestVerifyChain_ForgedLinkReportsPreviousHashMismatch(t *testing.T) {
	entries := sealedChain(t, 3)
	entries[2].PreviousHash = "not-the-real-previous-hash"
	response := postVerifyChain(Server{Audit: &fakeAuditReader{entries: entries}})
	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, response.Code)
	}
	body := decodeVerifyChainBody(t, response)
	if body["verified"] != false {
		t.Errorf("verified = %v, want false", body["verified"])
	}
	if body["first_break_at_row"] != float64(2) {
		t.Errorf("first_break_at_row = %v, want 2", body["first_break_at_row"])
	}
	if body["detail"] != "previous hash mismatch" {
		t.Errorf("detail = %v, want %q", body["detail"], "previous hash mismatch")
	}
}

func TestVerifyChain_StoreErrorReturns500WithoutLeakingDetail(t *testing.T) {
	reader := &fakeAuditReader{err: errors.New("dial tcp 10.0.0.5:3306: connection refused")}
	response := postVerifyChain(Server{Audit: reader})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected %d, got %d", http.StatusInternalServerError, response.Code)
	}
	if strings.Contains(response.Body.String(), "10.0.0.5") {
		t.Errorf("response leaked internal error detail: %s", response.Body.String())
	}
	body := decodeVerifyChainBody(t, response)
	if body["error"] != "failed to read audit log" {
		t.Errorf("error = %v, want %q", body["error"], "failed to read audit log")
	}
}
