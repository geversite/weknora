#!/usr/bin/env python3
"""Turn a C4.10 folder inventory into a small, editable corpus-selection queue.

The inventory step deliberately preserves every file, including duplicate hashes and
empty placeholder Markdown files.  That is useful for audit, but inconvenient when
a human needs to select a first real-document pilot.  This helper only reads the
inventory CSV (never the source document bodies) and produces a deduplicated,
size-filtered queue plus directory/family triage views.

It does *not* infer fact-family truth, issuer authority, dates, versions, expected
winners, or conflict pairs.  Those fields remain blank for explicit human review.
"""

from __future__ import annotations

import argparse
import collections
import csv
import json
import sys
from pathlib import Path, PurePosixPath
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_MIN_BYTES = 1024
BINARY_FILE_EXTENSIONS = {".pdf", ".doc", ".docx"}
UNSUPPORTED_UPLOAD_EXTENSIONS = {".rtf"}
MANUAL_TEXT_EXTENSIONS = {".md", ".markdown", ".txt", ".html", ".htm"}
REQUIRED_INVENTORY_FIELDS = {
    "document_id", "relative_path", "absolute_path", "extension", "bytes", "sha256",
    "filename_stem", "version_hint", "date_hint", "family_candidate", "duplicate_of",
}


class SelectionError(RuntimeError):
    """The inventory cannot be safely converted into a review queue."""


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def as_int(value: Any, *, field: str, row_number: int) -> int:
    try:
        parsed = int(str(value or "0").strip())
    except ValueError as exc:
        raise SelectionError(f"inventory 第 {row_number} 行 {field} 不是整数: {value!r}") from exc
    if parsed < 0:
        raise SelectionError(f"inventory 第 {row_number} 行 {field} 不能为负数")
    return parsed


def normalize_extensions(raw: str) -> set[str] | None:
    if not raw.strip():
        return None
    extensions: set[str] = set()
    for item in raw.split(","):
        suffix = item.strip().lower()
        if not suffix:
            continue
        extensions.add(suffix if suffix.startswith(".") else "." + suffix)
    if not extensions:
        raise SelectionError("--extensions 至少要包含一个有效扩展名")
    return extensions


def read_inventory(path: Path) -> list[dict[str, str]]:
    try:
        with path.open("r", encoding="utf-8", newline="") as handle:
            reader = csv.DictReader(handle)
            fields = set(reader.fieldnames or [])
            missing = sorted(REQUIRED_INVENTORY_FIELDS - fields)
            if missing:
                raise SelectionError(f"inventory 缺少列: {', '.join(missing)}")
            rows: list[dict[str, str]] = []
            seen_ids: set[str] = set()
            for row_number, row in enumerate(reader, start=2):
                normalized = {key: str(value or "").strip() for key, value in row.items()}
                document_id = normalized["document_id"]
                if not document_id:
                    raise SelectionError(f"inventory 第 {row_number} 行 document_id 为空")
                if document_id in seen_ids:
                    raise SelectionError(f"inventory 存在重复 document_id: {document_id}")
                seen_ids.add(document_id)
                if not normalized["relative_path"] or not normalized["absolute_path"]:
                    raise SelectionError(f"inventory 第 {row_number} 行缺少 relative_path/absolute_path")
                normalized["bytes"] = str(as_int(normalized["bytes"], field="bytes", row_number=row_number))
                normalized["extension"] = normalized["extension"].lower()
                rows.append(normalized)
    except FileNotFoundError as exc:
        raise SelectionError(f"找不到 inventory CSV: {path}") from exc
    if not rows:
        raise SelectionError(f"inventory CSV 没有数据行: {path}")
    return rows


def parent_directory(relative_path: str) -> str:
    parent = str(PurePosixPath(relative_path).parent)
    return "" if parent == "." else parent


def ingest_mode_for_extension(extension: str) -> str:
    # File mode is required for binary office/PDF inputs.  Text formats default
    # to manual mode so legacy C1/C2 scenarios stay reproducible; a reviewer may
    # explicitly change the CSV cell to file when DocReader behavior is desired.
    if extension in BINARY_FILE_EXTENSIONS:
        return "file"
    if extension in UNSUPPORTED_UPLOAD_EXTENSIONS:
        # RTF is inventoried for visibility, but the current server-side file
        # import allowlist does not accept it. Make the manual conversion step
        # explicit rather than producing a later opaque HTTP 400.
        return "unsupported"
    return "manual"


