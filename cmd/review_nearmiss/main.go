package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"
)

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func showPager(content string) {
	cmd := exec.Command("less", "-R")
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func main() {
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

	// Load near misses
	rows, err := pool.Query(ctx, `
		SELECT id, run_id, domain, url, response_content_type, response_body, response_size, url_score, body_score, final_confidence 
		FROM near_misses 
		WHERE label IS NULL 
		ORDER BY final_confidence DESC
	`)
	if err != nil {
		log.Fatalf("failed to query near_misses: %v", err)
	}

	type NearMiss struct {
		ID          int64
		RunID       string
		Domain      string
		URL         string
		ContentType *string
		Body        string
		Size        int
		URLScore    float64
		BodyScore   float64
		Confidence  float64
	}

	var misses []NearMiss
	for rows.Next() {
		var m NearMiss
		if err := rows.Scan(&m.ID, &m.RunID, &m.Domain, &m.URL, &m.ContentType, &m.Body, &m.Size, &m.URLScore, &m.BodyScore, &m.Confidence); err != nil {
			log.Fatalf("row scan failed: %v", err)
		}
		misses = append(misses, m)
	}
	rows.Close()

	if len(misses) == 0 {
		fmt.Println("No pending near-misses to review.")
		return
	}

	fmt.Printf("Loaded %d near-misses for review.\n", len(misses))

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	labelledCount := 0

	for i, m := range misses {
	render:
		// Clear screen
		fmt.Print("\033[H\033[2J")

		// Query domain context
		var otherCount int
		var sampleScores []string
		rowsCtx, errCtx := pool.Query(ctx, "SELECT final_confidence FROM near_misses WHERE domain = $1 AND label IS NULL AND id != $2 ORDER BY final_confidence DESC", m.Domain, m.ID)
		if errCtx == nil {
			for rowsCtx.Next() {
				var s float64
				if err := rowsCtx.Scan(&s); err == nil {
					otherCount++
					if len(sampleScores) < 3 {
						sampleScores = append(sampleScores, fmt.Sprintf("%.2f", s))
					}
				}
			}
			rowsCtx.Close()
		}

		var prettyJSON string
		var obj interface{}
		if err := json.Unmarshal([]byte(m.Body), &obj); err == nil {
			if b, err := json.MarshalIndent(obj, "", "  "); err == nil {
				prettyJSON = string(b)
			} else {
				prettyJSON = m.Body
			}
		} else {
			prettyJSON = m.Body
		}

		bodyPreview := prettyJSON
		if len(bodyPreview) > 3000 {
			bodyPreview = bodyPreview[:3000] + "\n... (truncated)"
		}

		cType := "unknown"
		if m.ContentType != nil {
			cType = *m.ContentType
		}

		fmt.Printf("Domain:        %s\r\n", m.Domain)
		if otherCount > 0 {
			fmt.Printf("Other misses:  %d (scores: %s)\r\n", otherCount, strings.Join(sampleScores, ", "))
		}
		fmt.Printf("URL:           %s\r\n", m.URL)
		fmt.Printf("Content-Type:  %s\r\n", cType)
		fmt.Printf("Scores:        URL=%.2f  Body=%.2f  Final=%.2f\r\n", m.URLScore, m.BodyScore, m.Confidence)
		fmt.Printf("Size:          %d bytes\r\n\r\n", m.Size)

		fmt.Print(strings.ReplaceAll(bodyPreview, "\n", "\r\n"))
		fmt.Print("\r\n\r\n")

		fmt.Printf("[j] Job API  [n] Not job API  [s] Skip  [?] Full body  [q] Quit\r\nRemaining: %d\r\n", len(misses)-i)

		var b [1]byte
		for {
			_, err := os.Stdin.Read(b[:])
			if err != nil {
				continue
			}
			c := b[0]

			if c == 'j' || c == 'n' || c == 's' || c == 'q' || c == '?' || c == 3 {
				if c == 3 || c == 'q' {
					fmt.Print("\033[H\033[2J")
					fmt.Printf("Exiting. Labelled %d items this session.\r\n", labelledCount)
					return
				} else if c == 's' {
					break
				} else if c == '?' {
					term.Restore(int(os.Stdin.Fd()), oldState)
					showPager(prettyJSON)
					oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
					if err != nil {
						panic(err)
					}
					goto render
				} else if c == 'j' {
					// Label=1
					_, _ = pool.Exec(ctx, `UPDATE near_misses SET label = 1 WHERE id = $1`, m.ID)
					_, _ = pool.Exec(ctx, `
						INSERT INTO raw_captures (run_id, domain, url, response_content_type, response_body, response_size, label, notes) 
						VALUES ($1, $2, $3, $4, $5, $6, 1, 'near_miss_review')
					`, m.RunID, m.Domain, m.URL, m.ContentType, m.Body, m.Size)
					labelledCount++
					break
				} else if c == 'n' {
					// Label=0
					_, _ = pool.Exec(ctx, `UPDATE near_misses SET label = 0 WHERE id = $1`, m.ID)
					_, _ = pool.Exec(ctx, `
						INSERT INTO raw_captures (run_id, domain, url, response_content_type, response_body, response_size, label, notes) 
						VALUES ($1, $2, $3, $4, $5, $6, 0, 'near_miss_review')
					`, m.RunID, m.Domain, m.URL, m.ContentType, m.Body, m.Size)
					labelledCount++
					break
				}
			}
		}
	}

	fmt.Print("\033[H\033[2J")
	fmt.Printf("All caught up. Labelled %d items this session.\r\n", labelledCount)
}
