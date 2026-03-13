package jobparser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Job struct {
	ExternalID      string
	Title           string
	LocationRaw     string
	City            string
	Country         string
	CountryCode     string
	Department      string
	Team            string
	EmploymentType  string
	ExperienceLevel string
	IsRemote        bool
	RemoteType      string
	ApplyURL        string
	JobPageURL      string
	PostedAt        *time.Time
	Description     string
	RawJSON         []byte
}

// SourceSchema defines how to extract job fields from any API response.
// Stored in DB — one row per ATS platform or custom source.
// Add a new ATS by adding one entry to KnownSchemas, no code changes needed.
type SourceSchema struct {
	ATSPlatform string `json:"ats_platform"`

	// Where is the jobs array in the response?
	// ""                              = root is already an array
	// "jobs"                          = response["jobs"]
	// "data"                          = response["data"]
	// "data.jobBoardWithTeams.jobPostings" = deeply nested
	JobsPath string `json:"jobs_path"`

	// Field paths — dot notation, array index supported
	// "id"                  = job["id"]
	// "location.name"       = job["location"]["name"]
	// "departments[0].name" = job["departments"][0]["name"]
	FieldExternalID     string `json:"field_external_id"`
	FieldTitle          string `json:"field_title"`
	FieldLocationRaw    string `json:"field_location_raw"`
	FieldCity           string `json:"field_city"`
	FieldCountryCode    string `json:"field_country_code"`
	FieldDepartment     string `json:"field_department"`
	FieldEmploymentType string `json:"field_employment_type"`
	FieldIsRemote       string `json:"field_is_remote"`
	FieldApplyURL       string `json:"field_apply_url"`
	FieldPostedAt       string `json:"field_posted_at"`
	FieldDescription    string `json:"field_description"`

	// Type hints
	PostedAtFormat  string `json:"posted_at_format"` // "rfc3339","unix_ms","unix_s","date"
	ExternalIDIsInt bool   `json:"external_id_is_int"`

	// Maps raw employment type strings to normalised values
	// e.g. {"FullTime":"full_time","FULL_TIME":"full_time"}
	EmploymentTypeMap map[string]string `json:"employment_type_map"`

	// String values that mean remote=true
	// e.g. ["remote","fully_remote","true"]
	RemoteValues []string `json:"remote_values"`

	// Lookup table config for relational responses (e.g. Freshteam)
	// where job has branch_id that references a separate branches array
	LookupConfig map[string]LookupTableConfig `json:"lookup_config"`
}

// LookupTableConfig describes how to resolve a relational ID to fields
type LookupTableConfig struct {
	IDField     string            `json:"id_field"`     // field in job: "branch_id"
	LookupArray string            `json:"lookup_array"` // top-level array: "branches"
	FieldMap    map[string]string `json:"field_map"`    // job_field -> lookup_field
}

