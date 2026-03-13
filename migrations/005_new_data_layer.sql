BEGIN;

CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE companies
    ADD COLUMN IF NOT EXISTS legal_name TEXT,
    ADD COLUMN IF NOT EXISTS industry TEXT,
    ADD COLUMN IF NOT EXISTS sub_industry TEXT,
    ADD COLUMN IF NOT EXISTS size_range TEXT,
    ADD COLUMN IF NOT EXISTS stage TEXT,
    ADD COLUMN IF NOT EXISTS hq_city TEXT,
    ADD COLUMN IF NOT EXISTS hq_country TEXT,
    ADD COLUMN IF NOT EXISTS hq_region TEXT,
    ADD COLUMN IF NOT EXISTS website TEXT,
    ADD COLUMN IF NOT EXISTS careers_page TEXT,
    ADD COLUMN IF NOT EXISTS linkedin_url TEXT,
    ADD COLUMN IF NOT EXISTS logo_url TEXT,
    ADD COLUMN IF NOT EXISTS description TEXT,
    ADD COLUMN IF NOT EXISTS founded_year INTEGER,
    ADD COLUMN IF NOT EXISTS data_quality SMALLINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS is_verified BOOLEAN DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS is_active BOOLEAN DEFAULT TRUE;

CREATE TABLE IF NOT EXISTS data_sources (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    public_ref      TEXT UNIQUE NOT NULL,
    company_id      UUID NOT NULL REFERENCES companies(id),
    discovery_method TEXT NOT NULL,
    discovery_tier  TEXT NOT NULL,
    ats_platform    TEXT,
    discovered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    discovered_by   TEXT DEFAULT 'system',
    endpoint_url    TEXT NOT NULL,
    endpoint_method TEXT DEFAULT 'GET',
    endpoint_headers JSONB,
    endpoint_body   TEXT,
    status          TEXT NOT NULL DEFAULT 'active',
    confidence      FLOAT NOT NULL DEFAULT 0.5,
    last_fetched_at  TIMESTAMPTZ,
    last_success_at  TIMESTAMPTZ,
    last_failure_at  TIMESTAMPTZ,
    fetch_count      INTEGER DEFAULT 0,
    success_count    INTEGER DEFAULT 0,
    failure_count    INTEGER DEFAULT 0,
    consecutive_failures INTEGER DEFAULT 0,
    fetch_interval_hours INTEGER DEFAULT 6,
    next_fetch_at   TIMESTAMPTZ DEFAULT NOW(),
    rate_limit_per_hour INTEGER DEFAULT 10,
    avg_response_ms  INTEGER,
    avg_job_count    INTEGER,
    last_job_count   INTEGER,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(endpoint_url)
);

CREATE INDEX IF NOT EXISTS idx_sources_company ON data_sources(company_id);
CREATE INDEX IF NOT EXISTS idx_sources_next_fetch ON data_sources(next_fetch_at) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_sources_platform ON data_sources(ats_platform);
CREATE INDEX IF NOT EXISTS idx_sources_method ON data_sources(discovery_method);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sources_endpoint ON data_sources(endpoint_url);

INSERT INTO data_sources (
    id,
    public_ref,
    company_id,
    discovery_method,
    discovery_tier,
    ats_platform,
    discovered_at,
    endpoint_url,
    status,
    confidence,
    created_at,
    updated_at
)
SELECT 
    id,
    'src_' || replace(id::text, '-', ''),
    company_id,
    CASE 
        WHEN tier_used = 'tier0' THEN 'ats_probe' 
        WHEN tier_used = 'tier2' THEN 'browser_intercept' 
        ELSE 'unknown' 
    END,
    tier_used,
    CASE
        WHEN api_url LIKE '%greenhouse.io%' THEN 'greenhouse'
        WHEN api_url LIKE '%lever.co%' THEN 'lever'
        WHEN api_url LIKE '%ashbyhq.com%' THEN 'ashbyhq'
        WHEN api_url LIKE '%workable.com%' THEN 'workable'
        WHEN api_url LIKE '%bamboohr.com%' THEN 'bamboohr'
        WHEN api_url LIKE '%recruitee.com%' THEN 'recruitee'
        WHEN api_url LIKE '%teamtailor.com%' THEN 'teamtailor'
        WHEN api_url LIKE '%rippling.com%' THEN 'rippling'
        WHEN api_url LIKE '%pinpointhq.com%' THEN 'pinpointhq'
        WHEN api_url LIKE '%freshteam.com%' THEN 'freshteam'
        WHEN api_url LIKE '%smartrecruiters.com%' THEN 'smartrecruiters'
        ELSE 'unknown'
    END,
    COALESCE(discovered_at, NOW()),
    api_url,
    CASE
        WHEN status = 'discovered' THEN 'active'
        WHEN status = 'pending' THEN 'unverified'
        ELSE 'retired'
    END,
    confidence,
    created_at,
    updated_at
