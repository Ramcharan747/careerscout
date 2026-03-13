package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/careerscout/careerscout/internal/tier2_v3"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/jackc/pgx/v5/pgxpool"
)

var skipList = []string{
	"google-analytics.com", "segment.io", "hotjar.com", "intercom.io",
	"amplitude.com", "cookielaw.org", "onetrust.com", "sentry.io",
	"bugsnag.com", "fullstory.com", "mixpanel.com", "heap.io",
}

type Company struct {
	ID     string
	Domain string
}

func getEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func getEnv(key string, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

// isJunk returns true for requests that are definitely not job API calls.
func isJunk(urlL string) bool {
	junkPatterns := []string{
		"/analytics", "/tracking", "/beacon", "/pixel",
		"/metrics", "/logs", "/error", "/crash",
		".css", ".woff", ".png", ".jpg", ".svg",
		"google-analytics", "segment.io", "hotjar", "intercom",
		"/auth/token", "/oauth/token", "/login", "/logout",
		"/cdn-cgi", "/healthcheck", "/ping",
		"cloudflare", "akamai", "fastly",
	}
	for _, p := range junkPatterns {
		if strings.Contains(urlL, p) {
			return true
		}
	}

	if strings.HasSuffix(urlL, ".js") || strings.Contains(urlL, ".js?") || strings.Contains(urlL, ".js#") {
		return true
	}

	return false
}

func isSkipDomain(reqURL string) bool {
	for _, sd := range skipList {
		if strings.Contains(reqURL, sd) {
			return true
		}
	}
	return false
}

func isHTMLResponse(contentType string) bool {
	return !strings.Contains(strings.ToLower(contentType), "application/json")
}

func main() {
	runID := time.Now().UTC().Format("capture_20060102_150405")
	log.Printf("Starting capture run %s", runID)

	dbURL := getEnv("DATABASE_URL", "")
	if dbURL == "" {
		host := getEnv("PGHOST", "127.0.0.1")
		port := getEnv("PGPORT", "5432")
		user := getEnv("PGUSER", "careerscout")
		pass := getEnv("PGPASSWORD", "careerscout_dev_password")
		dbname := getEnv("PGDATABASE", "careerscout")
		dbURL = fmt.Sprintf("postgres://%s:%s@%s:%s/%s", user, pass, host, port, dbname)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db connect failed: %v", err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, "SELECT id, domain FROM companies")
	if err != nil {
		log.Fatalf("failed to query companies: %v", err)
	}
	var companies []Company
	for rows.Next() {
		var c Company
		if err := rows.Scan(&c.ID, &c.Domain); err != nil {
			log.Fatalf("scan failed: %v", err)
		}
		companies = append(companies, c)
	}
	rows.Close()
	log.Printf("Loaded %d domains to capture", len(companies))

	// Task 2 — Capture-specific noise filter (label=0)
	captureBlockedDomains := make(map[string]bool)
	blockRows, err := pool.Query(ctx, `
		SELECT DISTINCT 
			split_part(split_part(url, '://', 2), '/', 1) AS api_domain
		FROM raw_captures
		WHERE label = 0
		AND url NOT IN (
			SELECT url FROM raw_captures WHERE label = 1
		)
		GROUP BY api_domain
		HAVING COUNT(*) >= 3
		AND COUNT(CASE WHEN label = 1 THEN 1 END) = 0
	`)
	if err == nil {
		for blockRows.Next() {
			var d string
			if err := blockRows.Scan(&d); err == nil {
				captureBlockedDomains[d] = true
			}
		}
		blockRows.Close()
	} else {
		log.Printf("failed to load label=0 blocked domains: %v", err)
	}

	// Task 3 — Capture accept-fast-path from label=1 data
	captureAcceptDomains := make(map[string]bool)
	acceptRows, err := pool.Query(ctx, `
		SELECT DISTINCT
			split_part(split_part(url, '://', 2), '/', 1) AS api_domain
		FROM raw_captures  
		WHERE label = 1
		GROUP BY api_domain
		HAVING COUNT(DISTINCT domain) >= 2
	`)
	if err == nil {
		for acceptRows.Next() {
			var d string
			if err := acceptRows.Scan(&d); err == nil {
				captureAcceptDomains[d] = true
			}
		}
		acceptRows.Close()
	} else {
		log.Printf("failed to load label=1 accept domains: %v", err)
	}

	log.Printf("capture patcache: %d blocked domains, %d fast-accept domains", len(captureBlockedDomains), len(captureAcceptDomains))

	tabCount := getEnvInt("BROWSER_TABS", 6)
	workerCount := getEnvInt("WORKER_COUNT", 50)
	windowSec := getEnvInt("CAPTURE_WINDOW_SECONDS", 6)
	windowDuration := time.Duration(windowSec) * time.Second

	u := launcher.New().
		Headless(true).
		NoSandbox(true).
		Set("disable-gpu", "").
		Set("disable-dev-shm-usage", "").
		Set("disable-extensions", "").
		Set("disable-background-networking", "").
		Set("disable-default-apps", "").
		Set("disable-sync", "").
		Set("blink-settings", "imagesEnabled=false").
		MustLaunch()

	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.Close()

	tabPool := make(chan *rod.Page, tabCount)
	for i := 0; i < tabCount; i++ {
		tabPool <- browser.MustPage("")
	}

	classifier := tier2_v3.NewClassifier()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Interrupt received, shutting down...")
		cancel()
	}()

	var captureCount int64
	var processedCount int64
	domainCounts := make(map[string]int)
	var dcMu sync.Mutex

	workCh := make(chan Company, len(companies))
	for _, c := range companies {
		workCh <- c
	}
	close(workCh)

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for co := range workCh {
				if ctx.Err() != nil {
					return
				}

				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("⚠️ PANIC recovered in worker for %s: %v", co.Domain, r)
						}
					}()

					page := <-tabPool
					defer func() { tabPool <- page }()

					pCtx, pCancel := context.WithTimeout(ctx, windowDuration+5*time.Second)
					defer pCancel()
					page = page.Context(pCtx)

					_ = page.Navigate("about:blank")
					_, _ = page.Eval(`Object.defineProperty(navigator, 'webdriver', { get: () => undefined, configurable: true }); window.chrome = { runtime: {} };`)

					router := page.HijackRequests()
					err := router.Add("*", "", func(h *rod.Hijack) {
						reqType := string(h.Request.Type())
						reqURL := h.Request.URL().String()

						if reqType != "XHR" && reqType != "Fetch" {
							h.ContinueRequest(&proto.FetchContinueRequest{})
							return
						}

						// Extract API domain from request URL for Patcache checks
						var apiDomain string
						if parsed, err := url.Parse(reqURL); err == nil {
							apiDomain = parsed.Host
						}

						// Task 2: Noise Filter Fast-Reject
						if apiDomain != "" && captureBlockedDomains[apiDomain] {
							h.ContinueRequest(&proto.FetchContinueRequest{})
							return
						}

						// Execute standard blocklist/junk fast-reject checks
						if isJunk(reqURL) {
							h.ContinueRequest(&proto.FetchContinueRequest{})
							return
						}

						if isSkipDomain(reqURL) {
							h.ContinueRequest(&proto.FetchContinueRequest{})
							return
						}

						if err := h.LoadResponse(http.DefaultClient, true); err != nil {
							return
						}

						respHeadersPayload := h.Response.Payload().ResponseHeaders
						var contentType string
						for _, hdr := range respHeadersPayload {
							if strings.ToLower(hdr.Name) == "content-type" {
								contentType = hdr.Value
								break
							}
						}

						respBody := h.Response.Body()
						size := len([]byte(respBody))

						notes := "shape_unknown"
						isFastAccept := apiDomain != "" && captureAcceptDomains[apiDomain]

						if isFastAccept {
							notes = "fast_accept"
						} else {
							// For non-accepted domains, enforce normal checks
							if isHTMLResponse(contentType) {
								return
							}

							if size < 200 || size > 10*1024*1024 {
								return
							}

							_, detectedShape := classifier.ScoreResponseBody(reqURL, []byte(respBody))
							notes = detectedShape
						}

						// Store directly
						rawHeaders := h.Request.Headers()
						hdrsBytes, _ := json.Marshal(rawHeaders)

						_, insertErr := pool.Exec(context.Background(), `
							INSERT INTO raw_captures
							(run_id, domain, url, method, request_headers, response_status, response_content_type, response_body, response_size, notes)
							VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
						`, runID, co.Domain, reqURL, h.Request.Method(), hdrsBytes, h.Response.Payload().ResponseCode, contentType, respBody, size, notes)

						if insertErr == nil {
							atomic.AddInt64(&captureCount, 1)
							dcMu.Lock()
							domainCounts[co.Domain]++
							dcMu.Unlock()
						} else {
							log.Printf("ERROR inserting capture for %s: %v", reqURL, insertErr)
						}
					})
					if err == nil {
						go router.Run()
						defer router.Stop()
					}

					url := "https://" + co.Domain + "/careers"
					_ = page.Navigate(url)
					time.Sleep(windowDuration) // Wait the full window to capture all requests
				}()

				atomic.AddInt64(&processedCount, 1)
			}
		}()
	}

	wg.Wait()

	totalCaptures := atomic.LoadInt64(&captureCount)
	totalProcessed := atomic.LoadInt64(&processedCount)

	log.Println("\n=== CAPTURE RUN COMPLETE ===")
	log.Printf("Run ID: %s", runID)
	log.Printf("Domains processed: %d", totalProcessed)
	log.Printf("Raw captures stored: %d", totalCaptures)

	if totalProcessed > 0 {
		log.Printf("Average captures per domain: %.2f", float64(totalCaptures)/float64(totalProcessed))
	}

	type domCount struct {
		name  string
		count int
	}
	var top []domCount
	dcMu.Lock()
	for d, c := range domainCounts {
		top = append(top, domCount{d, c})
	}
	dcMu.Unlock()

	sort.Slice(top, func(i, j int) bool {
		return top[i].count > top[j].count
	})

	log.Println("\nTop 10 domains by capture count:")
	limit := 10
	if len(top) < limit {
		limit = len(top)
	}
	for i := 0; i < limit; i++ {
		log.Printf("  %s: %d", top[i].name, top[i].count)
	}
}
