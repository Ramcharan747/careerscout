#!/usr/bin/env python3
"""Fit a link-following model on the outcomes of a completed deep crawl.

The crawl labels itself. Every link the expander followed was a guess about
where the openings live, and the fetch then settled it. So the ranking can be
fitted without anyone hand-labelling anything: features are what was knowable
before the fetch (anchor wording, href shape, depth, the state of the page the
link sat on), and the target is whether the destination actually held a list.

What this is for is budget. A firm gets a fixed number of fetches; spending them
on the two links that pay rather than the first four encountered is the whole
difference between reaching the openings and concluding a firm is not hiring.

Usage
    python3 fit_link_model.py
    python3 fit_link_model.py --file link_outcomes.csv --out link_model.json
"""
from __future__ import annotations

import argparse
import csv
import json
import re

import numpy as np
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import average_precision_score, roc_auc_score
from sklearn.model_selection import GroupKFold

FEATURES = [
    "txt_list", "txt_job", "txt_team", "txt_apply", "txt_page",
    "href_job", "href_query", "depth_2", "depth_3", "depth_4plus",
    "segs_norm", "words_norm", "parent_dead",
]


def load(path: str):
    rows = list(csv.DictReader(open(path, encoding="utf-8", errors="replace")))
    # A page that was never fetched has no outcome, only a missing one. Training
    # on it would teach the model to avoid links that happened to time out,
    # which is a property of the network and not of the link.
    rows = [r for r in rows if r.get("fetched") == "1"]

    # Whether the page a link sat on had jobs of its own. A button on a page
    # that already lists jobs is usually pagination; the same button on a page
    # with none is the redirect that matters.
    parent_live = {}
    for r in rows:
        parent_live.setdefault(r["parent_url"], False)
    by_url = {r["url"]: r for r in rows}
    for u, r in by_url.items():
        if int(r["n_jobs"] or 0) >= 3:
            parent_live[u] = True

    X, y, g, meta = [], [], [], []
    for r in rows:
        d = int(r["depth"] or 1)
        segs = min(int(r["href_segs"] or 0), 8) / 8.0
        words = min(int(r["txt_words"] or 0), 12) / 12.0
        X.append([
            float(r["txt_list"]), float(r["txt_job"]), float(r["txt_team"]),
            float(r["txt_apply"]), float(r["txt_page"]),
            float(r["href_job"]), float(r["href_query"]),
            1.0 if d == 2 else 0.0, 1.0 if d == 3 else 0.0, 1.0 if d >= 4 else 0.0,
            segs, words,
            0.0 if parent_live.get(r["parent_url"], False) else 1.0,
        ])
        y.append(int(r["label"]))
        g.append(r["domain"])          # group by firm: never score a firm on itself
        meta.append(r)
    return np.array(X), np.array(y), np.array(g), meta


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--file", default="link_outcomes.csv")
    ap.add_argument("--out", default="link_model.json")
    args = ap.parse_args()

    X, y, groups, meta = load(args.file)
    print(f"links with an outcome : {len(y)}")
    print(f"led to a job list     : {y.sum()}  ({y.mean():.1%})")

    # Grouped by firm, because the same site contributes several links and a
    # random split would let the model see one of them at training time.
    cv = GroupKFold(n_splits=5)
    aucs, aps = [], []
    for tr, te in cv.split(X, y, groups):
        m = LogisticRegression(max_iter=2000, C=1.0)
        m.fit(X[tr], y[tr])
        p = m.predict_proba(X[te])[:, 1]
        aucs.append(roc_auc_score(y[te], p))
        aps.append(average_precision_score(y[te], p))
    print(f"\ncross-validated (grouped by firm, 5 folds)")
    print(f"  ROC AUC          {np.mean(aucs):.3f} ± {np.std(aucs):.3f}")
    print(f"  avg precision    {np.mean(aps):.3f} ± {np.std(aps):.3f}   (base rate {y.mean():.3f})")

    model = LogisticRegression(max_iter=2000, C=1.0).fit(X, y)
    coefs = sorted(zip(FEATURES, model.coef_[0]), key=lambda kv: -abs(kv[1]))
    print("\nwhat actually predicts reaching a job list:")
    for k, v in coefs:
        print(f"  {k:<14}{v:+.2f}")

    # Precision at the budget that matters: if a firm gets k fetches, how good
    # are the top k links the model would pick?
    p_all = model.predict_proba(X)[:, 1]
    order = np.argsort(-p_all)
    print("\nranking quality (whole dataset, in-sample — read the CV numbers above for honesty):")
    for k in (500, 1000, 2000, 4000):
        if k <= len(order):
            print(f"  precision@{k:<5} {y[order[:k]].mean():.1%}")

    json.dump({"features": FEATURES,
               "coef": model.coef_[0].tolist(),
               "intercept": float(model.intercept_[0]),
               "n_train": int(len(y)), "base_rate": float(y.mean()),
               "cv_auc": float(np.mean(aucs)), "cv_ap": float(np.mean(aps))},
              open(args.out, "w"), indent=2)
    print(f"\nwrote {args.out}")

    # The plain-language version: which anchor wordings keep their promise.
    print("\nhit rate by anchor wording (n >= 40):")
    buckets: dict[str, list[int]] = {}
    for r in meta:
        t = re.sub(r"\s+", " ", (r["link_text"] or "").strip().lower())[:40]
        if t:
            buckets.setdefault(t, []).append(int(r["label"]))
    ranked = sorted(((t, sum(v) / len(v), len(v)) for t, v in buckets.items() if len(v) >= 40),
                    key=lambda x: -x[1])
    for t, rate, n in ranked[:12]:
        print(f"  {rate:>6.0%}  n={n:<5} {t}")
    print("  ...")
    for t, rate, n in ranked[-8:]:
        print(f"  {rate:>6.0%}  n={n:<5} {t}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
