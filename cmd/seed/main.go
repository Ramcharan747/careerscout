package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://careerscout:careerscout_dev_password@localhost:5432/careerscout?sslmode=disable"
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("failed to connect to db: %v", err)
	}
	defer pool.Close()

	// Determine input file — accept CLI arg or default to careers_urls.json
	inputFile := "careers_urls.json"
	isScored := false
	if _, err := os.Stat("careers_urls_scored.json"); err == nil {
		inputFile = "careers_urls_scored.json"
		isScored = true
	}
	if len(os.Args) > 1 {
		inputFile = os.Args[1]
		isScored = strings.HasSuffix(inputFile, "scored.json")
	}

	data, err := os.ReadFile(inputFile)
	if err != nil {
		log.Fatalf("failed to read %s: %v", inputFile, err)
	}

	var urls []string
	if isScored {
		var scored []struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(data, &scored); err != nil {
			log.Fatalf("failed to parse scored JSON from %s: %v", inputFile, err)
		}
		for _, s := range scored {
			urls = append(urls, s.URL)
		}
	} else {
		if err := json.Unmarshal(data, &urls); err != nil {
			log.Fatalf("failed to parse JSON from %s: %v", inputFile, err)
		}
	}

	inserted := 0
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		domain := u.Host

		tag, err := pool.Exec(context.Background(), `
			INSERT INTO companies (domain)
			VALUES ($1)
			ON CONFLICT (domain) DO NOTHING
		`, domain)
		if err != nil {
			log.Printf("error inserting %s: %v", domain, err)
			continue
		}
		if tag.RowsAffected() > 0 {
			inserted++
		}
	}

	fmt.Printf("Inserted %d new domains into companies table\n", inserted)
}
