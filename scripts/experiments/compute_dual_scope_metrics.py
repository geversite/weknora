#!/usr/bin/env python3
"""Compute final broad-maintenance and narrow-conflict metrics after scope review.

This script is intentionally downstream of human review:

* reviewed_metrics.json supplies raw prediction and gold-v1 semantic coverage;
* accepted_semantic_mappings.csv links each accepted prediction;
* gold_v2_scope_review_recommended.csv decides keep/merge/exclude and scope;
* narrow manifest supplies the frozen P1-P5/N1 base critical set.

No LLM, API, database, gold file, or source review CSV is modified.
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


class DualMetricsError(RuntimeError):
    pass


def normalized(value: str | None) -> str:
    return (value or "").strip().lower()


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise DualMetricsError(f"找不到 JSON: {path}") from exc
    except json.JSONDecodeError as exc:
        raise DualMetricsError(f"JSON 无法解析: {path}: {exc}") from exc


def read_csv(path: Path) -> list[dict[str, str]]:
    try:
        with path.open(encoding="utf-8-sig", newline="") as handle:
            return list(csv.DictReader(handle))
    except FileNotFoundError as exc:
        raise DualMetricsError(f"找不到 CSV: {path}") from exc


def write_json(path: Path, data: Any) -> None:
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def ratio(num: int, den: int) -> float | None:
    return round(num / den, 6) if den else None


def main() -> int:
    parser = argparse.ArgumentParser(description="Compute reviewed broad/narrow C1 metrics after gold-v2 scope decisions.")
    parser.add_argument("--reviewed-metrics", required=True, help="reviewed_metrics/reviewed_metrics.json")
    parser.add_argument("--mappings", required=True, help="reviewed_metrics/accepted_semantic_mappings.csv")
    parser.add_argument("--scope-review", required=True, help="gold_v2_scope_review_recommended.csv")
    parser.add_argument("--narrow-manifest", required=True, help="gold-v2-conflict manifest from finalize_gold_v2_scopes.py")
    parser.add_argument("--output", required=True, help="dual_scope_metrics.json output path")
    args = parser.parse_args()

    try:
        metric_path = Path(args.reviewed_metrics).expanduser().resolve()
        mapping_path = Path(args.mappings).expanduser().resolve()
        scope_path = Path(args.scope_review).expanduser().resolve()
        narrow_path = Path(args.narrow_manifest).expanduser().resolve()
        output_path = Path(args.output).expanduser().resolve()
        metrics = read_json(metric_path)
        if not metrics.get("metric_ready"):
            raise DualMetricsError("上游 reviewed_metrics.json 的 metric_ready 不是 true")
        mappings = read_csv(mapping_path)
        scope_rows = read_csv(scope_path)
        narrow = read_json(narrow_path)
        if not mappings or not scope_rows:
            raise DualMetricsError("mappings 或 scope review 没有数据行")

        scope_by_audit: dict[str, dict[str, str]] = {}
        for row in scope_rows:
            audit_id = (row.get("source_audit_row_id") or "").strip()
            if not audit_id or audit_id in scope_by_audit:
                raise DualMetricsError(f"scope review source_audit_row_id 缺失或重复: {audit_id!r}")
            broad = normalized(row.get("broad_maintenance"))
            critical = normalized(row.get("conflict_critical"))
            dedup = normalized(row.get("dedup_decision"))
            if broad not in {"yes", "no"} or critical not in {"yes", "no"} or dedup not in {"keep", "merge", "exclude"}:
                raise DualMetricsError(f"scope review 决策非法: {audit_id}")
            scope_by_audit[audit_id] = {"broad": broad, "critical": critical, "dedup": dedup, "gold_id": row.get("gold_id", "")}

        accepted_v1 = [row for row in mappings if row.get("mapping_type") in {"direct_reviewed_match", "equivalent_existing_gold"}]
        additions = [row for row in mappings if row.get("mapping_type") == "add_gold_v2"]
        if not additions:
            raise DualMetricsError("accepted mappings 中没有 add_gold_v2 行")
        addition_audit_ids = {row.get("audit_row_id", "") for row in additions}
        if addition_audit_ids != set(scope_by_audit):
            missing = sorted(addition_audit_ids - set(scope_by_audit))
            extra = sorted(set(scope_by_audit) - addition_audit_ids)
            parts = []
            if missing:
                parts.append("scope review 缺少 accepted gold-v2 addition: " + ", ".join(missing))
            if extra:
                parts.append("scope review 包含不在 accepted additions 的项: " + ", ".join(extra))
            raise DualMetricsError("; ".join(parts))

        kept_broad = [row for row in additions if scope_by_audit[row["audit_row_id"]]["broad"] == "yes" and scope_by_audit[row["audit_row_id"]]["dedup"] == "keep"]
        kept_narrow = [row for row in kept_broad if scope_by_audit[row["audit_row_id"]]["critical"] == "yes"]
        merged = [row for row in additions if scope_by_audit[row["audit_row_id"]]["dedup"] == "merge"]
        excluded = [row for row in additions if row not in kept_broad and row not in merged]

        v1 = metrics["gold_v1"]
        output = metrics["prediction_output"]
        narrow_base_total = int(narrow.get("base_gold_v1_count", 0))
        narrow_base_ids = set(narrow.get("base_gold_v1_ids") or [])
        v1_covered_critical = int(metrics["conflict_critical"]["covered_v1_gold_claims"])
        if narrow_base_total != len(narrow_base_ids):
            raise DualMetricsError("narrow manifest base count 与 ID 列表不一致")
        if len(kept_narrow) != len(narrow.get("v2_addition_ids") or []):
            raise DualMetricsError("narrow manifest v2 additions 与 scope review 不一致")

        v1_total = int(v1["included_gold_claims"])
        v1_covered = int(v1["covered_gold_claims"])
        raw_predictions = int(output["raw_predictions"])
        accepted_broad = len(accepted_v1) + len(kept_broad)
        broad_gold_total = v1_total + len(kept_broad)
        broad_gold_covered = v1_covered + len(kept_broad)
        duplicate_total = int(output["duplicate_predictions"]) + len(merged)
        excluded_total = int(output["low_value_excluded_predictions"]) + len(excluded)
        projected_den = raw_predictions - duplicate_total - excluded_total

        data = {
            "generated_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat(),
            "metric_ready": True,
            "sources": {
                "reviewed_metrics": str(metric_path),
                "accepted_mappings": str(mapping_path),
                "scope_review": str(scope_path),
                "narrow_manifest": str(narrow_path),
            },
            "broad_maintenance": {
                "base_gold_v1_claims": v1_total,
                "approved_v2_additions": len(kept_broad),
                "total_gold_claims": broad_gold_total,
                "covered_gold_claims": broad_gold_covered,
                "raw_predictions": raw_predictions,
                "accepted_semantic_predictions": accepted_broad,
                "human_adjusted_precision_raw_output": ratio(accepted_broad, raw_predictions),
                "human_adjusted_recall": ratio(broad_gold_covered, broad_gold_total),
                "merged_duplicate_additions": len(merged),
                "excluded_additions": len(excluded),
                "total_duplicate_predictions": duplicate_total,
                "total_low_value_or_excluded_predictions": excluded_total,
                "postfilter_precision_projection": ratio(accepted_broad, projected_den),
                "postfilter_projection_note": "Projection only; it assumes duplicate and low-value/excluded predictions are deterministically filtered. It is not the raw system metric.",
            },
            "narrow_conflict_critical": {
                "base_gold_v1_claims": narrow_base_total,
                "covered_base_gold_v1_claims": v1_covered_critical,
                "approved_v2_additions": len(kept_narrow),
                "total_gold_claims": narrow_base_total + len(kept_narrow),
                "covered_gold_claims": v1_covered_critical + len(kept_narrow),
                "human_adjusted_recall": ratio(v1_covered_critical + len(kept_narrow), narrow_base_total + len(kept_narrow)),
                "precision_available": False,
                "precision_note": "Narrow precision needs every raw prediction (not only gold-v2 additions) to be independently scoped critical/non-critical.",
            },
            "selection": {
                "v1_accepted_prediction_mappings": len(accepted_v1),
                "v2_additions_total_before_scope": len(additions),
                "broad_kept_audit_rows": [row["audit_row_id"] for row in kept_broad],
                "narrow_kept_audit_rows": [row["audit_row_id"] for row in kept_narrow],
                "merged_audit_rows": [row["audit_row_id"] for row in merged],
                "excluded_audit_rows": [row["audit_row_id"] for row in excluded],
            },
        }
        write_json(output_path, data)
        print(f"Dual-scope metrics written: {output_path}")
        print(
            "  broad P/R:", data["broad_maintenance"]["human_adjusted_precision_raw_output"],
            "/", data["broad_maintenance"]["human_adjusted_recall"],
        )
        print("  narrow critical recall:", data["narrow_conflict_critical"]["human_adjusted_recall"])
        return 0
    except DualMetricsError as exc:
        print(f"[dual-scope-metrics] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
