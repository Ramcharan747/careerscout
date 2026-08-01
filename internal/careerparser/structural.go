package careerparser

import (
	"regexp"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Structural extraction.
//
// A job board is a repeating structure before it is anything else: N sibling
// elements sharing a tag and class shape, each holding one posting. That
// repetition is the signal. Vocabulary ("engineer", "/jobs/") is only useful
// afterwards, to decide which of several repeating groups on a page is the job
// list rather than the blog roll or the footer nav.
//
// Matching on vocabulary first, as the earlier link strategy did, fails on any
// site whose URLs do not happen to contain a recognised job word. Measured over
// 4,571 real career pages that cost ~51% of extractable pages.

var (
	// Digits and hashes in class names are per-instance noise (grid-item-3,
	// css-1x9k2h). Stripping them lets siblings collapse to one signature.
	classNoiseRe = regexp.MustCompile(`[0-9]+|--[a-z0-9]{4,}|_[a-z0-9]{5,}`)

	jobVocabRe = regexp.MustCompile(`(?i)job|career|vacan|position|opening|opportunit|` +
		`role|listing|posting|stelle|vacature|emploi|jobb|empleo|posizion|recruit`)

	locVocabRe = regexp.MustCompile(`(?i)location|office|city|country|region|place|standort|` +
		`remote|hybrid|onsite|on-site`)

	deptVocabRe = regexp.MustCompile(`(?i)department|team|category|function|discipline|abteilung`)

	dateLikeRe = regexp.MustCompile(`\b(20\d{2}-\d{2}-\d{2}|\d{1,2}[./-]\d{1,2}[./-]\d{2,4})\b`)
)

// signature reduces an element to its structural shape so siblings that render
// the same template collapse to one key.
func signature(s *goquery.Selection) string {
	node := s.Get(0)
	if node == nil {
		return ""
	}
	tag := node.Data
	class, _ := s.Attr("class")
	class = classNoiseRe.ReplaceAllString(strings.ToLower(class), "")
	toks := strings.Fields(class)
	sort.Strings(toks)
	if len(toks) > 4 {
		toks = toks[:4]
	}
	// Child tag shape distinguishes a job row from a plain <li> bullet.
	var kids []string
	s.Children().Each(func(_ int, c *goquery.Selection) {
		if n := c.Get(0); n != nil {
			kids = append(kids, n.Data)
		}
	})
	if len(kids) > 5 {
		kids = kids[:5]
	}
	return tag + "|" + strings.Join(toks, ".") + "|" + strings.Join(kids, ",")
}

type group struct {
	members []*goquery.Selection
	sig     string
	score   float64
}

// chromeRe marks page furniture. Repetition inside these regions is a menu,
// a footer or a cookie banner, never a job list.
var chromeRe = regexp.MustCompile(`(?i)nav|menu|footer|header|breadcrumb|cookie|consent|` +
	`social|share|lang|locale|pagination|pager|sidebar|widget|banner|carousel|slider`)

// inChrome reports whether a node sits inside navigation or footer furniture.
func inChrome(s *goquery.Selection) bool {
	if s.Closest("nav,footer,header").Length() > 0 {
		return true
	}
	found := false
	s.ParentsUntil("body").EachWithBreak(func(_ int, p *goquery.Selection) bool {
		cls, _ := p.Attr("class")
		id, _ := p.Attr("id")
		role, _ := p.Attr("role")
		if chromeRe.MatchString(cls) || chromeRe.MatchString(id) ||
			role == "navigation" || role == "banner" || role == "contentinfo" {
			found = true
			return false
		}
		return true
	})
	return found
}

// findRepeatingGroups collects every set of >=3 structurally identical siblings.
func findRepeatingGroups(doc *goquery.Document) []group {
	var groups []group
	doc.Find("*").Each(func(_ int, parent *goquery.Selection) {
		kids := parent.Children()
		if kids.Length() < 3 || kids.Length() > 400 {
			return
		}
		if inChrome(parent) {
			return
		}
		bySig := map[string][]*goquery.Selection{}
		kids.Each(func(_ int, c *goquery.Selection) {
			if sig := signature(c); sig != "" {
				bySig[sig] = append(bySig[sig], c)
			}
		})
		for sig, members := range bySig {
			if len(members) >= 3 && len(members) <= 300 {
				groups = append(groups, group{members: members, sig: sig})
			}
		}
	})
	return groups
}

// scoreGroup rates how much a repeating group looks like a list of job
// postings. Structure carries most of the weight; vocabulary breaks ties.
func scoreGroup(g group, parentClassHint string) float64 {
	n := float64(len(g.members))
	var (
		withLink   int
		withinLen  int
		withLoc    int
		withDate   int
		textLens   []int
		distinct   = map[string]bool{}
		vocabInSig = jobVocabRe.MatchString(g.sig) || jobVocabRe.MatchString(parentClassHint)
	)

	for _, m := range g.members {
		txt := clean(m.Text())
		textLens = append(textLens, len(txt))
		distinct[strings.ToLower(txt)] = true
		if m.Find("a[href]").Length() > 0 || m.Is("a") {
			withLink++
		}
		if l := len(txt); l >= 8 && l <= 400 {
			withinLen++
		}
		html, _ := m.Html()
		if locVocabRe.MatchString(html) {
			withLoc++
		}
		if dateLikeRe.MatchString(txt) {
			withDate++
		}
	}

	// Every member showing identical text is navigation or a spacer, not a list.
	if len(distinct) < len(g.members)/2 {
		return -100
	}

	sort.Ints(textLens)
	median := textLens[len(textLens)/2]
	if median < 6 || median > 600 {
		return -100
	}

	// Link cohesion is the vocabulary-free discriminator between a job list and
	// a menu. Postings live under one deep path (/careers/x, /careers/y);
	// navigation scatters across the site root (/about, /products, /contact).
	cohesion, deep := linkCohesion(g.members)

	// Structure alone is not evidence. A group must show at least one positive
	// job signal, or it is furniture that happens to repeat.
	if !vocabInSig && cohesion < 0.6 && withLoc < len(g.members)/2 {
		return -100
	}
	// Mostly one-word entries are categories, not job titles.
	if medianWords(g.members) < 2 {
		return -100
	}

	score := 0.0
	score += 2.0 * float64(withLink) / n  // postings are almost always linked
	score += 1.5 * float64(withinLen) / n // consistent, title-sized text
	score += 1.5 * float64(withLoc) / n   // a location column is a strong tell
	score += 0.5 * float64(withDate) / n
	score += 2.5 * cohesion // shared deep path prefix
	if deep {
		score += 1.0
	}
	if vocabInSig {
		score += 2.0
	}
	// Prefer real lists over three-item teasers, with diminishing returns.
	switch {
	case n >= 8:
		score += 1.5
	case n >= 5:
		score += 1.0
	}
	return score
}

// linkCohesion measures how tightly member links cluster under a shared path
// prefix. Returns the clustered fraction and whether that prefix is deep
// (at least two segments), which distinguishes /careers/eng/123 from /about.
func linkCohesion(members []*goquery.Selection) (float64, bool) {
	var paths []string
	seen := map[string]bool{}
	for _, m := range members {
		h, ok := m.Find("a[href]").First().Attr("href")
		if !ok {
			if h, ok = m.Attr("href"); !ok {
				continue
			}
		}
		h = strings.TrimSpace(h)
		if h == "" || strings.HasPrefix(h, "#") || strings.HasPrefix(h, "mailto:") {
			continue
		}
		if i := strings.IndexAny(h, "?#"); i >= 0 {
			h = h[:i]
		}
		if seen[h] {
			continue // repeated identical href is navigation
		}
		seen[h] = true
		paths = append(paths, strings.Trim(h, "/"))
	}
	if len(paths) < 3 {
		return 0, false
	}

	// Longest first segment shared by the largest subset.
	firstSeg := map[string]int{}
	for _, p := range paths {
		segs := strings.Split(p, "/")
		if len(segs) >= 2 {
			firstSeg[segs[0]]++
		}
	}
	bestKey, best := "", 0
	for k, v := range firstSeg {
		if v > best {
			bestKey, best = k, v
		}
	}
	_ = bestKey
	return float64(best) / float64(len(paths)), best >= 3
}

func medianWords(members []*goquery.Selection) int {
	var counts []int
	for _, m := range members {
		t := clean(m.Text())
		if t == "" {
			continue
		}
		counts = append(counts, len(strings.Fields(t)))
	}
	if len(counts) == 0 {
		return 0
	}
	sort.Ints(counts)
	return counts[len(counts)/2]
}

// extractFromGroup pulls one Job per member of the winning group.
func extractFromGroup(g group, base *urlBase) []Job {
	var jobs []Job
	for _, m := range g.members {
		var title, href string

		// The anchor with the longest plausible text is the title far more
		// reliably than document order.
		m.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
			t := clean(a.Text())
			if plausibleTitle(t) && len(t) > len(title) {
				title = t
				href, _ = a.Attr("href")
			}
			return true
		})
		if title == "" && m.Is("a") {
			title = clean(m.Text())
			href, _ = m.Attr("href")
		}
		if title == "" {
			// Unlinked rows: fall back to the first heading-ish child.
			m.Find("h1,h2,h3,h4,h5,strong,b,[class*=title],[class*=name]").
				EachWithBreak(func(_ int, h *goquery.Selection) bool {
					if t := clean(h.Text()); plausibleTitle(t) {
						title = t
						return false
					}
					return true
				})
		}
		if !plausibleTitle(title) {
			continue
		}

		loc := firstMatchingChild(m, locVocabRe, title)
		dept := firstMatchingChild(m, deptVocabRe, title)
		posted := ""
		if d := dateLikeRe.FindString(clean(m.Text())); d != "" {
			posted = d
		}

		jobs = append(jobs, Job{
			Title:      title,
			URL:        base.resolve(href),
			Location:   loc,
			Department: dept,
			PostedAt:   posted,
			Method:     "structural",
			Confidence: 0.80,
		})
	}
	return jobs
}

