package ingestion_test

import (
	"sync"
	"testing"
	"time"

	"github.com/careerscout/careerscout/internal/ingestion"
)

func TestRateLimiter_AllowsFirstRequest(t *testing.T) {
	rl := ingestion.NewRateLimiter()
	if !rl.Allow("example.com") {
		t.Fatal("first request to a new domain should be allowed")
	}
}

func TestRateLimiter_BlocksSecondRequest(t *testing.T) {
	rl := ingestion.NewRateLimiter()
	rl.Allow("example.com") // first — allowed
	if rl.Allow("example.com") {
		t.Fatal("second immediate request should be blocked by rate limiter")
	}
}

func TestRateLimiter_MultipleDomainsIndependent(t *testing.T) {
	rl := ingestion.NewRateLimiter()

	domains := []string{"a.com", "b.com", "c.com"}
	for _, d := range domains {
		if !rl.Allow(d) {
			t.Fatalf("first request to %q should be allowed", d)
		}
	}
	// All should now be blocked
	for _, d := range domains {
		if rl.Allow(d) {
			t.Fatalf("second request to %q should be blocked", d)
		}
	}
}

func TestRateLimiter_ResetAllowsImmediate(t *testing.T) {
	rl := ingestion.NewRateLimiter()
	rl.Allow("example.com")
	rl.Reset("example.com")
	if !rl.Allow("example.com") {
		t.Fatal("after Reset, domain should be immediately allowed")
	}
}

func TestRateLimiter_ConcurrentSafety(t *testing.T) {
	rl := ingestion.NewRateLimiter()
	const goroutines = 100
	allowed := 0
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if rl.Allow("concurrent.com") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != 1 {
		t.Fatalf("expected exactly 1 goroutine to be allowed, got %d", allowed)
	}
}

func TestRateLimiter_SizeTracking(t *testing.T) {
	rl := ingestion.NewRateLimiter()
	for i := 0; i < 5; i++ {
		domain := time.Now().String() + string(rune('a'+i))
		rl.Allow(domain)
	}
	if rl.Size() < 5 {
		t.Fatalf("expected at least 5 tracked domains, got %d", rl.Size())
	}
}
