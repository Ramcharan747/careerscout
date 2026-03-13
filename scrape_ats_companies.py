#!/usr/bin/env python3
"""
ATS Company Scraper — Discovers companies per ATS using multiple search engines.

Uses Google and Bing site: queries to find all indexed company pages for each ATS.
Extracts company slugs from result URLs.

Usage:
  python3 scrape_ats_companies.py                    # All ATS, all engines
  ATS_FILTER=lever python3 scrape_ats_companies.py   # Just Lever
"""
import urllib.request, urllib.parse, json, re, ssl, time, os, sys
import random

ctx = ssl.create_default_context()

USER_AGENTS = [
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/129.0 Safari/537.36",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/128.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Gecko/20100101 Firefox/131.0",
]

# ATS domains and how to extract slugs from URLs
ATS_SEARCH_CONFIGS = [
    {
        "name": "lever",
        "site_query": "site:jobs.lever.co",
        "slug_patterns": [r"jobs\.lever\.co/([a-zA-Z0-9_-]+)"],
        "blocked_slugs": {"favicon.ico", "robots.txt", "sitemap.xml", "search", "api", "embed"},
    },
    {
        "name": "ashby",
        "site_query": "site:jobs.ashbyhq.com",
        "slug_patterns": [r"jobs\.ashbyhq\.com/([a-zA-Z0-9_.-]+)"],
        "blocked_slugs": {"favicon.ico", "robots.txt", "sitemap.xml", "api", "meeting", "embed"},
    },
    {
        "name": "workable",
        "site_query": "site:apply.workable.com",
        "slug_patterns": [r"apply\.workable\.com/([a-zA-Z0-9_-]+)"],
        "blocked_slugs": {"favicon.ico", "robots.txt", "sitemap.xml", "api", "o", "embed", "static"},
    },
    {
        "name": "smartrecruiters",
        "site_query": "site:careers.smartrecruiters.com",
        "slug_patterns": [r"careers\.smartrecruiters\.com/([a-zA-Z0-9_-]+)"],
        "blocked_slugs": {"favicon.ico", "robots.txt", "sitemap.xml", "api", "search", "static", "widget"},
    },
    {
        "name": "greenhouse",
        "site_query": "site:boards.greenhouse.io",
        "slug_patterns": [r"boards\.greenhouse\.io/([a-zA-Z0-9_-]+)"],
        "blocked_slugs": {"favicon.ico", "robots.txt", "sitemap.xml", "api", "embed", "static"},
    },
    {
        "name": "bamboohr",
        "site_query": "site:bamboohr.com/careers",
        "slug_patterns": [r"([a-zA-Z0-9_-]+)\.bamboohr\.com"],
        "blocked_slugs": {"www", "api", "app", "login", "help", "support", "status"},
    },
    {
        "name": "recruitee",
        "site_query": "site:recruitee.com/o",
        "slug_patterns": [r"([a-zA-Z0-9_-]+)\.recruitee\.com"],
        "blocked_slugs": {"www", "api", "app", "login", "help", "support", "blog", "docs"},
    },
    {
        "name": "freshteam",
        "site_query": "site:freshteam.com/jobs",
        "slug_patterns": [r"([a-zA-Z0-9_-]+)\.freshteam\.com"],
        "blocked_slugs": {"www", "api", "app", "login", "help", "support"},
    },
    {
        "name": "teamtailor",
        "site_query": "site:teamtailor.com/jobs",
        "slug_patterns": [r"([a-zA-Z0-9_-]+)\.teamtailor\.com"],
        "blocked_slugs": {"www", "api", "app", "login", "help", "support", "career"},
    },
    {
        "name": "breezy",
        "site_query": "site:breezy.hr",
        "slug_patterns": [r"([a-zA-Z0-9_-]+)\.breezy\.hr"],
        "blocked_slugs": {"www", "api", "app", "login", "help", "support"},
    },
    {
        "name": "rippling",
        "site_query": "site:rippling.com/company/jobs",
        "slug_patterns": [r"([a-zA-Z0-9_-]+)\.rippling\.com", r"rippling\.com/[^/]+/([a-zA-Z0-9_-]+)"],
        "blocked_slugs": {"www", "api", "app", "login", "help", "support", "ats", "en-gb"},
    },
    {
        "name": "pinpoint",
        "site_query": "site:pinpointhq.com",
        "slug_patterns": [r"([a-zA-Z0-9_-]+)\.pinpointhq\.com"],
        "blocked_slugs": {"www", "api", "app", "login", "help", "support"},
    },
]


def fetch_url(url, headers=None):
    """Fetch a URL with random UA and return the response body."""
    h = headers or {}
    if "User-Agent" not in h:
        h["User-Agent"] = random.choice(USER_AGENTS)
    h["Accept"] = "text/html,application/xhtml+xml"
    h["Accept-Language"] = "en-US,en;q=0.9"

    req = urllib.request.Request(url, headers=h)
    try:
        resp = urllib.request.urlopen(req, timeout=15, context=ctx)
        return resp.read().decode(errors="ignore")
    except Exception as e:
        return ""


