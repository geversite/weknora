#!/usr/bin/env python3
"""Materialize an explicitly reviewed C4.10 selection CSV into a corpus manifest.

The input is normally `corpus_selection.csv` from
prepare_winner_corpus_selection.py.  This program only validates rows the
reviewer has marked `include=yes`; it never reads document bodies, uploads
files, invokes a model/API/Asynq, or writes PostgreSQL.

A materialized file can contain binary PDF/DOC/DOCX sources.  Such rows carry
`ingest_mode: file`, which the live experiment runner sends through the normal
multipart file API and DocReader pipeline rather than decoding binary bytes as
manual Markdown.
"""

from __future__ import annotations

import argparse
import collections
import csv
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any


VALID_OUTCOMES = {"adopt_reopen", "no_proposal"}
VALID_SPLITS = {"development", "holdout"}
VALID_VARIANTS = {"v1", "c1", "c2-rules", "c2-batch"}
VALID_INGEST_MODES = {"manual", "file"}
BINARY_FILE_EXTENSIONS = {".pdf", ".doc", ".docx"}
SUPPORTED_FILE_UPLOAD_EXTENSIONS = {
    ".pdf", ".txt", ".docx", ".doc", ".epub", ".html", ".htm", ".mhtml", ".md", ".markdown",
    ".png", ".jpg", ".jpeg", ".gif", ".csv", ".xlsx", ".xls", ".pptx", ".ppt", ".json",
    ".mp3", ".wav", ".m4a", ".flac", ".ogg",
}
TRUE_VALUES = {"1", "true", "yes", "y", "selected", "include"}
FALSE_VALUES = {"", "0", "false", "no", "n", "exclude", "excluded"}
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
ISSUER_LABEL_RE = re.compile(
    r"(?:^|[；;|])\s*(?:发布机构|发布单位|编制单位|发文单位|发布者|issuer|publisher|issuing\s+organization)\s*[:：]",
    re.IGNORECASE,
)
RECENCY_LABEL_RE = re.compile(
    r"(?:^|[；;|])\s*(?:生效日期|生效时间|发布日期|发布日|更新日期|修订日期|版本日期|版本|版本号|修订版本|"
    r"effective\s+date|publication\s+date|release\s+date|updated\s+date|edition|version)\s*[:：]",
    re.IGNORECASE,
)


class MaterializationError(RuntimeError):
    """The reviewed selection cannot safely become a live experiment manifest."""


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def normalize_yes_no(value: Any, *, field: str, row_number: int) -> bool:
    normalized = str(value or "").strip().lower()
    if normalized in TRUE_VALUES:
        return True
    if normalized in FALSE_VALUES:
        return False
    raise MaterializationError(
        f"selection 第 {row_number} 行 {field} 只能是 yes/no（或 true/false、1/0），实际为 {value!r}",
    )


def required(row: dict[str, str], field: str, row_number: int) -> str:
    value = str(row.get(field, "") or "").strip()
    if not value:
        raise MaterializationError(f"selection 第 {row_number} 行缺少 {field}")
    return value


def as_nonnegative_int(value: Any, *, field: str, row_number: int) -> int:
    try:
        result = int(str(value or "0").strip())
    except ValueError as exc:
        raise MaterializationError(f"selection 第 {row_number} 行 {field} 不是整数: {value!r}") from exc
    if result < 0:
        raise MaterializationError(f"selection 第 {row_number} 行 {field} 不能为负数")
    return result


def source_path(raw: str) -> Path:
    return Path(raw).expanduser().resolve()


def inferred_ingest_mode(extension: str) -> str:
    return "file" if extension.lower() in BINARY_FILE_EXTENSIONS else "manual"


def validate_metadata_title(value: str, *, row_number: int) -> str:
    value = value.strip()
    if not value:
        raise MaterializationError(f"selection 第 {row_number} 行 metadata_title 不能为空")
    if any(char in value for char in ("\x00", "\r", "\n", "/", "\\")):
        raise MaterializationError(
            f"selection 第 {row_number} 行 metadata_title 不能含换行、路径分隔符或 NUL；"
            "file 模式会将它作为安全的实验显示文件名。",
        )
    if len(value.encode("utf-8")) > 220:
        raise MaterializationError(
            f"selection 第 {row_number} 行 metadata_title UTF-8 长度超过 220 字节；请缩短为明确 issuer/date/version 标签。",
        )
    return value


