// Command expand_career_links finds the second-level job pages that a career
// landing page links to.
//
// Many career pages advertise nothing themselves: they carry a "View open
// roles" or "Current vacancies" button through to the real listing. A one-page
// crawl sees only the landing page and concludes the firm is not hiring.
//
// This runs over the existing HTML archive rather than re-crawling, and emits a
// second-level target list in the same shape as career_pages.csv, so
// cmd/fetch_career_html can archive it with no changes. Discovery stays
// untouched: the expansion is a separate, re-runnable stage.
//
// Only same-host links are followed, deeper than the page they came from, at
// most a few per domain. That bound matters — career sites link to hundreds of
// pages, and an unbounded follow turns a targeted pass into a site crawl.
//
// Input:  html_store/*.html.gz  +  career_pages.csv
// Output: career_pages_l2.csv
//
// Env:
//
//	STORE_DIR   archive directory (default html_store)
//	INPUT       level-1 discovery output (default career_pages.csv)
//	OUTPUT      level-2 targets (default career_pages_l2.csv)
//	EXCLUDE     an earlier OUTPUT; its URLs and domain keys are not reused
//	PER_DOMAIN  max links to follow per firm (default 3)
//
// Re-running after a later crawl is safe: pass the previous output as EXCLUDE
// and only genuinely new links are emitted, under key indices that do not
// collide with the archives the first run already produced.
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

	"github.com/PuerkitoBio/goquery"
)

// Anchor text that promises a list of openings rather than a policy page.
var linkTextRe = regexp.MustCompile(`(?i)\b(open (roles|positions|jobs|vacanc)|` +
	`current (openings|vacanc|opportunit)|view (all )?(jobs|roles|vacanc|openings)|` +
	`see (all )?(jobs|roles|openings)|job (search|board|listings|openings)|` +
	`browse (jobs|roles|opportunit)|all (jobs|vacancies|openings)|search jobs|` +
	`join (our )?team|work (with|for) us|we'?re hiring|apply|` +
	`offene stellen|aktuelle stellen|stellenangebote|zu den stellen|` +
	`alle vacatures|bekijk vacatures|onze vacatures|` +
	`nos offres|voir les offres|toutes les offres|postes ouverts|` +
	`lediga jobb|se alla jobb|ledige stillinger|` +
	`ofertas de empleo|ver ofertas|posizioni aperte)\b`)

// URL shapes that are listing pages in their own right.
var linkHrefRe = regexp.MustCompile(`(?i)/(jobs?|careers?|vacanc(y|ies)|positions?|openings?|` +
	`opportunities|stellen|stellenangebote|vacatures|offres|emploi|lediga-?jobb|` +
	`stillinger|empleo|ofertas|posizioni|join-us|work-with-us|current-openings)(/|$|\?)`)

// Pages that look like listings but never are.
var rejectRe = regexp.MustCompile(`(?i)/(privacy|cookie|terms|legal|imprint|impressum|` +
	`contact|about|news|blog|press|login|signin|apply-now/thank|equal-opportunit)`)

// Share and tracking parameters. Without this, "?share=twitter" on a careers
// page counts as a distinct deeper URL and burns a slot per social network.
var junkQueryRe = regexp.MustCompile(`(?i)(^|&)(share|utm_[a-z]+|fbclid|gclid|mc_cid|` +
	`mc_eid|ref|source|s)=`)

// Language switchers point at the same career page in another locale. The href
// matches every listing pattern, so without this a trilingual site spends its
// whole per-domain budget re-fetching one page.
var langSwitchRe = regexp.MustCompile(`(?i)^(\p{L}{2,3}|english|englisch|deutsch|german|` +
	`français|francais|french|español|espanol|spanish|italiano|italian|nederlands|dutch|` +
	`svenska|swedish|norsk|dansk|suomi|polski|português|portugues|česky|cesky)$`)

// A trailing numeric or hash-like segment marks an individual posting rather
// than a listing. Those are leaves: level-1 extraction already has them, and
// fetching each one costs a request for a single job we can already see.
var leafPostingRe = regexp.MustCompile(`/(\d{4,}|[0-9a-f]{16,})/?$`)

