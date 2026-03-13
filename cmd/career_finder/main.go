package main

import (
	"bufio"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ── Types ────────────────────────────────────────────────────────────────────

type Company struct {
	Name          string
	Domain        string
	EmployeeCount string
	Country       string
	Industry      string
	LinkedInURL   string
	City          string
}

type CareerResult struct {
	CompanyName   string
	Domain        string
	CareerURL     string
	ATSPlatform   string
	ATSSlug       string
	EmployeeCount string
	Country       string
}

type Checkpoint struct {
	Processed map[string]bool `json:"processed"`
}

// ── ATS Detection ────────────────────────────────────────────────────────────

type ATSPattern struct {
	Name    string
	Match   string
	SlugRe  *regexp.Regexp
}

var atsPatterns = []ATSPattern{
	{
		Name:  "greenhouse",
		Match: "greenhouse.io",
		SlugRe: regexp.MustCompile(`(?:boards\.greenhouse\.io|boards-api\.greenhouse\.io/v1/boards)/([a-zA-Z0-9_-]+)`),
	},
	{
		Name:  "lever",
		Match: "lever.co",
		SlugRe: regexp.MustCompile(`(?:jobs\.lever\.co|api\.lever\.co/v0/postings)/([a-zA-Z0-9_-]+)`),
	},
	{
		Name:  "workday",
		Match: "myworkdayjobs.com",
		SlugRe: regexp.MustCompile(`([a-zA-Z0-9_-]+)\.wd\d+\.myworkdayjobs\.com(?:/.*?/([a-zA-Z0-9_-]+))?`),
	},
	{
		Name:  "smartrecruiters",
		Match: "smartrecruiters.com",
		SlugRe: regexp.MustCompile(`(?:careers\.smartrecruiters\.com|api\.smartrecruiters\.com/v1/companies)/([a-zA-Z0-9_-]+)`),
	},
	{
		Name:  "ashby",
		Match: "ashbyhq.com",
		SlugRe: regexp.MustCompile(`(?:jobs\.ashbyhq\.com|api\.ashbyhq\.com/posting-api/job-board)/([a-zA-Z0-9_-]+)`),
	},
	{
		Name:  "recruitee",
		Match: "recruitee.com",
		SlugRe: regexp.MustCompile(`([a-zA-Z0-9_-]+)\.recruitee\.com`),
	},
	{
		Name:  "pinpoint",
		Match: "pinpointhq.com",
		SlugRe: regexp.MustCompile(`([a-zA-Z0-9_-]+)\.pinpointhq\.com`),
	},
	{
		Name:  "freshteam",
		Match: "freshteam.com",
		SlugRe: regexp.MustCompile(`([a-zA-Z0-9_-]+)\.freshteam\.com`),
	},
	{
		Name:  "rippling",
		Match: "rippling.com",
		SlugRe: regexp.MustCompile(`([a-zA-Z0-9_-]+)(?:-careers)?\.rippling\.com`),
	},
	{
		Name:  "teamtailor",
		Match: "teamtailor.com",
		SlugRe: regexp.MustCompile(`([a-zA-Z0-9_-]+)\.teamtailor\.com`),
	},
	{
		Name:  "jobvite",
		Match: "jobvite.com",
		SlugRe: regexp.MustCompile(`jobs\.jobvite\.com/(?:company/|.*?c=)([a-zA-Z0-9_-]+)`),
	},
	{
		Name:  "breezyhr",
		Match: "breezy.hr",
		SlugRe: regexp.MustCompile(`([a-zA-Z0-9_-]+)\.breezy\.hr`),
	},
	{
		Name:  "personio",
		Match: "personio.",
		SlugRe: regexp.MustCompile(`([a-zA-Z0-9_-]+)\.jobs\.personio\.(?:com|de)`),
	},
	{
		Name:  "workable",
		Match: "workable.com",
		SlugRe: regexp.MustCompile(`(?:apply\.workable\.com/|jobs\.workable\.com/)([a-zA-Z0-9_-]+)`),
	},
	{
		Name:  "bamboohr",
		Match: "bamboohr.com",
		SlugRe: regexp.MustCompile(`([a-zA-Z0-9_-]+)\.bamboohr\.com`),
	},
}

// Regex to extract all href and src attribute values from HTML
var hrefSrcRe = regexp.MustCompile(`(?i)(?:href|src|action)\s*=\s*["']([^"']+)["']`)

// Domains to skip entirely
var skipDomains = []string{
	"instagram.com", "facebook.com", "twitter.com", "linkedin.com",
	"youtube.com", "tiktok.com", "pinterest.com", "x.com",
	"reddit.com", "snapchat.com", "whatsapp.com",
}

func isSocialDomain(domain string) bool {
	d := strings.ToLower(domain)
	for _, s := range skipDomains {
		if strings.Contains(d, s) {
			return true
		}
	}
	return false
}

func detectATS(html string) (platform, slug string) {
	// Extract all href/src URLs from the HTML
	matches := hrefSrcRe.FindAllStringSubmatch(html, -1)
	var urls []string
	for _, m := range matches {
		if len(m) > 1 {
			urls = append(urls, m[1])
		}
	}

	// Also check for JavaScript redirects like window.location = "..."
	redirectRe := regexp.MustCompile(`(?i)(?:window\.location|location\.href)\s*=\s*["']([^"']+)["']`)
	for _, m := range redirectRe.FindAllStringSubmatch(html, -1) {
		if len(m) > 1 {
			urls = append(urls, m[1])
		}
	}

	// Check extracted URLs against ATS patterns
	for _, u := range urls {
		uLower := strings.ToLower(u)
		for _, p := range atsPatterns {
			if strings.Contains(uLower, strings.ToLower(p.Match)) {
				platform = p.Name
				if m := p.SlugRe.FindStringSubmatch(u); len(m) > 1 {
					slug = m[1]
					if p.Name == "workday" && len(m) > 2 && m[2] != "" {
						slug = m[1] + "|" + m[2]
					}
				}
				return
			}
		}
	}
	return "", ""
}

// ── Career Page Probing ──────────────────────────────────────────────────────

func careerURLPatterns(domain string) []string {
	// Strip protocol and trailing slashes
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimRight(domain, "/")

	return []string{
		"https://" + domain + "/careers",
		"https://" + domain + "/jobs",
		"https://" + domain + "/about/careers",
		"https://" + domain + "/company/careers",
		"https://" + domain + "/en/careers",
		"https://careers." + domain,
		"https://jobs." + domain,
	}
}

func probeCareerPage(client *http.Client, domain string) (careerURL, ats, slug string) {
	urls := careerURLPatterns(domain)
	for _, u := range urls {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; CareerScout/1.0)")
		req.Header.Set("Accept", "text/html,application/xhtml+xml")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == 200 {
			// Read up to 512 KB of HTML for ATS detection
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
			resp.Body.Close()

			html := string(bodyBytes)
			careerURL = u

			// Check for ATS
			ats, slug = detectATS(html)
			return
		}
		resp.Body.Close()
	}
	return "", "", ""
}