// KnownSchemas — pre-registered schemas for all known ATS platforms
// To add a new ATS: add one entry here. Zero code changes elsewhere.
var KnownSchemas = map[string]SourceSchema{
	"jobvite": {
		ATSPlatform:      "jobvite",
		JobsPath:         "reqList",
		FieldExternalID:  "id",
		FieldTitle:       "title",
		FieldLocationRaw: "jobRegion",
		FieldCity:        "jobRegion", 
		FieldDepartment:  "jobCategory",
		FieldEmploymentType: "jobType",
		FieldApplyURL:    "applyLink",
		FieldPostedAt:    "publishedDate",
		PostedAtFormat:   "unix_ms",
		EmploymentTypeMap: map[string]string{
			"Full-Time": "full_time",
			"Part-Time": "part_time", 
			"Contract":  "contract",
			"Intern":    "internship",
		},
	},
	"breezyhr": {
		ATSPlatform:      "breezyhr",
		JobsPath:         "",           // root is array
		FieldExternalID:  "_id",
		FieldTitle:       "name",
		FieldLocationRaw: "location.name",
		FieldCity:        "location.city",
		FieldCountryCode: "location.country.id",
		FieldDepartment:  "department.name",
		FieldEmploymentType: "type.id",
		FieldIsRemote:    "location.is_remote",
		FieldApplyURL:    "url",
		RemoteValues:     []string{"true", "remote"},
		EmploymentTypeMap: map[string]string{
			"full_time":  "full_time",
			"part_time":  "part_time",
			"contract":   "contract",
			"internship": "internship",
			"temporary":  "contract",
		},
	},
	"personio": {
		ATSPlatform:      "personio",
		JobsPath:         "",           // root is array
		FieldExternalID:  "id",
		FieldTitle:       "name",
		FieldLocationRaw: "office",
		FieldDepartment:  "department",
		FieldEmploymentType: "employment_type",
		FieldApplyURL:    "url",
		ExternalIDIsInt:  true,
		EmploymentTypeMap: map[string]string{
			"full-time":  "full_time",
			"part-time":  "part_time",
			"intern":     "internship",
			"freelance":  "contract",
		},
	},
	"greenhouse": {
		ATSPlatform:     "greenhouse",
		JobsPath:        "jobs",
		FieldExternalID: "id",
		FieldTitle:      "title",
		FieldLocationRaw: "location.name",
		FieldDepartment: "departments[0].name",
		FieldApplyURL:   "absolute_url",
		FieldPostedAt:   "first_published",
		PostedAtFormat:  "rfc3339",
		ExternalIDIsInt: true,
	},
	"lever": {
		ATSPlatform:         "lever",
		JobsPath:            "",
		FieldExternalID:     "id",
		FieldTitle:          "text",
		FieldLocationRaw:    "categories.location",
		FieldDepartment:     "categories.team",
		FieldEmploymentType: "categories.commitment",
		FieldApplyURL:       "applyUrl",
		FieldPostedAt:       "createdAt",
		PostedAtFormat:      "unix_ms",
		EmploymentTypeMap: map[string]string{
			"Full-time": "full_time", "Full Time": "full_time", "Fulltime": "full_time",
			"Part-time": "part_time", "Part Time": "part_time",
			"Contract": "contract", "Contractor": "contract",
			"Internship": "internship", "Intern": "internship",
		},
	},
	"ashby": {
		ATSPlatform:         "ashby",
		JobsPath:            "data.jobBoardWithTeams.jobPostings",
		FieldExternalID:     "id",
		FieldTitle:          "title",
		FieldLocationRaw:    "locationName",
		FieldDepartment:     "teamName",
		FieldEmploymentType: "employmentType",
		FieldIsRemote:       "isRemote",
		FieldApplyURL:       "jobUrl",
		EmploymentTypeMap: map[string]string{
			"FullTime": "full_time", "PartTime": "part_time",
			"Contract": "contract", "Intern": "internship",
		},
		RemoteValues: []string{"true"},
	},
	"ashbyhq": {
		ATSPlatform:         "ashbyhq",
		JobsPath:            "data.jobBoardWithTeams.jobPostings",
		FieldExternalID:     "id",
		FieldTitle:          "title",
		FieldLocationRaw:    "locationName",
		FieldDepartment:     "teamName",
		FieldEmploymentType: "employmentType",
		FieldIsRemote:       "isRemote",
		FieldApplyURL:       "jobUrl",
		EmploymentTypeMap: map[string]string{
			"FullTime": "full_time", "PartTime": "part_time",
			"Contract": "contract", "Intern": "internship",
		},
		RemoteValues: []string{"true"},
	},
	"recruitee": {
		ATSPlatform:      "recruitee",
		JobsPath:         "offers",
		FieldExternalID:  "id",
		FieldTitle:       "title",
		FieldLocationRaw: "location",
		FieldDepartment:  "department",
		FieldIsRemote:    "remote",
		FieldApplyURL:    "url",
		ExternalIDIsInt:  true,
		RemoteValues:     []string{"true"},
	},
	"freshteam": {
		ATSPlatform:     "freshteam",
		JobsPath:        "jobs",
		FieldExternalID: "id",
		FieldTitle:      "title",
		FieldIsRemote:   "remote",
		FieldApplyURL:   "url",
		FieldPostedAt:   "created_at",
		PostedAtFormat:  "rfc3339",
		ExternalIDIsInt: true,
		RemoteValues:    []string{"true"},
		LookupConfig: map[string]LookupTableConfig{
			"location": {
				IDField:     "branch_id",
				LookupArray: "branches",
				FieldMap: map[string]string{
					"city":         "city",
					"country_code": "country_code",
					"location_raw": "location",
				},
			},
			"department": {
				IDField:     "job_role_id",
				LookupArray: "job_roles",
				FieldMap:    map[string]string{"department": "name"},
			},
		},
	},
	"pinpointhq": {
		ATSPlatform:         "pinpointhq",
		JobsPath:            "data",
		FieldExternalID:     "id",
		FieldTitle:          "title",
		FieldLocationRaw:    "location.name",
		FieldCity:           "location.city",
		FieldCountryCode:    "location.country_code",
		FieldDepartment:     "job.department.name",
		FieldEmploymentType: "employment_type",
		FieldIsRemote:       "workplace_type",
		FieldApplyURL:       "url",
		RemoteValues:        []string{"remote", "fully_remote"},
	},
	"rippling": {
		ATSPlatform:         "rippling",
		JobsPath:            "", // root array
		FieldExternalID:     "uuid",
		FieldTitle:          "name",
		FieldLocationRaw:    "workLocation.label",
		FieldDepartment:     "department.label",
		FieldApplyURL:       "url",
		FieldEmploymentType: "employmentType",
		EmploymentTypeMap: map[string]string{
			"FULL_TIME": "full_time", "PART_TIME": "part_time",
			"CONTRACTOR": "contract", "INTERN": "internship",
		},
	},
	"teamtailor": {
		ATSPlatform:      "teamtailor",
		JobsPath:         "data",
		FieldExternalID:  "id",
		FieldTitle:       "attributes.title",
		FieldIsRemote:    "attributes.remote-status",
		FieldDescription: "attributes.pitch",
		FieldApplyURL:    "links.careersite-job-url",
		RemoteValues:     []string{"fully-remote", "remote"},
	},
	"workable": {
		ATSPlatform:      "workable",
		JobsPath:         "results",
		FieldExternalID:  "id",
		FieldTitle:       "title",
		FieldCity:        "location.city",
		FieldCountryCode: "location.countryCode",
		FieldDepartment:  "department[0]",
		FieldIsRemote:    "remote",
		FieldPostedAt:    "published",
		PostedAtFormat:   "rfc3339",
		ExternalIDIsInt:  true,
		RemoteValues:     []string{"true"},
		FieldApplyURL:    "url",
		FieldEmploymentType: "employment_type",
		EmploymentTypeMap: map[string]string{
			"FullTime": "full_time", "PartTime": "part_time",
			"Contract": "contract", "Intern": "internship",
		},
	},
	"smartrecruiters": {
		ATSPlatform:         "smartrecruiters",
		JobsPath:            "content",
		FieldExternalID:     "id",
		FieldTitle:          "name",
		FieldCity:           "location.city",
		FieldCountryCode:    "location.country.code",
		FieldDepartment:     "department.label",
		FieldEmploymentType: "typeOfEmployment.label",
		EmploymentTypeMap: map[string]string{
			"Full-time": "full_time", "Part-time": "part_time",
			"Contract": "contract", "Temporary": "contract",
			"Intern": "internship",
		},
	},
}

