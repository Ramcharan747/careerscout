package frontier

import (
	"strings"
)

// ScoreStatic calculates the initial 0.0-1.0 score based on static heuristics prior to network calls.
func ScoreStatic(url string) float64 {
	score := 0.0
	lowerURL := strings.ToLower(url)

	// Strip scheme to prevent '://jobs' from matching the path segment '/jobs'
	hostPath := strings.TrimPrefix(lowerURL, "https://")
	hostPath = strings.TrimPrefix(hostPath, "http://")

	// 1. known career-path segment (+0.25)
	careerPaths := []string{"/jobs", "/careers", "/positions", "/openings", "/work-with-us"}
	for _, p := range careerPaths {
		if strings.Contains(hostPath, p) {
			score += 0.25
			break
		}
	}

	// 2. known ATS subdomain pattern (+0.20)
	atsSubdomains := []string{"jobs.", "careers.", "work.", "apply."}
	for _, sub := range atsSubdomains {
		if strings.HasPrefix(hostPath, sub) || strings.HasPrefix(hostPath, "www."+sub) {
			score += 0.20
			break
		}
	}

	// 3. URL path depth of 2 or fewer segments (+0.15)
	pathIdx := strings.Index(hostPath, "/")
	if pathIdx == -1 {
		score += 0.15
	} else {
		path := hostPath[pathIdx:]
		segments := strings.Split(strings.Trim(path, "/"), "/")
		if len(segments) <= 2 && segments[0] != "" {
			score += 0.15
		}
	}

	// 4. presence of a known company TLD pattern (+0.10)
	tlds := []string{".io", ".ai", ".co"}
	for _, tld := range tlds {
		if strings.Contains(hostPath, tld+"/") || strings.HasSuffix(hostPath, tld) {
			score += 0.10
			break
		}
	}

	// 5. absence of known noise segments (+0.10)
	noise := []string{"/blog", "/press", "/news", "/about", "/legal", "/privacy"}
	hasNoise := false
	for _, n := range noise {
		if strings.Contains(hostPath, n) {
			hasNoise = true
			break
		}
	}
	if !hasNoise {
		score += 0.10
	}

	if score > 0.80 {
		score = 0.80
	}
	return score
}
