CREATE TABLE IF NOT EXISTS near_misses (
    id BIGSERIAL PRIMARY KEY,
    run_id TEXT NOT NULL,
    domain TEXT NOT NULL,
    url TEXT NOT NULL,
    response_body TEXT,
    response_size INTEGER,
    url_score FLOAT,
    body_score FLOAT,
    final_confidence FLOAT,
    label INTEGER DEFAULT NULL,
    captured_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (run_id, url)
);

CREATE INDEX IF NOT EXISTS idx_near_misses_run_id ON near_misses(run_id);
CREATE INDEX IF NOT EXISTS idx_near_misses_label ON near_misses(label) WHERE label IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_near_misses_confidence ON near_misses(final_confidence DESC);
