// Command extract_apply_targets pulls the machine-actionable part of a career
// page: exactly where an application goes and what it has to contain.
//
// The LLM pass reads prose. Application mechanics are not prose — they are form
// actions, field names, input types and mail addresses, and a parser reads those
// exactly while a model paraphrases them. Field names in particular have to be
// character-exact or a later submission silently fails, so this is deliberately
// deterministic and does not go through the model at all.
//
// Per firm it emits every route in:
//
//	forms       absolute action URL, method, enctype, and every field with its
//	            name, type, label, required flag and accepted file types
//	emails      addresses that are aimed at hiring, with the text around them
//	apply_urls  absolute URLs behind apply/vacancy links, deduplicated
//
// Forms protected by a CAPTCHA are flagged rather than treated as submittable.
// A quarter of the upload forms in this archive carry one; they are a human
// route, not an automated one.
//
// Input:  html_store/  +  a firm list
// Output: apply_targets.jsonl, one object per firm
//
// Env:
//
//	STORE_DIR   archive directory (default html_store)
//	FIRMS       firm list with a domain column (default funds_crawl_list.csv)
//	OUTPUT      default apply_targets.jsonl
package main

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var (
	depthKeyRe = regexp.MustCompile(`^(.*)__l(\d+)_(\d+)$`)
	captchaRe  = regexp.MustCompile(`(?i)recaptcha|hcaptcha|turnstile|captcha`)
	hiringRe   = regexp.MustCompile(`(?i)^(career|job|hr|recruit|bewerb|talent|people|` +
		`personal|hiring|apply|application|cv|kandidat|resume)`)
	applyLinkRe = regexp.MustCompile(`(?i)apply|bewerb|postul|solliciteer|ans[oö]k|` +
		`candidat|aplicar|candidat|vacanc|stelle|position|job`)
	// Honeypot and framework plumbing. Carrying them into a later submission is
	// how a form gets rejected as spam, so they are marked rather than dropped.
	plumbingRe = regexp.MustCompile(`(?i)^(_wpcf7|_token|csrf|nonce|honeypot|hp_|` +
		`form_id|post_id|queried_id|referer|max_file_size|action$)`)
)

type field struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Label    string   `json:"label,omitempty"`
	Required bool     `json:"required,omitempty"`
	Accept   string   `json:"accept,omitempty"`
	Options  []string `json:"options,omitempty"`
	Plumbing bool     `json:"plumbing,omitempty"`
	Autofill string   `json:"autofill,omitempty"`
}

type form struct {
	PageURL    string  `json:"page_url"`
	Action     string  `json:"action"`
	Method     string  `json:"method"`
	Enctype    string  `json:"enctype,omitempty"`
	HasCaptcha bool    `json:"has_captcha"`
	AcceptsCV  bool    `json:"accepts_cv"`
	Fields     []field `json:"fields"`
}

type email struct {
	Address string `json:"address"`
	Context string `json:"context,omitempty"`
	// Says what to send. Firms are specific and contradictory about this —
	// "please send your CV and a short covering note", "do not attach
	// documents", "PDF only, max 5MB", "no agencies" — and getting it wrong is
	// how an application is deleted unread. The window is wide because the
	// instruction usually sits in the sentence around the address, not in the
	// element that holds it.
	Instructions string `json:"instructions,omitempty"`
	PageURL      string `json:"page_url"`
	Hiring       bool   `json:"hiring"`
}

// windowAround returns the text surrounding the first occurrence of needle.
func windowAround(text, needle string, before, after int) string {
	i := strings.Index(strings.ToLower(text), strings.ToLower(needle))
	if i < 0 {
		return ""
	}
	s := i - before
	if s < 0 {
		s = 0
	}
	e := i + len(needle) + after
	if e > len(text) {
		e = len(text)
	}
	return strings.TrimSpace(text[s:e])
}

type out struct {
	Domain    string   `json:"domain"`
	Name      string   `json:"name"`
	Country   string   `json:"country"`
	Forms     []form   `json:"forms"`
	Emails    []email  `json:"emails"`
	ApplyURLs []string `json:"apply_urls"`
	Pages     int      `json:"pages_scanned"`
}

