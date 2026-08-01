#!/usr/bin/env python3
"""Interactive page labeller for the career-page parser.

Walks a stage-1 sheet from cmd/build_label_set. You answer in the terminal; the
browser follows along by itself.

Why a local HTTP server rather than opening files directly:

  * Safari refuses file:// URLs outside its sandbox ("Ignoring request to load
    this main resource because it is outside the sandbox"), so the archived copy
    could not be shown at all.
  * Driving a browser with AppleScript triggers macOS automation and Finder
    permission prompts on every run.

Serving from 127.0.0.1 avoids both. The browser is opened once at the start and
then polls for the current page, so nothing needs to control it afterwards: no
AppleScript, no permission dialogs, no tab churn.

Scripts are stripped from the archived HTML before display. That is deliberate —
the parser does not execute JavaScript either, so what you see is what it saw,
and it stops pages from busting out of the iframe or redirecting.

Usage
    python3 label_pages.py
    python3 label_pages.py --file page_labels.csv --store html_store
    python3 label_pages.py --browser "Safari"
    python3 label_pages.py --no-browser        # decide from the text preview
    python3 label_pages.py --relabel           # revisit answered rows
    python3 label_pages.py --port 8777

Keys
    y / 1   page lists actual openings
    n / 0   it does not
    s       skip
    b       back one row
    l       open the LIVE site in a new tab
    q       save and quit
"""
from __future__ import annotations

import argparse
import csv
import gzip
import html as htmllib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PLAIN = [True]
STATE = {"idx": 0, "domain": "", "url": "", "title": "", "depth": "1",
         "total": 0, "done": False}
ROWS: list[dict] = []
STORE = "html_store"

SCRIPT_RE = re.compile(r"(?is)<script[^>]*>.*?</script>|<script[^>]*/>")
LINK_CSS_RE = re.compile(r"(?is)<link[^>]+rel=[\"\']?stylesheet[\"\']?[^>]*>")
IMG_RE = re.compile(r"(?is)<img[^>]*>")
PLAIN_CSS = ("<style>body{max-width:60rem;margin:2rem auto;padding:0 1.5rem;"
             "font:15px/1.6 -apple-system,BlinkMacSystemFont,sans-serif;color:#111}"
             "a{color:#06c}h1,h2,h3{line-height:1.25}"
             "table{border-collapse:collapse}td,th{border:1px solid #ddd;padding:4px 8px}"
             "</style>")
CSP_RE = re.compile(r"(?is)<meta[^>]+http-equiv=[\"']?content-security-policy[\"']?[^>]*>")
HEAD_RE = re.compile(r"(?i)(<head[^>]*>)")


def archived_html(domain: str, page_url: str, plain: bool = True) -> bytes | None:
    """Decompress a stored page and make it safe to display in an iframe.

    plain=True strips every remote asset. Loading the original stylesheets and
    images via <base href> is prettier but depends on the live host still being
    up and fast; when it is not, the iframe hangs blank forever. That silently
    cost 93 of 150 rows in the first labelling round, so reliability wins and
    plain is the default.
    """
    path = os.path.join(STORE, f"{domain}.html.gz")
    if not os.path.exists(path):
        return None
    try:
        with gzip.open(path, "rt", encoding="utf-8", errors="replace") as fh:
            doc = fh.read()
    except Exception:
        return None

    doc = SCRIPT_RE.sub("", doc)
    doc = CSP_RE.sub("", doc)

    if plain:
        doc = LINK_CSS_RE.sub("", doc)
        doc = IMG_RE.sub("", doc)
        inject = PLAIN_CSS
    else:
        # <base> so relative CSS, images and links resolve against the real host.
        inject = f'<base href="{htmllib.escape(page_url or f"https://{domain}/", quote=True)}">'

    doc = HEAD_RE.sub(r"\1" + inject, doc, count=1) if HEAD_RE.search(doc) else inject + doc
    return (doc + JUMP_TO_JOBS).encode("utf-8", "replace")


