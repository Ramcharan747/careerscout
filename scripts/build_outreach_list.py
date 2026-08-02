#!/usr/bin/env python3
"""Turn the door report into the list to actually work, in order.

Ranking is built around one constraint: a Nov–Jan off-cycle internship, paid,
US or Europe, at a firm with a real track record. That makes the megafunds
low-probability rather than high-value — their off-cycle slots are filled a
year ahead through structured programmes with a GPA screen — and it makes the
mid-market funds, which have no programme and hire when someone useful writes
to them, the best odds by a wide margin.

So the tiers are ordered by likelihood of a reply, not by fund size.

Output: outreach_list.csv
"""
from __future__ import annotations

import csv
import re

FUND = {"PE/Buyout", "Growth/Expansion", "Family Office",
        "Other Private Equity", "Investment Bank"}
EU = {"Germany", "France", "Netherlands", "Switzerland", "Belgium", "Sweden",
      "Italy", "Luxembourg", "Norway", "Denmark", "Finland", "Spain", "Austria",
      "Ireland", "Poland", "Portugal", "United Kingdom", "Czech Republic"}
US = {"United States", "Canada"}

# Firms large enough to run a structured programme with a screen Ram would not
# clear, judged by portfolio size. Kept in the list, ranked last.
MEGA_PORTFOLIO = 250

INTERN_TITLE = re.compile(
    r"(?i)intern|praktik|werkstudent|stagiair|stagiaire|\bstage\b|"
    r"graduate|summer analyst|off-?cycle|trainee|junior analyst|analyst")


def num(x) -> float:
    try:
        return float(x or 0)
    except ValueError:
        return 0.0


def tier(r: dict) -> tuple[int, str]:
    """Lower is better."""
    pf = num(r["portfolio_n"])
    dv = num(r["deal_velocity"])
    door = r["door"]
    active = dv >= 2 or pf >= 5          # a real track record, not a shell
    mid = 5 <= pf < MEGA_PORTFOLIO
    mega = pf >= MEGA_PORTFOLIO

    if mega:
        # Real jobs, real brand, but the off-cycle route is a closed programme.
        return (5, "megafund - programme route, long odds")
    if door in ("open_roles_incl_internship",) and mid:
        return (0, "mid-market, internship posted now")
    if door == "internship_programme" and mid and active:
        return (1, "mid-market, runs internships, none posted - write now")
    if door in ("speculative", "speculative_form", "upload_form") and mid and active:
        return (2, "mid-market, invites unsolicited applications")
    if door == "open_roles" and mid:
        return (3, "mid-market, roles posted - check fit")
    if door == "email_only" and active:
        return (4, "hiring address published, no listing")
    return (6, "smaller or quieter - lowest priority")


def main() -> int:
    rows = [r for r in csv.DictReader(
        open("company_doors.csv", encoding="utf-8", errors="replace"))
        if r["door"] not in ("none", "dead") and r["investor_type"] in FUND]

    out = []
    for r in rows:
        c = r["hq_country"]
        geo = "US" if c in US else ("EU" if c in EU else "other")
        if geo == "other":
            continue                      # not a market Ram is targeting
        t, why = tier(r)
        sample = r.get("sample_jobs", "")
        relevant = " | ".join(s.strip() for s in sample.split("|")
                              if INTERN_TITLE.search(s))
        out.append({
            "tier": t, "why": why, "geo": geo,
            "domain": r["domain"], "name": r["name"],
            "investor_type": r["investor_type"], "hq_country": c,
            "portfolio_n": r["portfolio_n"], "deal_velocity": r["deal_velocity"],
            "door": r["door"], "open_roles": r["open_roles"],
            "internship": r["internship"], "speculative": r["speculative"],
            "careers_email": r["careers_email"],
            "apply_url": r["best_url"],
            "relevant_titles": relevant,
            "all_sample_titles": sample,
        })

    out.sort(key=lambda r: (r["tier"], -num(r["deal_velocity"]), -num(r["portfolio_n"])))
    with open("outreach_list.csv", "w", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=list(out[0].keys()))
        w.writeheader()
        w.writerows(out)

    print(f"outreach_list.csv — {len(out)} firms\n")
    seen: dict[int, tuple[str, int, int, int]] = {}
    for r in out:
        t = r["tier"]
        _, n, us, eu = seen.get(t, (r["why"], 0, 0, 0))
        seen[t] = (r["why"], n + 1, us + (r["geo"] == "US"), eu + (r["geo"] == "EU"))
    print(f"{'tier':<5}{'firms':>6}{'US':>5}{'EU':>5}   what it is")
    for t in sorted(seen):
        why, n, us, eu = seen[t]
        print(f"{t:<5}{n:>6}{us:>5}{eu:>5}   {why}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
