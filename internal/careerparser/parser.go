// Package careerparser extracts job listings from career pages that do not
// expose a recognised ATS. These are typically small and mid-size firms that
// hand-write their openings directly into HTML, and they are invisible to the
// ATS slug-resolution path in cmd/career_finder.
//
// Three strategies run in order of decreasing confidence:
//
//  1. schema.org JobPosting embedded as JSON-LD  (confidence 0.95)
//  2. repeated job-link anchors sharing a URL shape (confidence 0.70)
//  3. heading/list-item blocks that look like job titles (confidence 0.45)
//
// The first strategy that yields results wins, so a page with valid JSON-LD is
// never second-guessed by the heuristics.
package careerparser

import (
	"encoding/json"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Job is a single extracted opening. Field names align with the ATS job schema
// so hardcoded and ATS-sourced rows can be merged into one dataset.
type Job struct {
	Title      string
	URL        string
	Location   string
	Department string
	PostedAt   string // ISO-8601 date, empty when the page does not state one
	Method     string // jsonld | links | headings
	Confidence float64
}

var (
	// URL path segments that indicate a link points at an individual posting.
	jobPathRe = regexp.MustCompile(`(?i)/(jobs?|careers?|vacanc(?:y|ies)|positions?|openings?|` +
		`stelle[n]?|stellenangebote|vacature[s]?|offre[s]?-?d?-?emploi|emploi|` +
		`lediga-?jobb|jobb|empleo|ofertas?|lavora-con-noi|posizion[ei])/`)

	// Anchor text that is navigation or boilerplate rather than a job title.
	noiseRe = regexp.MustCompile(`(?i)^(all|view all|see all|browse|search|filter|more|show more|` +
		`apply|apply now|learn more|read more|back|next|previous|home|contact|about|` +
		`life at|why join|our culture|benefits|diversity|graduate programme|sign up|` +
		`login|register|cookie|privacy|terms|newsletter|share|email|linkedin|twitter)\b`)

	locHintRe = regexp.MustCompile(`(?i)\b(remote|hybrid|on-?site|location)\b`)

	wsRe = regexp.MustCompile(`\s+`)

	// Generic site vocabulary that appears as link text on nearly every career
	// page. Matched whole-string, so "Careers" is rejected while "Careers
	// Operations Manager" survives.
	genericExact = map[string]bool{
		"careers": true, "career": true, "jobs": true, "job": true,
		"vacancies": true, "vacancy": true, "open roles": true, "open positions": true,
		"positions": true, "opportunities": true, "current openings": true,
		"what we do": true, "who we are": true, "our team": true, "team": true,
		"about us": true, "our story": true, "our people": true, "our values": true,
		"news": true, "blog": true, "events": true, "press": true, "media": true,
		"investors": true, "products": true, "services": true, "solutions": true,
		"insights": true, "resources": true, "sustainability": true, "esg": true,
		"locations": true, "offices": true, "culture": true, "students": true,
		"graduates": true, "internships": true, "professionals": true,
	}
)

func clean(s string) string {
	return strings.TrimSpace(wsRe.ReplaceAllString(strings.ReplaceAll(s, " ", " "), " "))
}

// plausibleTitle rejects strings that are too short, too long, or obviously
// navigation. Job titles in the wild cluster tightly in this range.
func plausibleTitle(s string) bool {
	if len(s) < 4 || len(s) > 140 {
		return false
	}
	if noiseRe.MatchString(s) {
		return false
	}
	if genericExact[strings.ToLower(strings.Trim(s, " .:-|›»→"))] {
		return false
	}
	// A title with no letters is a date, a number, or an icon.
	hasLetter := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
			break
		}
	}
	return hasLetter
}

func abs(base *url.URL, href string) string {
	if base == nil {
		return href
	}
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return href
	}
	return base.ResolveReference(u).String()
}

// ── strategy 1: JSON-LD ──────────────────────────────────────────────────────

func typeIs(v interface{}, want string) bool {
	switch t := v.(type) {
	case string:
		return strings.EqualFold(t, want)
	case []interface{}:
		for _, x := range t {
			if s, ok := x.(string); ok && strings.EqualFold(s, want) {
				return true
			}
		}
	}
	return false
}

// flattenLD walks arbitrary JSON-LD (objects, arrays, @graph) collecting nodes.
func flattenLD(v interface{}, out *[]map[string]interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		*out = append(*out, t)
		if g, ok := t["@graph"]; ok {
			flattenLD(g, out)
		}
	case []interface{}:
		for _, x := range t {
			flattenLD(x, out)
		}
	}
}

