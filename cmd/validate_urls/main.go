package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

type ValidationResult struct {
	OriginalURL         string `json:"original_url"`
	Status              string `json:"status"` // reachable, redirect, dead, timeout
	HTTPStatusCode      int    `json:"http_status_code"`
	RedirectDestination string `json:"redirect_destination,omitempty"`
	LatencyMs           int64  `json:"latency_ms"`
}

func main() {
	inputFile := "careers_urls.json"
	outputFile := "validation_results.json"

	data, err := os.ReadFile(inputFile)
	if err != nil {
		log.Fatalf("failed to read %s: %v", inputFile, err)
	}

	var urls []string
	if err := json.Unmarshal(data, &urls); err != nil {
		log.Fatalf("failed to parse JSON: %v", err)
	}

	results := make([]ValidationResult, len(urls))

	jobCh := make(chan int, len(urls))
	for i := range urls {
		jobCh <- i
	}
	close(jobCh)

	var wg sync.WaitGroup
	var mu sync.Mutex

	// Counters
	var (
		countReachable int
		countRedirect  int
		countDead      int
		countTimeout   int
	)

	workerCount := 50

	client := &fasthttp.Client{
		ReadTimeout:                   10 * time.Second,
		WriteTimeout:                  10 * time.Second,
		MaxIdleConnDuration:           10 * time.Second,
		NoDefaultUserAgentHeader:      false,
		DisableHeaderNamesNormalizing: true,
		DisablePathNormalizing:        true,
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobCh {
				url := urls[idx]

				req := fasthttp.AcquireRequest()
				resp := fasthttp.AcquireResponse()

				req.SetRequestURI(url)
				req.Header.SetMethod(fasthttp.MethodHead)
				req.Header.SetUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

				start := time.Now()
				err := client.DoTimeout(req, resp, 10*time.Second)
				latency := time.Since(start).Milliseconds()

				result := ValidationResult{
					OriginalURL: url,
					LatencyMs:   latency,
				}

				if err == fasthttp.ErrTimeout {
					result.Status = "timeout"
				} else if err != nil {
					result.Status = "dead"
				} else {
					code := resp.StatusCode()
					result.HTTPStatusCode = code

					if code >= 200 && code < 300 {
						result.Status = "reachable"
					} else if code == 301 || code == 302 || code == 307 || code == 308 {
						result.Status = "redirect"
						result.RedirectDestination = string(resp.Header.Peek("Location"))
					} else if code >= 400 && code <= 599 {
						result.Status = "dead"
					} else {
						// 3xx other than redirect, treat as reachable for now
						result.Status = "reachable"
					}
				}

				mu.Lock()
				results[idx] = result
				switch result.Status {
				case "reachable":
					countReachable++
				case "redirect":
					countRedirect++
				case "dead":
					countDead++
				case "timeout":
					countTimeout++
				}
				mu.Unlock()

				fasthttp.ReleaseRequest(req)
				fasthttp.ReleaseResponse(resp)
			}
		}()
	}

	wg.Wait()

	outData, _ := json.MarshalIndent(results, "", "  ")
	_ = os.WriteFile(outputFile, outData, 0644)

	fmt.Printf("=== Validation Summary ===\n")
	fmt.Printf("Total URLs Checked: %d\n", len(urls))
	fmt.Printf("Reachable: %d\n", countReachable)
	fmt.Printf("Redirects: %d\n", countRedirect)
	fmt.Printf("Dead:      %d\n", countDead)
	fmt.Printf("Timeout:   %d\n", countTimeout)
}
