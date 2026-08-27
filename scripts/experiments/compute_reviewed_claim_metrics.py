#!/usr/bin/env python3
"""Compute defensible human-reviewed C1 metrics from the audit workbooks.

This consumes two *reviewer-owned* files:

1. audit_rows_relabel.csv — the complete row-level audit with corrected labels;
2. prediction_semantic_review.csv — the smaller sheet that links schema-equivalent
   predictions to existing gold IDs or marks correct new facts for gold-v2.

Unlike a spreadsheet sum, the script deduplicates gold IDs and prediction IDs,
keeps original gold-v1 separate from proposed gold-v2, and reports raw-output
precision separately from a hypothetical post-filtered precision projection.
It never modifies source review sheets or gold files.

Example:

  python3 scripts/experiments/compute_reviewed_claim_metrics.py \
    --audit-csv experiments/runs/<run>/claim_audit/review_summary/audit_rows_relabel.csv \
    --semantic-review experiments/runs/<run>/claim_audit/review_summary_v2/prediction_semantic_review.csv
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from collections import Counter, defaultdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


KNOWN_LABELS = {
    "confirm_tp",
    "schema_equivalent",
    "gold_scope_mismatch",
    "gold_missing_claim",
    "genuine_fn",
    "low_value_fp",
    "genuine_fp",
    "duplicate",
    "quote_failure",
    "annotation_error",
}

VALID_RESOLUTIONS = {
    "equivalent_existing_gold",
    "add_gold_v2",
    "exclude_from_target",
    "model_error",
}


class MetricsError(RuntimeError):
    pass


def normalized(value: str | None) -> str:
    return (value or "").strip().lower()


def read_csv(path: Path) -> list[dict[str, str]]:
    try:
        with path.open(encoding="utf-8-sig", newline="") as handle:
            rows = list(csv.DictReader(handle))
    except FileNotFoundError as exc:
        raise MetricsError(f"找不到 CSV: {path}") from exc
    if not rows:
        raise MetricsError(f"CSV 没有数据行: {path}")
    return rows


def write_json(path: Path, data: Any) -> None:
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2, default=str) + "\n", encoding="utf-8")


def write_csv(path: Path, rows: list[dict[str, str]]) -> None:
    fields: list[str] = []
    for row in rows:
        for field in row:
            if field not in fields:
                fields.append(field)
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields, extrasaction="ignore")
        writer.writeheader()
        writer.writerows(rows)


def prediction_key(row: dict[str, str]) -> str:
    document = (row.get("document") or "").strip()
    index = (row.get("pred_index") or "").strip()
    return f"{document}#{index}" if document and index else ""


def ratio(numerator: int, denominator: int) -> float | None:
    return round(numerator / denominator, 6) if denominator else None


def row_excerpt(row: dict[str, str]) -> dict[str, str]:
    fields = (
        "audit_row_id", "document", "row_kind", "match_status", "match_tier", "priority",
        "review_label", "review_note", "gold_id", "gold_subject", "gold_predicate", "gold_value",
        "pred_index", "pred_subject", "pred_predicate", "pred_value", "pred_quote",
    )
    return {field: row.get(field, "") for field in fields}


def load_audit_rows(path: Path) -> list[dict[str, str]]:
    rows = read_csv(path)
    required = {"audit_row_id", "row_kind", "match_status", "review_label", "document"}
    missing = sorted(required - set(rows[0]))
    if missing:
        raise MetricsError(
            f"审计 CSV 缺少列 {', '.join(missing)}；请使用 review_summary/audit_rows_relabel.csv，而不是原始 audit_rows.csv"
        )
    ids: set[str] = set()
    for row in rows:
        row["review_label"] = normalized(row.get("review_label"))
        if row["review_label"] and row["review_label"] not in KNOWN_LABELS:
            raise MetricsError(f"未知 review_label: {row['review_label']} (row={row.get('audit_row_id')})")
        row_id = row.get("audit_row_id", "")
        if not row_id or row_id in ids:
            raise MetricsError(f"audit_row_id 缺失或重复: {row_id!r}")
        ids.add(row_id)
    return rows


def load_semantic_rows(path: Path) -> list[dict[str, str]]:
    rows = read_csv(path)
    required = {"audit_row_id", "review_label", "review_resolution", "review_link_gold_id"}
    missing = sorted(required - set(rows[0]))
    if missing:
        raise MetricsError(f"语义链接 CSV 缺少列: {', '.join(missing)}")
    for row in rows:
        row["review_label"] = normalized(row.get("review_label"))
        row["review_resolution"] = normalized(row.get("review_resolution"))
        row["include_in_conflict_critical"] = normalized(row.get("include_in_conflict_critical"))
    return rows


def build_metrics(
    audit_rows: list[dict[str, str]], semantic_rows: list[dict[str, str]],
) -> tuple[dict[str, Any], list[dict[str, str]], list[dict[str, str]], list[dict[str, str]]]:
    by_audit_id = {row["audit_row_id"]: row for row in audit_rows}
    original_gold = {row["gold_id"] for row in audit_rows if row.get("gold_id")}
    all_predictions = {prediction_key(row) for row in audit_rows if prediction_key(row)}
    original_gold_scope_excluded = {
        row["gold_id"]
        for row in audit_rows
        if row.get("gold_id") and row["review_label"] == "gold_scope_mismatch"
    }
    critical_gold = {
        row["gold_id"]
        for row in audit_rows
        if row.get("gold_id") and normalized(row.get("priority")) == "critical"
    }

    issues: list[dict[str, str]] = []
    mappings: list[dict[str, str]] = []
    gold_v2_additions: list[dict[str, str]] = []
    accepted_prediction_to_gold: dict[str, str] = {}
    accepted_v1_gold: set[str] = set()
    accepted_v2_additions: set[str] = set()
    excluded_predictions: set[str] = set()
    resolution_by_prediction: dict[str, str] = {}

    # Direct automatic matches that the reviewer confirmed. These pairs are
    # already one-to-one in audit_rows and need no second semantic-link sheet.
    for row in audit_rows:
        if normalized(row.get("row_kind")) != "match":
            continue
        if row["review_label"] not in {"confirm_tp", "schema_equivalent"}:
            continue
        gold_id = row.get("gold_id", "")
        pred_id = prediction_key(row)
        if not gold_id or not pred_id:
            issues.append({**row_excerpt(row), "issue_type": "invalid_direct_match", "issue": "match 行缺少 gold_id 或 pred_index"})
            continue
        accepted_prediction_to_gold[pred_id] = gold_id
        accepted_v1_gold.add(gold_id)
        resolution_by_prediction[pred_id] = "direct_reviewed_match"
        mappings.append({
            "mapping_type": "direct_reviewed_match",
            "audit_row_id": row["audit_row_id"],
            "prediction_key": pred_id,
            "gold_id": gold_id,
            "include_in_conflict_critical": "yes" if gold_id in critical_gold else "no",
            "review_note": row.get("review_note", ""),
        })

    # Process the one-directional reviewer decisions for prediction-only rows.
    seen_semantic_audit_ids: set[str] = set()
    for semantic in semantic_rows:
        audit_id = semantic.get("audit_row_id", "")
        if not audit_id:
            issues.append({"issue_type": "missing_audit_row_id", "issue": "semantic review 行缺少 audit_row_id"})
            continue
        if audit_id in seen_semantic_audit_ids:
            issues.append({"audit_row_id": audit_id, "issue_type": "duplicate_semantic_review", "issue": "同一 audit_row_id 在 semantic review 中出现多次"})
            continue
        seen_semantic_audit_ids.add(audit_id)
        audit = by_audit_id.get(audit_id)
        if audit is None:
            issues.append({"audit_row_id": audit_id, "issue_type": "unknown_audit_row", "issue": "semantic review 引用的 audit_row_id 不存在"})
            continue
        if normalized(audit.get("row_kind")) != "prediction_only":
            issues.append({**row_excerpt(audit), "issue_type": "wrong_link_direction", "issue": "semantic review 只能链接 prediction_only 行"})
            continue
        pred_id = prediction_key(audit)
        resolution = semantic["review_resolution"]
        critical = semantic["include_in_conflict_critical"]
        if critical not in {"", "yes", "no"}:
            issues.append({**row_excerpt(audit), "issue_type": "invalid_critical_flag", "issue": "include_in_conflict_critical 只能填 yes/no/空"})
        if not resolution:
            issues.append({**row_excerpt(audit), "issue_type": "unresolved_semantic_prediction", "issue": "prediction-only 语义行尚未填写 review_resolution"})
            continue
        if resolution not in VALID_RESOLUTIONS:
            issues.append({**row_excerpt(audit), "issue_type": "invalid_resolution", "issue": f"未知 review_resolution: {resolution}"})
            continue
        if resolution == "equivalent_existing_gold":
            gold_id = (semantic.get("review_link_gold_id") or "").strip()
            if not gold_id:
                issues.append({**row_excerpt(audit), "issue_type": "missing_gold_link", "issue": "equivalent_existing_gold 必须填写 review_link_gold_id"})
                continue
            if gold_id not in original_gold:
                issues.append({**row_excerpt(audit), "issue_type": "unknown_gold_link", "issue": f"review_link_gold_id 不在 gold-v1: {gold_id}"})
                continue
            prior = accepted_prediction_to_gold.get(pred_id)
            if prior and prior != gold_id:
                issues.append({**row_excerpt(audit), "issue_type": "prediction_link_conflict", "issue": f"prediction 已链接到 {prior}，不能再链接 {gold_id}"})
                continue
            accepted_prediction_to_gold[pred_id] = gold_id
            accepted_v1_gold.add(gold_id)
            resolution_by_prediction[pred_id] = resolution
            mappings.append({
                "mapping_type": resolution,
                "audit_row_id": audit_id,
                "prediction_key": pred_id,
                "gold_id": gold_id,
                "include_in_conflict_critical": critical or ("yes" if gold_id in critical_gold else "no"),
                "review_note": semantic.get("review_note", ""),
            })
        elif resolution == "add_gold_v2":
            addition_id = f"gold-v2:{audit_id}"
            accepted_v2_additions.add(addition_id)
            resolution_by_prediction[pred_id] = resolution
            mappings.append({
                "mapping_type": resolution,
                "audit_row_id": audit_id,
                "prediction_key": pred_id,
                "gold_id": addition_id,
                "include_in_conflict_critical": critical,
                "review_note": semantic.get("review_note", ""),
            })
            gold_v2_additions.append({
                "proposed_gold_id": addition_id,
                "source_audit_row_id": audit_id,
                "document": audit.get("document", ""),
                "subject": audit.get("pred_subject", ""),
                "predicate": audit.get("pred_predicate", ""),
                "value": audit.get("pred_value", ""),
                "qualifiers": audit.get("pred_qualifiers", ""),
                "quote": audit.get("pred_quote", ""),
                "include_in_conflict_critical": critical,
                "review_note": semantic.get("review_note", ""),
                "proposed_action": "add_to_gold_v2_after_second_review",
            })
        elif resolution == "exclude_from_target":
            excluded_predictions.add(pred_id)
            resolution_by_prediction[pred_id] = resolution
        elif resolution == "model_error":
            resolution_by_prediction[pred_id] = resolution

    # Prediction-only special rows that were not in semantic review are
    # unambiguously output-hygiene/model-error evidence.
    for row in audit_rows:
        if normalized(row.get("row_kind")) != "prediction_only":
            continue
        pred_id = prediction_key(row)
        if not pred_id or pred_id in resolution_by_prediction:
            continue
        label = row["review_label"]
        if label == "low_value_fp":
            excluded_predictions.add(pred_id)
            resolution_by_prediction[pred_id] = "low_value_fp"
        elif label in {"duplicate", "genuine_fp", "quote_failure"}:
            resolution_by_prediction[pred_id] = label
        elif label in {"schema_equivalent", "gold_missing_claim", "annotation_error", "confirm_tp"}:
            issues.append({**row_excerpt(row), "issue_type": "missing_semantic_resolution", "issue": "该 prediction-only 标签需要出现在 prediction_semantic_review.csv 并填写 resolution"})
        else:
            issues.append({**row_excerpt(row), "issue_type": "unclassified_prediction", "issue": f"prediction-only 标签尚不能计算: {label or '(empty)'}"})

    # A gold-only schema-equivalent row must be covered by an explicitly linked
    # prediction. A true FN remains an uncovered v1 gold item by design.
    for row in audit_rows:
        if normalized(row.get("row_kind")) != "gold_only":
            continue
        gold_id = row.get("gold_id", "")
        label = row["review_label"]
        if label == "schema_equivalent" and gold_id not in accepted_v1_gold:
            issues.append({**row_excerpt(row), "issue_type": "uncovered_schema_gold", "issue": "gold-only schema_equivalent 未被任何 prediction link 覆盖"})
        elif label == "confirm_tp":
            issues.append({**row_excerpt(row), "issue_type": "gold_only_confirm_tp", "issue": "gold-only 不应保留 confirm_tp；请改为 schema_equivalent/genuine_fn/gold_scope_mismatch"})

    # Multiple accepted predictions mapped to one v1 gold are allowed only as
    # an explicit duplicate; otherwise human-adjusted precision is ambiguous.
    gold_to_predictions: dict[str, list[str]] = defaultdict(list)
    for pred_id, gold_id in accepted_prediction_to_gold.items():
        gold_to_predictions[gold_id].append(pred_id)
    for gold_id, pred_ids in gold_to_predictions.items():
        if len(pred_ids) > 1:
            issues.append({
                "issue_type": "multiple_predictions_one_gold",
                "gold_id": gold_id,
                "prediction_keys": ";".join(sorted(pred_ids)),
                "issue": "多个 accepted prediction 指向同一 gold；确认其中额外项是否应标 duplicate",
            })

    included_v1_gold = original_gold - original_gold_scope_excluded
    accepted_predictions = set(accepted_prediction_to_gold) | {
        item["prediction_key"] for item in mappings if item["mapping_type"] == "add_gold_v2"
    }
    unresolved_predictions = all_predictions - set(resolution_by_prediction) - set(accepted_prediction_to_gold)
    # Direct accepted mappings are in resolution_by_prediction, but retain this
    # union for clarity in case future input columns differ.
    unresolved_predictions -= accepted_predictions

    raw_prediction_count = len(all_predictions)
    raw_semantic_precision = ratio(len(accepted_predictions), raw_prediction_count)
    gold_v1_recall = ratio(len(accepted_v1_gold & included_v1_gold), len(included_v1_gold))
    proposed_gold_v2_total = len(included_v1_gold) + len(accepted_v2_additions)
    proposed_gold_v2_covered = len(accepted_v1_gold & included_v1_gold) + len(accepted_v2_additions)
    proposed_gold_v2_precision = ratio(len(accepted_predictions), raw_prediction_count)
    proposed_gold_v2_recall = ratio(proposed_gold_v2_covered, proposed_gold_v2_total)

    hygiene_filtered_denominator = raw_prediction_count - len(excluded_predictions) - sum(
        1 for pred in all_predictions if resolution_by_prediction.get(pred) == "duplicate"
    )
    postfilter_precision_projection = ratio(len(accepted_predictions), hygiene_filtered_denominator)

    # Priority=critical identifies the planted P1-P5/N1 target claims already
    # represented in the original audit. New gold-v2 additions enter this
    # subset only when the reviewer explicitly writes yes.
    critical_v1_gold = critical_gold - original_gold_scope_excluded
    critical_additions = {
        item["gold_id"] for item in mappings
        if item["mapping_type"] == "add_gold_v2" and item.get("include_in_conflict_critical") == "yes"
    }
    covered_critical_v1 = accepted_v1_gold & critical_v1_gold
    critical_recall = ratio(
        len(covered_critical_v1) + len(critical_additions),
        len(critical_v1_gold) + len(critical_additions),
    )

    label_counts = Counter(row["review_label"] for row in audit_rows)
    metrics = {
        "generated_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat(),
        "metric_ready": len(issues) == 0 and not unresolved_predictions,
        "input": {
            "audit_rows": len(audit_rows),
            "semantic_review_rows": len(semantic_rows),
            "review_label_counts": dict(sorted(label_counts.items())),
        },
        "gold_v1": {
            "total_gold_claims": len(original_gold),
            "scope_excluded_gold_claims": len(original_gold_scope_excluded),
            "included_gold_claims": len(included_v1_gold),
            "covered_gold_claims": len(accepted_v1_gold & included_v1_gold),
            "human_semantic_recall": gold_v1_recall,
        },
        "proposed_gold_v2": {
            "added_gold_claims": len(accepted_v2_additions),
            "total_gold_claims": proposed_gold_v2_total,
            "covered_gold_claims": proposed_gold_v2_covered,
            "human_adjusted_precision_raw_output": proposed_gold_v2_precision,
            "human_adjusted_recall": proposed_gold_v2_recall,
        },
        "prediction_output": {
            "raw_predictions": raw_prediction_count,
            "accepted_semantic_predictions": len(accepted_predictions),
            "raw_semantic_precision_against_proposed_gold_v2": raw_semantic_precision,
            "duplicate_predictions": sum(1 for pred in all_predictions if resolution_by_prediction.get(pred) == "duplicate"),
            "low_value_excluded_predictions": len(excluded_predictions),
            "genuine_fp_predictions": sum(1 for pred in all_predictions if resolution_by_prediction.get(pred) in {"genuine_fp", "model_error"}),
            "unresolved_predictions": len(unresolved_predictions),
            "postfilter_precision_projection": postfilter_precision_projection,
            "postfilter_projection_note": "Projection only: assumes rows labelled duplicate/low_value_fp or explicitly exclude_from_target are removed deterministically. It is not the current raw-system precision.",
        },
        "conflict_critical": {
            "v1_gold_claims_identified_by_priority": len(critical_v1_gold),
            "covered_v1_gold_claims": len(covered_critical_v1),
            "gold_v2_added_claims_marked_critical": len(critical_additions),
            "human_adjusted_recall": critical_recall,
            "precision_available": False,
            "precision_note": "Critical precision requires every prediction to be scoped critical/non-critical, not only prediction-only review rows.",
        },
        "validation": {
            "issue_count": len(issues),
            "unresolved_prediction_count": len(unresolved_predictions),
            "formal_metric_note": "Use proposed_gold_v2 human-adjusted metrics only when metric_ready=true. Gold-v2 additions remain proposed until gold files are versioned and independently reviewed.",
        },
    }
    return metrics, issues, mappings, gold_v2_additions


def render_report(metrics: dict[str, Any], issues: list[dict[str, str]]) -> str:
    v1 = metrics["gold_v1"]
    v2 = metrics["proposed_gold_v2"]
    pred = metrics["prediction_output"]
    critical = metrics["conflict_critical"]
    validation = metrics["validation"]
    ready = "✅ READY" if metrics["metric_ready"] else "⚠️ NOT READY"
    return f"""# C1 Human-Reviewed Metrics

