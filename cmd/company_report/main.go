// Command company_report answers the question the job rows do not: is this firm
// worth writing to?
//
// A per-job list drops every firm with nothing posted, which for private equity
// is most of them — a fifteen-person fund rarely has an opening up. But the same
// page often says "we are always interested in exceptional people, send us your
// CV", or names an internship programme, or gives a hiring address. Those are
// doors, and for someone writing to a partner directly they can be better doors
// than a posted role with four hundred applicants.
//
// So this rolls the archive up to one row per firm, merging every page crawled
// for it at every depth, records which doors exist, and joins the firm profile
// back on so the list can be worked in a sensible order.
//
// Input:  html_store/  +  a firm list (investor or funds CSV)
// Output: company_doors.csv, one row per firm
//
// Env:
//
//	STORE_DIR   archive directory (default html_store)
//	FIRMS       firm list with a domain column (default funds_crawl_list.csv)
//	OUTPUT      report path (default company_doors.csv)
package main

import (
	"compress/gzip"
	"encoding/csv"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/careerscout/careerscout/internal/careerparser"
)

var depthKeyRe = regexp.MustCompile(`^(.*)__l(\d+)_(\d+)$`)

func splitKey(key string) (string, int) {
	if m := depthKeyRe.FindStringSubmatch(key); m != nil {
		d, _ := strconv.Atoi(m[2])
		return m[1], d
	}
	return key, 1
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type firm struct {
	name, kind, country, aum, velocity, portfolio string
}

func readFirms(path string) map[string]firm {
	out := map[string]firm{}
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil || len(recs) < 2 {
		log.Fatalf("read %s: %v", path, err)
	}
	idx := map[string]int{}
	for i, h := range recs[0] {
		idx[strings.TrimSpace(strings.ToLower(h))] = i
	}
	get := func(rec []string, k string) string {
		if i, ok := idx[k]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	for _, rec := range recs[1:] {
		d := strings.ToLower(get(rec, "domain"))
		if d == "" {
			continue
		}
		out[d] = firm{get(rec, "name"), get(rec, "investor_type"), get(rec, "hq_country"),
			get(rec, "aum"), get(rec, "deal_velocity"), get(rec, "portfolio_n")}
	}
	log.Printf("firms: %d from %s", len(out), path)
	return out
}

func readGz(path string) (html, pageURL string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return "", "", false
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		return "", "", false
	}
	return string(b), zr.Comment, true
}

type agg struct {
	door       careerparser.Door
	pages      int
	maxDepth   int
	bestURL    string // the page that carried the strongest door
	sampleJobs []string
}

// rank orders the list the way it should be worked: a live opening first, then
// a named internship programme, then an invitation to apply anyway.
func rank(d careerparser.Door) int {
	switch d.Best() {
	case "open_roles_incl_internship":
		return 0
	case "open_roles":
		return 1
	case "internship_programme":
		return 2
	case "speculative_form":
		return 3
	case "speculative":
		return 4
	case "upload_form":
		return 5
	case "email_only":
		return 6
	case "none":
		return 7
	}
	return 8
}

func main() {
	storeDir := env("STORE_DIR", "html_store")
	firms := readFirms(env("FIRMS", "funds_crawl_list.csv"))
	outPath := env("OUTPUT", "company_doors.csv")

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		log.Fatalf("read %s: %v", storeDir, err)
	}

	byFirm := map[string]*agg{}
	var scanned int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html.gz") {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".html.gz")
		dom, depth := splitKey(key)
		dom = strings.ToLower(dom)
		if _, ok := firms[dom]; !ok {
			continue
		}
		html, pageURL, ok := readGz(filepath.Join(storeDir, e.Name()))
		if !ok {
			continue
		}
		scanned++

		d := careerparser.Doors(html, pageURL)
		a := byFirm[dom]
		if a == nil {
			// Start dead and let the first live page clear it, so a firm whose
			// only pages are parked is reported as parked rather than silent.
			a = &agg{door: careerparser.Door{Dead: true}}
			byFirm[dom] = a
		}
		before := rank(a.door)
		a.door = a.door.Merge(d)
		a.pages++
		if depth > a.maxDepth {
			a.maxDepth = depth
		}
		if rank(a.door) < before || a.bestURL == "" {
			if d.Actionable() || a.bestURL == "" {
				a.bestURL = pageURL
			}
		}
		if d.OpenRoles > 0 && len(a.sampleJobs) < 3 {
			for _, j := range careerparser.Extract(html, pageURL) {
				if len(a.sampleJobs) >= 3 {
					break
				}
				a.sampleJobs = append(a.sampleJobs, j.Title)
			}
		}
	}

	type outRow struct {
		dom string
		a   *agg
	}
	var rows []outRow
	for d, a := range byFirm {
		rows = append(rows, outRow{d, a})
	}
	sort.Slice(rows, func(i, j int) bool {
		ri, rj := rank(rows[i].a.door), rank(rows[j].a.door)
		if ri != rj {
			return ri < rj
		}
		if rows[i].a.door.OpenRoles != rows[j].a.door.OpenRoles {
			return rows[i].a.door.OpenRoles > rows[j].a.door.OpenRoles
		}
		return rows[i].dom < rows[j].dom
	})

	f, err := os.Create(outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"domain", "name", "investor_type", "hq_country",
		"aum", "deal_velocity", "portfolio_n",
		"door", "open_roles", "internship", "speculative", "cv_upload", "careers_email",
		"pages_crawled", "max_depth", "best_url", "sample_jobs"})

	counts := map[string]int{}
	b2 := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	for _, r := range rows {
		d, a := r.a.door, r.a
		fm := firms[r.dom]
		counts[d.Best()]++
		_ = w.Write([]string{r.dom, fm.name, fm.kind, fm.country,
			fm.aum, fm.velocity, fm.portfolio,
			d.Best(), strconv.Itoa(d.OpenRoles), b2(d.Internship), b2(d.Speculative),
			b2(d.CVUpload), d.CareersEmail,
			strconv.Itoa(a.pages), strconv.Itoa(a.maxDepth), a.bestURL,
			strings.Join(a.sampleJobs, " | ")})
	}

	log.Printf("DONE | %d archived pages | %d firms -> %s", scanned, len(rows), outPath)
	var kinds []string
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return counts[kinds[i]] > counts[kinds[j]] })
	var actionable int
	for _, k := range kinds {
		log.Printf("  %-28s %5d", k, counts[k])
		if k != "none" && k != "dead" {
			actionable += counts[k]
		}
	}
	log.Printf("  %-28s %5d  (%.0f%% of crawled firms)", "ACTIONABLE", actionable,
		100*float64(actionable)/float64(max(len(rows), 1)))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
