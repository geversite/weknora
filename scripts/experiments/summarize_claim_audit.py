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
    # A source-supported, in-scope prediction with no gold counterpart. Kept
    # distinct from annotation_error so reviewed metrics can separate a missing
    # gold row from a wrong/underspecified existing gold row.
    "gold_missing_claim",
)

GOLD_REVISION_LABELS = {"annotation_error", "gold_scope_mismatch", "gold_missing_claim"}
MODEL_IMPROVEMENT_LABELS = {"genuine_fn", "genuine_fp", "low_value_fp", "duplicate", "quote_failure"}
LINK_REVIEW_LABELS = {"schema_equivalent", "annotation_error", "gold_missing_claim"}


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
    if label == "gold_missing_claim" or (not row.get("gold_id") and row.get("pred_subject")):
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


def label_consistency_issues(rows: list[dict[str, str]]) -> list[dict[str, str]]:
    """Return a small, non-destructive worklist for labels applied to wrong sides.

    The original audit sheet has one label column for both gold-only and
    prediction-only records. Some otherwise intuitive labels (for example a
    low-value *prediction* on a gold-only row) are directionally impossible.
    We never overwrite a reviewer's choice; this file only makes the handful
    of ambiguous rows explicit before formal metrics are computed.
    """
    issues: list[dict[str, str]] = []
    for row in rows:
        kind = normalized(row.get("row_kind"))
        status = normalized(row.get("match_status"))
        label = normalized(row.get("review_label"))
        if not label:
            continue
        reason = ""
        suggestion = ""
        severity = ""
        if kind == "gold_only" and label in {"low_value_fp", "genuine_fp", "duplicate"}:
            severity = "must_relabel"
            reason = "gold_only 行没有 prediction，不能使用 prediction-side FP/duplicate 标签。"
            suggestion = "genuine_fn（gold 仍在目标范围）或 gold_scope_mismatch（gold 不在当前目标范围）"
        elif kind == "gold_only" and label == "confirm_tp":
            severity = "needs_link_or_relabel"
            reason = "gold_only 行没有自动匹配 prediction，不能直接作为 confirm_tp。"
            suggestion = "schema_equivalent 并在 prediction-side 填链接，或 annotation_error / gold_scope_mismatch"
        elif kind == "prediction_only" and label == "genuine_fn":
            severity = "must_relabel"
            reason = "prediction_only 行没有 gold，不能使用 gold-side FN 标签。"
            suggestion = "gold_missing_claim、schema_equivalent、low_value_fp、duplicate 或 genuine_fp"
        elif kind == "prediction_only" and label == "confirm_tp":
            severity = "needs_link_or_relabel"
            reason = "prediction_only 行没有自动匹配 gold，不能直接作为 confirm_tp。"
            suggestion = "gold_missing_claim（正确且应补 gold）或 schema_equivalent（对应已有 gold）"
        elif status == "same_slot_value_mismatch" and label == "confirm_tp":
            severity = "clarify"
            reason = "系统认为同槽位但 value_norm 不同；需要说明为什么仍计为正确。"
            suggestion = "在 review_note 写明版本/单位/标注值原因；必要时改 annotation_error、schema_equivalent 或 gold_missing_claim"
        elif kind == "prediction_only" and label == "annotation_error":
            severity = "clarify"
            reason = "annotation_error 可能代表 gold 漏标，也可能代表已有 gold 错误，统计口径不同。"
            suggestion = "若 pred 正确且没有对应 gold，改 gold_missing_claim；若已有 gold 本身错误，保留 annotation_error 并写 gold_id"
        if reason:
            issue = make_candidate(row, suggestion)
            issue.update({"severity": severity, "issue": reason})
            issues.append(issue)
    return issues


def build_relabel_template(
    rows: list[dict[str, str]], issues: list[dict[str, str]],
) -> list[dict[str, str]]:
    """Create a safe editable copy of all audit rows with issue guidance.

    The original user-reviewed CSV is never changed. Reviewers filter
    ``label_consistency_severity`` to non-empty, fix only those rows, and pass
    this copy back through --reviewed-csv for a second summary pass.
    """
    issue_by_id = {issue["audit_row_id"]: issue for issue in issues}
    out: list[dict[str, str]] = []
    for row in rows:
        copied = dict(row)
        issue = issue_by_id.get(row["audit_row_id"], {})
        copied["label_consistency_severity"] = issue.get("severity", "")
        copied["label_consistency_issue"] = issue.get("issue", "")
        copied["suggested_label_action"] = issue.get("proposed_action", "")
        out.append(copied)
    return out


def build_prediction_semantic_review(rows: list[dict[str, str]]) -> list[dict[str, str]]:
    """Create the one-directional linking sheet needed for defensible P/R.

    Only prediction-only rows can add a missing semantic match or a new gold
    claim. Gold-only counterparts remain evidence in audit_rows.csv, avoiding
    a reviewer having to link the same equivalence twice.
    """
    out: list[dict[str, str]] = []
    for row in rows:
        if normalized(row.get("row_kind")) != "prediction_only":
            continue
        label = normalized(row.get("review_label"))
        if label not in {"schema_equivalent", "annotation_error", "gold_missing_claim", "confirm_tp"}:
            continue
        recommended_resolution = {
            "schema_equivalent": "equivalent_existing_gold",
            "gold_missing_claim": "add_gold_v2",
            "annotation_error": "classify_gold_missing_or_correct_existing_gold",
            "confirm_tp": "gold_missing_claim_or_schema_equivalent",
        }[label]
        out.append({
            "audit_row_id": row["audit_row_id"],
            "document": row.get("document", ""),
            "review_label": label,
            "recommended_resolution": recommended_resolution,
            "review_resolution": "",  # equivalent_existing_gold | add_gold_v2 | exclude_from_target | model_error
            "pred_index": row.get("pred_index", ""),
            "pred_subject": row.get("pred_subject", ""),
            "pred_predicate": row.get("pred_predicate", ""),
            "pred_value": row.get("pred_value", ""),
            "pred_qualifiers": row.get("pred_qualifiers", ""),
            "pred_quote": row.get("pred_quote", ""),
            "review_link_gold_id": "",
            "include_in_conflict_critical": "",
            "review_note": row.get("review_note", ""),
        })
    return out


