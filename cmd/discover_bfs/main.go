// Command discover_bfs walks a firm's own site until it finds the careers page.
//
// The earlier passes guessed URLs, then read the homepage, then read the
// sitemap. Each caught what the one before missed, and each still assumed the
// careers link is at most one hop from the front page. Often it is not: it sits
// under About, or Our Firm, or Contact, or on a subdomain the homepage links to
// only from a footer three levels in.
//
// So this does what a person does. Start at the homepage, follow the most
// career-looking link, look again, and keep going until a page actually carries
// openings or an application route. Stop the moment one is found. Give up after
// 20 pages so a site with no careers page cannot absorb the whole run.
//
// Never leaves the firm: same registrable domain only, though subdomains count
// because careers.<firm> and jobs.<firm> are exactly where these pages live.
//
// Input:  a firm list, minus every domain already covered
// Output: career_pages_bfs.csv, same shape as career_pages.csv
//
// Env:
//
//	FIRMS      firm list with a domain column (default funds_crawl_list.csv)
//	FOUND      comma-separated CSVs of domains already covered
//	OUTPUT     default career_pages_bfs.csv
//	WORKERS    concurrent firms (default 40)
//	BUDGET     pages per firm before giving up (default 20)
//	LIMIT      stop after N firms
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/careerscout/careerscout/internal/careerparser"
)

var (
	// Wording that names a careers section outright.
	strongTextRe = regexp.MustCompile(`(?i)\b(careers?|jobs?|vacanc|vacature|stellenangebote|` +
		`karriere|stellen|join (us|our team)|work (with|for) us|we'?re hiring|open (roles|positions)|` +
		`current openings|opportunities|carri[eè]res?|recrutement|nous rejoindre|emploi|` +
		`lediga jobb|jobb|stillinger|rekrytointi|empleo|trabaja con nosotros|lavora con noi|` +
		`kariera|praca|carreiras|werken bij|internship|praktikum)\b`)

	strongHrefRe = regexp.MustCompile(`(?i)/[^/]*(careers?|jobs?|vacanc|vacature|karriere|stellen|` +
		`carri[eè]re|recrutement|emploi|lediga|jobb|stillinger|kariera|praca|empleo|lavora|` +
		`join-?us|join-our|work-with-us|werken-bij|opportunities|hiring|talent|internship|praktik)`)

	// Pages that commonly HOLD the careers link without being it. Worth walking
	// through at lower priority — on many fund sites the only careers link in
	// the whole document sits on the About or Contact page.
	viaTextRe = regexp.MustCompile(`(?i)\b(about|our firm|who we are|the firm|company|team|` +
		`people|culture|contact|über uns|unternehmen|à propos|chi siamo|over ons|om oss)\b`)

	skipHrefRe = regexp.MustCompile(`(?i)/(privacy|cookie|terms|legal|imprint|impressum|` +
		`disclaimer|news|blog|press|media|portfolio|investor|login|signin|search|feed|` +
		`wp-content|wp-json|\.pdf|\.jpg|\.png|\.zip)`)

	// A page that is plainly about the people, not about hiring them. Worth
	// walking through, never worth stopping on without an explicit invitation.
	bioPathRe = regexp.MustCompile(`(?i)/(team|people|our-people|about|about-us|who-we-are|` +
		`our-firm|the-firm|leadership|partners|management|contact|profile)`)

	// The firm saying, in its own words, that it wants applications. Structure
	// alone cannot establish this; a sentence has to. "Join our team" is
	// deliberately absent — it is a heading on almost every team page and it was
	// the single biggest source of false stops.
	hiringPhraseRe = regexp.MustCompile(`(?i)(open position|current opening|current vacanc|` +
		`open role|job opening|we are hiring|we're hiring|now hiring|` +
		`apply now|apply here|apply for|view (all )?(jobs|openings|positions)|` +
		`send (us )?your (cv|r[ée]sum)|submit your (cv|r[ée]sum|application)|` +
		`spontaneous application|unsolicited application|open application|` +
		`no (current|suitable|open) (opening|vacanc|position)|` +
		`offene stellen|aktuelle stellen|stellenangebot|initiativbewerbung|` +
		`wir suchen|bewerben sie sich|jetzt bewerben|` +
		`vacature|solliciteer|spontaan sollicit|werken bij ons|` +
		`offres d'emploi|poste[s]? [àa] pourvoir|candidature spontan|nous recrutons|` +
		`envoyez votre candidature|rejoignez-nous|` +
		`lediga jobb|lediga tj[aä]nster|s[oö]k jobb|ledige stillinger|` +
		`avoimet ty[oö]paikat|ofertas de empleo|[uú]nete a|posizioni aperte|` +
		`oferty pracy|do[lł][aą]cz do)`)
)