## Status

{ready}

- Validation issues: `{validation['issue_count']}`
- Unresolved predictions: `{validation['unresolved_prediction_count']}`
- Rule: {validation['formal_metric_note']}

## Gold-v1 semantic coverage

| Metric | Value |
|---|---:|
| Included gold-v1 claims | {v1['included_gold_claims']} |
| Covered gold-v1 claims | {v1['covered_gold_claims']} |
| Human semantic recall | {v1['human_semantic_recall']} |

## Proposed gold-v2 metrics

| Metric | Value |
|---|---:|
| Proposed added gold claims | {v2['added_gold_claims']} |
| Proposed gold-v2 total | {v2['total_gold_claims']} |
| Covered gold-v2 claims | {v2['covered_gold_claims']} |
| Human-adjusted raw-output precision | {v2['human_adjusted_precision_raw_output']} |
| Human-adjusted recall | {v2['human_adjusted_recall']} |

## Output hygiene

| Metric | Value |
|---|---:|
| Raw predictions | {pred['raw_predictions']} |
| Accepted semantic predictions | {pred['accepted_semantic_predictions']} |
| Duplicates | {pred['duplicate_predictions']} |
| Low-value/excluded predictions | {pred['low_value_excluded_predictions']} |
| Genuine model FP | {pred['genuine_fp_predictions']} |
| Projected precision after deterministic hygiene filters | {pred['postfilter_precision_projection']} |

