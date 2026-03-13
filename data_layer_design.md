# CareerScout — Data Layer Design
## "Structured, secure, extensible"

---

## THE CORE PROBLEM

You have three ways to discover job data:
1. Direct ATS API (tier0) — highest quality, most reliable
2. Browser interception (tier2) — medium quality, fragile
3. HTML scraping (tier3, not built yet) — lowest quality, breaks often

Each method produces an API endpoint or data source with different:
- Reliability (does it break? does it rotate?)
- Freshness (how often can you call it?)
- Authentication requirements
- Rate limits
- Response format

You need ONE unified system that:
- Hides HOW you get the data (internal concern)
- Exposes WHAT the data is (public concern)
- Tracks quality and reliability over time
- Assigns stable IDs so your frontend never sees API URLs

---

## THE ID SYSTEM

Every data source gets a stable opaque ID. Example:

```
src_01HXKJ2M3N4P5Q6R7S8T9U0V  ← This is what your frontend uses
```

Nobody looking at this ID can tell:
- Which ATS platform it came from
- What the actual API URL is
- How you discovered it

The mapping lives only in your database, never exposed via API.

---

## COMPLETE DATABASE SCHEMA

### Table 1 — companies
The real-world entity. One row per company.

```sql
CREATE TABLE companies (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    
    -- Identity
    slug            TEXT UNIQUE NOT NULL,  -- "stripe", "airbnb"
    name            TEXT NOT NULL,
    legal_name      TEXT,                  -- "Stripe, Inc."
    
    -- Classification  
    industry        TEXT,                  -- "fintech", "devtools"
    sub_industry    TEXT,
    size_range      TEXT,                  -- "1-10","11-50","51-200","201-1000","1000+"
    stage           TEXT,                  -- "startup","growth","enterprise","public"
    
    -- Location
    hq_city         TEXT,
    hq_country      TEXT,
    hq_region       TEXT,                  -- "north-america","europe","india","apac"
    
    -- Online presence
    website         TEXT,
    careers_page    TEXT,                  -- their actual careers URL
    linkedin_url    TEXT,
    
    -- Metadata
    logo_url        TEXT,
    description     TEXT,
    founded_year    INTEGER,
    
    -- Quality signals
    data_quality    SMALLINT DEFAULT 0,    -- 0-100 score
    is_verified     BOOLEAN DEFAULT FALSE, -- manually verified
    is_active       BOOLEAN DEFAULT TRUE,
    
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### Table 2 — data_sources
THIS IS THE KEY TABLE. One row per discovered API/method.
Internal only — never exposed via public API.

```sql
CREATE TABLE data_sources (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    
    -- Stable public reference (never expose the actual API URL)
    public_ref      TEXT UNIQUE NOT NULL,  -- "src_XXXX" opaque ID
    
    -- Linkage
    company_id      UUID NOT NULL REFERENCES companies(id),
    
    -- Discovery metadata — HOW we found this
    discovery_method TEXT NOT NULL,        
    -- VALUES: 'ats_probe','browser_intercept','html_scrape','manual','api_partner'
    
    discovery_tier  TEXT NOT NULL,         
    -- VALUES: 'tier0','tier1','tier2','tier3'
    
    ats_platform    TEXT,                  
    -- VALUES: 'greenhouse','lever','ashby','workable','bamboohr',
    --         'recruitee','teamtailor','rippling','pinpoint',
    --         'freshteam','smartrecruiters','workday','icims',
    --         'custom','unknown'
    
    discovered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    discovered_by   TEXT DEFAULT 'system', -- 'system','manual','partner'
    
    -- The actual endpoint — NEVER exposed publicly
    endpoint_url    TEXT NOT NULL,         -- full API URL
    endpoint_method TEXT DEFAULT 'GET',    -- GET or POST
    endpoint_headers JSONB,               -- auth headers if any
    endpoint_body   TEXT,                 -- request body for POST
    
    -- Reliability tracking
    status          TEXT NOT NULL DEFAULT 'active',
    -- VALUES: 'active','degraded','failed','retired','unverified'
    
    confidence      FLOAT NOT NULL DEFAULT 0.5,  -- 0.0 to 1.0
    
    last_fetched_at  TIMESTAMPTZ,
    last_success_at  TIMESTAMPTZ,
    last_failure_at  TIMESTAMPTZ,
    
    fetch_count      INTEGER DEFAULT 0,
    success_count    INTEGER DEFAULT 0,
    failure_count    INTEGER DEFAULT 0,
    consecutive_failures INTEGER DEFAULT 0,
    
    -- Scheduling
    fetch_interval_hours INTEGER DEFAULT 6,
    next_fetch_at   TIMESTAMPTZ DEFAULT NOW(),
    
    -- Rate limiting
    rate_limit_per_hour INTEGER DEFAULT 10,
    
    -- Response metadata
    avg_response_ms  INTEGER,
    avg_job_count    INTEGER,
    last_job_count   INTEGER,
    
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Indexes
CREATE INDEX idx_sources_company ON data_sources(company_id);
CREATE INDEX idx_sources_next_fetch ON data_sources(next_fetch_at) 
    WHERE status = 'active';
CREATE INDEX idx_sources_platform ON data_sources(ats_platform);
CREATE INDEX idx_sources_method ON data_sources(discovery_method);
CREATE UNIQUE INDEX idx_sources_endpoint ON data_sources(endpoint_url);
```

### Table 3 — jobs
The actual job listings. Public-facing data.

```sql
CREATE TABLE jobs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    
    -- Linkage (use source_id not endpoint URL)
    company_id      UUID NOT NULL REFERENCES companies(id),
    source_id       UUID NOT NULL REFERENCES data_sources(id),
    
    -- External reference (ATS job ID, never the API URL)
    external_id     TEXT NOT NULL,
    
    -- Core fields
    title           TEXT NOT NULL,
    title_normalized TEXT,                 -- lowercase, no special chars, for dedup
    description     TEXT,
    description_html TEXT,
    
    -- Location
    location_raw    TEXT,                  -- exactly as returned by API
    city            TEXT,                  -- parsed
    state           TEXT,
    country         TEXT,
    country_code    TEXT,                  -- ISO 3166 "IN","US","GB"
    is_remote       BOOLEAN DEFAULT FALSE,
    remote_type     TEXT,                  -- 'fully_remote','hybrid','onsite'
    
    -- Classification
    department      TEXT,
    team            TEXT,
    employment_type TEXT,                  -- 'full_time','part_time','contract','internship'
    experience_level TEXT,                 -- 'entry','mid','senior','lead','executive'
    
    -- Compensation
    salary_min      INTEGER,               -- in USD always, convert at fetch time
    salary_max      INTEGER,
    salary_currency TEXT,
    salary_period   TEXT,                  -- 'annual','monthly','hourly'
    
    -- URLs (public, safe to expose)
    apply_url       TEXT,                  -- direct link to apply
    job_page_url    TEXT,                  -- job detail page
    
    -- Timestamps
    posted_at       TIMESTAMPTZ,           -- when company posted it
    expires_at      TIMESTAMPTZ,           -- when job listing expires
    first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),  -- KEY: when WE first saw it
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),  -- last time we confirmed active
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),  -- last fetch attempt
    
    -- Status
    is_active       BOOLEAN DEFAULT TRUE,
    deactivated_at  TIMESTAMPTZ,           -- when we stopped seeing it
    
    -- Quality
    data_quality    SMALLINT DEFAULT 0,    -- 0-100
    
    -- Raw data (internal, for debugging)
    raw_json        JSONB,
    
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    UNIQUE(source_id, external_id)
);

