#!/usr/bin/env python3
"""Apply a transparent, versioned dual-scope recommendation to a review CSV.

The recommendation file is deliberately committed beside this script. The
source review CSV is never modified; a new CSV containing broad-maintenance,
conflict-critical and dedup decisions is written for reviewer inspection.
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_RECOMMENDATIONS = ROOT / "scripts/experiments/gold_v2_scope_recommendations.json"
REQUIRED_DECISION_FIELDS = ("broad_maintenance", "conflict_critical", "dedup_decision", "merge_into_gold_id", "review_note")


class RecommendationError(RuntimeError):
    pass


def read_json(path: Path) -> dict[str, Any]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise RecommendationError(f"找不到推荐文件: {path}") from exc
    except json.JSONDecodeError as exc:
        raise RecommendationError(f"推荐 JSON 无法解析: {path}: {exc}") from exc
    if not isinstance(data, dict) or not isinstance(data.get("recommendations"), dict):
        raise RecommendationError(f"推荐 JSON 缺少 recommendations 对象: {path}")
    return data


def read_csv(path: Path) -> tuple[list[dict[str, str]], list[str]]:
    try:
        with path.open(encoding="utf-8-sig", newline="") as handle:
            reader = csv.DictReader(handle)
            return list(reader), list(reader.fieldnames or [])
    except FileNotFoundError as exc:
        raise RecommendationError(f"找不到 scope review CSV: {path}") from exc


def write_csv(path: Path, rows: list[dict[str, str]], fields: list[str]) -> None:
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields, extrasaction="ignore")
        writer.writeheader()
        writer.writerows(rows)


def main() -> int:
    parser = argparse.ArgumentParser(description="Apply committed gold-v2 dual-scope recommendations to a review sheet.")
    parser.add_argument("--review", required=True, help="gold_v2_scope_review.csv input")
    parser.add_argument("--output", required=True, help="new recommended CSV output")
    parser.add_argument("--recommendations", default=str(DEFAULT_RECOMMENDATIONS))
    parser.add_argument("--overwrite", action="store_true", help="Allow overwriting an existing output CSV")
    args = parser.parse_args()

    try:
        review_path = Path(args.review).expanduser().resolve()
        output_path = Path(args.output).expanduser().resolve()
        recommendation_doc = read_json(Path(args.recommendations).expanduser().resolve())
        recommendations = recommendation_doc["recommendations"]
        rows, fields = read_csv(review_path)
        if not rows or "gold_id" not in fields:
            raise RecommendationError("scope review CSV 缺少数据或 gold_id 列")
        if output_path.exists() and not args.overwrite:
            raise RecommendationError(f"输出文件已存在: {output_path}（使用 --overwrite）")

        actual_ids = {str(row.get("gold_id", "")).strip() for row in rows}
        expected_ids = set(recommendations)
        missing = sorted(expected_ids - actual_ids)
        extra = sorted(actual_ids - expected_ids)
        if missing or extra:
            parts = []
            if missing:
                parts.append("CSV 缺少推荐项: " + ", ".join(missing))
            if extra:
                parts.append("CSV 出现未知项: " + ", ".join(extra))
            raise RecommendationError("; ".join(parts))

        for field in ("recommendation_version", "recommendation_note"):
            if field not in fields:
                fields.append(field)
        for row in rows:
            gold_id = str(row["gold_id"]).strip()
            decision = recommendations[gold_id]
            absent = [field for field in REQUIRED_DECISION_FIELDS if field not in decision]
            if absent:
                raise RecommendationError(f"推荐 {gold_id} 缺少字段: {', '.join(absent)}")
            for field in REQUIRED_DECISION_FIELDS:
                row[field] = str(decision[field])
            row["recommendation_version"] = str(recommendation_doc.get("version", ""))
            row["recommendation_note"] = str(decision["review_note"])

        output_path.parent.mkdir(parents=True, exist_ok=True)
        write_csv(output_path, rows, fields)
        broad = sum(row["broad_maintenance"] == "yes" and row["dedup_decision"] == "keep" for row in rows)
        narrow = sum(row["conflict_critical"] == "yes" and row["dedup_decision"] == "keep" for row in rows)
        merged = sum(row["dedup_decision"] == "merge" for row in rows)
        excluded = sum(row["dedup_decision"] == "exclude" for row in rows)
        print(f"Recommended scope review written: {output_path}")
        print(f"  broad kept: {broad}; narrow kept: {narrow}; merged: {merged}; excluded: {excluded}")
        return 0
    except RecommendationError as exc:
        print(f"[gold-v2-scope-recommendation] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