// ── CSV I/O ──────────────────────────────────────────────────────────────────

func readCompanies(path string) ([]Company, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[strings.TrimSpace(strings.ToLower(h))] = i
	}

	var companies []Company
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		getField := func(name string) string {
			if idx, ok := colIdx[name]; ok && idx < len(record) {
				return strings.TrimSpace(record[idx])
			}
			return ""
		}

		domain := getField("domain")
		if domain == "" {
			continue
		}

		companies = append(companies, Company{
			Name:          getField("name"),
			Domain:        domain,
			EmployeeCount: getField("employee_count"),
			Country:       getField("country"),
			Industry:      getField("industry"),
			LinkedInURL:   getField("linkedin_url"),
			City:          getField("city"),
		})
	}

	return companies, nil
}

// ── Checkpoint ───────────────────────────────────────────────────────────────

func loadCheckpoint(path string) map[string]bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]bool)
	}
	var cp Checkpoint
	if json.Unmarshal(b, &cp) != nil || cp.Processed == nil {
		return make(map[string]bool)
	}
	return cp.Processed
}

func saveCheckpoint(path string, m map[string]bool) {
	b, _ := json.Marshal(Checkpoint{Processed: m})
	os.WriteFile(path, b, 0644)
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	csvPath := "companies_filtered.csv"
	checkpointPath := "career_finder_checkpoint.json"
	careerOutPath := "career_pages.csv"
	atsOutPath := "new_ats_sources.csv"

	log.Println("Reading companies CSV...")
	companies, err := readCompanies(csvPath)
	if err != nil {
		log.Fatalf("Failed to read CSV: %v", err)
	}
	log.Printf("Loaded %d companies\n", len(companies))

	processed := loadCheckpoint(checkpointPath)

	var toProcess []Company
	for _, c := range companies {
		if !processed[c.Domain] {
			toProcess = append(toProcess, c)
		}
	}

	log.Printf("Total: %d | Already processed: %d | To process: %d\n",
		len(companies), len(processed), len(toProcess))

	if len(toProcess) == 0 {
		log.Println("All done!")
		return
	}

	// Apply LIMIT if set
	if limitStr := os.Getenv("LIMIT"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 && limit < len(toProcess) {
			log.Printf("LIMIT=%d set, processing only first %d companies", limit, limit)
			toProcess = toProcess[:limit]
		}
	}

	// Open output files in append mode
	careerF, err := os.OpenFile(careerOutPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal(err)
	}
	defer careerF.Close()

	atsF, err := os.OpenFile(atsOutPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal(err)
	}
	defer atsF.Close()

	// Write headers if files are empty
	careerInfo, _ := careerF.Stat()
	if careerInfo.Size() == 0 {
		careerF.WriteString("company_name,domain,career_url,ats_platform,ats_slug,employee_count,country\n")
	}
	atsInfo, _ := atsF.Stat()
	if atsInfo.Size() == 0 {
		atsF.WriteString("company_name,domain,career_url,ats_platform,ats_slug,employee_count,country\n")
	}

	careerW := bufio.NewWriter(careerF)
	defer careerW.Flush()
	atsW := bufio.NewWriter(atsF)
	defer atsW.Flush()

	// Work channel
	workCh := make(chan Company, len(toProcess))
	for _, c := range toProcess {
		workCh <- c
	}
	close(workCh)

	var (
		wg             sync.WaitGroup
		outMu          sync.Mutex
		chkMu          sync.Mutex
		totalChecked   int32
		careerFound    int32
		atsDetected    int32
		chkCounter     int32
	)

	// Worker pool — 200 workers
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			transport := &http.Transport{
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
				MaxIdleConnsPerHost: 2,
				DisableKeepAlives:   true,
			}
			// Configurable timeout via TIMEOUT_MS env var (default 3000)
			timeoutMs := 3000
			if t := os.Getenv("TIMEOUT_MS"); t != "" {
				if v, err := strconv.Atoi(t); err == nil && v > 0 {
					timeoutMs = v
				}
			}
			client := &http.Client{
				Timeout:   time.Duration(timeoutMs) * time.Millisecond,
				Transport: transport,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if len(via) >= 3 {
						return http.ErrUseLastResponse
					}
					return nil
				},
			}

			for company := range workCh {
				// Skip social media domains
				if isSocialDomain(company.Domain) {
					atomic.AddInt32(&totalChecked, 1)
					chkMu.Lock()
					processed[company.Domain] = true
					chkMu.Unlock()
					continue
				}

				careerURL, ats, slug := probeCareerPage(client, company.Domain)

				if careerURL != "" {
					atomic.AddInt32(&careerFound, 1)

					result := CareerResult{
						CompanyName:   company.Name,
						Domain:        company.Domain,
						CareerURL:     careerURL,
						ATSPlatform:   ats,
						ATSSlug:       slug,
						EmployeeCount: company.EmployeeCount,
						Country:       company.Country,
					}

					line := csvLine(result)

					outMu.Lock()
					careerW.WriteString(line)

					if ats != "" {
						atomic.AddInt32(&atsDetected, 1)
						atsW.WriteString(line)
					}
					outMu.Unlock()
				}

				atomic.AddInt32(&totalChecked, 1)
				c := atomic.AddInt32(&chkCounter, 1)

				if c >= 1000 {
					atomic.StoreInt32(&chkCounter, 0)
					chkMu.Lock()
					processed[company.Domain] = true
					saveCheckpoint(checkpointPath, processed)
					// Flush writers periodically
					outMu.Lock()
					careerW.Flush()
					atsW.Flush()
					outMu.Unlock()
					chkMu.Unlock()
				} else {
					chkMu.Lock()
					processed[company.Domain] = true
					chkMu.Unlock()
				}
			}
		}()
	}

	// Progress ticker
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		start := time.Now()
		for range ticker.C {
			tc := atomic.LoadInt32(&totalChecked)
			cf := atomic.LoadInt32(&careerFound)
			ad := atomic.LoadInt32(&atsDetected)
			elapsed := time.Since(start).Seconds()
			rate := float64(tc) / elapsed
			rem := len(toProcess) - int(tc)
			eta := 0.0
			if rate > 0 {
				eta = float64(rem) / rate / 60
			}

			log.Printf("[%s] Processed: %d/%d | Career Pages: %d | ATS Detected: %d | Rate: %.1f/s | ETA: %.1f min",
				time.Now().Format("15:04:05"), tc, len(toProcess), cf, ad, rate, eta)
		}
	}()

	wg.Wait()
	ticker.Stop()

	// Final checkpoint + flush
	chkMu.Lock()
	saveCheckpoint(checkpointPath, processed)
	chkMu.Unlock()

	outMu.Lock()
	careerW.Flush()
	atsW.Flush()
	outMu.Unlock()

	log.Printf("\n══════════════════════════════════════════")
	log.Printf("DONE! Processed: %d | Career Pages: %d | ATS Detected: %d",
		totalChecked, careerFound, atsDetected)
	log.Printf("══════════════════════════════════════════")
}

func csvLine(r CareerResult) string {
	return fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s\n",
		csvEscape(r.CompanyName),
		csvEscape(r.Domain),
		csvEscape(r.CareerURL),
		csvEscape(r.ATSPlatform),
		csvEscape(r.ATSSlug),
		csvEscape(r.EmployeeCount),
		csvEscape(r.Country))
}

func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	// Also escape URL-unsafe commas
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil && u.Scheme != "" {
		return s
	}
	return s
}
