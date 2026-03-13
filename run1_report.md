## 1. Run Configuration

- **Hardware**: 8GB Mac
- **Duration**: ~1 minute 15 seconds (22:10:08 to 22:11:23)
- **Environment Variables**:
  - `WORKER_COUNT`: 50
  - `BROWSER_TABS`: 6
  - `DB_POOL_SIZE`: 20
  - `DNS_CONCURRENCY`: 8
  - `FRONTIER_MAX`: 10000
  - `POLITENESS_DELAY_MS`: 1500
  - `CAPTURE_PATH`: ./run1_capture.ndjson
  - `INPUT_MODE`: postgres

## 2. URL Inventory

*Based on validate_urls against `careers_urls.json`*

- **Total URLs Loaded**: 1000
- **Reachable**: 360
- **Redirects**: 388
- **Dead**: 248
- **Timeout**: 4

## 3. Discovery Results

- **Processed Domains**: 50 (Bounded inherently by the static `LIMIT 50` query in `cmd/discover/main.go`)
- **Total APIs Discovered**: 29
- **Total Failed**: 17
- **Pending at Termination**: 783 (Includes items loaded from previous local testing state plus the 661 net-new inserts)
- **Discovery Rate**: 56.0% (28 discoveries reported in termination logs / 50 reachable domains processed)

## 4. Classifier Performance

- **Total Requests Intercepted by Tier 2**: 346
- **Hit Rate**: 44.2% (153 individual candidate hits out of 346 request intercepts)
- **Near-Miss Rate**: 0.0% (0 near-misses with body score > 0.25 exclusively out of 346 intercepts)
- **Top 10 Discovered API URLs**:
  1. `https://api.greenhouse.io/v1/boards/robinhood/jobs` (Confidence: 0.85)
  2. `https://api.greenhouse.io/v1/boards/discord/jobs?content=true` (Confidence: 0.75)
  3. `https://jobs.ashbyhq.com/api/non-user-graphql?op=ApiJobBoardWithTeams` (Confidence: 0.75)
  4. `https://sxcontent9668.azureedge.us/cms-assets/job_posts.json` (Confidence: 0.65)
  5. `https://careers.cisco.com/widgets` (Confidence: 0.60)
  6. `https://graphql.contentful.com/content/v1/spaces/kp51zybwznx4/environments/master` (Confidence: 0.38)
  7. `https://www.databricks.com/careers-assets/page-data/company/careers/page-data.json` (Confidence: 0.35)
  8. `https://www.atlassian.com/gateway/api/graphql` (Confidence: 0.30)
  9. `https://exp.notion.so/v1/initialize?...` (Confidence: 0.30)
  10. `https://cdn.cookielaw.org/consent/33c336c7...` (Confidence: 0.25)

## 5. Near-Miss Analysis

No near-misses were detected during this specific 50-domain run batch. All intercepted payloads that produced a structural body score > 0.25 breached the minimum hit threshold logic universally, triggering an official hit rather than settling into a near-miss bracket.

## 6. Observations and Anomalies

- **Execution Panic on First Start**: `cmd/discover` initially suffered a `nil pointer dereference` panic due to a null `http.Client` injected into rod's `LoadResponse()`. This was isolated to my addition in the immediate previous session. Utilizing `http.DefaultClient` mitigated the issue perfectly and allowed successful, stable interception.
- **Process Saturation Limit**: Exactly 50 domains were executed. This traces directly back to the `LIMIT $1` parameterized variable inside `loadCompanies` mapped statically to `50` in Postgres execution mode, leaving the rest of the 783 queue unattempted.
- **False Positives with `.cookielaw.org`**: Several domains (including Zoom, Slack, Plaid, Asana, Netflix, Reddit, Sony) intercepted JSON configurations from `cdn.cookielaw.org` and scored a 0.25 hit. This implies the combination of `.json` file types paired with specific field keywords or size arrays successfully triggered the body classifier logic without being related to job feeds.

## 7. Recommended Next Actions

