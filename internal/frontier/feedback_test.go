package frontier_test

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/careerscout/careerscout/internal/frontier"
)

func TestFeedbackStore_BoostIncreasesWithHits(t *testing.T) {
	fb := frontier.NewFeedbackStore()
	domain := "acme.com"

	fb.RecordHit(domain)
	boost1 := fb.ScoreBoost(domain)
	if boost1 <= 0.0 {
		t.Fatalf("expected positive boost after hit, got %f", boost1)
	}

	fb.RecordHit(domain)
	boost2 := fb.ScoreBoost(domain)
	if boost2 <= boost1 {
		t.Fatalf("expected boost to increase with second hit, got %f vs %f", boost2, boost1)
	}

	// Max limit checks
	for i := 0; i < 100; i++ {
		fb.RecordHit(domain)
	}
	boostMax := fb.ScoreBoost(domain)
	if boostMax > 0.20 {
		t.Fatalf("expected boost to cap at 0.20, got %f", boostMax)
	}
}

func TestFeedbackStore_ZeroBoostOnNoData(t *testing.T) {
	fb := frontier.NewFeedbackStore()

	domain := "unknown.com"
	if fb.ScoreBoost(domain) != 0.0 {
		t.Fatalf("expected 0 boost")
	}

	fb.RecordMiss(domain)
	if fb.ScoreBoost(domain) != 0.0 {
		t.Fatalf("expected 0 boost on miss only")
	}
}

func TestFeedbackStore_PersistAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "feedback.json")

	fb1 := frontier.NewFeedbackStore()
	fb1.RecordHit("stripe.com")
	fb1.RecordHit("stripe.com")
	fb1.RecordMiss("stripe.com")

	if err := fb1.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	fb2 := frontier.NewFeedbackStore()
	if err := fb2.Load(path); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if math.Abs(fb1.ScoreBoost("stripe.com")-fb2.ScoreBoost("stripe.com")) > 0.0001 {
		t.Fatalf("boost mismatch after load")
	}
}

func TestFeedbackStore_SeedAppliesOnFirstRun(t *testing.T) {
	fb := frontier.NewFeedbackStore()
	fb.Seed("greenhouse.io", 50, 5)

	boost := fb.ScoreBoost("greenhouse.io")
	// 50 / (50 + 5 + 1) * 0.20 = 50 / 56 * 0.20 = 0.892 * 0.20 = 0.178
	if boost < 0.17 || boost > 0.18 {
		t.Fatalf("expected seed to apply and yield boost ~0.178, got %f", boost)
	}
}

func TestFeedbackStore_SeedDoesNotOverwriteExisting(t *testing.T) {
	fb := frontier.NewFeedbackStore()
	// Simulate existing data from loaded json
	fb.RecordHit("bamboohr.com")
	initialBoost := fb.ScoreBoost("bamboohr.com")

	// Attempt to seed which should be ignored since there is existing data
	fb.Seed("bamboohr.com", 100, 0)

	newBoost := fb.ScoreBoost("bamboohr.com")
	if math.Abs(initialBoost-newBoost) > 0.0001 {
		t.Fatalf("expected seed not to overwrite existing data, boost changed from %f to %f", initialBoost, newBoost)
	}
}
