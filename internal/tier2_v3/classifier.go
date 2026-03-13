// Package tier2_v3 — classifier.go
// Stage 1 rule-based fast filter. Runs on every XHR/Fetch request with zero latency.
// Improved logic: scores every signal independently and returns the best candidate.
package tier2_v3

import (
	"encoding/json"
	"math"
	"net/url"
	"strings"
)

// scoreURLPath evaluates the network request (URL, method, headers, response sizes)
// and returns a confidence score normalized to 1.0.
// Maximum score is 1.0.
type Classifier struct{}

func NewClassifier() *Classifier { return &Classifier{} }

// ScoreURLPath evaluates the network request (URL, method, headers, response sizes)
// and returns a confidence score normalized to 1.0.
// Maximum score is 1.0.
func ScoreURLPath(reqURL string, method string, respContentType string, respSize int) float64 {
	score := 0.0
	urlL := strings.ToLower(reqURL)

	// Skip clearly irrelevant requests early
	if isJunk(urlL) {
		return 0.0
	}

	// URL path contains job-related segments -> 0.25
	if containsAny(urlL, []string{
		"/jobs", "/positions", "/careers", "/openings", "/vacancies",
		"/postings", "/requisitions", "/roles", "/embed/jobs",
		"/embed/departments", "/departments", "/embedjobs",
		"/job_openings", "/boards", "/career", "/careerspagequery",
		"/employer", "/get-all-jobs", "/get_job", "/hire", "/hiring",
		"/job", "/job-board", "/job-posting", "/position_details",
		"/posting-api", "/recruit", "/recruiter", "/reqlist",
		"/sr-jobs", "/turbohire",
	}) {
		score += 0.25
	}

	// URL contains pagination or filtering parameters -> 0.15
	if containsAny(urlL, []string{"?page=", "&page=", "?offset=", "&offset=", "?limit=", "&limit=", "?department=", "&department=", "?location=", "&location=", "?category=", "&category="}) {
		score += 0.15
	}

	// Request method is POST -> 0.10
	if strings.ToUpper(method) == "POST" {
		score += 0.10
	}

	// Response Content-Type is application/json -> 0.15
	respCT := strings.ToLower(respContentType)
	if strings.Contains(respCT, "application/json") || strings.Contains(respCT, "application/graphql-response+json") {
		score += 0.15
	}

	// URL path depth is three or fewer segments -> 0.10
	if parsed, err := url.Parse(reqURL); err == nil {
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(segments) > 0 && segments[0] == "" {
			segments = nil
		}
		if len(segments) <= 3 {
			score += 0.10
		}
	}

	// Response size is between 1KB and 2MB -> 0.10
	if respSize >= 1024 && respSize <= 2*1024*1024 {
		score += 0.10
	}

	// Known ATS domain is present -> 0.15
	if IsKnownATSDomain(urlL) {
		score += 0.15
	}

	if score > 1.0 {
		return 1.0
	}
	return score
}

// IsKnownATSDomain checks if the URL matches any known ATS domain patterns
func IsKnownATSDomain(reqURL string) bool {
	urlL := strings.ToLower(reqURL)
	atsDomains := []string{
		"greenhouse.io", "lever.co", "ashbyhq.com", "ashby.io",
		"myworkdayjobs.com", "workday.com", "wd1.myworkdayjobs.com",
		"smartrecruiters.com", "bamboohr.com", "jobvite.com",
		"icims.com", "breezy.hr", "recruitee.com",
		"teamtailor.com", "personio.com", "personio.de", "workable.com",
		"paylocity.com", "taleo.net", "successfactors.com",
		"jazzhr.com", "pinpointhq.com", "dover.com",
		"rippling.com", "gusto.com", "rippling-ats.com",
		"freshteam.com", "ultipro.com",
		"fountain.com", "comeet.co", "traffit.com",
		"recruitcrm.io", "zohorecruit.com", "keka.com",
		"darwinbox.com", "springrecruit.com", "hirecraft.in",
		"ceipal.com", "turbohire.co", "aglasem.com",

		// Extracted from dynamic capture run
		"api.ashbyhq.com", "api.greenhouse.io", "api.lever.co", "apply.workable.com",
		"ashby-job-postings-serverless-function.vercel.app",
		"asteria.keka.com", "bnymellon.eightfold.ai", "eightfold.ai",
		"boards-api.greenhouse.io", "bounteous.com", "careers.alight.com",
		"careers.atherenergy.com", "careers.cisco.com", "careers.ey.com",
		"careers.freeagent.com", "careers.humana.com", "careers.ixigo.com",
		"careers.makemytrip.com", "careers.pnc.com", "careers.viasat.com",
		"careers.yellow.ai", "clootrack.freshteam.com", "coforma.pinpointhq.com",
		"impactanalytics.keka.com", "jarvis-deploy.byteridge.com", "jobs.ashbyhq.com",
		"jobs.booking.com", "jobs.medallia.com", "jobs.smartrecruiters.com",
		"jobs.twilio.com", "jobsapi-internal.m-cloud.io", "khanacademy.org",
		"khatabook.com", "micron.eightfold.ai", "pleo.io", "practo.app.param.ai",
		"param.ai", "scopicsoftware.zohorecruit.com", "sirion.mynexthire.com",
		"mynexthire.com", "swiggy.mynexthire.com",
	}
	return containsAny(urlL, atsDomains)
}

