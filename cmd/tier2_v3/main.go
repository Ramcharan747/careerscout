//go:build ignore

// Retired: superseded by cmd/discover. Remove after 30-day production validation.
// cmd/tier2_v3 is the entry point for the Tier 2 browser-interception worker.
// Uses go-rod (replacing chromedp) with a singleton browser + tab pool.
//
// Scale-up guide: to increase throughput on a larger machine, set:
// WORKER_COUNT=3000 BROWSER_TABS=80 DNS_CONCURRENCY=50 DB_POOL_SIZE=50
// No code changes required.
//
// Environment variables:
//
//	DATABASE_URL          — Postgres DSN (required)
//	REDPANDA_BROKERS      — Comma-separated broker list (default: localhost:19092)
//	WORKER_COUNT          — Concurrent browser goroutines (default: 150)
//	BROWSER_TABS          — Rod tab pool size (default: 8)
//	DB_POOL_SIZE          — Postgres max connections (default: 20)
//	TIER2_CONSUMER_GROUP  — Kafka consumer group (default: tier2-v4-workers)
//	TIER2_METRICS_PORT    — Prometheus metrics port (default: 2112)
//	LOG_LEVEL             — debug|info|warn|error (default: info)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/careerscout/careerscout/internal/db"
	"github.com/careerscout/careerscout/internal/queue"
	"github.com/careerscout/careerscout/internal/tier2_v3"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	metricProcessed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tier2_urls_processed_total",
		Help: "Total URLs processed by Tier 2 worker",
	}, []string{"status"})

	metricDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tier2_process_duration_seconds",
		Help:    "Time taken to process each URL",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 45},
	})
)

type urlMessage struct {
	Domain    string `json:"domain"`
	RawURL    string `json:"raw_url"`
	CompanyID string `json:"company_id"`
}

