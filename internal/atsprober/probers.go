package atsprober

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

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

	// Per-ATS rate limiters — adjustable based on concurrency
	Limiters = map[string]*rate.Limiter{
		"greenhouse":      rate.NewLimiter(rate.Limit(10), 10),
		"lever":           rate.NewLimiter(rate.Limit(10), 10),
		"ashby":           rate.NewLimiter(rate.Limit(10), 10),
		"workable":        rate.NewLimiter(rate.Limit(8), 8),
		"bamboohr":        rate.NewLimiter(rate.Limit(8), 8),
		"recruitee":       rate.NewLimiter(rate.Limit(8), 8),
		"teamtailor":      rate.NewLimiter(rate.Limit(8), 8),
		"rippling":        rate.NewLimiter(rate.Limit(8), 8),
		"pinpoint":        rate.NewLimiter(rate.Limit(8), 8),
		"freshteam":       rate.NewLimiter(rate.Limit(8), 8),
		"smartrecruiters": rate.NewLimiter(rate.Limit(8), 8),
		"jobvite":         rate.NewLimiter(rate.Limit(8), 8),
		"breezyhr":        rate.NewLimiter(rate.Limit(8), 8),
		"personio":        rate.NewLimiter(rate.Limit(8), 8),
	}
)

// SetRateLimit dynamically adjusts the permitted bursts.
func SetRateLimit(ats string, reqsPerSec, burst int) {
	if l, ok := Limiters[ats]; ok {
		l.SetLimit(rate.Limit(reqsPerSec))
		l.SetBurst(burst)
	}
}

