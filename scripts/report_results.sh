#!/usr/bin/env bash
set -e

echo "=== CareerScout Discovery Report ==="

echo -e "\n[1] Total Companies with Status: Discovered"
docker exec careerscout-postgres psql -U careerscout -d careerscout -t -c "SELECT COUNT(*) FROM discovery_records WHERE status = 'discovered';"

echo -e "\n[2] Total Companies with Status: Failed"
docker exec careerscout-postgres psql -U careerscout -d careerscout -t -c "SELECT COUNT(*) FROM discovery_records WHERE status = 'failed';"

echo -e "\n[3] Total Companies with Status: Pending"
docker exec careerscout-postgres psql -U careerscout -d careerscout -t -c "SELECT COUNT(*) FROM companies c LEFT JOIN discovery_records dr ON c.id = dr.company_id WHERE dr.status IS NULL OR dr.status = 'pending';"

echo -e "\n[4] Top 30 Discovered APIs (Ordered by Confidence)"
docker exec careerscout-postgres psql -U careerscout -d careerscout -c "SELECT confidence, domain, api_url FROM discovery_records WHERE status = 'discovered' ORDER BY confidence DESC LIMIT 30;"

echo "===================================="
