// Package tier1 — emitter.go
// Handles emitting Tier 1 results to the appropriate downstream topic.
package tier1

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/careerscout/careerscout/internal/db"
	"github.com/careerscout/careerscout/internal/queue"
	"go.uber.org/zap"
)

// DiscoveredPayload is written to the apis.discovered Redpanda topic.
type DiscoveredPayload struct {
	Domain       string            `json:"domain"`
	CompanyID    string            `json:"company_id"`
	APIURL       string            `json:"api_url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         string            `json:"body,omitempty"`
	TierUsed     string            `json:"tier_used"`
	Confidence   float64           `json:"confidence"`
	DiscoveredAt time.Time         `json:"discovered_at"`
}

// FailedPayload is written to the apis.failed topic for human review.
type FailedPayload struct {
	Domain    string    `json:"domain"`
	CompanyID string    `json:"company_id"`
	TierUsed  string    `json:"tier_used"`
	Error     string    `json:"error"`
	FailedAt  time.Time `json:"failed_at"`
}

// Emitter routes Tier 1 results to Redpanda and updates Postgres accordingly.
type Emitter struct {
	db       *db.Client
	producer *queue.Producer
	log      *zap.Logger
}

// NewEmitter creates a new Tier 1 result emitter.
func NewEmitter(dbClient *db.Client, producer *queue.Producer, log *zap.Logger) *Emitter {
	return &Emitter{db: dbClient, producer: producer, log: log}
}

// Emit processes a worker result: on success writes to apis.discovered + updates Postgres;
// on failure writes to urls.tier2_queue and increments failure count.
func (e *Emitter) Emit(ctx context.Context, result Result) error {
	if result.Success {
		return e.emitSuccess(ctx, result)
	}
	return e.emitFailure(ctx, result)
}

func (e *Emitter) emitSuccess(ctx context.Context, r Result) error {
	// Update Postgres first — fail fast if DB is down
	if err := e.db.MarkDiscovered(ctx, r.Domain, db.TierOne, r.APIURL, r.HTTPMethod, "", 0.95); err != nil {
		return fmt.Errorf("emitter: mark discovered in db: %w", err)
	}

	payload := DiscoveredPayload{
		Domain:       r.Domain,
		CompanyID:    r.CompanyID,
		APIURL:       r.APIURL,
		Method:       r.HTTPMethod,
		TierUsed:     "tier1",
		Confidence:   0.95,
		DiscoveredAt: time.Now(),
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("emitter: marshal discovered payload: %w", err)
	}

	if err := e.producer.Produce(ctx, queue.TopicAPIsDiscovered, r.Domain, b); err != nil {
		return fmt.Errorf("emitter: produce to apis.discovered: %w", err)
	}

	e.log.Info("tier1: emitted discovered API",
		zap.String("domain", r.Domain),
		zap.String("api_url", r.APIURL),
		zap.String("pattern", r.Pattern),
	)
	return nil
}

func (e *Emitter) emitFailure(ctx context.Context, r Result) error {
	// Increment failure count in Postgres
	if err := e.db.MarkFailed(ctx, r.Domain, r.Error); err != nil {
		return fmt.Errorf("emitter: mark failed in db: %w", err)
	}

	// Forward to Tier 2 CDP interception
	msg := struct {
		Domain    string    `json:"domain"`
		RawURL    string    `json:"raw_url"`
		CompanyID string    `json:"company_id"`
		QueuedAt  time.Time `json:"queued_at"`
		Reason    string    `json:"tier1_fail_reason"`
	}{
		Domain:    r.Domain,
		RawURL:    r.RawURL,
		CompanyID: r.CompanyID,
		QueuedAt:  time.Now(),
		Reason:    r.Error,
	}

	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("emitter: marshal tier2 message: %w", err)
	}

	if err := e.producer.Produce(ctx, queue.TopicTier2Queue, r.Domain, b); err != nil {
		return fmt.Errorf("emitter: produce to tier2 queue: %w", err)
	}

	e.log.Debug("tier1: forwarded to tier2",
		zap.String("domain", r.Domain),
		zap.String("reason", r.Error),
	)
	return nil
}
