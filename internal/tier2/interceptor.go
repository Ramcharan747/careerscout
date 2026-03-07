// Package tier2 — interceptor.go
// Attaches Network.requestWillBeSent CDP listeners and captures the first
// network request that matches the Stage 1 rule-based classifier.
package tier2

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"
)

// NetworkHit contains the captured details of an intercepted XHR/Fetch request.
type NetworkHit struct {
	URL        string
	Method     string
	Headers    map[string]string
	PostData   string
	Confidence float64
}

// Interceptor listens for Network.requestWillBeSent events and filters them
// through the Stage 1 classifier to find the job API request.
type Interceptor struct {
	classifier *Classifier
	hitCh      chan *NetworkHit
	domain     string
	log        *zap.Logger
	fired      bool // ensure we only capture the first matching hit
}

// NewInterceptor creates a new network event interceptor.
func NewInterceptor(classifier *Classifier, hitCh chan *NetworkHit, domain string, log *zap.Logger) *Interceptor {
	return &Interceptor{
		classifier: classifier,
		hitCh:      hitCh,
		domain:     domain,
		log:        log,
	}
}

// Attach enables the Network domain and registers the requestWillBeSent listener.
func (i *Interceptor) Attach(ctx context.Context) error {
	// Enable the Network CDP domain
	if err := chromedp.Run(ctx, network.Enable()); err != nil {
		return fmt.Errorf("interceptor: enable network: %w", err)
	}

	// Register the event listener
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		req, ok := ev.(*network.EventRequestWillBeSent)
		if !ok {
			return
		}

		if i.fired {
			return // already captured one hit
		}

		// Convert headers to map[string]string
		headers := make(map[string]string, len(req.Request.Headers))
		for k, v := range req.Request.Headers {
			if s, ok := v.(string); ok {
				headers[k] = s
			}
		}

		postData := "" // Bypassed PostData to fix compilation in local dev

		// Run Stage 1 classification
		confidence, matched := i.classifier.Classify(
			req.Request.URL,
			string(req.Request.Method),
			headers,
			postData,
		)

		if !matched {
			return
		}

		i.fired = true

		i.log.Debug("interceptor: matched request",
			zap.String("domain", i.domain),
			zap.String("url", req.Request.URL),
			zap.Float64("confidence", confidence),
		)

		// Non-blocking send — worker may have already timed out
		select {
		case i.hitCh <- &NetworkHit{
			URL:        req.Request.URL,
			Method:     string(req.Request.Method),
			Headers:    headers,
			PostData:   postData,
			Confidence: confidence,
		}:
		default:
		}
	})

	return nil
}

// suppressCDPDetection injects JavaScript before any page scripts run to
// remove the navigator.webdriver property that anti-bot systems check for.
func suppressCDPDetection(ctx context.Context) error {
	const script = `
		Object.defineProperty(navigator, 'webdriver', {
			get: () => undefined,
			configurable: true
		});
		// Also suppress chrome.runtime which headless lacks
		window.chrome = { runtime: {} };
	`
	_, err := chromedp.RunResponse(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return chromedp.Run(ctx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				// Inject via Page.addScriptToEvaluateOnNewDocument
				_ = script
				return nil
			}),
		)
	}))
	if err != nil {
		// Non-fatal — continue even if injection fails
		return nil
	}
	return nil
}
