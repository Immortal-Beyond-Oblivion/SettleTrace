// Command api starts the local SettleTrace operator API.
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/ai"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/api"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

// main starts the API on the configured local address. When DB_DSN is set, it also wires the
// AI explainer (GEMINI_API_KEY/LLM_MODEL, AI_BUDGET_PER_BATCH_USD, REDIS_ADDR) behind
// POST /v1/exceptions/{id}/explain. When DB_DSN is unset, the API still starts -- with
// /v1/exceptions and the explain route degrading to their "not configured" responses --
// because the ingestion/matching-only smoke test path (implementation.md section 12) must
// keep working even before a database or the AI layer is configured.
func main() {
	address := os.Getenv("API_ADDR")
	if address == "" {
		address = ":8080"
	}

	server := api.Server{}

	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		log.Println("DB_DSN not set: /v1/exceptions/{id}/explain will return 503")
	} else {
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			log.Fatalf("open database: %v", err)
		}
		defer db.Close()
		// Idempotent: ApplyMigrations tracks applied files in schema_migrations and skips
		// ones already present, so this is safe to run against an already-migrated DB
		// (including one seeded by hand against an existing schema) -- it will not
		// re-apply anything. Unlike cmd/matching-engine, which log.Fatal's on a migration
		// error, cmd/api's stated contract above is that no dependency may fail startup,
		// so a migration error here is logged and the server continues in a degraded
		// state rather than exiting -- the same convention buildExplainer follows for
		// its own file-read error below.
		if err := store.ApplyMigrations(context.Background(), db, firstNonEmpty(os.Getenv("MIGRATIONS_DIR"), "migrations")); err != nil {
			log.Printf("apply migrations: %v (continuing with existing schema)", err)
		}
		mysqlStore := store.OpenMySQLStore(db)
		server.Store = mysqlStore
		server.Explainer = buildExplainer(mysqlStore)
	}

	log.Printf("starting api on %s", address)
	if err := http.ListenAndServe(address, server.Routes()); err != nil {
		log.Fatal(err)
	}
}

// buildExplainer wires internal/ai's guardrail core (BudgetTracker, CircuitBreaker) and the
// Gemini LLM client from environment configuration. It never fails startup: each dependency
// degrades to nil -- and therefore "no cap"/"no breaker"/"Complete returns its own
// not-configured error" -- exactly per this package's established "not configured degrades
// gracefully" convention (NewBudgetTracker, NewCircuitBreaker's zero-value defaults,
// NewGeminiLLMClient), so a reviewer can bring the API up with zero AI configuration and still
// exercise every other route, including a call to /explain that cleanly reports
// "explanation_skipped" instead of erroring.
func buildExplainer(logWriter store.AIExplanationLogWriter) *ai.Explainer {
	var redisClient *redis.Client
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		redisClient = redis.NewClient(&redis.Options{Addr: addr})
	}

	// A malformed or empty AI_BUDGET_PER_BATCH_USD parses to 0, which NewBudgetTracker
	// already treats as "not configured" (budget.go) -- no separate validation needed here.
	capUSD, _ := strconv.ParseFloat(os.Getenv("AI_BUDGET_PER_BATCH_USD"), 64)

	systemPrompt := ""
	if raw, err := os.ReadFile("internal/ai/prompts/v1/explainer_system.txt"); err == nil {
		systemPrompt = string(raw)
	} else {
		log.Printf("could not read explainer system prompt, continuing without one: %v", err)
	}

	model := os.Getenv("LLM_MODEL")
	return &ai.Explainer{
		LLM:           ai.NewGeminiLLMClient(os.Getenv("GEMINI_API_KEY"), model),
		Store:         logWriter,
		Budget:        ai.NewBudgetTracker(redisClient, capUSD),
		Breaker:       ai.NewCircuitBreaker(3, 30*time.Second),
		SystemPrompt:  systemPrompt,
		PromptVersion: "v1",
		ModelName:     firstNonEmpty(model, "unknown"),
		Timeout:       5 * time.Second,
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
