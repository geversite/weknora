#!/usr/bin/env python3
"""Build an unlabeled native-DocReader scenario from a private document folder.

This is an observational ingest plan, not a labeled accuracy evaluation.
It does not read document bodies into artifacts, does not invent issuer/date/
version titles, and does not upload anything. The generated scenario uses
ingest_mode=file so the live runner sends original bytes through WeKnora's
public multipart API and DocReader.
"""

from __future__ import annotations

import argparse
import csv
import hashlib
import importlib.util
import json
import os
import sys
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
INVENTORY_PATH = Path(__file__).with_name("inventory_winner_corpus.py")
NATIVE_EXTENSIONS = {
    ".pdf", ".docx", ".doc", ".md", ".markdown", ".txt",
    ".html", ".htm", ".xlsx", ".xls", ".pptx", ".ppt",
}
DEFAULT_MAX_FILES = 8
DEFAULT_MIN_BYTES = 1024
DEFAULT_MAX_BYTES = 20 * 1024 * 1024


class PlanError(RuntimeError):
    """The native corpus plan cannot be generated safely."""


def load_inventory_module() -> Any:
    spec = importlib.util.spec_from_file_location("inventory_winner_corpus", INVENTORY_PATH)
    if spec is None or spec.loader is None:
        raise PlanError(f"cannot load inventory helper: {INVENTORY_PATH}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def slug_folder(path: Path) -> str:
    raw = path.name.strip() or "corpus"
    cleaned = "".join(ch.lower() if ch.isalnum() else "-" for ch in raw)
    cleaned = "-".join(part for part in cleaned.split("-") if part)[:40]
    return cleaned or "corpus"


def document_id(index: int) -> str:
    return f"doc-{index:04d}"


def stable_upload_name(doc_id: str, suffix: str) -> str:
    name = f"{doc_id}{suffix.lower()}"
    if len(name.encode("utf-8")) > 240:
        raise PlanError(f"generated upload_file_name too long: {name}")
    return name


def eligible_records(
    records: list[dict[str, Any]],
    *,
    min_bytes: int,
    max_bytes: int,
) -> tuple[list[dict[str, Any]], dict[str, int]]:
    unique: list[dict[str, Any]] = []
    skipped: dict[str, int] = defaultdict(int)
    for record in records:
        if record.get("duplicate_of"):
            skipped["duplicate"] += 1
            continue
        size = int(record["bytes"])
        if size < min_bytes:
            skipped["too_small"] += 1
            continue
        if max_bytes and size > max_bytes:
            skipped["too_large"] += 1
            continue
        unique.append(record)
    return unique, dict(skipped)


def drop_redundant_pdf(records: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], int]:
    grouped: dict[tuple[str, str], list[dict[str, Any]]] = defaultdict(list)
    for record in records:
        parent = str(Path(str(record["relative_path"])).parent)
        stem = Path(str(record["relative_path"])).stem.lower()
        grouped[(parent, stem)].append(record)
    kept: list[dict[str, Any]] = []
    dropped = 0
    for members in grouped.values():
        exts = {str(item["extension"]) for item in members}
        if ".docx" in exts and ".pdf" in exts:
            kept.extend(item for item in members if str(item["extension"]) != ".pdf")
            dropped += sum(1 for item in members if str(item["extension"]) == ".pdf")
        else:
            kept.extend(members)
    return kept, dropped


