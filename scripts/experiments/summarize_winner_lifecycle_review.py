#!/usr/bin/env python3
"""Summarize dual-reviewer C4.9 lifecycle review CSVs without altering them.

The input CSV is emitted by run_winner_lifecycle_eval.py. Reviewers fill
reviewer_1_* and reviewer_2_* independently; an adjudicator may fill
adjudicated_* when they disagree. This tool reports completion, agreement, and
policy correctness only for rows with a decidable final label. It deliberately
does not manufacture a human-accuracy number from blank or unresolved rows.
"""

from __future__ import annotations

import argparse
import collections
import csv
import json
import sys
from pathlib import Path
from typing import Any


LABELS = {
    "correct_winner",
    "correct_no_proposal",
    "wrong_winner",
    "missed_winner",
    "unsafe_action",
    "uncertain",
    "exclude",
}
NON_DECIDABLE = {"", "uncertain", "exclude"}


class ReviewError(RuntimeError):
    """A review file cannot be summarized safely."""


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def clean_label(value: str | None, field: str, line: int) -> str:
    label = (value or "").strip()
    if label and label not in LABELS:
        raise ReviewError(f"line {line}: {field} 非法标签 {label!r}；允许 {sorted(LABELS)}")
    return label


def effective_label(row: dict[str, str], line: int) -> tuple[str, str]:
    adjudicated = clean_label(row.get("adjudicated_label"), "adjudicated_label", line)
    r1 = clean_label(row.get("reviewer_1_label", row.get("review_label", "")), "reviewer_1_label", line)
    r2 = clean_label(row.get("reviewer_2_label"), "reviewer_2_label", line)
    if adjudicated:
        return adjudicated, "adjudicated"
    if r1 and r2:
        return (r1, "agreed") if r1 == r2 else ("", "disagreement")
    if r1 or r2:
        return r1 or r2, "single_reviewer"
    return "", "unreviewed"


def cohen_kappa(pairs: list[tuple[str, str]]) -> float | None:
    if not pairs:
        return None
    count = len(pairs)
    observed = sum(1 for left, right in pairs if left == right) / count
    left_counts = collections.Counter(left for left, _ in pairs)
    right_counts = collections.Counter(right for _, right in pairs)
    labels = set(left_counts) | set(right_counts)
    expected = sum((left_counts[label] / count) * (right_counts[label] / count) for label in labels)
    if expected >= 1:
        return None
    return round((observed - expected) / (1 - expected), 6)


def policy_correct(expected_outcome: str, label: str) -> bool:
    return (
        (expected_outcome == "adopt_reopen" and label == "correct_winner")
        or (expected_outcome == "no_proposal" and label == "correct_no_proposal")
    )


def summarize(rows: list[dict[str, str]]) -> tuple[dict[str, Any], list[dict[str, str]]]:
    if not rows:
        raise ReviewError("review CSV 没有数据行")
    required = {"case_id", "replicate", "expected_outcome", "automation_pass"}
    missing = required - set(rows[0])
    if missing:
        raise ReviewError(f"review CSV 缺少列: {sorted(missing)}")

    resolved_rows: list[dict[str, str]] = []
    labels = collections.Counter[str]()
    review_state = collections.Counter[str]()
    agreement_pairs: list[tuple[str, str]] = []
    decidable = correct = unsafe = 0
    automation_pass = 0
    for line, row in enumerate(rows, start=2):
        expected = (row.get("expected_outcome") or "").strip()
        if expected not in {"adopt_reopen", "no_proposal"}:
            raise ReviewError(f"line {line}: expected_outcome 非法 {expected!r}")
        r1 = clean_label(row.get("reviewer_1_label", row.get("review_label", "")), "reviewer_1_label", line)
        r2 = clean_label(row.get("reviewer_2_label"), "reviewer_2_label", line)
        if r1 and r2:
            agreement_pairs.append((r1, r2))
        label, state = effective_label(row, line)
        labels[label or "unresolved"] += 1
        review_state[state] += 1
        if label not in NON_DECIDABLE:
            decidable += 1
            correct += int(policy_correct(expected, label))
        unsafe += int(label == "unsafe_action")
        automation_pass += int((row.get("automation_pass") or "").strip().lower() == "yes")
        out = dict(row)
        out["effective_label"] = label
        out["review_state"] = state
        out["policy_correct"] = "" if label in NON_DECIDABLE else ("yes" if policy_correct(expected, label) else "no")
        resolved_rows.append(out)

    double_reviewed = len(agreement_pairs)
    exact_agreement = (
        round(sum(1 for left, right in agreement_pairs if left == right) / double_reviewed, 6)
        if double_reviewed else None
    )
    summary = {
        "total_rows": len(rows),
        "automation_pass_rows": automation_pass,
        "review_state_counts": dict(sorted(review_state.items())),
        "effective_label_counts": dict(sorted(labels.items())),
        "dual_reviewer": {
            "rows": double_reviewed,
            "exact_agreement": exact_agreement,
            "cohen_kappa": cohen_kappa(agreement_pairs),
            "note": "Kappa is omitted when no dual-reviewed rows or chance agreement is 1.",
        },
        "manual_policy_accuracy": {
            "decidable_rows": decidable,
            "correct_rows": correct,
            "accuracy": round(correct / decidable, 6) if decidable else None,
            "unsafe_action_rows": unsafe,
            "note": "Only adjudicated/agreed/single reviewed decidable labels are counted. This is not a real-corpus estimate unless the input matrix itself is a documented external corpus.",
        },
        "ready_for_claim": (
            review_state["unreviewed"] == 0
            and review_state["disagreement"] == 0
            and decidable > 0
        ),
    }
    return summary, resolved_rows


