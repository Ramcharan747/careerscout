package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type CCLine struct {
	URL string `json:"url"`
}

// isUUID returns true if s matches a UUID pattern natively protecting IDs from parsing.
func isUUID(s string) bool {
	matched, _ := regexp.MatchString(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`, s)
	return matched
}

// isDigits returns true if s consists entirely of digits.
func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

var atsPatterns = []struct {
	pattern string
	atsName string
	domain  string
}{
	{"*.greenhouse.io/jobs*", "greenhouse", "greenhouse.io"},
	{"*.lever.co/jobs*", "lever", "lever.co"},
	{"*.ashbyhq.com/*", "ashby", "ashbyhq.com"},
	{"*.workable.com/*", "workable", "workable.com"},
	{"*.myworkdayjobs.com/*", "workday", "myworkdayjobs.com"},
	{"*.icims.com/jobs*", "icims", "icims.com"},
	{"*.smartrecruiters.com/*/jobs*", "smartrecruiters", "smartrecruiters.com"},
	{"*.bamboohr.com/jobs*", "bamboohr", "bamboohr.com"},
	{"*.jobvite.com/jobs*", "jobvite", "jobvite.com"},
	{"*.taleo.net/careersection*", "taleo", "taleo.net"},
	{"*.successfactors.com/careers*", "successfactors", "successfactors.com"},
	{"*.pinpointhq.com/*", "pinpoint", "pinpointhq.com"},
	{"*.recruitee.com/*", "recruitee", "recruitee.com"},
	{"*.teamtailor.com/jobs*", "teamtailor", "teamtailor.com"},
}

var careerPatterns = []string{
	"*/careers*",
	"*/jobs*",
	"*/work-with-us*",
	"*/join-us*",
	"*/join-our-team*",
}

var aggregators = []string{
	"linkedin.com", "glassdoor.com", "indeed.com",
	"naukri.com", "monster.com", "adzuna.com", "ziprecruiter.com",
}

type RichURL struct {
	URL       string `json:"url"`
	ATS       string `json:"ats,omitempty"`
	BoardName string `json:"board_name,omitempty"`
}

func parseURL(raw string) (canonical string, ats string, board string, skip bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", true
	}
	host := strings.ToLower(u.Host)

	for _, agg := range aggregators {
		if strings.Contains(host, agg) {
			return "", "", "", true
		}
	}

	host = strings.TrimPrefix(host, "www.")

	// Global filtering layer: If the final path segment is heavily UUID or purely digits, skip it.
	// This naturally drops individual job listings globally across ATS or generic patterns.
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) > 0 {
		lastSeg := parts[len(parts)-1]
		if isUUID(lastSeg) || isDigits(lastSeg) {
			return "", "", "", true
		}
	}

	// ATS checking
	for _, a := range atsPatterns {
		if strings.HasSuffix(host, a.domain) {
			sub := strings.TrimSuffix(host, "."+a.domain)
			if sub == host {
				return "", "", "", true // skip bare ATS domain inherently
			}

			// General operational subdomains.
			blockedSubs := []string{
				"www", "api", "app", "developers", "support", "help", "blog",
				"docs", "status", "careers", "about", "press", "news",
				"login", "accounts", "dashboard",
			}
			for _, bs := range blockedSubs {
				if sub == bs || strings.HasPrefix(sub, bs) && (bs == "app" || bs == "api") {
					// Exception: "boards.greenhouse.io" is allowed below
					if sub != "boards" || a.atsName != "greenhouse" {
						return "", "", "", true
					}
				}
			}

			// When ATS uses unified paths (e.g boards.greenhouse.io/stripe)
			if sub == "boards" || sub == "jobs" || sub == "careers" || sub == "career" || sub == "careers-page" {
				if len(parts) > 0 && parts[0] != "jobs" && parts[0] != "careers" && parts[0] != "careersection" {
					board = parts[0]
					return fmt.Sprintf("https://%s/%s", host, board), a.atsName, board, false
				}
				// Cannot infer company from just boards.greenhouse.io/jobs/123 - abort
				return "", "", "", true
			}

			// Typical subdomain mode (e.g stripe.greenhouse.io)
			board = sub
			return fmt.Sprintf("https://%s", host), a.atsName, board, false
		}
	}

	// Normal company
	if len(parts) == 0 || parts[0] == "" {
		return fmt.Sprintf("https://%s", host), "", "", false
	}

	keep := 1
	if len(parts) > 1 && (parts[0] == "en" || parts[0] == "uk" || parts[0] == "us" || parts[0] == "corporate") {
		keep = 2
	}

	newPath := strings.Join(parts[:keep], "/")
	return fmt.Sprintf("https://%s/%s", host, newPath), "", "", false
}

func main() {
	isTest := os.Getenv("TEST_RUN") != ""
	isDebug := os.Getenv("DEBUG") == "1"

	limit := "1000"
	if isTest {
		limit = "100"
	}

	seen := make(map[string]bool)
	var outFlat []string
	var outRich []RichURL

	patterns := make([]string, 0)
	for _, a := range atsPatterns {
		patterns = append(patterns, a.pattern)
	}
	for _, cp := range careerPatterns {
		patterns = append(patterns, cp)
	}

	if isTest && len(patterns) > 3 {
		patterns = patterns[:3]
	}

	client := &http.Client{Timeout: 30 * time.Second}

	for _, pat := range patterns {
		fmt.Printf("Querying pattern %s...\n", pat)
		u := fmt.Sprintf("https://index.commoncrawl.org/CC-MAIN-2024-51-index?url=%s&output=json&limit=%s", url.QueryEscape(pat), limit)

		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			log.Printf("invalid request: %v", err)
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("request failed: %v", err)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if resp.StatusCode != 200 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			log.Printf("Non-200 response for %s: %d %s", pat, resp.StatusCode, string(bodyBytes))
			resp.Body.Close()
			time.Sleep(500 * time.Millisecond)
			continue
		}

		scanner := bufio.NewScanner(resp.Body)
		debugCount := 0
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var cc CCLine
			if err := json.Unmarshal([]byte(line), &cc); err != nil {
				continue
			}

			if isDebug && debugCount < 10 {
				fmt.Printf("[DEBUG RAW] %s\n", cc.URL)
				debugCount++
			}

			canonical, ats, board, skip := parseURL(cc.URL)
			if skip || canonical == "" {
				continue
			}

			if !seen[canonical] {
				seen[canonical] = true
				outFlat = append(outFlat, canonical)

				if ats != "" {
					outRich = append(outRich, RichURL{URL: canonical, ATS: ats, BoardName: board})
				} else {
					outRich = append(outRich, RichURL{URL: canonical})
				}
			}
		}

		resp.Body.Close()
		time.Sleep(500 * time.Millisecond)
	}

	// Write flat json
	flatBytes, _ := json.MarshalIndent(outFlat, "", "  ")
	_ = os.WriteFile("harvested_urls.json", flatBytes, 0644)

	// Write rich json
	richBytes, _ := json.MarshalIndent(outRich, "", "  ")
	_ = os.WriteFile("harvested_urls_with_ats.json", richBytes, 0644)

	fmt.Printf("\nDone! Harvested %d unique URLs.\n", len(outFlat))

	if isTest {
		fmt.Println("First 20 URLs:")
		for i := 0; i < 20 && i < len(outFlat); i++ {
			fmt.Println(outFlat[i])
		}
	}
}
