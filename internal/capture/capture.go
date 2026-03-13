// Package capture — capture.go
// NetworkCapture provides a thread-safe, high-performance structured logger
// for all intercepted HTTP requests, ensuring that near-misses and unknown
// job APIs can be discovered via post-scan analysis.
package capture

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
	"unicode/utf8"
)

// CaptureEntry records the metadata, signals, and outcome of an intercepted request.
type CaptureEntry struct {
	Timestamp           time.Time         `json:"timestamp"`
	Domain              string            `json:"domain"`
	URL                 string            `json:"url"`
	Method              string            `json:"method"`
	RequestHeaders      map[string]string `json:"request_headers"`
	ResponseStatus      int               `json:"response_status"`
	ResponseHeaders     map[string]string `json:"response_headers"`
	ResponseBodyPreview string            `json:"response_body_preview"`
	ClassifierScore     float64           `json:"classifier_score"`
	BodyScore           float64           `json:"body_score"`
	WasHit              bool              `json:"was_hit"`
}

// NetworkCapture handles buffered writing of CaptureEntries to an ndjson file.
type NetworkCapture struct {
	disabled bool

	mu   sync.Mutex
	file *os.File
	out  *bufio.Writer

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a new NetworkCapture. The output file is configured via the
// CAPTURE_PATH environment variable (defaults to ./capture_YYYYMMDD_HHMMSS.ndjson).
// If CAPTURE_PATH=none, recording is a zero-overhead no-op.
func New() (*NetworkCapture, error) {
	path := os.Getenv("CAPTURE_PATH")
	if path == "none" {
		return &NetworkCapture{disabled: true}, nil
	}

	if path == "" {
		path = fmt.Sprintf("./capture_%s.ndjson", time.Now().Format("20060102_150405"))
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open capture file %q: %w", path, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	nc := &NetworkCapture{
		file:   f,
		out:    bufio.NewWriterSize(f, 64*1024), // 64KB buffer
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go nc.flusher()

	return nc, nil
}

// flusher runs in the background to automatically flush the 64KB buffer every 5 seconds.
func (nc *NetworkCapture) flusher() {
	defer close(nc.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-nc.ctx.Done():
			return
		case <-ticker.C:
			nc.mu.Lock()
			_ = nc.out.Flush()
			nc.mu.Unlock()
		}
	}
}

// Record encodes the entry as JSON and appends it to the buffer (thread-safe).
func (nc *NetworkCapture) Record(entry CaptureEntry) error {
	if nc.disabled {
		return nil
	}

	// Make response body preview UTF-8 safe, max 2048 bytes
	if len(entry.ResponseBodyPreview) > 2048 {
		previewBytes := []byte(entry.ResponseBodyPreview)[:2048]
		// Fix partial utf8 character at the end
		for len(previewBytes) > 0 && !utf8.ValidString(string(previewBytes)) {
			previewBytes = previewBytes[:len(previewBytes)-1]
		}
		entry.ResponseBodyPreview = string(previewBytes)
	} else if !utf8.ValidString(entry.ResponseBodyPreview) {
		// Clean the string directly if it's invalid but shorter than 2048
		entry.ResponseBodyPreview = stringsToValidUTF8(entry.ResponseBodyPreview)
	}

	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	nc.mu.Lock()
	defer nc.mu.Unlock()

	_, err = nc.out.Write(b)
	if err != nil {
		return err
	}
	_ = nc.out.WriteByte('\n')

	return nil
}

// Close gracefully stops the background flusher, flushes the final buffer bytes,
// and closes the underlying file handle.
func (nc *NetworkCapture) Close() error {
	if nc.disabled {
		return nil
	}

	nc.cancel()
	<-nc.done

	nc.mu.Lock()
	defer nc.mu.Unlock()

	flushErr := nc.out.Flush()
	closeErr := nc.file.Close()

	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

// Helper to sanitise string to valid UTF8
func stringsToValidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	v := make([]rune, 0, len(s))
	for i, w := 0, 0; i < len(s); i += w {
		r, width := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && width == 1 {
			v = append(v, rune('?'))
		} else {
			v = append(v, r)
		}
		w = width
	}
	return string(v)
}