def search_google(query, start=0):
    """Search Google and return list of result URLs."""
    q = urllib.parse.quote_plus(query)
    url = f"https://www.google.com/search?q={q}&start={start}&num=100"
    html = fetch_url(url)
    if not html:
        return []
    
    # Extract URLs from Google results
    urls = []
    # Pattern 1: href="/url?q=<actual_url>&..."
    for m in re.finditer(r'/url\?q=(https?://[^&"]+)', html):
        urls.append(urllib.parse.unquote(m.group(1)))
    # Pattern 2: direct links in results
    for m in re.finditer(r'href="(https?://(?:jobs\.lever|jobs\.ashby|apply\.workable|careers\.smart|boards\.greenhouse|[^"]*\.bamboohr|[^"]*\.recruitee|[^"]*\.freshteam|[^"]*\.teamtailor|[^"]*\.breezy|[^"]*\.rippling|[^"]*\.pinpointhq)[^"]*)"', html):
        urls.append(m.group(1))
    
    return urls


def search_bing(query, offset=0):
    """Search Bing and return list of result URLs."""
    q = urllib.parse.quote_plus(query)
    url = f"https://www.bing.com/search?q={q}&first={offset}&count=50"
    html = fetch_url(url)
    if not html:
        return []
    
    urls = []
    for m in re.finditer(r'href="(https?://[^"]+)"', html):
        u = m.group(1)
        if not any(x in u for x in ["bing.com", "microsoft.com", "go.microsoft", "aka.ms"]):
            urls.append(u)
    
    return urls


def search_duckduckgo(query):
    """Search DuckDuckGo HTML and return result URLs."""
    q = urllib.parse.quote_plus(query)
    url = f"https://html.duckduckgo.com/html/?q={q}"
    html = fetch_url(url)
    if not html:
        return []
    
    urls = []
    for m in re.finditer(r'href="(https?://[^"]+)"', html):
        u = m.group(1)
        if "duckduckgo.com" not in u:
            urls.append(urllib.parse.unquote(u))
    
    return urls


def extract_slugs(urls, config):
    """Extract company slugs from result URLs using the ATS config patterns."""
    slugs = set()
    for url in urls:
        for pattern in config["slug_patterns"]:
            m = re.search(pattern, url, re.IGNORECASE)
            if m:
                slug = m.group(1).lower().split("?")[0].split("#")[0]
                if slug and len(slug) >= 2 and slug not in config["blocked_slugs"]:
                    slugs.add(slug)
    return slugs


def scrape_ats(config):
    """Run all search engines for a single ATS and return unique slugs."""
    name = config["name"]
    query = config["site_query"]
    all_slugs = set()
    
    # Google — paginate through results
    print(f"  Google: ", end="", flush=True)
    for page in range(0, 500, 100):  # Up to 5 pages
        urls = search_google(query, start=page)
        if not urls:
            break
        new = extract_slugs(urls, config)
        all_slugs |= new
        print(f"{len(new)}", end=" ", flush=True)
        time.sleep(random.uniform(3, 6))  # Be polite to Google
    print(f"→ {len(all_slugs)}")
    
    # Bing — paginate
    print(f"  Bing:   ", end="", flush=True)
    before = len(all_slugs)
    for page in range(0, 200, 50):  # Up to 4 pages
        urls = search_bing(query, offset=page)
        if not urls:
            break
        new = extract_slugs(urls, config)
        all_slugs |= new
        print(f"{len(new)}", end=" ", flush=True)
        time.sleep(random.uniform(2, 4))
    print(f"→ +{len(all_slugs) - before}")
    
    # DuckDuckGo — single query
    print(f"  DDG:    ", end="", flush=True)
    before = len(all_slugs)
    urls = search_duckduckgo(query)
    new = extract_slugs(urls, config)
    all_slugs |= new
    print(f"{len(new)} → +{len(all_slugs) - before}")
    
    # Also try variant queries for more coverage
    variants = [
        f'{query} careers',
        f'{query} jobs hiring',
        f'{query} "join us"',
    ]
    for vq in variants:
        urls = search_google(vq)
        new = extract_slugs(urls, config)
        all_slugs |= new
        time.sleep(random.uniform(3, 6))
    
    return all_slugs


def main():
    filter_ats = os.environ.get("ATS_FILTER", "")
    output_dir = os.environ.get("OUTPUT_DIR", ".")
    
    all_results = {}
    
    for config in ATS_SEARCH_CONFIGS:
        if filter_ats and filter_ats != config["name"]:
            continue
        
        name = config["name"]
        print(f"\n{'='*50}")
        print(f" {name.upper()} — {config['site_query']}")
        print(f"{'='*50}")
        
        slugs = scrape_ats(config)
        all_results[name] = sorted(slugs)
        
        # Save per-ATS file
        outfile = os.path.join(output_dir, f"scraped_{name}_companies.txt")
        with open(outfile, "w") as f:
            for s in sorted(slugs):
                f.write(s + "\n")
        
        print(f"  TOTAL: {len(slugs)} unique companies → {outfile}")
        if slugs:
            print(f"  Sample: {', '.join(sorted(slugs)[:10])}")
        
        time.sleep(2)
    
    # Summary
    print(f"\n{'='*50}")
    print(f" SUMMARY")
    print(f"{'='*50}")
    grand_total = 0
    for name in sorted(all_results.keys()):
        count = len(all_results[name])
        grand_total += count
        print(f"  {name:20s}: {count:6d}")
    print(f"  {'TOTAL':20s}: {grand_total:6d}")
    
    # Save combined JSON
    combined = os.path.join(output_dir, "scraped_all_companies.json")
    with open(combined, "w") as f:
        json.dump(all_results, f, indent=2)
    print(f"\n  Combined file: {combined}")


if __name__ == "__main__":
    main()
