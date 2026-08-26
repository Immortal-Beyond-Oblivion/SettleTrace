// Command matching-engine runs one deterministic reconciliation pass over a time window.
//
// It is designed to be invoked on a schedule (cron locally, a scheduled ECS task in prod)
// rather than run as a long-lived loop: each invocation acquires a Redis lock for the
// window it is about to process, matches every unmatched payment and ledger line inside
// that window against Tier 1, Tier 2, and Tier L, writes match_results/exception_log rows
// plus their audit_log entries, and exits. When FUZZY_RANKER_URL is set, a payment that
// survives Tier 1 and Tier 2 is also sent to the Tier 3 fuzzy-ranker service for advisory
// ranking before falling back to a plain exception. See state.md section 4 for what this
// still does not do (batch_queue integration, the AI layer, the API).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/matcher"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/store"
	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

// main wires MySQL, an optional Redis lock, and a bounded time window, then runs one pass.
func main() {
	lookback := flag.Duration("lookback", time.Hour, "how far back from now to start the matching window")
	lockTTL := flag.Duration("lock-ttl", 5*time.Minute, "how long the Redis window lock is held")
	flag.Parse()

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

	clock := recon.UTCClock{}
	end := clock.Now()
	start := end.Add(-*lookback)

	var locker matcher.Locker = matcher.NoopLocker{}
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		locker = matcher.RedisLocker{Client: redis.NewClient(&redis.Options{Addr: addr})}
	}
	release, err := locker.Acquire(ctx, "matching-engine:window-lock", *lockTTL)
	if err != nil {
		log.Fatalf("could not acquire window lock: %v", err)
	}
	defer func() {
		if releaseErr := release(ctx); releaseErr != nil {
			log.Printf("release window lock: %v", releaseErr)
		}
	}()

	engine := matcher.NewEngine(store.OpenMySQLStore(db), clock)
	engine.Tier3 = matcher.NewTier3Client(os.Getenv("FUZZY_RANKER_URL"))
	report, err := engine.RunWindow(ctx, start, end)
	if err != nil {
		log.Fatalf("matching run failed: %v", err)
	}
	printReport(report)
}

// printReport writes a human-readable summary of one matching-engine run to stdout.
func printReport(report *matcher.Report) {
	fmt.Printf("window: %s to %s\n", report.WindowStart.Format(time.RFC3339), report.WindowEnd.Format(time.RFC3339))
	fmt.Printf("matched: %d\n", report.TotalMatched())
	for confidence, count := range report.MatchedByConfidence {
		fmt.Printf("  %s: %d\n", confidence, count)
	}
	fmt.Printf("exceptions: %d\n", report.TotalExceptions())
	for reason, count := range report.ExceptionsByReason {
		fmt.Printf("  %s: %d\n", reason, count)
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