> {pred['postfilter_projection_note']}

## Conflict-critical subset

| Metric | Value |
|---|---:|
| Critical gold-v1 claims | {critical['v1_gold_claims_identified_by_priority']} |
| Covered critical gold-v1 claims | {critical['covered_v1_gold_claims']} |
| Added gold-v2 claims marked critical | {critical['gold_v2_added_claims_marked_critical']} |
| Human-adjusted critical recall | {critical['human_adjusted_recall']} |

> {critical['precision_note']}

## Files

- `reviewed_metrics.json`: machine-readable output.
- `accepted_semantic_mappings.csv`: every prediction→gold/gold-v2 mapping used.
- `gold_v2_additions.csv`: proposed gold-v2 additions; do not merge automatically.
- `metric_validation_issues.csv`: must be empty before treating adjusted metrics as formal.
"""


def main() -> int:
    parser = argparse.ArgumentParser(description="Compute human-reviewed C1 extraction metrics.")
    parser.add_argument("--audit-csv", required=True, help="review_summary/audit_rows_relabel.csv")
    parser.add_argument("--semantic-review", required=True, help="completed prediction_semantic_review.csv")
    parser.add_argument("--output", default="", help="Output directory; default beside semantic review as reviewed_metrics")
    parser.add_argument("--overwrite", action="store_true", help="Allow non-empty output directory")
    args = parser.parse_args()

    try:
        audit_path = Path(args.audit_csv).expanduser().resolve()
        semantic_path = Path(args.semantic_review).expanduser().resolve()
        output_dir = Path(args.output).expanduser().resolve() if args.output else semantic_path.parent / "reviewed_metrics"
        if output_dir.exists() and any(output_dir.iterdir()) and not args.overwrite:
            raise MetricsError(f"输出目录已存在且非空: {output_dir}（使用 --overwrite）")
        output_dir.mkdir(parents=True, exist_ok=True)

        audit_rows = load_audit_rows(audit_path)
        semantic_rows = load_semantic_rows(semantic_path)
        metrics, issues, mappings, additions = build_metrics(audit_rows, semantic_rows)

        write_json(output_dir / "reviewed_metrics.json", metrics)
        write_csv(output_dir / "metric_validation_issues.csv", issues)
        write_csv(output_dir / "accepted_semantic_mappings.csv", mappings)
        write_csv(output_dir / "gold_v2_additions.csv", additions)
        (output_dir / "reviewed_metrics.md").write_text(render_report(metrics, issues), encoding="utf-8")

        print(f"Reviewed metrics written: {output_dir}")
        print(f"  metric_ready: {metrics['metric_ready']}")
        print(f"  gold-v1 semantic recall: {metrics['gold_v1']['human_semantic_recall']}")
        print(f"  proposed gold-v2 precision/recall: {metrics['proposed_gold_v2']['human_adjusted_precision_raw_output']} / {metrics['proposed_gold_v2']['human_adjusted_recall']}")
        print(f"  validation issues: {len(issues)}")
        return 0 if metrics["metric_ready"] else 2
    except MetricsError as exc:
        print(f"[reviewed-metrics] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
