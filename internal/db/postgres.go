// Package db provides PostgreSQL client operations for CareerScout.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DiscoveryStatus represents the current state of a domain's discovery.
type DiscoveryStatus string

const (
	StatusPending    DiscoveryStatus = "pending"
	StatusDiscovered DiscoveryStatus = "discovered"
	StatusFailed     DiscoveryStatus = "failed"
	StatusStale      DiscoveryStatus = "stale"
)

// DiscoveryTier represents which tier discovered the API.
type DiscoveryTier string

const (
	TierOne   DiscoveryTier = "tier1"
	TierTwo   DiscoveryTier = "tier2"
	TierThree DiscoveryTier = "tier3"
)

// DiscoveryRecord holds the captured API details for a company domain.
type DiscoveryRecord struct {
	ID             string
	CompanyID      string
	Domain         string
	APIURL         string
	HTTPMethod     string
	RequestHeaders map[string]string
	RequestBody    string
	TierUsed       DiscoveryTier
	Status         DiscoveryStatus
	Confidence     float64
	DiscoveredAt   *time.Time
	LastReplayed   *time.Time
	NextReplay     *time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Client wraps a pgxpool connection pool with CareerScout-specific operations.
type Client struct {
	pool *pgxpool.Pool
}

// New creates a new database client using the provided connection string.
func New(ctx context.Context, dsn string) (*Client, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parse config: %w", err)
	}

	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db: ping: %w", err)
	}

	return &Client{pool: pool}, nil
}

// Close shuts down the connection pool.
func (c *Client) Close() {
	c.pool.Close()
}

// GetDiscoveryRecord fetches an existing discovery record for the given domain.
// Returns nil, nil if no record exists.
func (c *Client) GetDiscoveryRecord(ctx context.Context, domain string) (*DiscoveryRecord, error) {
	const q = `
		SELECT
			dr.id, dr.company_id, dr.domain, COALESCE(dr.api_url, ''),
			COALESCE(dr.http_method, ''), COALESCE(dr.request_body, ''),
			dr.tier_used, dr.status, COALESCE(dr.confidence, 0.0),
			dr.discovered_at, dr.last_replayed, dr.next_replay,
			COALESCE(dr.last_error, ''), dr.created_at, dr.updated_at
		FROM discovery_records dr
		WHERE dr.domain = $1
		LIMIT 1
	`

	row := c.pool.QueryRow(ctx, q, domain)

	var r DiscoveryRecord
	var tier, status string

	err := row.Scan(
		&r.ID, &r.CompanyID, &r.Domain, &r.APIURL,
		&r.HTTPMethod, &r.RequestBody,
		&tier, &status, &r.Confidence,
		&r.DiscoveredAt, &r.LastReplayed, &r.NextReplay,
		&r.LastError, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("db: get discovery record for %q: %w", domain, err)
	}

	r.TierUsed = DiscoveryTier(tier)
	r.Status = DiscoveryStatus(status)

	return &r, nil
}

// UpsertCompany ensures a company record exists for the domain and returns its ID.
func (c *Client) UpsertCompany(ctx context.Context, domain string) (string, error) {
	const q = `
		INSERT INTO companies (domain) VALUES ($1)
		ON CONFLICT (domain) DO UPDATE SET domain = EXCLUDED.domain
		RETURNING id
	`
	var id string
	if err := c.pool.QueryRow(ctx, q, domain).Scan(&id); err != nil {
		return "", fmt.Errorf("db: upsert company %q: %w", domain, err)
	}
	return id, nil
}

// CreatePendingDiscovery inserts a new pending discovery record for a domain.
// Idempotent — does nothing if a record already exists.
func (c *Client) CreatePendingDiscovery(ctx context.Context, domain, companyID string) error {
	const q = `
		INSERT INTO discovery_records (company_id, domain, status)
		VALUES ($1, $2, 'pending')
		ON CONFLICT (domain) DO NOTHING
	`
	if _, err := c.pool.Exec(ctx, q, companyID, domain); err != nil {
		return fmt.Errorf("db: create pending discovery %q: %w", domain, err)
	}
	return nil
}

// MarkDiscovered updates a record with the captured API details.
func (c *Client) MarkDiscovered(ctx context.Context, domain string, tier DiscoveryTier, apiURL, method, body string, confidence float64) error {
	const q = `
		UPDATE discovery_records
		SET
			status        = 'discovered',
			tier_used     = $2,
			api_url       = $3,
			http_method   = $4,
			request_body  = $5,
			confidence    = $6,
			discovered_at = NOW(),
			next_replay   = NOW() + INTERVAL '1 hour',
			consecutive_failures = 0,
			last_error    = NULL
		WHERE domain = $1
	`
	if _, err := c.pool.Exec(ctx, q, domain, tier, apiURL, method, body, confidence); err != nil {
		return fmt.Errorf("db: mark discovered %q: %w", domain, err)
	}
	return nil
}

// MarkFailed increments the failure count and records the error for a domain.
func (c *Client) MarkFailed(ctx context.Context, domain, errMsg string) error {
	const q = `
		UPDATE discovery_records
		SET
			consecutive_failures = consecutive_failures + 1,
			last_error = $2,
			status = CASE
				WHEN consecutive_failures + 1 >= 3 THEN 'failed'::discovery_status
				ELSE status
			END
		WHERE domain = $1
	`
	if _, err := c.pool.Exec(ctx, q, domain, errMsg); err != nil {
		return fmt.Errorf("db: mark failed %q: %w", domain, err)
	}
	return nil
}
