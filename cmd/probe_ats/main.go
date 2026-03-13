package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/careerscout/careerscout/internal/atsprober"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

// ProbeResult holds the result of probing a single slug against one ATS platform.
type ProbeResult struct {
	Slug       string `json:"slug"`
	ATSName    string `json:"ats_name"`
	APIURL     string `json:"api_url"`
	Domain     string `json:"domain"`
	JobCount   int    `json:"job_count"`
	Confirmed  bool   `json:"confirmed"`
	StatusCode int    `json:"status_code"`
	BodySize   int    `json:"body_size"`
}

var (
	httpClient = &http.Client{Timeout: 8 * time.Second}
	userAgent  = "Mozilla/5.0 (compatible; CareerScout/1.0; +https://careerscout.io/bot)"

	// Per-ATS rate limiters — 5 req/sec each
	limiters = map[string]*rate.Limiter{
		"greenhouse": rate.NewLimiter(rate.Limit(5), 5),
		"lever":      rate.NewLimiter(rate.Limit(5), 5),
		"ashby":      rate.NewLimiter(rate.Limit(5), 5),
		"workable":   rate.NewLimiter(rate.Limit(5), 5),
		"bamboohr":   rate.NewLimiter(rate.Limit(5), 5),
		"recruitee":  rate.NewLimiter(rate.Limit(5), 5),
		"teamtailor": rate.NewLimiter(rate.Limit(5), 5),
		"rippling":   rate.NewLimiter(rate.Limit(5), 5),
		"pinpoint":   rate.NewLimiter(rate.Limit(5), 5),
		"freshteam":  rate.NewLimiter(rate.Limit(5), 5),
		"jobvite":    rate.NewLimiter(rate.Limit(5), 5),
		"breezyhr":   rate.NewLimiter(rate.Limit(5), 5),
		"personio":   rate.NewLimiter(rate.Limit(5), 5),
	}
)

// domainForATS returns the canonical domain for a confirmed ATS hit.
func domainForATS(ats, slug string) string {
	switch ats {
	case "greenhouse":
		return slug + ".greenhouse.io"
	case "lever":
		return "jobs.lever.co/" + slug
	case "ashby":
		return "jobs.ashbyhq.com/" + slug
	case "workable":
		return slug + ".workable.com"
	case "bamboohr":
		return slug + ".bamboohr.com"
	case "recruitee":
		return slug + ".recruitee.com"
	case "teamtailor":
		return slug + ".teamtailor.com"
	case "pinpoint":
		return slug + ".pinpointhq.com"
	case "freshteam":
		return slug + ".freshteam.com"
	case "rippling":
		return slug + "-careers.rippling.com"
	case "smartrecruiters":
		return "smrtr.io/" + slug
	case "jobvite":
		return "jobs.jobvite.com/company/" + slug
	case "breezyhr":
		return slug + ".breezy.hr"
	case "personio":
		return slug + ".jobs.personio.com"
	default:
		return slug
	}
}

// doReq executes an HTTP request and returns body bytes and status code.
func doReq(req *http.Request) ([]byte, int, string) {
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	ct := resp.Header.Get("Content-Type")
	return body, resp.StatusCode, ct
}

// isJSON returns true if the content type indicates JSON.
func isJSON(ct string) bool {
	return strings.Contains(ct, "application/json") || strings.Contains(ct, "text/javascript")
}

// ── Probers ──────────────────────────────────────────────────────────────────