def priority_for(
    record: dict[str, str],
    directory_counts: dict[str, int],
    family_counts: dict[str, int],
) -> tuple[str, str]:
    directory = parent_directory(record["relative_path"])
    family = record["family_candidate"]
    signals: list[str] = []
    score = 0
    if family and family_counts.get(family, 0) >= 2:
        score += 3
        signals.append(f"同 filename family {family_counts[family]} 份")
    if directory_counts.get(directory, 0) >= 2:
        score += 2
        signals.append(f"同目录可审候选 {directory_counts[directory]} 份")
    if record.get("version_hint"):
        score += 1
        signals.append("文件名 version hint")
    if record.get("date_hint"):
        score += 1
        signals.append("文件名 date hint")
    if record.get("extension") in BINARY_FILE_EXTENSIONS:
        score += 1
        signals.append("可走文件/DocReader入口")
    if score >= 5:
        priority = "high"
    elif score >= 2:
        priority = "medium"
    else:
        priority = "low"
    return priority, "；".join(signals) or "仅单文件/无 filename revision hint；需人工确认"


def write_csv(path: Path, fields: list[str], rows: list[dict[str, Any]]) -> None:
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields, extrasaction="ignore")
        writer.writeheader()
        writer.writerows(rows)


def build_selection_rows(records: list[dict[str, str]], min_bytes: int, extensions: set[str] | None) -> tuple[list[dict[str, str]], dict[str, Any]]:
    all_by_hash: dict[str, list[dict[str, str]]] = collections.defaultdict(list)
    for record in records:
        all_by_hash[record["sha256"]].append(record)

    canonical = [record for record in records if not record["duplicate_of"]]
    if extensions is not None:
        canonical = [record for record in canonical if record["extension"] in extensions]
    eligible = [record for record in canonical if int(record["bytes"]) >= min_bytes]
    directory_counts = collections.Counter(parent_directory(record["relative_path"]) for record in eligible)
    family_counts = collections.Counter(record["family_candidate"] for record in eligible if record["family_candidate"])

    rows: list[dict[str, str]] = []
    for record in eligible:
        priority, reason = priority_for(record, directory_counts, family_counts)
        rows.append({
            "source_document_id": record["document_id"],
            "relative_path": record["relative_path"],
            "absolute_path": record["absolute_path"],
            "extension": record["extension"],
            "bytes": record["bytes"],
            "sha256": record["sha256"],
            "filename_stem": record["filename_stem"],
            "filename_family_candidate": record["family_candidate"],
            "version_hint": record["version_hint"],
            "date_hint": record["date_hint"],
            "parent_directory": parent_directory(record["relative_path"]),
            "duplicate_record_count": str(max(len(all_by_hash[record["sha256"]]) - 1, 0)),
            "triage_priority": priority,
            "triage_reason": reason,
            "ingest_mode": ingest_mode_for_extension(record["extension"]),
            # Everything below this line is intentionally blank or a safe local
            # default.  A reviewer, not this filename-only tool, supplies truth.
            "include": "",
            "case_id": "",
            "document_id": record["document_id"],
            "fact_family_id": "",
            "split": "",
            "expected_outcome": "",
            "winner": "",
            "adoption_cycles": "",
            "expected_conflict_with": "",
            "metadata_title": "",
            "metadata_evidence_verified": "",
            "metadata_evidence_location": "",
            "reviewer_note": "",
        })

    priority_order = {"high": 0, "medium": 1, "low": 2}
    rows.sort(key=lambda item: (
        priority_order.get(item["triage_priority"], 9),
        item["parent_directory"].lower(),
        item["relative_path"].lower(),
    ))
    stats = {
        "canonical_count": len(canonical),
        "eligible_count": len(eligible),
        "tiny_or_empty_canonical_count": len(canonical) - len(eligible),
        "filtered_by_extension_canonical_count": sum(
            1 for record in records
            if not record["duplicate_of"] and extensions is not None and record["extension"] not in extensions
        ),
    }
    return rows, stats


