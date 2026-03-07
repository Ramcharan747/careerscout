// cmd/tier2 is the entry point for Team 3 — Tier 2 CDP Interception Worker.
//
// This service:
//  1. Consumes URLs from `urls.tier2_queue`
//  2. Spawns Chromium instances via chromedp with aggressive resource blocking
//  3. Intercepts the first XHR/Fetch request matching Stage 1 + ML classifier
//  4. Kills Chromium after 800ms regardless of page state
//  5. On success: emits to `apis.discovered` and updates Postgres
//  6. On failure (2 retries): forwards to `urls.tier3_queue`
//
// Environment variables:
//
//	DATABASE_URL       — Postgres DSN (required)
//	REDPANDA_BROKERS   — Comma-separated broker list (default: localhost:19092)
//	TIER2_WORKERS      — Worker goroutines (default: 50)
//	TIER2_POOL_SIZE    — Chromium instances per worker (default: 20)
//	ML_GRPC_ADDR       — ML classifier gRPC address (default: localhost:50051)
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
	"github.com/careerscout/careerscout/internal/tier2"
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

	log.Info("CareerScout Tier2 CDP Worker starting")

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

	consumer, err := queue.NewConsumer(ctx, brokers, "tier2-workers", []string{queue.TopicTier2Queue}, log)
	if err != nil {
		log.Fatal("consumer init failed", zap.Error(err))
	}
	defer consumer.Close()

	// ── Worker pool ───────────────────────────────────────────────────────────
	workers := getEnvInt("TIER2_WORKERS", 50)
	pool := tier2.NewWorkerPool(workers, log)
	sem := make(chan struct{}, workers)

	log.Info("tier2 CDP worker ready",
		zap.Int("workers", workers),
		zap.String("topic", queue.TopicTier2Queue),
	)

	for {
		if ctx.Err() != nil {
			break
		}

		pollCtx, pollCancel := context.WithTimeout(ctx, 5*time.Second)
		err := consumer.Poll(pollCtx, func(_, _ string, value []byte) error {
			var msg urlMessage
			if err := json.Unmarshal(value, &msg); err != nil {
				return fmt.Errorf("unmarshal: %w", err)
			}

			sem <- struct{}{}
			go func() {
				defer func() { <-sem }()

				workCtx, workCancel := context.WithTimeout(ctx, 30*time.Second)
				defer workCancel()

				result := pool.Process(workCtx, msg.RawURL, msg.Domain, msg.CompanyID)

				if err := handleResult(workCtx, result, dbClient, producer, log); err != nil {
					log.Error("handle result failed",
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

	// Drain in-flight
	for i := 0; i < workers; i++ {
		sem <- struct{}{}
	}
	log.Info("tier2 worker shut down cleanly")
}

func handleResult(ctx context.Context, result tier2.CDPResult, dbClient *db.Client, producer *queue.Producer, log *zap.Logger) error {
	if result.Success {
		if err := dbClient.MarkDiscovered(ctx, result.Domain, db.TierTwo, result.APIURL, result.HTTPMethod, result.Body, result.Confidence); err != nil {
			return fmt.Errorf("mark discovered: %w", err)
		}

		payload := map[string]interface{}{
			"domain":        result.Domain,
			"company_id":    result.CompanyID,
			"api_url":       result.APIURL,
			"method":        result.HTTPMethod,
			"headers":       result.Headers,
			"body":          result.Body,
			"tier_used":     "tier2",
			"confidence":    result.Confidence,
			"discovered_at": time.Now(),
		}
		b, _ := json.Marshal(payload)
		return producer.Produce(ctx, queue.TopicAPIsDiscovered, result.Domain, b)
	}

	// Failure — forward to Tier 3
	if err := dbClient.MarkFailed(ctx, result.Domain, result.Error); err != nil {
		log.Warn("mark failed in db", zap.String("domain", result.Domain), zap.Error(err))
	}

	msg := map[string]interface{}{
		"domain":            result.Domain,
		"raw_url":           result.RawURL,
		"company_id":        result.CompanyID,
		"queued_at":         time.Now(),
		"tier2_fail_reason": result.Error,
	}
	b, _ := json.Marshal(msg)
	return producer.Produce(ctx, queue.TopicTier3Queue, result.Domain, b)
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
