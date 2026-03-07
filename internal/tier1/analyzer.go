// Package tier1 — analyzer.go
// Performs static analysis on HTML and JavaScript source to detect
// hardcoded job API endpoint patterns without executing any JavaScript.
package tier1

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
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

type endpointPattern struct {
	name    string
	re      *regexp.Regexp
	method  string
	extract func(groups []string, domain string) string
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
	}

	return a
}

// Analyze scans the given HTML/JS content for job API patterns.
// Returns the first match found, or nil if no pattern matches.
func (a *Analyzer) Analyze(content, domain string) *Match {
	for _, p := range a.patterns {
		groups := p.re.FindStringSubmatch(content)
		if groups == nil {
			continue
		}

		apiURL := p.extract(groups, domain)
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
		groups := p.re.FindStringSubmatch(content)
		if groups == nil {
			continue
		}
		apiURL := p.extract(groups, domain)
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
