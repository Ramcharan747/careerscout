package careerparser

import (
	"regexp"
	"strings"
)

// Doors describes every way into a firm that a career page offers, not just the
// one the extractor cares about.
//
// A listing is the obvious door and the rarest. Most funds are fifteen people
// and never have an opening posted, but the page still says "we are always
// interested in exceptional people, send us your CV" — which for someone
// writing to a partner directly is a better door than a posted analyst role
// they would be one of four hundred applicants for. Treating those pages as
// "no jobs" throws away most of the addressable list.
//
// Kept separate from Extract because they answer different questions. Extract
// asks what this page is advertising; Doors asks whether it is worth writing to
// this firm at all.
type Door struct {
	OpenRoles    int    // listings the extractor found
	Speculative  bool   // invites an unsolicited application
	Internship   bool   // names an internship, placement or graduate programme
	CareersEmail string // a mailto aimed at hiring rather than general enquiries
	CVUpload     bool   // a form that takes a file
	Dead         bool   // parked, 404, expired, or squatted
}

// Actionable reports whether there is any way in at all.
func (d Door) Actionable() bool {
	return !d.Dead && (d.OpenRoles > 0 || d.Speculative || d.Internship ||
		d.CareersEmail != "" || d.CVUpload)
}

// Best names the strongest door, for sorting a target list.
func (d Door) Best() string {
	switch {
	case d.Dead:
		return "dead"
	case d.OpenRoles > 0 && d.Internship:
		return "open_roles_incl_internship"
	case d.OpenRoles > 0:
		return "open_roles"
	case d.Internship:
		return "internship_programme"
	case d.Speculative && d.CVUpload:
		return "speculative_form"
	case d.Speculative:
		return "speculative"
	case d.CVUpload:
		return "upload_form"
	case d.CareersEmail != "":
		return "email_only"
	default:
		return "none"
	}
}

// Merge folds another page of the same firm in. A firm's doors are the union of
// what its pages offer: the landing page says "send us your CV" and the page
// two hops in carries the listing, and both are true of the firm.
func (d Door) Merge(o Door) Door {
	d.OpenRoles += o.OpenRoles
	d.Speculative = d.Speculative || o.Speculative
	d.Internship = d.Internship || o.Internship
	d.CVUpload = d.CVUpload || o.CVUpload
	if d.CareersEmail == "" {
		d.CareersEmail = o.CareersEmail
	}
	// One live page is enough to say the firm is not dead.
	d.Dead = d.Dead && o.Dead
	return d
}

var (
	// An invitation to apply without a posted role. Written across the
	// languages the archive actually contains, because the firms that make this
	// offer skew European and mid-market — exactly the ones worth writing to.
	speculativeRe = regexp.MustCompile(`(?i)` +
		`initiativbewerbung|unaufgeforderte bewerbung|blindbewerbung|` +
		`spontaneous application|speculative application|open application|` +
		`unsolicited application|general application|` +
		`candidature spontan|open sollicitatie|spontane sollicitatie|` +
		`spontaan sollicit|solliciteren|` + // nl: the stem is solliciter-, not sollicitatie
		`spontanans[oö]kan|åpen søknad|` +
		// fr: "no position currently available, send us your application" is the
		// single most common European phrasing and none of it was here.
		`aucun poste|pas de poste|poste[s]? [àa] pourvoir|envoyez[- ]nous|` +
		`envoyez votre candidature|transmettre votre candidature|` +
		`n'h[ée]sitez pas [àa] nous|adressez[- ]nous|` +
		`keine offenen stellen|derzeit keine|senden sie uns ihre|` +
		`schicken sie uns ihre|wir freuen uns auf ihre bewerbung|` +
		`send (us )?your (cv|r[ée]sum)|submit your (cv|r[ée]sum)|` +
		`drop (us )?your (cv|r[ée]sum)|share your (cv|r[ée]sum)|` +
		`don'?t see (a|the|any) (right )?(role|position|job|opening)|` +
		`can'?t find (a|the|any) (right )?(role|position|job|opening)|` +
		`no (current|suitable|open) (openings?|vacanc|positions?|roles?)|` +
		`always (looking|interested|on the lookout|keen)|` +
		`we are always|we're always|wir freuen uns (jederzeit|immer)`)

	// Internship, placement and graduate wording. "stage" is French and Dutch
	// for a placement but also an ordinary English word, so it is bounded and
	// paired with nothing else — a false positive here only adds a firm to a
	// list a human will read anyway.
	internshipRe = regexp.MustCompile(`(?i)` +
		`internship|\bintern\b|\binterns\b|praktikum|praktikant|praktik\b|` +
		`werkstudent|\bstage\b|stagiair|stagiaire|` +
		`graduate (program|programme|scheme)|summer (analyst|associate)|` +
		`off-?cycle|\btrainee\b|becario|pr[aá]cticas|tirocin`)

	// Pages that exist but say nothing: parked domains, 404s, expired domains
	// resold to spam. 14.5% of the archived fund pages, which is high enough
	// that leaving them in a target list would waste real outreach.
	deadPageRe = regexp.MustCompile(`(?i)` +
		`404|page not found|page doesn'?t exist|nothing was found|` +
		`domain (geparkt|parked|for sale)|\bis for sale\b|buy this domain|brandbucket|` +
		`sedo\.com|afternic|dan\.com|hugedomains|` +
		`under construction|coming soon|site temporarily unavailable|` +
		`account suspended|slot gacor|situs slot|judi bola|togel`)

	hiringMailRe = regexp.MustCompile(`(?i)^(career|job|hr|recruit|bewerb|talent|people|` +
		`personal|hiring|apply|application|cv)`)
	mailtoRe = regexp.MustCompile(`(?i)mailto:([^"'>?\s]+)`)
	fileInRe = regexp.MustCompile(`(?i)<input[^>]+type=["']?file`)
)

// Doors classifies one archived page.
func Doors(html, pageURL string) Door {
	var d Door
	text := clean(stripTags(html))

	// A page with almost no text is not a page. Judged before anything else,
	// because a parked domain's boilerplate can otherwise trip the speculative
	// patterns ("coming soon, get in touch").
	if len(text) < 200 || deadPageRe.MatchString(firstN(text, 400)) {
		d.Dead = true
		return d
	}

	d.OpenRoles = len(Extract(html, pageURL))
	d.Speculative = speculativeRe.MatchString(text)
	d.Internship = internshipRe.MatchString(text)
	d.CVUpload = fileInRe.MatchString(html)

	for _, m := range mailtoRe.FindAllStringSubmatch(html, -1) {
		addr := strings.TrimSpace(m[1])
		local, _, ok := strings.Cut(addr, "@")
		if !ok || !hiringMailRe.MatchString(local) {
			continue
		}
		d.CareersEmail = addr
		break
	}
	return d
}

var tagStripRe = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)

func stripTags(html string) string { return tagStripRe.ReplaceAllString(html, " ") }

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
