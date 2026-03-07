// Package ingestion implements the Tier Routing logic for the URL Ingestion service.
// It decides which Redpanda topic a URL should be emitted to based on its
// existing discovery record in Postgres.
package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/careerscout/careerscout/internal/db"
	"github.com/careerscout/careerscout/internal/queue"
	"go.uber.org/zap"
)

// URLMessage is the payload emitted to tier queues.
type URLMessage struct {
	Domain    string    `json:"domain"`
	RawURL    string    `json:"raw_url"`
	CompanyID string    `json:"company_id"`
	QueuedAt  time.Time `json:"queued_at"`
}

// Router makes tier routing decisions and emits URLs to the appropriate queue.
type Router struct {
	db          *db.Client
	producer    *queue.Producer
	rateLimiter *RateLimiter
	log         *zap.Logger
}

// NewRouter creates a new Router.
func NewRouter(dbClient *db.Client, producer *queue.Producer, rl *RateLimiter, log *zap.Logger) *Router {
	return &Router{
		db:          dbClient,
		producer:    producer,
		rateLimiter: rl,
		log:         log,
	}
}

// Route processes a single raw URL string and routes it to the appropriate tier.
// Decision logic:
//  1. Check per-domain rate limiter — if too recent, skip silently.
//  2. Check Postgres for existing discovery record:
//     - If status = "discovered" → emit to apis.discovered replay path.
//     - If status = "pending" or "failed" → emit to tier1_queue for fresh discovery.
//     - If no record exists → create company + pending record, emit to tier1_queue.
func (r *Router) Route(ctx context.Context, rawURL string) error {
	domain, err := extractDomain(rawURL)
	if err != nil {
		return fmt.Errorf("router: extract domain from %q: %w", rawURL, err)
	}

	// Rate limit check
	if !r.rateLimiter.Allow(domain) {
		r.log.Debug("rate limited, skipping", zap.String("domain", domain))
		return nil
	}

	// Fetch existing record
	record, err := r.db.GetDiscoveryRecord(ctx, domain)
	if err != nil {
		return fmt.Errorf("router: get discovery record: %w", err)
	}

	// Case 1: Already discovered — send to replay pipeline directly
	if record != nil && record.Status == db.StatusDiscovered {
		r.log.Info("domain already discovered, routing to replay",
			zap.String("domain", domain),
			zap.String("tier", string(record.TierUsed)),
		)
		msg := URLMessage{
			Domain:    domain,
			RawURL:    rawURL,
			CompanyID: record.CompanyID,
			QueuedAt:  time.Now(),
		}
		return r.emit(ctx, queue.TopicAPIsDiscovered, domain, msg)
	}

	// Case 2: New domain — ensure company + discovery record exist
	companyID, err := r.db.UpsertCompany(ctx, domain)
	if err != nil {
		return fmt.Errorf("router: upsert company: %w", err)
	}

	if err := r.db.CreatePendingDiscovery(ctx, domain, companyID); err != nil {
		return fmt.Errorf("router: create pending discovery: %w", err)
	}

	// All new/failed domains start at Tier 1
	r.log.Info("routing to tier1",
		zap.String("domain", domain),
		zap.String("company_id", companyID),
	)

	msg := URLMessage{
		Domain:    domain,
		RawURL:    rawURL,
		CompanyID: companyID,
		QueuedAt:  time.Now(),
	}
	return r.emit(ctx, queue.TopicTier1Queue, domain, msg)
}

// emit serialises the message and publishes it to the topic.
func (r *Router) emit(ctx context.Context, topic, key string, msg URLMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("router: marshal message: %w", err)
	}
	if err := r.producer.Produce(ctx, topic, key, b); err != nil {
		return fmt.Errorf("router: produce to %q: %w", topic, err)
	}
	return nil
}

// extractDomain parses a raw URL and returns its hostname.
func extractDomain(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("empty URL")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("no hostname in %q", rawURL)
	}
	return host, nil
}
