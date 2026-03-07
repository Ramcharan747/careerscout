// Package normalise — normaliser.go
// Maps arbitrary JSON from thousands of different company APIs into the
// canonical job schema: title, company, location, salary, posted_at, apply_url.
// Uses heuristic field name mapping first; falls back to LLM for unknowns.
package normalise

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// CanonicalJob is the normalised output schema for a single job listing.
type CanonicalJob struct {
	CompanyID      string
	ExternalJobID  string
	Title          string
	Location       string
	SalaryMin      float64
	SalaryMax      float64
	SalaryCurrency string
	SalaryRaw      string
	PostedAt       *time.Time
	ApplyURL       string
	RawJSON        json.RawMessage
}

// Normaliser maps raw API response JSON to canonical job records.
type Normaliser struct {
	// Future: LLM client for complex/unknown schemas
}

// NewNormaliser creates a new Normaliser.
func NewNormaliser() *Normaliser {
	return &Normaliser{}
}

// Normalise converts a raw job API response envelope to canonical job records.
func (n *Normaliser) Normalise(ctx context.Context, envelope RawJobEnvelope) ([]CanonicalJob, error) {
	// Try to parse the raw JSON as an array or object with a jobs array
	var rawData interface{}
	if err := json.Unmarshal(envelope.RawJSON, &rawData); err != nil {
		return nil, fmt.Errorf("normalise: parse raw json: %w", err)
	}

	// Extract the array of job objects
	jobsArray, err := extractJobsArray(rawData)
	if err != nil {
		return nil, fmt.Errorf("normalise: extract jobs array: %w", err)
	}

	var canonical []CanonicalJob
	for _, rawJob := range jobsArray {
		job, err := mapJob(rawJob, envelope.CompanyID)
		if err != nil {
			continue // skip unmappable records
		}
		rawBytes, _ := json.Marshal(rawJob)
		job.RawJSON = rawBytes
		canonical = append(canonical, job)
	}

	return canonical, nil
}

// extractJobsArray finds the array of job objects in a raw API response.
// Company APIs return jobs in many different shapes:
//   - Top-level array: [{...}, ...]
//   - Nested: {"jobs": [...]}  or {"postings": [...]} or {"data": {"jobs": [...]}}
func extractJobsArray(data interface{}) ([]map[string]interface{}, error) {
	switch v := data.(type) {
	case []interface{}:
		return toMapSlice(v)
	case map[string]interface{}:
		// Look for well-known array field names
		for _, key := range []string{
			"jobs", "postings", "data", "results", "items",
			"positions", "openings", "vacancies", "requisitions",
		} {
			if val, ok := v[key]; ok {
				if arr, ok := val.([]interface{}); ok {
					return toMapSlice(arr)
				}
				// data.jobs nesting
				if nested, ok := val.(map[string]interface{}); ok {
					return extractJobsArray(nested)
				}
			}
		}
	}
	return nil, fmt.Errorf("could not locate jobs array in response")
}

func toMapSlice(arr []interface{}) ([]map[string]interface{}, error) {
	result := make([]map[string]interface{}, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]interface{}); ok {
			result = append(result, m)
		}
	}
	return result, nil
}

// mapJob applies heuristic field name mapping to a single job object.
func mapJob(raw map[string]interface{}, companyID string) (CanonicalJob, error) {
	job := CanonicalJob{CompanyID: companyID}

	// External job ID — needed for deduplication
	for _, key := range []string{"id", "req_id", "jobId", "job_id", "requisitionId", "externalId"} {
		if v := getString(raw, key); v != "" {
			job.ExternalJobID = v
			break
		}
	}
	if job.ExternalJobID == "" {
		return job, fmt.Errorf("no external job ID found")
	}

	// Title
	for _, key := range []string{"title", "job_title", "jobTitle", "name", "position"} {
		if v := getString(raw, key); v != "" {
			job.Title = v
			break
		}
	}

	// Location
	for _, key := range []string{"location", "office", "city", "workplaceType", "locationName"} {
		if v := getStringOrNested(raw, key); v != "" {
			job.Location = v
			break
		}
	}

	// Apply URL
	for _, key := range []string{"applyUrl", "apply_url", "url", "jobUrl", "hostedUrl", "absoluteUrl"} {
		if v := getString(raw, key); v != "" {
			job.ApplyURL = v
			break
		}
	}

	// Salary (best-effort)
	for _, key := range []string{"salary", "compensation", "salaryRange"} {
		if v := getString(raw, key); v != "" {
			job.SalaryRaw = v
			job.SalaryMin, job.SalaryMax, job.SalaryCurrency = parseSalary(v)
			break
		}
	}

	// Posted date
	for _, key := range []string{"posted_at", "postedAt", "createdAt", "updatedAt", "pubDate"} {
		if v := getString(raw, key); v != "" {
			if t, err := parseTime(v); err == nil {
				job.PostedAt = &t
			}
			break
		}
	}

	return job, nil
}

// ── Helper utilities ──────────────────────────────────────────────────────────

var salaryRe = regexp.MustCompile(`([\$£€¥]|USD|EUR|GBP|INR)?[\s]*([\d,]+)[\s]*(?:-|to)?[\s]*([\d,]+)?`)

func parseSalary(raw string) (min, max float64, currency string) {
	m := salaryRe.FindStringSubmatch(raw)
	if m == nil {
		return 0, 0, ""
	}
	currency = strings.TrimSpace(m[1])
	minStr := strings.ReplaceAll(m[2], ",", "")
	maxStr := strings.ReplaceAll(m[3], ",", "")
	fmt.Sscanf(minStr, "%f", &min) //nolint:errcheck
	if maxStr != "" {
		fmt.Sscanf(maxStr, "%f", &max) //nolint:errcheck
	}
	return
}

func parseTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339, time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02",
		"January 2, 2006",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("no format matched for %q", s)
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		switch s := v.(type) {
		case string:
			return strings.TrimSpace(s)
		case float64:
			return fmt.Sprintf("%.0f", s)
		}
	}
	return ""
}

func getStringOrNested(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		switch s := v.(type) {
		case string:
			return strings.TrimSpace(s)
		case map[string]interface{}:
			// e.g. location: {name: "London"}
			for _, sub := range []string{"name", "text", "city"} {
				if sv := getString(s, sub); sv != "" {
					return sv
				}
			}
		}
	}
	return ""
}
