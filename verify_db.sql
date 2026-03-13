-- Query 1: Table counts
SELECT 'companies' as tbl, COUNT(*) FROM companies
UNION ALL SELECT 'discovery_records', COUNT(*) FROM discovery_records
UNION ALL SELECT 'data_sources', COUNT(*) FROM data_sources
UNION ALL SELECT 'jobs', COUNT(*) FROM jobs
UNION ALL SELECT 'fetch_logs', COUNT(*) FROM fetch_logs
UNION ALL SELECT 'job_skills', COUNT(*) FROM job_skills;

-- Query 2: Migration fidelity — data_sources must match discovery_records count
SELECT 
    (SELECT COUNT(*) FROM discovery_records WHERE status='discovered' AND api_url IS NOT NULL) as old_discovered,
    (SELECT COUNT(*) FROM data_sources WHERE status='active') as new_active,
    (SELECT COUNT(*) FROM discovery_records WHERE status='discovered' AND api_url IS NOT NULL) = 
    (SELECT COUNT(*) FROM data_sources WHERE status='active') as counts_match;

-- Query 3: ATS platform breakdown
SELECT ats_platform, COUNT(*) as count
FROM data_sources
WHERE status = 'active'
GROUP BY ats_platform
ORDER BY count DESC;

-- Query 4: Sample data_sources row — confirm public_ref and all fields populated
SELECT id, public_ref, ats_platform, discovery_method, 
       discovery_tier, status, confidence, endpoint_url
FROM data_sources 
WHERE status = 'active'
LIMIT 3;
