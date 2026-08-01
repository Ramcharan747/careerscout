// Command build_link_dataset turns a completed deep crawl into training data
// for deciding which links are worth following.
//
// The point is that this dataset labels itself. Every target the expander
// emitted was a guess — "this anchor looks like it leads to the openings" — and
// the crawl then went and found out. Whether the destination actually held a
// job list is knowable after the fact, for every link, at no labelling cost.
// Hand labels are needed only to check that "held a job list" is being measured
// correctly; the ranking itself can be fitted from the crawl.
//
// One row per followed link:
//
//	features   what was visible at decision time — anchor wording, href shape,
//	           depth, and whether the page it came from had jobs of its own
//	outcome    what the fetch found — jobs extracted, and job wording present
//
// Fitting on this ranks candidate links, so a later crawl spends its per-domain
// budget on the two links that pay rather than the first four it happens to see.
//
// Input:  career_pages_d*.csv (and any legacy level files) + html_store/
// Output: link_outcomes.csv
//
// Env:
//
//	STORE_DIR   archive directory (default html_store)
//	TARGETS     comma-separated target CSVs; default is every career_pages_d*.csv
//	OUTPUT      dataset path (default link_outcomes.csv)
package main

import (
	"compress/gzip"
	"encoding/csv"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/careerscout/careerscout/internal/careerparser"
)

var (
	depthKeyRe = regexp.MustCompile(`^(.*)__l(\d+)_(\d+)$`)
	tagRe      = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)
	wsRe       = regexp.MustCompile(`\s+`)

	// Wording groups, kept separate so fitting can tell which promise is
	// actually kept. "View all jobs" and "Join our team" are not equally
	// informative, and lumping them into one keyword flag hides that.
	listWordRe = regexp.MustCompile(`(?i)\b(view|see|browse|search|explore|all|current|open)\b`)
	jobWordRe  = regexp.MustCompile(`(?i)(job|vacan|opening|position|role|opportunit|` +
		`stelle|vacature|emploi|offre|poste|jobb|lediga|stilling|empleo|oferta|` +
		`posizion|lavoro|praca|vaga|ty[oö]paik)`)
	teamWordRe  = regexp.MustCompile(`(?i)(join|team|work (with|for) us|culture|life at|people)`)
	applyWordRe = regexp.MustCompile(`(?i)(apply|application|bewerb|candidature|postul)`)
	pageWordRe  = regexp.MustCompile(`(?i)(load more|show more|next|page \d|weiter|mehr)`)
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func splitKey(key string) (string, int) {
	if m := depthKeyRe.FindStringSubmatch(key); m != nil {
		d, _ := strconv.Atoi(m[2])
		return m[1], d
	}
	return key, 1
}

func b2i(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

type target struct {
	domainKey, url, linkText, parentURL string
}

func readTargets(paths []string) []target {
	var out []target
	seen := map[string]bool{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		r := csv.NewReader(f)
		r.LazyQuotes = true
		r.FieldsPerRecord = -1
		recs, err := r.ReadAll()
		f.Close()
		if err != nil || len(recs) < 2 {
			continue
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
			t := target{get(rec, "domain"), get(rec, "career_url"),
				get(rec, "link_text"), get(rec, "parent_url")}
			if t.domainKey == "" || seen[t.domainKey] {
				continue
			}
			seen[t.domainKey] = true
			out = append(out, t)
		}
		log.Printf("targets from %s: %d cumulative", p, len(out))
	}
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

// outcome describes what a fetched page turned out to contain.
type outcome struct {
	fetched  bool
	nJobs    int
	textLen  int
	jobWords bool
}

func inspect(storeDir, key string) outcome {
	html, pageURL, ok := readGz(filepath.Join(storeDir, key+".html.gz"))
	if !ok {
		return outcome{}
	}
	text := strings.TrimSpace(wsRe.ReplaceAllString(tagRe.ReplaceAllString(html, " "), " "))
	return outcome{
		fetched:  true,
		nJobs:    len(careerparser.Extract(html, pageURL)),
		textLen:  len(text),
		jobWords: jobWordRe.MatchString(text),
	}
}

func main() {
	storeDir := env("STORE_DIR", "html_store")
	outPath := env("OUTPUT", "link_outcomes.csv")

	var paths []string
	if t := env("TARGETS", ""); t != "" {
		for _, p := range strings.Split(t, ",") {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
		}
	} else {
		g, _ := filepath.Glob("career_pages_d*.csv")
		l, _ := filepath.Glob("career_pages_l2*.csv")
		paths = append(g, l...)
		sort.Strings(paths)
	}
	if len(paths) == 0 {
		log.Fatal("no target CSVs found; run expand_career_links first")
	}

	targets := readTargets(paths)
	log.Printf("followed links to score: %d", len(targets))

	// A link's own page matters: a button on a page that already lists jobs is
	// usually pagination, while one on a page with none is the real redirect.
	parentJobs := map[string]bool{}
	for _, t := range targets {
		if t.parentURL == "" {
			continue
		}
		if _, seen := parentJobs[t.parentURL]; !seen {
			parentJobs[t.parentURL] = false
		}
	}

	f, err := os.Create(outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{
		"domain", "depth", "url", "link_text", "parent_url",
		"txt_len", "txt_words", "txt_list", "txt_job", "txt_team", "txt_apply", "txt_page",
		"href_job", "href_segs", "href_query", "href_len",
		"fetched", "text_len", "job_words", "n_jobs", "label",
	})

	var rows, hits, dead int
	for _, t := range targets {
		domain, depth := splitKey(t.domainKey)
		u, err := url.Parse(t.url)
		if err != nil {
			continue
		}
		o := inspect(storeDir, t.domainKey)
		if !o.fetched {
			dead++
		}
		// A link "worked" when the page it reached actually carries a list.
		// Job wording alone is not enough — every careers page says "jobs" —
		// and extraction alone is not enough either, since the parser is what
		// this is meant to improve. Requiring both is the conservative choice
		// and it is the one that keeps the label honest.
		label := o.fetched && o.nJobs >= 3 && o.jobWords
		if label {
			hits++
		}

		segs := 0
		for _, s := range strings.Split(strings.Trim(u.Path, "/"), "/") {
			if s != "" {
				segs++
			}
		}
		txt := t.linkText

		_ = w.Write([]string{
			domain, strconv.Itoa(depth), t.url, txt, t.parentURL,
			strconv.Itoa(len(txt)), strconv.Itoa(len(strings.Fields(txt))),
			b2i(listWordRe.MatchString(txt)), b2i(jobWordRe.MatchString(txt)),
			b2i(teamWordRe.MatchString(txt)), b2i(applyWordRe.MatchString(txt)),
			b2i(pageWordRe.MatchString(txt)),
			b2i(jobWordRe.MatchString(u.Path)), strconv.Itoa(segs),
			b2i(u.RawQuery != ""), strconv.Itoa(len(u.Path)),
			b2i(o.fetched), strconv.Itoa(o.textLen), b2i(o.jobWords),
			strconv.Itoa(o.nJobs), b2i(label),
		})
		rows++
	}

	log.Printf("DONE | %d links | %d led to a job list (%.1f%%) | %d never fetched -> %s",
		rows, hits, 100*float64(hits)/float64(max(rows, 1)), dead, outPath)
	log.Printf("fit on `label`; the features are everything known before the fetch")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
