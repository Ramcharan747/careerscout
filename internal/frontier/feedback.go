package frontier

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricFeedbackHitRate = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "domain_feedback_hit_rate",
		Help: "Ratio of total hits to total hits+misses across all domains in the feedback store",
	})
)

// FeedbackEntry represents historical discovery data for a domain.
type FeedbackEntry struct {
	Hits    float64   `json:"hits"`
	Misses  float64   `json:"misses"`
	LastHit time.Time `json:"last_hit"`
}

// DomainFeedbackStore stores hit/miss performance of domains via sync.Map.
type DomainFeedbackStore struct {
	mu          sync.Mutex // For persistence only
	Map         sync.Map
	totalHits   float64
	totalMisses float64
}

// NewFeedbackStore initializes an empty store.
func NewFeedbackStore() *DomainFeedbackStore {
	return &DomainFeedbackStore{}
}

// Seed sets the initial state for a domain only if no existing entry is present.
// It must not overwrite data from a loaded feedback.json.
func (f *DomainFeedbackStore) Seed(domain string, hits float64, misses float64) {
	if _, loaded := f.Map.LoadOrStore(domain, &FeedbackEntry{
		Hits:    hits,
		Misses:  misses,
		LastHit: time.Now(),
	}); !loaded {
		f.updateMetrics(hits, misses)
	}
}

// RecordHit registers a successful discovery. Accepts an optional weight (default 1.0).
func (f *DomainFeedbackStore) RecordHit(domain string, weight ...float64) {
	w := 1.0
	if len(weight) > 0 {
		w = weight[0]
	}
	v, _ := f.Map.LoadOrStore(domain, &FeedbackEntry{})
	entry := v.(*FeedbackEntry)
	f.Map.Store(domain, &FeedbackEntry{
		Hits:    entry.Hits + w,
		Misses:  entry.Misses,
		LastHit: time.Now(),
	})
	f.updateMetrics(w, 0)
}

// RecordMiss registers processing without discovery. Accepts an optional weight (default 1.0).
func (f *DomainFeedbackStore) RecordMiss(domain string, weight ...float64) {
	w := 1.0
	if len(weight) > 0 {
		w = weight[0]
	}
	v, _ := f.Map.LoadOrStore(domain, &FeedbackEntry{})
	entry := v.(*FeedbackEntry)
	f.Map.Store(domain, &FeedbackEntry{
		Hits:    entry.Hits,
		Misses:  entry.Misses + w,
		LastHit: entry.LastHit,
	})
	f.updateMetrics(0, w)
}

func (f *DomainFeedbackStore) updateMetrics(hit float64, miss float64) {
	f.mu.Lock()
	f.totalHits += hit
	f.totalMisses += miss
	total := f.totalHits + f.totalMisses
	if total > 0 {
		metricFeedbackHitRate.Set(f.totalHits / total)
	}
	f.mu.Unlock()
}

// ScoreBoost calculates a boost between 0.0 and 0.20 based on domain history.
// boost = min(0.20, hits / (hits+misses+1) * 0.20)
func (f *DomainFeedbackStore) ScoreBoost(domain string) float64 {
	v, ok := f.Map.Load(domain)
	if !ok {
		return 0.0
	}
	entry := v.(*FeedbackEntry)
	if entry.Hits == 0 {
		return 0.0
	}
	ratio := float64(entry.Hits) / float64(entry.Hits+entry.Misses+1)
	val := ratio * 0.20
	if val > 0.20 {
		return 0.20
	}
	return val
}

// Save dumps the domain feedback to disk defensively (temp -> rename).
func (f *DomainFeedbackStore) Save(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	data := make(map[string]*FeedbackEntry)
	f.Map.Range(func(key, value any) bool {
		data[key.(string)] = value.(*FeedbackEntry)
		return true
	})

	bytes, err := json.Marshal(data)
	if err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, bytes, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Load restores the feedback store from disk. Missing file is not an error.
func (f *DomainFeedbackStore) Load(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	bytes, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var data map[string]*FeedbackEntry
	if err := json.Unmarshal(bytes, &data); err != nil {
		return err
	}

	f.totalHits = 0
	f.totalMisses = 0

	for k, v := range data {
		f.Map.Store(k, v)
		f.totalHits += v.Hits
		f.totalMisses += v.Misses
	}

	if (f.totalHits + f.totalMisses) > 0 {
		metricFeedbackHitRate.Set(float64(f.totalHits) / float64(f.totalHits+f.totalMisses))
	}

	return nil
}

// GetEnvStatePath returns the FEEDBACK_STATE_PATH or default.
func GetEnvStatePath() string {
	if p := os.Getenv("FEEDBACK_STATE_PATH"); p != "" {
		return p
	}
	return "./feedback.json"
}
