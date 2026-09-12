#!/usr/bin/env python3
"""Inventory a folder of candidate versioned documents without ingesting them.

This is the first, privacy-preserving C4.10 step for a large real corpus:
recursively scan filenames and file metadata, derive *candidate* version/date/
family hints, hash files for duplicate detection, and emit reviewable CSVs.

It deliberately does NOT read document bodies into output artifacts, call a
model, upload documents, call HTTP/Asynq, or touch PostgreSQL. Filename hints
are only triage aids; a human must verify issuer/date/version evidence and
fact-family membership before materializing a C4.10 labeled corpus.
"""

from __future__ import annotations

import argparse
import collections
import csv
import datetime as dt
import hashlib
import json
import os
import re
import sys
from pathlib import Path
from typing import Any


DEFAULT_EXTENSIONS = {
    ".md", ".markdown", ".txt", ".pdf", ".docx", ".doc", ".rtf", ".html", ".htm",
}
VERSION_PATTERNS = (
    # Filename separators such as `_`, `-` and whitespace are accepted before
    # V1; `\b` alone would miss `travel_policy_v2.pdf` because `_` is a word
    # character in regular-expression semantics.
    re.compile(r"(?i)(?:^|[^A-Za-z0-9])(?:v|ver(?:sion)?)\s*(\d+(?:[._-]\d+){0,3})"),
    re.compile(r"(?:版本号?|修订版本)\s*(\d+(?:[._-]\d+){0,3})"),
    re.compile(r"第\s*(\d+(?:[._-]\d+){0,3})\s*版"),
)
DATE_PATTERN = re.compile(
    r"(?<!\d)(?P<year>19\d{2}|20\d{2})[._/\-年\s]+(?P<month>0?[1-9]|1[0-2])"
    r"(?:[._/\-月\s]+(?P<day>0?[1-9]|[12]\d|3[01]))?(?:日)?(?!\d)"
)
# Existing enterprise files often encode a date as `20260325` rather than
# `2026-03-25`. This is still only a filename triage hint, but recognizing the
# unambiguous YYYYMMDD form prevents version candidates from being needlessly
# fragmented. Two-digit forms such as `26.05` remain intentionally ignored.
COMPACT_DATE_PATTERN = re.compile(
    r"(?<!\d)(?P<year>19\d{2}|20\d{2})(?P<month>0[1-9]|1[0-2])(?P<day>0[1-9]|[12]\d|3[01])(?!\d)"
)


