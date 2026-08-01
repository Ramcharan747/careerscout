// Command build_label_set produces hand-labelling sheets for the career-page
// parser, in two stages.
//
// The heuristic weights in internal/careerparser were set by hand and, measured
// against real pages, admit roughly three non-jobs for every job. Rather than
// guess at the weights again, these sheets create ground truth.
//
// Stage 1 — does this page list jobs at all?
//
//	One row per sampled page, with the top candidate's sample rows so most
//	pages are decidable without opening the URL. Mark label=1 if the page
//	lists openings, 0 if it does not (landing page, benefits blurb, an ATS
//	redirect, a press page).
//
// Stage 2 — is this extracted row actually a job?
//
//	Reads the completed stage-1 sheet, takes only the pages marked 1, runs the
//	parser over them, and emits one row per extracted job. Mark label=1 for a
//	real posting, 0 for furniture ("Sharp minds, good vibes").
//
// Splitting them isolates the two failure modes: stage 1 measures whether the
// page-level gate fires correctly, stage 2 measures extraction precision on
// pages that genuinely have a list. Tuning against a single blended number
// cannot separate the two, which is why hand-tuning stalled.
//
// Env:
//
//	STAGE       1 or 2 (default 1)
//	STORE_DIR   archive directory (default html_store)
//	JOBS        current parser output, for stratification (default career_jobs.csv)
//	LABELS      completed stage-1 sheet (stage 2 only, default page_labels.csv)
//	DOMAINS     restrict the sample to the domains in this CSV/TXT (optional)
//	OUTPUT      defaults to page_labels.csv (stage 1) / job_labels.csv (stage 2)
//	PAGES       pages to sample in stage 1 (default 120)
//	SEED        deterministic sample selection (default 1)
package main

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/careerscout/careerscout/internal/careerparser"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return d
}

var (
	titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	tagRe   = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)
	wsRe    = regexp.MustCompile(`\s+`)
)

func readGz(path string) (html, pageURL, domain string, err error) {
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

var jobWordRe = regexp.MustCompile(`(?i)(open position|current opening|current vacanc|` +
	`join our team|open role|we are hiring|we're hiring|apply now|job opening|` +
	`offene stellen|aktuelle stellen|vacature|offres d'emploi|lediga jobb|vacantes)`)

// previewAroundJobs returns a text window centred on the first job-related
// phrase, falling back to the head of the document when none is present.
func previewAroundJobs(html string, width int) string {
	text := strings.TrimSpace(wsRe.ReplaceAllString(tagRe.ReplaceAllString(html, " "), " "))
	loc := jobWordRe.FindStringIndex(text)
	if loc == nil {
		if len(text) > width {
			return text[:width]
		}
		return text
	}
	start := loc[0] - width/4
	if start < 0 {
		start = 0
	}
	end := start + width
	if end > len(text) {
		end = len(text)
	}
	return text[start:end]
}

func domOf(path, embedded string) string {
	if embedded != "" {
		return embedded
	}
	return strings.TrimSuffix(filepath.Base(path), ".html.gz")
}

// depthKeyRe splits the __l<depth>_<n> marker that cmd/expand_career_links
// appends, so a deep page keys back to the firm it belongs to and reports how
// many hops in it was found.
var depthKeyRe = regexp.MustCompile(`^(.*)__l(\d+)_(\d+)$`)

func splitKey(key string) (domain string, depth int) {
	if m := depthKeyRe.FindStringSubmatch(key); m != nil {
		d, _ := strconv.Atoi(m[2])
		return m[1], d
	}
	return key, 1
}

// readDomainFilter loads the "domain" column of a CSV, or one domain per line
// from a plain text file. Used to weight a labelling round towards a population
// that is under-represented in the archive as a whole.
func readDomainFilter(path string) map[string]bool {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil || len(recs) == 0 {
		log.Fatalf("read %s: %v", path, err)
	}
	col := 0
	for i, h := range recs[0] {
		if strings.EqualFold(strings.TrimSpace(h), "domain") {
			col = i
		}
	}
	out := map[string]bool{}
	for _, rec := range recs[1:] {
		if col < len(rec) {
			if d := strings.ToLower(strings.TrimSpace(rec[col])); d != "" {
				out[d] = true
			}
		}
	}
	log.Printf("domain filter: %d domains from %s", len(out), path)
	return out
}

func archive(storeDir string, only map[string]bool) []string {
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		log.Fatalf("read %s: %v", storeDir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html.gz") {
			continue
		}
		if only != nil {
			// A deep page inherits its parent firm's membership, otherwise the
			// pages furthest in — the ones most likely to hold the real list —
			// would be filtered out of every targeted sample.
			dom, _ := splitKey(strings.TrimSuffix(e.Name(), ".html.gz"))
			if !only[strings.ToLower(dom)] {
				continue
			}
		}
		out = append(out, filepath.Join(storeDir, e.Name()))
	}
	sort.Strings(out)
	return out
}

