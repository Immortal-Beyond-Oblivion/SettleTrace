//go:build integration

package ingestion

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/schema"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

// TestMySQLUniqueConstraintRejectsDuplicateRawEvent verifies uniqueness without the Redis fast-path.
func TestMySQLUniqueConstraintRejectsDuplicateRawEvent(t *testing.T) {
	db := openIntegrationDB(t)
	mysqlStore := store.OpenMySQLStore(db)
	pipeline := Pipeline{Store: mysqlStore, Cache: NoopCache{}, Clock: recon.UTCClock{}, Secret: "local-secret"}
	body := sampleWebhook("pay_mysql_dup")
	envelope := Envelope{Source: schema.SourceWebhook, Body: body, SignatureHex: signBody(body, "local-secret")}
	if _, err := pipeline.Process(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Process(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	counts, err := mysqlStore.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts.RawEvents != 1 || counts.Payments != 1 {
		t.Fatalf("expected one raw event and payment, got %+v", counts)
	}
}

// TestRedisFlushStillDedupesViaMySQL verifies a flushed cache cannot create duplicate payments.
func TestRedisFlushStillDedupesViaMySQL(t *testing.T) {
	db := openIntegrationDB(t)
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewRedisCache(client, 0)
	mysqlStore := store.OpenMySQLStore(db)
	pipeline := Pipeline{Store: mysqlStore, Cache: cache, Clock: recon.UTCClock{}, Secret: "local-secret"}
	body := sampleWebhook("pay_redis_flush")
	envelope := Envelope{Source: schema.SourceWebhook, Body: body, SignatureHex: signBody(body, "local-secret")}
	if _, err := pipeline.Process(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if err := client.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Process(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	counts, err := mysqlStore.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts.RawEvents != 1 || counts.Payments != 1 {
		t.Fatalf("expected mysql uniqueness after redis flush, got %+v", counts)
	}
}

// openIntegrationDB connects to local MySQL and applies versioned migrations.
func openIntegrationDB(t *testing.T) *sql.DB {
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
	if err := store.ApplyMigrations(context.Background(), db, findMigrations(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SET FOREIGN_KEY_CHECKS=0`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"ledger_lines", "bank_lines", "settlement_lines", "payments", "raw_events"} {
		if _, err := db.Exec(`TRUNCATE TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`SET FOREIGN_KEY_CHECKS=1`); err != nil {
		t.Fatal(err)
	}
	return db
}

// findMigrations walks parents until the versioned SQL directory is found.
func findMigrations(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("migrations directory not found")
		}
		dir = parent
	}
}
