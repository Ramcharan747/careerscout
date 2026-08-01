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
	for _, j := range jobs {
		if j.Method != "links" {
			t.Errorf("method = %s", j.Method)
		}
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
