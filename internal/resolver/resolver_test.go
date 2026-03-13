package resolver_test

import (
	"context"
	"testing"
	"time"

	"github.com/careerscout/careerscout/internal/resolver"
)

func TestCachingResolver_CacheHit(t *testing.T) {
	r, err := resolver.NewCachingResolver(2)
	if err != nil {
		t.Fatalf("failed to create resolver: %v", err)
	}

	ctx := context.Background()
	host := "google.com"

	// Initial lookup
	start := time.Now()
	ips1, err := r.LookupHost(ctx, host)
	if err != nil {
		t.Fatalf("LookupHost failed: %v", err)
	}
	duration1 := time.Since(start)

	if len(ips1) == 0 {
		t.Fatalf("expected at least one IP for %s", host)
	}

	// Second lookup (cache hit)
	start = time.Now()
	ips2, err := r.LookupHost(ctx, host)
	if err != nil {
		t.Fatalf("second LookupHost failed: %v", err)
	}
	duration2 := time.Since(start)

	if len(ips1) != len(ips2) {
		t.Fatalf("cache mismatch: got %d IPs, expected %d", len(ips2), len(ips1))
	}
	for i := range ips1 {
		if ips1[i] != ips2[i] {
			t.Fatalf("IP mismatch at index %d: %s != %s", i, ips2[i], ips1[i])
		}
	}

	// The second lookup should be practically instantaneous
	// We allow some jitter since measuring short periods can be noisy, but it should be orders of magnitude faster
	t.Logf("Lookup 1: %v, Lookup 2: %v", duration1, duration2)
	if duration2 > 5*time.Millisecond {
		t.Errorf("expected second lookup to be a fast cache hit, took %v", duration2)
	}
}

// TestCachingResolver_TTLExpiry verifies the cache invalidation behavior
func TestCachingResolver_TTLExpiry(t *testing.T) {
	r, err := resolver.NewCachingResolver(2)
	if err != nil {
		t.Fatalf("failed to create resolver: %v", err)
	}
	ctx := context.Background()
	host := "google.com"

	// Initial lookup caches the entry with expires = now + [60,300]
	_, err = r.LookupHost(ctx, host)
	if err != nil {
		t.Fatalf("LookupHost failed: %v", err)
	}

	// Move time forward slightly, shouldn't expire
	resolver.SetTimeNow(func() time.Time { return time.Now().Add(30 * time.Second) })

	start := time.Now()
	_, _ = r.LookupHost(ctx, host)
	if time.Since(start) > 10*time.Millisecond {
		t.Errorf("expected cache hit before expiry")
	}

	// Move time forward past the max 300s TTL
	resolver.SetTimeNow(func() time.Time { return time.Now().Add(301 * time.Second) })

	// The next lookup should be a cache miss (slower, actually goes to network)
	start = time.Now()
	_, err = r.LookupHost(ctx, host)
	if err != nil {
		t.Fatalf("LookupHost post-expiry failed: %v", err)
	}

	// Reset timeNow to avoid side effects
	resolver.SetTimeNow(time.Now)
}
