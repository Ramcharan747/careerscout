package tier2_v3_test

import (
	"testing"

	tier2_v3 "github.com/careerscout/careerscout/internal/tier2_v3"
)

// TestWorker_ResourceBlocking_ImageNotFetched verifies that the shouldBlockURL
// function — used inside the HijackRequests handler in worker.go — correctly
// identifies image requests as blockable. In production the handler calls
// h.Response.Fail(proto.NetworkErrorReasonAborted) for such URLs; this test
// confirms the decision function returns true without needing a live browser.
func TestWorker_ResourceBlocking_ImageNotFetched(t *testing.T) {
	imageURLs := []string{
		"https://cdn.example.com/hero.jpg",
		"https://example.com/images/logo.png",
		"https://static.company.io/assets/banner.webp",
		"https://media.lever.co/images/photo.gif",
		"https://assets.greenhouse.io/uploads/header.jpeg",
		"https://fonts.example.com/Inter.woff2",
		"https://cdn.company.com/styles/main.css",
		"https://video.example.com/intro.mp4",
	}

	for _, u := range imageURLs {
		if !tier2_v3.ShouldBlockURL(u) {
			t.Errorf("expected ShouldBlockURL(%q) = true (should be blocked), got false", u)
		}
	}

	// XHR to a real job API should NOT be blocked.
	apiURLs := []string{
		"https://boards-api.greenhouse.io/v1/boards/acme/jobs",
		"https://api.lever.co/v0/postings/nextcorp?mode=json",
		"https://api.ashby.io/job-postings?organization=techcorp",
	}

	for _, u := range apiURLs {
		if tier2_v3.ShouldBlockURL(u) {
			t.Errorf("expected ShouldBlockURL(%q) = false (should not be blocked), got true", u)
		}
	}
}

// TestWorker_AnalyticsBlocking verifies that analytics/tracking domains are blocked.
func TestWorker_AnalyticsBlocking(t *testing.T) {
	analyticsURLs := []string{
		"https://www.google-analytics.com/collect?v=1",
		"https://cdn.segment.com/analytics.js",
		"https://mixpanel.com/track",
		"https://hotjar.com/api/metrics",
		"https://intercom.io/api/users",
	}

	for _, u := range analyticsURLs {
		if !tier2_v3.ShouldBlockURL(u) {
			t.Errorf("expected ShouldBlockURL(%q) = true (analytics blocked), got false", u)
		}
	}
}

func TestBlocker_TelemetryPathBlocked(t *testing.T) {
	noiseURLs := []string{
		"https://example.com/api/v1/analytics/pageview",
		"https://corp.com/telemetry/event",
		"https://site.com/tracking/pixel.gif",
		"https://app.com/beacon/collect",
		"https://site.com/healthcheck",
		"https://app.com/auth/token/refresh",
		"https://site.com/feature-flag/check",
		"https://app.com/killswitch/status",
		"https://site.com/api/initialize?app=web",
	}
	for _, u := range noiseURLs {
		if !tier2_v3.ShouldBlockURL(u) {
			t.Errorf("expected ShouldBlockURL(%q) = true (telemetry path blocked), got false", u)
		}
	}

	// ATS providers should NOT be blocked even with /config in path
	atsURLs := []string{
		"https://boards-api.greenhouse.io/v1/config",
		"https://api.lever.co/v0/initialize",
	}
	for _, u := range atsURLs {
		if tier2_v3.ShouldBlockURL(u) {
			t.Errorf("expected ShouldBlockURL(%q) = false (ATS provider exempt), got true", u)
		}
	}
}

func TestBlocker_HTMLResponseBlocked(t *testing.T) {
	if !tier2_v3.ShouldBlockContentType("text/html; charset=utf-8") {
		t.Error("expected text/html to be blocked")
	}
	if !tier2_v3.ShouldBlockContentType("TEXT/HTML") {
		t.Error("expected TEXT/HTML to be blocked (case insensitive)")
	}
	if tier2_v3.ShouldBlockContentType("application/json") {
		t.Error("expected application/json to NOT be blocked")
	}
	if tier2_v3.ShouldBlockContentType("application/json; charset=utf-8") {
		t.Error("expected application/json with charset to NOT be blocked")
	}
}
