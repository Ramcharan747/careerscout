package main

import (
"fmt"
"io"
"net/http"
"time"

"github.com/careerscout/careerscout/internal/jobparser"
)

func fetch(ats, url string) {
	fmt.Printf("\n=== %s ===\n", ats)
	req, _ := http.NewRequest("GET", url, nil)
	// Simplified just to get headers right for simple GETs
	req.Header.Set("User-Agent", "CareerScoutBot/1.0")
	req.Header.Set("Accept", "application/json")
	
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	jobs, _, err := jobparser.Parse(ats, body, url, "")
	if err != nil {
		fmt.Println("Parse Error:", err)
		return
	}
	if len(jobs) == 0 {
		fmt.Println("No jobs parsed")
		return
	}
	for i, j := range jobs {
		if i >= 2 { break }
		fmt.Printf("Job %d: %s \n  Location: %s | URL: %s\n", i+1, j.Title, j.LocationRaw, j.ApplyURL)
	}
}

func main() {
	fetch("greenhouse", "https://boards-api.greenhouse.io/v1/boards/1021creative/jobs?content=true")
	fetch("lever", "https://api.lever.co/v0/postings/100ms?mode=json")
	fetch("smartrecruiters", "https://api.smartrecruiters.com/v1/companies/01systems/postings")
	fetch("recruitee", "https://10xcrew.recruitee.com/api/offers")
	fetch("freshteam", "https://10times.freshteam.com/hire/widgets/jobs.json")
}
