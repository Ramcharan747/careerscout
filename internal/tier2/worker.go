// Package tier2 implements the Chromium CDP interception worker (Tier 2).
// This is the primary discovery workhorse, handling ~50% of all URLs.
// Each worker manages a pool of concurrent Chromium instances via chromedp.
package tier2

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"go.uber.org/zap"
)

const (
	// Hard kill timeout for each Chromium context — must not exceed.
	browserTimeout = 800 * time.Millisecond

	// Memory ceiling per Chromium instance (enforced at container level,
	// but tracked here for observability).
	memCeilingMB = 512

	// Number of Chromium instances per worker goroutine.
	poolSizePerWorker = 20
)

// WorkerPool manages a bounded pool of Chromium browser contexts.
type WorkerPool struct {
	sem        chan struct{} // capacity = poolSizePerWorker * numWorkers
	classifier *Classifier
	log        *zap.Logger
	mu         sync.Mutex
}

// NewWorkerPool creates a CDP worker pool with total capacity = workers * poolSizePerWorker.
func NewWorkerPool(workers int, log *zap.Logger) *WorkerPool {
	capacity := workers * poolSizePerWorker
	return &WorkerPool{
		sem:        make(chan struct{}, capacity),
		classifier: NewClassifier(),
		log:        log,
	}
}

// CDPResult is the output of a single Tier 2 interception attempt.
type CDPResult struct {
	Domain     string
	RawURL     string
	CompanyID  string
	Success    bool
	APIURL     string
	HTTPMethod string
	Headers    map[string]string
	Body       string
	Confidence float64
	Error      string
}

// Process runs a single URL through Chromium CDP interception.
// It acquires a slot from the semaphore, spawns a Chromium context,
// captures the first matching XHR/Fetch request, then kills Chromium immediately.
func (wp *WorkerPool) Process(ctx context.Context, rawURL, domain, companyID string) CDPResult {
	base := CDPResult{
		Domain:    domain,
		RawURL:    rawURL,
		CompanyID: companyID,
	}

	// Acquire pool slot
	wp.sem <- struct{}{}
	defer func() { <-wp.sem }()

	// Create a fresh Chromium context with aggressive flags to minimise memory.
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx,
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Headless,
		chromedp.DisableGPU,
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-translate", true),
		chromedp.Flag("safebrowsing-disable-auto-update", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("js-flags", "--max_old_space_size=256"),
		// Cap per-instance memory at 512 MB via process flag (container enforces it)
		chromedp.Flag("memory-pressure-off", false),
	)
	defer allocCancel()

	// Hard kill: context dies after 800ms regardless
	timeoutCtx, timeoutCancel := context.WithTimeout(allocCtx, browserTimeout)
	defer timeoutCancel()

	cdpCtx, cdpCancel := chromedp.NewContext(timeoutCtx)
	defer cdpCancel()

	// Channel to receive the first matched network request
	hitCh := make(chan *NetworkHit, 1)

	// Run blocker + interceptor + navigation in parallel
	result, err := wp.runInterception(cdpCtx, rawURL, domain, hitCh)
	if err != nil {
		if ctx.Err() == nil { // not a context cancellation from caller
			wp.log.Debug("tier2: interception error",
				zap.String("domain", domain),
				zap.String("error", err.Error()),
			)
		}
		base.Error = err.Error()
		return base
	}

	base.Success = true
	base.APIURL = result.URL
	base.HTTPMethod = result.Method
	base.Headers = result.Headers
	base.Body = result.PostData
	base.Confidence = result.Confidence

	wp.log.Info("tier2: API intercepted",
		zap.String("domain", domain),
		zap.String("api_url", result.URL),
		zap.Float64("confidence", result.Confidence),
	)

	return base
}

// runInterception sets up resource blocking, attaches the network listener,
// injects anti-CDP-detection JS, and navigates to the target URL.
func (wp *WorkerPool) runInterception(ctx context.Context, rawURL, domain string, hitCh chan *NetworkHit) (*NetworkHit, error) {
	// Step 1: Prevent CDP detection (delete navigator.webdriver)
	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			return suppressCDPDetection(ctx)
		}),
	); err != nil {
		return nil, fmt.Errorf("suppress CDP detection: %w", err)
	}

	// Step 2: Set up Network domain listeners (blocking + interception)
	interceptor := NewInterceptor(wp.classifier, hitCh, domain, wp.log)
	if err := interceptor.Attach(ctx); err != nil {
		return nil, fmt.Errorf("attach interceptor: %w", err)
	}

	// Step 3: Enable resource blocking
	blocker := NewBlocker()
	if err := blocker.Enable(ctx); err != nil {
		return nil, fmt.Errorf("enable blocker: %w", err)
	}

	// Step 4: Navigate — we do NOT wait for the page to finish loading.
	// The interceptor fires the moment the XHR request is sent.
	go func() {
		// Navigate in background — context cancel kills it when we get a hit
		chromedp.Run(ctx, chromedp.Navigate(rawURL)) //nolint:errcheck
	}()

	// Step 5: Wait for first hit OR timeout
	select {
	case hit := <-hitCh:
		return hit, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("timeout or cancel waiting for XHR hit on %s", domain)
	}
}
