// cmd/ingestion is the entry point for Team 1 — URL Ingestion & Tier Routing.
//
// This service:
//  1. Consumes URLs from the `urls.to_process` Redpanda topic
//  2. Checks Postgres for existing discovery records
//  3. Applies per-domain rate limiting (4-hour cooldown)
//  4. Routes each URL to the appropriate tier queue
//
// Environment variables:
//
//	DATABASE_URL       — Postgres DSN (required)
//	REDPANDA_BROKERS   — Comma-separated broker list (default: localhost:19092)
//	INGESTION_WORKERS  — Number of parallel routing goroutines (default: 50)
//	LOG_LEVEL          — Zap log level: debug|info|warn|error (default: info)
package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/careerscout/careerscout/internal/db"
	"github.com/careerscout/careerscout/internal/ingestion"
	"github.com/careerscout/careerscout/internal/queue"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	log := newLogger(getEnv("LOG_LEVEL", "info"))
	defer log.Sync() //nolint:errcheck

	log.Info("CareerScout Ingestion Service starting")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// ── Database ──────────────────────────────────────────────────────────────
	dsn := mustEnv(log, "DATABASE_URL")
	dbClient, err := db.New(ctx, dsn)
	if err != nil {
		log.Fatal("failed to connect to database", zap.Error(err))
	}
	defer dbClient.Close()
	log.Info("connected to Postgres")

	// ── Redpanda ──────────────────────────────────────────────────────────────
	brokers := strings.Split(getEnv("REDPANDA_BROKERS", "localhost:19092"), ",")

	producer, err := queue.NewProducer(ctx, brokers, log)
	if err != nil {
		log.Fatal("failed to create Redpanda producer", zap.Error(err))
	}
	defer producer.Close()

	consumer, err := queue.NewConsumer(
		ctx, brokers,
		"ingestion-service",
		[]string{queue.TopicURLsToProcess},
		log,
	)
	if err != nil {
		log.Fatal("failed to create Redpanda consumer", zap.Error(err))
	}
	defer consumer.Close()
	log.Info("connected to Redpanda", zap.Strings("brokers", brokers))

	// ── Router setup ──────────────────────────────────────────────────────────
	rl := ingestion.NewRateLimiter()
	router := ingestion.NewRouter(dbClient, producer, rl, log)

	workers := getEnvInt("INGESTION_WORKERS", 50)
	sem := make(chan struct{}, workers) // semaphore to cap concurrency

	log.Info("ingestion service ready",
		zap.Int("workers", workers),
		zap.String("topic", queue.TopicURLsToProcess),
	)

	// ── Main consume loop ─────────────────────────────────────────────────────
	for {
		if ctx.Err() != nil {
			log.Info("context cancelled, draining and shutting down")
			break
		}

		pollCtx, pollCancel := context.WithTimeout(ctx, 5*time.Second)
		err := consumer.Poll(pollCtx, func(topic, key string, value []byte) error {
			rawURL := string(value)
			if rawURL == "" {
				return nil
			}

			sem <- struct{}{}
			go func() {
				defer func() { <-sem }()

				routeCtx, routeCancel := context.WithTimeout(ctx, 10*time.Second)
				defer routeCancel()

				if err := router.Route(routeCtx, rawURL); err != nil {
					log.Error("routing failed",
						zap.String("url", rawURL),
						zap.Error(err),
					)
				}
			}()
			return nil
		})
		pollCancel()

		if err != nil && ctx.Err() == nil {
			log.Warn("poll error, retrying in 1s", zap.Error(err))
			time.Sleep(1 * time.Second)
		}
	}

	// Drain in-flight goroutines
	for i := 0; i < workers; i++ {
		sem <- struct{}{}
	}
	log.Info("ingestion service shut down cleanly")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

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
		log.Fatal("required environment variable not set", zap.String("key", key))
	}
	return v
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
