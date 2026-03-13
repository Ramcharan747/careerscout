package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/csv"
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

// ── ATS Platform Definitions ─────────────────────────────────────────────────

type ATSConfig struct {
	Name          string
	CrtshDomain   string // If non-empty, use crt.sh subdomain harvest
	PathBased     bool   // true = slugs come from Majestic Million only
	ProbeFunc     func(string) atsprober.ProbeResult
}

var atsConfigs = []ATSConfig{
	{"greenhouse", "greenhouse.io", false, atsprober.ProbeGreenhouse},
	{"lever", "", true, atsprober.ProbeLever},
	{"ashby", "", true, atsprober.ProbeAshby},
	{"workable", "", true, atsprober.ProbeWorkable},
	{"bamboohr", "bamboohr.com", false, atsprober.ProbeBambooHR},
	{"recruitee", "recruitee.com", false, atsprober.ProbeRecruitee},
	{"teamtailor", "teamtailor.com", false, atsprober.ProbeTeamtailor},
	{"rippling", "rippling.com", false, atsprober.ProbeRippling},
	{"pinpoint", "pinpointhq.com", false, atsprober.ProbePinpoint},
	{"freshteam", "freshteam.com", false, atsprober.ProbeFreshteam},
	{"smartrecruiters", "", true, atsprober.ProbeSmartRecruiters},
}

// ── Company Result ───────────────────────────────────────────────────────────

type CompanyEntry struct {
	Slug     string `json:"slug"`
	ATS      string `json:"ats"`
	APIURL   string `json:"api_url"`
	Domain   string `json:"domain"`
	JobCount int    `json:"job_count"`
	Source   string `json:"source"` // "crtsh" or "majestic"
}

// ── Slug Validation ──────────────────────────────────────────────────────────

