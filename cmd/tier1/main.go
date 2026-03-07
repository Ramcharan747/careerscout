// cmd/tier1 is the entry point for Team 2 — Tier 1 Static Discovery Worker.
//
// This service:
//  1. Consumes URLs from the `urls.tier1_queue` Redpanda topic
//  2. Issues direct HTTP GET requests with Chrome-mimicking TLS fingerprints
//  3. Analyzes static HTML/JS for hardcoded job API endpoint patterns
//  4. On success: emits to `apis.discovered` + updates Postgres
//  5. On failure: forwards to `urls.tier2_queue` for CDP interception
//
// Environment variables:
//
//	DATABASE_URL       — Postgres DSN (required)
//	REDPANDA_BROKERS   — Comma-separated broker list (default: localhost:19092)
//	TIER1_WORKERS      — Number of parallel workers (default: 200)
//	LOG_LEVEL          — debug|info|warn|error (default: info)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/careerscout/careerscout/internal/db"
	"github.com/careerscout/careerscout/internal/queue"
	"github.com/careerscout/careerscout/internal/tier1"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type urlMessage struct {
	Domain    string `json:"domain"`
	RawURL    string `json:"raw_url"`
	CompanyID string `json:"company_id"`
}

func main() {
	log := newLogger(getEnv("LOG_LEVEL", "info"))
	defer log.Sync() //nolint:errcheck

	log.Info("CareerScout Tier1 Worker starting")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// ── Database ──────────────────────────────────────────────────────────────
	dbClient, err := db.New(ctx, mustEnv(log, "DATABASE_URL"))
	if err != nil {
		log.Fatal("database connect failed", zap.Error(err))
	}
	defer dbClient.Close()

	// ── Redpanda ──────────────────────────────────────────────────────────────
	brokers := strings.Split(getEnv("REDPANDA_BROKERS", "localhost:19092"), ",")

	producer, err := queue.NewProducer(ctx, brokers, log)
	if err != nil {
		log.Fatal("producer init failed", zap.Error(err))
	}
	defer producer.Close()

	consumer, err := queue.NewConsumer(ctx, brokers, "tier1-workers", []string{queue.TopicTier1Queue}, log)
	if err != nil {
		log.Fatal("consumer init failed", zap.Error(err))
	}
	defer consumer.Close()

	// ── Worker pool ───────────────────────────────────────────────────────────
	workers := getEnvInt("TIER1_WORKERS", 200)
	worker := tier1.NewWorker(log)
	emitter := tier1.NewEmitter(dbClient, producer, log)
	sem := make(chan struct{}, workers)

	log.Info("tier1 worker ready",
		zap.Int("concurrency", workers),
		zap.String("topic", queue.TopicTier1Queue),
	)

	for {
		if ctx.Err() != nil {
			break
		}

		pollCtx, pollCancel := context.WithTimeout(ctx, 5*time.Second)
		err := consumer.Poll(pollCtx, func(_, _ string, value []byte) error {
			var msg urlMessage
			if err := json.Unmarshal(value, &msg); err != nil {
				return fmt.Errorf("unmarshal url message: %w", err)
			}

			sem <- struct{}{}
			go func() {
				defer func() { <-sem }()

				workCtx, workCancel := context.WithTimeout(ctx, 20*time.Second)
				defer workCancel()

				result := worker.Process(workCtx, msg.RawURL, msg.Domain, msg.CompanyID)

				if err := emitter.Emit(workCtx, result); err != nil {
					log.Error("emit failed",
						zap.String("domain", msg.Domain),
						zap.Error(err),
					)
				}
			}()
			return nil
		})
		pollCancel()

		if err != nil && ctx.Err() == nil {
			log.Warn("poll error", zap.Error(err))
			time.Sleep(1 * time.Second)
		}
	}

	// Drain
	for i := 0; i < workers; i++ {
		sem <- struct{}{}
	}
	log.Info("tier1 worker shut down cleanly")
}

func newLogger(level string) *zap.Logger {
	lvl := zapcore.InfoLevel
	_ = lvl.UnmarshalText([]byte(level))
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(lvl)
	log, _ := cfg.Build()
	return log
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(log *zap.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatal("required env var missing", zap.String("key", key))
	}
	return v
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