def write_markdown(path: Path, review: Path, summary: dict[str, Any]) -> None:
    dual = summary["dual_reviewer"]
    accuracy = summary["manual_policy_accuracy"]
    lines = [
        "# C4.9 winner lifecycle manual review summary", "",
        f"- input: `{review}`",
        f"- rows: `{summary['total_rows']}`",
        f"- ready_for_claim: `{summary['ready_for_claim']}`", "",
        "## Completion", "", "```json",
        json.dumps({
            "review_state_counts": summary["review_state_counts"],
            "effective_label_counts": summary["effective_label_counts"],
        }, ensure_ascii=False, indent=2), "```", "",
        "## Dual-reviewer agreement", "",
        "```json", json.dumps(dual, ensure_ascii=False, indent=2), "```", "",
        "## Manual policy accuracy", "",
        "```json", json.dumps(accuracy, ensure_ascii=False, indent=2), "```", "",
        "## Interpretation boundary", "",
        "A blank, uncertain, excluded, or reviewer-disagreement row is not silently counted as correct. "
        "The reported value is a review-completion metric over this CSV's documented matrix, not a claim of real-document generalization unless the input corpus, annotation protocol, and independent reviewers are documented separately.",
        "",
    ]
    path.write_text("\n".join(lines), encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Summarize a dual-reviewer C4.9 winner lifecycle CSV.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--review", required=True, help="winner_lifecycle_review.csv after reviewer edits")
    parser.add_argument("--output-dir", default="", help="Defaults to <review parent>/review_summary")
    parser.add_argument("--overwrite", action="store_true", help="Allow a non-empty output directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        review = Path(args.review).expanduser().resolve()
        if not review.is_file():
            raise ReviewError(f"review CSV 不存在: {review}")
        with review.open("r", encoding="utf-8-sig", newline="") as handle:
            rows = list(csv.DictReader(handle))
        output = Path(args.output_dir).expanduser().resolve() if args.output_dir else review.parent / "review_summary"
        if output.exists() and any(output.iterdir()) and not args.overwrite:
            raise ReviewError(f"输出目录已存在且非空: {output}（需要 --overwrite）")
        summary, resolved = summarize(rows)
        output.mkdir(parents=True, exist_ok=True)
        json_dump(output / "review_summary.json", summary)
        fields = list(resolved[0].keys())
        with (output / "review_resolved.csv").open("w", encoding="utf-8", newline="") as handle:
            writer = csv.DictWriter(handle, fieldnames=fields)
            writer.writeheader()
            writer.writerows(resolved)
        write_markdown(output / "review_summary.md", review, summary)
        print(f"C4.9 review summary: {output}")
        print(f"  rows: {summary['total_rows']}")
        print(f"  ready_for_claim: {summary['ready_for_claim']}")
        print(f"  manual policy accuracy: {summary['manual_policy_accuracy']['accuracy']}")
        return 0 if summary["ready_for_claim"] else 2
    except ReviewError as exc:
        print(f"[c4.9-review] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