// ==========================================
// GENERIC PARSER ENGINE
// ==========================================

// ParseWithSchema parses any API response body using the given schema.
// This single function replaces all 10+ individual ATS parsers.
func ParseWithSchema(schema SourceSchema, body []byte) ([]Job, error) {
	var raw interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("json unmarshal failed: %w", err)
	}

	// Build lookup tables for relational responses (e.g. Freshteam)
	lookups := map[string]map[float64]map[string]interface{}{}
	if len(schema.LookupConfig) > 0 {
		if rawMap, ok := raw.(map[string]interface{}); ok {
			for lookupName, lc := range schema.LookupConfig {
				arr, ok := rawMap[lc.LookupArray].([]interface{})
				if !ok {
					continue
				}
				lookups[lookupName] = map[float64]map[string]interface{}{}
				for _, item := range arr {
					itemMap, ok := item.(map[string]interface{})
					if !ok {
						continue
					}
					if idFloat, ok := itemMap["id"].(float64); ok {
						lookups[lookupName][idFloat] = itemMap
					}
				}
			}
		}
	}

	// Navigate to jobs array
	jobsRaw := navigatePath(raw, schema.JobsPath)
	if jobsRaw == nil {
		return nil, fmt.Errorf("jobs path %q not found in response", schema.JobsPath)
	}
	jobsArr, ok := jobsRaw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("jobs path %q is not an array (got %T)", schema.JobsPath, jobsRaw)
	}

	var jobs []Job
	for _, jobRaw := range jobsArr {
		jobMap, ok := jobRaw.(map[string]interface{})
		if !ok {
			continue
		}

		title := extractString(jobMap, schema.FieldTitle)
		if title == "" {
			continue
		}

		job := Job{Title: title}

		// External ID
		if schema.ExternalIDIsInt {
			if v := extractFloat(jobMap, schema.FieldExternalID); v != 0 {
				job.ExternalID = strconv.Itoa(int(v))
			}
		} else {
			job.ExternalID = extractString(jobMap, schema.FieldExternalID)
		}
		if job.ExternalID == "" {
			continue
		}

		job.LocationRaw = extractString(jobMap, schema.FieldLocationRaw)
		job.City = extractString(jobMap, schema.FieldCity)
		job.CountryCode = extractString(jobMap, schema.FieldCountryCode)
		job.Department = extractString(jobMap, schema.FieldDepartment)
		job.ApplyURL = extractString(jobMap, schema.FieldApplyURL)
		job.Description = extractString(jobMap, schema.FieldDescription)

		rawET := extractString(jobMap, schema.FieldEmploymentType)
		job.EmploymentType = normaliseEmploymentType(rawET, schema.EmploymentTypeMap)

		remoteRaw := extractValue(jobMap, schema.FieldIsRemote)
		job.IsRemote = isRemoteValue(remoteRaw, schema.RemoteValues)

		job.PostedAt = extractTime(jobMap, schema.FieldPostedAt, schema.PostedAtFormat)

		// Apply lookup tables
		for lookupName, lc := range schema.LookupConfig {
			idVal := extractFloat(jobMap, lc.IDField)
			if idVal == 0 {
				continue
			}
			lookupTable, ok := lookups[lookupName]
			if !ok {
				continue
			}
			lookupRow, ok := lookupTable[idVal]
			if !ok {
				continue
			}
			for jobField, lookupField := range lc.FieldMap {
				val, _ := lookupRow[lookupField].(string)
				switch jobField {
				case "city":
					job.City = val
				case "country_code":
					job.CountryCode = val
				case "location_raw":
					job.LocationRaw = val
				case "department":
					job.Department = val
				}
			}
		}

		job.RawJSON, _ = json.Marshal(jobMap)
		jobs = append(jobs, job)
	}

	return jobs, nil
}

