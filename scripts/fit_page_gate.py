#!/usr/bin/env python3
"""Fit the page gate on hand labels instead of guessing its weights.

The gate answers one question: does this page list openings? The parser's
current answer is a hand-weighted score with a threshold picked by eye. This
measures that answer against ground truth, then fits the same features properly
and reports what is actually gained.

Leave-one-out by firm, because 184 pages is small and a held-out fifth would be
36 rows — too few for the difference between two models to mean anything.

Usage
    python3 fit_page_gate.py
    python3 fit_page_gate.py --file page_features.csv --out page_gate_model.json
"""
from __future__ import annotations

import argparse
import csv
import json

import numpy as np
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import roc_auc_score
from sklearn.model_selection import LeaveOneGroupOut
from sklearn.pipeline import make_pipeline
from sklearn.preprocessing import StandardScaler


def make_model():
    """Scale first. median_text_len runs to the thousands while the ratios sit
    in [0,1], so an L2 penalty on the raw columns is almost entirely a penalty
    on the ratios — and the coefficients cannot be read against each other at
    all. Standardising makes both the regularisation and the weights mean what
    they appear to mean."""
    return make_pipeline(
        StandardScaler(),
        LogisticRegression(max_iter=5000, C=0.5, class_weight="balanced"),
    )

FEATURES = [
    "n_groups", "n_members", "link_ratio", "cohesion", "cohesion_deep",
    "median_words", "median_text_len", "loc_ratio", "date_ratio",
    "distinct_ratio", "vocab_in_sig", "job_heading_near", "depth_from_body",
]


def prf(tp: int, fp: int, fn: int) -> tuple[float, float, float]:
    p = tp / max(tp + fp, 1)
    r = tp / max(tp + fn, 1)
    return p, r, 2 * p * r / max(p + r, 1e-9)


def report(name: str, y: np.ndarray, pred: np.ndarray) -> tuple[float, float, float]:
    tp = int(((pred == 1) & (y == 1)).sum())
    fp = int(((pred == 1) & (y == 0)).sum())
    fn = int(((pred == 0) & (y == 1)).sum())
    tn = int(((pred == 0) & (y == 0)).sum())
    p, r, f1 = prf(tp, fp, fn)
    print(f"  {name:<26} P {p:>6.1%}  R {r:>6.1%}  F1 {f1:>6.1%}   "
          f"(TP {tp} FP {fp} FN {fn} TN {tn})")
    return p, r, f1


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--file", default="page_features.csv")
    ap.add_argument("--out", default="page_gate_model.json")
    args = ap.parse_args()

    rows = list(csv.DictReader(open(args.file, encoding="utf-8", errors="replace")))
    X = np.array([[float(r[f]) for f in FEATURES] for r in rows])
    y = np.array([int(r["label"]) for r in rows])
    heur = np.array([float(r["heuristic_score"]) for r in rows])
    extracted = np.array([int(r["extracted"]) for r in rows])
    # Group by firm so a site contributing a landing page and a deep page cannot
    # appear on both sides of a split.
    groups = np.array([r["domain"].split("__l")[0] for r in rows])
    depth = np.array([int(r["depth"]) for r in rows])

    print(f"pages {len(y)}   list openings {y.sum()} ({y.mean():.1%})   firms {len(set(groups))}")
    print("\nBASELINES")
    report("parser as shipped", y, extracted)
    report("always yes", y, np.ones_like(y))

    # What the hand-tuned score alone can do at its best threshold. This is
    # generous to the heuristic: the threshold is chosen with the labels in
    # hand, so it is an upper bound rather than a fair estimate.
    best = max(((f1, t) for t in np.unique(heur)
                for f1 in [prf(int(((heur >= t) & (y == 1)).sum()),
                               int(((heur >= t) & (y == 0)).sum()),
                               int(((heur < t) & (y == 1)).sum()))[2]]),
               default=(0.0, 0.0))
    print(f"\n  best possible threshold on the hand score: {best[1]:.2f} -> F1 {best[0]:.1%}"
          f"   (chosen with the labels, so optimistic)")

    print("\nFITTED, leave-one-firm-out")
    logo = LeaveOneGroupOut()
    oof = np.zeros(len(y))
    for tr, te in logo.split(X, y, groups):
        m = make_model()
        m.fit(X[tr], y[tr])
        oof[te] = m.predict_proba(X[te])[:, 1]
    print(f"  ROC AUC {roc_auc_score(y, oof):.3f}")

    rows_out = []
    for t in (0.35, 0.40, 0.45, 0.50, 0.55, 0.60, 0.65):
        p, r, f1 = report(f"threshold {t:.2f}", y, (oof >= t).astype(int))
        rows_out.append((t, p, r, f1))
    t_best = max(rows_out, key=lambda x: x[3])
    print(f"\n  best out-of-fold F1 {t_best[3]:.1%} at threshold {t_best[0]:.2f}")

    print("\n  by depth, at that threshold:")
    pred = (oof >= t_best[0]).astype(int)
    for d in sorted(set(depth.tolist())):
        m = depth == d
        report(f"depth {d}  (n={m.sum()})", y[m], pred[m])

    model = make_model().fit(X, y)
    scaler, clf = model[0], model[1]
    print("\nwhat the gate is actually keying on (standardised, so comparable):")
    for k, v in sorted(zip(FEATURES, clf.coef_[0]), key=lambda kv: -abs(kv[1])):
        print(f"  {k:<18}{v:+.2f}")

    json.dump({"features": FEATURES, "coef": clf.coef_[0].tolist(),
               "mean": scaler.mean_.tolist(), "scale": scaler.scale_.tolist(),
               "intercept": float(clf.intercept_[0]),
               "threshold": float(t_best[0]), "n_train": int(len(y)),
               "oof_precision": float(t_best[1]), "oof_recall": float(t_best[2]),
               "oof_f1": float(t_best[3]), "oof_auc": float(roc_auc_score(y, oof))},
              open(args.out, "w"), indent=2)
    print(f"\nwrote {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
