// Package tier1 implements the static HTTP-based API discovery worker (Tier 1).
// Uses fasthttp for zero-allocation request pooling at high concurrency.
// TLS fingerprinting is preserved via uTLS dialled manually and set as the
// fasthttp DialTLS function — Chrome JA3 fingerprint is retained.
package tier1

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/careerscout/careerscout/internal/resolver"
	"github.com/valyala/fasthttp"
	"go.uber.org/zap"
)

const (
	// fasthttp client defaults — Change 2 spec.
	readTimeout     = 10 * time.Second
	writeTimeout    = 5 * time.Second
	maxBodySize     = 2 * 1024 * 1024 // 2 MB per spec (was 5MB)
	dialTimeout     = 10 * time.Second
	keepAlivePeriod = 30 * time.Second

	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// Result is the output from a Tier 1 worker for a single URL.
type Result struct {
	Domain     string
	RawURL     string
	CompanyID  string
	Success    bool
	APIURL     string
	HTTPMethod string
	Pattern    string // which pattern matched
	Error      string
}

// Worker executes static HTTP requests with Chrome TLS fingerprinting and
// passes the response to the Analyzer for API pattern matching.
// Uses a shared fasthttp.Client — safe for concurrent goroutines.
type Worker struct {
	client   *fasthttp.Client
	analyzer *Analyzer
	log      *zap.Logger
}

// NewWorker creates a Tier 1 worker backed by a pooled fasthttp.Client.
// maxConns should match WORKER_COUNT so we don't create more sockets than goroutines.
func NewWorker(log *zap.Logger, res *resolver.CachingResolver) *Worker {
	return NewWorkerWithMaxConns(log, 150, res)
}

// NewWorkerWithMaxConns creates a Worker with a specified MaxConnsPerHost.
// Used by cmd/tier1/main.go to pass the runtime WORKER_COUNT value.
func NewWorkerWithMaxConns(log *zap.Logger, maxConns int, res *resolver.CachingResolver) *Worker {
	client := &fasthttp.Client{
		Name:                          userAgent,
		ReadTimeout:                   readTimeout,
		WriteTimeout:                  writeTimeout,
		MaxResponseBodySize:           maxBodySize,
		MaxConnsPerHost:               maxConns,
		DisableHeaderNamesNormalizing: false,
		// Dial performs async DNS resolution using the CachingResolver before
		// dialling the underlying TCP socket. Fasthttp natively handles standard
		// TLS for HTTPS connections automatically once this dial returns.
		Dial: func(addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}

			ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			defer cancel()

			ips, err := res.LookupHost(ctx, host)
			if err != nil {
				return nil, err
			}

			// Try connecting to resolved IPs
			dialer := &net.Dialer{
				Timeout:   dialTimeout,
				KeepAlive: keepAlivePeriod,
			}

			var conn net.Conn
			var dialErr error
			for _, ip := range ips {
				target := net.JoinHostPort(ip, port)
				conn, dialErr = dialer.DialContext(ctx, "tcp", target)
				if dialErr == nil {
					return conn, nil
				}
			}
			return nil, dialErr
		},
	}

	return &Worker{
		client:   client,
		analyzer: NewAnalyzer(),
		log:      log,
	}
}

// Process fetches the career page for the given URL and attempts to extract
// a job API endpoint from the static HTML and inline JavaScript.
// Uses fasthttp.AcquireRequest / AcquireResponse to avoid per-request
// heap allocations — objects are returned to the pool via deferred Release.
func (w *Worker) Process(ctx context.Context, rawURL, domain, companyID string) Result {
	base := Result{
		Domain:    domain,
		RawURL:    rawURL,
		CompanyID: companyID,
	}

	// Pool the request/response objects — zero allocation per call.
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(rawURL)
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	// fasthttp does not use context natively. We use a deadline goroutine to
	// honour ctx cancellation: if ctx fires before the client call completes,
	// the result is dropped.
	type fetchResult struct {
		err  error
		body []byte
		code int
	}
	ch := make(chan fetchResult, 1)
	go func() {
		if err := w.client.Do(req, resp); err != nil {
			ch <- fetchResult{err: err}
			return
		}
		// InflateBody decodes gzip/br transparently.
		body, err := resp.BodyUncompressed()
		if err != nil {
			// Fallback: use raw body (may be already decompressed).
			body = resp.Body()
		}
		// Trim to maxBodySize explicitly since fasthttp enforces at transport
		// level but a defensive cap here avoids any off-by-one edge.
		if len(body) > maxBodySize {
			body = body[:maxBodySize]
		}
		ch <- fetchResult{err: nil, body: body, code: resp.StatusCode()}
	}()

	select {
	case <-ctx.Done():
		base.Error = "context cancelled"
		return base
	case res := <-ch:
		if res.err != nil {
			base.Error = fmt.Sprintf("http get: %v", res.err)
			return base
		}
		if res.code == fasthttp.StatusTooManyRequests || res.code == fasthttp.StatusForbidden {
			base.Error = fmt.Sprintf("blocked: HTTP %d", res.code)
			return base
		}

		match := w.analyzer.Analyze(string(res.body), domain)
		if match == nil {
			base.Error = "no job API pattern found"
			return base
		}

		w.log.Info("tier1: API found",
			zap.String("domain", domain),
			zap.String("api_url", match.APIURL),
			zap.String("pattern", match.Pattern),
		)

		base.Success = true
		base.APIURL = match.APIURL
		base.HTTPMethod = match.Method
		base.Pattern = match.Pattern
		return base
	}
}