// ── stage 1 ──────────────────────────────────────────────────────────────────

func stage1(storeDir, outPath string, nPages int, seed int64, jobsCSV string, only map[string]bool) {
	yielding := map[string]bool{}
	if f, err := os.Open(jobsCSV); err == nil {
		r := csv.NewReader(f)
		r.LazyQuotes = true
		r.FieldsPerRecord = -1
		if hdr, err := r.Read(); err == nil {
			di := -1
			for i, h := range hdr {
				if strings.EqualFold(strings.TrimSpace(h), "domain") {
					di = i
				}
			}
			for di >= 0 {
				rec, err := r.Read()
				if err != nil {
					break
				}
				if di < len(rec) {
					yielding[strings.TrimSpace(rec[di])] = true
				}
			}
		}
		f.Close()
	}

	// Strata are (depth, yields-jobs). Depth matters as much as yield: a
	// landing page and the page three hops in behind "Search careers" fail in
	// different ways, and an archive is mostly landing pages, so an unstratified
	// sample is nearly all depth 1 and teaches nothing about the redirects.
	type stratum struct {
		depth  int
		yields bool
	}
	buckets := map[stratum][]string{}
	depths := map[int]bool{}
	for _, p := range archive(storeDir, only) {
		key := strings.TrimSuffix(filepath.Base(p), ".html.gz")
		_, d := splitKey(key)
		s := stratum{d, yielding[key]}
		buckets[s] = append(buckets[s], p)
		depths[d] = true
	}
	var ds []int
	for d := range depths {
		ds = append(ds, d)
	}
	sort.Ints(ds)
	for _, d := range ds {
		log.Printf("archive depth %d: %d yielding, %d not",
			d, len(buckets[stratum{d, true}]), len(buckets[stratum{d, false}]))
	}

	rng := rand.New(rand.NewSource(seed))
	pick := func(src []string, n int) []string {
		if len(src) <= n {
			return src
		}
		out := make([]string, 0, n)
		for _, i := range rng.Perm(len(src))[:n] {
			out = append(out, src[i])
		}
		return out
	}

	// Even split across depths, then half yielding and half not within each.
	// False positives live in one half, false negatives in the other; a sample
	// drawn only from successes teaches nothing.
	var sample []string
	perDepth := nPages / len(ds)
	for i, d := range ds {
		want := perDepth
		if i == len(ds)-1 {
			want = nPages - perDepth*(len(ds)-1)
		}
		got := append(pick(buckets[stratum{d, true}], want/2),
			pick(buckets[stratum{d, false}], want-want/2)...)
		// A shallow archive cannot fill a deep quota; spend the remainder on
		// depth 1 rather than shipping a short sheet.
		sample = append(sample, got...)
	}
	if len(sample) < nPages {
		pool := append(append([]string{}, buckets[stratum{1, true}]...),
			buckets[stratum{1, false}]...)
		have := map[string]bool{}
		for _, s := range sample {
			have[s] = true
		}
		var rest []string
		for _, p := range pool {
			if !have[p] {
				rest = append(rest, p)
			}
		}
		sample = append(sample, pick(rest, nPages-len(sample))...)
	}
	sort.Strings(sample)

	f, err := os.Create(outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"label", "domain", "depth", "page_url", "page_title",
		"parser_extracted", "n_groups", "best_score",
		"top_sample_1", "top_sample_2", "top_sample_3", "text_preview"})

	n := 0
	for _, p := range sample {
		html, pageURL, dom, err := readGz(p)
		if err != nil {
			continue
		}
		dom = domOf(p, dom)

		pageTitle := ""
		if m := titleRe.FindStringSubmatch(html); len(m) > 1 {
			pageTitle = strings.TrimSpace(wsRe.ReplaceAllString(m[1], " "))
		}
		// A window around the first job-related phrase, not the top of the
		// document — page tops are cookie banners and navigation, which say
		// nothing about whether openings are listed below.
		preview := previewAroundJobs(html, 500)

		cands := careerparser.Candidates(html, pageURL, 1, 3)
		best, nGroups := "", 0
		var s1, s2, s3 string
		if len(cands) > 0 {
			nGroups = len(cands)
			best = fmt.Sprintf("%.2f", cands[0].Feat.HeuristicScr)
			get := func(i int) string {
				if i < len(cands[0].Samples) {
					return cands[0].Samples[i]
				}
				return ""
			}
			s1, s2, s3 = get(0), get(1), get(2)
		}
		extracted := "no"
		if len(careerparser.Extract(html, pageURL)) > 0 {
			extracted = "yes"
		}

		_, depth := splitKey(dom)
		_ = w.Write([]string{"", dom, strconv.Itoa(depth), pageURL, pageTitle, extracted,
			strconv.Itoa(nGroups), best, s1, s2, s3, preview})
		n++
	}
	log.Printf("STAGE 1 | %d pages -> %s", n, outPath)
	log.Printf("mark label=1 if the page lists openings, 0 if it does not")
}

