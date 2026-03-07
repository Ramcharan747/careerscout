# DIRECTIONS — Next Steps Guide

> Always read MEMORY.md first, then this file, before starting any session.
> This file tells the agent exactly what to build next and in what order.

---

## Current Priority: Team 1 — URL Ingestion & Tier Routing

### Why First
Team 1 is the entry point for the entire pipeline. Nothing else runs without URLs flowing in.

### What to Build
1. `internal/db/postgres.go` — Postgres client
   - Connect via `DATABASE_URL` env var
   - Expose: `GetDiscoveryRecord(domain string) (*DiscoveryRecord, error)`
   - Expose: `SaveDiscoveryRecord(record *DiscoveryRecord) error`

2. `internal/queue/redpanda.go` — Redpanda producer
   - Connect via `REDPANDA_BROKERS` env var (comma-separated)
   - Expose: `Produce(topic string, key string, value []byte) error`
   - Use `franz-go` library (NOT sarama — franz-go is pure Go, no CGO)

3. `internal/ingestion/ratelimiter.go` — Per-domain rate limiter
   - In-memory map: `domain → last_requested_at`
   - Block if last request was < 4 hours ago
   - Thread-safe with sync.RWMutex

4. `internal/ingestion/router.go` — Tier routing logic
   - Check Postgres for existing record: if found → emit to `apis.discovered` replay path
   - If new: emit to `urls.tier1_queue`
   - Apply rate limiter before any emit

5. `cmd/ingestion/main.go` — Entry point
   - Read URLs from `urls.to_process` Redpanda topic
   - Spin up N goroutines (configurable via `INGESTION_WORKERS` env)
   - Each goroutine calls router.Route(url)
   - Handle SIGTERM gracefully (drain current batch, then exit)

### After Team 1 is Done
→ Move to **Team 2: Tier 1 Static Discovery**

---

## Build Order (Full Sequence)

```
Team 1  →  Team 2  →  Team 3  →  Team 5 (ML)  →  Team 4 (eBPF)
                                     ↓
                              Team 6 (Replay)
                                     ↓
                              Team 7 (Normalise)
                                     ↓
                              Team 8 (Infra)
```

Team 5 (ML) must be done before Team 3 finishes because T2 workers call the ML gRPC service.
Team 4 (eBPF) can be built in parallel with Teams 5–7 since it's isolated.
Team 8 (Infra) is last — it wraps everything in Terraform.

---

## Resources Still Needed from User

Before the Replay Engine (Team 6) is built, ask the user:
1. **URL source** — where does the master list of 1M company domains come from?
2. **AWS setup** — do they have an AWS account ready? Which region?
3. **Redpanda** — self-hosted on ECS or Redpanda Cloud (managed)?
4. **ML training data** — 50K labelled API requests — existing dataset or collect as we go?
5. **Residential proxy provider** — for Tier 3 (Brightdata? Oxylabs?)

---

## Code Standards to Follow

- All configs via environment variables (no hardcoded values)
- All services log in JSON format (use `zap` for Go, `tracing` for Rust)
- All errors wrapped with context (`fmt.Errorf("operation: %w", err)`)
- Every Redpanda consumer must commit offsets only after successful processing
- All Chromium instances: hard kill after 800ms regardless of state
- Test coverage target: 70%+ on business logic, 0% required on main.go entry points
