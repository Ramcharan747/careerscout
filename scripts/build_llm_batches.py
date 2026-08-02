#!/usr/bin/env python3
"""Bundle each firm's career pages into one request.

One request per firm, not per page. A firm's career page and the pages it leads
to are one document about one employer — sending them separately asks the model
the same question five times and gives it less to answer with each time. It also
means the answer comes back keyed by firm, which is how the outreach list is
worked.

Not every nested page earns its place. Depth-1 is always in; a nested page is
included only if the link that led to it scored well under the fitted model in
link_model.json, or if it demonstrably carries a door. The rest are language
switchers, benefits blurbs and application forms that add tokens and no answer.

Dead pages — parked domains, 404s, expired domains resold to spam — are dropped
before bundling. They are 23% of the archive and worth nothing to a model.

Output: llm_batches.jsonl, one JSON object per firm.
"""
from __future__ import annotations

import argparse
import csv
import glob
import gzip
import json
import math
import os
import re

TAG = re.compile(r"(?s)<(script|style)[^>]*>.*?</\1>|<[^>]+>")
WS = re.compile(r"\s+")
DEPTH_KEY = re.compile(r"^(.*)__l(\d+)_(\d+)$")
DEAD = re.compile(r"(?i)404|page not found|domain (geparkt|parked|for sale)|is for sale|"
                  r"brandbucket|under construction|coming soon|account suspended|"
                  r"slot gacor|situs slot|judi bola|togel|hugedomains|sedo\.com")

# A nested page with no postings still earns its place if it carries a way in.
# Deliberately narrower than "mentions jobs" — every careers page mentions jobs.
DOORISH = re.compile(
    r"(?i)internship|praktikum|praktikant|werkstudent|stagiair|stagiaire|"
    r"graduate (program|programme|scheme)|summer (analyst|associate)|off-?cycle|"
    r"initiativbewerbung|spontaneous application|open application|"
    r"unsolicited application|candidature spontan|"
    r"send (us )?your (cv|r[ée]sum)|we are always (looking|interested)|"
    r"no (current|suitable) (openings?|vacanc|positions?)")


def split_key(k: str) -> tuple[str, int]:
    m = DEPTH_KEY.match(k)
    return (m.group(1), int(m.group(2))) if m else (k, 1)


def text_of(path: str) -> str:
    try:
        with gzip.open(path, "rt", encoding="utf-8", errors="replace") as fh:
            return WS.sub(" ", TAG.sub(" ", fh.read())).strip()
    except Exception:
        return ""


