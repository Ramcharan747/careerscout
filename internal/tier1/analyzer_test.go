package tier1_test

import (
	"testing"

	"github.com/careerscout/careerscout/internal/tier1"
)

func TestAnalyzer_Greenhouse(t *testing.T) {
	a := tier1.NewAnalyzer()
	html := `
		<script>
		fetch("https://boards-api.greenhouse.io/v1/boards/acmecorp/jobs?content=true")
		  .then(r => r.json())
		</script>
	`
	m := a.Analyze(html, "acmecorp.com")
	if m == nil {
		t.Fatal("expected a match, got nil")
	}
	if m.Pattern != "greenhouse" {
		t.Fatalf("expected pattern 'greenhouse', got %q", m.Pattern)
	}
	if m.Method != "GET" {
		t.Fatalf("expected method GET, got %q", m.Method)
	}
}

func TestAnalyzer_Lever(t *testing.T) {
	a := tier1.NewAnalyzer()
	html := `var url = "https://api.lever.co/v0/postings/my-company?mode=json";`
	m := a.Analyze(html, "my-company.com")
	if m == nil {
		t.Fatal("expected lever match, got nil")
	}
	if m.Pattern != "lever" {
		t.Fatalf("expected 'lever', got %q", m.Pattern)
	}
}

func TestAnalyzer_Ashby(t *testing.T) {
	a := tier1.NewAnalyzer()
	html := `apiUrl: "https://api.ashbyhq.com/posting-api/job-board/stripe"`
	m := a.Analyze(html, "stripe.com")
	if m == nil {
		t.Fatal("expected ashby match, got nil")
	}
	if m.Pattern != "ashby" {
		t.Fatalf("expected 'ashby', got %q", m.Pattern)
	}
}

func TestAnalyzer_GenericAPI_RelativePath(t *testing.T) {
	a := tier1.NewAnalyzer()
	html := `axios.get('/api/v2/jobs?limit=20&offset=0')`
	m := a.Analyze(html, "techcorp.com")
	if m == nil {
		t.Fatal("expected generic_api match, got nil")
	}
	if m.Pattern != "generic_api" {
		t.Fatalf("expected 'generic_api', got %q", m.Pattern)
	}
	// Should have been resolved to an absolute URL
	if m.APIURL == "" {
		t.Fatal("APIURL should not be empty")
	}
}

func TestAnalyzer_GraphQL(t *testing.T) {
	a := tier1.NewAnalyzer()
	html := `fetch('/graphql', { method: 'POST', body: JSON.stringify({operationName: 'GetJobs'}) })`
	m := a.Analyze(html, "graphqlco.com")
	if m == nil {
		t.Fatal("expected graphql match, got nil")
	}
	if m.Pattern != "graphql" {
		t.Fatalf("expected 'graphql', got %q", m.Pattern)
	}
	if m.Method != "POST" {
		t.Fatalf("expected POST for graphql, got %q", m.Method)
	}
}

func TestAnalyzer_NoMatch_ReturnsNil(t *testing.T) {
	a := tier1.NewAnalyzer()
	html := `<html><head><title>Jobs at Acme</title></head><body>We're hiring!</body></html>`
	m := a.Analyze(html, "acme.com")
	if m != nil {
		t.Fatalf("expected nil for no-match, got %+v", m)
	}
}

func TestAnalyzer_AnalyzeAll_ReturnsMultiple(t *testing.T) {
	a := tier1.NewAnalyzer()
	// Contrived HTML with two patterns (unlikely in real life but tests AnalyzeAll)
	html := `
		fetch("https://boards-api.greenhouse.io/v1/boards/co/jobs")
		fetch("https://api.lever.co/v0/postings/co")
	`
	matches := a.AnalyzeAll(html, "multi.com")
	if len(matches) < 2 {
		t.Fatalf("expected at least 2 matches, got %d", len(matches))
	}
}
