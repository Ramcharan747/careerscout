#!/usr/bin/env python3
"""Run the career-page extraction through 9router.

One request per firm by default. The deterministic apply_targets block is
merged in as fixed strings — form action URLs and field names must survive
character-exact or a later automated submission fails silently, and that is the
one thing a model cannot be trusted with. The model interprets those strings; it
does not invent them.

Usage
    python3 run_extraction.py --pilot 20
    python3 run_extraction.py --all --workers 4
    python3 run_extraction.py --pilot 20 --model ag/gemini-3-flash
"""
from __future__ import annotations

import argparse
import collections
import json
import random
import re
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

ENDPOINT = "http://127.0.0.1:20128/v1/chat/completions"
KEY = ""
# Read from a 0600 file rather than baked in, so the key never lands in a
# transcript, a commit or a process listing.
KEY_FILE = ".9router_key"


def api_key() -> str:
    try:
        return open(KEY_FILE, encoding="utf-8").read().strip()
    except OSError:
        sys.exit(f"no {KEY_FILE}; 9router rejects unauthenticated calls with 401")


def load_jsonl(path):
    return [json.loads(l) for l in open(path, encoding="utf-8") if l.strip()]


def build_prompt(schema: dict, firm: dict, targets: dict | None) -> str:
    parts = [
        schema["_note"],
        "\n## WHO THIS IS FOR\n" + schema["goal_context"],
        "\n## RULES\n" + "\n".join(f"- {i}" for i in schema["instructions"]),
        "\n## OUTPUT SCHEMA (return exactly this shape, as JSON)\n"
        + json.dumps({k: schema[k] for k in ("firm", "jobs_index", "jobs_detail")},
                     ensure_ascii=False, indent=1),
        f"\n## FIRM\nname: {firm['name']}\ndomain: {firm['domain']}\n"
        f"country: {firm['hq_country']}\ninvestor_type: {firm['investor_type']}",
    ]
    if targets:
        parts.append(
            "\n## APPLY TARGETS — machine-extracted, COPY THESE STRINGS EXACTLY\n"
            + json.dumps(targets, ensure_ascii=False)[:14000])
    parts.append("\n## CAREER PAGES")
    for p in firm["pages"]:
        parts.append(f"\n--- page (depth {p['depth']}) {p['url']}\n{p['text']}")
    parts.append("\nReturn ONLY the JSON object. No prose, no markdown fence.")
    return "\n".join(parts)


def parse_sse(raw: str) -> tuple[str, dict]:
    """9router answers as server-sent events whatever you ask for.

    It ignores `stream: false` and returns `data:` chunks with the text split
    across deltas, so a plain json.loads on the body fails at character zero.
    Reassemble the deltas; a non-streaming body is still handled in case that
    ever changes.
    """
    if not raw.lstrip().startswith("data:"):
        d = json.loads(raw)
        return d["choices"][0]["message"]["content"], d.get("usage", {})

    out, usage = [], {}
    for line in raw.splitlines():
        line = line.strip()
        if not line.startswith("data:"):
            continue
        payload = line[5:].strip()
        if not payload or payload == "[DONE]":
            continue
        try:
            chunk = json.loads(payload)
        except json.JSONDecodeError:
            continue
        if chunk.get("usage"):
            usage = chunk["usage"]
        for ch in chunk.get("choices", []):
            piece = (ch.get("delta") or {}).get("content") or ""
            if piece:
                out.append(piece)
    return "".join(out), usage


def call(model: str, prompt: str, timeout: int, retries: int = 3):
    body = json.dumps({
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0,
        "response_format": {"type": "json_object"},
    }).encode()
    last = None
    for attempt in range(retries):
        try:
            req = urllib.request.Request(
                ENDPOINT, data=body,
                headers={"Content-Type": "application/json",
                         "Authorization": "Bearer " + KEY})
            with urllib.request.urlopen(req, timeout=timeout) as r:
                raw = r.read().decode("utf-8", "replace")
            txt, usage = parse_sse(raw)
            # Some backends fence the JSON despite response_format.
            m = re.search(r"\{.*\}", txt, re.S)
            if not m:
                raise ValueError("no JSON in response")
            return json.loads(m.group(0)), usage, None
        except Exception as e:                       # noqa: BLE001
            last = f"{type(e).__name__}: {e}"
            if isinstance(e, urllib.error.HTTPError) and e.code in (400, 404):
                break                                # not worth retrying
            time.sleep(2 * (attempt + 1))
    return None, {}, last


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--batches", default="llm_batches.jsonl")
    ap.add_argument("--targets", default="apply_targets.jsonl")
    ap.add_argument("--schema", default="extract_schema.json")
    ap.add_argument("--doors", default="company_doors.csv")
    ap.add_argument("--out", default="extracted.jsonl")
    ap.add_argument("--model", default="ag/gemini-3-flash")
    ap.add_argument("--pilot", type=int, default=0)
    ap.add_argument("--all", action="store_true")
    ap.add_argument("--workers", type=int, default=3)
    ap.add_argument("--timeout", type=int, default=180)
    args = ap.parse_args()

    global KEY
    KEY = api_key()
    schema = json.load(open(args.schema, encoding="utf-8"))
    firms = load_jsonl(args.batches)
    targets = {t["domain"]: t for t in load_jsonl(args.targets)}

    if args.pilot:
        # Weight the pilot towards the pile the regex classifier called empty.
        # Agreement on the easy cases proves nothing; the question is whether
        # 384 "none" firms are really empty.
        buckets = collections.defaultdict(list)
        for f in firms:
            buckets[f.get("regex_door", "")].append(f)
        rng = random.Random(17)
        plan = {"none": 8, "speculative": 4, "internship_programme": 4,
                "open_roles_incl_internship": 2, "open_roles": 2}
        chosen = []
        for door, n in plan.items():
            pool = buckets.get(door, [])
            chosen += rng.sample(pool, min(n, len(pool)))
        firms = chosen[:args.pilot]
    elif not args.all:
        print("pass --pilot N or --all")
        return 1

    print(f"model {args.model} | {len(firms)} firms | {args.workers} workers")
    t0 = time.time()
    results, failures = [], []
    tok_in = tok_out = 0

    def work(f):
        prompt = build_prompt(schema, f, targets.get(f["domain"]))
        obj, usage, err = call(args.model, prompt, args.timeout)
        return f, obj, usage, err, len(prompt)

    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        for f, obj, usage, err, plen in ex.map(work, firms):
            if obj is None:
                failures.append((f["domain"], err))
                print(f"  FAIL {f['domain']}: {err}")
                continue
            obj["_input"] = {"domain": f["domain"], "regex_door": f.get("regex_door"),
                             "prompt_chars": plen}
            results.append(obj)
            tok_in += usage.get("prompt_tokens", 0)
            tok_out += usage.get("completion_tokens", 0)
            print(f"  ok   {f['domain']}")

    with open(args.out, "w", encoding="utf-8") as out:
        for r in results:
            out.write(json.dumps(r, ensure_ascii=False) + "\n")

    dt = time.time() - t0
    print(f"\n{len(results)} ok, {len(failures)} failed in {dt:.0f}s -> {args.out}")
    if tok_in or tok_out:
        print(f"tokens: {tok_in:,} in, {tok_out:,} out"
              f"  ({tok_in / max(len(results), 1):,.0f} in per firm)")
    if failures:
        print("failures:")
        for d, e in failures[:10]:
            print(f"  {d}: {e}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