def build_directory_rows(selection_rows: list[dict[str, str]], records: list[dict[str, str]]) -> list[dict[str, str]]:
    all_counts = collections.Counter(parent_directory(record["relative_path"]) for record in records)
    duplicate_counts = collections.Counter(
        parent_directory(record["relative_path"]) for record in records if record["duplicate_of"]
    )
    grouped: dict[str, list[dict[str, str]]] = collections.defaultdict(list)
    for row in selection_rows:
        grouped[row["parent_directory"]].append(row)

    rows: list[dict[str, str]] = []
    for directory, members in grouped.items():
        binary_count = sum(1 for row in members if row["extension"] in BINARY_FILE_EXTENSIONS)
        text_count = sum(1 for row in members if row["extension"] in MANUAL_TEXT_EXTENSIONS)
        date_count = sum(1 for row in members if row["date_hint"])
        version_count = sum(1 for row in members if row["version_hint"])
        distinct_families = len({row["filename_family_candidate"] for row in members if row["filename_family_candidate"]})
        score = (3 if len(members) >= 3 else 2 if len(members) >= 2 else 0) + date_count + version_count
        priority = "high" if score >= 5 else "medium" if score >= 2 else "low"
        rows.append({
            "parent_directory": directory,
            "triage_priority": priority,
            "eligible_unique_document_count": str(len(members)),
            "all_inventory_record_count": str(all_counts[directory]),
            "duplicate_record_count": str(duplicate_counts[directory]),
            "binary_file_count": str(binary_count),
            "manual_text_file_count": str(text_count),
            "filename_date_hint_count": str(date_count),
            "filename_version_hint_count": str(version_count),
            "distinct_filename_family_count": str(distinct_families),
            "source_document_ids": ";".join(row["source_document_id"] for row in members),
            "relative_paths": ";".join(row["relative_path"] for row in members),
        })
    priority_order = {"high": 0, "medium": 1, "low": 2}
    rows.sort(key=lambda item: (
        priority_order.get(item["triage_priority"], 9),
        -int(item["eligible_unique_document_count"]),
        item["parent_directory"].lower(),
    ))
    return rows


def build_same_filename_family_rows(selection_rows: list[dict[str, str]]) -> list[dict[str, str]]:
    grouped: dict[str, list[dict[str, str]]] = collections.defaultdict(list)
    for row in selection_rows:
        family = row["filename_family_candidate"]
        if family:
            grouped[family].append(row)

    rows: list[dict[str, str]] = []
    for family, members in grouped.items():
        if len(members) < 2:
            continue
        rows.append({
            "filename_family_candidate": family,
            "eligible_unique_document_count": str(len(members)),
            "parent_directories": ";".join(sorted({row["parent_directory"] for row in members})),
            "source_document_ids": ";".join(row["source_document_id"] for row in members),
            "relative_paths": ";".join(row["relative_path"] for row in members),
            "version_hints": ";".join(sorted({row["version_hint"] for row in members if row["version_hint"]})),
            "date_hints": ";".join(sorted({row["date_hint"] for row in members if row["date_hint"]})),
            "warning": "同名归一仅是 filename hint；跨目录或同名模板不能据此认定同一事实家族。",
        })
    rows.sort(key=lambda item: (-int(item["eligible_unique_document_count"]), item["filename_family_candidate"].lower()))
    return rows


