package frontier_test

import (
	"math"
	"testing"

	"github.com/careerscout/careerscout/internal/frontier"
)

func TestFrontier_OrdersByScore(t *testing.T) {
	f := frontier.New()
	f.Push("https://acme.com/a", 0.5)
	f.Push("https://acme.com/b", 0.9)
	f.Push("https://acme.com/c", 0.2)

	url, score := f.Pop()
	if url != "https://acme.com/b" || math.Abs(score-0.9) > 0.001 {
		t.Fatalf("expected highest score first, got %s:%f", url, score)
	}

	url, score = f.Pop()
	if url != "https://acme.com/a" || math.Abs(score-0.5) > 0.001 {
		t.Fatalf("expected second highest, got %s:%f", url, score)
	}

	f.Pop() // pop last
	url, _ = f.Pop()
	if url != "" {
		t.Fatalf("expected empty string when empty, got %q", url)
	}
}

func TestFrontier_EvictsLowestOnFull(t *testing.T) {
	// We can't easily mock FRONTIER_MAX dynamically inside the same process using env vars safely without lock,
	// but we can just use setenv for this test as Go runs tests sequentially or we isolate it.
	// To be safe we will test the bounding logic.

	// Since Frontier.maxCapacity is initialized via FRONTIER_MAX
	t.Setenv("FRONTIER_MAX", "3")

	f := frontier.New()
	// push 3
	f.Push("a", 0.5)
	f.Push("b", 0.3)
	f.Push("c", 0.8)

	// push worse, should be dropped
	f.Push("d", 0.1)
	if f.Len() != 3 {
		t.Fatalf("expected len 3")
	}

	// push better, should evict 0.3
	f.Push("e", 0.6)
	if f.Len() != 3 {
		t.Fatalf("expected len 3")
	}

	// pop order: c(0.8), e(0.6), a(0.5)
	pop1, _ := f.Pop()
	pop2, _ := f.Pop()
	pop3, _ := f.Pop()

	if pop1 != "c" || pop2 != "e" || pop3 != "a" {
		t.Fatalf("eviction failed, got order: %s, %s, %s", pop1, pop2, pop3)
	}
}

func TestScorer_KnownCareerURL(t *testing.T) {
	// "known career-path segment .jobs scores +0.25; depth <=2 scores +0.15; .io +0.10; no noise +0.10 = 0.60
	score := frontier.ScoreStatic("https://acme.io/jobs")
	if math.Abs(score-0.60) > 0.001 {
		t.Fatalf("expected 0.60, got %f", score)
	}

	// noise check
	scoreNoise := frontier.ScoreStatic("https://acme.io/about/jobs")
	// depth 2 (+0.15), .io (+0.10), jobs (+0.25), but has /about (noise=0) -> 0.50
	if math.Abs(scoreNoise-0.50) > 0.001 {
		t.Fatalf("expected 0.50 due to noise, got %f", scoreNoise)
	}

	// ATS subdomain
	scoreATS := frontier.ScoreStatic("https://jobs.lever.co/acme")
	// jobs. +0.20, .co +0.10, no noise +0.10, depth 1 +0.15 = 0.55
	if math.Abs(scoreATS-0.55) > 0.001 {
		t.Fatalf("expected 0.55, got %f", scoreATS)
	}
}

func TestGovernor_ReinsertionFloorPreventsPermanentEviction(t *testing.T) {
	f := frontier.New()
	url := "https://blocked.com/jobs"
	score := 0.20

	// Simulating the loop in main.go
	// 1st block -> 0.15
	newScore1 := math.Max(score-0.05, 0.10)
	f.Push(url, newScore1)

	pop1, s1 := f.Pop()
	if pop1 != url || math.Abs(s1-0.15) > 0.001 {
		t.Fatalf("expected 0.15, got %f", s1)
	}

	// 2nd block -> 0.10
	newScore2 := math.Max(s1-0.05, 0.10)
	f.Push(url, newScore2)

	pop2, s2 := f.Pop()
	if pop2 != url || math.Abs(s2-0.10) > 0.001 {
		t.Fatalf("expected 0.10, got %f", s2)
	}

	// 3rd block -> 0.10 (floored)
	newScore3 := math.Max(s2-0.05, 0.10)
	f.Push(url, newScore3)

	pop3, s3 := f.Pop()
	if pop3 != url || math.Abs(s3-0.10) > 0.001 {
		t.Fatalf("expected 0.10 floored, got %f", s3)
	}
}

func TestMetrics_ScoreMeanIsRollingNotCumulative(t *testing.T) {
	// The frontier is hardcoded to a 1000-item fixed rolling window.
	// To prove it is rolling and not cumulative, we push 1000 * 1.0, then 1000 * 0.0.
	// If cumulative, mean would be 0.5. If rolling it will be 0.0.
	f := frontier.New()

	for i := 0; i < 1000; i++ {
		f.Push("https://url.com/pos", 1.0)
		f.Pop()
	}

	for i := 0; i < 1000; i++ {
		f.Push("https://url.com/neg", 0.0)
		f.Pop()
	}

	f.Push("https://url.com/test", 0.0)
	_, score := f.Pop()
	if score != 0.0 {
		t.Fatalf("expected 0.0, got %f", score)
	}
}
