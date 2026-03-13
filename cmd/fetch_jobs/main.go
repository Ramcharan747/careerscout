package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/careerscout/careerscout/internal/jobparser"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DataSource struct {
	ID          string
	CompanyID   string
	ATSPlatform string
	EndpointURL string
}

type FetchResult struct {
	SourceID    string
	CompanyID   string
	ATSPlatform string
	EndpointURL string
	Jobs        []jobparser.Job
	JobCount    int
	HTTPStatus  int
	DurationMS  int64
	Error       error
}

var userAgent = "Mozilla/5.0 (compatible; CareerScout/1.0)"
var workableMu sync.Mutex

func fetchSource(source DataSource) FetchResult {
	start := time.Now()
	res := FetchResult{
		SourceID:    source.ID,
		CompanyID:   source.CompanyID,
		ATSPlatform: source.ATSPlatform,
		EndpointURL: source.EndpointURL,
	}

	var req *http.Request
	var err error

	fetchURL := source.EndpointURL
	if source.ATSPlatform == "greenhouse" && !strings.Contains(fetchURL, "content=true") {
		if strings.Contains(fetchURL, "?") {
			fetchURL += "&content=true"
		} else {
			fetchURL += "?content=true"
		}
	}

	if source.ATSPlatform == "ashby" {
		parsed, _ := url.Parse(fetchURL)
		slug := strings.Split(parsed.Path, "/")[len(strings.Split(parsed.Path, "/"))-1]
		if slug == "" || slug == "jobs" {
			parts := strings.Split(parsed.Path, "/")
			if len(parts) >= 2 {
				slug = parts[len(parts)-2]
			}
		}

		payload := fmt.Sprintf(`{"operationName":"JobBoardWithTeams","variables":{"organizationHostedJobsPageName":"%s"},"query":"{ jobBoardWithTeams(organizationHostedJobsPageName: \"%s\") { jobPostings { id title locationName teamName employmentType isRemote jobUrl } } }"}`, slug, slug)
		req, err = http.NewRequest("POST", fetchURL, bytes.NewBuffer([]byte(payload)))
		req.Header.Set("Content-Type", "application/json")
	} else if source.ATSPlatform == "workable" {
		payload := `{"query":"","location":[],"department":[],"worktype":[],"remote":[]}`
		req, err = http.NewRequest("POST", fetchURL, bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest("GET", fetchURL, nil)
	}

	if err != nil {
		res.Error = err
		return res
	}

	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 10 * time.Second}
	
	if source.ATSPlatform == "workable" {
		workableMu.Lock()
		defer workableMu.Unlock()
		time.Sleep(3 * time.Second)
	}

	resp, err := client.Do(req)

	if err == nil && resp.StatusCode == 429 && source.ATSPlatform == "workable" {
		resp.Body.Close()
		log.Printf("429 Rate limit hit for %s, retrying in 30s...", source.EndpointURL)
		time.Sleep(30 * time.Second)
		payload := `{"query":"","location":[],"department":[],"worktype":[],"remote":[]}`
		req, _ = http.NewRequest("POST", fetchURL, bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		resp, err = client.Do(req)
	}

	res.DurationMS = time.Since(start).Milliseconds()

	if err != nil {
		res.Error = err
		return res
	}
	defer resp.Body.Close()

	res.HTTPStatus = resp.StatusCode
	if resp.StatusCode != 200 {
		res.Error = fmt.Errorf("non-200 status: %d", resp.StatusCode)
		return res
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		res.Error = err
		return res
	}

	jobs, _, err := jobparser.Parse(source.ATSPlatform, body, source.EndpointURL, os.Getenv("GEMINI_API_KEY"))
	if err != nil {
		res.Error = err
		return res
	}

	res.Jobs = jobs
	res.JobCount = len(jobs)
	return res
}

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	workerCount := 30
	if wc := os.Getenv("WORKER_COUNT"); wc != "" {
		if c, err := strconv.Atoi(wc); err == nil && c > 0 {
			workerCount = c
		}
	}

	atsFilter := os.Getenv("ATS_FILTER")
	limitStr := os.Getenv("LIMIT")
	dryRun := os.Getenv("DRY_RUN") == "1"

	ctx := context.Background()
	dbpool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v", err)
	}
	defer dbpool.Close()

	query := `SELECT id, company_id, ats_platform, endpoint_url FROM data_sources WHERE status = 'active'`
	var args []interface{}
	argCount := 1

	if atsFilter != "" {
		query += fmt.Sprintf(` AND ats_platform = $%d`, argCount)
		args = append(args, atsFilter)
		argCount++
	}

	query += ` ORDER BY next_fetch_at ASC`

	if limitStr != "" {
		query += fmt.Sprintf(` LIMIT $%d`, argCount)
		lim, _ := strconv.Atoi(limitStr)
		args = append(args, lim)
	}

	rows, err := dbpool.Query(ctx, query, args...)
	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}

	var sources []DataSource
	platformCounts := make(map[string]int)

	for rows.Next() {
		var src DataSource
		if err := rows.Scan(&src.ID, &src.CompanyID, &src.ATSPlatform, &src.EndpointURL); err != nil {
			log.Printf("Row scan error: %v", err)
			continue
		}
		sources = append(sources, src)
		platformCounts[src.ATSPlatform]++
	}
	rows.Close()

	fmt.Printf("Loaded %d sources (", len(sources))
	for p, c := range platformCounts {
		fmt.Printf("%s:%d ", p, c)
	}
	fmt.Println(")")

	if len(sources) == 0 {
		return
	}

	workCh := make(chan DataSource, len(sources))
	resCh := make(chan FetchResult, len(sources))

	for _, src := range sources {
		workCh <- src
	}
	close(workCh)

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for src := range workCh {
				resCh <- fetchSource(src)
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resCh)
	}()

	var (
		succeeded int32
		failed    int32
		jobsAdded int32
	)

	start := time.Now()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	go func() {
		for range ticker.C {
			s := atomic.LoadInt32(&succeeded)
			f := atomic.LoadInt32(&failed)
			j := atomic.LoadInt32(&jobsAdded)
			dur := time.Since(start).Seconds()
			rate := float64(s+f) / dur
			rem := float64(len(sources)-(int(s)+int(f))) / rate
			fmt.Printf("[%s] Progress: %d/%d sources | Jobs: %d total | Errors: %d | Rate: %.1f src/sec | ETA: %.1fmin\n",
				time.Now().Format("15:04:05"), s+f, len(sources), j, f, rate, rem/60)
		}
	}()

	for res := range resCh {
		if res.Error != nil {
			log.Printf("FAILED %s: %v", res.EndpointURL, res.Error)
			atomic.AddInt32(&failed, 1)

			if !dryRun {
				_, _ = dbpool.Exec(ctx, `
					UPDATE data_sources SET
						last_fetched_at      = NOW(),
						last_failure_at      = NOW(),
						fetch_count          = fetch_count + 1,
						failure_count        = failure_count + 1,
						consecutive_failures = consecutive_failures + 1,
						status               = CASE WHEN consecutive_failures + 1 >= 5 THEN 'degraded' ELSE status END,
						next_fetch_at        = NOW() + INTERVAL '1 hour',
						updated_at           = NOW()
					WHERE id = $1
				`, res.SourceID)

				errMsg := res.Error.Error()
				if len(errMsg) > 255 {
					errMsg = errMsg[:255]
				}
				_, _ = dbpool.Exec(ctx, `
					INSERT INTO fetch_logs 
						(source_id, fetched_at, duration_ms, http_status, jobs_found, jobs_new, success, error_message)
					VALUES ($1, NOW(), $2, $3, $4, 0, false, $5)
				`, res.SourceID, res.DurationMS, res.HTTPStatus, 0, errMsg)
			}
			continue
		}

		atomic.AddInt32(&succeeded, 1)
		atomic.AddInt32(&jobsAdded, int32(res.JobCount))

		if !dryRun {
			for _, job := range res.Jobs {
				_, err := dbpool.Exec(ctx, `
					INSERT INTO jobs (
						company_id, source_id, external_id,
						title, location_raw, city, country, country_code,
						is_remote, remote_type,
						department, employment_type, experience_level,
						apply_url, job_page_url,
						posted_at, description,
						first_seen_at, last_seen_at, fetched_at,
						is_active, data_quality, raw_json,
						created_at, updated_at
					) VALUES (
						$1, $2, $3,
						$4, $5, $6, $7, $8,
						$9, $10,
						$11, $12, $13,
						$14, $15,
						$16, $17,
						NOW(), NOW(), NOW(),
						true, 50, $18,
						NOW(), NOW()
					)
					ON CONFLICT (source_id, external_id) DO UPDATE SET
						title           = EXCLUDED.title,
						location_raw    = EXCLUDED.location_raw,
						city            = EXCLUDED.city,
						country         = EXCLUDED.country,
						country_code    = EXCLUDED.country_code,
						is_remote       = EXCLUDED.is_remote,
						department      = EXCLUDED.department,
						employment_type = EXCLUDED.employment_type,
						apply_url       = EXCLUDED.apply_url,
						last_seen_at    = NOW(),
						fetched_at      = NOW(),
						updated_at      = NOW()
				`,
					res.CompanyID, res.SourceID, job.ExternalID,
					job.Title, job.LocationRaw, job.City, job.Country, job.CountryCode,
					job.IsRemote, job.RemoteType,
					job.Department, job.EmploymentType, job.ExperienceLevel,
					job.ApplyURL, job.JobPageURL,
					job.PostedAt, job.Description,
					job.RawJSON,
				)
				if err != nil {
					log.Printf("Error inserting job %s: %v", job.ExternalID, err)
				}
			}

			_, _ = dbpool.Exec(ctx, `
				UPDATE data_sources SET
					last_fetched_at      = NOW(),
					last_success_at      = NOW(),
					last_job_count       = $1,
					fetch_count          = fetch_count + 1,
					success_count        = success_count + 1,
					consecutive_failures = 0,
					next_fetch_at        = NOW() + INTERVAL '6 hours',
					updated_at           = NOW()
				WHERE id = $2
			`, res.JobCount, res.SourceID)

			_, _ = dbpool.Exec(ctx, `
				INSERT INTO fetch_logs 
					(source_id, fetched_at, duration_ms, http_status, jobs_found, jobs_new, success, error_message)
				VALUES ($1, NOW(), $2, $3, $4, 0, true, NULL)
			`, res.SourceID, res.DurationMS, res.HTTPStatus, res.JobCount)
		}
	}

	fmt.Println("\n=== Fetch Complete ===")
	fmt.Printf("Sources processed: %d\n", len(sources))
	fmt.Printf("Sources succeeded: %d\n", succeeded)
	fmt.Printf("Sources failed:    %d\n", failed)
	fmt.Printf("Jobs recorded:     %d\n", jobsAdded)
	fmt.Printf("Duration:          %s\n", time.Since(start).Round(time.Second))
}
