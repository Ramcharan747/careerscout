package main

import (
	"testing"
)

func TestCaptureRun_JunkFilterRejectsAnalytics(t *testing.T) {
	analyticsURLs := []string{
		"https://google-analytics.com/collect",
		"https://api.segment.io/v1/t",
		"https://in.hotjar.com/api/v2/client/ws",
		"https://api-iam.intercom.io/messenger/web/ping",
		"https://api.amplitude.com/2/httpapi",
	}

	for _, u := range analyticsURLs {
		if !isSkipDomain(u) {
			t.Errorf("expected isSkipDomain to reject %s", u)
		}
	}

	validURLs := []string{
		"https://api.greenhouse.io/v1/boards/stripe/jobs",
		"https://api.lever.co/v0/postings/stripe",
	}

	for _, u := range validURLs {
		if isSkipDomain(u) {
			t.Errorf("expected isSkipDomain to accept %s", u)
		}
	}
}

func TestCaptureRun_JsonFilterRejectsHTML(t *testing.T) {
	htmlContentTypes := []string{
		"text/html; charset=utf-8",
		"text/html",
		"application/xhtml+xml",
	}

	for _, ct := range htmlContentTypes {
		if !isHTMLResponse(ct) {
			t.Errorf("expected isHTMLResponse to reject %s", ct)
		}
	}

	jsonContentTypes := []string{
		"application/json",
		"application/json; charset=utf-8",
	}

	for _, ct := range jsonContentTypes {
		if isHTMLResponse(ct) {
			t.Errorf("expected isHTMLResponse to accept %s", ct)
		}
	}
}