def select_records(
    records: list[dict[str, Any]],
    *,
    max_files: int,
    min_bytes: int,
    max_bytes: int,
    prefer_families: bool,
    drop_pdf_if_docx: bool,
) -> tuple[list[dict[str, Any]], dict[str, int]]:
    unique, skipped = eligible_records(records, min_bytes=min_bytes, max_bytes=max_bytes)
    if drop_pdf_if_docx:
        unique, pdf_dropped = drop_redundant_pdf(unique)
        skipped["pdf_if_docx"] = pdf_dropped
    unique.sort(key=lambda item: (-int(item["bytes"]), str(item["relative_path"])))
    selected: list[dict[str, Any]] = []
    seen: set[str] = set()

    def take(record: dict[str, Any]) -> None:
        digest = str(record["sha256"])
        if digest in seen or len(selected) >= max_files:
            return
        seen.add(digest)
        selected.append(record)

    if prefer_families:
        groups: dict[str, list[dict[str, Any]]] = defaultdict(list)
        for record in unique:
            parent = str(Path(str(record["relative_path"])).parent)
            family = str(record.get("family_candidate") or record["filename_stem"])
            groups[f"{parent}::{family}"].append(record)
        ranked = sorted(
            groups.items(),
            key=lambda item: (-len(item[1]), -sum(int(row["bytes"]) for row in item[1]), item[0]),
        )
        for _, members in ranked:
            if len(members) < 2:
                continue
            for record in members[:4]:
                take(record)
            if len(selected) >= max_files:
                break
    by_dir: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for record in unique:
        by_dir[str(Path(str(record["relative_path"])).parent)].append(record)
    for _, members in sorted(by_dir.items(), key=lambda item: item[0]):
        if members:
            take(members[0])
        if len(selected) >= max_files:
            break
    for record in unique:
        take(record)
        if len(selected) >= max_files:
            break
    if not selected:
        raise PlanError(
            "no eligible unique files after filters "
            f"(min_bytes={min_bytes}, max_bytes={max_bytes}, skipped={skipped})"
        )
    selected.sort(key=lambda item: str(item["relative_path"]))
    skipped["eligible_unique"] = len(unique)
    return selected, skipped


def pack_batches(records: list[dict[str, Any]], batch_size: int) -> list[list[dict[str, Any]]]:
    if batch_size <= 0:
        raise PlanError("--batch-size must be positive")
    by_parent: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for record in records:
        by_parent[str(Path(str(record["relative_path"])).parent)].append(record)
    batches: list[list[dict[str, Any]]] = []
    current: list[dict[str, Any]] = []
    for _, members in sorted(by_parent.items()):
        for record in members:
            if len(current) >= batch_size:
                batches.append(current)
                current = []
            current.append(record)
    if current:
        batches.append(current)
    return batches


def corpus_census(source: Path, records: list[dict[str, Any]]) -> dict[str, Any]:
    walk_dirs = 0
    for dirpath, _dirnames, _filenames in os.walk(source):
        if Path(dirpath).resolve() == source:
            walk_dirs += 1
            continue
        walk_dirs += 1
    parents = {str(Path(str(item["relative_path"])).parent) for item in records}
    extensions = Counter(str(item["extension"]) for item in records)
    unique = sum(1 for item in records if not item.get("duplicate_of"))
    return {
        "schema_version": 1,
        "source_dir": str(source),
        "directory_count": walk_dirs,
        "directories_with_matching_files": len(parents),
        "file_count": len(records),
        "unique_file_count": unique,
        "duplicate_file_count": len(records) - unique,
        "bytes_total": sum(int(item["bytes"]) for item in records),
        "extensions": dict(sorted(extensions.items())),
        "note": (
            "Full-folder census from filenames/hashes only. Not ingest, not "
            "accuracy, and not a labeled corpus."
        ),
    }


def build_scenario(
    *,
    name: str,
    source: Path,
    selected: list[dict[str, Any]],
    start_index: int = 1,
) -> dict[str, Any]:
    documents = []
    for offset, record in enumerate(selected):
        index = start_index + offset
        doc_id = document_id(index)
        suffix = str(record["extension"])
        documents.append({
            "id": doc_id,
            "path": record["absolute_path"],
            "title": str(record["filename_stem"])[:80],
            "ingest_mode": "file",
            "upload_file_name": stable_upload_name(doc_id, suffix),
            "source_sha256": record["sha256"],
            "source_document_id": record["document_id"],
            "source_relative_path": record["relative_path"],
            "original_filename": Path(str(record["relative_path"])).name,
            "family_candidate": record.get("family_candidate", ""),
            "version_hint": record.get("version_hint", ""),
            "date_hint": record.get("date_hint", ""),
            "bytes": int(record["bytes"]),
        })
    return {
        "schema_version": 1,
        "name": name,
        "description": (
            "Unlabeled native-DocReader observational ingest. Original files are "
            "uploaded through the public multipart API. Filename hints are not "
            "issuer/date/version evidence. No gold pairs, no winner assertions, "
            "not a real-document accuracy estimate."
        ),
        "min_claims_per_document": 0,
        "source_dir": str(source),
        "documents": documents,
        "expected_conflict_document_pairs": [],
        "forbidden_conflict_document_pairs": [],
    }


