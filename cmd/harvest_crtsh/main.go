package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/careerscout/careerscout/internal/atsprober"
	"github.com/jackc/pgx/v5/pgxpool"
)

var atsDomains = []struct {
	domain  string
	atsName string
}{
	{"greenhouse.io", "greenhouse"},
	{"lever.co", "lever"},
	{"ashbyhq.com", "ashby"},
	{"workable.com", "workable"},
	{"bamboohr.com", "bamboohr"},
	{"recruitee.com", "recruitee"},
	{"teamtailor.com", "teamtailor"},
	{"pinpointhq.com", "pinpoint"},
	{"freshteam.com", "freshteam"},
	{"smartrecruiters.com", "smartrecruiters"},
	{"rippling.com", "rippling"},
}

var blockedSubstrings = []string{
	"prod-", "staging", "dev-", "test-", "uuid",
	"access-proxy", "use1-", "usw2-", "apse", "euw",
	"internal", "infra", "monitoring", "loadbalancer",
}

var blockedExact = []string{
	"www", "app", "api", "support", "inside", "brand",
	"grow", "boards", "job-boards", "developers", "help",
	"docs", "status", "careers", "about", "press", "login",
	"accounts", "dashboard", "blog", "marketing", "sales",
	"demo", "sandbox", "preview", "cdn", "static", "assets",
	"media", "images", "mail", "smtp", "vpn", "proxy",
}

var slugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{1,49}[a-z0-9]$`)

type Certificate struct {
	NameValue string `json:"name_value"`
}

func fetchCerts(domain string) ([]Certificate, error) {
	url := fmt.Sprintf("https://crt.sh/?q=%%25.%s&output=json", domain)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "CareerScout/1.0 (+https://careerscout.io/bot)")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("crt.sh HTTP %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var certs []Certificate
	if err := json.Unmarshal(body, &certs); err != nil {
		return nil, err
	}
	return certs, nil
}

func isBlockedSlug(slug string) (bool, string) {
	// Digits check
	if len(slug) > 0 && slug[0] >= '0' && slug[0] <= '9' {
		return true, "starts with digit"
	}
	// Hyphen count check
	if strings.Count(slug, "-") > 3 {
		return true, ">3 hyphens"
	}
	// Regex check
	if !slugRegex.MatchString(slug) {
		return true, "regex mismatch"
	}
	// Substrings check
	for _, sub := range blockedSubstrings {
		if strings.Contains(slug, sub) {
			return true, "contains " + sub
		}
	}
	// Exact matches check
	for _, exact := range blockedExact {
		if slug == exact {
			return true, "exact " + exact
		}
	}
	return false, ""
}

func main() {
	filter := os.Getenv("ATS_FILTER")
	
	// Open global results appending file
	resFile, err := os.OpenFile("crtsh_results.json", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal(err)
	}
	defer resFile.Close()
	var resFileMu sync.Mutex

	// Database init
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://careerscout:careerscout_dev_password@127.0.0.1:5432/careerscout"
	}
	dbpool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Printf("WARN: database connection failed: %v", err)
	} else {
		defer dbpool.Close()
	}

	// Load global checkpoint
	checkpoint := make(map[string]bool)
	var chkMu sync.RWMutex
	if data, err := os.ReadFile("crtsh_checkpoint.json"); err == nil {
		json.Unmarshal(data, &checkpoint)
	}

	// Adjust limiters down for shared tool profile
	for _, d := range atsDomains {
		atsprober.SetRateLimit(d.atsName, 3, 3)
	}

	// Background Checkpointer
	go func() {
		for {
			time.Sleep(30 * time.Second)
			chkMu.RLock()
			copy := make(map[string]bool, len(checkpoint))
			for k, v := range checkpoint {
				copy[k] = v
			}
			chkMu.RUnlock()

			if b, err := json.Marshal(copy); err == nil {
				os.WriteFile("crtsh_checkpoint.json", b, 0644)
			}
		}
	}()

	fmt.Println("--- Starting crt.sh harvest ---")

	type Summary struct {
		Raw   int
		Valid int
		Conf  int
	}
	summaries := make(map[string]*Summary)

	// Build the router mapping name to probe func
	proberMap := map[string]func(string) atsprober.ProbeResult{
		"greenhouse":      atsprober.ProbeGreenhouse,
		"lever":           atsprober.ProbeLever,
		"ashby":           atsprober.ProbeAshby,
		"workable":        atsprober.ProbeWorkable,
		"bamboohr":        atsprober.ProbeBambooHR,
		"recruitee":       atsprober.ProbeRecruitee,
		"teamtailor":      atsprober.ProbeTeamtailor,
		"rippling":        atsprober.ProbeRippling,
		"pinpoint":        atsprober.ProbePinpoint,
		"freshteam":       atsprober.ProbeFreshteam,
		"smartrecruiters": atsprober.ProbeSmartRecruiters,
	}

	overallStart := time.Now()
	var overallValid int32
	var overallConf int32

	for _, ats := range atsDomains {
		if filter != "" && filter != ats.atsName {
			continue
		}

		fmt.Printf("Querying crt.sh for %s...\n", ats.domain)
		certs, err := fetchCerts(ats.domain)
		if err != nil {
			log.Printf("ERROR: %s crt.sh query failed: %v\n", ats.domain, err)
			continue
		}

		debug := os.Getenv("DEBUG") == "1"
		if debug && (filter == "" || filter == ats.atsName) {
			fmt.Println("\n--- DEBUG: First 20 raw certs ---")
			for i, cert := range certs {
				if i >= 20 {
					break
				}
				fmt.Printf("RAW %d: %s\n", i, cert.NameValue)
			}
			fmt.Println("---------------------------------")
		}

		time.Sleep(1 * time.Second)

		validSlugs := make(map[string]bool)
		droppedCount := 0
		for _, cert := range certs {
			// They can be returned separated by \n within the name_value field
			for _, line := range strings.Split(cert.NameValue, "\n") {
				line = strings.TrimSpace(line)
				line = strings.TrimPrefix(line, "*.")
				line = strings.TrimSuffix(line, "."+ats.domain)
				
				if blocked, reason := isBlockedSlug(line); blocked {
					if debug {
						fmt.Printf("DROPPED %-30s | Reason: %s\n", line, reason)
						droppedCount++
					}
				} else {
					validSlugs[line] = true
				}
			}
		}

		numValid := len(validSlugs)
		fmt.Printf("%s: %d raw certs → %d valid slugs\n", ats.domain, len(certs), numValid)
		
		summaries[ats.atsName] = &Summary{
			Raw:   len(certs),
			Valid: numValid,
			Conf:  0,
		}

		if numValid == 0 {
			continue
		}

		var wg sync.WaitGroup
		slugCh := make(chan string, numValid)
		
		atomic.AddInt32(&overallValid, int32(numValid))

		var processed int32
		var confirmed int32

		for s := range validSlugs {
			// Compose a globally unique deduplication key per ATS + slug combination.
			chkKey := ats.atsName + ":" + s
			chkMu.RLock()
			seen := checkpoint[chkKey]
			chkMu.RUnlock()
			if !seen {
				slugCh <- s
			} else {
				atomic.AddInt32(&processed, 1)
			}
		}
		close(slugCh)

		stopProgress := make(chan struct{})
		go func() {
			for {
				select {
				case <-stopProgress:
					return
				case <-time.After(15 * time.Second):
					p := atomic.LoadInt32(&processed)
					c := atomic.LoadInt32(&confirmed)
					ratePerSec := float64(p) / time.Since(overallStart).Seconds()
					fmt.Printf("[%s] %d/%d verified | confirmed: %d | rate: %.1f/sec\n", ats.atsName, p, numValid, c, ratePerSec)
				}
			}
		}()

		workerCount := 30
		for i := 0; i < workerCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				probeFunc, ok := proberMap[ats.atsName]
				if !ok {
					return
				}

				for slug := range slugCh {
					res := probeFunc(slug)
					if res.Confirmed {
						atomic.AddInt32(&confirmed, 1)
						atomic.AddInt32(&overallConf, 1)

						resFileMu.Lock()
						if b, err := json.Marshal(res); err == nil {
							resFile.Write(append(b, '\n'))
						}
						resFileMu.Unlock()

						if os.Getenv("PROBE_TEST") == "1" || filter != "" {
							fmt.Printf("[HIT] %-20s -> %-12s (Jobs: %d) domain: %s\n",
								slug, res.ATSName, res.JobCount, res.Domain)
						}
						
						atsprober.SaveResult(context.Background(), dbpool, res, "crtsh")
					}
					
					chkKey := ats.atsName + ":" + slug
					chkMu.Lock()
					checkpoint[chkKey] = true
					chkMu.Unlock()

					atomic.AddInt32(&processed, 1)
				}
			}()
		}

		wg.Wait()
		close(stopProgress)
		summaries[ats.atsName].Conf = int(confirmed)
	}

	// Final explicit dump of checkpoint state upon completion.
	chkMu.RLock()
	if b, err := json.Marshal(checkpoint); err == nil {
		os.WriteFile("crtsh_checkpoint.json", b, 0644)
	}
	chkMu.RUnlock()

	// Output summary table.
	fmt.Printf("\n--- Harvest Results ---\n")
	fmt.Printf("%-18s %-12s %-12s %-12s %-10s\n", "ATS", "Raw Certs", "Valid Slugs", "Confirmed", "Hit Rate")
	var tRaw, tValid, tConf int
	for _, d := range atsDomains {
		if filter != "" && filter != d.atsName {
			continue
		}
		sum := summaries[d.atsName]
		if sum == nil {
			continue
		}
		tRaw += sum.Raw
		tValid += sum.Valid
		tConf += sum.Conf
		rate := 0.0
		if sum.Valid > 0 {
			rate = (float64(sum.Conf) / float64(sum.Valid)) * 100
		}
		fmt.Printf("%-18s %-12d %-12d %-12d %.1f%%\n", d.atsName, sum.Raw, sum.Valid, sum.Conf, rate)
	}
	
	totalRate := 0.0
	if tValid > 0 {
		totalRate = (float64(tConf) / float64(tValid)) * 100
	}
	fmt.Printf("%-18s %-12d %-12d %-12d %.1f%%\n", "TOTAL", tRaw, tValid, tConf, totalRate)
}
