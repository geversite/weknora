#!/usr/bin/env python3
"""Score a filled native disputed-fact review CSV.

Precision is defined only on rows with same_fact=yes and human_conflict in
{conflict, no_conflict}. System predicted conflict for every exported row.
Recall is not computed: missed conflicts were never listed.
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from collections import Counter
from pathlib import Path
from typing import Any


VALID_SAME = {"yes", "no", "uncertain"}
VALID_CONFLICT = {"conflict", "no_conflict", "not_comparable", "uncertain"}
DECIDABLE_CONFLICT = {"conflict", "no_conflict"}


class ScoreError(RuntimeError):
    """The review CSV cannot be scored."""


def ratio(num: int, den: int) -> float | None:
    if den <= 0:
        return None
    return num / den


def score_rows(rows: list[dict[str, str]]) -> dict[str, Any]:
    same = Counter()
    human = Counter()
    quality = Counter()
    tp = fp = 0
    unlabeled = 0
    for row in rows:
        same_fact = str(row.get("same_fact") or "").strip().lower()
        conflict = str(row.get("human_conflict") or "").strip().lower()
        cluster_quality = str(row.get("cluster_quality") or "").strip().lower()
        if same_fact:
            same[same_fact] += 1
        if conflict:
            human[conflict] += 1
        if cluster_quality:
            quality[cluster_quality] += 1
        if not same_fact or not conflict:
            unlabeled += 1
            continue
        if same_fact == "yes" and conflict in DECIDABLE_CONFLICT:
            if conflict == "conflict":
                tp += 1
            else:
                fp += 1
    labeled = len(rows) - unlabeled
    precision = ratio(tp, tp + fp)
    return {
        "schema_version": 1,
        "row_count": len(rows),
        "unlabeled_rows": unlabeled,
        "labeled_rows": labeled,
        "same_fact": dict(same),
        "human_conflict": dict(human),
        "cluster_quality": dict(quality),
        "same_fact_decidable_conflict_rows": tp + fp,
        "tp_real_conflict": tp,
        "fp_not_conflict": fp,
        "precision_same_fact_conflict": precision,
        "note": (
            "Precision of system DisputedFacts among human same-fact rows. "
            "Not recall, not enterprise-wide accuracy, not Cohen's kappa "
            "unless two independent reviewers are scored separately."
        ),
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Score a filled native review CSV.")
    parser.add_argument("--review", required=True, help="Filled CSV from export_native_review_csv.py")
    parser.add_argument("--output", default="", help="Optional JSON path for the score")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        path = Path(args.review).expanduser().resolve()
        if not path.is_file():
            raise ScoreError(f"missing review CSV: {path}")
        with path.open(encoding="utf-8", newline="") as handle:
            rows = list(csv.DictReader(handle))
        if not rows:
            raise ScoreError("review CSV has no data rows")
        result = score_rows(rows)
        text = json.dumps(result, ensure_ascii=False, indent=2)
        print(text)
        if args.output:
            out = Path(args.output).expanduser().resolve()
            out.parent.mkdir(parents=True, exist_ok=True)
            out.write_text(text + "\n", encoding="utf-8")
        return 0
    except ScoreError as exc:
        print(f"[native-review-score] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
