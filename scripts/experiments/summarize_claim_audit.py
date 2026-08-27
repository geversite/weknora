#!/usr/bin/env python3
"""Summarize a manually reviewed C1 audit sheet without inventing P/R.

``audit_rows.csv`` is deliberately row-oriented: a row can be a gold-only
claim, a prediction-only claim, or an automatic match.  Human labels such as
``schema_equivalent`` are therefore strong diagnostic evidence but cannot be
naively added to automatic true positives without linking a prediction-only row
to its intended gold row.  This script turns a reviewed CSV into a reproducible
review package and keeps that statistical distinction explicit.

Example:

  python3 scripts/experiments/summarize_claim_audit.py \
    --audit-dir experiments/runs/<run-id>/claim_audit

It chooses audit_rows_reviewed.csv when present, otherwise audit_rows.csv.
Outputs default to <audit-dir>/review_summary/.
"""

from __future__ import annotations

import argparse
import csv
import json
import shutil
import sys
from collections import Counter, defaultdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


KNOWN_LABELS = (
    "confirm_tp",
    "schema_equivalent",
    "gold_scope_mismatch",
    "genuine_fn",
    "low_value_fp",
    "genuine_fp",
    "duplicate",
    "quote_failure",
    "annotation_error",
)

GOLD_REVISION_LABELS = {"annotation_error", "gold_scope_mismatch"}
MODEL_IMPROVEMENT_LABELS = {"genuine_fn", "genuine_fp", "low_value_fp", "duplicate", "quote_failure"}
LINK_REVIEW_LABELS = {"schema_equivalent", "annotation_error"}


class ReviewError(RuntimeError):
    pass


def write_json(path: Path, data: Any) -> None:
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2, default=str) + "\n", encoding="utf-8")


def read_csv(path: Path) -> list[dict[str, str]]:
    try:
        with path.open(encoding="utf-8-sig", newline="") as handle:
            return list(csv.DictReader(handle))
    except FileNotFoundError as exc:
        raise ReviewError(f"找不到审计 CSV: {path}") from exc


def write_csv(path: Path, rows: list[dict[str, str]], fieldnames: list[str] | None = None) -> None:
    if fieldnames is None:
        fieldnames = []
        for row in rows:
            for key in row:
                if key not in fieldnames:
                    fieldnames.append(key)
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fieldnames, extrasaction="ignore")
        writer.writeheader()
        writer.writerows(rows)


def normalized(value: str | None) -> str:
    return (value or "").strip().lower()


def choose_source(audit_dir: Path, reviewed_csv: str) -> Path:
    if reviewed_csv:
        path = Path(reviewed_csv).expanduser().resolve()
        if not path.is_file():
            raise ReviewError(f"--reviewed-csv 不存在: {path}")
        return path
    reviewed = audit_dir / "audit_rows_reviewed.csv"
    if reviewed.is_file():
        return reviewed
    original = audit_dir / "audit_rows.csv"
    if original.is_file():
        return original
    raise ReviewError(f"目录中既没有 audit_rows_reviewed.csv 也没有 audit_rows.csv: {audit_dir}")


def nested_count(rows: list[dict[str, str]], key1: str, key2: str) -> dict[str, dict[str, int]]:
    result: dict[str, Counter[str]] = defaultdict(Counter)
    for row in rows:
        result[row.get(key1, "") or "(empty)"][row.get(key2, "") or "(empty)"] += 1
    return {outer: dict(inner) for outer, inner in sorted(result.items())}


def make_candidate(row: dict[str, str], action: str) -> dict[str, str]:
    fields = (
        "audit_row_id", "document", "priority", "row_kind", "match_status", "match_tier",
        "review_label", "review_note", "gold_id", "gold_subject", "gold_predicate", "gold_value",
        "gold_qualifiers", "gold_quote", "pred_index", "pred_subject", "pred_predicate", "pred_value",
        "pred_qualifiers", "pred_quote", "key_similarity", "quote_located",
    )
    candidate = {field: row.get(field, "") for field in fields}
    candidate["proposed_action"] = action
    return candidate


def gold_action(label: str, row: dict[str, str]) -> str:
    if label == "gold_scope_mismatch":
        return "reclassify_or_exclude_gold_scope"
    if not row.get("gold_id") and row.get("pred_subject"):
        return "consider_add_missing_gold_claim"
    return "review_and_correct_or_refine_gold_claim"


def model_action(label: str) -> str:
    return {
        "genuine_fn": "extractor_prompt_or_postprocess_recall_case",
        "genuine_fp": "extractor_hallucination_or_binding_case",
        "low_value_fp": "optional_low_value_filter_case",
        "duplicate": "deduplication_case",
        "quote_failure": "quote_span_alignment_case",
    }[label]


