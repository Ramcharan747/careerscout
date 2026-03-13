#!/usr/bin/env bash
set -e

# Reset only genuinely failed domains back to pending.
# This NEVER touches records that have status='discovered' — those are preserved.
# Only re-queues domains that failed and have no valid API URL.

PGPASSWORD=careerscout_dev_password psql -h 127.0.0.1 -U careerscout -d careerscout <<'SQL'
UPDATE discovery_records 
SET status = 'pending', consecutive_failures = 0, last_error = NULL, updated_at = NOW()
WHERE status = 'failed' 
AND (api_url IS NULL OR confidence IS NULL);
SQL

echo "Reset failed domains to pending (discoveries preserved)."
PGPASSWORD=careerscout_dev_password psql -h 127.0.0.1 -U careerscout -d careerscout -c "SELECT status, COUNT(*) FROM discovery_records GROUP BY status ORDER BY count DESC;"
