-- CareerScout — Initial Schema Migration
-- Migration: 001_initial
-- Run automatically by docker-entrypoint-initdb.d on first container start

-- ============================================================
-- EXTENSIONS
-- ============================================================
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pg_trgm";

-- ============================================================
-- COMPANIES
-- ============================================================
CREATE TABLE IF NOT EXISTS companies (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    domain          TEXT NOT NULL UNIQUE,
    name            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_companies_domain ON companies USING btree(domain);

-- ============================================================
-- DISCOVERY RECORDS
-- The heart of the system. One record per company domain.
-- ============================================================
CREATE TYPE discovery_tier AS ENUM ('tier1', 'tier2', 'tier3');
CREATE TYPE discovery_status AS ENUM ('pending', 'discovered', 'failed', 'stale');

CREATE TABLE IF NOT EXISTS discovery_records (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    domain          TEXT NOT NULL UNIQUE,

    -- Captured API details
    api_url         TEXT,
    http_method     TEXT,
    request_headers JSONB,
    request_body    TEXT,

    -- Discovery metadata
    tier_used       discovery_tier,
    status          discovery_status NOT NULL DEFAULT 'pending',
    confidence      FLOAT,
    discovered_at   TIMESTAMPTZ,
    last_replayed   TIMESTAMPTZ,
    next_replay     TIMESTAMPTZ,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_discovery_domain  ON discovery_records USING btree(domain);
CREATE INDEX idx_discovery_status  ON discovery_records USING btree(status);
CREATE INDEX idx_discovery_tier    ON discovery_records USING btree(tier_used);
CREATE INDEX idx_discovery_next_replay ON discovery_records USING btree(next_replay)
    WHERE status = 'discovered';

-- ============================================================
-- JOBS (canonical normalised schema)
-- ============================================================
CREATE TABLE IF NOT EXISTS jobs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    external_job_id TEXT NOT NULL,
    title           TEXT NOT NULL,
    location        TEXT,
    salary_min      NUMERIC,
    salary_max      NUMERIC,
    salary_currency TEXT,
    salary_raw      TEXT,
    posted_at       TIMESTAMPTZ,
    apply_url       TEXT,
    raw_json        JSONB,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(company_id, external_job_id)
);

CREATE INDEX idx_jobs_company     ON jobs USING btree(company_id);
CREATE INDEX idx_jobs_posted_at   ON jobs USING btree(posted_at DESC);
CREATE INDEX idx_jobs_is_active   ON jobs USING btree(is_active);
CREATE INDEX idx_jobs_title_trgm  ON jobs USING gin(title gin_trgm_ops);
CREATE INDEX idx_jobs_location_trgm ON jobs USING gin(location gin_trgm_ops);

-- ============================================================
-- API PAYLOAD ARCHIVE (pointer to S3, not the payload itself)
-- ============================================================
CREATE TABLE IF NOT EXISTS api_payload_archive (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    domain          TEXT NOT NULL,
    s3_bucket       TEXT NOT NULL,
    s3_key          TEXT NOT NULL,
    captured_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tier_used       discovery_tier,
    byte_size       BIGINT
);

CREATE INDEX idx_archive_domain ON api_payload_archive USING btree(domain);

-- ============================================================
-- UPDATED_AT TRIGGER FUNCTION
-- ============================================================
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_companies_updated_at
    BEFORE UPDATE ON companies
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER trg_discovery_updated_at
    BEFORE UPDATE ON discovery_records
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER trg_jobs_updated_at
    BEFORE UPDATE ON jobs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