// IsKnownATS checks if the URL matches any known ATS domain patterns
func (c *Classifier) IsKnownATS(reqURL string) bool {
	return IsKnownATSDomain(reqURL)
}

// CalculateFinalConfidence computes the final hybrid score and applies the ATS floor
func (c *Classifier) CalculateFinalConfidence(urlConf float64, bodyConf float64, reqURL string) float64 {
	blend := (urlConf * 0.40) + (bodyConf * 0.60)
	boostedMax := math.Max(urlConf, bodyConf) * 0.85
	conf := math.Max(blend, boostedMax)

	// Apply known ATS confidence floor
	if c.IsKnownATS(reqURL) && bodyConf > 0.20 {
		if conf < 0.62 {
			conf = 0.62
		}
	}

	if conf > 1.0 {
		return 1.0
	}
	return conf
}

func (c *Classifier) OldClassifyRemoval() {
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

	// Block .js files without matching .json:
	// - ends with ".js" (no trailing char)
	// - contains ".js?" (query string)
	// - contains ".js#" (fragment)
	if strings.HasSuffix(urlL, ".js") ||
		strings.Contains(urlL, ".js?") ||
		strings.Contains(urlL, ".js#") {
		return true
	}

	return false
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ── Structural Body Classifier ───────────────────────────────────────────────

var (
	bodyJobKeys = map[string]bool{
		"jobs": true, "positions": true, "openings": true, "listings": true, "vacancies": true,
	}

	atsVocab = map[string]bool{
		"title":           true,
		"location":        true,
		"department":      true,
		"description":     true,
		"apply_url":       true,
		"application_url": true,
		"req_id":          true,
		"requisition_id":  true,
		"employment_type": true,
		"posted_at":       true,
		"offices":         true,
		"absolute_url":    true,
		"updated_at":      true,
		"data_compliance": true,
		"internal_job_id": true,
	}

	generalVocab = map[string]bool{
		"name":         true,
		"role":         true,
		"position":     true,
		"city":         true,
		"country":      true,
		"region":       true,
		"office":       true,
		"team":         true,
		"function":     true,
		"level":        true,
		"salary":       true,
		"compensation": true,
		"remote":       true,
		"type":         true,
		"url":          true,
		"link":         true,
		"deadline":     true,
		"start_date":   true,
		"experience":   true,
		"skills":       true,
		"reqId":        true,
		"reqTitle":     true,
		"buName":       true,
		"statusId":     true,
		"position_id":  true,
		"req_title":    true,
		"job_id":       true,
		"opening_id":   true,
	}

	locationFields = map[string]bool{
		"city": true, "country": true, "region": true,
	}
)

// isBlockedResponseURL returns true if the URL belongs to a known CDN, tracking, or cookie service,
// or if the URL path ends with a static asset extension.
func isBlockedResponseURL(rawURL string) bool {
	u := strings.ToLower(rawURL)
	blockedDomains := []string{
		"cookielaw.org", "onetrust.com", "cookiepro.com", "googletagmanager.com",
		"google-analytics.com", "segment.io", "mixpanel.com", "amplitude.com",
		"hotjar.com", "intercom.io", "contentful.com",
		"cdn.growthbook.io", "cmp.inmobi.com", "cdn.cookie-script.com", "website-files.com",
	}

	// Exemptions for ATS base domains potentially caught by aggressive sub-matching
	exemptions := []string{
		"keka.com", "eightfold.ai", "mynexthire.com", "zohorecruit.com",
		"param.ai", "freshteam.com", "pinpointhq.com", "smartrecruiters.com",
		"medallia.com",
	}

	for _, domain := range blockedDomains {
		if strings.Contains(u, domain) {
			// Ensure it's not a false positive against our exempted known-ATS bases
			isExempt := false
			for _, ex := range exemptions {
				if strings.Contains(u, ex) {
					isExempt = true
					break
				}
			}
			if !isExempt {
				return true
			}
		}
	}

	// Block specific noise path segments
	noisePaths := []string{
		"/lottie/", "/page-data/sq/", "/wp-json/contact-form",
		"/youtubei/", "/ims/check/", "/v1/open", "/v3/company/details",
		"helios/page-data",
	}
	for _, np := range noisePaths {
		if strings.Contains(u, np) {
			return true
		}
	}

	// Next.js static data block: Only block if path does NOT contain job-related keywords
	if strings.Contains(u, "_next/data") {
		if !containsAny(u, []string{"career", "job", "position", "opening", "hiring"}) {
			return true
		}
	}

	exts := []string{".css", ".js", ".woff", ".woff2", ".png", ".jpg", ".svg"}
	if parsed, err := url.Parse(u); err == nil {
		path := parsed.Path
		for _, ext := range exts {
			if strings.HasSuffix(path, ext) {
				return true
			}
		}
	}

	return false
}

// ScoreResponseBody evaluates the structural confidence that a JSON body is a job API response.
func (c *Classifier) ScoreResponseBody(rawURL string, body []byte) (float64, string) {
	if isBlockedResponseURL(rawURL) {
		return 0.0, "shape_unknown"
	}

	baseScore := 0.0
	size := len(body)

	// Correct Size Response (500B - 500KB) -> +0.10
	if size >= 500 && size <= 500000 {
		baseScore += 0.10
	}

	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return baseScore, "shape_unknown"
	}

	// Valid parseable JSON with application/json bounds -> +0.15
	baseScore += 0.15

	shape1Score := checkShape1(data)
	shape2Score := checkShape2(data)
	shape3Score := checkShape3(data)
	shape4Score := checkShape4(data)

	maxScore := shape1Score
	bestShape := "shape1_full_list"

	if shape2Score > 0 && shape2Score >= maxScore {
		maxScore = shape2Score
		bestShape = "shape2_single_job"
	}
	if shape3Score > 0 && shape3Score >= maxScore {
		maxScore = shape3Score
		bestShape = "shape3_minimal_list"
	}
	if shape4Score > 0 && shape4Score >= maxScore {
		maxScore = shape4Score
		bestShape = "shape4_paginated"
	}

	if maxScore == 0.0 {
		bestShape = "shape_unknown"
	}

	finalScore := baseScore + maxScore
	if finalScore > 1.0 {
		finalScore = 1.0
	}

	return finalScore, bestShape
}

