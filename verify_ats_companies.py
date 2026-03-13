#!/usr/bin/env python3
"""
ATS Company Verifier — Probes each company slug against its ATS API to check if
the company still has an active career page with job listings.

Runs concurrently for speed. Outputs verified companies to separate files.

Usage: python3 verify_ats_companies.py [--workers 50] [--ats greenhouse]
"""
import urllib.request, ssl, json, os, sys, time
from concurrent.futures import ThreadPoolExecutor, as_completed
from collections import defaultdict
import threading

ctx = ssl.create_default_context()

def log(msg):
    print(msg, flush=True)

# ─── ATS API probe endpoints ────────────────────────────────────────────────

def probe_greenhouse(slug):
    url = f"https://boards-api.greenhouse.io/v1/boards/{slug}/jobs?content=true"
    data = _get_json(url)
    if data is None: return None
    return isinstance(data, dict) and len(data.get("jobs", [])) > 0

def probe_lever(slug):
    url = f"https://api.lever.co/v0/postings/{slug}?limit=1&mode=json"
    data = _get_json(url)
    if data is None: return None
    return isinstance(data, list) and len(data) > 0

def probe_ashby(slug):
    """Ashby uses GraphQL. Matches Go prober: uses jobPostings field, not jobs on teams."""
    body = json.dumps({
        "operationName": "ApiJobBoardWithTeams",
        "variables": {"organizationHostedJobsPageName": slug},
        "query": "query ApiJobBoardWithTeams($organizationHostedJobsPageName: String!) { jobBoardWithTeams(organizationHostedJobsPageName: $organizationHostedJobsPageName) { teams { name } jobPostings { id title } } }"
    })
    data = _post_json("https://jobs.ashbyhq.com/api/non-user-graphql?op=ApiJobBoardWithTeams", body)
    if data is None: return None
    errors = data.get("errors", [])
    if errors:
        for e in errors:
            msg = e.get("message", "").lower()
            if "not found" in msg or "does not exist" in msg:
                return False
        return None
    board = data.get("data", {}).get("jobBoardWithTeams")
    if board is None:
        return False
    return True  # Board exists = confirmed Ashby user

def probe_workable(slug):
    url = f"https://apply.workable.com/api/v3/accounts/{slug}/jobs?limit=1"
    data = _get_json(url)
    if data is None: return None
    return isinstance(data, dict) and len(data.get("results", [])) > 0

def probe_smartrecruiters(slug):
    # Matches Go prober: api.smartrecruiters.com/v1/companies/{slug}/postings
    url = f"https://api.smartrecruiters.com/v1/companies/{slug}/postings"
    data = _get_json(url)
    if data is None: return None
    if isinstance(data, dict) and "content" in data:
        content = data.get("content", [])
        if isinstance(content, list) and len(content) > 0:
            return True
    return False

def probe_recruitee(slug):
    url = f"https://{slug}.recruitee.com/api/offers"
    data = _get_json(url)
    if data is None: return None
    return isinstance(data, dict) and len(data.get("offers", [])) > 0

def probe_freshteam(slug):
    # Matches Go prober: /hire/widgets/jobs.json, checks for "title","id","remote"
    url = f"https://{slug}.freshteam.com/hire/widgets/jobs.json"
    data = _get_json(url, raw=True)
    if data is None: return None
    if isinstance(data, str):
        return '"title"' in data and '"id"' in data and '"remote"' in data and len(data) > 500
    return False

def probe_bamboohr(slug):
    # Matches Go prober: /jobs/embed2.php, checks for "jobOpenings" in response
    url = f"https://{slug}.bamboohr.com/jobs/embed2.php"
    data = _get_json(url, raw=True)
    if data is None: return None
    if isinstance(data, str):
        return '"jobOpenings"' in data
    return False

def probe_teamtailor(slug):
    # Matches Go prober: jobs.teamtailor.com/companies/{slug}/jobs.json
    url = f"https://jobs.teamtailor.com/companies/{slug}/jobs.json"
    data = _get_json(url)
    if data is None: return None
    if isinstance(data, dict):
        d = data.get("data")
        if isinstance(d, list) and len(d) > 0:
            return True
    return False

def probe_breezy(slug):
    url = f"https://{slug}.breezy.hr/json"
    data = _get_json(url)
    if data is None: return None
    return isinstance(data, list) and len(data) > 0

def probe_pinpoint(slug):
    url = f"https://{slug}.pinpointhq.com/postings.json?per_page=1"
    data = _get_json(url)
    if data is None: return None
    return isinstance(data, dict) and len(data.get("data", [])) > 0

def probe_rippling(slug):
    # Matches Go prober: api.rippling.com/platform/api/ats/v1/board/{slug}-careers/jobs
    url = f"https://api.rippling.com/platform/api/ats/v1/board/{slug}-careers/jobs"
    data = _get_json(url, raw=True)
    if data is None: return None
    if isinstance(data, str):
        return '"id"' in data and '"name"' in data and '"department"' in data and len(data) > 200
    return False


# ─── HTTP helpers with retry ─────────────────────────────────────────────────

def _get_json(url, retries=2, raw=False):
    """GET JSON with retry on rate limits. If raw=True, return raw string."""
    for attempt in range(retries + 1):
        try:
            req = urllib.request.Request(url, headers={
                "User-Agent": "Mozilla/5.0 (compatible; CareerScout/1.0)",
                "Accept": "application/json",
            })
            resp = urllib.request.urlopen(req, timeout=10, context=ctx)
            body = resp.read().decode()
            if raw:
                return body
            return json.loads(body)
        except urllib.error.HTTPError as e:
            if e.code in (404, 410):
                return {"_dead": True} if not raw else None
            if e.code in (429, 503) and attempt < retries:
                time.sleep(1 + attempt)
                continue
            return None
        except:
            if attempt < retries:
                time.sleep(0.5)
                continue
            return None
    return None

