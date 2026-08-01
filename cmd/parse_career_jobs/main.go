// Command parse_career_jobs extracts job listings from the HTML archive built
// by cmd/fetch_career_html, using internal/careerparser.
//
// This is the non-ATS half of the pipeline. Firms small enough to hand-write
// their openings into HTML never appear in ATS slug resolution, and they are
// exactly the firms a targeted search cares about most.
//
// Because it reads a local archive rather than the network, it is safe to run
// repeatedly while tuning the parser.
//
// Input:  html_store/*.html.gz  (+ career_pages.csv for company metadata)
// Output: career_jobs.csv, one row per extracted opening
//
// Env:
//
//	STORE_DIR   archive directory (default html_store)
//	INPUT       discovery CSV for metadata (default career_pages.csv)
//	OUTPUT      results file (default career_jobs.csv)
//	MIN_CONF    drop rows below this confidence (default 0)
package main

import (
	"compress/gzip"
	"encoding/csv"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/careerscout/careerscout/internal/careerparser"
)

type meta struct{ Company, URL, ATS string }

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// readMeta indexes discovery output by domain so extracted jobs carry the
// company name and the ATS verdict alongside the listing.
func readMeta(path string) map[string]meta {
	out := map[string]meta{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return out
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.TrimSpace(strings.ToLower(h))] = i
	}
	get := func(rec []string, n string) string {
		if i, ok := idx[n]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if d := get(rec, "domain"); d != "" {
			out[d] = meta{get(rec, "company_name"), get(rec, "career_url"), get(rec, "ats_platform")}
		}
	}
	return out
}

func readGz(path string) (body string, comment string, name string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return "", "", "", err
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		return "", "", "", err
	}
	return string(b), zr.Comment, zr.Name, nil
}

type row struct {
	Company, Domain, CareerURL, ATS string
	Job                             careerparser.Job
}

func main() {
	storeDir := env("STORE_DIR", "html_store")
	outPath := env("OUTPUT", "career_jobs.csv")
	metaMap := readMeta(env("INPUT", "career_pages.csv"))
	minConf, _ := strconv.ParseFloat(env("MIN_CONF", "0"), 64)

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		log.Fatalf("read %s: %v", storeDir, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".html.gz") {
			files = append(files, filepath.Join(storeDir, e.Name()))
		}
	}
	log.Printf("archived pages: %d", len(files))

	var (
		mu     sync.Mutex
		out    []row
		parsed int
		empty  int
		byMeth = map[string]int{}
		sem    = make(chan struct{}, 8)
		wg     sync.WaitGroup
	)

	for _, path := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()
			// A malformed page must not take the run down.
			defer func() { _ = recover() }()

			html, pageURL, domain, err := readGz(p)
			if err != nil {
				return
			}
			if domain == "" {
				domain = strings.TrimSuffix(filepath.Base(p), ".html.gz")
			}
			m := metaMap[domain]
			if pageURL == "" {
				pageURL = m.URL
			}

			jobs := careerparser.Extract(html, pageURL)

			mu.Lock()
			defer mu.Unlock()
			parsed++
			if len(jobs) == 0 {
				empty++
				return
			}
			for _, j := range jobs {
				if j.Confidence < minConf {
					continue
				}
				byMeth[j.Method]++
				out = append(out, row{m.Company, domain, pageURL, m.ATS, j})
			}
		}(path)
	}
	wg.Wait()

	f, err := os.Create(outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"company_name", "domain", "career_url", "ats_platform",
		"job_title", "job_url", "location", "department", "posted", "method", "confidence"})
	for _, r := range out {
		_ = w.Write([]string{r.Company, r.Domain, r.CareerURL, r.ATS,
			r.Job.Title, r.Job.URL, r.Job.Location, r.Job.Department, r.Job.PostedAt,
			r.Job.Method, strconv.FormatFloat(r.Job.Confidence, 'f', 2, 64)})
	}

	log.Printf("DONE | pages parsed %d | pages with no jobs %d | jobs extracted %d", parsed, empty, len(out))
	for _, m := range []string{"jsonld", "links", "headings"} {
		if byMeth[m] > 0 {
			log.Printf("  %-9s %d", m, byMeth[m])
		}
	}
	log.Printf("-> %s", outPath)
}
