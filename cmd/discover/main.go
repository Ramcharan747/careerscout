// cmd/discover/main.go
// Consolidated pipeline for CareerScout discovery.
// Reads from Postgres OR Redpanda, pushes URLs into a priority Frontier,
// processes them with the CDP-Surgical worker, and writes results to Postgres
// (and optionally Redpanda).
//
// Usage:
//
//	DATABASE_URL=... INPUT_MODE=postgres|redpanda ./discover [--workers 4] [--limit 20]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/careerscout/careerscout/internal/frontier"
	"github.com/careerscout/careerscout/internal/ingestion"
	"github.com/careerscout/careerscout/internal/queue"
	"github.com/careerscout/careerscout/internal/tier2_v3"
)

// ── Types ─────────────────────────────────────────────────────────────────────

type Company struct {
	ID     string
	Name   string
	Domain string
}

type urlMessage struct {
	CompanyID string `json:"company_id"`
	Domain    string `json:"domain"`
	RawURL    string `json:"raw_url"`
}

// ── DB helpers ────────────────────────────────────────────────────────────────

func loadCompanies(ctx context.Context, pool *pgxpool.Pool, batchSize int) ([]Company, error) {
	var rows pgx.Rows
	var err error
	if batchSize > 0 {
		rows, err = pool.Query(ctx, `
			SELECT c.id, COALESCE(c.name, c.domain), c.domain
			FROM companies c
			LEFT JOIN discovery_records dr ON dr.domain = c.domain
			WHERE dr.domain IS NULL OR dr.status NOT IN ('discovered')
			ORDER BY c.created_at
			LIMIT $1
		`, batchSize)
	} else {
		rows, err = pool.Query(ctx, `
			SELECT c.id, COALESCE(c.name, c.domain), c.domain
			FROM companies c
			LEFT JOIN discovery_records dr ON dr.domain = c.domain
			WHERE dr.domain IS NULL OR dr.status NOT IN ('discovered')
			ORDER BY c.created_at
		`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Company
	for rows.Next() {
		var co Company
		if err := rows.Scan(&co.ID, &co.Name, &co.Domain); err != nil {
			continue
		}
		out = append(out, co)
	}
	return out, rows.Err()
}

func saveDiscovered(ctx context.Context, pool *pgxpool.Pool, co Company, result tier2_v3.CDPResult) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO discovery_records
			(company_id, domain, status, tier_used, api_url, http_method, request_body, confidence, discovered_at, next_replay, updated_at)
		VALUES
			($1, $2, 'discovered', 'tier2', $3, $4, $5, $6, NOW(), NOW() + INTERVAL '1 hour', NOW())
		ON CONFLICT (domain) DO UPDATE SET
			status        = 'discovered',
			tier_used     = 'tier2',
			api_url       = EXCLUDED.api_url,
			http_method   = EXCLUDED.http_method,
			request_body  = EXCLUDED.request_body,
			confidence    = EXCLUDED.confidence,
			discovered_at = NOW(),
			next_replay   = NOW() + INTERVAL '1 hour',
			consecutive_failures = 0,
			last_error    = NULL,
			updated_at    = NOW()
	`, co.ID, co.Domain, result.APIURL, result.HTTPMethod, result.Body, result.Confidence)
	return err
}

func saveFailed(ctx context.Context, pool *pgxpool.Pool, co Company, reason string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO discovery_records (company_id, domain, status, tier_used, consecutive_failures, last_error, updated_at)
		VALUES ($1, $2, 'pending', 'tier2', 1, $3, NOW())
		ON CONFLICT (domain) DO UPDATE SET
			consecutive_failures = discovery_records.consecutive_failures + 1,
			last_error = EXCLUDED.last_error,
			tier_used = COALESCE(discovery_records.tier_used, 'tier2'),
			updated_at = NOW(),
			status = CASE
				WHEN discovery_records.consecutive_failures + 1 >= 3 THEN 'failed'::discovery_status
				ELSE discovery_records.status
			END
	`, co.ID, co.Domain, reason)
	return err
}

