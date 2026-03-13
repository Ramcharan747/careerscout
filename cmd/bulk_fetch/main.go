// cmd/bulk_fetch/main.go — Imports verified ATS company slugs into the DB
// and optionally spot-checks a few, then triggers the fetch pipeline.
//
// Flow:
//  1. Reads verified_*_companies.txt files
//  2. For each slug, inserts into companies + data_sources tables
//  3. With --spot-check, tests 3 slugs per ATS with live API calls
//  4. With --fetch, also fetches jobs using jobparser (like fetch_jobs)
//
// Usage:
//
//	go run ./cmd/bulk_fetch --spot-check                  # test 3 per ATS, no DB
//	go run ./cmd/bulk_fetch --import                       # import into DB
//	go run ./cmd/bulk_fetch --import --fetch --workers 30  # import + fetch jobs
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/careerscout/careerscout/internal/atsprober"
	"github.com/careerscout/careerscout/internal/jobparser"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ua = "Mozilla/5.0 (compatible; CareerScout/1.0; +https://careerscout.io/bot)"

var httpClient = &http.Client{Timeout: 15 * time.Second}

// ── ATS endpoint URL builders ───────────────────────────────────────────────

func endpointURL(ats, slug string) string {
	switch ats {
	case "greenhouse":
		return fmt.Sprintf("https://boards-api.greenhouse.io/v1/boards/%s/jobs", slug)
	case "lever":
		return fmt.Sprintf("https://api.lever.co/v0/postings/%s?mode=json", slug)
	case "ashby":
		return "https://jobs.ashbyhq.com/api/non-user-graphql?op=ApiJobBoardWithTeams"
	case "workable":
		return fmt.Sprintf("https://apply.workable.com/api/v3/accounts/%s/jobs", slug)
	case "smartrecruiters":
		return fmt.Sprintf("https://api.smartrecruiters.com/v1/companies/%s/postings", slug)
	case "recruitee":
		return fmt.Sprintf("https://%s.recruitee.com/api/offers", slug)
	case "freshteam":
		return fmt.Sprintf("https://%s.freshteam.com/hire/widgets/jobs.json", slug)
	case "pinpoint":
		return fmt.Sprintf("https://%s.pinpointhq.com/postings.json", slug)
	case "teamtailor":
		return fmt.Sprintf("https://%s.teamtailor.com/jobs", slug)
	default:
		return ""
	}
}

func httpMethod(ats string) string {
	switch ats {
	case "ashby", "workable":
		return "POST"
	default:
		return "GET"
	}
}

func httpBody(ats, slug string) string {
	switch ats {
	case "ashby":
		return fmt.Sprintf(`{"operationName":"ApiJobBoardWithTeams","variables":{"organizationHostedJobsPageName":"%s"},"query":"query ApiJobBoardWithTeams($organizationHostedJobsPageName: String!) { jobBoardWithTeams(organizationHostedJobsPageName: $organizationHostedJobsPageName) { jobPostings { id title locationName employmentType } } }"}`, slug)
	case "workable":
		return `{"query":"","location":[],"department":[],"worktype":[]}`
	default:
		return ""
	}
}

// ── Spot-check: fetch a few jobs from a slug ────────────────────────────────

