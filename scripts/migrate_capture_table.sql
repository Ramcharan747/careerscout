CREATE TABLE IF NOT EXISTS raw_captures (
    id BIGSERIAL PRIMARY KEY,
    run_id TEXT NOT NULL,
    domain TEXT NOT NULL,
    url TEXT NOT NULL,
    method TEXT,
    request_headers JSONB,
    response_status INTEGER,
    response_content_type TEXT,
    response_body TEXT,
    response_size INTEGER,
    captured_at TIMESTAMPTZ DEFAULT NOW(),
    label INTEGER DEFAULT NULL,
    notes TEXT DEFAULT NULL
);

CREATE INDEX IF NOT EXISTS idx_raw_captures_domain ON raw_captures(domain);
CREATE INDEX IF NOT EXISTS idx_raw_captures_run_id ON raw_captures(run_id);
CREATE INDEX IF NOT EXISTS idx_raw_captures_label ON raw_captures(label) WHERE label IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_raw_captures_url ON raw_captures(url);