// autofillHint maps a field to what Ram would put in it, so a later submitter
// does not have to re-derive the mapping for every site's naming scheme.
func autofillHint(name, typ, label string) string {
	s := strings.ToLower(name + " " + label)
	switch {
	case typ == "file":
		return "cv_file"
	case strings.Contains(s, "first") && strings.Contains(s, "name"):
		return "first_name"
	case strings.Contains(s, "last") || strings.Contains(s, "surname") ||
		strings.Contains(s, "nachname"):
		return "last_name"
	case strings.Contains(s, "email") || strings.Contains(s, "mail"):
		return "email"
	case strings.Contains(s, "phone") || strings.Contains(s, "tel"):
		return "phone"
	case strings.Contains(s, "linkedin"):
		return "linkedin"
	case strings.Contains(s, "message") || strings.Contains(s, "cover") ||
		strings.Contains(s, "motivat") || strings.Contains(s, "nachricht"):
		return "cover_letter"
	case strings.Contains(s, "name"):
		return "full_name"
	case typ == "checkbox" && (strings.Contains(s, "privacy") ||
		strings.Contains(s, "datenschutz") || strings.Contains(s, "consent") ||
		strings.Contains(s, "gdpr")):
		return "consent_checkbox"
	}
	return ""
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func splitKey(k string) string {
	if m := depthKeyRe.FindStringSubmatch(k); m != nil {
		return m[1]
	}
	return k
}

func readFirms(path string) map[string][2]string {
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil || len(recs) < 2 {
		log.Fatalf("read %s: %v", path, err)
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
	m := map[string][2]string{}
	for _, rec := range recs[1:] {
		if d := strings.ToLower(get(rec, "domain")); d != "" {
			m[d] = [2]string{get(rec, "name"), get(rec, "hq_country")}
		}
	}
	return m
}

func labelFor(doc *goquery.Document, s *goquery.Selection) string {
	if id, ok := s.Attr("id"); ok && id != "" {
		if l := doc.Find("label[for='" + id + "']").First(); l.Length() > 0 {
			return strings.Join(strings.Fields(l.Text()), " ")
		}
	}
	if l := s.Closest("label"); l.Length() > 0 {
		return strings.Join(strings.Fields(l.Text()), " ")
	}
	if ph, ok := s.Attr("placeholder"); ok {
		return strings.TrimSpace(ph)
	}
	if al, ok := s.Attr("aria-label"); ok {
		return strings.TrimSpace(al)
	}
	return ""
}

func main() {
	storeDir := env("STORE_DIR", "html_store")
	firms := readFirms(env("FIRMS", "funds_crawl_list.csv"))
	outPath := env("OUTPUT", "apply_targets.jsonl")

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		log.Fatalf("read %s: %v", storeDir, err)
	}

	acc := map[string]*out{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html.gz") {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".html.gz")
		dom := strings.ToLower(splitKey(key))
		meta, ok := firms[dom]
		if !ok {
			continue
		}

		fh, err := os.Open(filepath.Join(storeDir, e.Name()))
		if err != nil {
			continue
		}
		zr, err := gzip.NewReader(fh)
		if err != nil {
			fh.Close()
			continue
		}
		body, _ := io.ReadAll(zr)
		pageURL := zr.Comment
		zr.Close()
		fh.Close()

		base, err := url.Parse(pageURL)
		if err != nil {
			continue
		}
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
		if err != nil {
			continue
		}

		o := acc[dom]
		if o == nil {
			o = &out{Domain: dom, Name: meta[0], Country: meta[1]}
			acc[dom] = o
		}
		o.Pages++
		// goquery's Text() walks every node including <style> and <script>, so
		// without this the window around an address comes back full of CSS
		// rules instead of the sentence telling you what to send.
		txtDoc := goquery.CloneDocument(doc)
		txtDoc.Find("script,style,noscript,svg").Remove()
		pageText := strings.Join(strings.Fields(txtDoc.Text()), " ")
		abs := func(h string) string {
			u, err := url.Parse(strings.TrimSpace(h))
			if err != nil {
				return ""
			}
			return base.ResolveReference(u).String()
		}

		doc.Find("form").Each(func(_ int, fs *goquery.Selection) {
			fr := form{PageURL: pageURL, Method: "GET"}
			if a, ok := fs.Attr("action"); ok && strings.TrimSpace(a) != "" {
				fr.Action = abs(a)
			} else {
				fr.Action = pageURL // an empty action posts back to the page
			}
			if m, ok := fs.Attr("method"); ok {
				fr.Method = strings.ToUpper(strings.TrimSpace(m))
			}
			fr.Enctype, _ = fs.Attr("enctype")
			h, _ := fs.Html()
			fr.HasCaptcha = captchaRe.MatchString(h)

			fs.Find("input,select,textarea").Each(func(_ int, is *goquery.Selection) {
				name, _ := is.Attr("name")
				if name == "" {
					name, _ = is.Attr("id")
				}
				if name == "" {
					return
				}
				typ := strings.ToLower(is.AttrOr("type", ""))
				if typ == "" {
					if is.Is("select") {
						typ = "select"
					} else if is.Is("textarea") {
						typ = "textarea"
					} else {
						typ = "text"
					}
				}
				if typ == "submit" || typ == "button" || typ == "image" {
					return
				}
				lbl := labelFor(doc, is)
				f := field{
					Name:     name,
					Type:     typ,
					Label:    lbl,
					Accept:   is.AttrOr("accept", ""),
					Plumbing: plumbingRe.MatchString(name),
					Autofill: autofillHint(name, typ, lbl),
				}
				_, f.Required = is.Attr("required")
				if is.Is("select") {
					is.Find("option").Each(func(_ int, op *goquery.Selection) {
						if v := strings.TrimSpace(op.AttrOr("value", op.Text())); v != "" {
							f.Options = append(f.Options, v)
						}
					})
				}
				if typ == "file" {
					fr.AcceptsCV = true
				}
				fr.Fields = append(fr.Fields, f)
			})
			// A form with no fields is a search box or a stub, not a route in.
			if len(fr.Fields) > 0 {
				o.Forms = append(o.Forms, fr)
			}
		})

		doc.Find("a[href^='mailto:'], a[href^='MAILTO:']").Each(func(_ int, as *goquery.Selection) {
			h, _ := as.Attr("href")
			addr := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(h, "mailto:"), "MAILTO:"))
			if i := strings.IndexAny(addr, "?"); i >= 0 {
				addr = addr[:i]
			}
			// Sites percent-encode addresses to defeat scrapers, so the href
			// reads re%63r%75t%65%6de%6e%74@i%73alt.... Passed through raw it
			// looks like a valid address and is not one; nothing sent to it
			// would arrive.
			if dec, err := url.QueryUnescape(addr); err == nil && dec != "" {
				addr = dec
			}
			addr = strings.TrimSpace(addr)
			if addr == "" || !strings.Contains(addr, "@") {
				return
			}
			local, _, _ := strings.Cut(addr, "@")
			o.Emails = append(o.Emails, email{
				Address:      addr,
				Context:      strings.Join(strings.Fields(as.Parent().Text()), " "),
				Instructions: windowAround(pageText, addr, 320, 320),
				PageURL:      pageURL,
				Hiring:       hiringRe.MatchString(local),
			})
		})

		doc.Find("a[href]").Each(func(_ int, as *goquery.Selection) {
			txt := strings.Join(strings.Fields(as.Text()), " ")
			h, _ := as.Attr("href")
			if !applyLinkRe.MatchString(txt) && !applyLinkRe.MatchString(h) {
				return
			}
			if u := abs(h); strings.HasPrefix(u, "http") {
				o.ApplyURLs = append(o.ApplyURLs, u)
			}
		})
	}

	f, err := os.Create(outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)

	var nFirms, nForms, nCV, nMail, nURL, nCaptcha int
	keys := make([]string, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		o := acc[k]
		o.Emails = dedupeEmails(o.Emails)
		o.ApplyURLs = dedupeStrings(o.ApplyURLs)
		if len(o.Forms) == 0 && len(o.Emails) == 0 && len(o.ApplyURLs) == 0 {
			continue
		}
		nFirms++
		nForms += len(o.Forms)
		nMail += len(o.Emails)
		nURL += len(o.ApplyURLs)
		for _, fr := range o.Forms {
			if fr.AcceptsCV {
				nCV++
			}
			if fr.HasCaptcha {
				nCaptcha++
			}
		}
		_ = enc.Encode(o)
	}
	log.Printf("DONE | %d firms with a usable route -> %s", nFirms, outPath)
	log.Printf("  forms %d (%d take a CV upload, %d behind a CAPTCHA)", nForms, nCV, nCaptcha)
	log.Printf("  email addresses %d | apply URLs %d", nMail, nURL)
}

func dedupeEmails(in []email) []email {
	seen := map[string]bool{}
	var out []email
	// Hiring addresses first, so the one to write to is the first in the list.
	sort.SliceStable(in, func(i, j int) bool { return in[i].Hiring && !in[j].Hiring })
	for _, e := range in {
		k := strings.ToLower(e.Address)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
