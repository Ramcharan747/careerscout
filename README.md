<div align="center">

# 🔍 CareerScout

**The open-source engine that maps every company's hiring infrastructure on the internet.**

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://golang.org)
[![Rust](https://img.shields.io/badge/Rust-Tokio-DEA584?style=for-the-badge&logo=rust&logoColor=white)](https://www.rust-lang.org)
[![Python](https://img.shields.io/badge/Python-ML-3776AB?style=for-the-badge&logo=python&logoColor=white)](https://python.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=for-the-badge&logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=for-the-badge)](LICENSE)
[![CI](https://github.com/Ramcharan747/careerscout/actions/workflows/ci.yml/badge.svg)](https://github.com/Ramcharan747/careerscout/actions/workflows/ci.yml)

*Discovers career pages, detects ATS platforms, extracts job APIs, and normalises job data — all at scale.*

[Architecture](#architecture) · [Tools](#tools) · [Quick Start](#quick-start) · [How It Works](#how-it-works) · [Results](#results)

</div>

---

## 🚀 What Is This?

CareerScout is a **multi-stage intelligence pipeline** that automatically:

1. **Discovers** career pages across millions of company domains
2. **Detects** which Applicant Tracking System (ATS) each company uses
3. **Extracts** the hidden JSON APIs behind those career pages
4. **Probes** ATS platforms directly to find companies the web missed
5. **Fetches** and **normalises** structured job data from every discovered source

It supports **17 ATS platforms** out of the box and has been tested against **5.5 million company domains**.

---

## 🏗️ Architecture

```
                         ┌──────────────────────────────────────────────────────────┐
                         │                    DATA SOURCES                          │
                         │  PDL Dataset (35M)  ·  Wayback Machine  ·  CT Logs       │
                         │  S&P 500  ·  Fortune 500  ·  Majestic Million            │
                         └─────────────────────────┬────────────────────────────────┘
                                                   │
                    ┌──────────────────────────────▼──────────────────────────────┐
                    │                      DISCOVERY LAYER                        │
                    │                                                             │
                    │  ┌─────────────┐  ┌──────────────┐  ┌───────────────────┐   │
                    │  │Career Finder│  │  ATS Prober  │  │ Workday Prober    │   │
                    │  │ 200 workers │  │  14 platforms │  │ 4 envs × fallback│   │
                    │  │ 5.5M domains│  │  slug probing │  │ CDX extraction   │   │
                    │  └──────┬──────┘  └──────┬───────┘  └───────┬───────────┘   │
                    └─────────┴───────────────┴──────────────────┴───────────────┘
                                              │
                    ┌─────────────────────────▼──────────────────────────────────┐
                    │                   INTERCEPTION LAYER                        │
                    │                                                             │
                    │  Tier 1: Static HTTP  →  Tier 2: CDP Chromium  →  Tier 3    │
                    │  (pattern matching)      (network interception)    (eBPF)   │
                    │                                                             │
                    │  ML Classifier (ONNX) filters false positives               │
                    └─────────────────────────┬─────────────────────────────────┘
                                              │
                    ┌─────────────────────────▼──────────────────────────────────┐
                    │                     DATA LAYER                              │
                    │                                                             │
                    │  Job Fetcher  →  Schema Parser  →  Normaliser  →  Postgres  │
                    │  (rate-limited)   (17 schemas)     (Go)           (product)  │
                    │                                                             │
                    │  Replay Engine (Rust/Tokio) for continuous re-fetching       │
                    └────────────────────────────────────────────────────────────┘
```

---

## 🛠️ Tools

CareerScout is built as a collection of focused, standalone Go binaries:

### Discovery & Probing

| Tool | Description |
|------|-------------|
| `cmd/career_finder` | Probes 7 URL patterns across millions of domains to find career pages. Detects 15+ ATS platforms via HTML analysis of `href`/`src` attributes. 200 concurrent workers with configurable timeouts. |
| `cmd/probe_ats` | Brute-force probes slugs against 14 ATS APIs (Greenhouse, Lever, Ashby, Workable, BambooHR, Recruitee, Teamtailor, Rippling, Pinpoint, Freshteam, SmartRecruiters, Jobvite, BreezyHR, Personio). |
| `cmd/probe_workday` | Specialised Workday prober — tests 4 environments (`wd1`/`wd3`/`wd5`/`wd12`) × 8 fallback board names per company. |
| `cmd/harvest_companies` | Extracts company slugs from Wayback Machine, CT logs, and web scraping. |
| `cmd/discover` | Priority-frontier URL crawler with domain-level learning and politeness governor. |

### Data Pipeline

| Tool | Description |
|------|-------------|
| `cmd/fetch_jobs` | Fetches job listings from confirmed ATS APIs. Platform-specific request handling (POST for Workable/Ashby, GET for others). Sequential rate limiting for aggressive APIs. |
| `cmd/ingestion` | URL ingestion service with tier-based routing and rate limiting. |
| `cmd/tier1` | Static HTTP analysis worker — pattern matching for API endpoints. |
| `cmd/tier2` | CDP-based Chromium worker — intercepts XHR/Fetch calls to capture hidden APIs. |
| `cmd/normalise` | Transforms raw API responses into structured job records using schema-driven parsing. |

### Analysis & Quality

| Tool | Description |
|------|-------------|
| `cmd/label_captures` | Interactive CLI for labeling API captures as job-related or false positives. |
| `cmd/review_nearmiss` | Reviews near-miss captures that almost matched job API patterns. |
| `cmd/validate_urls` | Validates URL reachability and correctness before pipeline ingestion. |

---

## ⚡ Supported ATS Platforms

| Platform | Detection | API Probing | Job Parsing |
|----------|:---------:|:-----------:|:-----------:|
| Greenhouse | ✅ | ✅ | ✅ |
| Lever | ✅ | ✅ | ✅ |
| Workday | ✅ | ✅ | ✅ |
| Ashby | ✅ | ✅ | ✅ |
| Workable | ✅ | ✅ | ✅ |
| SmartRecruiters | ✅ | ✅ | ✅ |
| BambooHR | ✅ | ✅ | ✅ |
| Recruitee | ✅ | ✅ | ✅ |
| Teamtailor | ✅ | ✅ | ✅ |
| Rippling | ✅ | ✅ | ✅ |
| Pinpoint | ✅ | ✅ | ✅ |
| Freshteam | ✅ | ✅ | ✅ |
| Jobvite | ✅ | ✅ | ✅ |
| BreezyHR | ✅ | ✅ | ✅ |
| Personio | ✅ | ✅ | ✅ |

---

## 📊 Results

Real numbers from production runs:

| Metric | Value |
|--------|-------|
| Companies scanned | **5,530,940** |
| Career pages discovered | ~**930,000** (16.8% hit rate) |
| ATS platforms detected | **15** |
| Workday boards confirmed | **155** unique companies |
| ATS API sources confirmed | **2,100+** via direct probing |
| Job schemas supported | **17** with field-level parsing |
| Probing throughput | **200+ domains/sec** |

---

## 📁 Project Structure

```
careerscout/
├── cmd/                        # Standalone Go binaries
│   ├── career_finder/          # Mass career page discovery + ATS detection
│   ├── probe_ats/              # Multi-platform ATS slug prober
│   ├── probe_workday/          # Workday-specific environment prober
│   ├── fetch_jobs/             # Job data fetcher with rate limiting
│   ├── discover/               # Priority-frontier URL crawler
│   ├── ingestion/              # URL ingestion & tier routing
│   ├── tier1/                  # Static HTTP analysis worker
│   ├── tier2/                  # CDP Chromium interception worker
│   ├── tier2_v3/               # Hardened Tier 2 with metrics
│   ├── normalise/              # Data normalisation service
│   ├── harvest_companies/      # Company slug harvester
│   ├── harvest_urls/           # URL discovery from multiple sources
│   ├── label_captures/         # Interactive capture labeling tool
│   └── ...                     # 15+ more specialised tools
├── internal/                   # Shared Go packages
│   ├── atsprober/              # ATS probing logic & rate limiters
│   ├── jobparser/              # Schema-driven job parsing (17 schemas)
│   ├── capture/                # Network capture analysis
│   ├── frontier/               # Priority queue with domain feedback
│   ├── ingestion/              # Rate limiting & routing logic
│   ├── tier1/                  # Static analysis engine
│   ├── tier2_v3/               # CDP interception engine
│   ├── db/                     # PostgreSQL client
│   ├── queue/                  # Redpanda/Kafka producer-consumer
│   ├── normalise/              # Field mapping & deduplication
│   └── resolver/               # Domain resolution utilities
├── ml/                         # Machine learning
│   ├── classifier/             # gRPC inference service (ONNX)
│   └── training/               # Model training pipeline
├── replay/                     # Rust replay engine (Tokio + Reqwest)
├── ebpf/                       # eBPF kernel-level capture (Linux)
├── schema/                     # SQL migrations
├── scripts/                    # Deployment & maintenance scripts
├── infra/                      # Terraform + Prometheus + Grafana
├── grafana/                    # Dashboard definitions
└── docs/                       # Architecture docs & ADRs
```

---

## 🚀 Quick Start

### Prerequisites

- Go 1.22+
- PostgreSQL 16+
- Docker & Docker Compose

### 1. Clone & Setup

```bash
git clone https://github.com/Ramcharan747/careerscout.git
cd careerscout
```

### 2. Start Infrastructure

```bash
docker compose up -d   # PostgreSQL, Redpanda, Redis, Prometheus
```

### 3. Run Database Migrations

```bash
psql $DATABASE_URL < schema/001_initial.sql
```

### 4. Run Discovery Tools

```bash
# Discover career pages across domains
LIMIT=1000 TIMEOUT_MS=3000 go run ./cmd/career_finder

# Probe ATS platforms for known slugs
WORKER_COUNT=20 LIMIT=100 DATABASE_URL="..." go run ./cmd/probe_ats

# Probe Workday specifically
go run ./cmd/probe_workday

# Fetch jobs from confirmed sources
DATABASE_URL="..." go run ./cmd/fetch_jobs
```

### 5. Cross-Compile for Linux

```bash
GOOS=linux GOARCH=amd64 go build -o career_finder ./cmd/career_finder
GOOS=linux GOARCH=amd64 go build -o probe_ats ./cmd/probe_ats
GOOS=linux GOARCH=amd64 go build -o fetch_jobs ./cmd/fetch_jobs
```

---

## 🔧 How It Works

### Career Page Discovery

The `career_finder` tool takes a list of company domains and probes 7 common URL patterns:

```
https://domain.com/careers
https://domain.com/jobs
https://domain.com/about/careers
https://domain.com/company/careers
https://domain.com/en/careers
https://careers.domain.com
https://jobs.domain.com
```

When a career page responds with HTTP 200, the HTML is scanned for ATS platform indicators — but **only inside `href`, `src`, and `action` attributes**, preventing false positives from casual text mentions.

### Schema-Driven Job Parsing

Each ATS platform has a registered schema in `internal/jobparser/parser.go` that defines:

```go
"greenhouse": {
    ATSPlatform:     "greenhouse",
    JobsPath:        "jobs",           // Where is the array?
    FieldExternalID: "id",             // Dot-notation field paths
    FieldTitle:      "title",
    FieldLocationRaw: "location.name",
    FieldDepartment: "departments[0].name",
    FieldApplyURL:   "absolute_url",
    FieldPostedAt:   "first_published",
    PostedAtFormat:  "rfc3339",
    ExternalIDIsInt: true,
}
```

Adding a new ATS requires **zero code changes** — just add one schema entry.

### Workday Discovery

Workday is the largest enterprise ATS but has no public directory. CareerScout uses a novel approach:

1. **CDX Extraction** — Queries the Wayback Machine for all `*.wd{1,3,5,12}.myworkdayjobs.com` URLs
2. **Board Enumeration** — Extracts company subdomains and board names from archived URLs
3. **Environment Brute-Force** — Tests each company across all 4 Workday environments with 8 fallback board names
4. **API Verification** — Sends POST requests to the undocumented `/wday/cxs/` API to confirm live job boards

---

## 🔬 Technical Highlights

- **Zero-browser job fetching** — Once an API endpoint is discovered, jobs are fetched via simple HTTP forever
- **Schema-driven parsing** — 17 ATS schemas with dot-notation field paths, array indexing, and lookup tables
- **Platform-specific protocols** — POST for Workable/Ashby, GraphQL for Ashby, GET for most others
- **Concurrent architecture** — 200 goroutine worker pools with per-platform rate limiting
- **Checkpoint/resume** — Every tool supports crash recovery via JSON checkpointing
- **eBPF kernel capture** — Tier 3 uses eBPF to intercept `connect()` syscalls at the kernel level
- **ML false-positive filtering** — ONNX-based classifier trained on labeled API captures
- **Priority frontier** — Learning-based URL priority queue with domain feedback scores

---

## 🤝 Contributing

Contributions are welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

Areas where help is needed:
- Adding new ATS platform schemas
- Improving career page URL pattern coverage
- Training data for the ML classifier
- International career page patterns (non-English)

---

## 📄 License

This project is licensed under the MIT License — see the [LICENSE](LICENSE) file for details.

---

<div align="center">

**Built with obsessive attention to scale.**

*CareerScout — Mapping the world's hiring infrastructure, one API at a time.*

</div>
