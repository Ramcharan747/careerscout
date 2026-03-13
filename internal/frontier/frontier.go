package frontier

import (
	"container/heap"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricQueueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "frontier_queue_depth",
		Help: "Current number of items in the frontier priority queue",
	})
	metricScoreMean = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "frontier_score_mean",
		Help: "Rolling mean of the last 1000 popped scores",
	})
	metricEvictions = promauto.NewCounter(prometheus.CounterOpts{
		Name: "frontier_evictions_total",
		Help: "Total number of URLs evicted from the frontier due to full queue",
	})
)

type item struct {
	url        string
	score      float64
	insertedAt time.Time
	index      int
}

type maxHeap []*item

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i].score > h[j].score } // Max-heap
func (h maxHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *maxHeap) Push(x any) {
	n := len(*h)
	item := x.(*item)
	item.index = n
	*h = append(*h, item)
}

func (h *maxHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // for safety
	*h = old[0 : n-1]
	return item
}

// Frontier is a thread-safe max-heap priority queue bounded by capacity.
type Frontier struct {
	mu          sync.Mutex
	pq          maxHeap
	maxCapacity int
	inFlight    int32 // atomic: number of items currently being processed

	// For metricScoreMean
	popCount  int
	scoreSum  float64
	last1000  [1000]float64
	histIndex int
}

// New creates a new bounding scored priority queue.
func New() *Frontier {
	maxCap := 500000
	if val := os.Getenv("FRONTIER_MAX"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			maxCap = parsed
		}
	}
	f := &Frontier{
		pq:          make(maxHeap, 0, 1024),
		maxCapacity: maxCap,
	}
	heap.Init(&f.pq)
	metricQueueDepth.Set(0)
	return f
}

// Push adds a URL/score to the queue.
// If the heap is full and the score is higher than the minimum, it evicts the minimum.
// If the heap is full and the score is lower than the minimum, it silently discards it.
func (f *Frontier) Push(url string, score float64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.pq) >= f.maxCapacity {
		// The minimum item in a max-heap is at a leaf, but it's O(N) to find perfectly.
		// However, we can track the exact minimum or just find the leaf with min score.
		// A simple way is to find the minimum among the leaves (roughly half the heap).
		minIdx := -1
		minScore := score // only evict if it's strictly worse than our new score

		startIndex := len(f.pq) / 2
		for i := startIndex; i < len(f.pq); i++ {
			if f.pq[i].score < minScore {
				minScore = f.pq[i].score
				minIdx = i
			}
		}

		if minIdx == -1 {
			// score is worse or equal to everything in the leaves, drop it.
			return
		}

		// Evict and replace
		metricEvictions.Inc()
		f.pq[minIdx] = &item{
			url:        url,
			score:      score,
			insertedAt: time.Now(),
			index:      minIdx,
		}
		heap.Fix(&f.pq, minIdx)
	} else {
		heap.Push(&f.pq, &item{
			url:        url,
			score:      score,
			insertedAt: time.Now(),
		})
	}
	metricQueueDepth.Set(float64(len(f.pq)))
}

// Pop removes and returns the highest score URL from the queue.
// Returns "", 0.0 if empty.
func (f *Frontier) Pop() (string, float64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.pq) == 0 {
		return "", 0.0
	}

	it := heap.Pop(&f.pq).(*item)
	metricQueueDepth.Set(float64(len(f.pq)))

	// Update rolling mean
	// Confirmed: This is a fixed window rolling mean over the last 1000 popped scores.
	oldScore := f.last1000[f.histIndex]
	f.last1000[f.histIndex] = it.score
	f.scoreSum = f.scoreSum - oldScore + it.score
	f.histIndex = (f.histIndex + 1) % 1000
	if f.popCount < 1000 {
		f.popCount++
		metricScoreMean.Set(f.scoreSum / float64(f.popCount))
	} else {
		metricScoreMean.Set(f.scoreSum / 1000.0)
	}

	return it.url, it.score
}

// Len returns the current size of the queue.
func (f *Frontier) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pq)
}

// CheckOut increments the in-flight counter. Call when a worker starts processing a popped item.
func (f *Frontier) CheckOut() {
	atomic.AddInt32(&f.inFlight, 1)
}

// CheckIn decrements the in-flight counter. Call when a worker finishes processing an item.
func (f *Frontier) CheckIn() {
	atomic.AddInt32(&f.inFlight, -1)
}

// WaitUntilDrained blocks until both the queue is empty AND no items are in-flight.
// Poll every 500ms. Returns immediately if already drained.
func (f *Frontier) WaitUntilDrained() {
	for {
		f.mu.Lock()
		queueEmpty := len(f.pq) == 0
		f.mu.Unlock()

		if queueEmpty && atomic.LoadInt32(&f.inFlight) == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}