-- Indexes for fast queries
CREATE INDEX idx_jobs_company ON jobs(company_id);
CREATE INDEX idx_jobs_source ON jobs(source_id);
CREATE INDEX idx_jobs_active ON jobs(is_active, first_seen_at DESC);
CREATE INDEX idx_jobs_location ON jobs(country_code, city);
CREATE INDEX idx_jobs_type ON jobs(employment_type);
CREATE INDEX idx_jobs_remote ON jobs(is_remote) WHERE is_remote = TRUE;
CREATE INDEX idx_jobs_first_seen ON jobs(first_seen_at DESC);
CREATE INDEX idx_jobs_posted ON jobs(posted_at DESC NULLS LAST);

-- Full text search index
CREATE INDEX idx_jobs_fts ON jobs 
    USING gin(to_tsvector('english', 
        coalesce(title,'') || ' ' || 
        coalesce(department,'') || ' ' ||
        coalesce(description,'')
    ));
```

### Table 4 — fetch_logs
Every fetch attempt logged for debugging and analytics.

```sql
CREATE TABLE fetch_logs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    source_id       UUID NOT NULL REFERENCES data_sources(id),
    
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    duration_ms     INTEGER,
    http_status     INTEGER,
    
    jobs_found      INTEGER DEFAULT 0,
    jobs_new        INTEGER DEFAULT 0,     -- first time we see this job
    jobs_updated    INTEGER DEFAULT 0,     -- job changed since last fetch
    jobs_removed    INTEGER DEFAULT 0,     -- job no longer in response
    
    error_message   TEXT,
    success         BOOLEAN NOT NULL
);

CREATE INDEX idx_fetch_logs_source ON fetch_logs(source_id, fetched_at DESC);
```

### Table 5 — job_skills (extracted automatically)
```sql
CREATE TABLE job_skills (
    job_id      UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    skill       TEXT NOT NULL,
    confidence  FLOAT DEFAULT 1.0,        -- how sure we are this skill is required
    PRIMARY KEY (job_id, skill)
);

CREATE INDEX idx_skills_skill ON job_skills(skill);
```

---

## THE PUBLIC_REF SYSTEM

The `public_ref` in `data_sources` is generated like this:

```go
func generatePublicRef() string {
    // Generates: "src_01HXKJ2M3N4P5Q6R7S8T9U0V"
    // Uses ULID — sortable, unique, opaque
    return "src_" + ulid.Make().String()
}
```

Your public API returns `source_ref` not `source_id` not `api_url`.

Frontend gets: `{"source_ref": "src_01HXKJ2M", "company": "Stripe", "title": "Engineer"}`
Nobody can reverse-engineer the API URL from this.

---

## MIGRATION FROM CURRENT SCHEMA

Your current tables:
- `companies` — migrate directly, add new columns
- `discovery_records` — migrate to `data_sources`, map tier_used
- `raw_captures` — archive, not needed in new schema
- `jobs` — create fresh

Migration plan:
1. Create new tables alongside old ones
2. Copy data from discovery_records → data_sources
3. Keep old tables as backup for 30 days
4. Switch application code to new tables
5. Drop old tables

---

## SUMMARY

Old system: discovery_records is a mix of discovery metadata AND operational data
New system: clean separation:
- data_sources = HOW you get data (internal, secured)
- jobs = WHAT the data is (public)
- companies = WHO the data is about (public)
- fetch_logs = audit trail (internal)

