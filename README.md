# CareerScout

> A continuously-running intelligence pipeline that discovers, intercepts, and stores the hidden backend APIs of every major company career portal on the internet — targeting 1,000,000+ unique company domains on a rolling daily cycle.

---

## Core Insight

Every modern career page (React, Angular, Next.js) makes a hidden XHR/Fetch call to a private backend API to load its job listings. CareerScout intercepts that network call once, captures the endpoint, headers, and payload — then replays it cheaply and indefinitely **without ever opening a browser again**.

---

## Architecture Overview

```
URLs → [Tier 1: Static HTTP] → [Tier 2: CDP Chromium] → [Tier 3: eBPF Kernel]
                                       ↓
                              apis.discovered (Redpanda)
                                       ↓
                            [Replay Engine (Rust)]
                                       ↓
                            jobs.raw → [Normalisation (Go)]
                                       ↓
                               Product Database
```

---

## Technology Stack

| Component          | Technology              |
|--------------------|-------------------------|
| Orchestration      | Go (goroutines)         |
| Discovery Workers  | Go + chromedp (CDP)     |
| Replay Engine      | Rust + Tokio + Reqwest  |
| ML Classifier      | Python + ONNX Runtime   |
| Queue & Streaming  | Redpanda (Kafka-compat) |
| Database           | PostgreSQL + Redis + S3 |
| Infrastructure     | AWS ECS Graviton4 ARM Spot |

---

## Project Structure

```
careerscout/
├── cmd/
│   ├── ingestion/      # Team 1: URL ingestion & tier routing
│   ├── tier1/          # Team 2: Static discovery worker
│   ├── tier2/          # Team 3: CDP interception worker
│   ├── tier3/          # Team 4: eBPF sidecar process
│   └── normalise/      # Team 7: Data normalisation
├── internal/
│   ├── ingestion/      # Routing, rate limiting logic
│   ├── tier1/          # Static analysis logic
│   ├── tier2/          # Chromium pool, interceptor, classifier
│   ├── tier3/          # eBPF loader, ring buffer reader
│   ├── db/             # Postgres client
│   ├── queue/          # Redpanda producer/consumer
│   └── normalise/      # LLM field mapping, dedup, writer
├── ebpf/               # eBPF C program + Makefile
├── ml/
│   ├── classifier/     # Team 5: gRPC inference service
│   └── training/       # Training pipeline
├── replay/             # Team 6: Rust replay engine
├── infra/
│   ├── terraform/      # AWS infrastructure
│   └── monitoring/     # Prometheus + Grafana
├── schema/             # SQL migrations
└── docs/               # Architecture diagrams, ADRs
```

---

## Getting Started (Local Dev)

### Prerequisites
- Go 1.22+
- Rust 1.78+
- Python 3.11+
- Docker & Docker Compose
- Chromium (for Tier 2 testing)
- Linux 5.8+ kernel with root access (for Tier 3 only)

### Start local infrastructure
```bash
docker compose up -d   # starts Redpanda, Postgres, Redis
```

### Run individual services
```bash
# Team 1 — URL Ingestion
go run ./cmd/ingestion

# Team 2 — Tier 1 Static Worker
go run ./cmd/tier1

# Team 3 — Tier 2 CDP Worker
go run ./cmd/tier2

# ML Classifier
cd ml && pip install -r requirements.txt
python classifier/server.py

# Replay Engine
cd replay && cargo run

# Normalisation
go run ./cmd/normalise
```

---

## Milestones

| Milestone | Target |
|-----------|--------|
| Week 2    | Tier 1 + Tier 2 workers processing 1,000 URLs/day in dev |
| Week 4    | All 3 tiers + ML Classifier live. 50,000 URLs/day |
| Week 6    | Replay Engine live. 10,000 companies in rotation |
| Week 10   | 250,000 URLs/day. Auto auth-refresh working |
| Week 16   | 1,000,000 URLs/day. 85%+ Tier 2 success rate |

---

## Key Constraints

- Playwright, Puppeteer, Selenium — **banned**
- Python — **ML service and tooling only**, never in hot paths
- Every Chromium instance — hard 512MB memory ceiling
- All services must handle SIGTERM gracefully
- All API payloads written to S3 before downstream processing
- No single point of failure

---

*CareerScout v1.0 — Confidential*
