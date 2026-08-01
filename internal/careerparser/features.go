package careerparser

import (
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Feature extraction for supervised tuning.
//
// The heuristic in scoreGroup was hand-weighted and, measured against real
// pages, admits roughly three non-jobs for every job. Rather than guess at the
// weights again, Candidates exposes the same signals as a labelled row so the
// thresholds can be fitted to hand-labelled ground truth.

// Features describes one repeating group numerically. Every field is either a
// ratio in [0,1] or a small count, so rows are directly usable for fitting.
type Features struct {
	NMembers      int     `json:"n_members"`
	LinkRatio     float64 `json:"link_ratio"`       // members containing an <a>
	Cohesion      float64 `json:"cohesion"`         // share of links under one path prefix
	CohesionDeep  int     `json:"cohesion_deep"`    // 1 if that prefix holds >=3 links
	MedianWords   int     `json:"median_words"`     // words in member text
	MedianTextLen int     `json:"median_text_len"`  // chars in member text
	LocRatio      float64 `json:"loc_ratio"`        // members with location-ish markup
	DateRatio     float64 `json:"date_ratio"`       // members containing a date
	DistinctRatio float64 `json:"distinct_ratio"`   // unique member texts / members
	VocabInSig    int     `json:"vocab_in_sig"`     // 1 if job word in class signature
	JobHeadingNr  int     `json:"job_heading_near"` // 1 if a jobs heading is nearby
	DepthFromBody int     `json:"depth_from_body"`
	HeuristicScr  float64 `json:"heuristic_score"` // current hand-weighted score
}

// CandidateGroup is one repeating group plus enough context to label it.
type CandidateGroup struct {
	Signature string   `json:"signature"`
	Samples   []string `json:"samples"`
	SampleURL []string `json:"sample_urls"`
	Feat      Features `json:"features"`
}

func featurise(g group, hint string) Features {
	n := float64(len(g.members))
	var withLink, withLoc, withDate int
	var textLens []int
	distinct := map[string]bool{}

	for _, m := range g.members {
		txt := clean(m.Text())
		textLens = append(textLens, len(txt))
		distinct[strings.ToLower(txt)] = true
		if m.Find("a[href]").Length() > 0 || m.Is("a") {
			withLink++
		}
		html, _ := m.Html()
		if locVocabRe.MatchString(html) {
			withLoc++
		}
		if dateLikeRe.MatchString(txt) {
			withDate++
		}
	}
	sort.Ints(textLens)
	cohesion, deep := linkCohesion(g.members)

	depth := 0
	g.members[0].ParentsUntil("body").Each(func(_ int, _ *goquery.Selection) { depth++ })

	f := Features{
		NMembers:      len(g.members),
		LinkRatio:     float64(withLink) / n,
		Cohesion:      cohesion,
		MedianWords:   medianWords(g.members),
		MedianTextLen: textLens[len(textLens)/2],
		LocRatio:      float64(withLoc) / n,
		DateRatio:     float64(withDate) / n,
		DistinctRatio: float64(len(distinct)) / n,
		DepthFromBody: depth,
	}
	if deep {
		f.CohesionDeep = 1
	}
	if jobVocabRe.MatchString(g.sig) || jobVocabRe.MatchString(hint) {
		f.VocabInSig = 1
	}
	if nearestJobHeading(g) {
		f.JobHeadingNr = 1
	}
	f.HeuristicScr = scoreGroup(g, hint)
	return f
}

// Candidates returns every repeating group on a page with its features and a
// few sample rows, ranked by the current heuristic. Used to build labelling
// sets; extraction itself goes through Extract.
func Candidates(html, pageURL string, maxGroups, maxSamples int) []CandidateGroup {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	base := newURLBase(pageURL)

	var out []CandidateGroup
	for _, g := range findRepeatingGroups(doc) {
		hint := ""
		if p := g.members[0].Parent(); p != nil {
			hint, _ = p.Attr("class")
		}
		cg := CandidateGroup{Signature: g.sig, Feat: featurise(g, hint)}
		for i, m := range g.members {
			if i >= maxSamples {
				break
			}
			t := clean(m.Text())
			if len(t) > 90 {
				t = t[:90]
			}
			cg.Samples = append(cg.Samples, t)
			if h, ok := m.Find("a[href]").First().Attr("href"); ok {
				cg.SampleURL = append(cg.SampleURL, base.resolve(h))
			} else {
				cg.SampleURL = append(cg.SampleURL, "")
			}
		}
		out = append(out, cg)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Feat.HeuristicScr > out[j].Feat.HeuristicScr
	})
	if maxGroups > 0 && len(out) > maxGroups {
		out = out[:maxGroups]
	}
	return out
}
