#!/bin/bash
# CareerScout — Universal Colab / Ubuntu Setup Script
# Run this script on Google Colab or any fresh Ubuntu VM to install Go, Redpanda,
# Postgres, and boot the CareerScout Tier 1 and Tier 2 pipeline to collect data.
#
# Usage in Google Colab (run in a notebook cell):
#   !bash careerscout_colab_setup.sh

set -e

echo "================================================"
echo "🚀 CareerScout Ubuntu/Colab Bootstrapper"
echo "================================================"

# 1. Update and install basic dependencies
echo "[1/6] Installing dependencies..."
sudo apt-get update -y
sudo apt-get install -y wget curl git postgresql postgresql-contrib

# 2. Install Go (v1.22.1)
echo "[2/6] Installing Go 1.22..."
if ! command -v go &> /dev/null; then
    wget -q https://go.dev/dl/go1.22.1.linux-amd64.tar.gz
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf go1.22.1.linux-amd64.tar.gz
    rm go1.22.1.linux-amd64.tar.gz
fi
export PATH=$PATH:/usr/local/go/bin
echo "Go version: $(go version)"

# 3. Setup PostgreSQL
echo "[3/6] Setting up PostgreSQL..."
sudo service postgresql start
sudo -u postgres psql -c "CREATE USER careerscout WITH PASSWORD 'careerscout_dev_password';" || true
sudo -u postgres psql -c "CREATE DATABASE careerscout OWNER careerscout;" || true
sudo -u postgres psql -c "ALTER USER careerscout CREATEDB;" || true

# 4. Install Redpanda locally via binary (Docker is tough in Colab)
echo "[4/6] Installing Redpanda via rpk..."
if ! command -v rpk &> /dev/null; then
    curl -1sLf 'https://dl.redpanda.com/nzc4ZYQK3WRGd9sy/redpanda/cfg/setup/bash.deb.sh' | sudo -E bash
    sudo apt install redpanda -y
fi
sudo systemctl start redpanda || sudo rpk redpanda start --mode dev-container &
sleep 5 # Wait for Redpanda to start

echo "[5/6] Creating Redpanda Topics..."
rpk topic create urls.to_process --brokers localhost:9092 || true
rpk topic create urls.tier1_queue --brokers localhost:9092 || true
rpk topic create urls.tier2_queue --brokers localhost:9092 || true
rpk topic create urls.tier3_queue --brokers localhost:9092 || true
rpk topic create apis.discovered  --brokers localhost:9092 || true
rpk topic create apis.failed      --brokers localhost:9092 || true
rpk topic create jobs.raw         --brokers localhost:9092 || true

echo "[6/6] Environment Setup Complete!"
echo "================================================"
echo "To run CareerScout inside Colab/Ubuntu:"
echo ""
echo "1. Export environment variables:"
echo "   export PATH=\$PATH:/usr/local/go/bin"
echo "   export DATABASE_URL=\"postgres://careerscout:careerscout_dev_password@localhost:5432/careerscout?sslmode=disable\""
echo "   export REDPANDA_BROKERS=\"localhost:9092\""
echo ""
echo "2. Run DB Migration:"
echo "   psql \$DATABASE_URL -f schema/001_initial.sql"
echo ""
echo "3. Seed DB with 100+ URLs:"
echo "   psql \$DATABASE_URL -f data/seed_100_urls.sql"
echo ""
echo "4. Start microservices (in background or separate terminals):"
echo "   nohup go run cmd/ingestion/main.go &"
echo "   nohup go run cmd/tier1/main.go &"
echo "   nohup go run cmd/tier2/main.go &"
echo "================================================"
