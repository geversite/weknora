#!/usr/bin/env python3
"""Prepare a nine-row dual-scope/dedup review sheet for a gold-v2 candidate.

A research corpus needs two distinct scopes:

* broad-maintenance: source-grounded facts useful to an evolving wiki;
* narrow-conflict-critical: facts whose changed value/status/time/scope should
  directly enter the claim-level conflict metric.

This script reads materialize_gold_v2.py's provenance and candidate JSON files,
then emits a reviewer worksheet. It never modifies the candidate corpus.
"""

from __future__ import annotations

import argparse
import csv
import json
import re
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any


class ScopeReviewError(RuntimeError):
    pass


# These are suggestions only. They deliberately err on the side of asking a
# reviewer rather than silently excluding a fact from the broad wiki scope.
NARROW_NO_PREDICATES = ("别名", "代号", "发布机构", "全称")
NARROW_REVIEW_PREDICATES = ("用途", "目标", "定义", "验证目标", "资源范围")
NARROW_YES_HINTS = (
    "上限", "下限", "时限", "标准", "状态", "供应", "权限", "班次", "时间",
    "日期", "周期", "比例", "人数", "费用", "补贴", "半径", "温度", "副作用",
)


def normalized(value: str | None) -> str:
    return (value or "").strip()


def norm_slot(subject: str, predicate: str) -> str:
    value = (subject + predicate).lower()
    return re.sub(r"\s+|[，,。、“”\"'（）()]", "", value)


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ScopeReviewError(f"找不到文件: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ScopeReviewError(f"JSON 无法解析: {path}: {exc}") from exc


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


def narrow_suggestion(predicate: str) -> str:
    p = normalized(predicate)
    if any(token in p for token in NARROW_NO_PREDICATES):
        return "no"
    if any(token in p for token in NARROW_REVIEW_PREDICATES):
        return "review"
    if any(token in p for token in NARROW_YES_HINTS):
        return "yes"
    return "review"


def main() -> int:
    parser = argparse.ArgumentParser(description="Prepare a dual broad-maintenance / narrow-conflict gold-v2 review sheet.")
    parser.add_argument("--candidate-dir", required=True, help="gold_v2_candidate_reviewed directory")
    parser.add_argument("--output", required=True, help="CSV worksheet output path")
    parser.add_argument("--overwrite", action="store_true", help="Allow overwriting an existing output CSV")
    args = parser.parse_args()

    try:
        candidate_dir = Path(args.candidate_dir).expanduser().resolve()
        output_path = Path(args.output).expanduser().resolve()
        provenance_path = candidate_dir / "provenance.json"
        if not candidate_dir.is_dir() or not provenance_path.is_file():
            raise ScopeReviewError(f"candidate directory/provenance 不存在: {candidate_dir}")
        if output_path.exists() and not args.overwrite:
            raise ScopeReviewError(f"输出文件已存在: {output_path}（使用 --overwrite）")
        provenance = read_json(provenance_path)
        entries = provenance.get("entries") or []
        if not entries:
            raise ScopeReviewError("provenance 中没有新增 gold-v2 entries")

        claims_by_id: dict[str, dict[str, Any]] = {}
        for path in candidate_dir.glob("doc*.json"):
            data = read_json(path)
            for claim in data.get("claims") or []:
                claim_id = normalized(claim.get("id"))
                if claim_id:
                    claims_by_id[claim_id] = claim

        slot_groups: dict[tuple[str, str], list[str]] = defaultdict(list)
        for entry in entries:
            slot_groups[(normalized(entry.get("gold_doc")), norm_slot(entry.get("subject", ""), entry.get("predicate", "")))].append(
                normalized(entry.get("gold_id"))
            )

        rows: list[dict[str, str]] = []
        for entry in entries:
            gold_id = normalized(entry.get("gold_id"))
            claim = claims_by_id.get(gold_id, {})
            document = normalized(entry.get("gold_doc"))
            subject = normalized(claim.get("subject") or entry.get("subject"))
            predicate = normalized(claim.get("predicate") or entry.get("predicate"))
            value = normalized(claim.get("value") or entry.get("value"))
            same_slot = slot_groups[(document, norm_slot(subject, predicate))]
            warnings: list[str] = []
            if len(same_slot) > 1:
                warnings.append("possible_duplicate_same_slot=" + ";".join(item for item in same_slot if item != gold_id))
            if any(token in predicate for token in NARROW_NO_PREDICATES):
                warnings.append("likely_metadata_or_alias")
            if any(token in predicate for token in NARROW_REVIEW_PREDICATES):
                warnings.append("scope_requires_research_decision")
            rows.append({
                "gold_id": gold_id,
                "source_audit_row_id": normalized(entry.get("source_audit_row_id")),
                "gold_doc": document,
                "subject": subject,
                "predicate": predicate,
                "value": value,
                "qualifiers": json.dumps(claim.get("qualifiers") or {}, ensure_ascii=False),
                "quote": normalized(claim.get("quote")),
                "current_include_in_conflict_critical": normalized(entry.get("include_in_conflict_critical")).lower(),
                "scope_warnings": " | ".join(warnings),
                "suggested_broad_maintenance": "yes",
                "broad_maintenance": "yes",
                "suggested_conflict_critical": narrow_suggestion(predicate),
                "conflict_critical": "",
                "dedup_decision": "",  # keep | merge | exclude
                "merge_into_gold_id": "",
                "review_note": normalized(entry.get("review_note")),
            })

        output_path.parent.mkdir(parents=True, exist_ok=True)
        write_csv(output_path, rows)
        duplicates = sum(bool(row["scope_warnings"]) for row in rows)
        print(f"Gold-v2 dual-scope review sheet written: {output_path}")
        print(f"  entries: {len(rows)}")
        print(f"  entries with scope/dedup warnings: {duplicates}")
        return 0
    except ScopeReviewError as exc:
        print(f"[gold-v2-scope-review] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
