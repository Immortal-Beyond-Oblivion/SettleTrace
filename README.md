# SettleTrace

A payment-reconciliation system modeled on Razorpay's stack (Go, PHP, Python, AWS, Redis, MySQL). It ingests payment webhooks, settlement reports, bank statements, and merchant ledger exports; deterministically matches payments across multiple confidence tiers; logs anything unmatched as a structured, reason-coded exception; and layers a strictly read-only, evidence-bound AI explainer on top.

- **`architecture.md`** — the design doc: what and why.
- **`implementation.md`** — the runbook: how, in what order, and the 15-day build plan this project follows.
- **`state.md`** — session-by-session working notes for whoever (human or AI) picks this repo up next. The most detailed, most current source of truth on exactly what's done and what's broken.

This file is the short version of `state.md`, kept in sync manually at the end of each session.

## What's working right now

- **Ingestion** (`internal/ingestion`) — schema validation, PCI/PAN rejection, HMAC webhook verification, Redis-fast-path + MySQL-unique-constraint idempotency, transactional writes, local file-watcher and SQS/S3 polling. Migrations auto-apply on startup for every `cmd/*` binary except `cmd/api` (see below).
- **Deterministic matching** (`internal/recon`, `internal/matcher`) — Tier 1 (exact), Tier 2 (bounded fuzzy), Tier 3 (advisory-only ranking via the Python fuzzy-ranker service), and Tier L (ledger matching, ±3-day window). All pure, all unit-tested.
- **Append-only audit hash chain** (`internal/audit`) — `Seal`/`Verify`; `reconctl verify-chain` works against a real DB.
- **The AI explainer, end to end** — `internal/ai`'s guardrail core (budget cap, circuit breaker) wraps a real `GeminiLLMClient` (Google Gemini REST API), wired into `cmd/api` behind `POST /v1/exceptions/{id}/explain`. **Manually verified working against a live Gemini call** (see "Last verified" below) — this was the main open question for several sessions and is now resolved.
- **Infra scaffolding** — `docker-compose.yml`, Terraform, CI (lint/test/terraform-fmt/gitleaks).

## What's not working / not started

- **The Settlement Q&A agent (`internal/ai/qa`) doesn't exist at all** — no intent classifier, no SQL templates, no `/v1/qa` route. This is the single biggest missing piece relative to `implementation.md`.
- **`GET /v1/exceptions` is a hardcoded in-memory slice, not a DB read.** `POST /v1/exceptions/{id}/resolve` is still `501`. No `/v1/batches/{id}`, no `/v1/ingest/verify-chain` HTTP route.
- **The PHP legacy-adapter service is a no-op** — it wraps the raw payload instead of transforming it.
- **No batch-queue integration** (`batch_queue` table / `ClaimNextBatch` unused) — the matching engine works on time windows instead.
- **No property-based or chaos tests.** No benchmark results (`benchmarks/` is empty). No `scripts/localstack_setup.sh`.
- **`cmd/api/main.go`'s migration-apply call is commented out** — intentional or a regression, not yet confirmed by the owner. See `state.md` §8.
- **The ledger-side query (`GetUnmatchedLedgerLines`) is unbounded** — scans every unmatched row, a known shortcut.

## Last verified (owner, manual, outside a session)

```
go build ./... && go test ./...           # green
go run ./cmd/api                           # starts cleanly on :8080
curl -X POST http://localhost:8080/v1/exceptions/1/explain
# → {"evidence":{"candidates_checked":0},"prompt_version":"v1","reason_code":"NO_CANDIDATE_IN_WINDOW","text":"..."}
```

A real explanation came back (not `explanation_skipped`), confirming the Gemini wiring works end to end against a live API call, using `LLM_MODEL=gemini-3.6-flash` and the key in `.env`. See `state.md` §0 (top) and §2.1 for the debugging history that got here.

## What's left, roughly in priority order

Full detail and reasoning in `state.md` §4 — short version:

1. Rebuild/expand test coverage for the AI explain path — **in progress this session**, see `state.md`.
2. Build the Q&A agent (`internal/ai/qa`) — the largest remaining unit of work.
3. Make `GET /v1/exceptions` DB-backed; add the missing HTTP routes; finish `/resolve`.
4. Finish the legacy-adapter, add `localstack_setup.sh`, property/chaos tests, a real benchmark run.
5. Window-bound `GetUnmatchedLedgerLines`.
6. Reconcile `architecture.md`/`implementation.md`'s example env vars (they still describe an Anthropic-backed LLM client; the shipped code uses Gemini).

## Running it locally

See `implementation.md` Part III (§9-12) for full setup. Quick version:

```bash
docker compose up -d mysql redis
migrate -path ./migrations -database "$DB_DSN" up   # or let cmd/matching-engine/cmd/ingestion-worker auto-apply
go run ./cmd/api
```

`.env.example` documents every environment variable; copy it to `.env` and fill in real values (never commit `.env` — it's git-ignored and gitleaks-checked in CI).