FROM discovery_records
WHERE api_url IS NOT NULL
ON CONFLICT (endpoint_url) DO NOTHING;

ALTER TABLE IF EXISTS jobs RENAME TO jobs_old;

CREATE TABLE IF NOT EXISTS jobs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    company_id      UUID NOT NULL REFERENCES companies(id),
    source_id       UUID NOT NULL REFERENCES data_sources(id),
    external_id     TEXT NOT NULL,
    title           TEXT NOT NULL,
    title_normalized TEXT GENERATED ALWAYS AS (lower(regexp_replace(title, '[^a-zA-Z0-9\s]', '', 'g'))) STORED,
    description     TEXT,
    description_html TEXT,
    location_raw    TEXT,
    city            TEXT,
    state           TEXT,
    country         TEXT,
    country_code    TEXT,
    is_remote       BOOLEAN DEFAULT FALSE,
    remote_type     TEXT,
    department      TEXT,
    team            TEXT,
    employment_type TEXT,
    experience_level TEXT,
    salary_min      INTEGER,
    salary_max      INTEGER,
    salary_currency TEXT,
    salary_period   TEXT,
    apply_url       TEXT,
    job_page_url    TEXT,
    posted_at       TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_active       BOOLEAN DEFAULT TRUE,
    deactivated_at  TIMESTAMPTZ,
    data_quality    SMALLINT DEFAULT 0,
    raw_json        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(source_id, external_id)
);

CREATE INDEX IF NOT EXISTS idx_jobs_company ON jobs(company_id);
CREATE INDEX IF NOT EXISTS idx_jobs_source ON jobs(source_id);
CREATE INDEX IF NOT EXISTS idx_jobs_active ON jobs(is_active, first_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_location ON jobs(country_code, city);
CREATE INDEX IF NOT EXISTS idx_jobs_type ON jobs(employment_type);
CREATE INDEX IF NOT EXISTS idx_jobs_remote ON jobs(is_remote) WHERE is_remote = TRUE;
CREATE INDEX IF NOT EXISTS idx_jobs_first_seen ON jobs(first_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_posted ON jobs(posted_at DESC NULLS LAST);

CREATE INDEX IF NOT EXISTS idx_jobs_fts ON jobs USING gin(to_tsvector('english', coalesce(title,'') || ' ' || coalesce(department,'') || ' ' || coalesce(description,'')));
CREATE INDEX IF NOT EXISTS idx_jobs_trgm ON jobs USING gin(title_normalized gin_trgm_ops);

CREATE TABLE IF NOT EXISTS fetch_logs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    source_id       UUID NOT NULL REFERENCES data_sources(id),
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    duration_ms     INTEGER,
    http_status     INTEGER,
    jobs_found      INTEGER DEFAULT 0,
    jobs_new        INTEGER DEFAULT 0,
    jobs_updated    INTEGER DEFAULT 0,
    jobs_removed    INTEGER DEFAULT 0,
    error_message   TEXT,
    success         BOOLEAN NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fetch_logs_source ON fetch_logs(source_id, fetched_at DESC);

CREATE TABLE IF NOT EXISTS job_skills (
    job_id      UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    skill       TEXT NOT NULL,
    confidence  FLOAT DEFAULT 1.0,
    PRIMARY KEY (job_id, skill)
);

CREATE INDEX IF NOT EXISTS idx_skills_skill ON job_skills(skill);

COMMIT;