def _post_json(url, body, retries=2):
    """POST JSON with retry on rate limits."""
    for attempt in range(retries + 1):
        try:
            req = urllib.request.Request(url, headers={
                "User-Agent": "Mozilla/5.0 (compatible; CareerScout/1.0)",
                "Accept": "application/json",
                "Content-Type": "application/json",
            }, data=body.encode(), method="POST")
            resp = urllib.request.urlopen(req, timeout=10, context=ctx)
            return json.loads(resp.read().decode())
        except urllib.error.HTTPError as e:
            if e.code in (404, 410):
                return {"_dead": True, "errors": [{"message": "not found"}]}
            if e.code in (429, 503) and attempt < retries:
                time.sleep(1 + attempt)
                continue
            return None
        except:
            if attempt < retries:
                time.sleep(0.5)
                continue
            return None
    return None


# ─── Probe dispatcher ────────────────────────────────────────────────────────

PROBE_FUNCTIONS = {
    "greenhouse": probe_greenhouse,
    "lever": probe_lever,
    "ashby": probe_ashby,
    "workable": probe_workable,
    "smartrecruiters": probe_smartrecruiters,
    "recruitee": probe_recruitee,
    "freshteam": probe_freshteam,
    "bamboohr": probe_bamboohr,
    "teamtailor": probe_teamtailor,
    "breezy": probe_breezy,
    "pinpoint": probe_pinpoint,
    "rippling": probe_rippling,
}


def verify_ats(ats_name, slugs, workers=30):
    """Verify all slugs for a single ATS using concurrent requests."""
    probe_fn = PROBE_FUNCTIONS.get(ats_name)
    if not probe_fn:
        log(f"  No probe function for {ats_name}")
        return [], [], slugs

    active = []
    inactive = []
    errors = []

    with ThreadPoolExecutor(max_workers=workers) as pool:
        futures = {pool.submit(probe_fn, slug): slug for slug in slugs}

        done = 0
        for future in as_completed(futures):
            slug = futures[future]
            try:
                result = future.result()
            except Exception:
                result = None

            done += 1

            if result is True:
                active.append(slug)
            elif result is False:
                inactive.append(slug)
            else:
                errors.append(slug)

            if done % 100 == 0:
                log(f"    Progress: {done}/{len(slugs)} — active={len(active)} inactive={len(inactive)} errors={len(errors)}")

    return sorted(active), sorted(inactive), sorted(errors)


def main():
    import argparse
    parser = argparse.ArgumentParser(description="Verify ATS company slugs")
    parser.add_argument("--workers", type=int, default=30, help="Concurrent workers per ATS")
    parser.add_argument("--ats", default="", help="Only verify this ATS (e.g. 'greenhouse')")
    parser.add_argument("--input-dir", default=".", help="Dir with wayback_*_companies.txt files")
    parser.add_argument("--output-dir", default=".", help="Dir for verified_*_companies.txt output")
    parser.add_argument("--retry-errors", action="store_true", help="Retry from errors_* files instead of wayback_*")
    args = parser.parse_args()

    ats_filter = args.ats

    grand_active = 0
    grand_total = 0
    summary = {}

    for ats_name in sorted(PROBE_FUNCTIONS.keys()):
        if ats_filter and ats_filter != ats_name:
            continue

        if args.retry_errors:
            input_file = os.path.join(args.input_dir, f"errors_{ats_name}_companies.txt")
        else:
            input_file = os.path.join(args.input_dir, f"wayback_{ats_name}_companies.txt")

        if not os.path.exists(input_file):
            continue

        with open(input_file) as f:
            slugs = [line.strip() for line in f if line.strip()]

        if not slugs:
            continue

        log(f"\n{'='*60}")
        log(f" {ats_name.upper()} — Verifying {len(slugs)} companies")
        log(f"{'='*60}")

        active, inactive, errors = verify_ats(ats_name, slugs, workers=args.workers)

        log(f"  Active:   {len(active)}")
        log(f"  Inactive: {len(inactive)}")
        log(f"  Errors:   {len(errors)}")

        grand_active += len(active)
        grand_total += len(slugs)
        summary[ats_name] = {"active": len(active), "inactive": len(inactive), "errors": len(errors)}

        # Save active companies
        active_file = os.path.join(args.output_dir, f"verified_{ats_name}_companies.txt")
        # Append if retrying errors, overwrite otherwise
        mode = "a" if args.retry_errors else "w"
        with open(active_file, mode) as f:
            for slug in active:
                f.write(slug + "\n")
        log(f"  Saved {len(active)} to {active_file}")

        # Save errors for retry
        if errors:
            error_file = os.path.join(args.output_dir, f"errors_{ats_name}_companies.txt")
            with open(error_file, "w") as f:
                for slug in errors:
                    f.write(slug + "\n")
            log(f"  Saved {len(errors)} errors to {error_file}")

        sys.stdout.flush()

    log(f"\n{'='*60}")
    log(f" GRAND TOTAL: {grand_active} active / {grand_total} total")
    log(f"{'='*60}")
    for name in sorted(summary.keys()):
        s = summary[name]
        log(f"  {name:20s}: {s['active']:5d} active / {s['inactive']:5d} inactive / {s['errors']:5d} errors")

if __name__ == "__main__":
    main()
