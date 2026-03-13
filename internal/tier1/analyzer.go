// Package tier1 — analyzer.go
// Performs static analysis on HTML and JavaScript source to detect
// hardcoded job API endpoint patterns without executing any JavaScript.
package tier1

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Match represents a discovered API endpoint pattern from static analysis.
type Match struct {
	APIURL  string
	Method  string
	Pattern string
}

// Analyzer scans raw HTML/JS content for job API endpoint patterns.
type Analyzer struct {
	patterns []*endpointPattern
}

// endpointPattern holds either a regex-based or HTML-based extractor.
// If htmlExtract is non-nil it is used and re is ignored.
type endpointPattern struct {
	name        string
	re          *regexp.Regexp
	method      string
	extract     func(groups []string, domain string) string
	htmlExtract func(content, domain string) string // nil for regex-based patterns
}

// NewAnalyzer creates an Analyzer with all known job API patterns pre-compiled.
func NewAnalyzer() *Analyzer {
	a := &Analyzer{}

	// ── Pattern set: covers the most common SaaS ATS platforms + custom APIs ──

	a.patterns = []*endpointPattern{
		{
			// Greenhouse ATS: fetch("https://boards-api.greenhouse.io/v1/boards/SLUG/jobs")
			name:    "greenhouse",
			method:  "GET",
			re:      regexp.MustCompile(`(?i)(https?://boards-api\.greenhouse\.io/v1/boards/[a-z0-9_-]+/jobs[^"'\s]*)`),
			extract: func(g []string, _ string) string { return g[1] },
		},
		{
			// Lever ATS: fetch("https://api.lever.co/v0/postings/COMPANY")
			name:    "lever",
			method:  "GET",
			re:      regexp.MustCompile(`(?i)(https?://api\.lever\.co/v0/postings/[a-z0-9_-]+[^"'\s]*)`),
			extract: func(g []string, _ string) string { return g[1] },
		},
		{
			// Workday: /api/apply/v1/jobs or /wday/cxs/TENANT/jobs/... pattern
			name:    "workday",
			method:  "GET",
			re:      regexp.MustCompile(`(?i)"(https?://[^"]+\.myworkdayjobs\.com/[^"]+/jobs[^"]*)"`),
			extract: func(g []string, _ string) string { return g[1] },
		},
		{
			// Ashby ATS: https://api.ashbyhq.com/posting-api/job-board/SLUG
			name:    "ashby",
			method:  "GET",
			re:      regexp.MustCompile(`(?i)(https?://api\.ashbyhq\.com/posting-api/job-board/[a-z0-9_-]+)`),
			extract: func(g []string, _ string) string { return g[1] },
		},
		{
			// Generic REST: /api/jobs, /api/v1/jobs, /api/v2/positions, etc.
			name:   "generic_api",
			method: "GET",
			re:     regexp.MustCompile(`(?i)["'](/?api/v?\d*/(?:jobs|positions|openings|careers)[^"'\s]*)["']`),
			extract: func(g []string, domain string) string {
				path := g[1]
				if strings.HasPrefix(path, "http") {
					return path
				}
				return fmt.Sprintf("https://%s%s", domain, path)
			},
		},
		{
			// GraphQL endpoints: /graphql with operationName hint
			name:   "graphql",
			method: "POST",
			re:     regexp.MustCompile(`(?i)["'](/?graphql[^"'\s]*)["']`),
			extract: func(g []string, domain string) string {
				path := g[1]
				if strings.HasPrefix(path, "http") {
					return path
				}
				return fmt.Sprintf("https://%s%s", domain, path)
			},
		},
		{
			// SmartRecruiters: /api/v1/companies/SLUG/postings
			name:    "smartrecruiters",
			method:  "GET",
			re:      regexp.MustCompile(`(?i)(https?://api\.smartrecruiters\.com/v1/companies/[^"'\s]+/postings[^"'\s]*)`),
			extract: func(g []string, _ string) string { return g[1] },
		},
		{
			// Bamboo HR: /api/gateway.php/COMPANY/v1/applicant_tracking/jobs
			name:    "bamboohr",
			method:  "GET",
			re:      regexp.MustCompile(`(?i)(https?://[^"']+\.bamboohr\.com/api/gateway\.php/[^"'\s]+/v1/applicant_tracking/jobs[^"'\s]*)`),
			extract: func(g []string, _ string) string { return g[1] },
		},
		{
			// Jobvite: /api/website/jobs
			name:    "jobvite",
			method:  "GET",
			re:      regexp.MustCompile(`(?i)(https?://[^"'\s]+jobvite\.com/api/website/jobs[^"'\s]*)`),
			extract: func(g []string, _ string) string { return g[1] },
		},
		{
			// iCIMS: /icims/data/jobs/...
			name:    "icims",
			method:  "GET",
			re:      regexp.MustCompile(`(?i)(https?://[^"'\s]+\.icims\.com/jobs/[^"'\s]*)`),
			extract: func(g []string, _ string) string { return g[1] },
		},
		{
			// Next.js __NEXT_DATA__: uses goquery to find the script tag by ID,
			// then JSON-walks the parsed payload for any ATS or job API URL.
			// This correctly handles minified, deeply nested, and non-standard
			// __NEXT_DATA__ structures that a flat regex silently misses.
			name:        "nextjs_data",
			method:      "GET",
			htmlExtract: extractNextData,
		},
		{
			// Preconnect hint: browsers declare the domains they will XHR to
			// via <link rel=preconnect href=...> before the page even executes JS.
			// This is free signal: an ATS domain listed in preconnect is almost
			// certainly the company's job data backend.
			name:   "preconnect",
			method: "GET",
			re: regexp.MustCompile(
				`(?i)<link[^>]+rel=["']preconnect["'][^>]+href=["'](https?://[^"']+(?:greenhouse|lever|ashby|workday|smartrecruiters|bamboohr|jobvite|icims|teamtailor|personio|workable)[^"']*)["']`),
			extract: func(g []string, _ string) string {
				u, err := url.Parse(g[1])
				if err != nil {
					return g[1]
				}
				return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
			},
		},
		{
			// CSP meta tag: some pages deliver their Content-Security-Policy as
			// an HTML <meta http-equiv="Content-Security-Policy"> tag instead of
			// an HTTP header. Extracting connect-src from this lets Tier 1 resolve
			// pages whose ATS backend is visible here, skipping the browser entirely.
			name:        "csp_meta",
			method:      "GET",
			htmlExtract: extractCSPMeta,
		},
		{
			// schema.org JobPosting: <script type="application/ld+json"> tags
			// containing @type: "JobPosting" or @graph arrays with JobPosting entries.
			// This deflects companies using schema.org markup entirely from Tier 2.
			name:        "schema_org_jobs",
			method:      "GET",
			htmlExtract: extractSchemaOrgJobs,
		},
	}

	return a
}

