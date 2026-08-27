#!/usr/bin/env python3
"""Materialize a review-approved *candidate* gold-v2 corpus.

The script reads ``gold_v2_additions.csv`` produced by
compute_reviewed_claim_metrics.py, copies immutable gold-v1 JSON documents into
a new directory, and appends only additions that a reviewer explicitly marked
``add_gold_v2``. The source gold-v1 files are never modified.

The result is a reviewable candidate corpus, not an automatic truth update.
After reviewing its README/provenance, a maintainer may promote it to a tracked
``testdata/claims_eval/gold-v2`` corpus and run evaluate.py --gold-dir on it.

Example:

  python3 scripts/experiments/materialize_gold_v2.py \
    --additions experiments/runs/<run>/claim_audit/review_summary_v2/reviewed_metrics/gold_v2_additions.csv \
    --output experiments/runs/<run>/claim_audit/review_summary_v2/reviewed_metrics/gold_v2_candidate
"""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import shutil
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_GOLD_DIR = ROOT / "testdata/claims_eval/gold"
DEFAULT_DOCS_DIR = ROOT / "testdata/claims_eval/docs"
VALID_VALUE_KINDS = {"number", "enum", "date", "text"}


class GoldMaterializeError(RuntimeError):
    pass


def normalized(value: str | None) -> str:
    return (value or "").strip()


def read_csv(path: Path) -> list[dict[str, str]]:
    try:
        with path.open(encoding="utf-8-sig", newline="") as handle:
            return list(csv.DictReader(handle))
    except FileNotFoundError as exc:
        raise GoldMaterializeError(f"找不到 additions CSV: {path}") from exc


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise GoldMaterializeError(f"找不到 gold 文件: {path}") from exc
    except json.JSONDecodeError as exc:
        raise GoldMaterializeError(f"gold JSON 无法解析: {path}: {exc}") from exc


def write_json(path: Path, data: Any) -> None:
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def whitespace_fold(value: str) -> str:
    return "".join(value.split())


def source_doc_for(gold_doc: str, docs_dir: Path) -> Path:
    source = docs_dir / gold_doc
    if not source.is_file():
        raise GoldMaterializeError(f"gold 文档对应原文不存在: {source}")
    return source


def claim_id_for(row: dict[str, str], used: set[str]) -> str:
    source = normalized(row.get("source_audit_row_id")) or normalized(row.get("proposed_gold_id"))
    base = "v2-" + (source or "addition")
    base = base.replace(" ", "-").replace(":", "-")
    candidate = base
    suffix = 2
    while candidate in used:
        candidate = f"{base}-{suffix}"
        suffix += 1
    return candidate


def validate_addition(row: dict[str, str], docs_dir: Path) -> tuple[str, dict[str, Any], dict[str, Any]]:
    required = ("document", "subject", "predicate", "value", "value_kind", "quote", "source_audit_row_id")
    missing = [name for name in required if not normalized(row.get(name))]
    if missing:
        raise GoldMaterializeError(
            f"gold-v2 addition 缺少字段 {', '.join(missing)} (audit={row.get('source_audit_row_id', '')})"
        )
    document = normalized(row["document"])
    kind = normalized(row["value_kind"]).lower()
    if kind not in VALID_VALUE_KINDS:
        raise GoldMaterializeError(
            f"无效 value_kind={row['value_kind']!r} (audit={row['source_audit_row_id']})"
        )
    try:
        qualifiers = json.loads(row.get("qualifiers") or "{}")
    except json.JSONDecodeError as exc:
        raise GoldMaterializeError(
            f"qualifiers 不是 JSON (audit={row['source_audit_row_id']}): {exc}"
        ) from exc
    if not isinstance(qualifiers, dict):
        raise GoldMaterializeError(f"qualifiers 必须是对象 (audit={row['source_audit_row_id']})")

    source_text = source_doc_for(document, docs_dir).read_text(encoding="utf-8")
    quote = normalized(row["quote"])
    quote_exact = quote in source_text
    quote_folded = whitespace_fold(quote) in whitespace_fold(source_text)
    if not quote_exact and not quote_folded:
        raise GoldMaterializeError(
            f"quote 不在原文中 (audit={row['source_audit_row_id']}, doc={document}): {quote[:120]!r}"
        )

    claim = {
        "subject": normalized(row["subject"]),
        "predicate": normalized(row["predicate"]),
        "value": normalized(row["value"]),
        "value_kind": kind,
        "qualifiers": qualifiers,
        "quote": quote,
    }
    provenance = {
        "source_audit_row_id": normalized(row["source_audit_row_id"]),
        "proposed_gold_id": normalized(row.get("proposed_gold_id")),
        "include_in_conflict_critical": normalized(row.get("include_in_conflict_critical")).lower(),
        "review_note": normalized(row.get("review_note")),
        "quote_match": "exact" if quote_exact else "whitespace_folded",
    }
    return document, claim, provenance


