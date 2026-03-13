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

func TestAnalyzer_NextData(t *testing.T) {
	a := tier1.NewAnalyzer()
	// Use a generic /api/jobs URL embedded in __NEXT_DATA__ — one that does NOT
	// match any of the named ATS patterns (greenhouse, lever, ashby etc.) so only
	// the nextjs_data regex can match it.
	html := `<script id="__NEXT_DATA__" type="application/json">
	{"props":{"pageProps":{"apiBase":"https://careers.acme-corp.io/api/jobs?limit=20"}}}
	</script>`
	m := a.Analyze(html, "acme-corp.io")
	if m == nil {
		t.Fatal("expected nextjs_data match, got nil")
	}
	if m.Pattern != "nextjs_data" {
		t.Fatalf("expected 'nextjs_data', got %q", m.Pattern)
	}
	if m.Method != "GET" {
		t.Fatalf("expected GET, got %q", m.Method)
	}
}

func TestAnalyzer_Preconnect(t *testing.T) {
	a := tier1.NewAnalyzer()
	// Simulate a page that declares a preconnect hint to a known ATS domain
	html := `<html><head>
	<link rel="preconnect" href="https://boards-api.greenhouse.io">
	</head><body>Jobs</body></html>`
	m := a.Analyze(html, "someco.com")
	if m == nil {
		t.Fatal("expected preconnect match, got nil")
	}
	if m.Pattern != "preconnect" {
		t.Fatalf("expected 'preconnect', got %q", m.Pattern)
	}
	if m.APIURL == "" {
		t.Fatal("APIURL should not be empty")
	}
}

func TestAnalyzer_NextData_Nested(t *testing.T) {
	a := tier1.NewAnalyzer()
	// Use JSON escaping (\/) to defeat the flat regex patterns.
	// The goquery + unmarshal approach will decode it and find the API!
	html := `<html><body>
	<script id="__NEXT_DATA__" type="application/json">
	{
		"props": {
			"pageProps": {
				"initialState": {
					"jobBoard": {
						"config": {
							"endpoints": [
								"https:\/\/ignore.me",
								"https:\/\/api.lever.co\/v0\/postings\/nested-corp?mode=json"
							]
						}
					}
				}
			}
		}
	}
	</script>
	</body></html>`

	m := a.Analyze(html, "nested-corp.com")
	if m == nil {
		t.Fatal("expected a match from deeply nested Next.js data, got nil")
	}
	if m.Pattern != "nextjs_data" {
		t.Fatalf("expected 'nextjs_data', got %q", m.Pattern)
	}
	if m.APIURL != "https://api.lever.co/v0/postings/nested-corp?mode=json" {
		t.Fatalf("unexpected APIURL extracted: %q", m.APIURL)
	}
}

func TestAnalyzer_CSPMeta(t *testing.T) {
	a := tier1.NewAnalyzer()
	html := `<head>
	<meta http-equiv="Content-Security-Policy" content="default-src 'self'; connect-src 'self' https://api.lever.co https://google-analytics.com;">
	</head>`

	m := a.Analyze(html, "csp-corp.com")
	if m == nil {
		t.Fatal("expected match from CSP meta tag, got nil")
	}
	if m.Pattern != "csp_meta" {
		t.Fatalf("expected 'csp_meta', got %q", m.Pattern)
	}
	if m.APIURL != "https://api.lever.co" {
		t.Fatalf("expected extracted URL to be https://api.lever.co, got %q", m.APIURL)
	}
}

func TestAnalyzer_SchemaOrgJobPosting_Detected(t *testing.T) {
	a := tier1.NewAnalyzer()
	html := `<html><head>
	<script type="application/ld+json">
	{"@type": "JobPosting", "title": "Engineer", "url": "https://example.com/jobs/123"}
	</script>
	</head><body>Jobs</body></html>`

	m := a.Analyze(html, "example.com")
	if m == nil {
		t.Fatal("expected schema_org_jobs match, got nil")
	}
	if m.Pattern != "schema_org_jobs" {
		t.Fatalf("expected 'schema_org_jobs', got %q", m.Pattern)
	}
	if m.APIURL != "https://example.com/jobs/123" {
		t.Fatalf("expected URL https://example.com/jobs/123, got %q", m.APIURL)
	}
}

func TestAnalyzer_SchemaOrgGraph_Detected(t *testing.T) {
	a := tier1.NewAnalyzer()
	html := `<html><head>
	<script type="application/ld+json">
	{"@graph": [{"@type": "Organization"}, {"@type": "JobPosting", "url": "https://example.com/jobs/456"}]}
	</script>
	</head><body>Jobs</body></html>`

	m := a.Analyze(html, "example.com")
	if m == nil {
		t.Fatal("expected schema_org_jobs match for @graph, got nil")
	}
	if m.Pattern != "schema_org_jobs" {
		t.Fatalf("expected 'schema_org_jobs', got %q", m.Pattern)
	}
	if m.APIURL != "https://example.com/jobs/456" {
		t.Fatalf("expected URL https://example.com/jobs/456, got %q", m.APIURL)
	}
}