def build_semantic_link_rows(rows: list[dict[str, str]]) -> list[dict[str, str]]:
    out: list[dict[str, str]] = []
    for row in rows:
        label = normalized(row.get("review_label"))
        if label not in LINK_REVIEW_LABELS:
            continue
        # A match row already has a direct gold link. Prediction-only and
        # gold-only schema-equivalent rows need a reviewer-provided link before
        # they can safely contribute to a human-adjusted P/R score.
        link_status = "already_linked" if row.get("gold_id") and row.get("pred_index") else "needs_gold_link"
        out.append({
            "audit_row_id": row["audit_row_id"],
            "document": row.get("document", ""),
            "row_kind": row.get("row_kind", ""),
            "review_label": label,
            "link_status": link_status,
            "gold_id": row.get("gold_id", ""),
            "pred_index": row.get("pred_index", ""),
            "gold_subject": row.get("gold_subject", ""),
            "gold_predicate": row.get("gold_predicate", ""),
            "gold_value": row.get("gold_value", ""),
            "pred_subject": row.get("pred_subject", ""),
            "pred_predicate": row.get("pred_predicate", ""),
            "pred_value": row.get("pred_value", ""),
            "review_link_gold_id": row.get("gold_id", "") if link_status == "already_linked" else "",
            "count_as_semantic_tp": "" if link_status == "needs_gold_link" else "yes",
            "include_in_conflict_critical": "",
            "review_note": row.get("review_note", ""),
        })
    return out


