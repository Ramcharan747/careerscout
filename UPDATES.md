# UPDATES — CareerScout Build Log

> Chronological log of everything built. Newest entry at the top.

---

## 2026-03-07 — Session 1: Full Foundation Build (ALL 8 TEAMS)

### What Was Done
- Read and analysed `careerscout_foundation.md`
- Created full project folder structure
- Built all 8 engineering teams end-to-end: 44 source files

### All Files Created
```
careerscout/
├── README.md / MEMORY.md / UPDATES.md / DIRECTIONS.md
├── go.mod, docker-compose.yml
├── schema/001_initial.sql
├── cmd/{ingestion,tier1,tier2,normalise}/main.go
├── internal/
│   ├── db/postgres.go
│   ├── queue/redpanda.go
│   ├── ingestion/{router,ratelimiter,ratelimiter_test}.go
│   ├── tier1/{worker,analyzer,analyzer_test,emitter}.go
│   ├── tier2/{worker,interceptor,classifier,classifier_test,blocker}.go
│   ├── tier3/loader.go
│   └── normalise/{consumer,normaliser,writer}.go
├── ebpf/ssl_intercept.c
├── ml/
│   ├── classifier/{model.py,server.py,proto/classifier.proto}
│   └── training/train.py
├── replay/
│   ├── Cargo.toml
│   └── src/{main,scheduler,replayer,auth,emitter}.rs
└── infra/
    ├── terraform/main.tf
    └── monitoring/{prometheus.yml,alerts.yml}
```

### Still Needed (need real-world data/infra)
- Postgres upsert SQL in `writer.go`
- `cilium/ebpf` production wiring in `tier3/loader.go`
- 50K labelled training samples for ML model
- gRPC stub generation from `classifier.proto`

---

## Session Template (copy for next session)

## YYYY-MM-DD — Session N: [Focus Area]

### What Was Done
-

### Current Status


### Files Created/Modified
```
```