def split_partners(value: str) -> list[str]:
    # Comma and semicolon both work so the CSV remains comfortable in Chinese
    # spreadsheet software.  An asterisk is an explicit all-pairs request.
    return [item.strip() for item in re.split(r"[;,；|]+", value or "") if item.strip()]


def stable_pair_id(case_id: str, left: str, right: str, used: set[str]) -> str:
    base = re.sub(r"[^A-Za-z0-9_-]+", "_", f"{case_id}_{left}_{right}").strip("_")[:72]
    if not base:
        base = "pair"
    candidate = base
    if candidate in used:
        candidate = f"{base[:56]}_{hashlib.sha256((case_id + left + right).encode('utf-8')).hexdigest()[:12]}"
    used.add(candidate)
    return candidate


def read_selected_rows(path: Path, *, min_bytes: int, allow_missing_documents: bool) -> list[dict[str, Any]]:
    try:
        with path.open("r", encoding="utf-8", newline="") as handle:
            reader = csv.DictReader(handle)
            fields = set(reader.fieldnames or [])
            required_columns = {
                "include", "case_id", "document_id", "fact_family_id", "split", "expected_outcome",
                "winner", "adoption_cycles", "expected_conflict_with", "metadata_title",
                "metadata_evidence_verified", "absolute_path", "extension", "bytes", "sha256", "ingest_mode",
            }
            missing = sorted(required_columns - fields)
            if missing:
                raise MaterializationError(f"selection CSV 缺少列: {', '.join(missing)}")
            selected: list[dict[str, Any]] = []
            seen_aliases: set[str] = set()
            for row_number, raw in enumerate(reader, start=2):
                row = {key: str(value or "").strip() for key, value in raw.items()}
                if not normalize_yes_no(row.get("include"), field="include", row_number=row_number):
                    continue
                case_id = required(row, "case_id", row_number)
                document_id = required(row, "document_id", row_number)
                if document_id in seen_aliases:
                    raise MaterializationError(f"selection 中 include=yes 的 document_id 重复: {document_id}")
                seen_aliases.add(document_id)
                extension = required(row, "extension", row_number).lower()
                if not extension.startswith("."):
                    extension = "." + extension
                path_value = required(row, "absolute_path", row_number)
                resolved = source_path(path_value)
                bytes_value = as_nonnegative_int(row.get("bytes"), field="bytes", row_number=row_number)
                if bytes_value < min_bytes:
                    raise MaterializationError(
                        f"selection 第 {row_number} 行选中了仅 {bytes_value} bytes 的文件；"
                        f"低于 --min-bytes={min_bytes}，请确认它不是占位文件后再降低阈值。",
                    )
                digest = required(row, "sha256", row_number).lower()
                if not SHA256_RE.fullmatch(digest):
                    raise MaterializationError(f"selection 第 {row_number} 行 sha256 非法")
                mode = (row.get("ingest_mode") or inferred_ingest_mode(extension)).lower()
                if mode not in VALID_INGEST_MODES:
                    raise MaterializationError(f"selection 第 {row_number} 行 ingest_mode 必须为 manual/file")
                if extension in BINARY_FILE_EXTENSIONS and mode != "file":
                    raise MaterializationError(
                        f"selection 第 {row_number} 行 {extension} 是二进制文件，ingest_mode 必须为 file；"
                        "不能由 manual Markdown API 读取。",
                    )
                if mode == "file" and extension not in SUPPORTED_FILE_UPLOAD_EXTENSIONS:
                    raise MaterializationError(
                        f"selection 第 {row_number} 行 {extension} 不受当前文件上传 API 支持；"
                        "请先转换为 pdf/doc/docx/md/txt 等受支持格式。",
                    )
                if not allow_missing_documents:
                    if not resolved.is_file():
                        raise MaterializationError(f"selection 第 {row_number} 行文件不存在: {resolved}")
                    actual_size = resolved.stat().st_size
                    if actual_size != bytes_value:
                        raise MaterializationError(
                            f"selection 第 {row_number} 行文件大小已变化：CSV={bytes_value}，当前={actual_size}；"
                            "请重新 inventory/selection，避免来源漂移。",
                        )
                outcome = required(row, "expected_outcome", row_number)
                if outcome not in VALID_OUTCOMES:
                    raise MaterializationError(f"selection 第 {row_number} 行 expected_outcome 非法: {outcome}")
                split = required(row, "split", row_number)
                if split not in VALID_SPLITS:
                    raise MaterializationError(f"selection 第 {row_number} 行 split 必须为 development/holdout")
                verified = normalize_yes_no(
                    row.get("metadata_evidence_verified"), field="metadata_evidence_verified", row_number=row_number,
                )
                if not verified:
                    raise MaterializationError(
                        f"selection 第 {row_number} 行尚未确认 metadata_evidence_verified=yes；"
                        "不要把仅由文件名推断的 issuer/date/version 写入实验真值。",
                    )
                title = validate_metadata_title(row.get("metadata_title", ""), row_number=row_number)
                selected.append({
                    "row_number": row_number,
                    "case_id": case_id,
                    "document_id": document_id,
                    "fact_family_id": required(row, "fact_family_id", row_number),
                    "split": split,
                    "expected_outcome": outcome,
                    "winner": normalize_yes_no(row.get("winner"), field="winner", row_number=row_number),
                    "adoption_cycles": row.get("adoption_cycles", ""),
                    "expected_conflict_with": split_partners(row.get("expected_conflict_with", "")),
                    "title": title,
                    "metadata_evidence_location": row.get("metadata_evidence_location", ""),
                    "reviewer_note": row.get("reviewer_note", ""),
                    "path": str(resolved),
                    "extension": extension,
                    "bytes": bytes_value,
                    "sha256": digest,
                    "ingest_mode": mode,
                    "source_document_id": row.get("source_document_id", ""),
                    "relative_path": row.get("relative_path", ""),
                })
    except FileNotFoundError as exc:
        raise MaterializationError(f"找不到 selection CSV: {path}") from exc
    if not selected:
        raise MaterializationError("没有 include=yes 的行；请先在 corpus_selection.csv 标记少量已人工确认的来源")
    return selected