// DomainForATS returns the canonical domain for a confirmed ATS hit.
func DomainForATS(ats, slug string) string {
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
		return "smrtr.io/" + slug // Canonical shortlink representation for DB.
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

// ATSPlatformFromURL derives the ats_platform name from an API URL
func ATSPlatformFromURL(apiURL string) string {
	switch {
	case strings.Contains(apiURL, "greenhouse"):      return "greenhouse"
	case strings.Contains(apiURL, "lever.co"):        return "lever"
	case strings.Contains(apiURL, "ashbyhq"):         return "ashby"
	case strings.Contains(apiURL, "workable"):        return "workable"
	case strings.Contains(apiURL, "bamboohr"):        return "bamboohr"
	case strings.Contains(apiURL, "recruitee"):       return "recruitee"
	case strings.Contains(apiURL, "teamtailor"):      return "teamtailor"
	case strings.Contains(apiURL, "rippling"):        return "rippling"
	case strings.Contains(apiURL, "pinpointhq"):      return "pinpoint"
	case strings.Contains(apiURL, "freshteam"):       return "freshteam"
	case strings.Contains(apiURL, "smartrecruiters"): return "smartrecruiters"
	case strings.Contains(apiURL, "jobvite.com"):     return "jobvite"
	case strings.Contains(apiURL, "breezy.hr"):       return "breezyhr"
	case strings.Contains(apiURL, "personio."):       return "personio"
	default:                                          return "custom"
	}
}

// DoReq executes an HTTP request and returns body bytes, status code, and content type.
func DoReq(req *http.Request) ([]byte, int, string) {
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

// IsJSON returns true if the content type indicates JSON.
func IsJSON(ct string) bool {
	return strings.Contains(ct, "application/json") || strings.Contains(ct, "text/javascript")
}

// SaveResult persists the discovered ATS endpoint to the DB
func SaveResult(ctx context.Context, db *pgxpool.Pool, res ProbeResult, tierUsed string) {
	if db == nil || res.Domain == "" {
		return
	}

	var companyID string
	err := db.QueryRow(ctx, `
		INSERT INTO companies (domain, name, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (domain) DO UPDATE SET updated_at = NOW()
		RETURNING id
	`, res.Domain, res.Slug).Scan(&companyID)
	if err != nil {
		log.Printf("ERROR: upsert company failed slug=%s ats=%s: %v", res.Slug, res.ATSName, err)
		return
	}

	_, err = db.Exec(ctx, `
		INSERT INTO discovery_records
			(company_id, domain, api_url, http_method, tier_used, status, confidence, discovered_at, created_at, updated_at)
		VALUES ($1, $2, $3, 'GET', $4, 'discovered', 0.95, NOW(), NOW(), NOW())
		ON CONFLICT (domain) DO UPDATE SET
			api_url     = EXCLUDED.api_url,
			status      = 'discovered',
			tier_used   = EXCLUDED.tier_used,
			confidence  = 0.95,
			updated_at  = NOW()
		WHERE discovery_records.status != 'discovered'
	`, companyID, res.Domain, res.APIURL, tierUsed)
	if err != nil {
		log.Printf("ERROR: upsert discovery failed slug=%s ats=%s domain=%s: %v", res.Slug, res.ATSName, res.Domain, err)
	}
}

// ── Probers ──────────────────────────────────────────────────────────────────

func ProbeGreenhouse(slug string) ProbeResult {
	_ = Limiters["greenhouse"].Wait(context.Background())
	u := fmt.Sprintf("https://boards-api.greenhouse.io/v1/boards/%s/jobs", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "greenhouse", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && len(body) > 100 && IsJSON(ct) {
		var data struct {
			Jobs []json.RawMessage `json:"jobs"`
		}
		if json.Unmarshal(body, &data) == nil && strings.Contains(string(body), `"jobs"`) {
			res.Confirmed = true
			res.JobCount = len(data.Jobs)
			res.Domain = DomainForATS("greenhouse", slug)
		}
	}
	return res
}

func ProbeLever(slug string) ProbeResult {
	_ = Limiters["lever"].Wait(context.Background())
	u := fmt.Sprintf("https://api.lever.co/v0/postings/%s?mode=json&limit=5", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "lever", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && len(body) > 2 && IsJSON(ct) {
		var arr []map[string]interface{}
		if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
			if _, ok := arr[0]["text"]; ok {
				res.Confirmed = true
				res.JobCount = len(arr)
				res.Domain = DomainForATS("lever", slug)
			}
		}
	}
	return res
}

func ProbeAshby(slug string) ProbeResult {
	_ = Limiters["ashby"].Wait(context.Background())
	u := "https://jobs.ashbyhq.com/api/non-user-graphql?op=ApiJobBoardWithTeams"
	payload := fmt.Sprintf(`{"operationName":"ApiJobBoardWithTeams","variables":{"organizationHostedJobsPageName":"%s"},"query":"query ApiJobBoardWithTeams($organizationHostedJobsPageName: String!) { jobBoardWithTeams(organizationHostedJobsPageName: $organizationHostedJobsPageName) { teams { name } jobPostings { id title locationName employmentType } } }"}`, slug)
	req, _ := http.NewRequest("POST", u, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	body, code, _ := DoReq(req)

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
			res.Domain = DomainForATS("ashby", slug)
		}
	}
	return res
}

func ProbeWorkable(slug string) ProbeResult {
	_ = Limiters["workable"].Wait(context.Background())
	u := fmt.Sprintf("https://apply.workable.com/api/v3/accounts/%s/jobs", slug)
	req, _ := http.NewRequest("POST", u, strings.NewReader(`{"query":"","location":[],"department":[],"worktype":[]}`))
	req.Header.Set("Content-Type", "application/json")
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "workable", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && IsJSON(ct) && len(body) > 0 {
		var obj struct {
			Total   int           `json:"total"`
			Results []interface{} `json:"results"`
		}
		if err := json.Unmarshal(body, &obj); err == nil && obj.Total > 0 {
			res.Confirmed = true
			res.JobCount = obj.Total
			res.Domain = DomainForATS("workable", slug)
		}
	}
	return res
}

func ProbeBambooHR(slug string) ProbeResult {
	_ = Limiters["bamboohr"].Wait(context.Background())
	// Try embed2.php first, fallback to careers/list
	u := fmt.Sprintf("https://%s.bamboohr.com/jobs/embed2.php", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, _ := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "bamboohr", APIURL: u, StatusCode: code, BodySize: len(body)}
	bodyStr := string(body)
	if code == 200 && len(body) > 50 && strings.Contains(bodyStr, `"jobOpenings"`) {
		res.Confirmed = true
		res.Domain = DomainForATS("bamboohr", slug)
		return res
	}
	// Fallback: try the careers endpoint
	u2 := fmt.Sprintf("https://%s.bamboohr.com/careers", slug)
	req2, _ := http.NewRequest("GET", u2, nil)
	body2, code2, _ := DoReq(req2)
	res.APIURL = u2
	res.StatusCode = code2
	res.BodySize = len(body2)
	if code2 == 200 && len(body2) > 500 && strings.Contains(string(body2), `"jobOpenings"`) {
		res.Confirmed = true
		res.Domain = DomainForATS("bamboohr", slug)
	}
	return res
}

func ProbeRecruitee(slug string) ProbeResult {
	_ = Limiters["recruitee"].Wait(context.Background())
	u := fmt.Sprintf("https://%s.recruitee.com/api/offers", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "recruitee", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && IsJSON(ct) && len(body) > 100 {
		var obj struct {
			Offers []struct {
				Title string `json:"title"`
				Slug  string `json:"slug"`
			} `json:"offers"`
		}
		if err := json.Unmarshal(body, &obj); err == nil && len(obj.Offers) > 0 {
			if obj.Offers[0].Title != "" && obj.Offers[0].Slug != "" {
				res.Confirmed = true
				res.JobCount = len(obj.Offers)
				res.Domain = DomainForATS("recruitee", slug)
			}
		}
	}
	return res
}

func ProbeTeamtailor(slug string) ProbeResult {
	_ = Limiters["teamtailor"].Wait(context.Background())
	// Try jobs.teamtailor.com first
	u := fmt.Sprintf("https://jobs.teamtailor.com/companies/%s/jobs.json", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "teamtailor", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && IsJSON(ct) && strings.Contains(string(body), `"data"`) && len(body) > 50 {
		var obj map[string]interface{}
		if err := json.Unmarshal(body, &obj); err == nil {
			if data, ok := obj["data"].([]interface{}); ok && len(data) > 0 {
				res.Confirmed = true
				res.JobCount = len(data)
				res.Domain = DomainForATS("teamtailor", slug)
				return res
			}
		}
	}
	// Fallback: check if subdomain responds with a valid career page
	u2 := fmt.Sprintf("https://%s.teamtailor.com/jobs", slug)
	req2, _ := http.NewRequest("HEAD", u2, nil)
	_, code2, _ := DoReq(req2)
	if code2 == 200 {
		res.Confirmed = true
		res.APIURL = u2
		res.StatusCode = code2
		res.Domain = DomainForATS("teamtailor", slug)
	}
	return res
}

func ProbeRippling(slug string) ProbeResult {
	_ = Limiters["rippling"].Wait(context.Background())
	u := fmt.Sprintf("https://api.rippling.com/platform/api/ats/v1/board/%s-careers/jobs", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "rippling", APIURL: u, StatusCode: code, BodySize: len(body)}
	bodyStr := string(body)
	if code == 200 && IsJSON(ct) &&
		strings.Contains(bodyStr, `"id"`) &&
		strings.Contains(bodyStr, `"name"`) &&
		strings.Contains(bodyStr, `"department"`) &&
		len(body) > 200 {
		res.Confirmed = true
		res.Domain = DomainForATS("rippling", slug)
	}
	return res
}

func ProbePinpoint(slug string) ProbeResult {
	_ = Limiters["pinpoint"].Wait(context.Background())
	u := fmt.Sprintf("https://%s.pinpointhq.com/postings.json", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "pinpoint", APIURL: u, StatusCode: code, BodySize: len(body)}
	bodyStr := string(body)

	if strings.Contains(bodyStr, "<html") || strings.Contains(bodyStr, "<!DOCTYPE") {
		return res
	}
	if code == 200 && IsJSON(ct) && len(body) > 300 &&
		strings.Contains(bodyStr, `"data"`) &&
		strings.Contains(bodyStr, `"title"`) &&
		strings.Contains(bodyStr, `"location"`) &&
		strings.Contains(bodyStr, `"employment_type"`) {
		var obj map[string]interface{}
		if err := json.Unmarshal(body, &obj); err == nil {
			if data, ok := obj["data"].([]interface{}); ok && len(data) > 0 {
				res.Confirmed = true
				res.JobCount = len(data)
				res.Domain = DomainForATS("pinpoint", slug)
			}
		}
	}
	return res
}

func ProbeFreshteam(slug string) ProbeResult {
	_ = Limiters["freshteam"].Wait(context.Background())
	u := fmt.Sprintf("https://%s.freshteam.com/hire/widgets/jobs.json", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "freshteam", APIURL: u, StatusCode: code, BodySize: len(body)}
	bodyStr := string(body)
	if code == 200 && IsJSON(ct) && len(body) > 500 &&
		strings.Contains(bodyStr, `"title"`) &&
		strings.Contains(bodyStr, `"id"`) &&
		strings.Contains(bodyStr, `"remote"`) {
		res.Confirmed = true
		res.Domain = DomainForATS("freshteam", slug)
	}
	return res
}

func ProbeSmartRecruiters(slug string) ProbeResult {
	_ = Limiters["smartrecruiters"].Wait(context.Background())
	u := fmt.Sprintf("https://api.smartrecruiters.com/v1/companies/%s/postings", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "smartrecruiters", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && IsJSON(ct) && len(body) > 50 {
		var obj map[string]interface{}
		if err := json.Unmarshal(body, &obj); err == nil {
			if content, ok := obj["content"]; ok {
				if arr, ok := content.([]interface{}); ok && len(arr) > 0 {
					if firstElement, ok := arr[0].(map[string]interface{}); ok {
						if _, ok := firstElement["name"]; ok {
							res.Confirmed = true
							res.JobCount = len(arr)
							res.Domain = DomainForATS("smartrecruiters", slug)
						}
					}
				}
			}
		}
	}
	return res
}

func ProbeJobvite(slug string) ProbeResult {
	_ = Limiters["jobvite"].Wait(context.Background())
	u := fmt.Sprintf("https://jobs.jobvite.com/api/job?c=%s", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "jobvite", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && IsJSON(ct) {
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
				res.Domain = DomainForATS("jobvite", slug)
			}
		}
	}
	return res
}

func ProbeBreezyHR(slug string) ProbeResult {
	_ = Limiters["breezyhr"].Wait(context.Background())
	apiURL := fmt.Sprintf("https://api.breezy.hr/v3/company/%s/positions?state=published", slug)
	req, _ := http.NewRequest("GET", apiURL, nil)
	body, code, ct := DoReq(req)

	res := ProbeResult{Slug: slug, ATSName: "breezyhr", APIURL: apiURL, StatusCode: code, BodySize: len(body)}
	if code == 200 && IsJSON(ct) {
		var positions []struct {
			ID    string `json:"_id"`
			Name  string `json:"name"`
			State string `json:"state"`
		}
		if json.Unmarshal(body, &positions) == nil && len(positions) > 0 {
			res.Confirmed = true
			res.JobCount = len(positions)
			res.Domain = DomainForATS("breezyhr", slug)
		}
	}
	return res
}

func ProbePersonio(slug string) ProbeResult {
	_ = Limiters["personio"].Wait(context.Background())
	u := fmt.Sprintf("https://%s.jobs.personio.com/api/v1/jobs", slug)
	req, _ := http.NewRequest("GET", u, nil)
	body, code, ct := DoReq(req)

	if code != 200 {
		u = fmt.Sprintf("https://%s.jobs.personio.de/api/v1/jobs", slug)
		req, _ = http.NewRequest("GET", u, nil)
		body, code, ct = DoReq(req)
	}

	res := ProbeResult{Slug: slug, ATSName: "personio", APIURL: u, StatusCode: code, BodySize: len(body)}
	if code == 200 && IsJSON(ct) && len(body) > 50 {
		var jobs []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(body, &jobs) == nil && len(jobs) > 0 {
			res.Confirmed = true
			res.JobCount = len(jobs)
			res.Domain = DomainForATS("personio", slug)
		}
	}
	return res
}
