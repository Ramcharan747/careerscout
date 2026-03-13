// cmd/verify_companies/main.go — Bulk-verifies wayback company slugs against ATS APIs.
// Reuses the proven probers from internal/atsprober.
//
// Usage:
//
//	go run ./cmd/verify_companies --input-dir . --workers 50
//	go run ./cmd/verify_companies --ats greenhouse --workers 100
//	go run ./cmd/verify_companies --retry-errors --workers 30
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/careerscout/careerscout/internal/atsprober"
)

// probeFunc maps ATS name → prober function from probers.go
var probeFuncs = map[string]func(string) atsprober.ProbeResult{
	"greenhouse":      atsprober.ProbeGreenhouse,
	"lever":           atsprober.ProbeLever,
	"ashby":           atsprober.ProbeAshby,
	"workable":        atsprober.ProbeWorkable,
	"bamboohr":        atsprober.ProbeBambooHR,
	"recruitee":       atsprober.ProbeRecruitee,
	"teamtailor":      atsprober.ProbeTeamtailor,
	"rippling":        atsprober.ProbeRippling,
	"pinpoint":        atsprober.ProbePinpoint,
	"freshteam":       atsprober.ProbeFreshteam,
	"smartrecruiters": atsprober.ProbeSmartRecruiters,
	// breezy uses subdomain pattern — add probe if exists
}

func main() {
	inputDir := flag.String("input-dir", ".", "Directory with wayback_*_companies.txt files")
	outputDir := flag.String("output-dir", ".", "Output directory for verified_*_companies.txt")
	workers := flag.Int("workers", 50, "Concurrent workers per ATS")
	atsFilter := flag.String("ats", "", "Only verify this ATS (e.g. 'greenhouse')")
	retryErrors := flag.Bool("retry-errors", false, "Retry from errors_* files instead")
	flag.Parse()

	type summary struct {
		Active   int
		Inactive int
		Errors   int
	}
	results := make(map[string]*summary)
	var grandActive, grandTotal int64

	// Increase rate limits for bulk verification
	for ats := range probeFuncs {
		atsprober.SetRateLimit(ats, 20, 20)
	}

	for atsName, probeFn := range probeFuncs {
		if *atsFilter != "" && *atsFilter != atsName {
			continue
		}

		// Pick input file
		prefix := "wayback"
		if *retryErrors {
			prefix = "errors"
		}
		inputFile := filepath.Join(*inputDir, fmt.Sprintf("%s_%s_companies.txt", prefix, atsName))

		slugs, err := readLines(inputFile)
		if err != nil {
			continue // file doesn't exist
		}
		if len(slugs) == 0 {
			continue
		}

		fmt.Printf("\n%s\n %s — Verifying %d companies (%d workers)\n%s\n",
			strings.Repeat("=", 60), strings.ToUpper(atsName), len(slugs), *workers, strings.Repeat("=", 60))

		active, inactive, errors := verifyATS(atsName, slugs, probeFn, *workers)

		results[atsName] = &summary{Active: len(active), Inactive: len(inactive), Errors: len(errors)}
		atomic.AddInt64(&grandActive, int64(len(active)))
		atomic.AddInt64(&grandTotal, int64(len(slugs)))

		fmt.Printf("  ✅ Active:   %d\n", len(active))
		fmt.Printf("  ❌ Inactive: %d\n", len(inactive))
		fmt.Printf("  ⚠️  Errors:   %d\n", len(errors))

		// Save active companies
		activeFile := filepath.Join(*outputDir, fmt.Sprintf("verified_%s_companies.txt", atsName))
		mode := os.O_CREATE | os.O_WRONLY
		if *retryErrors {
			mode |= os.O_APPEND
		} else {
			mode |= os.O_TRUNC
		}
		writeLines(activeFile, active, mode)
		fmt.Printf("  → Saved %d to %s\n", len(active), activeFile)

		// Save errors for retry
		if len(errors) > 0 {
			errFile := filepath.Join(*outputDir, fmt.Sprintf("errors_%s_companies.txt", atsName))
			writeLines(errFile, errors, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
			fmt.Printf("  → Saved %d errors to %s\n", len(errors), errFile)
		}
	}

	// Grand summary
	fmt.Printf("\n%s\n GRAND TOTAL: %d active / %d total\n%s\n",
		strings.Repeat("=", 60), grandActive, grandTotal, strings.Repeat("=", 60))

	names := make([]string, 0, len(results))
	for k := range results {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		s := results[name]
		fmt.Printf("  %-20s: %5d active / %5d inactive / %5d errors\n", name, s.Active, s.Inactive, s.Errors)
	}
}

func verifyATS(atsName string, slugs []string, probeFn func(string) atsprober.ProbeResult, workers int) (active, inactive, errors []string) {
	type result struct {
		slug      string
		confirmed bool
		errored   bool
	}

	ch := make(chan string, len(slugs))
	resCh := make(chan result, len(slugs))

	var wg sync.WaitGroup

	// Launch workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for slug := range ch {
				pr := probeFn(slug)
				r := result{slug: slug}
				if pr.StatusCode == 0 {
					// Network error
					r.errored = true
				} else if pr.Confirmed {
					r.confirmed = true
				}
				// StatusCode != 0 && !Confirmed = inactive (404/empty)
				resCh <- r
			}
		}()
	}

	// Feed slugs
	for _, s := range slugs {
		ch <- s
	}
	close(ch)

	// Collect results in background
	go func() {
		wg.Wait()
		close(resCh)
	}()

	var done int64
	total := int64(len(slugs))
	start := time.Now()

	for r := range resCh {
		done++
		if r.confirmed {
			active = append(active, r.slug)
		} else if r.errored {
			errors = append(errors, r.slug)
		} else {
			inactive = append(inactive, r.slug)
		}

		if done%200 == 0 {
			elapsed := time.Since(start).Seconds()
			rate := float64(done) / elapsed
			fmt.Printf("    Progress: %d/%d (%.0f/s) — active=%d inactive=%d errors=%d\n",
				done, total, rate, len(active), len(inactive), len(errors))
		}
	}

	sort.Strings(active)
	sort.Strings(inactive)
	sort.Strings(errors)
	return
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, sc.Err()
}

func writeLines(path string, lines []string, flag int) {
	f, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		log.Printf("ERROR writing %s: %v", path, err)
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
	w.Flush()
}
