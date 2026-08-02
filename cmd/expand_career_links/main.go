// Command expand_career_links walks a career site inwards, one level at a time,
// until it reaches the page that actually holds the openings.
//
// A career landing page frequently advertises nothing itself. It carries a
// "Search careers", "Job openings" or "View open roles" button, that page
// carries another, and the list is two or three hops in. Stopping at the first
// hop concludes the firm is not hiring when it plainly is.
//
// This runs over the existing HTML archive rather than re-crawling, and emits
// the next level's targets in the same shape as career_pages.csv, so
// cmd/fetch_career_html archives them with no changes. Discovery stays
// untouched: expansion is a separate, re-runnable stage.
//
// Run it in a loop, alternating with the fetcher, until a level comes back
// empty:
//
//	./expand_career_links                                   # depth 1 -> 2
//	INPUT=career_pages_d2.csv ./fetch_career_html
//	FROM_DEPTH=2 EXCLUDE=career_pages_d2.csv ./expand_career_links
//	INPUT=career_pages_d3.csv ./fetch_career_html
//	...
//
// scripts/crawl_deep.sh does exactly that.
//
// Archive keys carry their depth: a depth-1 page is stored under its bare
// domain, deeper pages under <domain>__l<depth>_<n>. Only same-host links are
// followed, and a firm has a total page budget across every level, so a site
// with a thousand job pages cannot turn a targeted walk into a site crawl.
//
// Input:  html_store/*.html.gz  +  career_pages.csv
// Output: career_pages_d<depth+1>.csv
//
// Env:
//
//	STORE_DIR    archive directory (default html_store)
//	INPUT        level-1 discovery output (default career_pages.csv)
//	OUTPUT       next-level targets (default career_pages_d<depth+1>.csv)
//	EXCLUDE      comma-separated earlier outputs; their URLs and keys are not reused
//	FROM_DEPTH   depth of the archived pages to expand (default 1)
//	PER_DOMAIN   new links per firm at this level (default 4)
//	MAX_DOMAIN   total pages per firm across all levels (default 14)
package main

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/careerscout/careerscout/internal/careerparser"
)

// Anchor text that promises a list of openings rather than a policy page.
var linkTextRe = regexp.MustCompile(`(?i)\b(open (roles|positions|jobs|vacanc)|` +
	`current (openings|vacanc|opportunit|job)|view (all )?(jobs|roles|vacanc|openings|opportunit)|` +
	`see (all )?(jobs|roles|openings|opportunit)|job (search|board|listings?|openings?|opportunit)|` +
	`browse (jobs|roles|opportunit|openings)|all (jobs|vacancies|openings|opportunities|roles)|` +
	`search (jobs|careers|openings|opportunit)|career (search|opportunit)|` +
	`explore (careers|jobs|opportunit|roles)|find (a |your )?(job|role)|` +
	`our (jobs|openings|vacancies|roles)|open (opportunit|application)|` +
	`join (our )?team|work (with|for) us|we'?re hiring|` +
	`(load|show) more|next page|` +
	`offene stellen|aktuelle stellen|stellenangebote|zu den stellen|stellensuche|` +
	`alle vacatures|bekijk vacatures|onze vacatures|` +
	`nos offres|voir les offres|toutes les offres|postes ouverts|` +
	`lediga jobb|se alla jobb|ledige stillinger|` +
	`ofertas de empleo|ver ofertas|posizioni aperte)\b`)

// URL shapes that are listing pages in their own right.
//
// The token is matched anywhere inside a path segment, not only as a whole
// segment. Anchoring it to segment boundaries missed /formation-emploi/,
// /vacature-financial-controller and /en/carrieres/ — real listing pages that
// simply do not put the keyword first.
var linkHrefRe = regexp.MustCompile(`(?i)/[^/]*(jobs?|careers?|carri[eè]res?|carriere|` +
	`vacanc(y|ies)|vacature|vacatures|positions?|openings?|opportunit|` +
	`stellen|stellenangebot|offres?|emploi|lediga-?jobb|jobb|` +
	`stillinger|empleo|oferta|posizion|lavora|praca|kariera|karriere|` +
	`join-us|work-with-us|werken-bij|rejoignez|recrutement|reclutamiento)`)

// Pages that look like listings but never are.
var rejectRe = regexp.MustCompile(`(?i)/(privacy|cookie|terms|legal|imprint|impressum|` +
	`contact|about|news|blog|press|login|signin|apply-now/thank|equal-opportunit)`)

// Share and tracking parameters. Without this, "?share=twitter" on a careers
// page counts as a distinct deeper URL and burns a slot per social network.
var junkQueryRe = regexp.MustCompile(`(?i)(^|&)(share|utm_[a-z]+|fbclid|gclid|mc_cid|` +
	`mc_eid|ref|source)=`)

