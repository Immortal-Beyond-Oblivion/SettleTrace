// Command ingestion-worker validates and persists local or queued source payloads.
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/ingestion"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

// main wires MySQL, optional Redis, local files, HTTP webhooks, and LocalStack SQS.
func main() {
	ctx := context.Background()
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		log.Fatal("DB_DSN is required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := store.ApplyMigrations(ctx, db, firstNonEmpty(os.Getenv("MIGRATIONS_DIR"), "migrations")); err != nil {
		log.Fatal(err)
	}
	pipeline := ingestion.Pipeline{
		Store:  store.OpenMySQLStore(db),
		Cache:  ingestion.NoopCache{},
		Clock:  recon.UTCClock{},
		Secret: os.Getenv("WEBHOOK_HMAC_SECRET"),
	}
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		pipeline.Cache = ingestion.NewRedisCache(redis.NewClient(&redis.Options{Addr: addr}), 24*time.Hour)
	}
	if landing := os.Getenv("LANDING_DIR"); landing != "" {
		result, err := pipeline.ProcessLandingDir(ctx, landing)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("landing ingest applied=%d", result.Applied)
	}
	if queueURL := os.Getenv("SQS_QUEUE_URL"); queueURL != "" {
		cfg, err := ingestion.NewLocalAWSClients(ctx)
		if err != nil {
			log.Fatal(err)
		}
		queue := ingestion.NewSQSQueue(cfg, queueURL)
		var objects ingestion.ObjectStore
		if os.Getenv("S3_BUCKET") != "" {
			objects = ingestion.NewS3Store(cfg)
		}
		go pollQueue(ctx, pipeline, queue, objects)
	}
	address := os.Getenv("INGEST_ADDR")
	if address == "" {
		address = ":8081"
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/webhooks", ingestion.WebhookHandler(pipeline))
	mux.HandleFunc("GET /health", health)
	log.Printf("starting ingestion-worker on %s", address)
	if err := http.ListenAndServe(address, mux); err != nil {
		log.Fatal(err)
	}
}

// health reports process liveness without exposing credentials.
func health(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(`{"status":"ok"}`))
}

// pollQueue repeatedly receives jobs and acknowledges them only after commit.
func pollQueue(ctx context.Context, pipeline ingestion.Pipeline, queue ingestion.Queue, objects ingestion.ObjectStore) {
	for {
		if err := pipeline.ProcessQueue(ctx, queue, objects); err != nil {
			log.Printf("queue ingest error: %v", err)
			time.Sleep(2 * time.Second)
		}
	}
}

// firstNonEmpty returns the first nonempty string.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
