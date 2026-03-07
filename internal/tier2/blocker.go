// Package tier2 — blocker.go
// Configures Chrome's Network domain to block non-essential resources
// (images, fonts, CSS, media, analytics) to minimise memory usage and
// ensure the page's XHR/Fetch request fires as early as possible.
package tier2

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// blockedURLPatterns defines the resource patterns to block.
// Analytics, ads, and media are blocked; only the page JS executes.
var blockedURLPatterns = []string{
	// Analytics & tracking
	"*google-analytics.com*",
	"*googletagmanager.com*",
	"*doubleclick.net*",
	"*analytics.twitter.com*",
	"*facebook.net*",
	"*segment.io*",
	"*segment.com*",
	"*mixpanel.com*",
	"*hotjar.com*",
	"*intercom.io*",
	"*fullstory.com*",
	"*logrocket.io*",
	"*clarity.ms*",

	// Media files
	"*.jpg", "*.jpeg", "*.png", "*.gif", "*.webp", "*.svg",
	"*.mp4", "*.webm", "*.ogg", "*.mp3", "*.wav",
	"*.woff", "*.woff2", "*.ttf", "*.otf", "*.eot",

	// CSS (page doesn't need to render)
	"*.css",

	// Feature flags & A/B testing
	"*launchdarkly.com*",
	"*optimizely.com*",
	"*split.io*",

	// Ad networks
	"*ads.yahoo.com*",
	"*criteo.com*",
	"*adroll.com*",
	"*amazon-adsystem.com*",
}

// Blocker configures Chrome's Network resource blocking.
type Blocker struct{}

// NewBlocker creates a new resource Blocker.
func NewBlocker() *Blocker {
	return &Blocker{}
}

// Enable activates URL blocking for the given Chromium context.
func (b *Blocker) Enable(ctx context.Context) error {
	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetBlockedURLs(blockedURLPatterns).Do(ctx)
		}),
	); err != nil {
		return fmt.Errorf("blocker: set blocked URLs: %w", err)
	}
	return nil
}
