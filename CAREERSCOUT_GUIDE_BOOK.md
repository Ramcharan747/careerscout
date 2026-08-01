# CareerScout: The Definitive Technical Guide

## Table of Contents
1. [Introduction and Architectural Overview](#1-introduction-and-architectural-overview)
2. [Go Command Entrypoints (`cmd/`)](#2-go-command-entrypoints)
3. [Internal Core Libraries (`internal/`)](#3-internal-core-libraries)
4. [Rust Replay Engine (`replay/`)](#4-rust-replay-engine)
5. [Machine Learning & Classifiers (`ml/`)](#5-machine-learning--classifiers)
6. [Network & Kernel-Level Capture (`ebpf/`)](#6-network--kernel-level-capture)
7. [Database Schemas & Data Pipelines (`schema/`, `migrations/`)](#7-database-schemas--data-pipelines)
8. [Real-world Diagnostics & Reliability Analysis](#8-real-world-diagnostics--reliability-analysis)

---

## 1. Introduction and Architectural Overview

CareerScout is a monumental engineering feat. It acts as an open-source intelligence pipeline that maps out the world's hiring infrastructure. By scanning over 5.5 million company domains, it seeks out career pages, detects Applicant Tracking Systems (ATS), extracts job APIs, and normalizes job data into a relational database.

### The Problem It Solves
The hiring landscape is fragmented across dozens of ATS platforms (Greenhouse, Lever, Workday, etc.). Scraping individual job boards is brittle. CareerScout solves this by discovering the underlying undocumented APIs that power these career pages, extracting data at the source.

### The 3-Tier Interception Model
To achieve a near-perfect hit rate, the system scales its intrusiveness across three tiers:
- **Tier 1 (Static HTTP):** Fast, inexpensive matching. Downloads HTML using `fasthttp` and uses regular expressions to find hardcoded URLs within `href`, `src`, and `action` tags.
- **Tier 2 (CDP Chromium):** For Single Page Applications (SPAs). It runs headless Chromium via `go-rod`, rendering JavaScript and intercepting raw XHR/Fetch network calls to capture the API payloads dynamically.
- **Tier 3 (eBPF Kernel Capture):** The "nuclear option." When frontend obfuscation masks network traffic, CareerScout uses an eBPF `ssl_intercept.c` program attached to kernel `uprobes` on OpenSSL/BoringSSL. It captures raw plaintext HTTP requests/responses before encryption.

### System Architecture
The system is composed of several decoupled microservices connected by PostgreSQL and Kafka/Redpanda:
1. **Discovery Layer:** Tools like `career_finder` and `probe_ats` ingest massive domain lists and hunt for active ATS endpoints.
2. **Interception Layer:** The Tiers 1-3.
3. **Data Layer:** `fetch_jobs` and `normalise` extract structured JSON using 17 schema definitions.
4. **Replay Engine:** A Rust/Tokio async engine for high-speed continuous polling of confirmed APIs.
5. **Machine Learning:** A Python-based ONNX classifier filters false positives out of Tier 2/3 interceptions.

---

## 2. Go Command Entrypoints (`cmd/`)

The `cmd/` directory is the beating heart of CareerScout, containing over 20 standalone Go binaries. Each binary is designed for horizontal scaling, meaning you can spin up 10 instances of `tier2` workers across 10 machines, and they will coordinate via Redpanda.

### 2.1 `cmd/career_finder/main.go`
The tip of the spear. `career_finder` ingests a list of companies (e.g., from `companies_filtered.csv`) and probes common paths like `/careers` and `/jobs`.
- **Line-by-Line Logic:**
  - **Initialization:** It reads the CSV, parses the target limit (`LIMIT` env var), and loads a checkpoint file (`career_finder_checkpoint.json`) to resume where it left off.
  - **Worker Pool:** It launches a massive pool of 200 `goroutines`. Each worker listens on a `jobsChan`.
  - **Execution:** For each URL, it sets `InsecureSkipVerify: true` (ignoring bad SSL certs is crucial for bulk scanning) and follows up to 3 redirects.
  - **ATS Detection:** If it receives an HTTP 200, it passes the HTML body to regex scanners looking for `greenhouse.io`, `lever.co`, etc., but restricts matches strictly to valid HTML attributes to avoid false positives.

### 2.2 `cmd/discover/main.go`
The consolidated orchestrator. Rather than manual scripts, `discover` uses a priority queue (`internal/frontier`).
- **Logic:**
  - Consumes URLs from `INPUT_MODE=redpanda` or `postgres`.
  - Dedupes using `BloomDeduper` (`internal/ingestion/bloom.go`) persisted to `bloom.bin`.
  - Evaluates domains against the `HostGovernor` (`internal/frontier/politeness.go`) to ensure it doesn't spam a single company (e.g., maximum 5 concurrent connections per domain).
  - Routes valid, polite URLs to the internal `tier2_v3.WorkerPool` for browser execution.

### 2.3 `cmd/probe_ats/main.go`
A brute-force prober that doesn't crawl web pages. Instead, it generates permutations of known ATS API endpoints.
- **Logic:**
  - Takes a company slug (e.g., `acmecorp`) and checks `api.greenhouse.io/v1/boards/acmecorp/jobs`.
  - Uses `golang.org/x/time/rate` to enforce a strict rate limit of 5 req/sec per platform, avoiding IP bans from the ATS vendors.
  - Workers parse the JSON. If the response matches the expected schema (e.g., contains a `jobs` array), the endpoint is saved to `data_sources`.

### 2.4 `cmd/probe_workday/main.go`
Workday is the hardest ATS to scrape because it uses dynamic CSRF tokens (`wday_v`), complex subdomains (`wd1`, `wd3`, `wd5`), and custom paths.
- **Logic:**
  - Parses `workday_companies.txt`.
  - Iterates through a massive loop: `for env := range workdayEnvs { for board := range boards { ... } }`.
  - **Phase 1:** Performs an initial GET request to extract the session cookies and CSRF token from the HTML body.
  - **Phase 2:** Issues a POST request to the undocumented `/wday/cxs/` API with the extracted tokens. If it returns a 200 OK with job data, it succeeds.

### 2.5 The Tier Workers (`tier1`, `tier2_v3`)
- **`tier1/main.go`:** Uses `valyala/fasthttp`, a high-performance HTTP client allocation-free library, to download pages in milliseconds. It relies on a custom concurrent DNS resolver (`internal/resolver`) to avoid overwhelming the OS resolver.
- **`tier2_v3/main.go`:** The ultimate browser automation worker.
  - Uses `github.com/go-rod/rod` to connect to a headless Chromium instance.
  - Opens a tab, injects stealth scripts (to evade basic bot protection), and overrides the `window.fetch` and `XMLHttpRequest` prototypes to capture outbound API payloads.
  - Filters out noise (CSS, images) to save memory.
  - Sends the captured payload to `ml/classifier` via gRPC for verification.

---

## 3. Internal Core Libraries (`internal/`)

The `internal/` packages provide the shared logic and domain primitives for the standalone command-line tools. They handle everything from parsing undocumented APIs to managing massive-scale domain politeness.

### 3.1 `internal/frontier`
This package orchestrates the crawl order and prevents the crawler from becoming a disruptive DDoS tool.
- **Priority Queue (`frontier.go`):** Implements `container/heap`. URLs are popped based on a `Score`. The score is calculated by a static heuristic (e.g., a path ending in `/careers` starts with a high score, `/about` with a lower one) plus a dynamic `ScoreBoost`.
- **Feedback Loop (`feedback.go`):** Persists state to `feedback.json`. If a URL on `acme.com` successfully yields a job API, the `FeedbackStore` records it. Subsequent URLs from `acme.com` receive a massive `ScoreBoost`.
- **Politeness Governor (`politeness.go`):** Uses a thread-safe map and `sync.Mutex` to count in-flight connections per hostname. It enforces a strict upper limit (e.g., 5 concurrent connections) to protect the target server.

### 3.2 `internal/ingestion`
Handles deduplication and rate-limiting at the very entrance of the pipeline.
- **Bloom Deduper (`bloom.go`):** Uses `github.com/bits-and-blooms/bloom` to filter out previously seen URLs. A Bloom filter is highly memory-efficient for millions of URLs. It persists its binary state to `bloom.bin` to survive process restarts.
- **Rate Limiter (`ratelimiter.go`):** Applies a sliding-window algorithm to ensure a domain isn't scanned too frequently within a 4-hour cooldown period.

### 3.3 `internal/jobparser`
The universal translation engine. Every ATS has a completely different JSON structure. `jobparser` uses declarative schemas mapped by dot-notation JSON paths.
- **Example Schema logic:** `departments[0].name` extracts the department string from an array.
- Parses 22 distinct ATS structures out-of-the-box.
- Features a dynamic fallback: if an unknown JSON structure is intercepted, it can query the **Gemini 2.0 Flash API** to generate the parsing schema dynamically.

### 3.4 `internal/db` and `internal/queue`
- **`db`:** Uses `pgxpool` (`github.com/jackc/pgx/v5`) for high-performance PostgreSQL connection pooling. It provides atomic transaction wrappers for massive bulk inserts (`pgx.Batch`).
- **`queue`:** Wraps `franz-go` to connect to Redpanda/Kafka. The pipeline uses topics like `urls.to_process`, `urls.tier1_queue`, `urls.tier2_queue`, and `jobs.raw` to decouple discovery from parsing.

### 3.5 The Tiers (`tier1`, `tier2_v3`, `tier3`)
- **`tier1`:** Utilizes pre-compiled regex arrays (`analyzer.go`) to rapidly scan the raw HTML text of `fasthttp` responses.
- **`tier2_v3`:** 
  - **Browser Pool:** Uses a `go-rod` tab pool.
  - **Blocker (`blocker.go`):** Intercepts and blocks loading of CSS, images, and tracking scripts (Google Analytics, Mixpanel) to vastly reduce memory usage per tab.
  - **Interceptor (`interceptor.go`):** Injects JavaScript into the browser page context to hook `window.fetch` and `XMLHttpRequest.prototype.send`, logging all outgoing payloads.
  - **Classifier (`classifier.go`):** Scores intercepted payloads using regex heuristics (`generated_patterns.go`) or the Python ML gRPC server.
- **`tier3`:** The Go sidecar loader for eBPF. It finds the Chromium process PID, locates `libssl.so` in `/proc/<pid>/maps`, compiles the C code, and attaches kernel uprobes. It reads the raw intercepted plaintext traffic directly from the BPF ring buffer.

---

## 4. Rust Replay Engine (`replay/`)

Once an API endpoint is discovered, spinning up headless browsers (Tier 2) or eBPF (Tier 3) to fetch new jobs every week is wildly inefficient. The Rust Replay Engine takes over to perform continuous fetching using raw HTTP requests.

### 4.1 Architecture and Concurrency
Built on Tokio, the async engine queries PostgreSQL for target APIs that are due for a refresh (`next_replay <= NOW()`). 
- **`main.rs`:** Sets up `sqlx` (PostgreSQL), `reqwest` (HTTP Client with Brotli/Gzip decompression), and `rdkafka` (Redpanda producer).
- **`scheduler.rs`:** Polls the database every 30 seconds. It limits concurrency using `tokio::sync::Semaphore` (e.g., 500 concurrent connections).
- **`replayer.rs`:** Rebuilds the original HTTP request (GET/POST) using the exact headers, cookies, and JSON body payload saved during the initial discovery phase.
- **`emitter.rs`:** Pushes the fetched raw JSON job data into the `jobs.raw` Redpanda topic.

### 4.2 Stale Token Recovery (`auth.rs`)
Many APIs (like Workday) use temporary CSRF tokens or session cookies. When the Rust engine receives a `401 Unauthorized` or `403 Forbidden` response, `auth.rs` kicks in. It marks the endpoint as `stale` in PostgreSQL. This signals the Go discovery pipeline to re-run the domain through Tier 2 to capture a fresh session token.

---

## 5. Machine Learning & Classifiers (`ml/`)

When Tier 2 intercepts a JSON response, it doesn't know if it's a list of jobs or just a product catalog. The Python ML stack provides a high-confidence false-positive filter.

### 5.1 Training (`train.py`)
- Reads labeled JSONL datasets containing the URL, HTTP method, headers, and body.
- Engineers 16 specific features (e.g., URL path depth, presence of pagination parameters like `offset=`, ATS domains, header counts, and body length buckets).
- Trains an XGBoost classifier with `scale_pos_weight` to handle imbalanced data.
- Exports the model to ONNX format (`model.onnx`).

### 5.2 Inference Server (`server.py` & `model.py`)
- **gRPC Interface:** Defined in `classifier.proto`, it exposes a `ClassifierService` on port `50051`.
- **ONNX Runtime:** Uses `onnxruntime.InferenceSession` for sub-2ms CPU inference.
- If the model file is missing, it falls back to a deterministic sum-based heuristic score.

---

## 6. Network & Kernel-Level Capture (`ebpf/`)

The eBPF module is CareerScout's Tier 3 mechanism. It runs when a target company has anti-bot protections (like Cloudflare or Akamai) that trigger on headless browsers or custom headers. By hooking Chromium's OpenSSL dynamic library calls directly inside the kernel, it intercepts outbound requests in plaintext before TLS encryption.

### 6.1 `ssl_intercept.c`
- **Uprobe Attachments:** It attaches kernel uprobes to the addresses of `SSL_write` and `SSL_read_ex`.
- **Kernel-to-Userspace Streaming:** When Chromium invokes `SSL_write` (to send a request to a server), BPF catches the call, reserves a slot in the ring buffer (`bpf_ringbuf_reserve`), copies the raw request plaintext buffer prior to TLS encryption using `bpf_probe_read_user`, and submits the event to the ring buffer.

### 6.2 `internal/tier3/loader.go`
- **Go Sidecar Loader:** The Go program serves as the BPF driver. It reads the target Chromium process ID (PID) and compiles/loads `ssl_intercept.o` into the Linux kernel.
- **Process Interception:** The Go sidecar queries `/proc/<pid>/maps` to find the path of Chromium's SSL library (`libssl.so`).
- It continuously polls the 256MB eBPF ring buffer map (`ringbuf`), reads captured payload structs, and streams them into the CareerScout classifier and database logic.

---

## 7. Database Schemas & Data Pipelines (`schema/`, `migrations/`)

The backbone of CareerScout is its PostgreSQL relational database.

### 7.1 Core Tables
- `companies`: Primary catalog of discovered target domains.
- `discovery_records`: Tracks the result of every scan attempt across Tier 1, 2, and 3. Records state as `pending`, `discovered`, `failed`, or `stale`. Stores the intercepted API URL, headers, and payload bodies.
- `jobs`: Stores the normalized job listings, including extracted `Title`, `Location`, `Department`, `PostedAt`, and `ApplyURL`.

### 7.2 Data Pipelines
- High-volume data uses the Redpanda/Kafka pipeline. Raw discoveries are pushed to Redpanda for asynchronous evaluation. Normalized job definitions are pushed to `jobs.raw` to be consumed by `internal/normalise/consumer.go`, batch processed, and inserted into PostgreSQL using atomic `pgx.Batch` transactions.

---

## 8. Real-world Diagnostics & Reliability Analysis

When running massive scale network scans against highly fortified corporate networks, failures are expected. Diagnosing failures requires looking closely at network logs.

### 8.1 Common Failure Modes
- **Workday Timeouts:** Workday tenant servers frequently ratelimit or drop connections during brute-force scanning (as seen in `probe_workday`). The timeout behavior leads to hanging goroutines.
- **Chromium Memory Leaks (Tier 2):** Running thousands of headless Chromium tabs per minute naturally causes severe memory spikes. `go-rod` limits the tab count, but runaway tabs with infinite React loops require hard kills.
- **Stale Auth Loop:** When tokens expire, the Replay Engine correctly marks endpoints as stale. However, if the target site fundamentally changed its API schema, the Tier 2 re-discovery loop might continually fail to find a valid new token.

### 8.2 Reliability Enhancements
To improve reliability, developers should:
1. Increase strict timeouts on `probe_workday` HTTP clients to prevent connection hangs.
2. Ensure the `Blocker` in Tier 2 aggressively filters non-essential resources (e.g., video assets) to conserve memory.
3. Improve exponential backoff algorithms in `internal/ingestion/ratelimiter.go` to deal with 429 Too Many Requests statuses gracefully.

---
**End of Document**
