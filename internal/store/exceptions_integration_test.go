//go:build integration

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// openExceptionIntegrationDB connects to local MySQL, applies migrations, and truncates
// exception_log so each test starts from a clean, known state -- same discipline as
// internal/ingestion/pipeline_integration_test.go's openIntegrationDB, kept separate here
// since that helper lives in a different package and truncates ingestion-only tables.
func openExceptionIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		t.Skip("DB_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplyMigrations(context.Background(), db, findMigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`TRUNCATE TABLE exception_log`); err != nil {
		t.Fatal(err)
	}
	return db
}

// findMigrationsDir walks parents from the working directory until the versioned SQL
// directory is found, mirroring internal/ingestion/pipeline_integration_test.go's
// findMigrations.
func findMigrationsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "migrations")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("migrations directory not found")
		}
		dir = parent
	}
}

func TestMySQLStore_GetExceptionByID_ReturnsInsertedRow(t *testing.T) {
	db := openExceptionIntegrationDB(t)
	evidence := json.RawMessage(`{"candidates_checked":2}`)
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	result, err := db.Exec(`
		INSERT INTO exception_log (record_type, record_id, reason_code, amount_at_risk_paise, evidence_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		"payment", "pay_TEST1", "NO_CANDIDATE_IN_WINDOW", int64(50000), []byte(evidence), createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	mysqlStore := OpenMySQLStore(db)
	record, err := mysqlStore.GetExceptionByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetExceptionByID returned error: %v", err)
	}
	if record.ID != id {
		t.Fatalf("expected ID %d, got %d", id, record.ID)
	}
	if record.RecordType != "payment" || record.RecordID != "pay_TEST1" {
		t.Fatalf("expected record_type=payment record_id=pay_TEST1, got %+v", record)
	}
	if record.ReasonCode != "NO_CANDIDATE_IN_WINDOW" || record.AmountAtRiskPaise != 50000 {
		t.Fatalf("unexpected reason_code/amount_at_risk_paise: %+v", record)
	}
	if string(record.EvidenceJSON) != string(evidence) {
		t.Fatalf("expected evidence_json to round-trip exactly, got %s", record.EvidenceJSON)
	}
	if record.ResolvedAt != nil {
		t.Fatalf("expected a fresh exception to have no resolved_at, got %v", record.ResolvedAt)
	}
}

func TestMySQLStore_GetExceptionByID_ResolvedAtRoundTrips(t *testing.T) {
	db := openExceptionIntegrationDB(t)
	resolvedAt := time.Now().UTC().Truncate(time.Microsecond)

	result, err := db.Exec(`
		INSERT INTO exception_log (record_type, record_id, reason_code, amount_at_risk_paise, evidence_json, resolved_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"ledger_line", "42", "LEDGER_ORPHAN", int64(1000), []byte(`{}`), resolvedAt, resolvedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	mysqlStore := OpenMySQLStore(db)
	record, err := mysqlStore.GetExceptionByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetExceptionByID returned error: %v", err)
	}
	if record.ResolvedAt == nil || !record.ResolvedAt.Equal(resolvedAt) {
		t.Fatalf("expected resolved_at to round-trip as %v, got %v", resolvedAt, record.ResolvedAt)
	}
}

func TestMySQLStore_GetExceptionByID_UnknownIDReturnsErrExceptionNotFound(t *testing.T) {
	db := openExceptionIntegrationDB(t)
	mysqlStore := OpenMySQLStore(db)
	_, err := mysqlStore.GetExceptionByID(context.Background(), 999999999)
	if !errors.Is(err, ErrExceptionNotFound) {
		t.Fatalf("expected ErrExceptionNotFound, got %v", err)
	}
}
