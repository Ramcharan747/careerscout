package frontier

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// HostGovernor ensures we do not hammer a single domain too frequently.
type HostGovernor struct {
	mu        sync.Mutex
	lastFetch map[string]time.Time
	delay     time.Duration
}

// NewHostGovernor creates a new politeness governor.
func NewHostGovernor() *HostGovernor {
	delayMs := 2000
	if val := os.Getenv("POLITENESS_DELAY_MS"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			delayMs = parsed
		}
	}
	return &HostGovernor{
		lastFetch: make(map[string]time.Time),
		delay:     time.Duration(delayMs) * time.Millisecond,
	}
}

// Allowed returns true if the host is allowed to be fetched right now.
func (g *HostGovernor) Allowed(host string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	last, exists := g.lastFetch[host]
	if !exists {
		return true
	}
	return time.Since(last) >= g.delay
}

// Record updates the last fetch timestamp for the host to now.
func (g *HostGovernor) Record(host string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastFetch[host] = time.Now()
}
