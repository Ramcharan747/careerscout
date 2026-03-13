package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"
)

func getEnv(key string, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func main() {
	dbURL := getEnv("DATABASE_URL", "postgres://careerscout:careerscout_dev_password@127.0.0.1:5432/careerscout?sslmode=disable")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db connect failed: %v", err)
	}
	defer pool.Close()

	var totalUnlabelled, totalJobAPIs, totalNoise int
	pool.QueryRow(ctx, "SELECT count(*) FROM raw_captures WHERE label IS NULL").Scan(&totalUnlabelled)
	pool.QueryRow(ctx, "SELECT count(*) FROM raw_captures WHERE label = 1").Scan(&totalJobAPIs)
	pool.QueryRow(ctx, "SELECT count(*) FROM raw_captures WHERE label = 0").Scan(&totalNoise)

	rows, err := pool.Query(ctx, `
		SELECT id, domain, url, response_size, response_body
		FROM raw_captures
		WHERE label IS NULL
		AND response_body NOT ILIKE '%viewBox%'
		AND response_body NOT ILIKE '%xmlns="http://www.w3.org/2000/svg"%'
		AND url NOT ILIKE '%icon%'
		AND url NOT ILIKE '%chatbot-builds%'
		AND url NOT ILIKE '%.svg%'
		ORDER BY 
			CASE 
				WHEN (url ILIKE '%/job%' OR url ILIKE '%/career%' OR url ILIKE '%/position%' OR url ILIKE '%/opening%' OR url ILIKE '%/posting%' OR url ILIKE '%/hiring%') THEN 0
				WHEN response_body ILIKE '%"title"%' AND response_body ILIKE '%"location"%' THEN 1
				WHEN response_size > 5000 THEN 2
				ELSE 3
			END,
			response_size DESC
	`)
	if err != nil {
		log.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	sessionJobAPIs := 0
	sessionNoise := 0

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		log.Fatalf("MakeRaw failed: %v", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	for rows.Next() {
		var id string
		var domain, url, body string
		var size int
		if err := rows.Scan(&id, &domain, &url, &size, &body); err != nil {
			term.Restore(int(os.Stdin.Fd()), oldState)
			fmt.Printf("\nscan failed: %v\n", err)
			break
		}

		var currentLabel *int
		pool.QueryRow(ctx, "SELECT label FROM raw_captures WHERE id = $1", id).Scan(&currentLabel)
		if currentLabel != nil {
			continue // Already handled by bulk dismiss
		}

		var otherTotal, otherNoise, otherJobAPI int
		pool.QueryRow(ctx, `
			SELECT COUNT(*), 
			       COUNT(CASE WHEN label = 0 THEN 1 END), 
			       COUNT(CASE WHEN label = 1 THEN 1 END) 
			FROM raw_captures 
			WHERE domain = $1 AND id != $2`, domain, id).Scan(&otherTotal, &otherNoise, &otherJobAPI)

		term.Restore(int(os.Stdin.Fd()), oldState)

		fmt.Printf("\nSession: %d labelled (%d job APIs, %d noise) | Total: %d job APIs, %d noise, %d remaining\n",
			sessionJobAPIs+sessionNoise, sessionJobAPIs, sessionNoise, totalJobAPIs, totalNoise, totalUnlabelled)
		fmt.Printf("=====================================\n")
		fmt.Printf("Domain:  %s\n", domain)
		fmt.Printf("URL:     %s\n", url)
		fmt.Printf("Size:    %d bytes\n", size)
		fmt.Printf("Context: %d other captures from this domain — %d labelled noise, %d labelled job API\n", otherTotal, otherNoise, otherJobAPI)
		fmt.Printf("=====================================\n")
		fmt.Printf("RESPONSE PREVIEW:\n")

		var parsed interface{}
		var displayBody string
		if err := json.Unmarshal([]byte(body), &parsed); err == nil {
			pretty, err := json.MarshalIndent(parsed, "", "  ")
			if err == nil {
				displayBody = string(pretty)
			} else {
				displayBody = body
			}
		} else {
			displayBody = body
		}

		if len(displayBody) > 1500 {
			displayBody = displayBody[:1500] + "\n... (truncated)"
		}
		fmt.Printf("%s\n", displayBody)

		fmt.Printf("=====================================\n")
		fmt.Printf("[j] Job API  [n] Not job API  [s] Skip  [d] Dismiss domain  [q] Quit\n")

		term.MakeRaw(int(os.Stdin.Fd()))

		var label *int
		quit := false
		bulkDismissed := false

		for {
			b := make([]byte, 1)
			_, err := os.Stdin.Read(b)
			if err != nil {
				continue
			}
			char := b[0]
			if char == 'j' {
				val := 1
				label = &val
				break
			} else if char == 'n' {
				val := 0
				label = &val
				break
			} else if char == 's' {
				break
			} else if char == 'd' {
				term.Restore(int(os.Stdin.Fd()), oldState)
				var remainingDomain int
				pool.QueryRow(ctx, "SELECT COUNT(*) FROM raw_captures WHERE domain = $1 AND label IS NULL", domain).Scan(&remainingDomain)
				fmt.Printf("\nLabel all %d remaining captures from %s as noise? (y/n) ", remainingDomain, domain)
				term.MakeRaw(int(os.Stdin.Fd()))

				confirmed := false
				for {
					b2 := make([]byte, 1)
					if _, err := os.Stdin.Read(b2); err == nil {
						if b2[0] == 'y' || b2[0] == 'Y' {
							confirmed = true
							break
						} else if b2[0] == 'n' || b2[0] == 'N' {
							break
						}
					}
				}

				if confirmed {
					pool.Exec(ctx, "UPDATE raw_captures SET label = 0 WHERE domain = $1 AND label IS NULL", domain)
					totalUnlabelled -= remainingDomain
					sessionNoise += remainingDomain
					totalNoise += remainingDomain
					bulkDismissed = true
					break
				} else {
					term.Restore(int(os.Stdin.Fd()), oldState)
					fmt.Printf("\n[j] Job API  [n] Not job API  [s] Skip  [d] Dismiss domain  [q] Quit\n")
					term.MakeRaw(int(os.Stdin.Fd()))
				}
			} else if char == 'q' || char == 3 {
				quit = true
				break
			}
		}

		if quit {
			term.Restore(int(os.Stdin.Fd()), oldState)
			break
		}

		if bulkDismissed {
			continue
		}

		if label != nil {
			_, err := pool.Exec(ctx, "UPDATE raw_captures SET label = $1 WHERE id = $2", *label, id)
			if err != nil {
				term.Restore(int(os.Stdin.Fd()), oldState)
				fmt.Printf("\nupdate failed: %v\n", err)
				term.MakeRaw(int(os.Stdin.Fd()))
			} else {
				totalUnlabelled--
				if *label == 1 {
					sessionJobAPIs++
					totalJobAPIs++
				} else {
					sessionNoise++
					totalNoise++
				}
			}
		}
	}

	term.Restore(int(os.Stdin.Fd()), oldState)
	fmt.Printf("\nSession complete. Session: %d labelled (%d job APIs, %d noise)\n", sessionJobAPIs+sessionNoise, sessionJobAPIs, sessionNoise)
}