def load_model(path: str):
    if not os.path.exists(path):
        return None
    m = json.load(open(path))
    w = dict(zip(m["features"], m["coef"]))
    b = m["intercept"]

    def score(row: dict) -> float:
        d = int(row.get("depth") or 1)
        segs = min(int(row.get("href_segs") or 0), 8) / 8
        words = min(int(row.get("txt_words") or 0), 12) / 12
        f = {
            "txt_list": float(row.get("txt_list") or 0),
            "txt_job": float(row.get("txt_job") or 0),
            "txt_team": float(row.get("txt_team") or 0),
            "txt_apply": float(row.get("txt_apply") or 0),
            "txt_page": float(row.get("txt_page") or 0),
            "href_job": float(row.get("href_job") or 0),
            "href_query": float(row.get("href_query") or 0),
            "depth_2": 1.0 if d == 2 else 0.0,
            "depth_3": 1.0 if d == 3 else 0.0,
            "depth_4plus": 1.0 if d >= 4 else 0.0,
            "segs_norm": segs, "words_norm": words,
            "parent_dead": 0.0,
        }
        z = b + sum(w.get(k, 0.0) * v for k, v in f.items())
        return 1 / (1 + math.exp(-z))

    return score


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--firms", default="funds_crawl_list.csv")
    ap.add_argument("--doors", default="company_doors.csv")
    ap.add_argument("--store", default="html_store")
    ap.add_argument("--out", default="llm_batches.jsonl")
    ap.add_argument("--min-score", type=float, default=0.55,
                    help="model probability a nested page must clear to be included")
    ap.add_argument("--max-pages", type=int, default=6, help="pages per firm")
    ap.add_argument("--max-chars", type=int, default=9000, help="chars per page")
    ap.add_argument("--max-firm-chars", type=int, default=28000, help="chars per firm")
    ap.add_argument("--limit", type=int, default=0, help="pilot: first N firms only")
    ap.add_argument("--only-door", default="", help="pilot: restrict to this door class")
    ap.add_argument("--pack", action="store_true",
                    help="also bin-pack firms into full requests")
    ap.add_argument("--packed", default="llm_requests.jsonl")
    ap.add_argument("--budget-chars", type=int, default=60000,
                    help="input chars per request (~15k tokens at 60k)")
    ap.add_argument("--window", type=int, default=20,
                    help="how far to look ahead for a firm that still fits")
    args = ap.parse_args()

    firms = {r["domain"].lower(): r for r in csv.DictReader(
        open(args.firms, encoding="utf-8", errors="replace")) if r.get("domain")}
    doors = {r["domain"].lower(): r for r in csv.DictReader(
        open(args.doors, encoding="utf-8", errors="replace"))}

    # Model score for each nested page, via the link that reached it.
    # The link model ranks links before they are fetched. These pages are
    # already fetched, so the page's own outcome is the better selector and the
    # link score is only a tiebreak. Measured: raising the link-score threshold
    # from 0.45 to 0.70 moves the share of links that reached a real list from
    # 47.1% to 49.1% while discarding a quarter of them — as a filter here it
    # costs coverage and buys almost nothing. n_jobs is ground truth.
    score = load_model("link_model.json")
    url_score: dict[str, float] = {}
    url_jobs: dict[str, int] = {}
    for row in csv.DictReader(open("link_outcomes.csv", encoding="utf-8",
                                   errors="replace")):
        if score:
            url_score[row["url"]] = score(row)
        url_jobs[row["url"]] = int(row.get("n_jobs") or 0)

    # Map archive key -> the URL it was fetched from, so scores can be applied.
    key_url: dict[str, str] = {}
    for path in (["career_pages.csv"] + glob.glob("career_pages_d*.csv")
                 + glob.glob("career_pages_l2*.csv")):
        for row in csv.DictReader(open(path, encoding="utf-8", errors="replace")):
            if row.get("domain"):
                key_url[row["domain"]] = row.get("career_url", "")

    by_firm: dict[str, list[str]] = {}
    for f in os.listdir(args.store):
        if not f.endswith(".html.gz"):
            continue
        key = f[:-8]
        dom, _ = split_key(key)
        dom = dom.lower()
        if dom in firms:
            by_firm.setdefault(dom, []).append(key)

    n_out = n_pages = n_chars = 0
    dropped_dead = dropped_lowscore = 0
    with open(args.out, "w", encoding="utf-8") as out:
        for dom in sorted(by_firm):
            d = doors.get(dom, {})
            if d.get("door") == "dead":
                continue
            if args.only_door and d.get("door") != args.only_door:
                continue

            chosen = []
            for key in sorted(by_firm[dom], key=lambda k: split_key(k)[1]):
                _, depth = split_key(key)
                txt = text_of(os.path.join(args.store, f"{key}.html.gz"))
                if len(txt) < 200 or DEAD.search(txt[:400]):
                    dropped_dead += 1
                    continue
                if depth > 1:
                    u = key_url.get(key, "")
                    jobs = url_jobs.get(u, 0)
                    door = bool(DOORISH.search(txt))
                    # Every nested page that is not dead goes in. An earlier
                    # version required 3+ postings or a door keyword, and that
                    # keyword list had the same language gaps the pilot exposed
                    # — it dropped a French page reading "Aucun poste à
                    # pourvoir, envoyez votre candidature". The page is already
                    # fetched; the only cost of including it is tokens, and
                    # packing amortises those. Ranking still decides what
                    # survives the per-firm cap.
                    chosen.append((depth, jobs * 10 + (5 if door else 0)
                                   + url_score.get(u, 0.0), key, txt))
                else:
                    chosen.append((depth, 1e9, key, txt))

            if not chosen:
                continue
            # Depth first, then model score: the landing page sets context and
            # the deep pages carry the detail.
            chosen.sort(key=lambda x: (x[0], -x[1]))
            chosen = chosen[:args.max_pages]

            # The same page often sits in the archive twice — once as the
            # firm's career page and again as a nested target, differing only
            # by a trailing slash. Paying to send it twice teaches nothing.
            pages, budget, seen_text = [], args.max_firm_chars, set()
            for depth, s, key, txt in chosen:
                h = hash(txt[:2000])
                if h in seen_text:
                    continue
                seen_text.add(h)
                take = min(len(txt), args.max_chars, budget)
                if take <= 0:
                    break
                pages.append({"depth": depth, "url": key_url.get(key, ""),
                              "link_score": round(s, 3), "text": txt[:take]})
                budget -= take

            fm = firms[dom]
            rec = {
                "domain": dom,
                "name": fm.get("name", ""),
                "investor_type": fm.get("investor_type", ""),
                "hq_country": fm.get("hq_country", ""),
                "portfolio_n": fm.get("portfolio_n", ""),
                "deal_velocity": fm.get("deal_velocity", ""),
                "regex_door": d.get("door", ""),
                "pages": pages,
            }
            out.write(json.dumps(rec, ensure_ascii=False) + "\n")
            n_out += 1
            n_pages += len(pages)
            n_chars += sum(len(p["text"]) for p in pages)
            if args.limit and n_out >= args.limit:
                break

    print(f"{args.out}: {n_out} firms, {n_pages} pages "
          f"({n_pages / max(n_out, 1):.1f} per firm)")
    print(f"  dropped: {dropped_dead} dead pages, {dropped_lowscore} low-scoring nested")
    print(f"  {n_chars:,} chars  ~= {n_chars / 4:,.0f} input tokens "
          f"({n_chars / 4 / max(n_out, 1):,.0f} per firm)")

    if args.pack:
        pack(args)
    return 0


