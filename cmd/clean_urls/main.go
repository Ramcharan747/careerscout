package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
)

type RemovedEntry struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

func main() {
	data, err := os.ReadFile("careers_urls.json")
	if err != nil {
		log.Fatalf("failed to read careers_urls.json: %v", err)
	}

	var rawURLs []string
	if err := json.Unmarshal(data, &rawURLs); err != nil {
		log.Fatalf("failed to parse JSON: %v", err)
	}

	aggregators := []string{"glassdoor", "linkedin", "naukri", "indeed", "adzuna", "monster", "shine.com", "timesjobs", "iimjobs", "ziprecruiter", "simplyhired"}
	social := []string{"twitter.com", "facebook.com", "instagram.com", "youtube.com"}
	gov := []string{".gov", ".gov.in", ".mil", ".ac.in"}
	redirects := []string{"bit.ly", "t.co", "goo.gl", "ow.ly", "tinyurl.com", "is.gd", "buff.ly", "lnkd.in"}

	var cleanURLs []string
	var removed []RemovedEntry

	counts := map[string]int{
		"aggregator": 0,
		"social":     0,
		"government": 0,
		"redirect":   0,
		"duplicate":  0,
		"invalid":    0,
	}

	domainBest := make(map[string]string) // domain -> shortest URL string

	for _, raw := range rawURLs {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			removed = append(removed, RemovedEntry{URL: raw, Reason: "invalid"})
			counts["invalid"]++
			continue
		}

		host := strings.ToLower(u.Host)

		matched := false
		for _, agg := range aggregators {
			if strings.Contains(host, agg) {
				removed = append(removed, RemovedEntry{URL: raw, Reason: "aggregator (" + agg + ")"})
				counts["aggregator"]++
				matched = true
				break
			}
		}
		if matched {
			continue
		}

		for _, soc := range social {
			if strings.Contains(host, soc) {
				removed = append(removed, RemovedEntry{URL: raw, Reason: "social (" + soc + ")"})
				counts["social"]++
				matched = true
				break
			}
		}
		if matched {
			continue
		}

		for _, g := range gov {
			if strings.HasSuffix(host, g) {
				removed = append(removed, RemovedEntry{URL: raw, Reason: "government (" + g + ")"})
				counts["government"]++
				matched = true
				break
			}
		}
		if matched {
			continue
		}

		for _, r := range redirects {
			if strings.Contains(host, r) {
				removed = append(removed, RemovedEntry{URL: raw, Reason: "redirect (" + r + ")"})
				counts["redirect"]++
				matched = true
				break
			}
		}
		if matched {
			continue
		}

		// Handle duplicates keeping canonical (shortest)
		baseDomain := strings.TrimPrefix(host, "www.")
		if existing, ok := domainBest[baseDomain]; ok {
			if len(raw) < len(existing) {
				// new one is shorter, replace and mark old as removed
				removed = append(removed, RemovedEntry{URL: existing, Reason: "duplicate (keeping " + raw + ")"})
				counts["duplicate"]++
				domainBest[baseDomain] = raw
			} else {
				// old one is shorter, drop new
				removed = append(removed, RemovedEntry{URL: raw, Reason: "duplicate (keeping " + existing + ")"})
				counts["duplicate"]++
			}
		} else {
			domainBest[baseDomain] = raw
		}
	}

	for _, v := range domainBest {
		cleanURLs = append(cleanURLs, v)
	}

	cleanBytes, _ := json.MarshalIndent(cleanURLs, "", "  ")
	_ = os.WriteFile("careers_urls.json", cleanBytes, 0644)

	removedBytes, _ := json.MarshalIndent(removed, "", "  ")
	_ = os.WriteFile("careers_urls_removed.json", removedBytes, 0644)

	fmt.Printf("Initial count: %d\n", len(rawURLs))
	fmt.Printf("Clean count:   %d\n\n", len(cleanURLs))
	fmt.Println("Removed by category:")
	for k, v := range counts {
		fmt.Printf("  - %-12s: %d\n", k, v)
	}
	fmt.Printf("Total removed: %d\n", len(removed))
}