// Language switchers point at the same career page in another locale. The href
// matches every listing pattern, so without this a trilingual site spends its
// whole per-domain budget re-fetching one page.
var langSwitchRe = regexp.MustCompile(`(?i)^(\p{L}{2,3}|english|englisch|deutsch|german|` +
	`français|francais|french|español|espanol|spanish|italiano|italian|nederlands|dutch|` +
	`svenska|swedish|norsk|dansk|suomi|polski|português|portugues|česky|cesky)$`)

// A trailing numeric or hash-like segment marks an individual posting rather
// than a listing. Those are leaves: extraction already has the title from the
// link that pointed at them, so fetching each one buys a request for one job.
var leafPostingRe = regexp.MustCompile(`/(\d{4,}|[0-9a-f]{16,})/?$`)

// depthKeyRe splits an archive key into its firm and its depth. Depth 1 pages
// have no suffix; deeper ones carry __l<depth>_<n>.
var depthKeyRe = regexp.MustCompile(`^(.*)__l(\d+)_(\d+)$`)

func splitKey(key string) (domain string, depth int) {
	if m := depthKeyRe.FindStringSubmatch(key); m != nil {
		d, _ := strconv.Atoi(m[2])
		return m[1], d
	}
	return key, 1
}

type row struct{ company, domain, careerURL, ats string }

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

// readExclude loads every earlier level so a deeper run neither re-emits a URL
// that is already archived nor reuses a key, which would overwrite the stored
// HTML of a different page. It also returns how many pages each firm has spent,
// so the total budget holds across levels rather than resetting at each one.
func readExclude(paths string) (urls map[string]bool, taken map[string]map[string]bool, spent map[string]int) {
	urls, taken, spent = map[string]bool{}, map[string]map[string]bool{}, map[string]int{}
	for _, path := range strings.Split(paths, ",") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			log.Printf("exclude %s: %v (skipped)", path, err)
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
				key := strings.TrimSpace(rec[di])
				dom, _ := splitKey(key)
				if taken[dom] == nil {
					taken[dom] = map[string]bool{}
				}
				if !taken[dom][key] {
					taken[dom][key] = true
					spent[dom]++
				}
			}
		}
	}
	if len(urls) > 0 {
		log.Printf("exclude: %d urls already targeted across %d firms", len(urls), len(taken))
	}
	return
}

type cand struct {
	url   string
	text  string
	score float64
}

// Wording groups matching scripts/fit_link_model.py, so a fitted model and the
// fallback heuristic are scoring the same things.
var (
	listWordRe  = regexp.MustCompile(`(?i)\b(view|see|browse|search|explore|all|current|open)\b`)
	teamWordRe  = regexp.MustCompile(`(?i)(join|team|work (with|for) us|culture|life at|people)`)
	applyWordRe = regexp.MustCompile(`(?i)(apply|application|bewerb|candidature|postul)`)
	pageWordRe  = regexp.MustCompile(`(?i)(load more|show more|next|page \d|weiter|mehr)`)
	jobWordRe   = regexp.MustCompile(`(?i)(job|vacan|opening|position|role|opportunit|` +
		`stelle|vacature|emploi|offre|poste|jobb|lediga|stilling|empleo|oferta|` +
		`posizion|lavoro|praca|vaga|ty[oö]paik)`)
)

// linkModel is the logistic regression fitted by scripts/fit_link_model.py on
// the outcomes of an earlier crawl. Every link a previous run followed is a
// labelled example — the fetch settled whether the destination really held a
// list — so the ranking improves each time without anyone labelling anything.
type linkModel struct {
	Features  []string  `json:"features"`
	Coef      []float64 `json:"coef"`
	Intercept float64   `json:"intercept"`
	CVAuc     float64   `json:"cv_auc"`
	w         map[string]float64
}

func loadModel(path string) *linkModel {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m linkModel
	if json.Unmarshal(b, &m) != nil || len(m.Features) != len(m.Coef) {
		log.Printf("model %s unreadable; falling back to the heuristic", path)
		return nil
	}
	m.w = map[string]float64{}
	for i, f := range m.Features {
		m.w[f] = m.Coef[i]
	}
	log.Printf("link model: %d features, cross-validated AUC %.3f", len(m.Features), m.CVAuc)
	return &m
}

func (m *linkModel) score(text string, u *url.URL, depth int, parentHasJobs bool) float64 {
	segs := 0
	for _, s := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if s != "" {
			segs++
		}
	}
	b := func(cond bool) float64 {
		if cond {
			return 1
		}
		return 0
	}
	f := map[string]float64{
		"txt_list":    b(listWordRe.MatchString(text)),
		"txt_job":     b(jobWordRe.MatchString(text)),
		"txt_team":    b(teamWordRe.MatchString(text)),
		"txt_apply":   b(applyWordRe.MatchString(text)),
		"txt_page":    b(pageWordRe.MatchString(text)),
		"href_job":    b(jobWordRe.MatchString(u.Path)),
		"href_query":  b(u.RawQuery != ""),
		"depth_2":     b(depth == 2),
		"depth_3":     b(depth == 3),
		"depth_4plus": b(depth >= 4),
		"segs_norm":   math.Min(float64(segs), 8) / 8,
		"words_norm":  math.Min(float64(len(strings.Fields(text))), 12) / 12,
		"parent_dead": b(!parentHasJobs),
	}
	z := m.Intercept
	for k, v := range f {
		z += m.w[k] * v
	}
	return 1 / (1 + math.Exp(-z))
}