def pack(args) -> None:
    """Fill each request to the budget instead of sending one firm at a time.

    A firm is never split across requests — the whole point is that the model
    sees one employer's pages together. But a firm is usually far smaller than a
    request can hold, so packing several into one turns 746 requests into a few
    dozen at the same token cost.

    Greedy in order, with a lookahead: when the next firm does not fit, scan
    ahead through a window for one that does rather than closing the request
    half empty. The window is bounded so ordering is broadly preserved and a
    single oversized firm cannot drag the whole tail forward.
    """
    firms = [json.loads(line) for line in open(args.out, encoding="utf-8")]
    for f in firms:
        f["_chars"] = sum(len(p["text"]) for p in f["pages"])

    pending = list(firms)
    requests, oversized = [], 0
    while pending:
        cur, used, i = [], 0, 0
        while i < len(pending):
            f = pending[i]
            if f["_chars"] > args.budget_chars and not cur:
                # Bigger than an empty request: it gets one to itself rather
                # than being dropped or truncated further.
                cur.append(pending.pop(i))
                oversized += 1
                break
            if used + f["_chars"] <= args.budget_chars:
                used += f["_chars"]
                cur.append(pending.pop(i))
                continue
            i += 1
            if i > args.window and cur:
                break                     # window exhausted, close the request
        if not cur:
            break
        requests.append(cur)

    with open(args.packed, "w", encoding="utf-8") as out:
        for n, group in enumerate(requests, 1):
            out.write(json.dumps({
                "request_id": n,
                "n_firms": len(group),
                "chars": sum(f["_chars"] for f in group),
                "firms": [{k: v for k, v in f.items() if k != "_chars"} for f in group],
            }, ensure_ascii=False) + "\n")

    sizes = [sum(f["_chars"] for f in g) for g in requests]
    counts = [len(g) for g in requests]
    fill = sum(sizes) / (len(requests) * args.budget_chars)
    print(f"\n{args.packed}: {len(requests)} requests for {sum(counts)} firms")
    print(f"  firms per request : min {min(counts)}  median "
          f"{sorted(counts)[len(counts) // 2]}  max {max(counts)}")
    print(f"  budget fill       : {fill:.0%} of {args.budget_chars:,} chars")
    print(f"  {sum(sizes) / 4:,.0f} input tokens total"
          + (f"  ({oversized} firms needed a request to themselves)" if oversized else ""))


if __name__ == "__main__":
    raise SystemExit(main())