func checkShape1(data interface{}) float64 {
	score := 0.0
	var hasJobKey bool
	var hasLocationObj bool
	var hasTitleShape bool

	var maxArrayBonus float64 = 0.0

	var walk func(node interface{}, depth int)
	walk = func(node interface{}, depth int) {
		switch v := node.(type) {
		case map[string]interface{}:
			locCount := 0
			for k, child := range v {
				kl := strings.ToLower(k)
				if bodyJobKeys[kl] {
					hasJobKey = true
				}
				if kl == "title" {
					hasTitleShape = true
				}
				if locationFields[kl] {
					locCount++
				}

				if depth == 0 && kl == "data" {
					if hasGraphQLJobArray(v) {
						if 0.25 > maxArrayBonus {
							maxArrayBonus = 0.25
						}
					}
				}

				if childArr, ok := child.([]interface{}); ok {
					if depth == 0 {
						if hasJobArrayElements(childArr) {
							if 0.40 > maxArrayBonus {
								maxArrayBonus = 0.40
							}
						}
					} else if depth == 1 {
						if len(childArr) >= 3 && hasJobArrayElements(childArr) {
							if 0.30 > maxArrayBonus {
								maxArrayBonus = 0.30
							}
						}
					}
				}
				walk(child, depth+1)
			}
			if locCount > 0 {
				hasLocationObj = true
			}
		case []interface{}:
			if depth == 0 {
				if hasJobArrayElements(v) {
					if 0.40 > maxArrayBonus {
						maxArrayBonus = 0.40
					}
				}
			}
			for _, child := range v {
				walk(child, depth+1)
			}
		}
	}

	walk(data, 0)
	score += maxArrayBonus

	if hasJobKey {
		score += 0.20
	}

	if hasLocationObj && hasTitleShape {
		score += 0.15
	}
	return score
}

