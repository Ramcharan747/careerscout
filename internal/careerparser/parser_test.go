package careerparser

import "testing"

func TestJSONLDWins(t *testing.T) {
	html := `<html><head>
	<script type="application/ld+json">
	{"@context":"https://schema.org","@graph":[
	  {"@type":"JobPosting","title":"Origination Analyst","datePosted":"2026-06-01T00:00:00Z",
	   "url":"/jobs/origination-analyst",
	   "jobLocation":{"address":{"addressLocality":"Amsterdam","addressCountry":"NL"}}},
	  {"@type":"Organization","name":"Not A Job"}
	]}
	</script></head><body>
	  <a href="/jobs/one">Decoy One</a><a href="/jobs/two">Decoy Two</a><a href="/jobs/three">Decoy Three</a>
	</body></html>`

	jobs := Extract(html, "https://example.com/careers")
	if len(jobs) != 1 {
		t.Fatalf("want 1 job from JSON-LD, got %d", len(jobs))
	}
	j := jobs[0]
	if j.Title != "Origination Analyst" {
		t.Errorf("title = %q", j.Title)
	}
	if j.Method != "jsonld" || j.Confidence != 0.95 {
		t.Errorf("method/confidence = %s/%v", j.Method, j.Confidence)
	}
	if j.PostedAt != "2026-06-01" {
		t.Errorf("posted = %q, want date-only", j.PostedAt)
	}
	if j.Location != "Amsterdam, NL" {
		t.Errorf("location = %q", j.Location)
	}
	if j.URL != "https://example.com/jobs/origination-analyst" {
		t.Errorf("url not resolved against base: %q", j.URL)
	}
}

func TestJobLinksNeedRepetition(t *testing.T) {
	// Two links is navigation, not a listing.
	few := `<html><body><a href="/jobs/a">Analyst</a><a href="/jobs/b">Associate</a></body></html>`
	if got := Extract(few, "https://x.com/careers"); len(got) != 0 {
		t.Errorf("2 links should not qualify, got %d", len(got))
	}

	many := `<html><body>
	  <a href="/vacatures/deal-analyst">Deal Analyst</a>
	  <a href="/vacatures/m-a-associate">M&amp;A Associate</a>
	  <a href="/vacatures/sourcing-lead">Sourcing Lead</a>
	  <a href="/about">About</a><a href="/vacatures">View all</a>
	</body></html>`
	jobs := Extract(many, "https://x.com/careers")
	if len(jobs) != 3 {
		t.Fatalf("want 3 jobs, got %d: %+v", len(jobs), jobs)
	}
	// Either structural or links may claim these; structural takes precedence
	// when the anchors are siblings sharing a shape.
	for _, j := range jobs {
		if j.Method != "links" && j.Method != "structural" {
			t.Errorf("unexpected method %s", j.Method)
		}
	}
}

// The point of structural extraction: job URLs carrying no job vocabulary at
// all. The old URL-shape strategy scored zero on pages like this, which cost
// ~51% of extractable pages in a 4,571-page measurement.
func TestStructuralIgnoresURLVocabulary(t *testing.T) {
	// The <h2> is required: fromStructure gates on the page announcing openings
	// somewhere, because a repeating list of jobs is structurally identical to a
	// repeating list of property adverts.
	html := `<html><body><main><h2>Open positions</h2><ul class="listing">
	  <li class="row"><a href="/p/8812">Deal Origination Analyst</a><span class="location">Amsterdam</span></li>
	  <li class="row"><a href="/p/8813">Corporate Development Associate</a><span class="location">London</span></li>
	  <li class="row"><a href="/p/8814">Investment Analyst</a><span class="location">Remote</span></li>
	  <li class="row"><a href="/p/8815">Portfolio Operations Manager</a><span class="location">Berlin</span></li>
	</ul></main></body></html>`

	jobs := Extract(html, "https://fund.example/careers")
	if len(jobs) != 4 {
		t.Fatalf("want 4 jobs, got %d: %+v", len(jobs), jobs)
	}
	if jobs[0].Method != "structural" {
		t.Errorf("method = %s, want structural", jobs[0].Method)
	}
	if jobs[0].URL != "https://fund.example/p/8812" {
		t.Errorf("url = %q, not resolved", jobs[0].URL)
	}
	if jobs[0].Location != "Amsterdam" {
		t.Errorf("location = %q", jobs[0].Location)
	}
}

// Repetition alone must not qualify: a nav bar repeats too.
func TestStructuralRejectsNavigation(t *testing.T) {
	html := `<html><body><nav><ul>
	  <li><a href="/about">About</a></li>
	  <li><a href="/products">Products</a></li>
	  <li><a href="/contact">Contact</a></li>
	  <li><a href="/blog">Blog</a></li>
	</ul></nav></body></html>`

	if jobs := Extract(html, "https://x.com/careers"); len(jobs) != 0 {
		t.Errorf("navigation should yield nothing, got %d: %+v", len(jobs), jobs)
	}
}

func TestNoiseRejected(t *testing.T) {
	for _, s := range []string{"View all jobs", "Apply now", "Privacy policy", "LinkedIn", "12", "ab"} {
		if plausibleTitle(s) {
			t.Errorf("%q should be rejected", s)
		}
	}
	for _, s := range []string{"Business Development Representative", "Praktikum Private Equity (m/w/d)"} {
		if !plausibleTitle(s) {
			t.Errorf("%q should be accepted", s)
		}
	}
}

func TestDedupe(t *testing.T) {
	in := []Job{
		{Title: "Analyst", URL: "/a"},
		{Title: "analyst", URL: "/a"},
		{Title: "Analyst", URL: "/b"},
	}
	if got := dedupe(in); len(got) != 2 {
		t.Errorf("want 2 after dedupe, got %d", len(got))
	}
}