# The page's own scripts are stripped, so this is the only script that runs and
# it cannot be hijacked by the archived page. It jumps to the first job wording
# instead of leaving the iframe at the top: measured over 200 sampled pages the
# job wording sits a median 16% and a p90 63% of the way down, so opening at the
# top shows a marketing header and nothing that helps you decide.
JUMP_TO_JOBS = """
<script>
(function(){
  var re = /(open position|current opening|current vacanc|open role|job opening|
            we are hiring|apply now|view job|search job|offene stellen|
            aktuelle stellen|vacature|offre|lediga jobb|posizioni)/i;
  var w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  var n, hit = null;
  while ((n = w.nextNode())) {
    if (n.nodeValue && re.test(n.nodeValue)) { hit = n.parentElement; break; }
  }
  var tag = document.createElement('div');
  tag.style.cssText = 'position:fixed;top:0;left:0;right:0;z-index:99999;padding:4px 10px;'
    + 'font:12px -apple-system,sans-serif;color:#fff;background:'
    + (hit ? '#1a7f37' : '#9a6700');
  tag.textContent = hit ? 'jumped to the first job wording on this page'
                        : 'no job wording anywhere in this page';
  document.body.appendChild(tag);
  if (hit) {
    hit.style.outline = '3px solid #1a7f37';
    hit.scrollIntoView({block: 'center'});
  }
})();
</script>
""".replace("\n            ", "")

