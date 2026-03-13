// Package ingestion provides per-domain rate limiting for the URL Ingestion service.
// No domain may be requested more than once per 4 hours during discovery.
package ingestion

import (
	"sync"
	"time"
)

const domainCooldown = 4 * time.Hour

// RateLimiter enforces a per-domain cooldown window.
type RateLimiter struct {
	mu       sync.RWMutex
	lastSeen map[string]time.Time
}

// NewRateLimiter creates a new in-memory rate limiter.
func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		lastSeen: make(map[string]time.Time),
	}
	// Start background cleanup goroutine — prevents unbounded memory growth
	// for long-running processes by removing entries older than 2x the cooldown.
	go rl.cleanup()
	return rl
}

// Allow returns true if the domain may be processed right now.
// If allowed, it updates the last-seen timestamp atomically.
func (rl *RateLimiter) Allow(domain string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	last, exists := rl.lastSeen[domain]
	if !exists || time.Since(last) >= domainCooldown {
		rl.lastSeen[domain] = time.Now()
		return true
	}
	return false
}

// Reset removes the rate limit entry for a domain, allowing it to be processed
// immediately. Used when a domain needs forced re-discovery (e.g., auth refresh).
func (r *RateLimiter) Reset(domain string) {
	r.mu.Lock()
	delete(r.lastSeen, domain)
	r.mu.Unlock()
}

// Size returns the number of domains currently tracked.
func (r *RateLimiter) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.lastSeen)
}

// cleanup removes stale entries every hour to prevent unbounded growth.
func (r *RateLimiter) cleanup() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		cutoff := time.Now().Add(-2 * domainCooldown)
		r.mu.Lock()
		for domain, t := range r.lastSeen {
			if t.Before(cutoff) {
				delete(r.lastSeen, domain)
			}
		}
		r.mu.Unlock()
	}
}
