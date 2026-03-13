// Package tier2_v3 — blocker.go
// shouldBlockURL decides whether a URL should be aborted based on domain/extension/path.
// Used from worker.go's HijackRequests default handler.
// Resource-type blocking (image, font, media, stylesheet) is handled directly
// in worker.go via typed proto.NetworkResourceType handler registration.
package tier2_v3

import (
	"strings"
)

// blockedDomains are analytics/tracking domains whose requests we drop.
var blockedDomains = []string{
	"google-analytics.com",
	"googletagmanager.com",
	"doubleclick.net",
	"analytics.twitter.com",
	"facebook.net",
	"segment.io",
	"segment.com",
	"mixpanel.com",
	"hotjar.com",
	"intercom.io",
	"fullstory.com",
	"logrocket.io",
	"clarity.ms",
	"launchdarkly.com",
	"optimizely.com",
	"split.io",
	"ads.yahoo.com",
	"criteo.com",
	"adroll.com",
	"amazon-adsystem.com",
	"cookielaw.org",
	"onetrust.com",
	"axept.io",
	"cookiebot.com",
	"quantcast.com",
	"sentry.io",
	"bugsnag.com",
	"newrelic.com",
	"datadoghq.com",
	"nr-data.net",
}

// blockedExtensions blocks media and style resources by URL suffix.
var blockedExtensions = []string{
	".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg",
	".mp4", ".webm", ".ogg", ".mp3", ".wav",
	".woff", ".woff2", ".ttf", ".otf", ".eot",
	".css",
}

// blockedPathSegments are URL path segments that indicate noise traffic.
// Only blocked when the URL is NOT on a known ATS provider domain.
var blockedPathSegments = []string{
	"/analytics",
	"/telemetry",
	"/tracking",
	"/beacon",
	"/pixel",
	"/log",
	"/metrics",
	"/healthcheck",
	"/ping",
	"/auth/token",
	"/oauth",
	"/feature-flag",
	"/killswitch",
	"/initialize",
	"/config",
}

// blockedQueryParams are session-tracking query parameters that are not API calls.
var blockedQueryParams = []string{
	"?k=",
	"?sid=",
	"?st=",
	"&k=",
	"&sid=",
	"&st=",
}

// blockedResourceTypes are the proto.NetworkResourceType string values to block.
// These are registered as typed handlers in worker.go NewWorkerPool.
var blockedResourceTypes = []string{
	"Image",
	"Font",
	"Media",
	"Stylesheet",
}

// atsProviderDomains is used to exempt known ATS providers from path-based blocking.
// These domains should never be blocked even if they match path patterns like /config.
var atsProviderDomains = []string{
	"greenhouse.io", "lever.co", "ashbyhq.com", "ashby.io",
	"workable.com", "smartrecruiters.com", "bamboohr.com",
	"jobvite.com", "icims.com", "teamtailor.com",
	"personio.com", "personio.de", "pinpointhq.com",
	"freshteam.com", "keka.com", "darwinbox.com",
}

// ShouldBlockURL returns true if the URL should be aborted.
// Called from the default hijack handler for URLs not matched by resource-type handlers.
func ShouldBlockURL(rawURL string) bool {
	urlL := strings.ToLower(rawURL)

	for _, ext := range blockedExtensions {
		if strings.HasSuffix(urlL, ext) || strings.Contains(urlL, ext+"?") {
			return true
		}
	}

	for _, domain := range blockedDomains {
		if strings.Contains(urlL, domain) {
			return true
		}
	}

	// Session-tracking query params are always noise.
	for _, q := range blockedQueryParams {
		if strings.Contains(urlL, q) {
			return true
		}
	}

	// Path-based blocking — only for non-ATS domains.
	isATS := false
	for _, ats := range atsProviderDomains {
		if strings.Contains(urlL, ats) {
			isATS = true
			break
		}
	}
	if !isATS {
		for _, seg := range blockedPathSegments {
			if strings.Contains(urlL, seg) {
				return true
			}
		}
	}

	return false
}

// ShouldBlockContentType returns true if the response Content-Type is not JSON.
// Job APIs always return application/json — HTML responses are page nav noise.
func ShouldBlockContentType(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "text/html")
}
