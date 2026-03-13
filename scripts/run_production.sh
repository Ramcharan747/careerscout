#!/usr/bin/env bash
set -e

# CareerScout 8GB Mac Production Validation Script

# Automatic backup before any data-modifying operation
mkdir -p backups
PGPASSWORD=careerscout_dev_password pg_dump -h 127.0.0.1 -U careerscout -d careerscout > backups/backup_$(date +%Y%m%d_%H%M%S).sql && echo "Backup saved"

# Discovery preservation guard — makes disappearing discoveries immediately visible
DISCOVERY_COUNT=$(PGPASSWORD=careerscout_dev_password psql -h 127.0.0.1 -U careerscout -d careerscout -t -c "SELECT COUNT(*) FROM discovery_records WHERE status = 'discovered';")
DISCOVERY_COUNT=$(echo "$DISCOVERY_COUNT" | xargs)  # trim whitespace
echo "Existing discoveries before run: $DISCOVERY_COUNT"
if [ "$DISCOVERY_COUNT" -gt "0" ]; then
    echo "✅ $DISCOVERY_COUNT discoveries preserved from previous runs"
fi

# Database connection (Docker Postgres on localhost)
export PGHOST=127.0.0.1
export PGPORT=5432
export PGUSER=careerscout
export PGPASSWORD=careerscout_dev_password
export PGDATABASE=careerscout
export DATABASE_URL="postgres://${PGUSER}:${PGPASSWORD}@${PGHOST}:${PGPORT}/${PGDATABASE}?sslmode=disable"


# --- CAPTURE MODE (uncomment to run capture-only pass) ---
# CAPTURE_MODE=capture go run ./cmd/capture_run/main.go
export WORKER_COUNT=50
export BROWSER_TABS=6
export DB_POOL_SIZE=20
export DNS_CONCURRENCY=8
export FRONTIER_MAX=10000
export POLITENESS_DELAY_MS=1500
export BATCH_SIZE=0
export MIN_CONFIDENCE=0.60
export FEEDBACK_STATE_PATH=./feedback.json
export INPUT_MODE=postgres
export CAPTURE_PATH=./logs/capture_$(date +%Y%m%d_%H%M%S).ndjson

echo "=== CareerScout Production Configuration ==="
echo "WORKER_COUNT=$WORKER_COUNT"
echo "BROWSER_TABS=$BROWSER_TABS"
echo "DB_POOL_SIZE=$DB_POOL_SIZE"
echo "DNS_CONCURRENCY=$DNS_CONCURRENCY"
echo "FRONTIER_MAX=$FRONTIER_MAX"
echo "POLITENESS_DELAY_MS=$POLITENESS_DELAY_MS"
echo "BATCH_SIZE=$BATCH_SIZE"
echo "MIN_CONFIDENCE=$MIN_CONFIDENCE"
echo "INPUT_MODE=$INPUT_MODE"
echo "CAPTURE_PATH=$CAPTURE_PATH"
echo "============================================"

read -p "Press Enter to continue or Ctrl+C to abort..."

mkdir -p logs

echo "Seeding career URLs..."
go run ./cmd/seed/main.go careers_urls.json

LOG_FILE="./logs/discover_$(date +%Y%m%d_%H%M%S).log"
echo "Running cmd/discover... Logging to terminal and $LOG_FILE"
go run ./cmd/discover/main.go 2>&1 | tee "$LOG_FILE"

ANALYSIS_FILE="./logs/analysis_$(date +%Y%m%d_%H%M%S).txt"
echo "Running post-run analysis against capture file..."
go run ./cmd/analyse/main.go --file "$CAPTURE_PATH" > "$ANALYSIS_FILE"
echo "Analysis written to $ANALYSIS_FILE"
