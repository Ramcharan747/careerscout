// Package tier2_v3 — worker.go
// Manages a singleton Rod browser process and a tab pool.
// go-rod replaces chromedp entirely. All classifier logic, 4s window,
// and early-exit are preserved from the original implementation.
package tier2_v3

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/careerscout/careerscout/internal/capture"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const (
	// hitWaitTimeout is the maximum time we wait for a job API XHR to fire.
	// APEX upgrade: 4s window — real XHRs fire within 1–3s of navigation.
	hitWaitTimeout = 4 * time.Second

	// earlyExitConfidence triggers immediate tab release and result return.
	earlyExitConfidence = 0.85
)

// WorkerPool manages a singleton Rod browser and a bounded pool of reusable tabs.
type WorkerPool struct {
	classifier *Classifier
	browser    *rod.Browser
	tabPool    chan *rod.Page
	log        *zap.Logger
	capture    *capture.NetworkCapture
	db         *pgxpool.Pool
	runID      string
}

// CDPResult is the output from a single browser-based URL analysis.
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

// NewWorkerPool starts a singleton headless Chrome via Rod and pre-warms the tab pool.
func NewWorkerPool(ctx context.Context, _ int, log *zap.Logger, db *pgxpool.Pool, runID string) *WorkerPool {
	defaultTabs := 6
	if runtime.GOOS == "linux" {
		defaultTabs = 20
	}
	tabCount := envIntPositive("BROWSER_TABS", defaultTabs)

	chromeBin := os.Getenv("CHROME_BIN")
	if chromeBin == "" && runtime.GOOS == "linux" {
		chromeBin = "/usr/bin/google-chrome"
	}

	if chromeBin != "" {
		log.Info("initializing rod browser", zap.String("os", runtime.GOOS), zap.String("chrome_bin", chromeBin))
	} else {
		log.Info("initializing rod browser", zap.String("os", runtime.GOOS), zap.String("chrome_bin", "rod-default"))
	}

	l := launcher.New().
		Headless(true).
		NoSandbox(true).
		Set("disable-gpu", "").
		Set("disable-dev-shm-usage", "").
		Set("disable-extensions", "").
		Set("disable-background-networking", "").
		Set("disable-default-apps", "").
		Set("disable-sync", "").
		Set("blink-settings", "imagesEnabled=false")

	if chromeBin != "" {
		l = l.Bin(chromeBin)
	}

	u := l.MustLaunch()

	browser := rod.New().ControlURL(u).MustConnect()

	// Pre-warm the tab pool.
	pool := make(chan *rod.Page, tabCount)
	for i := 0; i < tabCount; i++ {
		page := browser.MustPage("")
		pool <- page
	}

	log.Info("go-rod browser ready", zap.Int("tab_pool", tabCount))

	nc, err := capture.New()
	if err != nil {
		log.Fatal("failed to initialize network capture", zap.Error(err))
	}

	return &WorkerPool{
		classifier: NewClassifier(),
		browser:    browser,
		tabPool:    pool,
		log:        log,
		capture:    nc,
		db:         db,
		runID:      runID,
	}
}

func (wp *WorkerPool) recordNearMiss(domain, url, contentType, body string, size int, urlScore, bodyScore, confidence float64) {
	if wp.db == nil {
		return
	}
	_, err := wp.db.Exec(context.Background(), `
		INSERT INTO near_misses (run_id, domain, url, response_content_type, response_body, response_size, url_score, body_score, final_confidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING`,
		wp.runID, domain, url, contentType, body, size, urlScore, bodyScore, confidence,
	)
	if err != nil {
		wp.log.Warn("failed to record near-miss", zap.String("url", url), zap.Error(err))
	}
}

// Close stops all tabs and the browser process.
func (wp *WorkerPool) Close() {
	if wp.capture != nil {
		_ = wp.capture.Close()
	}
	close(wp.tabPool)
	for page := range wp.tabPool {
		_ = page.Close()
	}
	_ = wp.browser.Close()
}