// ==========================================
// GEMINI FLASH SCHEMA DETECTION (FREE LLM)
// ==========================================

// DetectSchema sends an unknown API response to Gemini Flash to auto-detect
// the field schema. Called only ONCE per new unknown source — result is stored
// in DB and reused forever. Never called again for the same source.
func DetectSchema(apiURL string, body []byte, geminiAPIKey string) (*SourceSchema, error) {
	// Use first 3000 bytes — enough for Gemini to see the structure
	sample := string(body)
	if len(sample) > 3000 {
		sample = sample[:3000] + "\n...(truncated)"
	}

	prompt := fmt.Sprintf(`You are a JSON schema analyser for a job board data pipeline.

Analyse this API response from: %s

Response sample:
%s

Identify the JSON field paths for job listing data. Use dot notation for nested fields.
Examples: "location.name", "categories.team", "departments[0].name", "job.department.name"
Use "" (empty string) if a field does not exist in this response.

Return ONLY this JSON object, no markdown, no explanation:
{
  "jobs_path": "path to array of jobs, or empty string if root is the array",
  "field_external_id": "path to unique job ID",
  "field_title": "path to job title",
  "field_location_raw": "path to full location string",
  "field_city": "path to city name or empty",
  "field_country_code": "path to ISO country code or empty",
  "field_department": "path to department or team name",
  "field_employment_type": "path to employment type field",
  "field_is_remote": "path to remote work indicator",
  "field_apply_url": "path to application or job page URL",
  "field_posted_at": "path to posting date",
  "field_description": "path to job description text",
  "posted_at_format": "one of: rfc3339, unix_ms, unix_s, date, or empty",
  "external_id_is_int": false,
  "employment_type_map": {},
  "remote_values": []
}`, apiURL, sample)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]string{{"text": prompt}}},
		},
	})

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key=%s",
		geminiAPIKey,
	)
	resp, err := http.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("gemini request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gemini %d: %s", resp.StatusCode, string(respBody))
	}

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("gemini response parse failed: %w", err)
	}
	if len(geminiResp.Candidates) == 0 {
		return nil, fmt.Errorf("gemini returned no candidates")
	}

	text := strings.TrimSpace(geminiResp.Candidates[0].Content.Parts[0].Text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var schema SourceSchema
	if err := json.Unmarshal([]byte(text), &schema); err != nil {
		return nil, fmt.Errorf("schema parse failed: %w\ngot: %s", err, text)
	}
	return &schema, nil
}

