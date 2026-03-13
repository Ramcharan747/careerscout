// Package ingestion — bloom.go
// Thread-safe Bloom filter wrapper for in-memory URL/domain deduplication.
// This sits in front of the Postgres round-trip in the ingestion hot path.
// At 10M entries with 0.01% FP rate the filter consumes ~24MB on disk and
// in memory — negligibly cheap compared to repeated DB queries at scale.
//
// State is persisted to disk via Save/Load so the filter survives restarts.
// Without persistence the 97% dedup claim only holds within a single process run.
package ingestion

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/bits-and-blooms/bloom/v3"
)

const (
	// bloomCapacity is the maximum expected number of distinct domains in a
	// single ingestion run. Matches APEX's 10M URL recommendation.
	bloomCapacity = 10_000_000

	// bloomFPRate is the acceptable false-positive rate.
	// A FP means we skip a domain we haven't actually processed — negligible
	// for large-scale discovery workloads.
	bloomFPRate = 0.0001 // 0.01%
)

// BloomDeduper is a concurrency-safe Bloom filter for domain deduplication.
// Persist the filter between process restarts using Save and Load.
type BloomDeduper struct {
	mu     sync.RWMutex
	filter *bloom.BloomFilter
}

// NewBloomDeduper allocates a new Bloom filter sized for bloomCapacity entries.
func NewBloomDeduper() *BloomDeduper {
	return &BloomDeduper{
		filter: bloom.NewWithEstimates(bloomCapacity, bloomFPRate),
	}
}

// Seen returns true if the domain has been added to the filter before.
// A false return guarantees the domain has NOT been seen in this run.
// A true return means the domain was seen (with negligible false-positive chance).
func (b *BloomDeduper) Seen(domain string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.filter.TestString(domain)
}

// Add marks the domain as seen in the filter.
func (b *BloomDeduper) Add(domain string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.filter.AddString(domain)
}

// Count returns the estimated number of distinct domains added so far.
func (b *BloomDeduper) Count() uint {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return uint(b.filter.ApproximatedSize())
}

// Save serialises the Bloom filter to the given file path using the binary
// format from bits-and-blooms. The file is written atomically via a temp file
// and rename to avoid partial writes corrupting the state on crash.
func (b *BloomDeduper) Save(path string) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Write to a temp file adjacent to the target, then rename atomically.
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("bloom.Save: create temp %q: %w", tmp, err)
	}

	if _, werr := b.filter.WriteTo(f); werr != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("bloom.Save: write %q: %w", tmp, werr)
	}
	if serr := f.Sync(); serr != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("bloom.Save: sync %q: %w", tmp, serr)
	}
	f.Close()

	if err = os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("bloom.Save: rename %q → %q: %w", tmp, path, err)
	}
	return nil
}

// Load reads a previously saved Bloom filter from path.
// If the file does not exist, Load returns nil — the caller should treat this
// as a fresh (empty) filter rather than an error.
func (b *BloomDeduper) Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no saved state — start fresh
		}
		return fmt.Errorf("bloom.Load: open %q: %w", path, err)
	}
	defer f.Close()

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, err = b.filter.ReadFrom(f); err != nil {
		if err == io.EOF {
			// Empty or truncated file — treat as fresh filter.
			return nil
		}
		return fmt.Errorf("bloom.Load: read %q: %w", path, err)
	}
	return nil
}