var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9\-]{1,49}$`)

var skipExact = map[string]bool{
	"www": true, "mail": true, "api": true, "app": true, "apps": true,
	"play": true, "maps": true, "docs": true, "drive": true, "support": true,
	"help": true, "blog": true, "shop": true, "store": true, "news": true,
	"forum": true, "static": true, "cdn": true, "media": true, "images": true,
	"staging": true, "dev": true, "test": true, "demo": true, "sandbox": true,
	"preview": true, "internal": true, "admin": true, "login": true,
	"dashboard": true, "status": true, "careers": true, "about": true,
	"boards": true, "job-boards": true, "developers": true, "brand": true,
	"press": true, "accounts": true, "marketing": true, "sales": true,
	"vpn": true, "proxy": true, "smtp": true, "ftp": true,
}

var blockedSubstrings = []string{
	"prod-", "staging-", "dev-", "test-", "uuid",
	"access-proxy", "use1-", "usw2-", "apse", "euw",
	"internal-", "infra-", "monitoring", "loadbalancer",
}

func isValidSlug(slug string) bool {
	if len(slug) < 3 || len(slug) > 50 {
		return false
	}
	if !slugRegex.MatchString(slug) {
		return false
	}
	if skipExact[slug] {
		return false
	}
	for _, sub := range blockedSubstrings {
		if strings.Contains(slug, sub) {
			return false
		}
	}
	// Drop all-digit slugs
	allDigit := true
	for _, c := range slug {
		if c < '0' || c > '9' {
			allDigit = false
			break
		}
	}
	if allDigit {
		return false
	}
	// Drop IDN encoded
	if strings.HasPrefix(slug, "xn--") {
		return false
	}
	return true
}

// ── crt.sh Harvest ───────────────────────────────────────────────────────────

type Certificate struct {
	NameValue string `json:"name_value"`
}

func fetchCertsForDomain(domain string) ([]string, error) {
	url := fmt.Sprintf("https://crt.sh/?q=%%25.%s&output=json", domain)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "CareerScout/1.0")

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

	slugs := make(map[string]bool)
	for _, cert := range certs {
		for _, line := range strings.Split(cert.NameValue, "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "*.")
			// Remove trailing .domain
			if idx := strings.Index(line, "."+domain); idx > 0 {
				line = line[:idx]
			}
			// Remove any remaining dots (subdomain nesting)
			if strings.Contains(line, ".") {
				parts := strings.Split(line, ".")
				line = parts[0]
			}
			line = strings.ToLower(line)
			if isValidSlug(line) {
				slugs[line] = true
			}
		}
	}

	result := make([]string, 0, len(slugs))
	for slug := range slugs {
		result = append(result, slug)
	}
	return result, nil
}

// ── Majestic Million Harvest ─────────────────────────────────────────────────

func downloadMajesticSlugs() ([]string, error) {
	fmt.Println("Downloading Majestic Million CSV...")
	resp, err := http.Get("https://downloads.majestic.com/majestic_million.csv")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	slugs := make(map[string]bool)
	reader := csv.NewReader(resp.Body)
	reader.Read() // skip header

	for {
		record, err := reader.Read()
		if err != nil {
			break
		}
		if len(record) < 3 {
			continue
		}
		domain := strings.ToLower(record[2]) // Domain column
		// Extract slug: remove TLD
		parts := strings.Split(domain, ".")
		if len(parts) < 2 {
			continue
		}
		slug := parts[0]
		if isValidSlug(slug) {
			slugs[slug] = true
		}
	}

	result := make([]string, 0, len(slugs))
	for slug := range slugs {
		result = append(result, slug)
	}
	fmt.Printf("Majestic Million: extracted %d valid slugs\n", len(result))
	return result, nil
}

// ── Checkpoint ───────────────────────────────────────────────────────────────

type Checkpoint struct {
	mu   sync.RWMutex
	data map[string]bool
	file string
}

func loadCheckpoint(file string) *Checkpoint {
	cp := &Checkpoint{data: make(map[string]bool), file: file}
	if raw, err := os.ReadFile(file); err == nil {
		json.Unmarshal(raw, &cp.data)
	}
	return cp
}

func (c *Checkpoint) Has(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data[key]
}

func (c *Checkpoint) Set(key string) {
	c.mu.Lock()
	c.data[key] = true
	c.mu.Unlock()
}

func (c *Checkpoint) Save() {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if b, err := json.Marshal(c.data); err == nil {
		os.WriteFile(c.file, b, 0644)
	}
}

func (c *Checkpoint) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.data)
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	filter := os.Getenv("ATS_FILTER")
	skipCrtsh := os.Getenv("SKIP_CRTSH") == "1"
	skipMajestic := os.Getenv("SKIP_MAJESTIC") == "1"
	workerCount := 50

	// DB connection (optional)
	var dbpool *pgxpool.Pool
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		var err error
		dbpool, err = pgxpool.New(context.Background(), dbURL)
		if err != nil {
			log.Printf("WARN: database connection failed: %v", err)
		} else {
			defer dbpool.Close()
		}
	}

	checkpoint := loadCheckpoint("harvest_companies_checkpoint.json")

	// Background checkpoint saver
	go func() {
		for {
			time.Sleep(30 * time.Second)
			checkpoint.Save()
		}
	}()

	// Per-ATS result accumulators
	type atsResults struct {
		mu      sync.Mutex
		entries []CompanyEntry
	}
	results := make(map[string]*atsResults)
	for _, cfg := range atsConfigs {
		results[cfg.Name] = &atsResults{}
	}

	// ── Phase 1: crt.sh for subdomain-based ATS ──────────────────────────
	if !skipCrtsh {
		fmt.Println("\n=== Phase 1: crt.sh Certificate Transparency Harvest ===")
		for _, cfg := range atsConfigs {
			if cfg.CrtshDomain == "" || cfg.PathBased {
				continue
			}
			if filter != "" && filter != cfg.Name {
				continue
			}

			fmt.Printf("\n--- %s: querying crt.sh for %%.%s ---\n", cfg.Name, cfg.CrtshDomain)
			slugs, err := fetchCertsForDomain(cfg.CrtshDomain)
			if err != nil {
				log.Printf("ERROR: crt.sh for %s: %v", cfg.CrtshDomain, err)
				time.Sleep(2 * time.Second)
				continue
			}
			fmt.Printf("%s: %d unique slugs from crt.sh\n", cfg.Name, len(slugs))

			// Filter already-checked slugs
			var toProbe []string
			for _, s := range slugs {
				key := cfg.Name + ":crtsh:" + s
				if !checkpoint.Has(key) {
					toProbe = append(toProbe, s)
				}
			}
			fmt.Printf("%s: %d new slugs to probe (%d already checked)\n", cfg.Name, len(toProbe), len(slugs)-len(toProbe))

			if len(toProbe) == 0 {
				time.Sleep(2 * time.Second)
				continue
			}

			// Probe workers
			slugCh := make(chan string, len(toProbe))
			for _, s := range toProbe {
				slugCh <- s
			}
			close(slugCh)

			var confirmed int32
			var processed int32
			total := int32(len(toProbe))

			var wg sync.WaitGroup
			wc := workerCount
			if wc > len(toProbe) {
				wc = len(toProbe)
			}

			stopProgress := make(chan struct{})
			go func() {
				for {
					select {
					case <-stopProgress:
						return
					case <-time.After(10 * time.Second):
						p := atomic.LoadInt32(&processed)
						c := atomic.LoadInt32(&confirmed)
						fmt.Printf("[%s crt.sh] %d/%d probed | confirmed: %d\n", cfg.Name, p, total, c)
					}
				}
			}()

			for i := 0; i < wc; i++ {
				wg.Add(1)
				go func(probeFn func(string) atsprober.ProbeResult, atsName string) {
					defer wg.Done()
					for slug := range slugCh {
						res := probeFn(slug)
						key := atsName + ":crtsh:" + slug
						checkpoint.Set(key)
						atomic.AddInt32(&processed, 1)

						if res.Confirmed {
							atomic.AddInt32(&confirmed, 1)
							entry := CompanyEntry{
								Slug:     slug,
								ATS:      atsName,
								APIURL:   res.APIURL,
								Domain:   res.Domain,
								JobCount: res.JobCount,
								Source:   "crtsh",
							}
							results[atsName].mu.Lock()
							results[atsName].entries = append(results[atsName].entries, entry)
							results[atsName].mu.Unlock()

							if dbpool != nil {
								atsprober.SaveResult(context.Background(), dbpool, res, "crtsh")
							}
						}
					}
				}(cfg.ProbeFunc, cfg.Name)
			}

			wg.Wait()
			close(stopProgress)
			fmt.Printf("%s: crt.sh done → %d confirmed\n", cfg.Name, confirmed)

			time.Sleep(2 * time.Second) // Be polite to crt.sh
		}
	}

	// ── Phase 2: Majestic Million brute-force ────────────────────────────
	if !skipMajestic {
		fmt.Println("\n=== Phase 2: Majestic Million Brute-Force Probing ===")
		majesticSlugs, err := downloadMajesticSlugs()
		if err != nil {
			log.Printf("ERROR: Majestic download failed: %v", err)
		} else {
			// For each ATS, probe all slugs
			for _, cfg := range atsConfigs {
				if filter != "" && filter != cfg.Name {
					continue
				}

				// Filter already-checked slugs
				var toProbe []string
				for _, s := range majesticSlugs {
					key := cfg.Name + ":majestic:" + s
					if !checkpoint.Has(key) {
						toProbe = append(toProbe, s)
					}
				}
				fmt.Printf("\n--- %s: %d Majestic slugs to probe (%d already checked) ---\n",
					cfg.Name, len(toProbe), len(majesticSlugs)-len(toProbe))

				if len(toProbe) == 0 {
					continue
				}

				slugCh := make(chan string, len(toProbe))
				for _, s := range toProbe {
					slugCh <- s
				}
				close(slugCh)

				var confirmed int32
				var processed int32
				total := int32(len(toProbe))
				start := time.Now()

				var wg sync.WaitGroup
				wc := workerCount
				if wc > len(toProbe) {
					wc = len(toProbe)
				}

				stopProgress := make(chan struct{})
				go func() {
					for {
						select {
						case <-stopProgress:
							return
						case <-time.After(10 * time.Second):
							p := atomic.LoadInt32(&processed)
							c := atomic.LoadInt32(&confirmed)
							dur := time.Since(start).Seconds()
							rate := float64(p) / dur
							rem := float64(total-p) / rate
							fmt.Printf("[%s majestic] %d/%d probed | confirmed: %d | %.1f/sec | ETA: %.1fmin\n",
								cfg.Name, p, total, c, rate, rem/60)
						}
					}
				}()

				for i := 0; i < wc; i++ {
					wg.Add(1)
					go func(probeFn func(string) atsprober.ProbeResult, atsName string) {
						defer wg.Done()
						for slug := range slugCh {
							res := probeFn(slug)
							key := atsName + ":majestic:" + slug
							checkpoint.Set(key)
							atomic.AddInt32(&processed, 1)

							if res.Confirmed {
								atomic.AddInt32(&confirmed, 1)
								entry := CompanyEntry{
									Slug:     slug,
									ATS:      atsName,
									APIURL:   res.APIURL,
									Domain:   res.Domain,
									JobCount: res.JobCount,
									Source:   "majestic",
								}
								results[atsName].mu.Lock()
								results[atsName].entries = append(results[atsName].entries, entry)
								results[atsName].mu.Unlock()

								if dbpool != nil {
									atsprober.SaveResult(context.Background(), dbpool, res, "majestic")
								}
							}
						}
					}(cfg.ProbeFunc, cfg.Name)
				}

				wg.Wait()
				close(stopProgress)
				fmt.Printf("%s: Majestic done → %d confirmed out of %d probed\n", cfg.Name, confirmed, total)
			}
		}
	}

	// ── Phase 3: Write per-ATS JSON files ────────────────────────────────
	fmt.Println("\n=== Writing per-ATS company lists ===")
	var totalCompanies int
	for _, cfg := range atsConfigs {
		if filter != "" && filter != cfg.Name {
			continue
		}
		res := results[cfg.Name]
		res.mu.Lock()
		entries := res.entries
		res.mu.Unlock()

		// Also load existing file and merge
		filename := fmt.Sprintf("companies_%s.json", cfg.Name)
		var existing []CompanyEntry
		if raw, err := os.ReadFile(filename); err == nil {
			json.Unmarshal(raw, &existing)
		}

		// Merge by slug dedup
		seen := make(map[string]bool)
		var merged []CompanyEntry
		for _, e := range existing {
			if !seen[e.Slug] {
				seen[e.Slug] = true
				merged = append(merged, e)
			}
		}
		for _, e := range entries {
			if !seen[e.Slug] {
				seen[e.Slug] = true
				merged = append(merged, e)
			}
		}

		if len(merged) > 0 {
			data, _ := json.MarshalIndent(merged, "", "  ")
			os.WriteFile(filename, data, 0644)
		}

		totalCompanies += len(merged)
		fmt.Printf("%-18s → %d companies (new: %d, existing: %d) → %s\n",
			cfg.Name, len(merged), len(entries), len(existing), filename)
	}

	// Final checkpoint save
	checkpoint.Save()

	fmt.Printf("\n=== Harvest Complete ===\n")
	fmt.Printf("Total companies discovered: %d\n", totalCompanies)
	fmt.Printf("Checkpoint entries: %d\n", checkpoint.Len())

	// Also write a combined list
	var allEntries []CompanyEntry
	for _, cfg := range atsConfigs {
		res := results[cfg.Name]
		res.mu.Lock()
		allEntries = append(allEntries, res.entries...)
		res.mu.Unlock()
	}
	if len(allEntries) > 0 {
		data, _ := json.MarshalIndent(allEntries, "", "  ")
		os.WriteFile("companies_all.json", data, 0644)
		fmt.Printf("Combined file: companies_all.json (%d entries)\n", len(allEntries))
	}
}

// Unused import suppression
var _ = gzip.NewReader
var _ = bufio.NewReader
var _ = csv.NewReader
