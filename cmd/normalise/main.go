// cmd/normalise is the entry point for Team 7 — Data Normalisation & Storage.
//
// Environment variables:
//
//	DATABASE_URL      — Postgres DSN (required)
//	REDPANDA_BROKERS  — Comma-separated broker list (default: localhost:19092)
//	LOG_LEVEL         — debug|info|warn|error (default: info)
package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/careerscout/careerscout/internal/db"
	"github.com/careerscout/careerscout/internal/normalise"
	"github.com/careerscout/careerscout/internal/queue"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	log := newLogger(getEnv("LOG_LEVEL", "info"))
	defer log.Sync() //nolint:errcheck

	log.Info("CareerScout Normalisation Service starting")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	dbClient, err := db.New(ctx, mustEnv(log, "DATABASE_URL"))
	if err != nil {
		log.Fatal("database connect failed", zap.Error(err))
	}
	defer dbClient.Close()

	brokers := strings.Split(getEnv("REDPANDA_BROKERS", "localhost:19092"), ",")

	consumer, err := queue.NewConsumer(ctx, brokers, "normalise-service", []string{queue.TopicJobsRaw}, log)
	if err != nil {
		log.Fatal("consumer init failed", zap.Error(err))
	}
	defer consumer.Close()

	n := normalise.NewNormaliser()
	w := normalise.NewWriter(dbClient, log)
	svc := normalise.NewConsumer(consumer, n, w, log)

	log.Info("normalisation service ready", zap.String("topic", queue.TopicJobsRaw))

	if err := svc.Run(ctx); err != nil {
		log.Error("service exited with error", zap.Error(err))
	}

	log.Info("normalisation service shut down cleanly")
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
