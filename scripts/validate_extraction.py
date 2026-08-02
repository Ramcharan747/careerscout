#!/usr/bin/env python3
"""Check that every claimed door is backed by text that is actually on the page.

The model recovers real doors the regex missed, but it also invents them from
culture copy — one firm with no application wording anywhere was classified
open_roles on the strength of "We seek out entrepreneurial spirits". A door that
is not on the page is worse than a missed one: it sends a real application into
nothing.

So each verbatim quote is looked for in the source text. Normalisation is
whitespace and case only; the quote has to genuinely be there.
"""
from __future__ import annotations
import gzip, json, os, re, sys, argparse, collections

WS = re.compile(r"\s+")
TAG = re.compile(r"(?s)<(script|style)[^>]*>.*?</\1>|<[^>]+>")
DEPTH = re.compile(r"__l\d+_\d+$")


def firm_text(domain: str, store="html_store") -> str:
    out = []
    for f in os.listdir(store):
        if f.endswith(".html.gz") and DEPTH.sub("", f[:-8]).lower() == domain:
            try:
                t = gzip.open(os.path.join(store, f), "rt", encoding="utf-8",
                              errors="replace").read()
            except OSError:
                continue
            out.append(WS.sub(" ", TAG.sub(" ", t)))
    return WS.sub(" ", " ".join(out)).lower()


def grounded(quote: str, text: str) -> bool:
    q = WS.sub(" ", quote or "").strip().lower()
    if len(q) < 12:
        return False
    if q in text:
        return True
    # Allow a prefix match: models often trail off or re-punctuate the tail.
    return len(q) > 40 and q[:40] in text


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--file", default="extracted.jsonl")
    ap.add_argument("--out", default="")
    args = ap.parse_args()
    rows = [json.loads(l) for l in open(args.file, encoding="utf-8") if l.strip()]

    stats = collections.Counter()
    fixed = []
    for r in rows:
        f = r.get("firm", {})
        dom = f.get("domain") or r["_input"]["domain"]
        text = firm_text(dom)
        quotes = (f.get("evidence_verbatim") or []) + (f.get("evidence") or [])
        ok = [q for q in quotes if grounded(q, text)]
        f["_grounded_quotes"] = len(ok)
        f["_total_quotes"] = len(quotes)
        claims_door = f.get("page_type") not in ("no_door", "dead_or_irrelevant")
        if claims_door and quotes and not ok:
            # Quoted something that is not on the page: an invented door. This
            # is the dangerous failure and it is downgraded.
            stats["ungrounded_door"] += 1
            f["_original_page_type"] = f.get("page_type")
            f["page_type"] = "no_door"
            f["_downgraded"] = "quoted text that is not on the page"
        elif claims_door and not quotes:
            # Claimed a door and quoted nothing. Not the same failure — one
            # firm here plainly says "we are always on the lookout for driven
            # and talented individuals" and simply was not quoted. Downgrading
            # it lost a real door, so these are kept and flagged instead.
            stats["unquoted_door"] += 1
            f["_needs_review"] = "door claimed with no supporting quote"
        elif claims_door:
            stats["grounded_door"] += 1
        else:
            stats["no_door"] += 1
        fixed.append(r)

    tot = stats["grounded_door"] + stats["ungrounded_door"]
    print(f"firms claiming a door : {tot}")
    print(f"  backed by real text : {stats['grounded_door']}")
    print(f"  UNGROUNDED, downgraded: {stats['ungrounded_door']}"
          + (f"  ({100*stats['ungrounded_door']/tot:.0f}%)" if tot else ""))
    print(f"  claimed, unquoted, kept for review: {stats['unquoted_door']}")
    print(f"firms with no door    : {stats['no_door']}")
    if args.out:
        with open(args.out, "w", encoding="utf-8") as o:
            for r in fixed:
                o.write(json.dumps(r, ensure_ascii=False) + "\n")
        print(f"-> {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