func checkShape2(data interface{}) float64 {
	m, ok := data.(map[string]interface{})
	if !ok {
		return 0.0
	}

	shape2Fields := []string{
		"title", "name", "role", "description", "requirements", "responsibilities",
		"apply_url", "application_url", "location", "city", "department", "team",
		"salary", "compensation", "employment_type", "posted_at", "deadline", "start_date",
	}

	count := 0
	for k := range m {
		kl := strings.ToLower(k)
		for _, f := range shape2Fields {
			if kl == f {
				count++
				break
			}
		}
	}
	if count >= 4 {
		return 0.30
	}
	return 0.0
}

func checkShape3(data interface{}) float64 {
	arr, ok := data.([]interface{})
	if !ok || len(arr) == 0 {
		return 0.0
	}
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			return 0.0
		}

		hasTitle := false
		hasName := false
		hasPosition := false
		hasRole := false
		hasId := false
		hasUrl := false
		hasApplyUrl := false

		for k := range m {
			kl := strings.ToLower(k)
			if kl == "title" {
				hasTitle = true
			}
			if kl == "name" {
				hasName = true
			}
			if kl == "position" {
				hasPosition = true
			}
			if kl == "role" {
				hasRole = true
			}
			if kl == "id" {
				hasId = true
			}
			if kl == "url" {
				hasUrl = true
			}
			if kl == "apply_url" {
				hasApplyUrl = true
			}
		}

		pairFound := (hasTitle && hasId) ||
			(hasTitle && hasUrl) ||
			(hasName && hasId) ||
			(hasPosition && hasId) ||
			(hasRole && hasId) ||
			(hasTitle && hasApplyUrl)

		if !pairFound {
			return 0.0
		}
	}
	return 0.25
}

func checkShape4(data interface{}) float64 {
	m, ok := data.(map[string]interface{})
	if !ok {
		return 0.0
	}

	hasPaging := false
	pagingFields := []string{"total", "count", "totalcount", "total_count", "pagination", "meta", "page", "pages", "per_page", "pagesize"}

	var arrayCandidate []interface{}

	for k, v := range m {
		kl := strings.ToLower(k)
		for _, pf := range pagingFields {
			if kl == pf {
				hasPaging = true
				break
			}
		}
		if arr, isArr := v.([]interface{}); isArr {
			arrayCandidate = arr
		}
	}

	if !hasPaging || arrayCandidate == nil || len(arrayCandidate) == 0 {
		return 0.0
	}

	if hasJobArrayElements(arrayCandidate) {
		return 0.35
	}

	first, ok := arrayCandidate[0].(map[string]interface{})
	if ok {
		count := 0
		shape2Fields := []string{
			"title", "name", "role", "description", "requirements", "responsibilities",
			"apply_url", "application_url", "location", "city", "department", "team",
			"salary", "compensation", "employment_type", "posted_at", "deadline", "start_date",
		}
		for k := range first {
			kl := strings.ToLower(k)
			for _, f := range shape2Fields {
				if kl == f {
					count++
					break
				}
			}
		}
		if count >= 4 {
			return 0.35
		}
	}

	return 0.0
}

// hasJobArrayElements checks if at least one element meets the vocabulary requirements.
func hasJobArrayElements(arr []interface{}) bool {
	for _, item := range arr {
		if obj, ok := item.(map[string]interface{}); ok {
			atsCount := 0
			genCount := 0
			for k := range obj {
				kl := strings.ToLower(k)
				if atsVocab[kl] {
					atsCount++
				}
				if generalVocab[kl] {
					genCount++
				}
			}
			if atsCount >= 3 || genCount >= 4 {
				return true
			}
		}
	}
	return false
}

func hasGraphQLJobArray(data map[string]interface{}) bool {
	dataObj, ok := data["data"].(map[string]interface{})
	if !ok {
		return false
	}
	for _, child := range dataObj {
		if arr, ok := child.([]interface{}); ok {
			for _, item := range arr {
				if obj, ok := item.(map[string]interface{}); ok {
					atsCount := 0
					genCount := 0
					for k := range obj {
						kl := strings.ToLower(k)
						if atsVocab[kl] {
							atsCount++
						}
						if generalVocab[kl] {
							genCount++
						}
					}
					if atsCount >= 2 || genCount >= 2 {
						return true
					}
				}
			}
		}
	}
	return false
}