func getCanonicalURL(domain string) string {
	careerURL := "https://" + domain
	if !strings.Contains(domain, "career") && !strings.Contains(domain, "job") {
		careerURL = "https://" + domain + "/careers"
	}
	return careerURL
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	workers := flag.Int("workers", 3, "concurrent browser tabs")
	flag.Parse()

	// Override --workers with WORKER_COUNT env var if set
	if wc := os.Getenv("WORKER_COUNT"); wc != "" {
		if n, err := strconv.Atoi(wc); err == nil && n > 0 {
			*workers = n
		}
	}

	batchSizeStr := getEnv("BATCH_SIZE", "0")
	batchSize, err := strconv.Atoi(batchSizeStr)
	if err != nil || batchSize < 0 {
		fmt.Println("BATCH_SIZE must be 0 (unlimited) or a positive integer")
		os.Exit(1)
	}

	minConfStr := getEnv("MIN_CONFIDENCE", "0.55")
	minConf, err := strconv.ParseFloat(minConfStr, 64)
	if err != nil {
		minConf = 0.55
	}

	cfg := zap.NewDevelopmentConfig()
	cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	log, _ := cfg.Build()
	defer log.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runID := "discover_" + time.Now().UTC().Format("20060102_150405")

	// ── Metrics ──
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":9090", nil); err != nil {
			log.Error("prometheus server failed", zap.Error(err))
		}
	}()

	// ── Configuration ──
	inputMode := getEnv("INPUT_MODE", "postgres")
	outputTopic := os.Getenv("OUTPUT_TOPIC")
	ingestionTopic := getEnv("INGESTION_TOPIC", "urls.to_process")
	dsn := getEnv("DATABASE_URL", "postgres://careerscout:careerscout_dev_password@localhost:5432/careerscout?sslmode=disable")
	redpandaBrokersStr := getEnv("REDPANDA_BROKERS", "localhost:19092")
	redpandaBrokers := strings.Split(redpandaBrokersStr, ",")

	// ── DB ──
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal("db connect failed", zap.Error(err))
	}
	defer pool.Close()

	// ── Frontier & Feedback Store ──
	feedbackPath := frontier.GetEnvStatePath()
	fb := frontier.NewFeedbackStore()
	if err := fb.Load(feedbackPath); err != nil {
		log.Warn("failed to load feedback store", zap.Error(err))
	}

	fb.Seed("greenhouse.io", 50, 5)
	fb.Seed("lever.co", 50, 5)
	fb.Seed("ashbyhq.com", 40, 4)
	fb.Seed("workday.com", 35, 8)
	fb.Seed("icims.com", 30, 6)
	fb.Seed("smartrecruiters.com", 30, 6)
	fb.Seed("jobvite.com", 25, 5)
	fb.Seed("taleo.net", 20, 8)
	fb.Seed("bamboohr.com", 25, 4)
	fb.Seed("successfactors.com", 20, 7)

	defer func() {
		if err := fb.Save(feedbackPath); err != nil {
			log.Error("failed to save feedback store", zap.Error(err))
		} else {
			log.Info("saved domain feedback store", zap.String("path", feedbackPath))
		}
	}()

	f := frontier.New()
	gov := frontier.NewHostGovernor()

	// Thread-safe map for metadata lookups when popping from Frontier
	var lookup sync.Map

	// ── Output Producer (Optional) ──
	var producer *queue.Producer
	if outputTopic != "" {
		p, err := queue.NewProducer(ctx, redpandaBrokers, log)
		if err != nil {
			log.Fatal("failed to initialize Redpanda output producer", zap.Error(err))
		}
		producer = p
		defer producer.Close()
		log.Info("publishing discoveries to Redpanda", zap.String("topic", outputTopic))
	}

	// ── Input Population ──
	if inputMode == "postgres" {
		companies, err := loadCompanies(ctx, pool, batchSize)
		if err != nil {
			log.Fatal("load companies failed", zap.Error(err))
		}
		log.Info("Loaded companies from postgres", zap.Int("count", len(companies)))

		if len(companies) == 0 {
			log.Info("No companies pending discovery — all done!")
			return
		}

		for _, co := range companies {
			careerURL := getCanonicalURL(co.Domain)
			static := frontier.ScoreStatic(careerURL)
			boost := fb.ScoreBoost(co.Domain)
			score := static + boost
			if score > 1.0 {
				score = 1.0
			}

			lookup.Store(careerURL, co)
			f.Push(careerURL, score)
		}
		log.Info("Frontier initialized", zap.Int("urls", f.Len()))
		log.Info("Worker config", zap.Int("goroutines", *workers))

	} else if inputMode == "redpanda" {
		log.Info("consuming input from Redpanda", zap.String("topic", ingestionTopic))

		bloom := ingestion.NewBloomDeduper()
		bloomPath := getEnv("BLOOM_STATE_PATH", "./bloom.bin")
		if err := bloom.Load(bloomPath); err != nil {
			log.Warn("failed to load Bloom state, starting fresh", zap.Error(err))
		}
		defer func() {
			if err := bloom.Save(bloomPath); err != nil {
				log.Error("failed to save Bloom state on shutdown", zap.Error(err))
			}
		}()

		consumer, err := queue.NewConsumer(ctx, redpandaBrokers, "discover-input", []string{ingestionTopic}, log)
		if err != nil {
			log.Fatal("consumer init failed", zap.Error(err))
		}
		defer consumer.Close()

		go func() {
			err := consumer.Poll(ctx, func(_, _ string, value []byte) error {
				var msg urlMessage
				if err := json.Unmarshal(value, &msg); err != nil {
					return fmt.Errorf("unmarshal: %w", err)
				}
				if msg.RawURL == "" {
					return nil
				}
				if bloom.Seen(msg.Domain) {
					return nil // Duplicate skip
				}
				bloom.Add(msg.Domain)

				careerURL := msg.RawURL
				static := frontier.ScoreStatic(careerURL)
				boost := fb.ScoreBoost(msg.Domain)
				score := static + boost
				if score > 1.0 {
					score = 1.0
				}

				co := Company{
					ID:     msg.CompanyID,
					Name:   msg.Domain, // fallback name
					Domain: msg.Domain,
				}
				lookup.Store(careerURL, co)
				f.Push(careerURL, score)

				return nil
			})
			if err != nil {
				log.Warn("redpanda consumer exited", zap.Error(err))
			}
		}()
	} else {
		log.Fatal("unknown INPUT_MODE", zap.String("mode", inputMode))
	}

	// ── Browser (singleton rod browser + tab pool) ──
	wp := tier2_v3.NewWorkerPool(ctx, *workers, log, pool, runID)
	defer wp.Close()

	// ── Worker pool ──
	var wg sync.WaitGroup
	var mu sync.Mutex
	discovered, failed := 0, 0

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			emptyRetries := 0
			const maxEmptyRetries = 3
			for {
				if ctx.Err() != nil {
					return
				}

				url, score := f.Pop()
				if url == "" {
					if inputMode == "postgres" {
						emptyRetries++
						if emptyRetries >= maxEmptyRetries {
							return // queue confirmed drained after retries
						}
						time.Sleep(500 * time.Millisecond)
						continue
					}
					// If redpanda mode, sleep and wait for new items rather than exiting.
					time.Sleep(200 * time.Millisecond)
					continue
				}
				emptyRetries = 0 // reset on successful pop

				val, ok := lookup.Load(url)
				if !ok {
					continue
				}
				co := val.(Company)

				if !gov.Allowed(co.Domain) {
					newScore := score - 0.05
					if newScore < 0.10 {
						newScore = 0.10
					}
					f.Push(url, newScore)
					time.Sleep(100 * time.Millisecond) // Don't tight-loop on blocks
					continue
				}

				f.CheckOut() // Track in-flight processing

				log.Info("→ processing",
					zap.String("company", co.Name),
					zap.String("url", url),
					zap.Float64("score", score),
				)
				start := time.Now()

				// Panic recovery: if go-rod panics (Must calls, browser crash, etc.),
				// catch it and continue to the next domain instead of killing the run.
				var result tier2_v3.CDPResult
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Warn("⚠️ PANIC recovered in worker",
								zap.String("domain", co.Domain),
								zap.Any("panic", r),
							)
							result = tier2_v3.CDPResult{
								Domain:    co.Domain,
								RawURL:    url,
								CompanyID: co.ID,
								Error:     fmt.Sprintf("panic: %v", r),
							}
						}
					}()
					result = wp.Process(ctx, url, co.Domain, co.ID)
				}()

				elapsed := time.Since(start).Round(time.Millisecond)
				gov.Record(co.Domain)
				f.CheckIn() // Done processing

				if result.Success {
					fb.RecordHit(co.Domain)
					if result.Confidence >= minConf {
						if err := saveDiscovered(ctx, pool, co, result); err != nil {
							log.Error("save failed", zap.String("domain", co.Domain), zap.Error(err))
						} else {
							mu.Lock()
							discovered++
							mu.Unlock()
							log.Info("✅ DISCOVERED",
								zap.String("company", co.Name),
								zap.String("api", result.APIURL),
								zap.Float64("conf", result.Confidence),
								zap.Duration("elapsed", elapsed),
							)

							// Fan-out to output topic
							if producer != nil {
								payload, _ := json.Marshal(result)
								if err := producer.Produce(context.Background(), outputTopic, co.Domain, payload); err != nil {
									log.Error("failed to emit to output topic", zap.String("domain", co.Domain), zap.Error(err))
								}
							}
						}
					} else {
						// Confidence below threshold, record as failed
						_ = saveFailed(ctx, pool, co, fmt.Sprintf("confidence %.2f below MIN_CONFIDENCE", result.Confidence))
						mu.Lock()
						failed++
						mu.Unlock()
						log.Warn("❌ low confidence",
							zap.String("company", co.Name),
							zap.Float64("conf", result.Confidence),
							zap.Duration("elapsed", elapsed),
						)
					}
				} else {
					fb.RecordMiss(co.Domain)
					_ = saveFailed(ctx, pool, co, result.Error)
					mu.Lock()
					failed++
					mu.Unlock()
					log.Warn("❌ no match",
						zap.String("company", co.Name),
						zap.Duration("elapsed", elapsed),
					)
				}
			}
		}(i)
	}

	// In postgres mode, we wait for the waitgroup (queue empty).
	// In redpanda mode, we wait for context cancellation on SIGTERM/SIGINT.

	var nearMissCount int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM near_misses WHERE run_id = $1`, runID).Scan(&nearMissCount); err != nil {
		log.Warn("Failed to count near-misses", zap.Error(err))
	}

	if inputMode == "postgres" {
		wg.Wait()
		log.Info("Done",
			zap.Int("discovered", discovered),
			zap.Int("failed", failed),
			zap.Int("near_misses", nearMissCount),
		)
	} else {
		<-ctx.Done()
		log.Info("Shutting down workers...")
		wg.Wait()
		log.Info("Shutdown complete",
			zap.Int("discovered", discovered),
			zap.Int("failed", failed),
			zap.Int("near_misses", nearMissCount),
		)
	}
}