// firstMatchingChild returns the text of the first descendant whose class
// matches vocab, excluding the title itself.
func firstMatchingChild(m *goquery.Selection, vocab *regexp.Regexp, exclude string) string {
	out := ""
	m.Find("*[class]").EachWithBreak(func(_ int, c *goquery.Selection) bool {
		cls, _ := c.Attr("class")
		if !vocab.MatchString(cls) {
			return true
		}
		t := clean(c.Text())
		if t != "" && t != exclude && len(t) < 120 {
			out = t
			return false
		}
		return true
	})
	return out
}

// nearestJobHeading reports whether the group sits under, or just after, a
// heading that announces openings. A repeating list of property adverts is
// structurally identical to a repeating list of jobs; only the surrounding
// label separates them.
func nearestJobHeading(g group) bool {
	node := g.members[0]
	for i := 0; i < 6; i++ {
		p := node.Parent()
		if p.Length() == 0 {
			break
		}
		// A heading anywhere in this container, or immediately before it.
		found := false
		p.Find("h1,h2,h3,h4,[class*=title],[class*=heading]").EachWithBreak(
			func(_ int, h *goquery.Selection) bool {
				if jobVocabRe.MatchString(clean(h.Text())) {
					found = true
					return false
				}
				return true
			})
		if found {
			return true
		}
		p.PrevAll().EachWithBreak(func(i int, s *goquery.Selection) bool {
			if i > 2 {
				return false
			}
			if jobVocabRe.MatchString(clean(s.Text())) && len(clean(s.Text())) < 120 {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
		node = p
	}
	return false
}

// pageAdvertisesJobs gates extraction at document level. Without it, a page
// with no openings still surrenders its best-scoring repeating group, which is
// how cookie preferences and press releases end up in the output.
func pageAdvertisesJobs(doc *goquery.Document) bool {
	sel := doc.Find("h1,h2,h3,h4,title,[class*=title],[class*=heading],button,[class*=filter]")
	found := false
	sel.EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if jobVocabRe.MatchString(clean(s.Text())) {
			found = true
			return false
		}
		return true
	})
	return found
}

// fromStructure is the primary extraction path: find every repeating sibling
// group, score them, and take the winner if it clears the confidence floor.
func fromStructure(doc *goquery.Document, base *urlBase) []Job {
	if !pageAdvertisesJobs(doc) {
		return nil
	}
	groups := findRepeatingGroups(doc)
	if len(groups) == 0 {
		return nil
	}

	best := group{score: -1e9}
	for _, g := range groups {
		hint := ""
		if p := g.members[0].Parent(); p != nil {
			hint, _ = p.Attr("class")
		}
		s := scoreGroup(g, hint)
		if s > -50 && nearestJobHeading(g) {
			s += 2.5 // labelled as a jobs section
		}
		g.score = s
		if g.score > best.score {
			best = g
		}
	}
	if best.score < 5.5 {
		return nil
	}
	return extractFromGroup(best, base)
}