// Process navigates rawURL in a borrowed tab, intercepts XHR/Fetch calls via
// rod HijackRequests, and returns the best job API hit within hitWaitTimeout.
func (wp *WorkerPool) Process(ctx context.Context, rawURL, domain, companyID string) CDPResult {
	base := CDPResult{Domain: domain, RawURL: rawURL, CompanyID: companyID}

	wp.log.Debug("waiting for browser tab", zap.String("domain", domain))
	waitStart := time.Now()

	// Borrow a tab from the pool — blocks if all tabs are busy (backpressure).
	// Separate 60s timeout just for tab acquisition.
	acquireCtx, acquireCancel := context.WithTimeout(ctx, 60*time.Second)
	defer acquireCancel()

	var page *rod.Page
	select {
	case <-acquireCtx.Done():
		base.Error = "timeout waiting for browser tab (>60s)"
		return base
	case page = <-wp.tabPool:
		wp.log.Debug("acquired browser tab",
			zap.String("domain", domain),
			zap.Duration("wait_time", time.Since(waitStart).Round(time.Millisecond)))
	}
	defer func() { wp.tabPool <- page }()

	// Scope all page ops to a dedicated 20-second processing context.
	processCtx, processCancel := context.WithTimeout(ctx, 20*time.Second)
	defer processCancel()
	page = page.Context(processCtx)

	// Reset to blank before reuse.
	_ = page.Navigate("about:blank")

	// Suppress webdriver detection via JS eval before navigation.
	_, _ = page.Eval(suppressJS)

	hitCh := make(chan *NetworkHit, 20)
	earlyExitCh := make(chan struct{})
	var earlyExitOnce bool
	var hitFound int32 // atomic: set to 1 after first hit to stop intercepting

	// The rod hijack router intercepts every network request.
	// We handle blocking and XHR/Fetch scoring inside one handler.
	router := page.HijackRequests()

	// Single catch-all hijack handler. Resource type and URL blocking are
	// done inside the handler to work with rod's 2-arg MustAdd API.
	err := router.Add("*", "", func(h *rod.Hijack) {
		// If a hit was already found, skip all further interception.
		if atomic.LoadInt32(&hitFound) == 1 {
			h.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}

		reqType := string(h.Request.Type())
		reqURL := h.Request.URL().String()

		// Block non-API resource types at the network layer.
		for _, t := range blockedResourceTypes {
			if reqType == t {
				_ = h.Response.Fail(proto.NetworkErrorReasonAborted)
				return
			}
		}

		// Block analytics/tracking URLs.
		if ShouldBlockURL(reqURL) {
			_ = h.Response.Fail(proto.NetworkErrorReasonAborted)
			return
		}

		// Only score XHR and Fetch — continue all other types without scoring.
		if reqType != "XHR" && reqType != "Fetch" {
			h.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}

		// Score the XHR/Fetch request.
		wp.handleHijack(h, domain, hitCh, earlyExitCh, &earlyExitOnce, &hitFound)
	})
	if err != nil {
		base.Error = fmt.Sprintf("hijack setup failed: %v", err)
		return base
	}

	go router.Run()
	defer router.Stop()

	// Navigation — non-blocking.
	navErrCh := make(chan error, 1)
	go func() {
		navErrCh <- page.Navigate(rawURL)
	}()

	hitTimer := time.NewTimer(hitWaitTimeout)
	defer hitTimer.Stop()

	select {
	case <-earlyExitCh:
		_ = page.StopLoading()
		if hit := pickBest(hitCh); hit != nil {
			wp.log.Info("discovered (early-exit)",
				zap.String("domain", domain),
				zap.String("url", hit.URL),
				zap.Float64("conf", hit.Confidence),
			)
			base.Success = true
			base.APIURL = hit.URL
			base.HTTPMethod = hit.Method
			base.Headers = hit.Headers
			base.Body = hit.PostData
			base.Confidence = hit.Confidence
			return base
		}
		base.Error = "early-exit triggered but no candidate in buffer"
		return base

	case <-hitTimer.C:
		if hit := pickBest(hitCh); hit != nil {
			wp.log.Info("discovered (drained)",
				zap.String("domain", domain),
				zap.String("url", hit.URL),
				zap.Float64("conf", hit.Confidence),
			)
			base.Success = true
			base.APIURL = hit.URL
			base.HTTPMethod = hit.Method
			base.Headers = hit.Headers
			base.Body = hit.PostData
			base.Confidence = hit.Confidence
			return base
		}
		base.Error = "no job API detected"
		return base

	case navErr := <-navErrCh:
		if navErr != nil {
			if hit := pickBest(hitCh); hit != nil {
				base.Success = true
				base.APIURL = hit.URL
				base.HTTPMethod = hit.Method
				base.Headers = hit.Headers
				base.Body = hit.PostData
				base.Confidence = hit.Confidence
				return base
			}
			select {
			case <-hitTimer.C:
				if hit := pickBest(hitCh); hit != nil {
					base.Success = true
					base.APIURL = hit.URL
					base.HTTPMethod = hit.Method
					base.Headers = hit.Headers
					base.Body = hit.PostData
					base.Confidence = hit.Confidence
					return base
				}
				base.Error = fmt.Sprintf("nav error: %v", navErr)
				return base
			case <-ctx.Done():
				base.Error = "timeout"
				return base
			}
		}
		// Navigation succeeded — wait for hitTimer.
		select {
		case <-hitTimer.C:
			if hit := pickBest(hitCh); hit != nil {
				wp.log.Info("discovered (post-nav)",
					zap.String("domain", domain),
					zap.String("url", hit.URL),
					zap.Float64("conf", hit.Confidence),
				)
				base.Success = true
				base.APIURL = hit.URL
				base.HTTPMethod = hit.Method
				base.Headers = hit.Headers
				base.Body = hit.PostData
				base.Confidence = hit.Confidence
				return base
			}
			base.Error = "page loaded, no job API found"
			return base
		case <-ctx.Done():
			base.Error = "timeout"
			return base
		}

	case <-ctx.Done():
		base.Error = "process timeout (45s)"
		return base
	}
}

