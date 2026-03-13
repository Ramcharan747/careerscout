package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/careerscout/careerscout/internal/capture"
	"github.com/careerscout/careerscout/internal/frontier"
	"go.uber.org/zap"
)

type DomainStats struct {
	Total      int
	Hits       int
	Misses     int
	NearMisses []capture.CaptureEntry
}

func main() {
	captureFile := flag.String("file", "", "Path to the capture NDJSON file")
	flag.Parse()

	log, _ := zap.NewDevelopment()
	defer log.Sync()

	if *captureFile == "" {
		log.Fatal("must provide --file path to capture file")
	}

	f, err := os.Open(*captureFile)
	if err != nil {
		log.Fatal("failed to open capture file", zap.Error(err))
	}
	defer f.Close()

	// Load existing feedback store
	feedbackPath := frontier.GetEnvStatePath()
	fb := frontier.NewFeedbackStore()
	if err := fb.Load(feedbackPath); err != nil {
		log.Warn("could not load feedback store, creating new", zap.Error(err))
	}

	stats := make(map[string]*DomainStats)
	scanner := bufio.NewScanner(f)

	// Increase buffer capacity for potentially large JSON lines (e.g. 2048 body + headers)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var totalEntries int
	var totalNearMisses int

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry capture.CaptureEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			log.Warn("skipping invalid log entry", zap.Error(err))
			continue
		}

		totalEntries++
		ds, ok := stats[entry.Domain]
		if !ok {
			ds = &DomainStats{}
			stats[entry.Domain] = ds
		}

		ds.Total++
		if entry.WasHit {
			ds.Hits++
		} else {
			ds.Misses++
			// Check for near-miss
			if entry.BodyScore > 0.25 {
				ds.NearMisses = append(ds.NearMisses, entry)
				totalNearMisses++
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Error("error scrolling through capture file", zap.Error(err))
	}

	fmt.Printf("\n=== CareerScout Network Capture Analysis ===\n")
	fmt.Printf("Parsed %d traffic entries.\n\n", totalEntries)

	for domain, ds := range stats {
		if ds.Hits == 0 && len(ds.NearMisses) == 0 {
			// Skip printing domains that are pure noise to keep report readable
			continue
		}

		fmt.Printf("Domain: %s\n", domain)
		fmt.Printf("  Total: %d | Hits: %d | Misses: %d\n", ds.Total, ds.Hits, ds.Misses)

		if len(ds.NearMisses) > 0 {
			fmt.Printf("  ⚠️ Near-Miss Candidates (Body Score > 0.25):\n")
			for _, nm := range ds.NearMisses {
				shortPreview := nm.ResponseBodyPreview
				if len(shortPreview) > 200 {
					shortPreview = shortPreview[:200]
				}
				fmt.Printf("    * URL: %s\n", nm.URL)
				fmt.Printf("      Body Score: %.2f\n", nm.BodyScore)
				fmt.Printf("      Preview: %s\n\n", shortPreview)

				// Inject partial hit directly to Frontier
				fb.RecordHit(domain, 0.5)
			}
		}
	}

	fmt.Printf("=== Analysis Complete ===\n")
	fmt.Printf("Found %d near-misses. Injected fractional feedback for learning.\n", totalNearMisses)

	// Save back to disk so next scan prioritizes these
	if err := fb.Save(feedbackPath); err != nil {
		log.Error("failed to save feedback store", zap.Error(err))
	} else {
		log.Info("saved updated domain feedback store", zap.String("path", feedbackPath))
	}
}
