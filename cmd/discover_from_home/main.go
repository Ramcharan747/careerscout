// Command discover_from_home finds the career pages that URL guessing misses.
//
// cmd/career_finder probes 25 fixed paths — /careers, /jobs, /karriere and so
// on. That works where a firm uses a conventional path and fails completely
// where it does not, and US funds very often do not: /firm/careers,
// /about-us/join-our-team, /culture, /who-we-are/careers. Measured on the fund
// list, path guessing converted 23% of German firms and 13% of US ones, and the
// gap is not that American funds hire less.
//
// So this does what a person would: fetch the homepage, read the navigation,
// and follow the link that says careers. It runs only over the domains that
// produced nothing, so it costs one request per firm plus one per hit.
//
// Input:  a firm list, minus whatever already has a career page
// Output: career_pages_home.csv, same shape as career_pages.csv
//
// Env:
//
//	FIRMS     firm list with a domain column (default funds_crawl_list.csv)
//	FOUND     already-discovered pages to skip (default career_pages.csv)
//	OUTPUT    default career_pages_home.csv
//	WORKERS   concurrent fetchers (default 40)
//	LIMIT     stop after N domains
package main

import (
	"crypto/tls"
	"encoding/csv"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Anchor text that names a careers section, across the languages in the list.
//
// Matched two ways. An anchor whose whole text is "Careers" is a near-certain
// nav or footer link. But requiring the whole string was why the first version
// converted only 4.7%: "Careers »", "Our Careers", "Careers (5)" and "Join Our
// Team →" all failed, and those are how footers are actually written.
var careerTextExactRe = regexp.MustCompile(`(?i)^\s*(careers?|jobs?|join us|join our team|` +
	`work with us|work for us|working here|life at|our people|opportunities|` +
	`open roles|open positions|vacancies|hiring|recruiting|talent|` +
	`karriere|stellen|stellenangebote|jobs & karriere|` +
	`vacatures|werken bij|` +
	`carri[eè]res?|recrutement|nous rejoindre|emploi|` +
	`jobb|lediga jobb|karri[aä]r|stillinger|ledige stillinger|ura|rekrytointi|` +
	`empleo|trabaja con nosotros|carreras|` +
	`lavora con noi|carriere|posizioni aperte|` +
	`kariera|praca|carreiras)\s*$`)

// The same words appearing anywhere in short anchor text. Length-bounded,
// because "careers" inside a paragraph is prose, not a link to a careers page.
var careerTextLooseRe = regexp.MustCompile(`(?i)\b(careers?|jobs?|join (us|our team)|` +
	`work (with|for) us|vacanc|hiring|open (roles|positions)|opportunities|` +
	`karriere|stellen|vacatures|werken bij|carri[eè]re|recrutement|nous rejoindre|` +
	`lediga jobb|jobb|stillinger|rekrytointi|empleo|trabaja|lavora con noi|` +
	`kariera|praca|carreiras)\b`)

var careerHrefRe = regexp.MustCompile(`(?i)/[^/]*(careers?|jobs?|join-?us|join-our|work-with-us|` +
	`work-for-us|vacanc|karriere|stellen|vacatures|werken-bij|carri[eè]re|recrutement|` +
	`nous-rejoindre|emploi|lediga|jobb|stillinger|kariera|praca|empleo|lavora|carreiras|` +
	`opportunities|our-people|life-at|talent)`)

var rejectRe = regexp.MustCompile(`(?i)/(privacy|cookie|terms|legal|imprint|impressum|` +
	`disclaimer|news|blog|press|investor|portfolio|login|signin)`)

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

func readCol(path, col string) map[string][]string {
	out := map[string][]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil || len(recs) < 2 {
		return out
	}
	idx := map[string]int{}
	for i, h := range recs[0] {
		idx[strings.TrimSpace(strings.ToLower(h))] = i
	}
	ci, ok := idx[col]
	if !ok {
		return out
	}
	for _, rec := range recs[1:] {
		if ci < len(rec) {
			if d := strings.ToLower(strings.TrimSpace(rec[ci])); d != "" {
				out[d] = rec
			}
		}
	}
	return out
}

type hit struct{ company, domain, careerURL, country string }

func main() {
	firmsPath := env("FIRMS", "funds_crawl_list.csv")
	found := readCol(env("FOUND", "career_pages.csv"), "domain")
	firms := readCol(firmsPath, "domain")

	f, err := os.Open(firmsPath)
	if err != nil {
		log.Fatal(err)
	}
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	recs, _ := r.ReadAll()
	f.Close()
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

	var todo [][]string
	for _, rec := range recs[1:] {
		d := strings.ToLower(get(rec, "domain"))
		if d == "" {
			continue
		}
		if _, ok := found[d]; ok {
			continue
		}
		todo = append(todo, rec)
	}
	if n := envInt("LIMIT", 0); n > 0 && n < len(todo) {
		todo = todo[:n]
	}
	log.Printf("firms with no career page yet: %d (of %d)", len(todo), len(firms))

	client := &http.Client{
		Timeout: 12 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			MaxIdleConnsPerHost: 4,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 6 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	out, err := os.Create(env("OUTPUT", "career_pages_home.csv"))
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	w := csv.NewWriter(out)
	defer w.Flush()
	_ = w.Write([]string{"company_name", "domain", "career_url", "ats_platform",
		"ats_slug", "employee_count", "country"})

	var mu sync.Mutex
	var done, hits int64
	jobs := make(chan []string)
	var wg sync.WaitGroup

	fetch := func(u string) (*goquery.Document, string, bool) {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, "", false
		}
		req.Header.Set("User-Agent",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
				"(KHTML, like Gecko) Chrome/124.0 Safari/537.36")
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", false
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, "", false
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, "", false
		}
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
		if err != nil {
			return nil, "", false
		}
		return doc, resp.Request.URL.String(), true
	}

	for i := 0; i < envInt("WORKERS", 40); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rec := range jobs {
				dom := strings.ToLower(get(rec, "domain"))
				atomic.AddInt64(&done, 1)

				doc, finalURL, ok := fetch("https://" + dom + "/")
				if !ok {
					continue
				}
				base, err := url.Parse(finalURL)
				if err != nil {
					continue
				}

				// Rank by how the link is worded: an anchor that says exactly
				// "Careers" beats one that merely has "jobs" somewhere in its
				// href, which is often a portfolio-company link.
				best, bestScore := "", 0
				doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
					href, _ := s.Attr("href")
					href = strings.TrimSpace(href)
					if href == "" || strings.HasPrefix(href, "#") ||
						strings.HasPrefix(href, "mailto:") {
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
					abs.Fragment = ""
					// Same site, or a careers subdomain of it.
					h := strings.ToLower(abs.Host)
					if !strings.EqualFold(h, base.Host) &&
						!strings.HasSuffix(h, "."+strings.TrimPrefix(base.Host, "www.")) {
						return
					}
					if rejectRe.MatchString(abs.Path) {
						return
					}
					txt := strings.Join(strings.Fields(s.Text()), " ")
					score := 0
					switch {
					case careerTextExactRe.MatchString(txt):
						score += 3
					case len(txt) <= 40 && careerTextLooseRe.MatchString(txt):
						score += 2
					}
					if careerHrefRe.MatchString(abs.Path) ||
						strings.HasPrefix(h, "careers.") || strings.HasPrefix(h, "jobs.") {
						score += 2
					}
					if score > bestScore {
						bestScore, best = score, abs.String()
					}
				})
				// A career-shaped href alone is enough. Requiring wording too
				// discarded every site whose footer link is an icon, an image,
				// or text the pattern did not anticipate.
				// Nothing in the navigation or footer. The sitemap lists every
				// URL a site is willing to declare, so it finds career pages
				// that are simply not linked from the front page.
				if best == "" || bestScore < 2 {
					if u := fromSitemap(client, base); u != "" {
						best = u
					} else {
						continue
					}
				}
				// Confirm it resolves before recording it as a career page.
				if _, _, ok := fetch(best); !ok {
					continue
				}
				mu.Lock()
				_ = w.Write([]string{get(rec, "name"), dom, best, "", "",
					"", get(rec, "hq_country")})
				w.Flush()
				mu.Unlock()
				atomic.AddInt64(&hits, 1)
			}
		}()
	}

	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for range t.C {
			d, h := atomic.LoadInt64(&done), atomic.LoadInt64(&hits)
			if d >= int64(len(todo)) {
				return
			}
			log.Printf("%d/%d | career pages found: %d (%.1f%%)", d, len(todo), h,
				100*float64(h)/float64(max64(d, 1)))
		}
	}()

	for _, rec := range todo {
		jobs <- rec
	}
	close(jobs)
	wg.Wait()
	log.Printf("DONE | probed %d homepages | %d career pages found (%.1f%%)",
		done, hits, 100*float64(hits)/float64(max64(done, 1)))
}