func main() {
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	logger, _ := config.Build()
	defer logger.Sync()

	logger.Info("CareerScout Tier2 (go-rod) starting")

	// ── Concurrency config — all env-driven, validated at startup ─────────────
	workerCount := mustEnvInt(logger, "WORKER_COUNT", 150)
	browserTabs := mustEnvInt(logger, "BROWSER_TABS", 8)
	dbPoolSize := mustEnvInt(logger, "DB_POOL_SIZE", 20)
	// DNS_CONCURRENCY is logged here for consistency; actual use is in resolver.
	dnsConcurrency := mustEnvInt(logger, "DNS_CONCURRENCY", 8)

	logger.Info("startup config resolved",
		zap.Int("WORKER_COUNT", workerCount),
		zap.Int("BROWSER_TABS", browserTabs),
		zap.Int("DB_POOL_SIZE", dbPoolSize),
		zap.Int("DNS_CONCURRENCY", dnsConcurrency),
	)

	consumerGroup := getEnv("TIER2_CONSUMER_GROUP", "tier2-v4-workers")
	metricsPort := getEnv("TIER2_METRICS_PORT", "2112")

	// ── Signal handling ───────────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Metrics server ────────────────────────────────────────────────────────
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		logger.Info("Starting Prometheus metrics server", zap.String("port", metricsPort))
		if err := http.ListenAndServe(":"+metricsPort, mux); err != nil {
			logger.Error("prometheus server failed", zap.Error(err))
		}
	}()

	// ── Database ──────────────────────────────────────────────────────────────
	dbClient, err := db.NewWithPoolSize(ctx, mustEnvStr(logger, "DATABASE_URL"), dbPoolSize)
	if err != nil {
		logger.Fatal("database connect failed", zap.Error(err))
	}
	defer dbClient.Close()

	// ── Queue ─────────────────────────────────────────────────────────────────
	redpandaBrokers := strings.Split(getEnv("REDPANDA_BROKERS", "localhost:19092"), ",")

	producer, err := queue.NewProducer(ctx, redpandaBrokers, logger)
	if err != nil {
		logger.Fatal("producer init failed", zap.Error(err))
	}
	defer producer.Close()

	consumer, err := queue.NewConsumer(ctx, redpandaBrokers, consumerGroup, []string{queue.TopicTier2Queue}, logger)
	if err != nil {
		logger.Fatal("consumer init failed", zap.Error(err))
	}
	defer consumer.Close()

	// ── Worker Pool (singleton go-rod browser + tab pool) ─────────────────────
	// BROWSER_TABS is read inside NewWorkerPool from the env var directly.
	// workerCount is not passed — tab pool size drives browser concurrency.
	_ = browserTabs // already consumed by NewWorkerPool via env
	wp := tier2_v3.NewWorkerPool(ctx, 0, logger)
	defer wp.Close()
	defer func() {
		_ = exec.Command("pkill", "-f", "chromium").Run()
	}()

	// Semaphore: caps goroutine count to workerCount.
	// Tab pool in rod provides additional backpressure via blocking send.
	sem := make(chan struct{}, workerCount)

	logger.Info("tier2 go-rod worker ready",
		zap.Int("workers", workerCount),
		zap.String("group", consumerGroup),
		zap.String("topic", queue.TopicTier2Queue),
	)

	// ── Main consume loop ─────────────────────────────────────────────────────
	for {
		if ctx.Err() != nil {
			break
		}

		err := consumer.Poll(ctx, func(_, _ string, value []byte) error {
			var msg urlMessage
			if err := json.Unmarshal(value, &msg); err != nil {
				return fmt.Errorf("unmarshal: %w", err)
			}

			if msg.RawURL == "" {
				logger.Warn("received message with empty RawURL, skipping", zap.String("domain", msg.Domain))
				return nil
			}

			sem <- struct{}{}

			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				defer func() { <-sem }()

				start := time.Now()
				logger.Info("tier2: processing URL",
					zap.String("domain", msg.Domain),
					zap.String("url", msg.RawURL),
				)

				workCtx, workCancel := context.WithTimeout(ctx, 45*time.Second)
				defer workCancel()

				result := wp.Process(workCtx, msg.RawURL, msg.Domain, msg.CompanyID)
				elapsed := time.Since(start)
				metricDuration.Observe(elapsed.Seconds())

				if result.Success {
					payload, _ := json.Marshal(result)

					if err := producer.Produce(context.Background(), queue.TopicAPIsDiscovered, msg.Domain, payload); err != nil {
						logger.Error("failed to emit discovered API", zap.String("domain", msg.Domain), zap.Error(err))
					}

					if err := dbClient.MarkDiscovered(
						context.Background(), msg.CompanyID, msg.Domain,
						db.TierTwo, result.APIURL, result.HTTPMethod, result.Body, result.Confidence,
					); err != nil {
						logger.Error("failed to mark discovered in DB", zap.String("domain", msg.Domain), zap.Error(err))
					}

					metricProcessed.WithLabelValues("success").Inc()
					logger.Info("tier2: ✅ discovered",
						zap.String("domain", msg.Domain),
						zap.String("api_url", result.APIURL),
						zap.Duration("elapsed", elapsed),
					)
				} else {
					logger.Warn("tier2: ❌ failed",
						zap.String("domain", msg.Domain),
						zap.String("error", result.Error),
						zap.Duration("elapsed", elapsed),
					)

					if err := producer.Produce(context.Background(), queue.TopicTier3Queue, msg.Domain, value); err != nil {
						logger.Error("failed to escalate to tier3", zap.String("domain", msg.Domain), zap.Error(err))
					}

					if err := dbClient.MarkFailed(context.Background(), msg.CompanyID, msg.Domain, result.Error); err != nil {
						logger.Error("failed to mark failed in DB", zap.String("domain", msg.Domain), zap.Error(err))
					}

					metricProcessed.WithLabelValues("failed").Inc()
				}
			}()

			wg.Wait()
			return nil
		})

		if err != nil && ctx.Err() == nil {
			logger.Warn("poll error — retrying in 1s", zap.Error(err))
			time.Sleep(1 * time.Second)
		}
	}

	logger.Info("tier2 shutting down gracefully...")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnvStr(log *zap.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatal("required env var missing", zap.String("key", key))
	}
	return v
}

// mustEnvInt reads an int env var. If set, validates it is a positive integer.
// Zero, negative, or non-integer values cause immediate process exit.
func mustEnvInt(log *zap.Logger, key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatal("env var must be an integer", zap.String("key", key), zap.String("value", v))
	}
	if n <= 0 {
		log.Fatal("env var must be a positive integer", zap.String("key", key), zap.Int("value", n))
	}
	return n
}