VIEWER = """<!doctype html><meta charset="utf-8"><title>careerscout labeller</title>
<style>
 html,body{margin:0;height:100%;font:14px -apple-system,BlinkMacSystemFont,sans-serif;
   background:#111;color:#eee}
 #bar{height:44px;display:flex;align-items:center;gap:14px;padding:0 14px;
   background:#1c1c1e;border-bottom:1px solid #333}
 #bar b{font-size:14px}#bar span{color:#9a9a9e}
 #f{width:100%;height:calc(100% - 44px);border:0;background:#fff}
 .pill{background:#2c2c2e;padding:3px 9px;border-radius:99px;font-size:12px}
</style>
<div id="bar">
  <b id="dom">…</b><span class="pill" id="dep"></span><span class="pill" id="cnt"></span>
  <span id="ttl"></span>
  <span style="margin-left:auto;color:#666">answer in the terminal</span>
</div>
<iframe id="f" src="about:blank"></iframe>
<script>
let cur = -1;
async function tick(){
  try{
    const s = await (await fetch('/current',{cache:'no-store'})).json();
    if(s.done){ document.getElementById('dom').textContent='finished';
                document.getElementById('f').src='about:blank'; return; }
    if(s.idx !== cur){
      cur = s.idx;
      document.getElementById('f').src = '/page/' + encodeURIComponent(s.domain);
      document.getElementById('dom').textContent = s.domain;
      document.getElementById('ttl').textContent = s.title || '';
      document.getElementById('dep').textContent = 'depth ' + (s.depth || '1');
      document.getElementById('cnt').textContent = (s.idx+1) + ' / ' + s.total;
    }
  }catch(e){}
}
setInterval(tick, 400); tick();
</script>
"""


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):  # keep the terminal clean for prompts
        pass

    def _send(self, code, body, ctype="text/html; charset=utf-8"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path in ("/", "/view"):
            return self._send(200, VIEWER.encode())
        if self.path.startswith("/current"):
            return self._send(200, json.dumps(STATE).encode(), "application/json")
        if self.path.startswith("/page/"):
            from urllib.parse import unquote
            dom = unquote(self.path[len("/page/"):])
            url = ""
            for r in ROWS:
                if r.get("domain") == dom:
                    url = r.get("page_url", "")
                    break
            body = archived_html(dom, url, plain=PLAIN[0])
            if body is None:
                return self._send(404, b"<h2>no archived copy for this domain</h2>")
            return self._send(200, body)
        self._send(404, b"not found")


def start_server(port: int) -> ThreadingHTTPServer:
    srv = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def open_browser(url: str, browser: str | None) -> None:
    """One plain `open` call. No AppleScript, so no automation prompts."""
    cmd = ["open"]
    if browser:
        cmd += ["-a", browser]
    subprocess.run(cmd + [url], check=False)


def save(path: str, fields: list[str], rows: list[dict]) -> None:
    """Atomic write: a crash mid-save must not destroy hours of labelling."""
    d = os.path.dirname(os.path.abspath(path)) or "."
    fd, tmp = tempfile.mkstemp(dir=d, suffix=".tmp")
    with os.fdopen(fd, "w", newline="", encoding="utf-8") as fh:
        w = csv.DictWriter(fh, fieldnames=fields)
        w.writeheader()
        w.writerows(rows)
    shutil.move(tmp, path)


def main() -> int:
    global ROWS, STORE
    ap = argparse.ArgumentParser()
    ap.add_argument("--file", default="page_labels.csv")
    ap.add_argument("--store", default="html_store")
    ap.add_argument("--browser", default="Google Chrome")
    ap.add_argument("--no-browser", action="store_true")
    ap.add_argument("--relabel", action="store_true")
    ap.add_argument("--port", type=int, default=8777)
    ap.add_argument("--render", choices=["plain", "styled"], default="plain",
                    help="plain strips remote assets and always renders; "
                         "styled loads the original CSS and may hang")
    args = ap.parse_args()
    STORE = args.store
    PLAIN[0] = args.render == "plain"

    if not os.path.exists(args.file):
        print(f"not found: {args.file}\nrun:  STAGE=1 build_label_set")
        return 1

    with open(args.file, encoding="utf-8", errors="replace") as fh:
        r = csv.DictReader(fh)
        fields = r.fieldnames or []
        ROWS = list(r)
    if "label" not in fields:
        print("no 'label' column")
        return 1

    todo = [i for i, row in enumerate(ROWS)
            if args.relabel or not (row.get("label") or "").strip()]
    STATE["total"] = len(todo)

    srv = None
    if not args.no_browser:
        for p in range(args.port, args.port + 12):
            try:
                srv = start_server(p)
                args.port = p
                break
            except OSError:
                continue
        if srv is None:
            print("could not bind a local port; continuing without the browser")

    print("=" * 96)
    print(f"  {len(ROWS)} pages   {len(ROWS) - len(todo)} already labelled   {len(todo)} to go")
    if srv:
        print(f"  viewer : http://127.0.0.1:{args.port}/   ({args.browser})")
    print("  y=lists jobs   n=no jobs   s=skip   b=back   l=live site   q=quit")
    print("=" * 96)

    if srv:
        open_browser(f"http://127.0.0.1:{args.port}/", args.browser)

    pos = 0
    while 0 <= pos < len(todo):
        i = todo[pos]
        row = ROWS[i]
        depth = row.get("depth", "") or "1"
        STATE.update(idx=pos, domain=row.get("domain", ""),
                     url=row.get("page_url", ""), title=row.get("page_title", ""),
                     depth=depth)

        n_done = sum(1 for x in ROWS if (x.get("label") or "").strip())
        print(f"\n[{pos + 1}/{len(todo)}]  labelled {n_done}/{len(ROWS)}   "
              f"depth {depth}   {row.get('domain','')}")
        print(f"  {row.get('page_url','')}")
        print(f"  parser: extracted={row.get('parser_extracted','?')} "
              f"score={row.get('best_score','?')}   guess: {row.get('top_sample_1','')[:60]}")

        while True:
            try:
                ans = input("  > ").strip().lower()
            except (EOFError, KeyboardInterrupt):
                ans = "q"
            if ans in ("y", "1", "yes"):
                row["label"] = "1"
            elif ans in ("n", "0", "no"):
                row["label"] = "0"
            elif ans == "s":
                pass
            elif ans == "b":
                pos = max(0, pos - 2)
                break
            elif ans == "l":
                open_browser(row.get("page_url", ""), args.browser)
                continue
            elif ans == "q":
                save(args.file, fields, ROWS)
                lab = sum(1 for x in ROWS if (x.get("label") or "").strip())
                print(f"\nsaved {args.file} — {lab}/{len(ROWS)} labelled")
                return 0
            else:
                print("  y / n / s / b / l / q")
                continue
            break

        save(args.file, fields, ROWS)
        pos += 1

    STATE["done"] = True
    yes = sum(1 for x in ROWS if (x.get("label") or "").strip() == "1")
    no = sum(1 for x in ROWS if (x.get("label") or "").strip() == "0")
    print(f"\ndone — {yes} yes, {no} no, {len(ROWS) - yes - no} unlabelled")
    print(f"saved {args.file}")
    print("\nnext:  STAGE=2 LABELS=page_labels.csv build_label_set")
    return 0


if __name__ == "__main__":
    sys.exit(main())
