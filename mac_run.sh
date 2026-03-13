#!/bin/bash
# CareerScout — Native macOS Local Runner

echo "================================================"
echo "🚀 Starting CareerScout on macOS (M2 Native)"
echo "================================================"

mkdir -p bin
ln -sf "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" bin/chrome
export PATH=$(pwd)/bin:$PATH:/usr/local/go/bin
export DATABASE_URL="postgres://careerscout:careerscout_dev_password@localhost:5432/careerscout?sslmode=disable"
export REDPANDA_BROKERS="localhost:19092"
export ML_GRPC_ADDR="localhost:50051"

# We are testing with 10 concurrent headless Chromes
export TIER2_WORKERS=10
export TIER2_POOL_SIZE=10
export LOG_LEVEL="debug"

echo "[1/4] Stopping any old zombie processes..."
pkill -f "cmd/ingestion/main.go" || true
pkill -f "cmd/tier1/main.go" || true
pkill -f "cmd/tier2/main.go" || true
pkill -f "server.py" || true
# Kill any lingering headless Chromes from previous tests
pkill -f "Google Chrome Helper.*--headless" || true
sleep 2

echo "[2/4] Starting Local Docker DBs..."
docker compose up -d
sleep 3

echo "[3/4] Starting ML Classifier Server..."
(cd ml/classifier && PYTHONPATH=. nohup python3 server.py > ml_server.log 2>&1 &)
sleep 2

echo "[4/4] Starting Go Pipeline Workers (10x Chromium)..."
nohup go run cmd/ingestion/main.go > ingestion.log 2>&1 &
nohup go run cmd/tier1/main.go > tier1.log 2>&1 &
nohup go run cmd/tier2/main.go > tier2.log 2>&1 &

echo "✅ System is ONLINE."
echo ""
echo "Injecting 10 test URLs into the ingestion queue now..."

# Inject 10 massive tech companies to hit the scrapers
docker compose exec -T redpanda rpk topic produce urls.to_process -k "jobs.apple.com" <<< '{"url":"https://jobs.apple.com/en-us/search", "domain":"jobs.apple.com", "company_id":"00000000-0000-0000-0000-000000000000"}'
docker compose exec -T redpanda rpk topic produce urls.to_process -k "careers.microsoft.com" <<< '{"url":"https://careers.microsoft.com/v2/global/en/home.html", "domain":"careers.microsoft.com", "company_id":"00000000-0000-0000-0000-000000000001"}'
docker compose exec -T redpanda rpk topic produce urls.to_process -k "careers.google.com" <<< '{"url":"https://careers.google.com/jobs/results/", "domain":"careers.google.com", "company_id":"00000000-0000-0000-0000-000000000002"}'
docker compose exec -T redpanda rpk topic produce urls.to_process -k "amazon.jobs" <<< '{"url":"https://amazon.jobs/en/", "domain":"amazon.jobs", "company_id":"00000000-0000-0000-0000-000000000003"}'
docker compose exec -T redpanda rpk topic produce urls.to_process -k "metacareers.com" <<< '{"url":"https://www.metacareers.com/jobs", "domain":"metacareers.com", "company_id":"00000000-0000-0000-0000-000000000004"}'
docker compose exec -T redpanda rpk topic produce urls.to_process -k "jobs.netflix.com" <<< '{"url":"https://jobs.netflix.com/search", "domain":"jobs.netflix.com", "company_id":"00000000-0000-0000-0000-000000000005"}'
docker compose exec -T redpanda rpk topic produce urls.to_process -k "tesla.com" <<< '{"url":"https://www.tesla.com/careers/search", "domain":"tesla.com", "company_id":"00000000-0000-0000-0000-000000000006"}'
docker compose exec -T redpanda rpk topic produce urls.to_process -k "stripe.com" <<< '{"url":"https://stripe.com/jobs/search", "domain":"stripe.com", "company_id":"00000000-0000-0000-0000-000000000007"}'
docker compose exec -T redpanda rpk topic produce urls.to_process -k "airbnb.com" <<< '{"url":"https://careers.airbnb.com/positions/", "domain":"airbnb.com", "company_id":"00000000-0000-0000-0000-000000000008"}'
docker compose exec -T redpanda rpk topic produce urls.to_process -k "spotify.com" <<< '{"url":"https://lifeatspotify.com/jobs", "domain":"spotify.com", "company_id":"00000000-0000-0000-0000-000000000009"}'

echo "Done! Run '!cat tier2.log' to watch the API extraction in real-time."
