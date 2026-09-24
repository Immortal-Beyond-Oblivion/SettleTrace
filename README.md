# SettleTrace

A payment-reconciliation system modeled on Razorpay's stack (Go, PHP, Python, AWS, Redis, MySQL). It ingests payment webhooks, settlement reports, bank statements, and merchant ledger exports; deterministically matches payments across multiple confidence tiers; logs anything unmatched as a structured, reason-coded exception; and layers a strictly read-only, evidence-bound AI explainer on top.

- **`architecture.md`** — the design doc: what and why.
- **`implementation.md`** — the runbook: how, in what order, and the 15-day build plan this project follows.
- **`state.md`** — session-by-session working notes for whoever (human or AI) picks this repo up next. The most detailed, most current source of truth on exactly what's done and what's broken.

This file is the short version of `state.md`, kept in sync manually at the end of each session.

## What's working right now

- **Ingestion** (`internal/ingestion`) — schema validation, PCI/PAN rejection, HMAC webhook verification, Redis-fast-path + MySQL-unique-constraint idempotency, transactional writes, local file-watcher and SQS/S3 polling. Migrations auto-apply on startup (`store.ApplyMigrations`, tracked in `settletrace_schema_migrations`) for `cmd/api`, `cmd/matching-engine`, and the ingestion worker.
- **Deterministic matching** (`internal/recon`, `internal/matcher`) — Tier 1 (exact), Tier 2 (bounded fuzzy), Tier 3 (advisory-only ranking via the Python fuzzy-ranker service), and Tier L (ledger matching, ±3-day window). All pure, all unit-tested.
- **Append-only audit hash chain** (`internal/audit`) — `Seal`/`Verify`; `reconctl verify-chain` works against a real DB.
- **The AI explainer, end to end** — `internal/ai`'s guardrail core (budget cap, circuit breaker) wraps a real `GeminiLLMClient` (Google Gemini REST API), wired into `cmd/api` behind `POST /v1/exceptions/{id}/explain`. **Manually verified working against a live Gemini call** (see "Last verified" below) — this was the main open question for several sessions and is now resolved.
- **`GET /v1/exceptions`** — DB-backed, keyset-paginated, worst amount-at-risk first (falls back to an empty in-memory list when `DB_DSN` is unset). *Not yet compiler-verified.*
- **The Settlement Q&A agent (`internal/ai/qa`, `POST /v1/qa`)** — rule-based intent classifier → one of three fixed, read-only store queries (`store.QAStore`) → optional LLM phrasing constrained to the returned rows, with a deterministic fallback and the raw evidence rows always returned. *Written this session; not yet compiler-verified — run `go build ./... && go test ./...`.*
- **Infra scaffolding** — `docker-compose.yml`, Terraform, CI (lint/test/terraform-fmt/gitleaks).

## What's not working / not started

- **`POST /v1/exceptions/{id}/resolve` is still `501`.** No `/v1/batches/{id}`, no `/v1/ingest/verify-chain` HTTP route.
- **The PHP legacy-adapter service is a no-op** — it wraps the raw payload instead of transforming it.
- **No batch-queue integration** (`batch_queue` table / `ClaimNextBatch` unused) — the matching engine works on time windows instead.
- **No property-based or chaos tests.** No benchmark results (`benchmarks/` is empty). No `scripts/localstack_setup.sh`.
- **The Q&A agent has no `audit_log` keyword-search fallback** for unrecognized questions (`implementation.md` §2.4) — unrecognized questions get a fixed "here's what I can answer" reply instead.

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

1. Compiler-verify the last few sessions' changes (`go build ./... && go test ./...`, plus the `integration`-tagged store tests).
2. Add the missing HTTP routes (`/v1/batches/{id}`, `/v1/ingest/verify-chain`) and finish `/resolve`.
3. Finish the legacy-adapter, add `localstack_setup.sh`, property/chaos tests, a real benchmark run.
4. Reconcile `architecture.md`/`implementation.md`'s example env vars (they still describe an Anthropic-backed LLM client; the shipped code uses Gemini).

## Running it locally

See `implementation.md` Part III (§9-12) for full setup. Quick version:

```bash
docker compose up -d mysql redis
set -a && source .env && set +a
go run ./cmd/api   # applies ./migrations automatically on startup; no separate migrate CLI needed
```

`.env.example` documents every environment variable; copy it to `.env` and fill in real values (never commit `.env` — it's git-ignored and gitleaks-checked in CI).