func probeGreenhouse(slug string) ProbeResult {
	_ = limiters["greenhouse"].Wait(context.Background())
	u := fmt.Sprintf("https://boards-api.greenhouse.io/v1/boards/%s/jobs", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "greenhouse", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && len(body) > 100 && isJSON(ct) {
		var data struct {
			Jobs []json.RawMessage `json:"jobs"`
		}
		if json.Unmarshal(body, &data) == nil && strings.Contains(string(body), `"jobs"`) {
			res.Confirmed = true
			res.JobCount = len(data.Jobs)
			res.Domain = domainForATS("greenhouse", slug)
		}
	}
	return res
}

func probeLever(slug string) ProbeResult {
	_ = limiters["lever"].Wait(context.Background())
	u := fmt.Sprintf("https://api.lever.co/v0/postings/%s?mode=json&limit=5", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "lever", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && len(body) > 2 && isJSON(ct) {
		var arr []map[string]interface{}
		if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
			if _, ok := arr[0]["text"]; ok {
				res.Confirmed = true
				res.JobCount = len(arr)
				res.Domain = domainForATS("lever", slug)
			}
		}
	}
	return res
}

func probeAshby(slug string) ProbeResult {
	_ = limiters["ashby"].Wait(context.Background())
	u := "https://jobs.ashbyhq.com/api/non-user-graphql?op=ApiJobBoardWithTeams"
	// Use confirmed working field: jobBoardWithTeams (verified via introspection 2026-03-10)
	payload := fmt.Sprintf(`{"operationName":"ApiJobBoardWithTeams","variables":{"organizationHostedJobsPageName":"%s"},"query":"query ApiJobBoardWithTeams($organizationHostedJobsPageName: String!) { jobBoardWithTeams(organizationHostedJobsPageName: $organizationHostedJobsPageName) { teams { name } jobPostings { id title } } }"}`, slug)
	req, _ := http.NewRequest("POST", u, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	body, code, _ := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "ashby", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && len(body) > 0 {
		var resp struct {
			Data struct {
				JobBoardWithTeams *struct {
					Teams       []interface{} `json:"teams"`
					JobPostings []interface{} `json:"jobPostings"`
				} `json:"jobBoardWithTeams"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.Data.JobBoardWithTeams != nil {
			res.Confirmed = true
			res.JobCount = len(resp.Data.JobBoardWithTeams.JobPostings)
			res.Domain = domainForATS("ashby", slug)
		}
	}
	return res
}

func probeWorkable(slug string) ProbeResult {
	_ = limiters["workable"].Wait(context.Background())
	u := fmt.Sprintf("https://apply.workable.com/api/v3/accounts/%s/jobs", slug)
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Content-Type", "application/json")
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "workable", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && len(body) > 0 && isJSON(ct) {
		var obj map[string]interface{}
		if err := json.Unmarshal(body, &obj); err == nil {
			if results, ok := obj["results"]; ok {
				if arr, ok := results.([]interface{}); ok {
					res.Confirmed = true
					res.JobCount = len(arr)
					res.Domain = domainForATS("workable", slug)
				}
			}
		}
	}
	return res
}

func probeBambooHR(slug string) ProbeResult {
	_ = limiters["bamboohr"].Wait(context.Background())
	u := fmt.Sprintf("https://%s.bamboohr.com/jobs/embed2.php", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, _ := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "bamboohr", APIURL: u, StatusCode: code, BodySize: len(body)}
	bodyStr := string(body)
	if code == 200 && strings.Contains(bodyStr, `"jobOpenings"`) {
		res.Confirmed = true
		res.Domain = domainForATS("bamboohr", slug)
	}
	return res
}

func probeRecruitee(slug string) ProbeResult {
	_ = limiters["recruitee"].Wait(context.Background())
	u := fmt.Sprintf("https://%s.recruitee.com/api/offers", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "recruitee", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && isJSON(ct) && strings.Contains(string(body), `"offers"`) {
		res.Confirmed = true
		res.Domain = domainForATS("recruitee", slug)
	}
	return res
}

func probeTeamtailor(slug string) ProbeResult {
	_ = limiters["teamtailor"].Wait(context.Background())
	// Teamtailor uses company subdomain not query param
	u := fmt.Sprintf("https://jobs.teamtailor.com/companies/%s/jobs.json", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "teamtailor", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && isJSON(ct) && strings.Contains(string(body), `"data"`) && len(body) > 50 {
		var obj map[string]interface{}
		if err := json.Unmarshal(body, &obj); err == nil {
			if data, ok := obj["data"].([]interface{}); ok && len(data) > 0 {
				res.Confirmed = true
				res.JobCount = len(data)
				res.Domain = domainForATS("teamtailor", slug)
			}
		}
	}
	return res
}

func probeRippling(slug string) ProbeResult {
	_ = limiters["rippling"].Wait(context.Background())
	u := fmt.Sprintf("https://api.rippling.com/platform/api/ats/v1/board/%s-careers/jobs", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "rippling", APIURL: u, StatusCode: code, BodySize: len(body)}
	bodyStr := string(body)
	if code == 200 && isJSON(ct) &&
		strings.Contains(bodyStr, `"id"`) &&
		strings.Contains(bodyStr, `"name"`) &&
		strings.Contains(bodyStr, `"department"`) &&
		len(body) > 200 {
		res.Confirmed = true
		res.Domain = domainForATS("rippling", slug)
	}
	return res
}

func probePinpoint(slug string) ProbeResult {
	_ = limiters["pinpoint"].Wait(context.Background())
	u := fmt.Sprintf("https://%s.pinpointhq.com/postings.json", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "pinpoint", APIURL: u, StatusCode: code, BodySize: len(body)}
	bodyStr := string(body)

	// Hard reject HTML — Pinpoint returns marketing page for non-existent subdomains
	if strings.Contains(bodyStr, "<html") || strings.Contains(bodyStr, "<!DOCTYPE") {
		return res
	}
	// Require strict JSON + Pinpoint-specific job fields including employment_type
	if code == 200 && isJSON(ct) && len(body) > 300 &&
		strings.Contains(bodyStr, `"data"`) &&
		strings.Contains(bodyStr, `"title"`) &&
		strings.Contains(bodyStr, `"location"`) &&
		strings.Contains(bodyStr, `"employment_type"`) {
		var obj map[string]interface{}
		if err := json.Unmarshal(body, &obj); err == nil {
			if data, ok := obj["data"].([]interface{}); ok && len(data) > 0 {
				res.Confirmed = true
				res.JobCount = len(data)
				res.Domain = domainForATS("pinpoint", slug)
			}
		}
	}
	return res
}

func probeFreshteam(slug string) ProbeResult {
	_ = limiters["freshteam"].Wait(context.Background())
	u := fmt.Sprintf("https://%s.freshteam.com/hire/widgets/jobs.json", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "freshteam", APIURL: u, StatusCode: code, BodySize: len(body)}
	bodyStr := string(body)
	// Require JSON + minimum size + Freshteam-specific fields including "remote"
	if code == 200 && isJSON(ct) && len(body) > 500 &&
		strings.Contains(bodyStr, `"title"`) &&
		strings.Contains(bodyStr, `"id"`) &&
		strings.Contains(bodyStr, `"remote"`) {
		res.Confirmed = true
		res.Domain = domainForATS("freshteam", slug)
	}
	return res
}

func probeSmartRecruiters(slug string) ProbeResult {
	_ = limiters["smartrecruiters"].Wait(context.Background())
	u := fmt.Sprintf("https://api.smartrecruiters.com/v1/companies/%s/postings", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "smartrecruiters", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && isJSON(ct) && len(body) > 50 {
		var result struct {
			Content []json.RawMessage `json:"content"`
			TotalFound int `json:"totalFound"`
		}
		if json.Unmarshal(body, &result) == nil && result.TotalFound > 0 {
			res.Confirmed = true
			res.JobCount = result.TotalFound
			res.Domain = domainForATS("smartrecruiters", slug)
		}
	}
	return res
}

func probeJobvite(slug string) ProbeResult {
	_ = limiters["jobvite"].Wait(context.Background())
	u := fmt.Sprintf("https://jobs.jobvite.com/api/job?c=%s", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "jobvite", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && isJSON(ct) {
		var result struct {
			ReqList []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"reqList"`
			TotalCount int `json:"totalCount"`
		}
		if json.Unmarshal(body, &result) == nil {
			if result.TotalCount > 0 || len(result.ReqList) > 0 {
				res.Confirmed = true
				res.JobCount = len(result.ReqList)
				res.Domain = domainForATS("jobvite", slug)
			}
		}
	}
	return res
}

func probeBreezyHR(slug string) ProbeResult {
	_ = limiters["breezyhr"].Wait(context.Background())
	apiURL := fmt.Sprintf("https://api.breezy.hr/v3/company/%s/positions?state=published", slug)
	req, _ := http.NewRequest("GET", apiURL, nil)
	body, code, ct := doReq(req)

	res := ProbeResult{Slug: slug, ATSName: "breezyhr", APIURL: apiURL, StatusCode: code, BodySize: len(body)}
	if code == 200 && isJSON(ct) {
		var positions []struct {
			ID    string `json:"_id"`
			Name  string `json:"name"`
			State string `json:"state"`
		}
		if json.Unmarshal(body, &positions) == nil && len(positions) > 0 {
			res.Confirmed = true
			res.JobCount = len(positions)
			res.Domain = domainForATS("breezyhr", slug)
		}
	}
	return res
}

func probePersonio(slug string) ProbeResult {
	_ = limiters["personio"].Wait(context.Background())
	u := fmt.Sprintf("https://%s.jobs.personio.com/api/v1/jobs", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := doReq(req)

	if code != 200 {
		u = fmt.Sprintf("https://%s.jobs.personio.de/api/v1/jobs", slug)
		req, _ = http.NewRequest("GET", u, nil)
		body, code, ct = doReq(req)
	}

	res := ProbeResult{Slug: slug, ATSName: "personio", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && isJSON(ct) && len(body) > 50 {
		var jobs []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(body, &jobs) == nil && len(jobs) > 0 {
			res.Confirmed = true
			res.JobCount = len(jobs)
			res.Domain = domainForATS("personio", slug)
		}
	}
	return res
}

func isValidSlug(slug string) bool {
	// Must be at least 3 chars
	if len(slug) < 3 || len(slug) > 50 {
		return false
	}
	// Drop all-digit slugs like 00035116
	allDigit := true
	for _, c := range slug {
		if c < '0' || c > '9' {
			allDigit = false
			break
		}
	}
	if allDigit {
		return false
	}
	// Drop mostly-numeric slugs (>60% digits)
	digitCount := 0
	for _, c := range slug {
		if c >= '0' && c <= '9' {
			digitCount++
		}
	}
	if float64(digitCount)/float64(len(slug)) > 0.6 {
		return false
	}
	// Drop IDN encoded domains
	if strings.HasPrefix(slug, "xn--") {
		return false
	}
	// Must start with a letter
	if slug[0] < 'a' || slug[0] > 'z' {
		return false
	}
	// Must match clean slug pattern
	for _, c := range slug {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

func buildSlugs() []string {
	sources := []string{
		"https://raw.githubusercontent.com/datasets/s-and-p-500-companies/master/data/constituents.csv",
		"https://raw.githubusercontent.com/namelist/fortune500/master/fortune500.csv",
		"https://downloads.majestic.com/majestic_million.csv",
	}

	skipExact := map[string]bool{
		// Big tech / social
		"google": true, "facebook": true, "youtube": true, "youtu": true,
		"instagram": true, "twitter": true, "linkedin": true, "tiktok": true,
		"whatsapp": true, "snapchat": true, "pinterest": true, "reddit": true,
		"netflix": true, "spotify": true, "apple": true, "microsoft": true,
		"amazon": true, "adobe": true, "mozilla": true, "apache": true,
		// Generic subdomains that are not companies
		"www": true, "mail": true, "api": true, "app": true, "apps": true,
		"play": true, "maps": true, "docs": true, "drive": true, "support": true,
		"help": true, "blog": true, "shop": true, "store": true, "news": true,
		"forum": true, "static": true, "cdn": true, "media": true, "images": true,
		"player": true, "plus": true, "go": true, "goo": true, "bit": true,
		"my": true, "the": true, "web": true, "site": true, "online": true,
		// CDN / infra
		"amazonaws": true, "cloudfront": true, "akamai": true, "fastly": true,
		"cloudflare": true, "googletagmanager": true, "doubleclick": true,
		"googleapis": true, "gstatic": true, "fbcdn": true, "jsdelivr": true,
		"unpkg": true, "cdnjs": true, "nginx": true,
		// Generic words
		"policies": true, "europa": true, "mailinabox": true, "vimeo": true,
		// Explicit legacy carryovers
		"xn": true, "free": true, "best": true, "top": true,
		"wikipedia": true, "wordpress": true, "blogspot": true, "tumblr": true,
		"github": true, "yahoo": true, "itunes": true, "gravatar": true,
		"jobvite": true, "breezyhr": true, "personio": true, "smartrecruiters": true,
	}

	slugSet := make(map[string]bool)
	var counts = map[string]int{
		"S&P500":           0,
		"Fortune500":       0,
		"Majestic Million": 0,
		"careers_urls":     0,
	}

	reSpaces := regexp.MustCompile(`[\s_]+`)
	reSpecs := regexp.MustCompile(`[^a-z0-9\-]`)
	suffixes := []string{" inc", " corp", " ltd", " llc", " co", " group", " holdings", " company", " limited"}

	cleanSlug := func(name string) string {
		name = strings.ToLower(strings.TrimSpace(name))
		for _, suf := range suffixes {
			name = strings.TrimSuffix(name, suf)
			name = strings.TrimSuffix(name, suf+".")
			name = strings.TrimSuffix(name, suf+",")
		}
		name = reSpaces.ReplaceAllString(name, "-")
		name = reSpecs.ReplaceAllString(name, "")
		return strings.Trim(name, "-")
	}

	for _, src := range sources {
		resp, err := http.Get(src)
		if err != nil || resp.StatusCode != 200 {
			log.Printf("WARN: failed to fetch %s", src)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		isMajestic := strings.Contains(src, "majestic_million")
		isSP := strings.Contains(src, "s-and-p-500")
		isFortune := strings.Contains(src, "fortune500")

		lines := strings.Split(string(body), "\n")
		sourceCounts := 0

		for i, line := range lines {
			if len(strings.TrimSpace(line)) > 3 {
				if isMajestic {
					if i == 0 {
						continue // skip CSV header
					}
					parts := strings.Split(line, ",")
					if len(parts) > 3 {
						domain := parts[2]
						tld := strings.TrimSpace(parts[3])
						if strings.HasSuffix(tld, "gov") || strings.HasSuffix(tld, "edu") || strings.HasSuffix(tld, "mil") {
							continue
						}
						slug := strings.Split(domain, ".")[0]
						slug = strings.ToLower(slug)
						if skipExact[slug] {
							continue
						}
						if isValidSlug(slug) {
							if !slugSet[slug] {
								slugSet[slug] = true
								sourceCounts++
							}
						}
					}
				} else {
					for _, part := range strings.Split(line, ",") {
						sl := cleanSlug(part)
						if skipExact[sl] {
							continue
						}
						if isValidSlug(sl) {
							if !slugSet[sl] {
								slugSet[sl] = true
								sourceCounts++
							}
						}
						if strings.Contains(sl, "-") {
							v1 := strings.ReplaceAll(sl, "-", "")
							if !skipExact[v1] && isValidSlug(v1) {
								if !slugSet[v1] {
									slugSet[v1] = true
									sourceCounts++
								}
							}
							v2 := strings.Split(sl, "-")[0]
							if !skipExact[v2] && isValidSlug(v2) {
								if !slugSet[v2] {
									slugSet[v2] = true
									sourceCounts++
								}
							}
						}
					}
				}
			}
		}

		if isMajestic {
			counts["Majestic Million"] = sourceCounts
		} else if isSP {
			counts["S&P500"] = sourceCounts
		} else if isFortune {
			counts["Fortune500"] = sourceCounts
		}
	}

	careersCount := 0
	if data, err := os.ReadFile("careers_urls.json"); err == nil {
		var urls []string
		if json.Unmarshal(data, &urls) == nil {
			for _, part := range urls {
				sl := cleanSlug(part)
				if skipExact[sl] {
					continue
				}
				if isValidSlug(sl) {
					if !slugSet[sl] {
						slugSet[sl] = true
						careersCount++
					}
				}
			}
		}
	}
	counts["careers_urls"] = careersCount

	fmt.Printf("S&P500:          %d slugs\n", counts["S&P500"])
	fmt.Printf("Fortune500:      %d slugs\n", counts["Fortune500"])
	fmt.Printf("Majestic Million: %d slugs\n", counts["Majestic Million"])
	fmt.Printf("careers_urls:    %d slugs\n", counts["careers_urls"])
	fmt.Printf("Total unique:    %d slugs\n", len(slugSet))

	out := make([]string, 0, len(slugSet))
	for s := range slugSet {
		out = append(out, s)
	}
	return out
}

// ── Slug DB Sync & Analysis ──────────────────────────────────────────────────────────────────

func UpsertDataSource(ctx context.Context, tx pgx.Tx, res ProbeResult, companyID string) error {
	newUUID := uuid.New().String()
	publicRef := "src_" + strings.ReplaceAll(newUUID, "-", "")
	platform := atsprober.ATSPlatformFromURL(res.APIURL)

	_, err := tx.Exec(ctx, `
		INSERT INTO data_sources
			(public_ref, company_id, discovery_method, discovery_tier, ats_platform, 
			 endpoint_url, status, confidence, created_at, updated_at)
		VALUES ($1, $2, 'ats_probe', 'tier0', $3, $4, 'active', 0.95, NOW(), NOW())
		ON CONFLICT (endpoint_url) DO UPDATE SET 
			status='active', 
			updated_at=NOW()
	`, publicRef, companyID, platform, res.APIURL)
	return err
}

func saveDiscovery(ctx context.Context, db *pgxpool.Pool, res ProbeResult) {
	if db == nil || res.Domain == "" {
		return
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		log.Printf("ERROR: failed to begin transaction: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	// Step 1 — upsert company row
	var companyID string
	err = tx.QueryRow(ctx, `
		INSERT INTO companies (domain, name, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (domain) DO UPDATE SET updated_at = NOW()
		RETURNING id
	`, res.Domain, res.Slug).Scan(&companyID)
	if err != nil {
		log.Printf("ERROR: upsert company failed slug=%s ats=%s: %v", res.Slug, res.ATSName, err)
		return
	}

	// Step 2 — upsert legacy discovery record
	_, err = tx.Exec(ctx, `
		INSERT INTO discovery_records
			(company_id, domain, api_url, http_method, tier_used, status, confidence, discovered_at, created_at, updated_at)
		VALUES ($1, $2, $3, 'GET', 'tier0', 'discovered', 0.95, NOW(), NOW(), NOW())
		ON CONFLICT (domain) DO UPDATE SET
			api_url     = EXCLUDED.api_url,
			status      = 'discovered',
			confidence  = 0.95,
			updated_at  = NOW()
		WHERE discovery_records.status != 'discovered'
	`, companyID, res.Domain, res.APIURL)
	if err != nil {
		log.Printf("ERROR: legacy upsert discovery failed slug=%s ats=%s domain=%s: %v", res.Slug, res.ATSName, res.Domain, err)
		return
	}

	// Step 3 — upsert structured data_sources architecture record
	err = UpsertDataSource(ctx, tx, res, companyID)
	if err != nil {
		log.Printf("ERROR: upsert data_source failed slug=%s ats=%s domain=%s: %v", res.Slug, res.ATSName, res.Domain, err)
		return
	}

	err = tx.Commit(ctx)
	if err != nil {
		log.Printf("ERROR: transaction commit failed: %v", err)
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	testSlugs := []string{
		"stripe", "airbnb", "notion", "figma", "linear", "vercel", "github",
		"shopify", "twilio", "datadog", "confluent", "hashicorp", "mongodb",
		"elastic", "cloudflare", "discord", "dropbox", "intercom", "zendesk", "hubspot",
	}

	testRun := os.Getenv("PROBE_TEST") == "1"

	var allSlugs []string
	if testRun {
		allSlugs = testSlugs
		fmt.Printf("Startup: Running PROBE_TEST=1 with %d explicit slugs.\n", len(allSlugs))
	} else {
		allSlugs = buildSlugs()
		fmt.Printf("Startup: Merged and built %d total slugs from datasets.\n", len(allSlugs))
	}

	// Load checkpoint
	checkpoint := make(map[string]bool)
	var chkMu sync.RWMutex
	if data, err := os.ReadFile("probe_checkpoint.json"); err == nil {
		json.Unmarshal(data, &checkpoint)
		fmt.Printf("Resumed: skipping %d already-processed slugs.\n", len(checkpoint))
	}

	// Open results file
	resFile, err := os.OpenFile("probe_results.json", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal(err)
	}
	defer resFile.Close()
	var resFileMu sync.Mutex

	// Connect to database (skip in test mode)
	var dbpool *pgxpool.Pool
	if !testRun {
		dbURL := os.Getenv("DATABASE_URL")
		if dbURL == "" {
			dbURL = "postgres://careerscout:careerscout_dev_password@127.0.0.1:5432/careerscout"
		}
		dbpool, err = pgxpool.New(context.Background(), dbURL)
		if err != nil {
			log.Printf("WARN: database connection failed: %v — results will not be saved to DB", err)
		} else {
			defer dbpool.Close()
			fmt.Println("Database connected.")
		}
	}

	// Build work queue — count total before closing channel
	slugCh := make(chan string, len(allSlugs))
	totalStart := 0
	for _, s := range allSlugs {
		chkMu.RLock()
		seen := checkpoint[s]
		chkMu.RUnlock()
		if !seen {
			slugCh <- s
			totalStart++
		}
	}
	close(slugCh)

	if totalStart == 0 {
		fmt.Println("All slugs already processed. Delete probe_checkpoint.json to restart.")
		return
	}
	fmt.Printf("Queued %d slugs to probe.\n", totalStart)

	probers := []func(string) ProbeResult{
		probeGreenhouse, probeLever, probeAshby, probeWorkable, probeBambooHR,
		probeRecruitee, probeTeamtailor, probeRippling, probePinpoint, probeFreshteam,
		probeSmartRecruiters, probeJobvite, probeBreezyHR, probePersonio,
	}
	var (
		wg        sync.WaitGroup
		processed int32
		found     int32
	)

	atsCounts := struct {
		sync.Mutex
		m map[string]int
	}{m: make(map[string]int)}

	startTime := time.Now()

	// Progress monitor goroutine
	go func() {
		for {
			time.Sleep(10 * time.Second)
			p := atomic.LoadInt32(&processed)
			f := atomic.LoadInt32(&found)
			if p == 0 {
				continue
			}

			elapsed := time.Since(startTime).Minutes()
			ratePerMin := float64(p) / elapsed
			remaining := float64(int32(totalStart) - p)
			etaHours := (remaining / ratePerMin) / 60

			atsCounts.Lock()
			gh := atsCounts.m["greenhouse"]
			lv := atsCounts.m["lever"]
			ab := atsCounts.m["ashby"]
			wk := atsCounts.m["workable"]
			bh := atsCounts.m["bamboohr"]
			atsCounts.Unlock()

			fmt.Printf("Progress: %d/%d slugs | Found: %d (GH:%d LV:%d AB:%d WK:%d BH:%d) | Rate: %.0f/min | ETA: %.1fh\n",
				p, totalStart, f, gh, lv, ab, wk, bh, ratePerMin, etaHours)
		}
	}()

	// Checkpoint flush goroutine — writes every 30 seconds, not on every slug
	go func() {
		for {
			time.Sleep(30 * time.Second)
			chkMu.RLock()
			copy := make(map[string]bool, len(checkpoint))
			for k, v := range checkpoint {
				copy[k] = v
			}
			chkMu.RUnlock()

			if b, err := json.Marshal(copy); err == nil {
				os.WriteFile("probe_checkpoint.json", b, 0644)
			}
		}
	}()

	// Worker pool — 100 concurrent workers
	workerCount := 100
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for slug := range slugCh {
				for _, pFn := range probers {
					res := pFn(slug)
					if res.Confirmed {
						atomic.AddInt32(&found, 1)

						atsCounts.Lock()
						atsCounts.m[res.ATSName]++
						atsCounts.Unlock()

						// Write to results file
						resFileMu.Lock()
						if b, err := json.Marshal(res); err == nil {
							resFile.Write(append(b, '\n'))
						}
						resFileMu.Unlock()

						if testRun {
							fmt.Printf("[HIT] %-20s -> %-12s (Jobs: %d) domain: %s\n",
								slug, res.ATSName, res.JobCount, res.Domain)
						} else {
							saveDiscovery(context.Background(), dbpool, res)
						}
						break // One ATS per slug — stop probing after first hit
					}
				}

				// Mark slug as done in checkpoint
				chkMu.Lock()
				checkpoint[slug] = true
				chkMu.Unlock()

				atomic.AddInt32(&processed, 1)
			}
		}()
	}

	wg.Wait()

	// Final checkpoint flush
	chkMu.RLock()
	if b, err := json.Marshal(checkpoint); err == nil {
		os.WriteFile("probe_checkpoint.json", b, 0644)
	}
	chkMu.RUnlock()

	fmt.Printf("\n--- Probe Complete ---\n")
	fmt.Printf("Total processed: %d | Total confirmed: %d\n", processed, found)
	fmt.Printf("Breakdown by ATS:\n")
	for ats, count := range atsCounts.m {
		fmt.Printf("  %-12s: %d\n", ats, count)
	}
	fmt.Printf("\nResults saved to probe_results.json\n")
	if dbpool != nil {
		fmt.Printf("Discoveries saved to database.\n")
	}
}