def build_pairs(case_id: str, rows: list[dict[str, Any]]) -> list[dict[str, str]]:
    aliases = [str(row["document_id"]) for row in rows]
    alias_set = set(aliases)
    order = {alias: index for index, alias in enumerate(aliases)}
    pair_keys: set[tuple[str, str]] = set()
    for row in rows:
        left = str(row["document_id"])
        partners = row["expected_conflict_with"]
        for partner in partners:
            if partner == "*":
                for other in aliases:
                    if other != left:
                        pair_keys.add(tuple(sorted((left, other))))
                continue
            if partner not in alias_set:
                raise MaterializationError(
                    f"case {case_id}: document {left} 的 expected_conflict_with 引用了 case 外 document_id {partner}",
                )
            if partner == left:
                raise MaterializationError(f"case {case_id}: document {left} 不能与自身组成 conflict pair")
            pair_keys.add(tuple(sorted((left, partner))))
    if not pair_keys:
        raise MaterializationError(
            f"case {case_id}: 至少需要一个 expected_conflict_with；"
            "在任一 selected row 填写伙伴 document_id，或明确填写 * 表示全对全冲突。",
        )
    used_ids: set[str] = set()
    pairs: list[dict[str, str]] = []
    for left, right in sorted(pair_keys, key=lambda item: (min(order[item[0]], order[item[1]]), max(order[item[0]], order[item[1]]))):
        pairs.append({"id": stable_pair_id(case_id, left, right, used_ids), "left": left, "right": right})
    return pairs


def normalize_cycles(value: str, *, case_id: str) -> int:
    try:
        cycles = int(value)
    except (TypeError, ValueError) as exc:
        raise MaterializationError(f"case {case_id}: adoption_cycles 必须为 1–3") from exc
    if cycles < 1 or cycles > 3:
        raise MaterializationError(f"case {case_id}: adoption_cycles 必须为 1–3")
    return cycles


