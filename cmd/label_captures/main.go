package main

import (
	"context"
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

	var totalUnlabelled int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM raw_captures WHERE label IS NULL").Scan(&totalUnlabelled)
	if err != nil {
		log.Fatalf("count failed: %v", err)
	}

	if totalUnlabelled == 0 {
		fmt.Println("No unlabelled captures found!")
		return
	}

	rows, err := pool.Query(ctx, "SELECT id, domain, url, run_id, response_size, response_body FROM raw_captures WHERE label IS NULL ORDER BY domain, captured_at")
	if err != nil {
		log.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	type Capture struct {
		ID     int64
		Domain string
		URL    string
		RunID  string
		Size   int
		Body   string
	}
	var caps []Capture
	for rows.Next() {
		var c Capture
		rows.Scan(&c.ID, &c.Domain, &c.URL, &c.RunID, &c.Size, &c.Body)
		caps = append(caps, c)
	}
	rows.Close()

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	labelledThisSession := 0

	for i := 0; i < len(caps); i++ {
		c := caps[i]

		// Check if it was domain-skipped
		var isDomainSkipped bool
		err = pool.QueryRow(ctx, "SELECT label IS NOT NULL FROM raw_captures WHERE id = $1", c.ID).Scan(&isDomainSkipped)
		if err == nil && isDomainSkipped {
			continue
		}

		fmt.Print("\033[H\033[2J") // Clear screen

		previewLen := 1000
		if len(c.Body) < previewLen {
			previewLen = len(c.Body)
		}
		preview := c.Body[:previewLen]

		fmt.Printf("Domain:  %s\r\n", c.Domain)
		fmt.Printf("URL:     %s\r\n", c.URL)
		fmt.Printf("Size:    %d bytes\r\n", c.Size)
		fmt.Printf("Run:     %s\r\n\r\n", c.RunID)
		fmt.Printf("--- RESPONSE PREVIEW (first 1000 chars) ---\r\n")
		fmt.Printf("%s\r\n", preview)
		fmt.Printf("-------------------------------------------\r\n\r\n")
		fmt.Printf("[j] Job API   [n] Not a job API   [s] Skip   [d] Domain skip   [q] Quit\r\n")
		fmt.Printf("Progress: %d/%d overall, %d this session\r\n", (len(caps)-totalUnlabelled)+labelledThisSession, len(caps), labelledThisSession)

		b := make([]byte, 1)
		for {
			_, err = os.Stdin.Read(b)
			if err != nil {
				break
			}
			char := string(b)

			if char == "j" {
				pool.Exec(ctx, "UPDATE raw_captures SET label = 1 WHERE id = $1", c.ID)
				labelledThisSession++
				break
			} else if char == "n" {
				pool.Exec(ctx, "UPDATE raw_captures SET label = 0 WHERE id = $1", c.ID)
				labelledThisSession++
				break
			} else if char == "s" {
				// skip
				break
			} else if char == "d" {
				pool.Exec(ctx, "UPDATE raw_captures SET label = 0 WHERE domain = $1 AND label IS NULL", c.Domain)
				labelledThisSession++
				break
			} else if char == "q" || b[0] == 3 { // 3 is Ctrl+C
				fmt.Print("\033[H\033[2J")
				fmt.Printf("Saved and exited. Labelled %d this session.\r\n", labelledThisSession)
				return
			}
		}
	}

	fmt.Print("\033[H\033[2J")
	fmt.Printf("All caught up! Labelled %d this session.\r\n", labelledThisSession)
}