func spotCheckSlug(ats, slug string) (int, string, error) {
	ep := endpointURL(ats, slug)
	if ep == "" {
		return 0, "", fmt.Errorf("no endpoint for ATS %s", ats)
	}

	var req *http.Request
	if httpMethod(ats) == "POST" {
		req, _ = http.NewRequest("POST", ep, bytes.NewBufferString(httpBody(ats, slug)))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest("GET", ep, nil)
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Parse with jobparser if available, else count naively
	jobs, _, parseErr := jobparser.Parse(ats, body, ep, "")
	if parseErr == nil && len(jobs) > 0 {
		return len(jobs), jobs[0].Title, nil
	}

	// Fallback counting
	count := strings.Count(string(body), `"title"`)
	if count == 0 {
		count = strings.Count(string(body), `"id"`)
	}
	preview := ""
	if len(body) > 100 {
		preview = string(body[:100])
	}
	return count, preview, nil
}

// ── DB import ───────────────────────────────────────────────────────────────

func importToDB(ctx context.Context, db *pgxpool.Pool, ats string, slugs []string) (inserted int, skipped int) {
	for _, slug := range slugs {
		domain := atsprober.DomainForATS(ats, slug)
		ep := endpointURL(ats, slug)
		if ep == "" || domain == "" {
			skipped++
			continue
		}

		// 1. Upsert company
		var companyID string
		err := db.QueryRow(ctx, `
			INSERT INTO companies (domain, name, created_at, updated_at)
			VALUES ($1, $2, NOW(), NOW())
			ON CONFLICT (domain) DO UPDATE SET updated_at = NOW()
			RETURNING id
		`, domain, slug).Scan(&companyID)
		if err != nil {
			log.Printf("ERROR company upsert slug=%s: %v", slug, err)
			skipped++
			continue
		}

		// 2. Upsert data_source
		publicRef := fmt.Sprintf("wayback_%s_%s", ats, slug)
		_, err = db.Exec(ctx, `
			INSERT INTO data_sources (
				public_ref, company_id, discovery_method, discovery_tier,
				ats_platform, endpoint_url, endpoint_method, endpoint_body,
				status, confidence, created_at, updated_at
			) VALUES (
				$1, $2, 'wayback_cdx', 'tier0',
				$3, $4, $5, $6,
				'active', 0.85, NOW(), NOW()
			)
			ON CONFLICT (endpoint_url) DO UPDATE SET
				status = 'active',
				updated_at = NOW()
		`, publicRef, companyID, ats, ep, httpMethod(ats), httpBody(ats, slug))
		if err != nil {
			log.Printf("ERROR source upsert slug=%s: %v", slug, err)
			skipped++
			continue
		}

		inserted++
	}
	return
}

// ── Main ────────────────────────────────────────────────────────────────────

func main() {
	inputDir := flag.String("input-dir", ".", "Directory with verified_*_companies.txt files")
	outputDir := flag.String("output-dir", ".", "Output directory for jobs NDJSON files")
	workers := flag.Int("workers", 30, "Concurrent workers per ATS")
	atsFilter := flag.String("ats", "", "Only process this ATS")
	spotCheck := flag.Bool("spot-check", false, "Test 3 slugs per ATS (no DB needed)")
	doImport := flag.Bool("import", false, "Import into DB (needs DATABASE_URL)")
	doFetch := flag.Bool("fetch", false, "Also fetch jobs (implies --import)")
	flag.Parse()

	atsList := []string{"greenhouse", "lever", "ashby", "workable", "smartrecruiters",
		"recruitee", "freshteam", "pinpoint", "teamtailor"}

	// Reduce Workable rate to avoid 429s
	atsprober.SetRateLimit("workable", 2, 2)

	// DB connection (only if importing or fetching)
	var db *pgxpool.Pool
	if *doImport || *doFetch {
		dbURL := os.Getenv("DATABASE_URL")
		if dbURL == "" {
			dbURL = "postgres://careerscout:careerscout_dev_password@localhost:5432/careerscout?sslmode=disable"
		}
		var err error
		db, err = pgxpool.New(context.Background(), dbURL)
		if err != nil {
			log.Fatalf("DB connection failed: %v", err)
		}
		defer db.Close()
	}

	grandInserted := 0
	grandJobs := int64(0)

	for _, atsName := range atsList {
		if *atsFilter != "" && *atsFilter != atsName {
			continue
		}

		inputFile := filepath.Join(*inputDir, fmt.Sprintf("verified_%s_companies.txt", atsName))
		slugs, err := readLines(inputFile)
		if err != nil || len(slugs) == 0 {
			continue
		}

		fmt.Printf("\n%s\n %s — %d verified companies\n%s\n",
			strings.Repeat("=", 60), strings.ToUpper(atsName), len(slugs), strings.Repeat("=", 60))

		// ── Spot-check mode ──
		if *spotCheck {
			for i, slug := range slugs {
				if i >= 3 {
					break
				}
				_ = atsprober.Limiters[atsName].Wait(context.Background())
				count, preview, err := spotCheckSlug(atsName, slug)
				if err != nil {
					fmt.Printf("  ❌ %s: %v\n", slug, err)
				} else {
					fmt.Printf("  ✅ %s: %d jobs", slug, count)
					if preview != "" {
						fmt.Printf(" (%s)", truncate(preview, 50))
					}
					fmt.Println()
				}
			}
			continue
		}

		// ── Import mode ──
		if *doImport || *doFetch {
			inserted, skipped := importToDB(context.Background(), db, atsName, slugs)
			fmt.Printf("  📥 Imported: %d | Skipped: %d\n", inserted, skipped)
			grandInserted += inserted
		}

		// ── Fetch mode (also writes NDJSON) ──
		if *doFetch {
			outputFile := filepath.Join(*outputDir, fmt.Sprintf("jobs_%s.ndjson", atsName))
			jobCount := fetchAllJobs(atsName, slugs, outputFile, *workers)
			atomic.AddInt64(&grandJobs, int64(jobCount))
		}
	}

	fmt.Printf("\n%s\n COMPLETE\n%s\n", strings.Repeat("=", 60), strings.Repeat("=", 60))
	if *doImport {
		fmt.Printf("  Companies+Sources imported: %d\n", grandInserted)
	}
	if *doFetch {
		fmt.Printf("  Total jobs fetched: %d\n", grandJobs)
	}
	_ = sort.Strings
}

func fetchAllJobs(ats string, slugs []string, outputFile string, workers int) int {
	f, err := os.Create(outputFile)
	if err != nil {
		log.Printf("Cannot create %s: %v", outputFile, err)
		return 0
	}
	defer f.Close()

	var (
		totalJobs    int64
		totalSuccess int64
		totalFail    int64
		mu           sync.Mutex
	)
	writer := bufio.NewWriter(f)

	ch := make(chan string, len(slugs))
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for slug := range ch {
				_ = atsprober.Limiters[ats].Wait(context.Background())
				count, _, err := spotCheckSlug(ats, slug)
				if err != nil {
					atomic.AddInt64(&totalFail, 1)
					continue
				}
				atomic.AddInt64(&totalSuccess, 1)
				atomic.AddInt64(&totalJobs, int64(count))

				// Write a summary line
				mu.Lock()
				line, _ := json.Marshal(map[string]interface{}{
					"ats": ats, "slug": slug, "job_count": count,
				})
				writer.Write(line)
				writer.WriteByte('\n')
				mu.Unlock()
			}
		}()
	}

	for _, s := range slugs {
		ch <- s
	}
	close(ch)

	start := time.Now()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s := atomic.LoadInt64(&totalSuccess)
			fl := atomic.LoadInt64(&totalFail)
			j := atomic.LoadInt64(&totalJobs)
			done := s + fl
			if done >= int64(len(slugs)) {
				return
			}
			rate := float64(done) / time.Since(start).Seconds()
			fmt.Printf("    Progress: %d/%d (%.0f/s) — jobs=%d errors=%d\n",
				done, len(slugs), rate, j, fl)
		}
	}()

	wg.Wait()
	writer.Flush()

	fmt.Printf("  ✅ %d companies | ❌ %d errors | 📄 %d jobs → %s\n",
		totalSuccess, totalFail, totalJobs, outputFile)
	return int(totalJobs)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
		l := strings.TrimSpace(sc.Text())
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, sc.Err()
}