class InventoryError(RuntimeError):
    """The source folder cannot be inventoried safely."""


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def utc_iso(timestamp: float) -> str:
    return dt.datetime.fromtimestamp(timestamp, tz=dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while block := handle.read(1024 * 1024):
            digest.update(block)
    return digest.hexdigest()


def normalize_extensions(raw: str) -> set[str]:
    if not raw.strip():
        return set(DEFAULT_EXTENSIONS)
    values = set()
    for value in raw.split(","):
        suffix = value.strip().lower()
        if not suffix:
            continue
        if not suffix.startswith("."):
            suffix = "." + suffix
        values.add(suffix)
    if not values:
        raise InventoryError("--extensions 至少需要一个有效扩展名")
    return values


def version_hint(stem: str) -> str:
    for pattern in VERSION_PATTERNS:
        match = pattern.search(stem)
        if match:
            return match.group(1).replace("_", ".").replace("-", ".")
    return ""


def date_hint(stem: str) -> str:
    for pattern in (DATE_PATTERN, COMPACT_DATE_PATTERN):
        match = pattern.search(stem)
        if not match:
            continue
        year = int(match.group("year"))
        month = int(match.group("month"))
        day_text = match.group("day")
        if day_text:
            day = int(day_text)
            try:
                dt.date(year, month, day)
            except ValueError:
                return ""
            return f"{year:04d}-{month:02d}-{day:02d}"
        return f"{year:04d}-{month:02d}"
    return ""


def family_candidate(stem: str, relative_path: str) -> str:
    value = stem.lower()
    for pattern in VERSION_PATTERNS:
        value = pattern.sub(" ", value)
    for pattern in (DATE_PATTERN, COMPACT_DATE_PATTERN):
        value = pattern.sub(" ", value)
    # Keep Unicode letters/digits, so Chinese filename groups remain useful;
    # fold all punctuation and whitespace into a stable underscore separator.
    parts: list[str] = []
    current: list[str] = []
    for char in value:
        if char.isalnum():
            current.append(char)
        elif current:
            parts.append("".join(current))
            current = []
    if current:
        parts.append("".join(current))
    slug = "_".join(parts).strip("_")[:96]
    if slug:
        return slug
    return "family_" + hashlib.sha256(relative_path.encode("utf-8")).hexdigest()[:12]


def document_id(stem: str, relative_path: str) -> str:
    ascii_stem = "".join(char.lower() if char.isascii() and char.isalnum() else "-" for char in stem)
    ascii_stem = re.sub(r"-+", "-", ascii_stem).strip("-")[:36]
    suffix = hashlib.sha256(relative_path.encode("utf-8")).hexdigest()[:8]
    return f"{ascii_stem or 'doc'}-{suffix}"


def metadata_status(version: str, date: str) -> str:
    missing = []
    if not version:
        missing.append("version")
    if not date:
        missing.append("date")
    # Issuer must be verified from a title/header or source provenance; file
    # names alone are not reliable enough to infer it.
    missing.append("issuer")
    return "needs_" + "_and_".join(missing)


def is_under(path: Path, parent: Path) -> bool:
    try:
        path.relative_to(parent)
        return True
    except ValueError:
        return False


def inventory(source: Path, output: Path, extensions: set[str], max_files: int) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    for path in sorted(source.rglob("*"), key=lambda item: item.relative_to(source).as_posix().lower()):
        if not path.is_file() or is_under(path.resolve(), output):
            continue
        suffix = path.suffix.lower()
        if suffix not in extensions:
            continue
        relative = path.relative_to(source).as_posix()
        stat = path.stat()
        stem = path.stem
        records.append({
            "document_id": document_id(stem, relative),
            "relative_path": relative,
            "absolute_path": str(path.resolve()),
            "extension": suffix,
            "bytes": stat.st_size,
            "modified_at_utc": utc_iso(stat.st_mtime),
            "sha256": sha256_file(path),
            "filename_stem": stem,
            "version_hint": version_hint(stem),
            "date_hint": date_hint(stem),
            "family_candidate": family_candidate(stem, relative),
            "metadata_status": metadata_status(version_hint(stem), date_hint(stem)),
            "duplicate_of": "",
            "selection_status": "unreviewed",
            "review_note": "",
        })
        if max_files and len(records) >= max_files:
            break
    if not records:
        raise InventoryError(
            f"在 {source} 下未找到匹配扩展名的文件: {', '.join(sorted(extensions))}"
        )
    return records


def mark_duplicates(records: list[dict[str, Any]]) -> None:
    canonical: dict[str, str] = {}
    for record in records:
        digest = record["sha256"]
        if digest in canonical:
            record["duplicate_of"] = canonical[digest]
        else:
            canonical[digest] = record["document_id"]


def write_inventory_csv(path: Path, records: list[dict[str, Any]]) -> None:
    fields = [
        "document_id", "relative_path", "absolute_path", "extension", "bytes", "modified_at_utc", "sha256",
        "filename_stem", "version_hint", "date_hint", "family_candidate", "metadata_status", "duplicate_of",
        "selection_status", "review_note",
    ]
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        writer.writerows(records)


def family_rows(records: list[dict[str, Any]]) -> list[dict[str, str]]:
    groups: dict[str, list[dict[str, Any]]] = collections.defaultdict(list)
    for record in records:
        groups[str(record["family_candidate"])].append(record)
    rows: list[dict[str, str]] = []
    for family, members in sorted(groups.items()):
        unique = [item for item in members if not item["duplicate_of"]]
        versions = sorted({str(item["version_hint"]) for item in unique if item["version_hint"]})
        dates = sorted({str(item["date_hint"]) for item in unique if item["date_hint"]})
        rows.append({
            "case_id_draft": "case_" + hashlib.sha256(family.encode("utf-8")).hexdigest()[:10],
            "family_candidate": family,
            "document_count": str(len(members)),
            "unique_document_count": str(len(unique)),
            "document_ids": ";".join(str(item["document_id"]) for item in unique),
            "relative_paths": ";".join(str(item["relative_path"]) for item in unique),
            "version_hints": ";".join(versions),
            "date_hints": ";".join(dates),
            "selection_status": "unreviewed",
            "fact_family_id": "",
            "split": "",
            "expected_outcome": "",
            "expected_winner_document": "",
            "adoption_cycles": "",
            "metadata_evidence_verified": "",
            "reviewer_note": "",
        })
    return rows


def write_family_csv(path: Path, rows: list[dict[str, str]]) -> None:
    fields = [
        "case_id_draft", "family_candidate", "document_count", "unique_document_count", "document_ids",
        "relative_paths", "version_hints", "date_hints", "selection_status", "fact_family_id", "split",
        "expected_outcome", "expected_winner_document", "adoption_cycles", "metadata_evidence_verified",
        "reviewer_note",
    ]
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        writer.writerows(rows)


def write_readme(path: Path, source: Path, extensions: set[str]) -> None:
    text = f"""# C4.10 source-folder inventory

Source folder: `{source}`
Scanned extensions: `{', '.join(sorted(extensions))}`

This inventory contains filenames, paths, sizes, timestamps and SHA-256 digests only. It does **not** export document bodies.

## Recommended workflow

1. Deduplicate `document_inventory.csv` using `duplicate_of`.
2. Review `family_candidates.csv`; filename-derived `family_candidate`, `version_hints` and `date_hints` are only suggestions.
3. Select 30–50 fact families for a pilot. A family should normally represent one target DisputedFact, not an entire long document.
4. Verify explicit issuer/date/version evidence in each selected source; record it in your corpus manifest title/header.
5. Assign each fact family to exactly one `development` or `holdout` split. Never reuse a document path across splits.
6. Create a labeled C4.10 corpus JSON by copying `testdata/winner_lifecycle_corpus/corpus.sample.json` and replacing only selected documents/cases.
7. Run `make experiment-c410-plan CORPUS=<your manifest> OUTPUT=<private plan dir>` before any live C4.9 run.

## Important boundaries

- A filename `V2` or `2025-06` is not sufficient evidence by itself. C3/C4.6 only trusts explicit issuer/date/version labels in the document title/header.
- Do not upload this whole inventory blindly: C4.9 cases need deliberate fact-family labels and expected conflict pairs.
- `rtf` is visible for inventory completeness, but the current server-side file-import allowlist does not accept it; convert a selected RTF source to a supported format before materializing a live case.
- Keep licensed/private documents and generated inventory output outside Git.
"""
    path.write_text(text, encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Scan a private document folder into C4.10 corpus triage CSVs without ingestion.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--source-dir", required=True, help="Folder containing candidate documents; scanned recursively")
    parser.add_argument("--output-dir", default="", help="Defaults to <source-dir>/.weknora-corpus-inventory")
    parser.add_argument("--extensions", default=",".join(sorted(DEFAULT_EXTENSIONS)), help="Comma-separated extensions, e.g. pdf,docx,md")
    parser.add_argument("--max-files", type=int, default=0, help="Optional safety cap; 0 means no cap")
    parser.add_argument("--overwrite", action="store_true", help="Allow a non-empty output directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        source = Path(args.source_dir).expanduser().resolve()
        if not source.is_dir():
            raise InventoryError(f"source folder 不存在或不是目录: {source}")
        if args.max_files < 0:
            raise InventoryError("--max-files 不能为负数")
        output = Path(args.output_dir).expanduser().resolve() if args.output_dir else source / ".weknora-corpus-inventory"
        if output.exists() and any(output.iterdir()) and not args.overwrite:
            raise InventoryError(f"输出目录已存在且非空: {output}（需要 --overwrite）")
        output.mkdir(parents=True, exist_ok=True)
        extensions = normalize_extensions(args.extensions)
        records = inventory(source, output, extensions, args.max_files)
        mark_duplicates(records)
        families = family_rows(records)
        write_inventory_csv(output / "document_inventory.csv", records)
        write_family_csv(output / "family_candidates.csv", families)
        summary = {
            "schema_version": 1,
            "source_dir": str(source),
            "output_dir": str(output),
            "extensions": sorted(extensions),
            "file_count": len(records),
            "unique_file_count": sum(1 for item in records if not item["duplicate_of"]),
            "duplicate_file_count": sum(1 for item in records if item["duplicate_of"]),
            "family_candidate_count": len(families),
            "extensions_count": dict(sorted(collections.Counter(str(item["extension"]) for item in records).items())),
            "version_hint_count": sum(1 for item in records if item["version_hint"]),
            "date_hint_count": sum(1 for item in records if item["date_hint"]),
            "note": "Filename hints only; no document body/API/model/database operation was performed.",
        }
        json_dump(output / "inventory_summary.json", summary)
        write_readme(output / "README.md", source, extensions)
        print(f"C4.10 corpus inventory complete: {output}")
        print(f"  files: {summary['file_count']} (unique={summary['unique_file_count']}, duplicates={summary['duplicate_file_count']})")
        print(f"  family candidates: {summary['family_candidate_count']}")
        print(f"  version/date filename hints: {summary['version_hint_count']} / {summary['date_hint_count']}")
        print("  document body/API/model/database: not accessed")
        return 0
    except InventoryError as exc:
        print(f"[c4.10-inventory] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
