// Package tier2 — classifier.go
// Implements the Stage 1 rule-based fast classifier described in Section 4
// of the CareerScout foundation document. Runs synchronously inside the
// Tier 2 worker with zero added latency (no network calls, pure CPU).
package tier2

import (
	"strings"
)

// signalWeight is the score contribution of each positive signal.
// A request is classified as a job API target if totalScore >= threshold.
const threshold = 3

// Classifier implements the Stage 1 rule-based fast filter.
type Classifier struct{}

// NewClassifier creates a new Stage 1 Classifier.
func NewClassifier() *Classifier {
	return &Classifier{}
}

// Classify evaluates a network request against all Stage 1 signals.
// Returns (confidence, matched) where confidence is 0.0–1.0.
// A request must score >= 3 signals to be considered a match.
func (c *Classifier) Classify(rawURL, method string, headers map[string]string, body string) (confidence float64, matched bool) {
	score := 0

	urlLower := strings.ToLower(rawURL)
	bodyLower := strings.ToLower(body)

	// ── Signal 1: URL path contains job-related segments ─────────────────────
	jobPathSegments := []string{
		"/jobs", "/careers", "/positions", "/openings",
		"/graphql", "/api/v",
		"/postings", "/vacancies", "/roles",
	}
	for _, seg := range jobPathSegments {
		if strings.Contains(urlLower, seg) {
			score++
			break // only count once
		}
	}

	// ── Signal 2: POST body keys indicate a job listing query ─────────────────
	bodyKeys := []string{
		`"limit"`, `"offset"`, `"departments"`, `"jobtype"`,
		`"locationid"`, `"operationname"`, `"page"`, `"size"`,
		`"category"`, `"team"`, `"req_id"`,
	}
	for _, key := range bodyKeys {
		if strings.Contains(bodyLower, key) {
			score++
			break // only count once
		}
	}

	// ── Signal 3: Auth or API key headers present ──────────────────────────────
	for k, v := range headers {
		kl := strings.ToLower(k)
		vl := strings.ToLower(v)
		if (kl == "authorization" && strings.HasPrefix(vl, "bearer ")) ||
			kl == "x-api-key" || kl == "x-auth-token" {
			score++
			break
		}
	}

	// ── Signal 4: JSON content-type ───────────────────────────────────────────
	for k, v := range headers {
		if strings.ToLower(k) == "content-type" && strings.Contains(strings.ToLower(v), "json") {
			score++
			break
		}
	}

	// ── Signal 5: Pagination query parameters ─────────────────────────────────
	paginationParams := []string{"?page=", "?from=", "?size=", "?offset=", "?start=", "?limit="}
	for _, p := range paginationParams {
		if strings.Contains(urlLower, p) {
			score++
			break
		}
	}

	// ── Signal 6: Well-known ATS domains ─────────────────────────────────────
	atsDomains := []string{
		"greenhouse.io", "lever.co", "ashbyhq.com",
		"workday.com", "myworkdayjobs.com", "smartrecruiters.com",
		"bamboohr.com", "jobvite.com", "icims.com", "breezy.hr",
		"recruitee.com", "teamtailor.com", "personio.com",
	}
	for _, ats := range atsDomains {
		if strings.Contains(urlLower, ats) {
			score += 2 // strong signal — count double
			break
		}
	}

	// ── Signal 7: GraphQL with jobs-related operation ─────────────────────────
	if strings.Contains(urlLower, "graphql") &&
		(strings.Contains(bodyLower, `"getjobs"`) ||
			strings.Contains(bodyLower, `"searchjobs"`) ||
			strings.Contains(bodyLower, `"listjobs"`) ||
			strings.Contains(bodyLower, `"operationname"`) &&
				strings.Contains(bodyLower, "job")) {
		score += 2
	}

	if score < threshold {
		return 0, false
	}

	// Confidence: normalise score to 0.0–1.0 range (cap at 1.0)
	conf := float64(score) / 8.0
	if conf > 1.0 {
		conf = 1.0
	}

	return conf, true
}