def build_corpus(rows: list[dict[str, Any]], variant: str, allow_source_reuse: bool) -> tuple[dict[str, Any], dict[str, Any]]:
    by_case: dict[str, list[dict[str, Any]]] = collections.defaultdict(list)
    for row in rows:
        by_case[str(row["case_id"])].append(row)

    seen_fact_families: dict[str, str] = {}
    seen_paths: dict[str, str] = {}
    seen_hashes: dict[str, str] = {}
    cases: list[dict[str, Any]] = []
    report_cases: list[dict[str, Any]] = []
    for case_id, members in by_case.items():
        fact_family = str(members[0]["fact_family_id"])
        split = str(members[0]["split"])
        outcome = str(members[0]["expected_outcome"])
        for member in members[1:]:
            for field, expected in (("fact_family_id", fact_family), ("split", split), ("expected_outcome", outcome)):
                if str(member[field]) != expected:
                    raise MaterializationError(f"case {case_id}: 同一 case 的 {field} 必须一致")
        if fact_family in seen_fact_families and seen_fact_families[fact_family] != split:
            raise MaterializationError(f"fact_family_id {fact_family!r} 跨 development/holdout 重用，存在泄漏")
        seen_fact_families[fact_family] = split
        if len(members) < 2:
            raise MaterializationError(f"case {case_id}: 至少需要两份 selected documents")

        for member in members:
            path = str(member["path"])
            if path in seen_paths and seen_paths[path] != split:
                raise MaterializationError(f"document path 跨 split 重用，存在泄漏: {path}")
            seen_paths[path] = split
            digest = str(member["sha256"])
            if not allow_source_reuse and digest in seen_hashes:
                raise MaterializationError(
                    f"同一 SHA-256 来源被重复选入 {seen_hashes[digest]} 与 case {case_id}；"
                    "请只保留一个事实家族，或明确使用 --allow-source-reuse 并在论文中说明依赖关系。",
                )
            seen_hashes[digest] = case_id

        winners = [member for member in members if bool(member["winner"])]
        cycles_raw = {str(member["adoption_cycles"]).strip() for member in members}
        pairs = build_pairs(case_id, members)
        if outcome == "adopt_reopen":
            if len(members) < 3:
                raise MaterializationError(f"case {case_id}: adopt_reopen 正例至少需要三份来源")
            if len(winners) != 1:
                raise MaterializationError(f"case {case_id}: adopt_reopen 正例必须且只能标记一个 winner=yes")
            if len(cycles_raw) != 1 or not next(iter(cycles_raw)):
                raise MaterializationError(f"case {case_id}: adopt_reopen 正例每行的 adoption_cycles 必须一致且为 1–3")
            cycles = normalize_cycles(next(iter(cycles_raw)), case_id=case_id)
            for member in members:
                if not ISSUER_LABEL_RE.search(str(member["title"])) or not RECENCY_LABEL_RE.search(str(member["title"])):
                    raise MaterializationError(
                        f"case {case_id}: 正例的 document_id={member['document_id']} metadata_title 必须含显式 issuer 标签"
                        "以及 date/version 标签；这不是从文件名推断，须来自已确认的正文标题/header。",
                    )
            winner_document = str(winners[0]["document_id"])
        else:
            if winners:
                raise MaterializationError(f"case {case_id}: no_proposal 负例不得标记 winner=yes")
            if cycles_raw - {"", "0"}:
                raise MaterializationError(f"case {case_id}: no_proposal 负例的 adoption_cycles 必须为空或 0")
            cycles = 0
            winner_document = ""

        documents: list[dict[str, Any]] = []
        for member in members:
            documents.append({
                "id": member["document_id"],
                "path": member["path"],
                "title": member["title"],
                "ingest_mode": member["ingest_mode"],
                "source_sha256": member["sha256"],
                "source_document_id": member["source_document_id"],
                "source_relative_path": member["relative_path"],
                "metadata_evidence_location": member["metadata_evidence_location"],
            })
        notes = [str(member["reviewer_note"]).strip() for member in members if str(member["reviewer_note"]).strip()]
        cases.append({
            "id": case_id,
            "fact_family_id": fact_family,
            "split": split,
            "description": " | ".join(dict.fromkeys(notes)) or f"Human-reviewed C4.10 case {case_id}",
            "expected_outcome": outcome,
            "expected_winner_document": winner_document,
            "adoption_cycles": cycles,
            "expected_disputed_fact_count": 1,
            "expected_disputed_fact_anchor_kinds": {"claim_key": 1},
            "documents": documents,
            "expected_conflict_document_pairs": pairs,
        })
        report_cases.append({
            "case_id": case_id,
            "fact_family_id": fact_family,
            "split": split,
            "expected_outcome": outcome,
            "expected_winner_document": winner_document,
            "adoption_cycles": cycles,
            "document_count": len(documents),
            "expected_conflict_pair_count": len(pairs),
            "ingest_modes": sorted({str(member["ingest_mode"]) for member in members}),
        })

    cases.sort(key=lambda item: (str(item["split"]), str(item["id"])))
    report_cases.sort(key=lambda item: (str(item["split"]), str(item["case_id"])))
    corpus = {
        "schema_version": 1,
        "name": "",
        "description": "Materialized from an explicit human-reviewed C4.10 selection CSV. Filename/path hints were not used as truth labels.",
        "variant": variant,
        "cases": cases,
    }
    report = {
        "schema_version": 1,
        "selected_row_count": len(rows),
        "case_count": len(cases),
        "development_case_count": sum(1 for item in cases if item["split"] == "development"),
        "holdout_case_count": sum(1 for item in cases if item["split"] == "holdout"),
        "positive_case_count": sum(1 for item in cases if item["expected_outcome"] == "adopt_reopen"),
        "no_proposal_case_count": sum(1 for item in cases if item["expected_outcome"] == "no_proposal"),
        "cases": report_cases,
        "note": "Manifest materialization only; no document body/API/model/Asynq/PostgreSQL operation was performed.",
    }
    return corpus, report


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Materialize an explicitly reviewed C4.10 corpus-selection CSV into a corpus JSON manifest.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--selection", required=True, help="Edited corpus_selection.csv from C4.10 selection queue")
    parser.add_argument("--output", required=True, help="Private path for generated corpus JSON")
    parser.add_argument("--name", default="winner_lifecycle_real_pilot", help="Corpus manifest name")
    parser.add_argument("--description", default="", help="Optional corpus-level description")
    parser.add_argument("--variant", choices=sorted(VALID_VARIANTS), default="c2-rules")
    parser.add_argument("--min-bytes", type=int, default=1024, help="Reject selected files smaller than this unless intentionally lowered")
    parser.add_argument("--allow-missing-documents", action="store_true", help="Only for drafting before private files are mounted")
    parser.add_argument("--allow-source-reuse", action="store_true", help="Allow duplicate SHA-256 sources across cases (document the dependence)")
    parser.add_argument("--overwrite", action="store_true", help="Allow overwriting an existing corpus manifest")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if args.min_bytes < 0:
            raise MaterializationError("--min-bytes 不能为负数")
        selection = Path(args.selection).expanduser().resolve()
        output = Path(args.output).expanduser().resolve()
        if output.exists() and not args.overwrite:
            raise MaterializationError(f"输出 corpus 已存在: {output}（需要 --overwrite）")
        rows = read_selected_rows(
            selection, min_bytes=args.min_bytes, allow_missing_documents=args.allow_missing_documents,
        )
        corpus, report = build_corpus(rows, args.variant, args.allow_source_reuse)
        corpus["name"] = args.name.strip() or "winner_lifecycle_real_pilot"
        if args.description.strip():
            corpus["description"] = args.description.strip()
        report.update({
            "selection": str(selection),
            "output": str(output),
            "corpus_name": corpus["name"],
            "variant": args.variant,
            "min_bytes": args.min_bytes,
            "allow_missing_documents": bool(args.allow_missing_documents),
            "allow_source_reuse": bool(args.allow_source_reuse),
        })
        json_dump(output, corpus)
        report_path = output.with_suffix(".materialization.json")
        json_dump(report_path, report)
        print(f"C4.10 reviewed corpus manifest materialized: {output}")
        print(f"  cases: {report['case_count']} (development={report['development_case_count']}, holdout={report['holdout_case_count']})")
        print(f"  positive/no-proposal: {report['positive_case_count']} / {report['no_proposal_case_count']}")
        print(f"  provenance report: {report_path}")
        print("  document body/API/model/database: not accessed")
        return 0
    except MaterializationError as exc:
        print(f"[c4.10-materialize] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
