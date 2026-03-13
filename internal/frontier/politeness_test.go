package frontier_test

import (
	"testing"
	"time"

	"github.com/careerscout/careerscout/internal/frontier"
)

func TestGovernor_AllowsAfterDelay(t *testing.T) {
	t.Setenv("POLITENESS_DELAY_MS", "50")
	gov := frontier.NewHostGovernor()

	host := "acme.com"
	if !gov.Allowed(host) {
		t.Fatalf("expected allowed on first check")
	}

	gov.Record(host)
	if gov.Allowed(host) {
		t.Fatalf("expected blocked immediately after record")
	}

	time.Sleep(60 * time.Millisecond)
	if !gov.Allowed(host) {
		t.Fatalf("expected allowed after delay")
	}
}

func TestGovernor_BlocksWithinDelay(t *testing.T) {
	t.Setenv("POLITENESS_DELAY_MS", "5000") // 5s
	gov := frontier.NewHostGovernor()

	host := "blocked.com"
	gov.Record(host)

	// should block immediately after
	if gov.Allowed(host) {
		t.Fatalf("expected block within delay")
	}
}
