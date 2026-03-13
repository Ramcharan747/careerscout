package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type WorkdayEntry struct {
	Company string
	Board   string
}

type Checkpoint struct {
	ProcessedSlugs map[string]bool `json:"processed"`
}

func main() {
	entries, err := readEntries("workday_companies.txt")
	if err != nil {
		log.Fatalf("Failed to read workday_companies.txt: %v", err)
	}

	checkpointFile := "workday_checkpoint.json"
	processed := loadCheckpoint(checkpointFile)

	var toProcess []WorkdayEntry
	for _, e := range entries {
		key := e.Company + "|" + e.Board
		if !processed[key] {
			toProcess = append(toProcess, e)
		}
	}

	fmt.Printf("Total entries: %d | Already processed: %d | To process: %d\n",
		len(entries), len(processed), len(toProcess))

	if len(toProcess) == 0 {
		fmt.Println("All done!")
		return
	}

	outF, err := os.OpenFile("verified_workday.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal(err)
	}
	defer outF.Close()

	var wg sync.WaitGroup
	var outMu sync.Mutex
	var chkMu sync.Mutex

	outSeen := make(map[string]bool)

	workCh := make(chan WorkdayEntry, len(toProcess))
	for _, e := range toProcess {
		workCh <- e
	}
	close(workCh)

	var (
		checkedCount   int32
		confirmedCount int32
		totalChecked   int32
	)

	// Worker Pool
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 15 * time.Second}

			for entry := range workCh {
				envsToTry := []string{"wd1", "wd3", "wd5", "wd12"}
				boardsToTry := []string{entry.Board, "External_Careers", "External", "careers", "Careers", "jobs", "Jobs", "hiring", "opportunities"}
				seenBoards := make(map[string]bool)

				var jobCount int
				var confirmedBoard string
				var confirmedURL string
				confirmed := false

				for _, env := range envsToTry {
					for _, board := range boardsToTry {
						if seenBoards[env+"|"+strings.ToLower(board)] {
							continue
						}
						seenBoards[env+"|"+strings.ToLower(board)] = true

						apiURL := fmt.Sprintf("https://%s.%s.myworkdayjobs.com/wday/cxs/%s/%s/jobs",
							entry.Company, env, entry.Company, board)

						payload := []byte(`{"limit":20,"offset":0,"searchText":"","locations":[],"workdayCategory":[]}`)
						req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(payload))
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Accept", "application/json")
						req.Header.Set("User-Agent", "Mozilla/5.0")

						resp, err := client.Do(req)

						if err == nil {
							if resp.StatusCode == 200 {
								body, _ := io.ReadAll(resp.Body)
								var res struct {
									JobPostings []interface{} `json:"jobPostings"`
									Total       int           `json:"total"`
								}
								if err := json.Unmarshal(body, &res); err == nil {
									jc := len(res.JobPostings)
									if jc > 0 || res.Total > 0 {
										confirmed = true
										jobCount = jc
										if res.Total > jobCount {
											jobCount = res.Total
										}
										confirmedBoard = board
										confirmedURL = apiURL
									}
								}
							}
							resp.Body.Close()
						}
						if confirmed {
							break
						}
					}
					if confirmed {
						break
					}
				}

				if confirmed {
					key := entry.Company + "|" + strings.ToLower(confirmedBoard)
					
					outMu.Lock()
					if !outSeen[key] {
						outSeen[key] = true
						atomic.AddInt32(&confirmedCount, 1)
						outF.WriteString(fmt.Sprintf("%s|%s|%s|%d\n", entry.Company, confirmedBoard, confirmedURL, jobCount))
					}
					outMu.Unlock()
				}

				c := atomic.AddInt32(&checkedCount, 1)
				atomic.AddInt32(&totalChecked, 1)

				chkMu.Lock()
				processed[entry.Company+"|"+entry.Board] = true
				if c >= 500 {
					atomic.StoreInt32(&checkedCount, 0)
					saveCheckpoint(checkpointFile, processed)
				}
				chkMu.Unlock()
			}
		}()
	}

	// Progress ticker
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		start := time.Now()
		for range ticker.C {
			tc := atomic.LoadInt32(&totalChecked)
			cc := atomic.LoadInt32(&confirmedCount)
			rem := len(toProcess) - int(tc)
			rate := float64(tc) / time.Since(start).Seconds()

			fmt.Printf("[%s] Checked: %d/%d | Confirmed: %d | Rate: %.1f/s | ETA: %.1f min\n",
				time.Now().Format("15:04:05"), tc, len(toProcess), cc, rate, float64(rem)/rate/60)
		}
	}()

	wg.Wait()
	ticker.Stop()
	chkMu.Lock()
	saveCheckpoint(checkpointFile, processed)
	chkMu.Unlock()

	fmt.Printf("\nDone! Checked: %d | Confirmed: %d\n", totalChecked, confirmedCount)
}

func readEntries(filepath string) ([]WorkdayEntry, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []WorkdayEntry
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 2 {
			comp := parts[0]
			board := parts[1]
			key := comp + "|" + strings.ToLower(board)
			if !seen[key] {
				seen[key] = true
				entries = append(entries, WorkdayEntry{Company: comp, Board: board})
			}
		}
	}

	hardcoded := []string{"nike", "amazon", "google", "microsoft", "apple", "meta", "salesforce", "oracle", "ibm", "jpmorgan", "wellsfargo", "bankofamerica", "disney", "boeing", "ford", "gm", "walmart", "target", "homedepot"}
	for _, comp := range hardcoded {
		key := comp + "|external_careers"
		if !seen[key] {
			seen[key] = true
			entries = append(entries, WorkdayEntry{Company: comp, Board: "External_Careers"})
		}
	}

	return entries, scanner.Err()
}

func loadCheckpoint(filepath string) map[string]bool {
	b, err := os.ReadFile(filepath)
	if err != nil {
		return make(map[string]bool)
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return make(map[string]bool)
	}
	if cp.ProcessedSlugs == nil {
		cp.ProcessedSlugs = make(map[string]bool)
	}
	return cp.ProcessedSlugs
}

func saveCheckpoint(filepath string, processed map[string]bool) {
	b, _ := json.Marshal(Checkpoint{ProcessedSlugs: processed})
	os.WriteFile(filepath, b, 0644)
}
