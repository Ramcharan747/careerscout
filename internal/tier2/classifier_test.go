package tier2_test

import (
	"testing"

	"github.com/careerscout/careerscout/internal/tier2"
)

var c = tier2.NewClassifier()

func TestClassifier_GreenhouseURL_Matches(t *testing.T) {
	conf, matched := c.Classify(
		"https://boards-api.greenhouse.io/v1/boards/stripe/jobs",
		"GET",
		map[string]string{"Content-Type": "application/json"},
		"",
	)
	if !matched {
		t.Fatal("Greenhouse URL should match")
	}
	if conf <= 0 {
		t.Fatal("confidence should be > 0")
	}
}

func TestClassifier_LeverURL_Matches(t *testing.T) {
	conf, matched := c.Classify(
		"https://api.lever.co/v0/postings/acmecorp?mode=json",
		"GET",
		map[string]string{},
		"",
	)
	if !matched {
		t.Fatal("Lever URL should match")
	}
	_ = conf
}

func TestClassifier_GraphQLJobsQuery_Matches(t *testing.T) {
	_, matched := c.Classify(
		"https://example.com/graphql",
		"POST",
		map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer eyJhbGciOiJSUzI1NiJ9.test",
		},
		`{"operationName":"GetJobs","variables":{"limit":20,"offset":0}}`,
	)
	if !matched {
		t.Fatal("GraphQL job query should match")
	}
}

func TestClassifier_GenericJobsAPI_Matches(t *testing.T) {
	_, matched := c.Classify(
		"https://api.techcorp.com/careers/v2?page=1&size=20",
		"GET",
		map[string]string{"x-api-key": "secret"},
		"",
	)
	if !matched {
		t.Fatal("Generic jobs API with pagination and api key should match")
	}
}

func TestClassifier_AnalyticsRequest_NoMatch(t *testing.T) {
	_, matched := c.Classify(
		"https://www.google-analytics.com/collect",
		"POST",
		map[string]string{},
		"v=1&t=event&cid=12345",
	)
	if matched {
		t.Fatal("Analytics request should NOT match")
	}
}

func TestClassifier_CDNImageRequest_NoMatch(t *testing.T) {
	_, matched := c.Classify(
		"https://cdn.techcorp.com/images/hero-banner.jpg",
		"GET",
		map[string]string{},
		"",
	)
	if matched {
		t.Fatal("CDN image request should NOT match")
	}
}

func TestClassifier_Confidence_Ceiling(t *testing.T) {
	// Very strong signal — should cap at 1.0
	conf, matched := c.Classify(
		"https://api.greenhouse.io/v1/boards/test/jobs?page=1&size=20",
		"POST",
		map[string]string{
			"Authorization": "Bearer token",
			"Content-Type":  "application/json",
			"x-api-key":     "key",
		},
		`{"operationName":"GetJobs","limit":20}`,
	)
	if !matched {
		t.Fatal("should match with all signals present")
	}
	if conf > 1.0 {
		t.Fatalf("confidence %f exceeds 1.0", conf)
	}
}
