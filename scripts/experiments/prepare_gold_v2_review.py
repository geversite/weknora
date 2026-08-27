#!/usr/bin/env python3
"""Create a reviewer worksheet for gold-v2 additions with quote repair hints.

Claims with span_start/span_end = 0 often reach the audit as a correct
prediction with an empty ``pred_quote``. Such claims must not be automatically
promoted into gold-v2: gold evidence needs a verbatim source quote.

This script copies ``gold_v2_additions.csv`` into an editable CSV, validates
existing quotes, and supplies paragraph-level source candidates for any missing
or invalid quote. A reviewer fills ``review_quote`` with an exact supporting
substring; materialize_gold_v2.py will prefer that field.
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_DOCS_DIR = ROOT / "testdata/claims_eval/docs"


class QuoteReviewError(RuntimeError):
    pass


def normalized(value: str | None) -> str:
    return (value or "").strip()


def whitespace_fold(value: str) -> str:
    return "".join(value.split())


def read_csv(path: Path) -> list[dict[str, str]]:
    try:
        with path.open(encoding="utf-8-sig", newline="") as handle:
            return list(csv.DictReader(handle))
    except FileNotFoundError as exc:
        raise QuoteReviewError(f"找不到 additions CSV: {path}") from exc


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


def paragraph_at(text: str, index: int) -> str:
    """Return the Markdown paragraph/line around index, retaining exact text."""
    before = text.rfind("\n\n", 0, index)
    start = 0 if before < 0 else before + 2
    after = text.find("\n\n", index)
    end = len(text) if after < 0 else after
    candidate = text[start:end].strip()
    if len(candidate) <= 800:
        return candidate
    # Long paragraphs are still source-exact; narrow around the hit but avoid
    # pretending the truncated context itself is a ready-to-copy gold quote.
    from_index = max(start, index - 300)
    to_index = min(end, index + 500)
    return text[from_index:to_index].strip()


def quote_candidates(source: str, row: dict[str, str], limit: int = 3) -> list[str]:
    needles = [normalized(row.get("value")), normalized(row.get("subject")), normalized(row.get("predicate"))]
    candidates: list[str] = []
    for needle in needles:
        if not needle:
            continue
        offset = 0
        while len(candidates) < limit:
            index = source.find(needle, offset)
            if index < 0:
                break
            candidate = paragraph_at(source, index)
            if candidate and candidate not in candidates:
                candidates.append(candidate)
            offset = index + max(1, len(needle))
        if candidates:
            break
    return candidates


def quote_state(quote: str, source: str) -> str:
    if not quote:
        return "needs_review_missing"
    if quote in source:
        return "ready_exact"
    if whitespace_fold(quote) in whitespace_fold(source):
        return "ready_whitespace_folded"
    return "needs_review_not_found"


def main() -> int:
    parser = argparse.ArgumentParser(description="Prepare a gold-v2 reviewer sheet with quote repair candidates.")
    parser.add_argument("--additions", required=True, help="reviewed_metrics/gold_v2_additions.csv")
    parser.add_argument("--output", required=True, help="Editable gold_v2_additions_review.csv output path")
    parser.add_argument("--docs-dir", default=str(DEFAULT_DOCS_DIR), help="Original Markdown documents directory")
    parser.add_argument("--overwrite", action="store_true", help="Allow overwriting an existing output CSV")
    args = parser.parse_args()

    try:
        additions_path = Path(args.additions).expanduser().resolve()
        output_path = Path(args.output).expanduser().resolve()
        docs_dir = Path(args.docs_dir).expanduser().resolve()
        if not docs_dir.is_dir():
            raise QuoteReviewError(f"docs 目录不存在: {docs_dir}")
        if output_path.exists() and not args.overwrite:
            raise QuoteReviewError(f"输出文件已存在: {output_path}（使用 --overwrite）")
        rows = read_csv(additions_path)
        if not rows:
            raise QuoteReviewError("additions CSV 没有数据行")

        ready = 0
        needs_review = 0
        output_rows: list[dict[str, str]] = []
        for row in rows:
            document = normalized(row.get("document"))
            source_path = docs_dir / document
            if not document or not source_path.is_file():
                raise QuoteReviewError(f"addition 的 document 不存在: {document!r}")
            source = source_path.read_text(encoding="utf-8")
            quote = normalized(row.get("quote"))
            state = quote_state(quote, source)
            copied = dict(row)
            copied["review_quote"] = quote if state.startswith("ready_") else ""
            copied["quote_review_status"] = state
            copied["suggested_quote_candidates"] = json.dumps(quote_candidates(source, row), ensure_ascii=False)
            copied["quote_review_note"] = ""
            output_rows.append(copied)
            if state.startswith("ready_"):
                ready += 1
            else:
                needs_review += 1

        output_path.parent.mkdir(parents=True, exist_ok=True)
        write_csv(output_path, output_rows)
        print(f"Gold-v2 quote review sheet written: {output_path}")
        print(f"  ready quotes: {ready}")
        print(f"  quotes needing review: {needs_review}")
        return 0
    except QuoteReviewError as exc:
        print(f"[gold-v2-review] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