// ==========================================
// MAIN ENTRY POINT
// ==========================================

// Parse is the unified entry point for all job parsing.
// - Known ATS platform → uses pre-registered schema, zero API calls
// - Unknown platform with Gemini key → auto-detects schema, one API call, stores result
// - Unknown platform without key → returns empty slice
// Returns jobs, the schema used (for storage), and any error.
func Parse(atsPlatform string, body []byte, apiURL string, geminiAPIKey string) ([]Job, *SourceSchema, error) {
	if schema, ok := KnownSchemas[atsPlatform]; ok {
		jobs, err := ParseWithSchema(schema, body)
		return jobs, &schema, err
	}
	if geminiAPIKey != "" {
		schema, err := DetectSchema(apiURL, body, geminiAPIKey)
		if err != nil {
			return nil, nil, err
		}
		jobs, err := ParseWithSchema(*schema, body)
		return jobs, schema, err
	}
	return []Job{}, nil, nil
}

// ==========================================
// PATH NAVIGATION + TYPE HELPERS
// ==========================================

func navigatePath(obj interface{}, path string) interface{} {
	if path == "" {
		return obj
	}
	current := obj
	for _, part := range strings.Split(path, ".") {
		if current == nil {
			return nil
		}
		// Handle array index: "departments[0]"
		if idx := strings.Index(part, "["); idx != -1 {
			key := part[:idx]
			indexStr := strings.TrimSuffix(part[idx+1:], "]")
			index, err := strconv.Atoi(indexStr)
			if err != nil {
				return nil
			}
			m, ok := current.(map[string]interface{})
			if !ok {
				return nil
			}
			arr, ok := m[key].([]interface{})
			if !ok || index >= len(arr) {
				return nil
			}
			current = arr[index]
		} else {
			m, ok := current.(map[string]interface{})
			if !ok {
				return nil
			}
			current = m[part]
		}
	}
	return current
}

func extractValue(obj map[string]interface{}, path string) interface{} {
	if path == "" {
		return nil
	}
	return navigatePath(obj, path)
}

func extractString(obj map[string]interface{}, path string) string {
	v := extractValue(obj, path)
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return strconv.Itoa(int(val))
	case bool:
		return strconv.FormatBool(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func extractFloat(obj map[string]interface{}, path string) float64 {
	v := extractValue(obj, path)
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	}
	return 0
}

func extractTime(obj map[string]interface{}, path, format string) *time.Time {
	v := extractValue(obj, path)
	if v == nil {
		return nil
	}
	var t time.Time
	var err error
	switch format {
	case "unix_ms":
		ms, ok := v.(float64)
		if !ok {
			return nil
		}
		t = time.Unix(0, int64(ms)*int64(time.Millisecond))
	case "unix_s":
		s, ok := v.(float64)
		if !ok {
			return nil
		}
		t = time.Unix(int64(s), 0)
	case "date":
		s, ok := v.(string)
		if !ok {
			return nil
		}
		t, err = time.Parse("2006-01-02", s)
		if err != nil {
			return nil
		}
	default:
		s, ok := v.(string)
		if !ok {
			return nil
		}
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			t, err = time.Parse("2006-01-02T15:04:05", s)
			if err != nil {
				return nil
			}
		}
	}
	return &t
}

func isRemoteValue(v interface{}, remoteValues []string) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		lower := strings.ToLower(val)
		for _, rv := range remoteValues {
			if lower == strings.ToLower(rv) {
				return true
			}
		}
	}
	return false
}

func normaliseEmploymentType(raw string, mapping map[string]string) string {
	if raw == "" {
		return ""
	}
	if mapping != nil {
		if v, ok := mapping[raw]; ok {
			return v
		}
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "full"):
		return "full_time"
	case strings.Contains(lower, "part"):
		return "part_time"
	case strings.Contains(lower, "contract") || strings.Contains(lower, "freelance"):
		return "contract"
	case strings.Contains(lower, "intern"):
		return "internship"
	}
	return strings.ToLower(strings.ReplaceAll(raw, " ", "_"))
}
