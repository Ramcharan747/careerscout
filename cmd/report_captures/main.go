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

func getEnv(key string, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func main() {
	dbURL := getEnv("DATABASE_URL", "")
	if dbURL == "" {
		dbURL = "postgres://careerscout:careerscout_dev_password@127.0.0.1:5432/careerscout"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db connect failed: %v", err)
	}
	defer pool.Close()

	// SECTION 1 — Overall stats
	var totalCaptures, totalLabelled, jobAPICount, notJobAPICount, unlabelledCount, uniqueDomains, uniqueRuns int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM raw_captures").Scan(&totalCaptures)
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM raw_captures WHERE label IS NOT NULL").Scan(&totalLabelled)
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM raw_captures WHERE label = 1").Scan(&jobAPICount)
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM raw_captures WHERE label = 0").Scan(&notJobAPICount)
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM raw_captures WHERE label IS NULL").Scan(&unlabelledCount)
	pool.QueryRow(ctx, "SELECT COUNT(DISTINCT domain) FROM raw_captures").Scan(&uniqueDomains)
	pool.QueryRow(ctx, "SELECT COUNT(DISTINCT run_id) FROM raw_captures").Scan(&uniqueRuns)

	fmt.Println("=== SECTION 1 — Overall Stats ===")
	fmt.Printf("Total captures:      %d\n", totalCaptures)
	fmt.Printf("Total labelled:      %d\n", totalLabelled)
	fmt.Printf("Job APIs (label=1):  %d\n", jobAPICount)
	fmt.Printf("Not Job APIs (0):    %d\n", notJobAPICount)
	fmt.Printf("Unlabelled:          %d\n", unlabelledCount)
	fmt.Printf("Unique Domains:      %d\n", uniqueDomains)
	fmt.Printf("Unique Runs:         %d\n", uniqueRuns)
	fmt.Println()

	// SECTION 2 — Top 30 domains
	fmt.Println("=== SECTION 2 — Top 30 Domains by Capture Count ===")
	rows, err := pool.Query(ctx, `
		SELECT 
			domain, 
			COUNT(*) as total, 
			COUNT(label) as labelled, 
			SUM(CASE WHEN label = 1 THEN 1 ELSE 0 END) as job_api,
			MODE() WITHIN GROUP (ORDER BY url) as top_url
		FROM raw_captures
		GROUP BY domain
		ORDER BY total DESC
		LIMIT 30
	`)
	if err == nil {
		fmt.Printf("%-30s | %-6s | %-8s | %-7s | %s\n", "Domain", "Total", "Labelled", "Job API", "Top URL")
		fmt.Println(strings.Repeat("-", 100))
		for rows.Next() {
			var d string
			var t, l int
			var j *int
			var topURL string
			rows.Scan(&d, &t, &l, &j, &topURL)
			jVal := 0
			if j != nil {
				jVal = *j
			}
			fmt.Printf("%-30s | %-6d | %-8d | %-7d | %s\n", d, t, l, jVal, topURL)
		}
		rows.Close()
	}
	fmt.Println()

	// SECTION 3 — URL pattern analysis (label=1)
	fmt.Println("=== SECTION 3 — URL Pattern Analysis (label=1) ===")
	rows, err = pool.Query(ctx, "SELECT url FROM raw_captures WHERE label = 1")
	pathSegments := make(map[string]int)
	queryParams := make(map[string]int)
	if err == nil {
		for rows.Next() {
			var u string
			rows.Scan(&u)
			parsed, err := url.Parse(u)
			if err != nil {
				continue
			}
			segs := strings.Split(parsed.Path, "/")
			for _, s := range segs {
				if s != "" {
					pathSegments["/"+s]++
				}
			}
			for k := range parsed.Query() {
				queryParams[k]++
			}
		}
		rows.Close()

		printTopMap("Top 20 Path Segments", pathSegments, 20)
		printTopMap("Top 20 Query Parameters", queryParams, 20)
	}
	fmt.Println()

	// SECTION 4 — Response body field analysis (label=1)
	fmt.Println("=== SECTION 4 — Response Body Field Analysis (label=1) ===")
	rows, err = pool.Query(ctx, "SELECT response_body FROM raw_captures WHERE label = 1")
	fields := make(map[string]int)
	if err == nil {
		for rows.Next() {
			var body string
			rows.Scan(&body)
			var parsed interface{}
			err := json.Unmarshal([]byte(body), &parsed)
			if err != nil {
				continue
			}
			extractFields(parsed, "", fields)
		}
		rows.Close()
		printTopMap("Top 40 Field Names (root and 1 level deep)", fields, 40)
	}
	fmt.Println()

	// SECTION 5 — Near-miss candidates
	fmt.Println("=== SECTION 5 — Near-miss Candidates (label IS NULL, size > 2000) ===")
	rows, err = pool.Query(ctx, `
		SELECT domain, url, response_size 
		FROM raw_captures 
		WHERE label IS NULL AND response_size > 2000 
		ORDER BY response_size DESC 
		LIMIT 50
	`)
	if err == nil {
		fmt.Printf("%-25s | %-10s | %s\n", "Domain", "Size", "URL")
		fmt.Println(strings.Repeat("-", 100))
		for rows.Next() {
			var d, u string
			var s int
			rows.Scan(&d, &u, &s)
			fmt.Printf("%-25s | %-10d | %s\n", d, s, u)
		}
		rows.Close()
	}
}

type kv struct {
	k string
	v int
}

func printTopMap(title string, m map[string]int, limit int) {
	fmt.Println(title + ":")
	var sorted []kv
	for k, v := range m {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].v > sorted[j].v
	})
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	if len(sorted) == 0 {
		fmt.Println("  (none)")
	}
	for _, s := range sorted {
		fmt.Printf("  %-20s: %d\n", s.k, s.v)
	}
	fmt.Println()
}

func extractFields(val interface{}, prefix string, counts map[string]int) {
	// Only go 1 level deep. If prefix is not empty, we are at level 1.
	if m, ok := val.(map[string]interface{}); ok {
		for k, v := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			counts[key]++

			if prefix == "" { // only recurse if we are at root
				// if object, recurse
				if _, isMap := v.(map[string]interface{}); isMap {
					extractFields(v, k, counts)
				}
				// if array of objects, recurse into first element
				if arr, isArr := v.([]interface{}); isArr && len(arr) > 0 {
					if _, isMap := arr[0].(map[string]interface{}); isMap {
						extractFields(arr[0], k+"[]", counts)
					}
				}
			}
		}
	} else if arr, isArr := val.([]interface{}); isArr && len(arr) > 0 && prefix == "" {
		if _, isMap := arr[0].(map[string]interface{}); isMap {
			extractFields(arr[0], "[]", counts)
		}
	}
}
