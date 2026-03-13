# MEMORY — CareerScout Agent Context

> This file is the persistent memory for the AI agent building CareerScout.
> Updated every session. Read this first before touching any code.

---

## Project Identity
- **Name:** CareerScout
- **Goal:** Intercept hidden backend APIs of company career portals at scale (1M+ domains/day)
- **Version:** 1.0 (March 2026)
- **Workspace:** `/Users/ramcharan/.gemini/antigravity/playground/tensor-photon/careerscout`
- **Foundation Doc:** `/Users/ramcharan/Downloads/careerscout_foundation.md`

---

## Core Architecture Decisions (Locked)

1. **Go** for all orchestration and discovery workers (goroutines, chromedp)
2. **Rust + Tokio + Reqwest** for the Replay Engine (sub-ms latency, no GC)
3. **Python + ONNX** for ML only — never in hot paths
4. **Redpanda** (not Kafka) — avoids JVM overhead
5. **PostgreSQL + Redis + S3** — structured, ephemeral state, immutable archive
6. **AWS ECS Graviton4 ARM Spot** — 70% cost savings vs x86
7. **No Playwright, Puppeteer, or Selenium** — use chromedp directly

---

## Three-Tier Processing

| Tier | Method | % of URLs | Target Latency |
|------|--------|-----------|----------------|
| 1    | Static HTTP + regex (Go + uTLS) | ~40% | <50ms |
| 2    | CDP Chromium interception (Go + chromedp) | ~50% | <800ms |
| 3    | eBPF kernel-level TLS hook (C + Go sidecar) | ~10% | best-effort |

---

## Redpanda Topics

| Topic | Purpose |
|---|---|
| `urls.to_process` | Master URL list (nightly) |
| `urls.tier1_queue` | Static URLs |
| `urls.tier2_queue` | CDP URLs |
| `urls.tier3_queue` | Bot-protected URLs |
| `apis.discovered` | Captured API payloads |
| `apis.failed` | Failed all tiers — human review |
| `jobs.raw` | Raw job JSON from replays |

---

## Build Status

| Team | Module | Status | Files |
|------|--------|--------|-------|
| 1 | URL Ingestion & Routing | ✅ Done | router.go, ratelimiter.go, postgres.go, redpanda.go, cmd/main.go, test |
| 2 | Tier 1 Static Discovery | ✅ Done | worker.go, analyzer.go (10 ATS patterns), emitter.go, cmd/main.go, test |
| 3 | Tier 2 CDP Interception | ✅ v3.2 | worker.go (Surgical), interceptor.go (Body Capture), classifier.go (7 signals), blocker.go, cmd/main.go |
| 4 | Tier 3 eBPF | ✅ Done | ssl_intercept.c (uprobes), internal/tier3/loader.go |
| 5 | ML Classifier | ✅ Done | model.py (16-feature ONNX), server.py (gRPC), classifier.proto, train.py |
| 6 | Replay Engine | ✅ Done | main.rs, scheduler.rs, replayer.rs, auth.rs, emitter.rs, Cargo.toml |
| 7 | Data Normalisation | ✅ Done | consumer.go, normaliser.go (field mapping), writer.go, cmd/main.go |
| 8 | Infrastructure | ✅ Done | terraform/main.tf (ECS Graviton4), prometheus.yml, alerts.yml (6 rules) |

**Total files: 44 across all teams**

## Still Needed (Phase 10 — real-world data/infra required)
- Fill in `writer.go` Postgres upsert SQL
- Wire `cilium/ebpf` in `tier3/loader.go` on Linux production nodes
- Collect 50K labelled API request training data for ML model
- Generate gRPC stubs from `classifier.proto`
- End-to-end smoke test with real URLs

---

## Important Constraints (Never Violate)

- Python: ML service + tooling scripts ONLY
- Every Chromium: hard 512MB memory ceiling (container level)
- All services: must handle SIGTERM gracefully
- All API payloads: written to S3 BEFORE any downstream processing
- No single point of failure — every service tolerates loss of any one node
- Tier 3 (eBPF): requires Linux 5.8+ kernel + root — NOT testable on macOS
- Strictly NO Direct ATS Targets: We must only target parent company career pages (e.g., careers.microsoft.com). We are strictly prohibited from feeding direct ATS domains (e.g., boards.greenhouse.io) into the URL queues.

---

## Job API Detection Signals (Stage 1 Filter)

Classify as target if 3+ signals present:
- URL contains: `/jobs`, `/careers`, `/positions`, `/openings`, `/graphql`, `/api/v*`
- POST body keys: `limit`, `offset`, `departments`, `jobType`, `locationId`, `operationName`
- Headers include: `Authorization: Bearer` or `x-api-key` + JSON content-type
- Query params: `?page=`, `?from=`, `?size=`

---

## Resources Needed (Ask User)

- [ ] AWS account credentials / region preference
- [ ] Redpanda cluster config (self-hosted or Redpanda Cloud?)
- [ ] Postgres RDS config (instance size?)
- [ ] Company URL list source (Common Crawl? Purchased list?)
- [ ] ML training data — 50,000 labelled API requests (existing or to be collected?)
- [ ] Residential proxy provider for Tier 3
---
107: 
108: ## Bottlenecks & Hardware Constraints (Resolved & Pending)
109: 
110: ### 1. Resolved: CDP Protocol Saturation (Tier 2 v3.1)
111: - **Issue**: M2 Mac hardware (8GB RAM) hit Mach port limits when initializing `fetch.Enable` per-request under burst load.
112: - **Fix**: Implemented **Pre-Armed Hot Tabs**. CDP domain is now enabled during background warming, removing 10s of swap-induced latency from the critical path.
113: 
114: ### 2. Resolved: Renderer-Level Deadlocks (Tier 2 v3.2)
115: - **Issue**: Worker timeouts (45s) left browser tabs in a 'paused' state, causing renderer hangs that blocked the entire pool.
116: - **Fix**: Decoupled `fetch.ContinueRequest` from the worker context. Cleanup now runs on `context.Background()` with a hard 2s guardrail.
117: 
118: ### 3. Current: M2 Swap Latency
119: - **Issue**: Heavy Chrome tabs induce disk swap on 8GB machines, causing 10s+ pauses in uTLS handshakes and CDP responses.
120: - **Mitigation**: Staggered pool startup (500ms delay) and aggressive RAM-saving (images disabled, `StopLoading` on hit).
121: 
122: ### 4. Resolved: POST Body Capture Regression
123: - **Issue**: Surgical engine originally missed `PostDataEntries`, disabling Signal 7 (GraphQL) classification.
124: - **Fix**: Implemented robust reconstruction from `network.PostDataEntry` slices.
