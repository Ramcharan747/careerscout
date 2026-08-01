#!/usr/bin/env bash
# Walk career sites inwards until the openings turn up.
#
# A career landing page often advertises nothing itself: it carries a "Search
# careers" or "Job openings" button, and the list is one, two or three hops
# further in. One hop is not enough — measured on a 200-page sample of investor
# firms, 116 pages had no job wording anywhere in their HTML, because the jobs
# were never on that page to begin with.
#
# Each round expands the level the previous round fetched, so the walk stops by
# itself when a level comes back empty rather than at a depth picked in advance.
#
# Usage:  scripts/crawl_deep.sh [max_depth]        (default 5)
set -euo pipefail

MAX_DEPTH="${1:-5}"
BIN="${BIN:-./bin}"
EXCLUDE="${SEED_EXCLUDE:-}"

for (( d=1; d<MAX_DEPTH; d++ )); do
  next=$(( d + 1 ))
  out="career_pages_d${next}.csv"

  echo "── expanding depth ${d} → ${next} ────────────────────────────────"
  FROM_DEPTH="$d" OUTPUT="$out" EXCLUDE="$EXCLUDE" "$BIN/expand_career_links"

  # Header only means the walk has bottomed out.
  if [[ $(wc -l < "$out") -le 1 ]]; then
    echo "no new pages at depth ${next}; stopping"
    rm -f "$out"
    break
  fi

  echo "── fetching depth ${next} ───────────────────────────────────────"
  INPUT="$out" "$BIN/fetch_career_html"

  EXCLUDE="${EXCLUDE:+$EXCLUDE,}$out"
done

echo "── parsing the full archive ─────────────────────────────────────"
"$BIN/parse_career_jobs"
