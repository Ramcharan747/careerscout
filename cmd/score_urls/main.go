package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type ScoredURL struct {
	URL     string   `json:"url"`
	Score   int      `json:"score"`
	Signals []string `json:"signals"`
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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

	// Connect to PG to fetch discovered and raw_captures logic
	dbURL := getEnv("DATABASE_URL", "")
	if dbURL == "" {
		host := getEnv("PGHOST", "127.0.0.1")
		port := getEnv("PGPORT", "5432")
		user := getEnv("PGUSER", "careerscout")
		pass := getEnv("PGPASSWORD", "careerscout_dev_password")
		dbname := getEnv("PGDATABASE", "careerscout")
		dbURL = fmt.Sprintf("postgres://%s:%s@%s:%s/%s", user, pass, host, port, dbname)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db connect failed: %v", err)
	}
	defer pool.Close()

	discovered := make(map[string]bool)
	rows, _ := pool.Query(ctx, "SELECT domain FROM discovery_records WHERE status = 'discovered'")
	for rows.Next() {
		var domain string
		if rows.Scan(&domain) == nil {
			discovered[domain] = true
		}
	}
	rows.Close()

	labeled := make(map[string]bool)
	rows2, _ := pool.Query(ctx, "SELECT domain FROM raw_captures WHERE label = 1")
	for rows2.Next() {
		var domain string
		if rows2.Scan(&domain) == nil {
			labeled[domain] = true
		}
	}
	rows2.Close()

	knownATS := []string{"greenhouse.io", "lever.co", "ashbyhq.com", "workable.com", "myworkdayjobs.com", "icims.com", "smartrecruiters.com", "bamboohr.com", "jobvite.com", "taleo.net", "successfactors.com", "pinpointhq.com", "recruitee.com", "teamtailor.com"}
	indianATS := []string{"keka.com", "darwinbox.in", "darwinbox.com", "zohorecruit.com", "springrecruit.com", "zohorecruit.in"}

	var results []ScoredURL
	dist := map[string]int{"Above 60": 0, "40 to 60": 0, "20 to 39": 0, "Below 20": 0}

	for _, raw := range rawURLs {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}

		host := strings.ToLower(u.Host)
		path := strings.ToLower(u.Path)
		baseDomain := strings.TrimPrefix(host, "www.")

		score := 0
		var signals []string

		// URL contains known ATS subdomain pattern: +40
		for _, ats := range knownATS {
			// e.g if host is stripe.greenhouse.io, ats is greenhouse.io
			if strings.HasSuffix(host, "."+ats) || host == ats {
				score += 40
				signals = append(signals, "Known Global ATS Subdomain (+40)")
				break
			}
		}

		// URL is a dedicated careers subdomain: +20
		if strings.HasPrefix(host, "careers.") || strings.HasPrefix(host, "jobs.") {
			score += 20
			signals = append(signals, "Dedicated Careers Subdomain (+20)")
		}

		// URL path contains /careers or /jobs: +15
		if strings.Contains(path, "/careers") || strings.Contains(path, "/jobs") {
			score += 15
			signals = append(signals, "Careers/Jobs contained in path (+15)")
		}

		// Indian ATS: +15
		for _, ats := range indianATS {
			if strings.HasSuffix(host, "."+ats) || host == ats {
				score += 15
				signals = append(signals, "Known Indian ATS (+15)")
				break
			}
		}

		if discovered[baseDomain] {
			score += 25
			signals = append(signals, "Previously Discovered API (+25)")
		}

		if labeled[baseDomain] {
			score += 20
			signals = append(signals, "Exists in label=1 Captures (+20)")
		}

		// Ensure bounding
		if score > 100 {
			score = 100
		}

		if score >= 60 {
			dist["Above 60"]++
		} else if score >= 40 {
			dist["40 to 60"]++
		} else if score >= 20 {
			dist["20 to 39"]++
		} else {
			dist["Below 20"]++
		}

		results = append(results, ScoredURL{
			URL:     raw,
			Score:   score,
			Signals: signals,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	outBytes, _ := json.MarshalIndent(results, "", "  ")
	_ = os.WriteFile("careers_urls_scored.json", outBytes, 0644)

	fmt.Println("Score Distribution:")
	fmt.Printf("  Above 60: %d\n", dist["Above 60"])
	fmt.Printf("  40 to 60: %d\n", dist["40 to 60"])
	fmt.Printf("  20 to 39: %d\n", dist["20 to 39"])
	fmt.Printf("  Below 20: %d\n", dist["Below 20"])
	fmt.Printf("\nTotal Scored: %d\n", len(results))
}
