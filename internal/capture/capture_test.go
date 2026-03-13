package capture_test

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/careerscout/careerscout/internal/capture"
)

func TestCapture_RecordsHitAndMiss(t *testing.T) {
	// Use a temp file
	tmp, err := os.CreateTemp("", "capture-*.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.Close()

	os.Setenv("CAPTURE_PATH", tmp.Name())
	defer os.Unsetenv("CAPTURE_PATH")

	nc, err := capture.New()
	if err != nil {
		t.Fatal(err)
	}

	hit := capture.CaptureEntry{
		Domain:          "greenhouse.io",
		URL:             "https://api.greenhouse.io/v1/jobs",
		Method:          "GET",
		ClassifierScore: 0.9,
		WasHit:          true,
		Timestamp:       time.Now(),
	}
	miss := capture.CaptureEntry{
		Domain:          "unknown.com",
		URL:             "https://api.unknown.com/v1/track",
		Method:          "POST",
		ClassifierScore: 0.1,
		WasHit:          false,
		Timestamp:       time.Now(),
	}

	_ = nc.Record(hit)
	_ = nc.Record(miss)
	_ = nc.Close()

	f, err := os.Open(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var entries []capture.CaptureEntry
	for scanner.Scan() {
		var e capture.CaptureEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if !entries[0].WasHit || entries[1].WasHit {
		t.Fatal("was_hit flags not recorded correctly")
	}
}

func TestCapture_DisabledWhenPathIsNone(t *testing.T) {
	os.Setenv("CAPTURE_PATH", "none")
	defer os.Unsetenv("CAPTURE_PATH")

	nc, err := capture.New()
	if err != nil {
		t.Fatal(err)
	}

	err = nc.Record(capture.CaptureEntry{Domain: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_ = nc.Close()
	// Just ensuring it doesn't crash and returns no errors.
}

func TestCapture_FlushesOnClose(t *testing.T) {
	tmp, _ := os.CreateTemp("", "capture-flush-*.ndjson")
	defer os.Remove(tmp.Name())
	tmp.Close()

	os.Setenv("CAPTURE_PATH", tmp.Name())
	defer os.Unsetenv("CAPTURE_PATH")

	nc, _ := capture.New()
	_ = nc.Record(capture.CaptureEntry{Domain: "flush-test"})

	// Before close, file length might be zero due to buffering
	infoBefore, _ := os.Stat(tmp.Name())

	_ = nc.Close() // Should flush

	infoAfter, _ := os.Stat(tmp.Name())

	if infoAfter.Size() <= infoBefore.Size() && infoBefore.Size() == 0 {
		t.Fatalf("expected file to grow on Close flush, sizes: before=%d after=%d", infoBefore.Size(), infoAfter.Size())
	}
}