def render_report(
    source: Path,
    total: int,
    reviewed: int,
    unreviewed: int,
    label_counts: Counter[str],
    by_kind: dict[str, dict[str, int]],
    by_status: dict[str, dict[str, int]],
    gold_candidates: list[dict[str, str]],
    model_cases: list[dict[str, str]],
    link_rows: list[dict[str, str]],
) -> str:
    def label_table() -> list[str]:
        lines = ["| review_label | rows |", "|---|---:|"]
        for label, count in sorted(label_counts.items(), key=lambda pair: (-pair[1], pair[0])):
            lines.append(f"| `{label or '(unreviewed)'}` | {count} |")
        return lines

    lines = [
        "# C1 Manual Audit Summary",
        "",
        f"- Source CSV: `{source}`",
        f"- Generated: `{datetime.now(timezone.utc).replace(microsecond=0).isoformat()}`",
        f"- Rows: `{total}`; reviewed: `{reviewed}`; unreviewed: `{unreviewed}`",
        "",
        "## Human labels (row-level evidence)",
        "",
        *label_table(),
        "",
        "## What these counts mean",
        "",
        "- `confirm_tp` and `schema_equivalent` show automatic strict/relaxed P/R undercounts semantically useful extraction.",
        "- `genuine_fn` and `genuine_fp` are real model-error evidence.",
        "- `annotation_error` and `gold_scope_mismatch` are corpus/annotation maintenance evidence, not model errors.",
        "- `duplicate` and `low_value_fp` are post-processing/target-scope decisions.",
        "",
        "> Do not add these row counts directly to P/R: an unmatched gold and an unmatched prediction can describe the same semantic fact. `semantic_link_review.csv` records the small set of cross-links required for a defensible human-adjusted metric.",
        "",
        "## Review distribution by row kind",
        "",
        "```json",
        json.dumps(by_kind, ensure_ascii=False, indent=2),
        "```",
        "",
        "## Review distribution by automatic match status",
        "",
        "```json",
        json.dumps(by_status, ensure_ascii=False, indent=2),
        "```",
        "",
        "## Derived worklists",
        "",
        f"- `gold_revision_candidates.csv`: `{len(gold_candidates)}` gold-scope/correction candidates.",
        f"- `model_improvement_cases.csv`: `{len(model_cases)}` recall/precision/dedup/span candidates.",
        f"- `semantic_link_review.csv`: `{len(link_rows)}` schema/annotation rows; only rows marked `needs_gold_link` require a small second linking pass before calculating human-adjusted P/R.",
        "",
        "## Recommended order",
        "",
        "1. Freeze the reviewed CSV and keep the original gold files unchanged.",
        "2. Review `gold_revision_candidates.csv`; record each accepted correction in a versioned gold-v2 change set.",
        "3. Fill only `needs_gold_link` rows in `semantic_link_review.csv` if a formal human-adjusted P/R is required.",
        "4. Turn `genuine_fn` / `genuine_fp` rows into prompt or deterministic post-processing regressions.",
        "5. Keep `low_value_fp` and `duplicate` separate from hallucinations in the paper analysis.",
        "",
    ]
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser(description="Summarize a manually reviewed C1 claim-audit CSV.")
    parser.add_argument("--audit-dir", required=True, help="Directory containing audit_rows.csv")
    parser.add_argument("--reviewed-csv", default="", help="Optional reviewed CSV path; default audit_rows_reviewed.csv then audit_rows.csv")
    parser.add_argument("--output", default="", help="Output directory; default <audit-dir>/review_summary")
    parser.add_argument("--overwrite", action="store_true", help="Allow a non-empty output directory")
    args = parser.parse_args()

    try:
        audit_dir = Path(args.audit_dir).expanduser().resolve()
        if not audit_dir.is_dir():
            raise ReviewError(f"审计目录不存在: {audit_dir}")
        source = choose_source(audit_dir, args.reviewed_csv)
        output_dir = Path(args.output).expanduser().resolve() if args.output else audit_dir / "review_summary"
        if output_dir.exists() and any(output_dir.iterdir()) and not args.overwrite:
            raise ReviewError(f"输出目录已存在且非空: {output_dir}（使用 --overwrite）")
        output_dir.mkdir(parents=True, exist_ok=True)

        rows = read_csv(source)
        for index, row in enumerate(rows, start=1):
            row["audit_row_id"] = f"audit-{index:04d}"
            row["review_label"] = normalized(row.get("review_label"))

        labels = Counter(row["review_label"] for row in rows)
        unknown = sorted(label for label in labels if label and label not in KNOWN_LABELS)
        if unknown:
            raise ReviewError(f"发现未识别的 review_label: {', '.join(unknown)}")
        reviewed = sum(count for label, count in labels.items() if label)
        unreviewed = labels.get("", 0)

        gold_candidates: list[dict[str, str]] = []
        schema_candidates: list[dict[str, str]] = []
        model_cases: list[dict[str, str]] = []
        for row in rows:
            label = row["review_label"]
            if label in GOLD_REVISION_LABELS:
                gold_candidates.append(make_candidate(row, gold_action(label, row)))
            elif label == "schema_equivalent":
                schema_candidates.append(make_candidate(row, "add_equivalence_rule_or_align_gold_schema"))
            elif label in MODEL_IMPROVEMENT_LABELS:
                model_cases.append(make_candidate(row, model_action(label)))

        link_rows = build_semantic_link_rows(rows)
        summary = {
            "source_csv": str(source),
            "generated_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat(),
            "row_count": len(rows),
            "reviewed_row_count": reviewed,
            "unreviewed_row_count": unreviewed,
            "review_label_counts": dict(sorted(labels.items())),
            "by_row_kind": nested_count(rows, "row_kind", "review_label"),
            "by_match_status": nested_count(rows, "match_status", "review_label"),
            "by_document": nested_count(rows, "document", "review_label"),
            "worklist_counts": {
                "gold_revision_candidates": len(gold_candidates),
                "schema_equivalence_candidates": len(schema_candidates),
                "model_improvement_cases": len(model_cases),
                "semantic_link_rows": len(link_rows),
                "semantic_link_rows_needing_gold_link": sum(row["link_status"] == "needs_gold_link" for row in link_rows),
            },
            "metric_guard": {
                "human_adjusted_pr_computed": False,
                "reason": "review labels are row-level; schema-equivalent or annotation rows may require explicit prediction-to-gold links before deduplicated P/R is defensible",
            },
        }

        write_csv(output_dir / "gold_revision_candidates.csv", gold_candidates)
        write_csv(output_dir / "schema_equivalence_candidates.csv", schema_candidates)
        write_csv(output_dir / "model_improvement_cases.csv", model_cases)
        write_csv(output_dir / "semantic_link_review.csv", link_rows)
        write_json(output_dir / "review_summary.json", summary)
        (output_dir / "review_report.md").write_text(
            render_report(
                source, len(rows), reviewed, unreviewed, labels,
                summary["by_row_kind"], summary["by_match_status"],
                gold_candidates, model_cases, link_rows,
            ),
            encoding="utf-8",
        )

        print(f"Review summary written: {output_dir}")
        print(f"  reviewed rows: {reviewed}/{len(rows)}")
        print("  labels:", ", ".join(f"{label or 'unreviewed'}={count}" for label, count in sorted(labels.items())))
        print(f"  gold revision candidates: {len(gold_candidates)}")
        print(f"  model improvement cases: {len(model_cases)}")
        print(f"  semantic links needing review: {summary['worklist_counts']['semantic_link_rows_needing_gold_link']}")
        return 0
    except ReviewError as exc:
        print(f"[claim-audit-summary] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