func locationFromLD(v interface{}) string {
	m, ok := v.(map[string]interface{})
	if !ok {
		if arr, ok := v.([]interface{}); ok && len(arr) > 0 {
			return locationFromLD(arr[0])
		}
		return ""
	}
	if addr, ok := m["address"].(map[string]interface{}); ok {
		var parts []string
		for _, k := range []string{"addressLocality", "addressRegion", "addressCountry"} {
			if s, ok := addr[k].(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	}
	if s, ok := m["name"].(string); ok {
		return s
	}
	return ""
}

func fromJSONLD(doc *goquery.Document, base *url.URL) []Job {
	var jobs []Job
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		var raw interface{}
		if err := json.Unmarshal([]byte(s.Text()), &raw); err != nil {
			return
		}
		var nodes []map[string]interface{}
		flattenLD(raw, &nodes)
		for _, n := range nodes {
			if !typeIs(n["@type"], "JobPosting") {
				continue
			}
			title, _ := n["title"].(string)
			if title == "" {
				title, _ = n["name"].(string)
			}
			if !plausibleTitle(clean(title)) {
				continue
			}
			posted, _ := n["datePosted"].(string)
			if len(posted) > 10 {
				posted = posted[:10]
			}
			href, _ := n["url"].(string)
			jobs = append(jobs, Job{
				Title:      clean(title),
				URL:        abs(base, href),
				Location:   clean(locationFromLD(n["jobLocation"])),
				PostedAt:   posted,
				Method:     "jsonld",
				Confidence: 0.95,
			})
		}
	})
	return jobs
}

// ── strategy 2: repeated job links ───────────────────────────────────────────

// fromJobLinks finds anchors whose href looks like an individual posting. A
// single such link is usually navigation; three or more sharing a URL shape is
// a job list. Grouping by path prefix suppresses one-off matches.
func fromJobLinks(doc *goquery.Document, base *url.URL) []Job {
	type cand struct{ title, href, loc string }
	groups := map[string][]cand{}

	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") {
			return
		}
		full := abs(base, href)
		u, err := url.Parse(full)
		if err != nil || !jobPathRe.MatchString(u.Path) {
			return
		}
		// Require a segment beyond the listing path itself, i.e. an actual posting.
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segs) < 2 {
			return
		}
		title := clean(s.Text())
		if !plausibleTitle(title) {
			return
		}
		// Nearby text often carries the location.
		loc := ""
		if p := s.Parent(); p != nil {
			t := clean(p.Text())
			if locHintRe.MatchString(t) && len(t) < 200 {
				loc = t
			}
		}
		key := strings.Join(segs[:len(segs)-1], "/")
		groups[key] = append(groups[key], cand{title, full, loc})
	})

	var jobs []Job
	seen := map[string]bool{}
	for _, list := range groups {
		if len(list) < 3 {
			continue // too few to be a listing
		}
		for _, c := range list {
			if seen[c.href] {
				continue
			}
			seen[c.href] = true
			jobs = append(jobs, Job{
				Title: c.title, URL: c.href, Location: c.loc,
				Method: "links", Confidence: 0.70,
			})
		}
	}
	return jobs
}

// ── strategy 3: heading and list blocks ──────────────────────────────────────

// fromHeadings is the last resort for pages that list openings as plain text
// with no per-job link at all.
//
// DISABLED BY DEFAULT. Measured against 4,571 real career pages it produced
// 118,947 rows, 93% of all output, and essentially none of it was jobs — it
// matched values statements, benefits lists and navigation ("A place to be",
// "Coworking allowance", "Do the right thing"). Without a per-job link there is
// no structural signal separating a job title from any other short phrase on
// the page, so precision cannot be recovered by tightening alone.
//
// Enable with CAREERPARSER_HEADINGS=1 only when hand-reviewing the output.
func fromHeadings(doc *goquery.Document) []Job {
	if os.Getenv("CAREERPARSER_HEADINGS") != "1" {
		return nil
	}
	var jobs []Job
	seen := map[string]bool{}
	doc.Find("li, h3, h4, .job, .vacancy, .position, [class*=job], [class*=vacanc]").
		Each(func(_ int, s *goquery.Selection) {
			if s.Find("a").Length() > 3 {
				return // a container, not a row
			}
			t := clean(s.Text())
			if !plausibleTitle(t) || seen[t] {
				return
			}
			// Job titles are short phrases, not sentences.
			if strings.Count(t, " ") > 10 || strings.Contains(t, ".") && len(t) > 90 {
				return
			}
			seen[t] = true
			jobs = append(jobs, Job{Title: t, Method: "headings", Confidence: 0.45})
		})
	if len(jobs) < 3 || len(jobs) > 60 {
		return nil // too few to be a list, or we matched the whole page
	}
	return jobs
}

// ── entry point ──────────────────────────────────────────────────────────────

// Extract parses a career page and returns the openings it advertises.
// pageURL is used to resolve relative links and may be empty.
func Extract(html string, pageURL string) []Job {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	var base *url.URL
	if pageURL != "" {
		base, _ = url.Parse(pageURL)
	}

	if jobs := fromJSONLD(doc, base); len(jobs) > 0 {
		return dedupe(jobs)
	}
	if jobs := fromJobLinks(doc, base); len(jobs) > 0 {
		return dedupe(jobs)
	}
	return dedupe(fromHeadings(doc))
}

func dedupe(jobs []Job) []Job {
	seen := map[string]bool{}
	out := jobs[:0]
	for _, j := range jobs {
		k := strings.ToLower(j.Title) + "|" + j.URL
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, j)
	}
	return out
}

// ParsedAt stamps a run so downstream consumers can age the data.
func ParsedAt() string { return time.Now().UTC().Format("2006-01-02") }