// handleHijack scores a hijacked XHR or Fetch request and sends high-quality
// hits to hitCh. It also handles capturing all intercepted traffic for analysis.
func (wp *WorkerPool) handleHijack(h *rod.Hijack, domain string, hitCh chan *NetworkHit, earlyExitCh chan struct{}, once *bool, hitFound *int32) {
	reqURL := h.Request.URL().String()

	// URL-based blocking for analytics even for XHR/Fetch.
	if ShouldBlockURL(reqURL) {
		h.ContinueRequest(&proto.FetchContinueRequest{})
		return
	}

	rawHeaders := h.Request.Headers()
	headers := make(map[string]string, len(rawHeaders))
	for k, v := range rawHeaders {
		headers[k] = fmt.Sprint(v)
	}

	reqBody := h.Request.Body()

	// Transparently load the response to access the body for Stage 2 scoring
	// We need response headers/size for Path A too.
	err := h.LoadResponse(http.DefaultClient, true)
	if err != nil {
		return // Network error fetching resource, abort processing this intercepted request
	}

	respBodyStr := h.Response.Body()
	respBodyBytes := []byte(respBodyStr)
	respSize := len(respBodyBytes)

	// Extract Content-Type for Path A
	var respContentType string
	respHeadersPayload := h.Response.Payload().ResponseHeaders
	for _, hdr := range respHeadersPayload {
		if strings.ToLower(hdr.Name) == "content-type" {
			respContentType = hdr.Value
			break
		}
	}

	// Block HTML responses — job APIs always return JSON, never HTML.
	if ShouldBlockContentType(respContentType) {
		return
	}

	// Early exit: if the URL is on the response blocklist, skip scoring entirely.
	// This prevents cookielaw.org, statuspage.io, etc. from ever reaching the database.
	if isBlockedResponseURL(reqURL) {
		return
	}

	// Calculate independent path scores
	urlConf := ScoreURLPath(reqURL, h.Request.Method(), respContentType, respSize)
	bodyConf, _ := wp.classifier.ScoreResponseBody(reqURL, respBodyBytes)

	// Calculate final combination using classifier logic
	conf := wp.classifier.CalculateFinalConfidence(urlConf, bodyConf, reqURL)

	minConfStr := os.Getenv("MIN_CONFIDENCE")
	minConf := 0.60
	if minConfStr != "" {
		if val, err := strconv.ParseFloat(minConfStr, 64); err == nil {
			minConf = val
		}
	}

	// Always record to the capture file for post-scan analytics
	entry := capture.CaptureEntry{
		Timestamp:       time.Now(),
		Domain:          domain,
		URL:             reqURL,
		Method:          h.Request.Method(),
		RequestHeaders:  headers,
		ResponseStatus:  h.Response.Payload().ResponseCode,
		ClassifierScore: urlConf,
		BodyScore:       bodyConf,
		WasHit:          conf >= minConf,
	}

	// Response headers
	respHeaders := h.Response.Payload().ResponseHeaders
	if respHeaders != nil {
		rh := make(map[string]string)
		for _, h := range respHeaders {
			rh[h.Name] = h.Value
		}
		entry.ResponseHeaders = rh
	}

	// Preview body
	if len(respBodyBytes) > 2048 {
		entry.ResponseBodyPreview = string(respBodyBytes[:2048])
	} else {
		entry.ResponseBodyPreview = respBodyStr
	}

	// WasHit is based on the blended confidence vs MIN_CONFIDENCE threshold
	finalHit := conf >= minConf
	entry.WasHit = finalHit

	if conf < minConf && (urlConf >= 0.35 || bodyConf >= 0.35) {
		// Store near-miss for later review
		go wp.recordNearMiss(domain, reqURL, respContentType, respBodyStr, respSize, urlConf, bodyConf, conf)
	}

	if wp.capture != nil {
		_ = wp.capture.Record(entry)
	}

	// If it doesn't meet the MIN_CONFIDENCE threshold, stop processing
	if !finalHit {
		return
	}

	hit := &NetworkHit{
		URL:        reqURL,
		Method:     h.Request.Method(),
		Headers:    headers,
		PostData:   reqBody,
		Confidence: conf,
	}

	wp.log.Info("interceptor: candidate",
		zap.String("domain", domain),
		zap.String("url", reqURL),
		zap.Float64("url_conf", urlConf),
		zap.Float64("body_conf", bodyConf),
		zap.Float64("conf", conf),
	)

	select {
	case hitCh <- hit:
	default:
	}

	if conf >= earlyExitConfidence && !*once {
		*once = true
		atomic.StoreInt32(hitFound, 1)
		close(earlyExitCh)
	}
}

// pickBest drains hitCh and returns the candidate with the highest confidence.
func pickBest(hitCh chan *NetworkHit) *NetworkHit {
	var best *NetworkHit
	for {
		select {
		case h := <-hitCh:
			if best == nil || h.Confidence > best.Confidence {
				best = h
			}
		default:
			return best
		}
	}
}

// suppressJS hides navigator.webdriver before any page script runs.
const suppressJS = `
Object.defineProperty(navigator, 'webdriver', { get: () => undefined, configurable: true });
window.chrome = { runtime: {} };
`

// envIntPositive reads a positive int env var, returns def if missing/invalid/non-positive.
func envIntPositive(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