// Analyze scans the given HTML/JS content for job API patterns.
// Returns the first match found, or nil if no pattern matches.
func (a *Analyzer) Analyze(content, domain string) *Match {
	for _, p := range a.patterns {
		apiURL := a.applyPattern(p, content, domain)
		if apiURL == "" {
			continue
		}
		if !isValidURL(apiURL) {
			continue
		}
		return &Match{
			APIURL:  apiURL,
			Method:  p.method,
			Pattern: p.name,
		}
	}
	return nil
}

// AnalyzeAll returns all matches found (for debugging/testing).
func (a *Analyzer) AnalyzeAll(content, domain string) []Match {
	var matches []Match
	for _, p := range a.patterns {
		apiURL := a.applyPattern(p, content, domain)
		if apiURL == "" {
			continue
		}
		if !isValidURL(apiURL) {
			continue
		}
		matches = append(matches, Match{
			APIURL:  apiURL,
			Method:  p.method,
			Pattern: p.name,
		})
	}
	return matches
}

// applyPattern runs either the HTML extractor or regex extractor for a pattern.
func (a *Analyzer) applyPattern(p *endpointPattern, content, domain string) string {
	if p.htmlExtract != nil {
		return p.htmlExtract(content, domain)
	}
	if p.re != nil {
		groups := p.re.FindStringSubmatch(content)
		if groups == nil {
			return ""
		}
		return p.extract(groups, domain)
	}
	return ""
}

// ── HTML-based extractors ─────────────────────────────────────────────────────

// extractNextData finds the <script id="__NEXT_DATA__"> tag using goquery,
// unmarshals its JSON content, then recursively walks all string values looking
// for values that match job-related URL patterns. Returns the first match.
func extractNextData(content, domain string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
	if err != nil {
		return ""
	}

	var found string
	doc.Find(`script#__NEXT_DATA__`).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		raw := strings.TrimSpace(s.Text())
		if raw == "" {
			return true // continue
		}

		var payload interface{}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return true // not valid JSON, skip
		}

		found = walkJSONForJobURL(payload, domain)
		return found == "" // stop if we found something
	})

	return found
}

// walkJSONForJobURL recursively walks any JSON value (object/array/string)
// looking for string values that match known ATS hostnames or job API paths.
func walkJSONForJobURL(v interface{}, domain string) string {
	switch val := v.(type) {
	case string:
		if isJobAPIURL(val) {
			return val
		}

	case map[string]interface{}:
		for _, child := range val {
			if hit := walkJSONForJobURL(child, domain); hit != "" {
				return hit
			}
		}

	case []interface{}:
		for _, child := range val {
			if hit := walkJSONForJobURL(child, domain); hit != "" {
				return hit
			}
		}
	}
	return ""
}

