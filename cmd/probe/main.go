// cmd/probe/main.go — standalone probe using go-rod (replaces chromedp version)
// Usage: ./probe <url>
// Navigates to a URL, intercepts all XHR/Fetch requests, prints to stdout.
// No Kafka, no DB. Just raw proof that interception works.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/careerscout/careerscout/internal/tier2_v3"
)

func main() {
	url := "https://jobs.ashbyhq.com"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}

	fmt.Printf("🔍 Probing: %s\n\n", url)

	log, _ := zap.NewDevelopment()
	defer log.Sync()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Single worker for probe
	wp := tier2_v3.NewWorkerPool(ctx, 1, log, nil, "")
	defer wp.Close()

	result := wp.Process(ctx, url, "probe", "")

	fmt.Println("\n─────────────────────────────────────")
	if result.Success {
		fmt.Printf("✅ Discovered: %s %s (conf=%.2f)\n", result.HTTPMethod, result.APIURL, result.Confidence)
	} else {
		fmt.Printf("❌ No job API found: %s\n", result.Error)
	}
}