def main() -> int:
    parser = argparse.ArgumentParser(description="Materialize a review-approved candidate gold-v2 corpus.")
    parser.add_argument("--additions", required=True, help="reviewed_metrics/gold_v2_additions.csv")
    parser.add_argument("--gold-dir", default=str(DEFAULT_GOLD_DIR), help="Immutable gold-v1 source directory")
    parser.add_argument("--docs-dir", default=str(DEFAULT_DOCS_DIR), help="Original Markdown documents directory")
    parser.add_argument("--output", required=True, help="New candidate gold-v2 output directory")
    parser.add_argument("--overwrite", action="store_true", help="Allow overwriting a non-empty output directory")
    args = parser.parse_args()

    try:
        additions_path = Path(args.additions).expanduser().resolve()
        gold_dir = Path(args.gold_dir).expanduser().resolve()
        docs_dir = Path(args.docs_dir).expanduser().resolve()
        output_dir = Path(args.output).expanduser().resolve()
        if not gold_dir.is_dir():
            raise GoldMaterializeError(f"gold-v1 目录不存在: {gold_dir}")
        if not docs_dir.is_dir():
            raise GoldMaterializeError(f"docs 目录不存在: {docs_dir}")
        if output_dir.exists() and any(output_dir.iterdir()) and not args.overwrite:
            raise GoldMaterializeError(f"输出目录已存在且非空: {output_dir}（使用 --overwrite）")
        output_dir.mkdir(parents=True, exist_ok=True)

        additions = read_csv(additions_path)
        grouped: dict[str, list[tuple[dict[str, Any], dict[str, Any]]]] = {}
        for row in additions:
            document, claim, provenance = validate_addition(row, docs_dir)
            grouped.setdefault(document, []).append((claim, provenance))

        provenance_rows: list[dict[str, Any]] = []
        total_added = 0
        source_files = sorted(gold_dir.glob("*.json"))
        if not source_files:
            raise GoldMaterializeError(f"gold-v1 目录没有 JSON 文件: {gold_dir}")
        for source_path in source_files:
            gold_doc = load_json(source_path)
            document = normalized(gold_doc.get("doc"))
            claims = list(gold_doc.get("claims") or [])
            used_ids = {normalized(claim.get("id")) for claim in claims}
            for claim, provenance in grouped.pop(document, []):
                claim_id = claim_id_for(provenance, used_ids)
                used_ids.add(claim_id)
                materialized = {"id": claim_id, **claim}
                claims.append(materialized)
                provenance_rows.append({
                    "gold_doc": document,
                    "gold_id": claim_id,
                    **provenance,
                    "subject": claim["subject"],
                    "predicate": claim["predicate"],
                    "value": claim["value"],
                })
                total_added += 1
            gold_doc["claims"] = claims
            write_json(output_dir / source_path.name, gold_doc)

        if grouped:
            missing_docs = ", ".join(sorted(grouped))
            raise GoldMaterializeError(f"additions 指向 gold-v1 中不存在的 document: {missing_docs}")

        provenance = {
            "kind": "gold-v2-candidate",
            "generated_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat(),
            "source_gold_dir": str(gold_dir),
            "source_additions_csv": str(additions_path),
            "claims_added": total_added,
            "entries": provenance_rows,
        }
        write_json(output_dir / "provenance.json", provenance)
        digest = hashlib.sha256((output_dir / "provenance.json").read_bytes()).hexdigest()
        (output_dir / "README.md").write_text(
            "# Candidate gold-v2 corpus\n\n"
            "This directory was materialized from reviewer-approved `add_gold_v2` rows. "
            "It does **not** replace immutable gold-v1. Review `provenance.json`, then copy/promote "
            "this corpus only after a second reviewer checks the additions.\n\n"
            f"- Added claims: `{total_added}`\n"
            f"- Provenance SHA-256: `{digest}`\n\n"
            "Evaluate with:\n\n"
            "```bash\n"
            "python3 testdata/claims_eval/evaluate.py --run <claims_eval_run.json> --gold-dir <this-directory>\n"
            "```\n",
            encoding="utf-8",
        )
        print(f"Candidate gold-v2 written: {output_dir}")
        print(f"  claims added: {total_added}")
        print(f"  provenance: {output_dir / 'provenance.json'}")
        return 0
    except GoldMaterializeError as exc:
        print(f"[gold-v2] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