func main() {
	storeDir := env("STORE_DIR", "html_store")
	fromDepth := envInt("FROM_DEPTH", 1)
	nextDepth := fromDepth + 1
	outPath := env("OUTPUT", "career_pages_d"+strconv.Itoa(nextDepth)+".csv")
	perDomain := envInt("PER_DOMAIN", 4)
	maxDomain := envInt("MAX_DOMAIN", 14)

	l1 := readL1(env("INPUT", "career_pages.csv"))
	log.Printf("level-1 pages: %d | expanding depth %d -> %d", len(l1), fromDepth, nextDepth)
	model := loadModel(env("MODEL", "link_model.json"))

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		log.Fatalf("read %s: %v", storeDir, err)
	}

	seenURL, takenKey, spent := readExclude(env("EXCLUDE", ""))

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

	var pages, emitted, capped int

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html.gz") {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".html.gz")
		domain, depth := splitKey(key)
		// Expand one level per run. Anything shallower was expanded by an
		// earlier run; anything deeper belongs to a later one.
		if depth != fromDepth {
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
		pageURL, embedded := zr.Comment, zr.Name
		zr.Close()
		fh.Close()

		if embedded != "" {
			domain, _ = splitKey(embedded)
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

		if spent[domain] >= maxDomain {
			capped++
			continue
		}

		// Whether this page lists jobs already. Fitted on 7,391 followed links
		// it was the single strongest signal: a button on a page that already
		// has a list leads to another list, while the same button on an empty
		// careers page usually leads nowhere.
		pageHasJobs := false
		if model != nil {
			pageHasJobs = len(careerparser.Extract(string(body), pageURL)) >= 3
		}

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
			if junkQueryRe.MatchString(abs.RawQuery) || leafPostingRe.MatchString(abs.Path) {
				return
			}
			full := abs.String()
			if full == pageURL || seenURL[full] || rejectRe.MatchString(abs.Path) {
				return
			}
			// Do not climb back towards the site root. Going sideways or
			// deeper is fine — a listing often sits at /jobs while the page
			// that links to it sits at /careers/working-here, and requiring a
			// strictly longer path was what kept those out.
			if isAncestor(abs.Path, base.Path) && abs.RawQuery == "" {
				return
			}

			text := strings.Join(strings.Fields(s.Text()), " ")
			if langSwitchRe.MatchString(text) {
				return
			}
			// The patterns still decide what is even a candidate; the model
			// only decides which candidates are worth the firm's budget.
			// Keeping the gate rule-based means a bad fit degrades the ranking
			// rather than silently emptying the crawl.
			score := 0.0
			if linkTextRe.MatchString(text) {
				score += 3
			}
			if linkHrefRe.MatchString(abs.Path) {
				score += 2
			}
			if score == 0 {
				return
			}
			if model != nil {
				score = model.score(text, abs, nextDepth, pageHasJobs)
			}
			cands = append(cands, cand{full, text, score})
		})

		sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
		if takenKey[domain] == nil {
			takenKey[domain] = map[string]bool{}
		}
		n, slot := 0, 0
		for _, c := range cands {
			if n >= perDomain || spent[domain] >= maxDomain {
				break
			}
			if seenURL[c.url] {
				continue
			}
			seenURL[c.url] = true
			var k string
			for {
				slot++
				k = domain + "__l" + strconv.Itoa(nextDepth) + "_" + strconv.Itoa(slot)
				if !takenKey[domain][k] {
					break
				}
			}
			takenKey[domain][k] = true
			spent[domain]++
			_ = w.Write([]string{meta.company, k, c.url, meta.ats, "", "", c.text, pageURL})
			emitted++
			n++
		}
	}

	log.Printf("DONE | scanned %d depth-%d pages | %d depth-%d targets | %d firms at budget -> %s",
		pages, fromDepth, emitted, nextDepth, capped, outPath)
	if emitted == 0 {
		log.Printf("nothing new at this level; the walk has bottomed out")
	} else {
		log.Printf("next: INPUT=%s fetch_career_html", outPath)
	}
}

// isAncestor reports whether a is at or above b in the same path tree, which is
// the one direction a walk inwards must never take.
func isAncestor(a, b string) bool {
	as := strings.Trim(a, "/")
	bs := strings.Trim(b, "/")
	if as == bs {
		return true
	}
	return as == "" || strings.HasPrefix(bs+"/", as+"/")
}