// isJobAPIURL returns true if the string looks like a job API endpoint.
// Matches known ATS hostnames or generic job/api path patterns.
func isJobAPIURL(s string) bool {
	if !strings.HasPrefix(s, "http") {
		return false
	}
	lower := strings.ToLower(s)

	// Known ATS API hostnames — high precision.
	atsHosts := []string{
		"boards-api.greenhouse.io",
		"api.lever.co",
		"api.ashbyhq.com",
		"api.smartrecruiters.com",
		"api.jobvite.com",
		"myworkdayjobs.com",
		"bamboohr.com",
		"icims.com",
		"api.teamtailor.com",
		"api.personio.de",
		"api.workable.com",
	}
	for _, host := range atsHosts {
		if strings.Contains(lower, host) {
			return true
		}
	}

	// Generic job API path segments — require at least one path keyword.
	jobPaths := []string{"/api/jobs", "/api/careers", "/api/positions", "/jobs/search", "/graphql"}
	for _, p := range jobPaths {
		if strings.Contains(lower, p) {
			return true
		}
	}

	return false
}

// extractCSPMeta finds <meta http-equiv="Content-Security-Policy"> and extracts
// the first ATS domain from the connect-src directive. This allows Tier 1 to
// resolve pages whose ATS backend is declared in a meta CSP tag, avoiding the
// browser entirely for pages the Tier 2 classifier would have caught.
func extractCSPMeta(content, domain string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
	if err != nil {
		return ""
	}

	var found string
	doc.Find(`meta[http-equiv="Content-Security-Policy"], meta[http-equiv="content-security-policy"]`).
		EachWithBreak(func(_ int, s *goquery.Selection) bool {
			csp, exists := s.Attr("content")
			if !exists || csp == "" {
				return true
			}
			found = extractATSFromCSP(csp)
			return found == "" // stop when we find something
		})

	return found
}

// extractATSFromCSP parses the connect-src directive of a CSP string and returns
// the first recognised ATS domain as a fully-qualified URL, or "" if none found.
func extractATSFromCSP(csp string) string {
	atsDomains := []string{
		"greenhouse.io",
		"lever.co",
		"ashbyhq.com",
		"ashby.io",
		"smartrecruiters.com",
		"bamboohr.com",
		"jobvite.com",
		"icims.com",
		"teamtailor.com",
		"personio.de",
		"workable.com",
		"myworkdayjobs.com",
	}

	cspL := strings.ToLower(csp)
	// Find connect-src directive.
	idx := strings.Index(cspL, "connect-src")
	if idx == -1 {
		return ""
	}
	// Take the tokens after connect-src until the next directive (;).
	segment := csp[idx+len("connect-src"):]
	end := strings.Index(segment, ";")
	if end != -1 {
		segment = segment[:end]
	}

	for _, token := range strings.Fields(segment) {
		tokenL := strings.ToLower(token)
		for _, ats := range atsDomains {
			if strings.Contains(tokenL, ats) {
				// Normalise to a plain https:// URL.
				if strings.HasPrefix(token, "http") {
					return token
				}
				return "https://" + strings.TrimPrefix(strings.TrimPrefix(token, "//"), "https://")
			}
		}
	}
	return ""
}

// isValidURL checks that the extracted string is a well-formed HTTP URL.
func isValidURL(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// extractSchemaOrgJobs finds <script type="application/ld+json"> tags and checks
// for schema.org JobPosting objects. Supports both direct @type and @graph arrays.
func extractSchemaOrgJobs(content, domain string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
	if err != nil {
		return ""
	}

	var found string
	doc.Find(`script[type="application/ld+json"]`).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		raw := strings.TrimSpace(s.Text())
		if raw == "" {
			return true
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			return true // not a JSON object, skip
		}

		// Check 1: root @type is JobPosting or JobPostingList
		if typ, ok := data["@type"].(string); ok {
			if typ == "JobPosting" || typ == "JobPostingList" {
				found = extractJobPostingURL(data, domain)
				return false // stop
			}
		}

		// Check 2: root contains @graph array with JobPosting elements
		if graph, ok := data["@graph"].([]interface{}); ok {
			for _, item := range graph {
				if obj, ok := item.(map[string]interface{}); ok {
					if typ, ok := obj["@type"].(string); ok && typ == "JobPosting" {
						found = extractJobPostingURL(obj, domain)
						return false // stop
					}
				}
			}
		}

		return true // continue to next script tag
	})

	return found
}

// extractJobPostingURL extracts the url or @id from a JobPosting object.
// Falls back to the page URL (domain) if neither is present.
func extractJobPostingURL(obj map[string]interface{}, domain string) string {
	if u, ok := obj["url"].(string); ok && u != "" {
		return u
	}
	if id, ok := obj["@id"].(string); ok && id != "" {
		return id
	}
	// Jobs are embedded directly in the page
	return fmt.Sprintf("https://%s", domain)
}