var locRe = regexp.MustCompile(`(?is)<loc>\s*([^<\s]+)\s*</loc>`)

// fromSitemap reads the site's declared URL list and returns the best
// career-shaped entry. Sitemaps are cheap, complete by design, and unaffected
// by whatever the navigation happens to expose. One level of sitemap index is
// followed; deeper is a crawl, which this is not.
//
// Sitemaps are written to sitemaps/<host>.xml so a later pass can mine them for
// anything else without re-fetching.
func fromSitemap(client *http.Client, base *url.URL) string {
	_ = os.MkdirAll("sitemaps", 0o755)
	roots := []string{
		base.Scheme + "://" + base.Host + "/sitemap.xml",
		base.Scheme + "://" + base.Host + "/sitemap_index.xml",
		base.Scheme + "://" + base.Host + "/wp-sitemap.xml",
	}
	get := func(u string) string {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return ""
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; careerscout/1.0)")
		resp, err := client.Do(req)
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return ""
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return ""
		}
		return string(b)
	}

	seen := map[string]bool{}
	var best string
	var scan func(body string, depth int)
	scan = func(body string, depth int) {
		for _, m := range locRe.FindAllStringSubmatch(body, -1) {
			loc := strings.TrimSpace(m[1])
			if loc == "" || seen[loc] {
				continue
			}
			seen[loc] = true
			lu, err := url.Parse(loc)
			if err != nil {
				continue
			}
			if strings.HasSuffix(lu.Path, ".xml") {
				// A sitemap index. Only follow ones that might hold pages.
				if depth == 0 && len(seen) < 60 {
					if sub := get(loc); sub != "" {
						scan(sub, depth+1)
					}
				}
				continue
			}
			if rejectRe.MatchString(lu.Path) {
				continue
			}
			if careerHrefRe.MatchString(lu.Path) && best == "" {
				best = loc
			}
		}
	}

	for _, root := range roots {
		body := get(root)
		if body == "" {
			continue
		}
		name := strings.ReplaceAll(base.Host, "/", "_")
		_ = os.WriteFile("sitemaps/"+name+".xml", []byte(body), 0o644)
		scan(body, 0)
		if best != "" {
			return best
		}
	}
	return ""
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