type row struct{ company, domain, careerURL, ats string }

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func readL1(path string) map[string]row {
	out := map[string]row{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	hdr, err := r.Read()
	if err != nil {
		return out
	}
	idx := map[string]int{}
	for i, h := range hdr {
		idx[strings.TrimSpace(strings.ToLower(h))] = i
	}
	get := func(rec []string, k string) string {
		if i, ok := idx[k]; ok && i < len(rec) {
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
		d := get(rec, "domain")
		if d == "" {
			continue
		}
		out[d] = row{get(rec, "company_name"), d, get(rec, "career_url"), get(rec, "ats_platform")}
	}
	return out
}

type cand struct {
	url   string
	text  string
	score int
}

var l2KeyRe = regexp.MustCompile(`^(.*)__l2_(\d+)$`)

// readExclude loads a previous run's output so a re-run neither re-emits URLs
// that are already archived nor reuses a domain key, which would overwrite the
// stored HTML of a different page.
func readExclude(path string) (urls map[string]bool, taken map[string]map[int]bool) {
	urls, taken = map[string]bool{}, map[string]map[int]bool{}
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil || len(recs) < 2 {
		return
	}
	di, ui := 1, 2
	for i, h := range recs[0] {
		switch strings.TrimSpace(strings.ToLower(h)) {
		case "domain":
			di = i
		case "career_url":
			ui = i
		}
	}
	for _, rec := range recs[1:] {
		if ui < len(rec) {
			urls[strings.TrimSpace(rec[ui])] = true
		}
		if di < len(rec) {
			if m := l2KeyRe.FindStringSubmatch(strings.TrimSpace(rec[di])); m != nil {
				n, _ := strconv.Atoi(m[2])
				if taken[m[1]] == nil {
					taken[m[1]] = map[int]bool{}
				}
				taken[m[1]][n] = true
			}
		}
	}
	log.Printf("exclude: %d urls, %d domains with existing keys", len(urls), len(taken))
	return
}

func main() {
	storeDir := env("STORE_DIR", "html_store")
	outPath := env("OUTPUT", "career_pages_l2.csv")
	perDomain, _ := strconv.Atoi(env("PER_DOMAIN", "3"))
	if perDomain <= 0 {
		perDomain = 3
	}
	l1 := readL1(env("INPUT", "career_pages.csv"))
	log.Printf("level-1 pages: %d", len(l1))

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		log.Fatalf("read %s: %v", storeDir, err)
	}

	f, err := os.Create(outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	// Same header as career_pages.csv so fetch_career_html consumes it directly.
	_ = w.Write([]string{"company_name", "domain", "career_url", "ats_platform",
		"employee_count", "country", "link_text", "parent_url"})

	var pages, emitted int
	seenURL, takenKey := readExclude(env("EXCLUDE", ""))

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html.gz") {
			continue
		}
		// Never expand an expansion. Depth 3 is a site crawl, and the level-2
		// archives only exist here because a previous run put them there.
		if l2KeyRe.MatchString(strings.TrimSuffix(e.Name(), ".html.gz")) {
			continue
		}
		p := filepath.Join(storeDir, e.Name())
		fh, err := os.Open(p)
		if err != nil {
			continue
		}
		zr, err := gzip.NewReader(fh)
		if err != nil {
			fh.Close()
			continue
		}
		body, _ := io.ReadAll(zr)
		pageURL, domain := zr.Comment, zr.Name
		zr.Close()
		fh.Close()

		if domain == "" {
			domain = strings.TrimSuffix(e.Name(), ".html.gz")
		}
		meta := l1[domain]
		if pageURL == "" {
			pageURL = meta.careerURL
		}
		base, err := url.Parse(pageURL)
		if err != nil || base.Host == "" {
			continue
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
		if err != nil {
			continue
		}
		pages++

		var cands []cand
		doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
			href, _ := s.Attr("href")
			href = strings.TrimSpace(href)
			if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") {
				return
			}
			ref, err := url.Parse(href)
			if err != nil {
				return
			}
			abs := base.ResolveReference(ref)
			if abs.Scheme != "http" && abs.Scheme != "https" {
				return
			}
			// Same registrable host only; off-site links are ATS boards, which
			// the ATS path already handles.
			if !strings.EqualFold(abs.Host, base.Host) {
				return
			}
			abs.Fragment = ""
			if junkQueryRe.MatchString(abs.RawQuery) {
				return
			}
			if leafPostingRe.MatchString(abs.Path) {
				return
			}
			full := abs.String()
			if full == pageURL || seenURL[full] || rejectRe.MatchString(abs.Path) {
				return
			}
			// Must be deeper than the page we came from, or carry a query that
			// selects a listing.
			if len(strings.Split(strings.Trim(abs.Path, "/"), "/")) <=
				len(strings.Split(strings.Trim(base.Path, "/"), "/")) && abs.RawQuery == "" {
				return
			}

			text := strings.Join(strings.Fields(s.Text()), " ")
			if langSwitchRe.MatchString(text) {
				return
			}
			score := 0
			if linkTextRe.MatchString(text) {
				score += 3
			}
			if linkHrefRe.MatchString(abs.Path) {
				score += 2
			}
			if score == 0 {
				return
			}
			cands = append(cands, cand{full, text, score})
		})

		sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
		if takenKey[domain] == nil {
			takenKey[domain] = map[int]bool{}
		}
		// Slots already spent by an earlier run count against the per-domain
		// budget, so a re-run tops a firm up rather than doubling it.
		existing := len(takenKey[domain])
		n, slot := 0, 0
		for _, c := range cands {
			if n+existing >= perDomain {
				break
			}
			if seenURL[c.url] {
				continue
			}
			seenURL[c.url] = true
			for slot++; takenKey[domain][slot]; slot++ {
			}
			takenKey[domain][slot] = true
			// Suffix the domain key so level-2 archives never overwrite level-1.
			key := domain + "__l2_" + strconv.Itoa(slot)
			_ = w.Write([]string{meta.company, key, c.url, meta.ats, "", "", c.text, pageURL})
			emitted++
			n++
		}
	}

	log.Printf("DONE | scanned %d archived pages | %d second-level targets -> %s",
		pages, emitted, outPath)
	log.Printf("next: INPUT=%s fetch_career_html   (archives them alongside level 1)", outPath)
}