// ── stage 2 ──────────────────────────────────────────────────────────────────

func stage2(storeDir, labelsPath, outPath string, only map[string]bool) {
	f, err := os.Open(labelsPath)
	if err != nil {
		log.Fatalf("open %s: %v (run STAGE=1 first, then label it)", labelsPath, err)
	}
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	hdr, err := r.Read()
	if err != nil {
		log.Fatal(err)
	}
	idx := map[string]int{}
	for i, h := range hdr {
		idx[strings.TrimSpace(strings.ToLower(h))] = i
	}
	want := map[string]bool{}
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		get := func(k string) string {
			if i, ok := idx[k]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		if get("label") == "1" {
			want[get("domain")] = true
		}
	}
	f.Close()
	if len(want) == 0 {
		log.Fatalf("no rows labelled 1 in %s", labelsPath)
	}
	log.Printf("pages labelled as having a job list: %d", len(want))

	out, err := os.Create(outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	w := csv.NewWriter(out)
	defer w.Flush()
	_ = w.Write([]string{"label", "domain", "job_title", "location",
		"department", "posted", "method", "confidence", "job_url"})

	pages, rows := 0, 0
	for _, p := range archive(storeDir, only) {
		html, pageURL, dom, err := readGz(p)
		if err != nil {
			continue
		}
		dom = domOf(p, dom)
		if !want[dom] {
			continue
		}
		jobs := careerparser.Extract(html, pageURL)
		if len(jobs) == 0 {
			continue
		}
		pages++
		for _, j := range jobs {
			_ = w.Write([]string{"", dom, j.Title, j.Location, j.Department,
				j.PostedAt, j.Method, fmt.Sprintf("%.2f", j.Confidence), j.URL})
			rows++
		}
	}
	log.Printf("STAGE 2 | %d pages | %d extracted rows -> %s", pages, rows, outPath)
	log.Printf("mark label=1 if the row is a real job posting, 0 if it is not")
}

func main() {
	storeDir := env("STORE_DIR", "html_store")
	only := readDomainFilter(env("DOMAINS", ""))
	switch env("STAGE", "1") {
	case "1":
		stage1(storeDir, env("OUTPUT", "page_labels.csv"),
			envInt("PAGES", 120), int64(envInt("SEED", 1)),
			env("JOBS", "career_jobs.csv"), only)
	case "2":
		stage2(storeDir, env("LABELS", "page_labels.csv"),
			env("OUTPUT", "job_labels.csv"), only)
	default:
		log.Fatal("STAGE must be 1 or 2")
	}
}