var tagStripRe = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)

func stripTags(html string) string { return tagStripRe.ReplaceAllString(html, " ") }

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

// registrable reduces a host to the part that must match, so www., careers. and
// jobs. all count as the same firm while a link to an ATS vendor does not.
func registrable(host string) string {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	// Keep three labels for co.uk-style suffixes, two otherwise.
	last := parts[len(parts)-1]
	second := parts[len(parts)-2]
	if len(second) <= 3 && len(last) == 2 {
		if len(parts) >= 3 {
			return strings.Join(parts[len(parts)-3:], ".")
		}
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

type cand struct {
	url   string
	score int
	depth int
}

func main() {
	firmsPath := env("FIRMS", "funds_crawl_list.csv")
	budget := envInt("BUDGET", 20)

	covered := map[string]bool{}
	for _, p := range strings.Split(env("FOUND", "career_pages.csv"), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		r := csv.NewReader(f)
		r.LazyQuotes = true
		r.FieldsPerRecord = -1
		recs, _ := r.ReadAll()
		f.Close()
		if len(recs) < 2 {
			continue
		}
		di := 1
		for i, h := range recs[0] {
			if strings.EqualFold(strings.TrimSpace(h), "domain") {
				di = i
			}
		}
		for _, rec := range recs[1:] {
			if di < len(rec) {
				// Deep pages carry a __l<n>_<n> suffix; the firm is the stem.
				d := strings.ToLower(strings.TrimSpace(rec[di]))
				if i := strings.Index(d, "__l"); i > 0 {
					d = d[:i]
				}
				covered[d] = true
			}
		}
	}

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
		if d != "" && !covered[d] {
			todo = append(todo, rec)
		}
	}
	if n := envInt("LIMIT", 0); n > 0 && n < len(todo) {
		todo = todo[:n]
	}
	log.Printf("firms still without a career page: %d | budget %d pages each", len(todo), budget)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			MaxIdleConnsPerHost: 2,
			DisableKeepAlives:   false,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 6 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	out, err := os.Create(env("OUTPUT", "career_pages_bfs.csv"))
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	w := csv.NewWriter(out)
	defer w.Flush()
	_ = w.Write([]string{"company_name", "domain", "career_url", "ats_platform",
		"ats_slug", "employee_count", "country"})

	var mu sync.Mutex
	var done, hits, fetched int64
	jobs := make(chan []string)
	var wg sync.WaitGroup

	fetchDoc := func(u string) (*goquery.Document, string, string, bool) {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, "", "", false
		}
		req.Header.Set("User-Agent",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
				"(KHTML, like Gecko) Chrome/124.0 Safari/537.36")
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", "", false
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, "", "", false
		}
		if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "html") {
			return nil, "", "", false
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, "", "", false
		}
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
		if err != nil {
			return nil, "", "", false
		}
		atomic.AddInt64(&fetched, 1)
		return doc, resp.Request.URL.String(), string(body), true
	}

	// isCareerPage is the stopping condition, and it has to be strict.
	//
	// The first version accepted any page the extractor found two rows on, plus
	// any door signal. Tested on 200 firms it stopped on /team/, /people/ and
	// /about/ over and over, and only 18 of 57 results held up. Two reasons: a
	// grid of partner bios is structurally identical to a grid of job cards, so
	// the structural parser reads it as a listing; and partner bios contain the
	// word "internship" often enough to trip the door classifier on its own.
	//
	// So a page now has to say it is hiring, in a heading or a link, in its own
	// words — and pages whose URL is plainly a team or about page must clear a
	// higher bar than the extractor's opinion.
	isCareerPage := func(html, pageURL string) bool {
		u, err := url.Parse(pageURL)
		if err != nil {
			return false
		}
		d := careerparser.Doors(html, pageURL)
		if d.Dead {
			return false
		}
		// Every false positive in the first test was a team, about or contact
		// page — a grid of partner bios reads as a listing, and bios mention
		// internships. Those pages are worth walking through and never worth
		// stopping on, whatever the extractor thinks, unless the page itself
		// says it is hiring.
		if bioPathRe.MatchString(u.Path) {
			return hiringPhraseRe.MatchString(strings.ToLower(stripTags(html)))
		}
		// Anywhere else, the ordinary signals are enough. Requiring a hiring
		// phrase here as well took 57 results down to 3: plenty of genuine
		// listing pages just print the job titles and nothing else.
		if d.OpenRoles > 0 || d.Speculative || d.CVUpload {
			return true
		}
		return len(careerparser.Extract(html, pageURL)) >= 2
	}

	for i := 0; i < envInt("WORKERS", 40); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rec := range jobs {
				dom := strings.ToLower(get(rec, "domain"))
				atomic.AddInt64(&done, 1)
				root := registrable(dom)

				seen := map[string]bool{}
				queue := []cand{{"https://" + dom + "/", 0, 0}}
				spent := 0
				var found string

				for len(queue) > 0 && spent < budget && found == "" {
					// Best-first: always take the most career-looking link left.
					sort.SliceStable(queue, func(i, j int) bool {
						if queue[i].score != queue[j].score {
							return queue[i].score > queue[j].score
						}
						return queue[i].depth < queue[j].depth
					})
					cur := queue[0]
					queue = queue[1:]
					if seen[cur.url] {
						continue
					}
					seen[cur.url] = true
					spent++

					doc, finalURL, html, ok := fetchDoc(cur.url)
					if !ok {
						continue
					}
					if cur.depth > 0 && isCareerPage(html, finalURL) {
						found = finalURL
						break
					}
					base, err := url.Parse(finalURL)
					if err != nil {
						continue
					}
					doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
						href, _ := s.Attr("href")
						href = strings.TrimSpace(href)
						if href == "" || strings.HasPrefix(href, "#") ||
							strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "tel:") {
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
						// Stay on the firm, subdomains included.
						if registrable(abs.Host) != root {
							return
						}
						if skipHrefRe.MatchString(abs.Path) || seen[abs.String()] {
							return
						}
						txt := strings.Join(strings.Fields(s.Text()), " ")
						score := 0
						if len(txt) <= 60 && strongTextRe.MatchString(txt) {
							score += 4
						}
						if strongHrefRe.MatchString(abs.Path) {
							score += 3
						}
						h := strings.ToLower(abs.Host)
						if strings.HasPrefix(h, "careers.") || strings.HasPrefix(h, "jobs.") {
							score += 3
						}
						// Pages that tend to hold the careers link, walked only
						// once the strong candidates are exhausted.
						if score == 0 && cur.depth == 0 &&
							len(txt) <= 40 && viaTextRe.MatchString(txt) {
							score = 1
						}
						if score == 0 {
							return
						}
						queue = append(queue, cand{abs.String(), score, cur.depth + 1})
					})
				}

				if found == "" {
					continue
				}
				mu.Lock()
				_ = w.Write([]string{get(rec, "name"), dom, found, "", "", "", get(rec, "hq_country")})
				w.Flush()
				mu.Unlock()
				atomic.AddInt64(&hits, 1)
			}
		}()
	}

	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			d, h, fz := atomic.LoadInt64(&done), atomic.LoadInt64(&hits), atomic.LoadInt64(&fetched)
			if d >= int64(len(todo)) {
				return
			}
			log.Printf("%d/%d firms | %d career pages (%.1f%%) | %d pages fetched (%.1f per firm)",
				d, len(todo), h, 100*float64(h)/float64(max64(d, 1)), fz,
				float64(fz)/float64(max64(d, 1)))
		}
	}()

	for _, rec := range todo {
		jobs <- rec
	}
	close(jobs)
	wg.Wait()
	log.Printf("DONE | %d firms walked | %d pages fetched | %d career pages found (%.1f%%)",
		done, fetched, hits, 100*float64(hits)/float64(max64(done, 1)))
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