def write_selection_csv(path: Path, documents: list[dict[str, Any]]) -> None:
    fields = [
        "id", "original_filename", "source_relative_path", "bytes",
        "extension", "family_candidate", "version_hint", "date_hint", "source_sha256",
    ]
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for document in documents:
            writer.writerow({
                "id": document["id"],
                "original_filename": document["original_filename"],
                "source_relative_path": document["source_relative_path"],
                "bytes": document["bytes"],
                "extension": Path(document["path"]).suffix.lower(),
                "family_candidate": document.get("family_candidate", ""),
                "version_hint": document.get("version_hint", ""),
                "date_hint": document.get("date_hint", ""),
                "source_sha256": document["source_sha256"],
            })


def write_readme(path: Path, source: Path, scenario_path: Path, max_files: int) -> None:
    path.write_text(
        f"""# Native DocReader observational plan

Source folder: `{source}`
Scenario: `{scenario_path}`
Cap: `{max_files}` unique files

This plan does **not** copy document bodies. Live ingest must use
`ingest_mode=file` through WeKnora DocReader. Results are observational
parse/claim/conflict counts, not labeled accuracy.

Do not pool these numbers with synthetic policy tables or public-transfer tables.
""",
        encoding="utf-8",
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Create an unlabeled native-DocReader scenario from a private folder.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--source-dir", required=True, help="Private document folder; scanned recursively")
    parser.add_argument("--output-dir", required=True, help="Private output directory outside Git")
    parser.add_argument("--name", default="", help="Scenario name; default derived from folder name")
    parser.add_argument("--extensions", default=",".join(sorted(NATIVE_EXTENSIONS)))
    parser.add_argument("--max-files", type=int, default=DEFAULT_MAX_FILES, help="Stratified ingest sample cap, not the census size")
    parser.add_argument("--batch-size", type=int, default=8, help="Files per temporary KB; keep small to bound pairwise detect cost")
    parser.add_argument("--min-bytes", type=int, default=DEFAULT_MIN_BYTES)
    parser.add_argument("--max-bytes", type=int, default=DEFAULT_MAX_BYTES, help="0 disables the size cap")
    parser.add_argument("--no-prefer-families", action="store_true", help="Do not prioritize filename version groups")
    parser.add_argument("--keep-pdf-if-docx", action="store_true", help="Do not drop a PDF when a same-stem DOCX exists")
    parser.add_argument("--overwrite", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if args.max_files <= 0:
            raise PlanError("--max-files must be positive")
        if args.batch_size <= 0:
            raise PlanError("--batch-size must be positive")
        if args.min_bytes < 0 or args.max_bytes < 0:
            raise PlanError("--min-bytes/--max-bytes cannot be negative")
        source = Path(args.source_dir).expanduser().resolve()
        if not source.is_dir():
            raise PlanError(f"source folder does not exist: {source}")
        output = Path(args.output_dir).expanduser().resolve()
        if output.exists() and any(output.iterdir()) and not args.overwrite:
            raise PlanError(f"output directory exists and is not empty: {output} (need --overwrite)")
        output.mkdir(parents=True, exist_ok=True)
        inventory = load_inventory_module()
        extensions = inventory.normalize_extensions(args.extensions)
        unsupported = sorted(ext for ext in extensions if ext not in NATIVE_EXTENSIONS)
        if unsupported:
            raise PlanError(
                "unsupported native-upload extension(s): " + ", ".join(unsupported)
            )
        try:
            records = inventory.inventory(source, output, extensions, 0)
        except Exception as exc:
            raise PlanError(str(exc)) from exc
        inventory.mark_duplicates(records)
        census = corpus_census(source, records)
        json_dump(output / "corpus_census.json", census)
        selected, skipped = select_records(
            records,
            max_files=args.max_files,
            min_bytes=args.min_bytes,
            max_bytes=args.max_bytes,
            prefer_families=not args.no_prefer_families,
            drop_pdf_if_docx=not args.keep_pdf_if_docx,
        )
        name = args.name.strip() or f"native-{slug_folder(source)}"
        packed = pack_batches(selected, args.batch_size)
        batch_dir = output / "batches"
        batch_dir.mkdir(exist_ok=True)
        batch_entries = []
        start_index = 1
        all_documents: list[dict[str, Any]] = []
        for batch_index, batch_records in enumerate(packed, start=1):
            batch_name = f"{name}-b{batch_index:03d}"
            scenario = build_scenario(
                name=batch_name, source=source, selected=batch_records, start_index=start_index,
            )
            scenario_path = batch_dir / f"batch_{batch_index:03d}.json"
            json_dump(scenario_path, scenario)
            batch_entries.append({
                "batch_id": f"batch_{batch_index:03d}",
                "name": batch_name,
                "scenario_path": str(scenario_path),
                "document_count": len(batch_records),
            })
            all_documents.extend(scenario["documents"])
            start_index += len(batch_records)
        combined = build_scenario(name=name, source=source, selected=selected)
        scenario_path = output / "native_eval_scenario.json"
        json_dump(scenario_path, combined)
        write_selection_csv(output / "selected_documents.csv", all_documents)
        manifest = {
            "schema_version": 1,
            "name": name,
            "source_dir": str(source),
            "batch_size": args.batch_size,
            "selected_file_count": len(selected),
            "batch_count": len(batch_entries),
            "batches": batch_entries,
            "note": "Run batches separately. Do not upload the combined scenario of all sampled files into one KB.",
        }
        json_dump(output / "batch_manifest.json", manifest)
        summary = {
            "schema_version": 1,
            "source_dir": str(source),
            "output_dir": str(output),
            "scenario_path": str(scenario_path),
            "batch_manifest_path": str(output / "batch_manifest.json"),
            "census_path": str(output / "corpus_census.json"),
            "name": name,
            "census": census,
            "selection_skipped": skipped,
            "scanned_file_count": len(records),
            "unique_file_count": census["unique_file_count"],
            "selected_file_count": len(selected),
            "batch_count": len(batch_entries),
            "batch_size": args.batch_size,
            "extensions": sorted(extensions),
            "max_files": args.max_files,
            "min_bytes": args.min_bytes,
            "max_bytes": args.max_bytes,
            "prefer_families": not args.no_prefer_families,
            "drop_pdf_if_docx": not args.keep_pdf_if_docx,
            "note": (
                "Census covers the whole folder. Ingest sample is stratified and "
                "batched. Not real-document accuracy. Do not load all sampled "
                "files into one knowledge base."
            ),
        }
        json_dump(output / "plan_summary.json", summary)
        write_readme(output / "README.md", source, scenario_path, args.max_files)
        print(f"Native DocReader plan complete: {output}")
        print(
            "  census dirs/files/unique: "
            f"{census['directory_count']} / {census['file_count']} / {census['unique_file_count']}"
        )
        print(f"  selected / batches: {len(selected)} / {len(batch_entries)} (batch_size={args.batch_size})")
        print(f"  census: {output / 'corpus_census.json'}")
        print(f"  batch manifest: {output / 'batch_manifest.json'}")
        print("  document bodies/API/model/database: not accessed")
        return 0
    except PlanError as exc:
        print(f"[native-plan] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