def write_readme(path: Path, inventory: Path, min_bytes: int) -> None:
    text = f"""# C4.10 corpus selection queue

Input inventory: `{inventory}`

This directory was created from filenames, paths, sizes and SHA-256 values only. It did **not** read or export source document bodies, upload files, call a model/API/Asynq, or access PostgreSQL.

## Files

- `corpus_selection.csv` — edit only the rows that should enter a narrow pilot case.
- `directory_triage.csv` — convenient view of non-empty, deduplicated candidates by parent directory.
- `same_filename_families.csv` — only filename-normalized groups with two or more non-empty unique documents.
- `selection_summary.json` — counts and filtering parameters.

`corpus_selection.csv` excludes duplicate hashes and canonical files smaller than `{min_bytes}` bytes. They remain preserved in the original inventory for audit. A small Markdown file can be deliberately re-added only after checking that it is genuine content rather than a placeholder.

## Minimal editing workflow

1. Open `directory_triage.csv`, find a narrow business topic with 2–3 genuinely versioned documents, then locate those rows in `corpus_selection.csv`.
2. For every selected source row set `include=yes`; fill the same `case_id` and `fact_family_id`; choose one `split` (`development` or `holdout`).
3. Set `expected_outcome` to `adopt_reopen` only after verifying same issuer + comparable explicit date/version evidence; otherwise use a deliberately labeled `no_proposal` negative.
4. For an `adopt_reopen` case select at least three sources, mark exactly one source `winner=yes`, and set `adoption_cycles` (normally `1`) on every row in that case.
5. In `expected_conflict_with`, write the selected document IDs separated by `;`, or `*` only if this document conflicts with every other source in the same narrow fact case.
6. Copy the **verified, explicit** source metadata into `metadata_title`, e.g. `发布机构：某单位；生效日期：2026年3月25日；版本号：V3.0`; set `metadata_evidence_verified=yes` and write its page/header location.
7. Materialize the reviewed CSV without reading document bodies:

   ```bash
   make experiment-c410-materialize \\
     SELECTION="<this-dir>/corpus_selection.csv" \\
     CORPUS="$HOME/weknora-private-corpus/my_winner_corpus.json"
   ```

8. Validate/materialize the matrix with `make experiment-c410-plan ...`, then run only the intended development or frozen holdout matrix.

## Important boundaries

- `triage_priority`, filename version/date hints, parent directories, and normalized filename families are **not labels**. They never establish issuer, recency, fact-family membership, a conflict, or a winner.
- A binary `pdf/doc/docx` row is prefilled with `ingest_mode=file`. The updated experiment runner uploads only selected files through the real multipart HTTP endpoint and waits for the normal DocReader/Asynq pipeline; it does not read a binary file as Markdown. `rtf` remains visible but is marked `unsupported`, because the current server-side file-import allowlist requires it to be converted before use.
- Keep this queue, private document paths, generated corpus manifest and all run artifacts outside Git.
"""
    path.write_text(text, encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Create a human-editable C4.10 selection queue from folder inventory CSV metadata.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--inventory", required=True, help="Path to inventory_winner_corpus.py document_inventory.csv")
    parser.add_argument("--output-dir", required=True, help="Private directory for queue CSVs")
    parser.add_argument("--min-bytes", type=int, default=DEFAULT_MIN_BYTES, help="Exclude canonical files smaller than this from the editable queue")
    parser.add_argument("--extensions", default="", help="Optional comma-separated extension filter, e.g. pdf,docx,doc")
    parser.add_argument("--overwrite", action="store_true", help="Allow a non-empty output directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if args.min_bytes < 0:
            raise SelectionError("--min-bytes 不能为负数")
        inventory = Path(args.inventory).expanduser().resolve()
        output = Path(args.output_dir).expanduser().resolve()
        if output.exists() and any(output.iterdir()) and not args.overwrite:
            raise SelectionError(f"输出目录已存在且非空: {output}（需要 --overwrite）")
        output.mkdir(parents=True, exist_ok=True)
        extensions = normalize_extensions(args.extensions)
        records = read_inventory(inventory)
        selection_rows, stats = build_selection_rows(records, args.min_bytes, extensions)
        if not selection_rows:
            raise SelectionError("去重/大小/扩展名过滤后没有可审候选；请降低 --min-bytes 或调整 --extensions")
        directory_rows = build_directory_rows(selection_rows, records)
        family_rows = build_same_filename_family_rows(selection_rows)

        selection_fields = [
            "source_document_id", "relative_path", "absolute_path", "extension", "bytes", "sha256",
            "filename_stem", "filename_family_candidate", "version_hint", "date_hint", "parent_directory",
            "duplicate_record_count", "triage_priority", "triage_reason", "ingest_mode", "include", "case_id",
            "document_id", "fact_family_id", "split", "expected_outcome", "winner", "adoption_cycles",
            "expected_conflict_with", "metadata_title", "metadata_evidence_verified", "metadata_evidence_location",
            "reviewer_note",
        ]
        directory_fields = [
            "parent_directory", "triage_priority", "eligible_unique_document_count", "all_inventory_record_count",
            "duplicate_record_count", "binary_file_count", "manual_text_file_count", "filename_date_hint_count",
            "filename_version_hint_count", "distinct_filename_family_count", "source_document_ids", "relative_paths",
        ]
        family_fields = [
            "filename_family_candidate", "eligible_unique_document_count", "parent_directories", "source_document_ids",
            "relative_paths", "version_hints", "date_hints", "warning",
        ]
        write_csv(output / "corpus_selection.csv", selection_fields, selection_rows)
        write_csv(output / "directory_triage.csv", directory_fields, directory_rows)
        write_csv(output / "same_filename_families.csv", family_fields, family_rows)

        summary = {
            "schema_version": 1,
            "inventory": str(inventory),
            "output_dir": str(output),
            "min_bytes": args.min_bytes,
            "extensions": sorted(extensions) if extensions is not None else None,
            "inventory_record_count": len(records),
            "inventory_duplicate_record_count": sum(1 for record in records if record["duplicate_of"]),
            "review_queue_unique_candidates": len(selection_rows),
            "directory_triage_count": len(directory_rows),
            "same_filename_family_count": len(family_rows),
            **stats,
            "note": "Filename/hash/size triage only; no document body/API/model/Asynq/PostgreSQL operation was performed.",
        }
        json_dump(output / "selection_summary.json", summary)
        write_readme(output / "README.md", inventory, args.min_bytes)

        print(f"C4.10 selection queue complete: {output}")
        print(f"  reviewable unique candidates: {len(selection_rows)}")
        print(f"  directory triage groups: {len(directory_rows)}")
        print(f"  same filename family groups (>=2): {len(family_rows)}")
        print(f"  excluded canonical files below {args.min_bytes} bytes: {stats['tiny_or_empty_canonical_count']}")
        print("  document body/API/model/database: not accessed")
        return 0
    except SelectionError as exc:
        print(f"[c4.10-selection] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