def build_semantic_link_rows(rows: list[dict[str, str]]) -> list[dict[str, str]]:
    out: list[dict[str, str]] = []
    for row in rows:
        label = normalized(row.get("review_label"))
        if label not in LINK_REVIEW_LABELS:
            continue
        # A match row already has a direct gold link. For unmatched rows the
        # direction matters: prediction-only rows need a gold link/addition,
        # while gold-only rows need a prediction counterpart identified in the
        # one-directional prediction review sheet.
        kind = normalized(row.get("row_kind"))
        if row.get("gold_id") and row.get("pred_index"):
            link_status = "already_linked"
        elif kind == "prediction_only":
            link_status = "needs_gold_link_or_gold_addition"
        else:
            link_status = "needs_prediction_link"
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
            "count_as_semantic_tp": "yes" if link_status == "already_linked" else "",
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
    consistency_issues: list[dict[str, str]],
    prediction_link_rows: list[dict[str, str]],
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
        f"- `label_consistency_issues.csv`: `{len(consistency_issues)}` small set of directionally ambiguous labels to recheck before metric calculation.",
        "- `audit_rows_relabel.csv`: editable copy of every row with stable audit_row_id and issue guidance; filter label_consistency_severity to fix only the flagged rows.",
        f"- `gold_revision_candidates.csv`: `{len(gold_candidates)}` gold-scope/correction candidates.",
        f"- `schema_equivalence_candidates.csv`: schema/ontology alignment evidence.",
        f"- `model_improvement_cases.csv`: `{len(model_cases)}` recall/precision/dedup/span candidates.",
        f"- `prediction_semantic_review.csv`: `{len(prediction_link_rows)}` prediction-only semantic/gold-addition decisions; this is the only sheet that needs a gold-link/addition decision for human-adjusted P/R.",
        f"- `semantic_link_review.csv`: `{len(link_rows)}` complete cross-check view (both directions).",
        "",
        "## Recommended order",
        "",
        "1. Freeze the reviewed CSV and keep the original gold files unchanged.",
        "2. Open `audit_rows_relabel.csv`, filter `label_consistency_severity` to non-empty, and resolve only those rows; this is a small correction, not a full re-audit.",
        "3. Re-run this summarizer with `--reviewed-csv audit_rows_relabel.csv` into a new output directory.",
        "4. Review `gold_revision_candidates.csv`; record each accepted correction in a versioned gold-v2 change set.",
        "5. Fill `prediction_semantic_review.csv` only if a formal human-adjusted P/R is required.",
        "6. Turn `genuine_fn` / `genuine_fp` rows into prompt or deterministic post-processing regressions.",
        "7. Keep `low_value_fp` and `duplicate` separate from hallucinations in the paper analysis.",
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

        consistency_issues = label_consistency_issues(rows)
        relabel_template = build_relabel_template(rows, consistency_issues)
        prediction_link_rows = build_prediction_semantic_review(rows)
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
                "label_consistency_issues": len(consistency_issues),
                "prediction_semantic_review_rows": len(prediction_link_rows),
                "semantic_link_rows": len(link_rows),
                "semantic_link_rows_needing_gold_link_or_addition": sum(
                    row["link_status"] == "needs_gold_link_or_gold_addition" for row in link_rows
                ),
                "semantic_link_rows_needing_prediction_link": sum(
                    row["link_status"] == "needs_prediction_link" for row in link_rows
                ),
            },
            "metric_guard": {
                "human_adjusted_pr_computed": False,
                "reason": "review labels are row-level; schema-equivalent or annotation rows may require explicit prediction-to-gold links before deduplicated P/R is defensible",
            },
        }

        write_csv(output_dir / "label_consistency_issues.csv", consistency_issues)
        write_csv(output_dir / "audit_rows_relabel.csv", relabel_template)
        write_csv(output_dir / "gold_revision_candidates.csv", gold_candidates)
        write_csv(output_dir / "schema_equivalence_candidates.csv", schema_candidates)
        write_csv(output_dir / "model_improvement_cases.csv", model_cases)
        write_csv(output_dir / "prediction_semantic_review.csv", prediction_link_rows)
        write_csv(output_dir / "semantic_link_review.csv", link_rows)
        write_json(output_dir / "review_summary.json", summary)
        (output_dir / "review_report.md").write_text(
            render_report(
                source, len(rows), reviewed, unreviewed, labels,
                summary["by_row_kind"], summary["by_match_status"],
                gold_candidates, model_cases, link_rows, consistency_issues, prediction_link_rows,
            ),
            encoding="utf-8",
        )

        print(f"Review summary written: {output_dir}")
        print(f"  reviewed rows: {reviewed}/{len(rows)}")
        print("  labels:", ", ".join(f"{label or 'unreviewed'}={count}" for label, count in sorted(labels.items())))
        print(f"  label consistency issues: {len(consistency_issues)}")
        print(f"  gold revision candidates: {len(gold_candidates)}")
        print(f"  model improvement cases: {len(model_cases)}")
        print(f"  prediction semantic links/additions: {len(prediction_link_rows)}")
        return 0
    except ReviewError as exc:
        print(f"[claim-audit-summary] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
