// Package tier2_v3 — interceptor.go
// NetworkHit carries a scored XHR/Fetch request candidate.
// The actual interception logic is in worker.go via rod.HijackRequests —
// this file only defines the shared data type used across the package.
package tier2_v3

// NetworkHit is a captured API request that passed the Stage 1 classifier.
type NetworkHit struct {
	URL        string
	Method     string
	Headers    map[string]string
	PostData   string
	Confidence float64
}
