#!/usr/bin/env python3
"""Small standard-library helpers for externally sourced benchmark adapters.

The public benchmark adapters deliberately keep raw downloads and generated
text documents outside this Git repository.  This module contains only
formatting, deterministic-selection, and JSON/CSV utilities; it does not call
WeKnora services, a model provider, Docker, or a database.
"""

from __future__ import annotations

import csv
import datetime as dt
import gzip
import hashlib
import io
import json
import os
import re
import shutil
import subprocess
import sys
import unicodedata
import zipfile
from pathlib import Path
from typing import Any, Iterable, Iterator, Sequence


ROOT = Path(__file__).resolve().parents[2]
VALID_VARIANTS = {"v1", "c1", "c2-rules", "c2-batch"}


def locale_is_utf8(value: str) -> bool:
    return "UTF8" in value.upper().replace("-", "")


def utf8_child_environment(base: dict[str, str] | None = None) -> dict[str, str]:
    """Force UTF-8 I/O for detector subprocesses.

    A login shell with LANG=C makes Python 3 encode stderr/HTTP helpers as
    ASCII. ExperimentError messages and cloned KB JSON then raise
    UnicodeEncodeError before any document is uploaded (empty knowledge_ids).
    Non-UTF-8 locales such as en_US (no suffix) are also replaced.
    """
    env = dict(base or os.environ)
    env["PYTHONUTF8"] = "1"
    env["PYTHONIOENCODING"] = "utf-8"
    lang = (env.get("LC_ALL") or env.get("LANG") or "").strip()
    if not lang or lang.upper() in {"C", "POSIX"} or not locale_is_utf8(lang):
        env["LANG"] = "C.UTF-8"
        env["LC_ALL"] = "C.UTF-8"
    return env


def force_utf8_stdio() -> None:
    """Best-effort UTF-8 stdout/stderr so Chinese experiment errors can print."""
    os.environ["PYTHONUTF8"] = "1"
    os.environ["PYTHONIOENCODING"] = "utf-8"
    for name in ("stdout", "stderr"):
        stream = getattr(sys, name, None)
        if stream is None:
            continue
        reconfigure = getattr(stream, "reconfigure", None)
        if callable(reconfigure):
            try:
                reconfigure(encoding="utf-8", errors="replace")
                continue
            except (OSError, ValueError, AttributeError):
                pass
        buffer = getattr(stream, "buffer", None)
        if buffer is None:
            continue
        try:
            wrapped = io.TextIOWrapper(buffer, encoding="utf-8", errors="replace", line_buffering=True)
            setattr(sys, name, wrapped)
        except (OSError, ValueError, AttributeError):
            continue


class PublicBenchmarkError(RuntimeError):
    """The public source or benchmark layout is not safe to use."""


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def utc_stamp() -> str:
    return dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")


def safe_git_sha() -> str:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
        )
    except (OSError, subprocess.CalledProcessError):
        return "unknown"
    return result.stdout.strip() or "unknown"


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2, default=str) + "\n", encoding="utf-8")


def write_csv(path: Path, rows: Sequence[dict[str, Any]], fieldnames: Sequence[str]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(fieldnames), extrasaction="ignore")
        writer.writeheader()
        for row in rows:
            writer.writerow(row)


def stable_digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def normalized_text(value: Any) -> str:
    """Return a deterministic, content-preserving-enough identity string."""
    text = unicodedata.normalize("NFKC", str(value or ""))
    return " ".join(text.split())


def require_document_text(value: Any, field: str, *, minimum: int, maximum: int) -> str:
    if not isinstance(value, str):
        raise PublicBenchmarkError(f"{field} 必须是文本字符串")
    text = value.replace("\r\n", "\n").replace("\r", "\n").strip()
    if "\x00" in text:
        raise PublicBenchmarkError(f"{field} 含有 NUL 字符，拒绝作为 manual 文本上传")
    length = len(text)
    if length < minimum or length > maximum:
        raise PublicBenchmarkError(
            f"{field} 长度为 {length}，要求在 {minimum}–{maximum} 个字符之间",
        )
    return text + "\n"


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def source_descriptor(path: Path, *, archive_member: str = "") -> dict[str, Any]:
    resolved = path.expanduser().resolve()
    if not resolved.is_file():
        raise PublicBenchmarkError(f"找不到公开数据文件: {resolved}")
    data: dict[str, Any] = {
        "path": str(resolved),
        "sha256": sha256_file(resolved),
        "bytes": resolved.stat().st_size,
    }
    if archive_member:
        data["archive_member"] = archive_member
    return data


def ensure_new_output_dir(path: Path) -> Path:
    output = path.expanduser().resolve()
    if output.exists() and any(output.iterdir()):
        raise PublicBenchmarkError(
            f"输出目录已存在且非空: {output}；请使用新的专用目录，避免把不同数据版本混在一起。",
        )
    output.mkdir(parents=True, exist_ok=True)
    return output


def write_manual_document(path: Path, text: str) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    return path.resolve()


def scenario_document(
    *,
    document_id: str,
    path: Path,
    title: str,
    source_document_id: str,
    source_relative_path: str,
    metadata_evidence_location: str = "",
) -> dict[str, str]:
    result = {
        "id": document_id,
        "path": str(path.resolve()),
        "title": title,
        "ingest_mode": "manual",
        "source_document_id": source_document_id,
        "source_relative_path": source_relative_path,
    }
    if metadata_evidence_location:
        result["metadata_evidence_location"] = metadata_evidence_location
    return result


def write_pair_scenario(
    path: Path,
    *,
    name: str,
    description: str,
    fact_family_id: str,
    split: str,
    case_type: str,
    left_document: dict[str, str],
    right_document: dict[str, str],
    expected_conflict: bool,
) -> None:
    pair = {"id": "PAIR", "left": left_document["id"], "right": right_document["id"]}
    scenario: dict[str, Any] = {
        "schema_version": 1,
        "name": name,
        "description": description,
        "fact_family_id": fact_family_id,
        "split": split,
        "case_type": case_type,
        "min_claims_per_document": 1,
        "documents": [left_document, right_document],
        "expected_conflict_document_pairs": [pair] if expected_conflict else [],
        "forbidden_conflict_document_pairs": [] if expected_conflict else [pair],
    }
    json_dump(path, scenario)


def resolve_path(raw: str, *, base: Path) -> Path:
    path = Path(raw).expanduser()
    return path.resolve() if path.is_absolute() else (base / path).resolve()


def json_record_container(raw: Any, source_name: str) -> list[dict[str, Any]]:
    if isinstance(raw, list):
        records = raw
    elif isinstance(raw, dict):
        records = None
        for key in ("data", "records", "rows", "train"):
            if isinstance(raw.get(key), list):
                records = raw[key]
                break
        # A one-row JSON export is convenient for a smoke test. Restrict this
        # fallback to recognizable record-shaped objects rather than silently
        # treating arbitrary release metadata as data.
        if records is None and any(key in raw for key in ("claim", "evidence", "objects", "subject", "relation")):
            records = [raw]
        if records is None:
            raise PublicBenchmarkError(
                f"{source_name} JSON 根节点不是数组，且未找到 data/records/rows/train 数组。",
            )
    else:
        raise PublicBenchmarkError(f"{source_name} JSON 根节点必须是数组或对象")
    if not all(isinstance(item, dict) for item in records):
        raise PublicBenchmarkError(f"{source_name} 包含非对象记录")
    return list(records)


def is_jsonl_name(name: str) -> bool:
    lower = name.casefold()
    if lower.endswith(".gz"):
        lower = lower[:-3]
    return lower.endswith((".jsonl", ".ndjson"))


def iter_jsonl_stream(handle: Iterable[str], source_name: str) -> Iterator[dict[str, Any]]:
    count = 0
    for line_number, line in enumerate(handle, start=1):
        if not line.strip():
            continue
        try:
            record = json.loads(line)
        except json.JSONDecodeError as exc:
            raise PublicBenchmarkError(f"{source_name}:{line_number} JSONL 无法解析: {exc}") from exc
        if not isinstance(record, dict):
            raise PublicBenchmarkError(f"{source_name}:{line_number} JSONL 记录不是对象")
        count += 1
        yield record
    if not count:
        raise PublicBenchmarkError(f"{source_name} 未解析到 JSONL 记录")


def iter_json_records(path: Path, *, archive_member: str = "") -> Iterator[dict[str, Any]]:
    """Yield records from JSON/JSONL(.gz) or one ZIP member without dependencies.

    Large JSONL sources are streamed line-by-line. ZIP inputs intentionally
    require either an explicit member or exactly one JSON-like member; dataset
    release archives often contain several splits, and silently taking the
    first one would make a holdout claim irreproducible.
    """
    source = path.expanduser().resolve()
    if not source.is_file():
        raise PublicBenchmarkError(f"找不到输入数据: {source}")

    if source.suffix.lower() == ".zip":
        with zipfile.ZipFile(source) as archive:
            members = [
                name for name in archive.namelist()
                if not name.endswith("/") and is_json_like_name(name)
            ]
            member = archive_member
            if member:
                if member not in members:
                    raise PublicBenchmarkError(
                        f"ZIP 中找不到指定成员 {member!r}；可用 JSON 成员: {members[:30]}",
                    )
            elif len(members) == 1:
                member = members[0]
            else:
                raise PublicBenchmarkError(
                    f"ZIP 含 {len(members)} 个 JSON 成员；请显式指定 archive_member。候选: {members[:30]}",
                )
            with archive.open(member, "r") as binary:
                if is_jsonl_name(member) and not member.casefold().endswith(".gz"):
                    with io.TextIOWrapper(binary, encoding="utf-8") as text_handle:
                        yield from iter_jsonl_stream(text_handle, f"{source}!{member}")
                else:
                    payload = binary.read()
                    if member.casefold().endswith(".gz"):
                        try:
                            payload = gzip.decompress(payload)
                        except OSError as exc:
                            raise PublicBenchmarkError(f"无法解压 ZIP 内 gzip 成员 {source}!{member}: {exc}") from exc
                    yield from records_from_bytes(payload, f"{source}!{member}")
        return

    if is_jsonl_name(source.name):
        if source.suffix.casefold() == ".gz":
            with gzip.open(source, "rt", encoding="utf-8") as text_handle:
                yield from iter_jsonl_stream(text_handle, str(source))
        else:
            with source.open("r", encoding="utf-8") as text_handle:
                yield from iter_jsonl_stream(text_handle, str(source))
        return

    with source.open("rb") as binary:
        payload = binary.read()
    if source.suffix.lower() == ".gz":
        try:
            payload = gzip.decompress(payload)
        except OSError as exc:
            raise PublicBenchmarkError(f"无法解压 gzip 文件 {source}: {exc}") from exc
    yield from records_from_bytes(payload, str(source))

def records_from_bytes(payload: bytes, source_name: str) -> Iterator[dict[str, Any]]:
    try:
        text = payload.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise PublicBenchmarkError(f"{source_name} 不是 UTF-8 JSON/JSONL: {exc}") from exc
    stripped = text.lstrip()
    if not stripped:
        raise PublicBenchmarkError(f"{source_name} 为空")
    if stripped.startswith("[") or stripped.startswith("{"):
        try:
            raw = json.loads(text)
        except json.JSONDecodeError:
            # A JSONL record also begins with '{', so fall through to line mode.
            raw = None
        if raw is not None:
            yield from json_record_container(raw, source_name)
            return
    count = 0
    for line_number, line in enumerate(text.splitlines(), start=1):
        if not line.strip():
            continue
        try:
            record = json.loads(line)
        except json.JSONDecodeError as exc:
            raise PublicBenchmarkError(f"{source_name}:{line_number} JSONL 无法解析: {exc}") from exc
        if not isinstance(record, dict):
            raise PublicBenchmarkError(f"{source_name}:{line_number} JSONL 记录不是对象")
        count += 1
        yield record
    if not count:
        raise PublicBenchmarkError(f"{source_name} 未解析到 JSONL 记录")


def is_json_like_name(name: str) -> bool:
    lower = name.lower()
    return lower.endswith((".json", ".jsonl", ".ndjson", ".json.gz", ".jsonl.gz", ".ndjson.gz"))


def find_archive_member(
    archive_path: Path,
    *,
    include_tokens: Iterable[str],
    exclude_tokens: Iterable[str] = (),
) -> str:
    """Find one archive member by case-insensitive path tokens, or fail closed."""
    source = archive_path.expanduser().resolve()
    if source.suffix.lower() != ".zip" or not source.is_file():
        raise PublicBenchmarkError(f"不是可读取的 ZIP 文件: {source}")
    required = [token.casefold() for token in include_tokens if token]
    forbidden = [token.casefold() for token in exclude_tokens if token]
    with zipfile.ZipFile(source) as archive:
        candidates = []
        for name in archive.namelist():
            lowered = name.casefold()
            if name.endswith("/") or not is_json_like_name(name):
                continue
            if all(token in lowered for token in required) and not any(token in lowered for token in forbidden):
                candidates.append(name)
    if len(candidates) != 1:
        raise PublicBenchmarkError(
            f"无法唯一定位 ZIP 数据成员（include={required}, exclude={forbidden}）；候选: {candidates[:30]}",
        )
    return candidates[0]


def valid_variant(value: str) -> str:
    variant = str(value).strip()
    if variant not in VALID_VARIANTS:
        raise PublicBenchmarkError(f"variant 必须为 {sorted(VALID_VARIANTS)}，实际为 {variant!r}")
    return variant


def stable_rank(seed: str, value: str) -> str:
    return stable_digest(f"{seed}\x1f{value}")


def safe_case_token(value: str) -> str:
    text = re.sub(r"[^a-z0-9]+", "-", value.casefold()).strip("-")
    return text[:48] or "case"


def remove_tree_if_requested(path: Path, overwrite: bool) -> Path:
    """Create an output folder; destructive replacement needs explicit opt-in."""
    return prepare_output_dir(path, overwrite=overwrite, resume=False)


def prepare_output_dir(path: Path, *, overwrite: bool, resume: bool) -> Path:
    """Create or reuse an experiment output folder.

    ``--overwrite`` deletes a non-empty directory. ``--resume`` keeps existing
    per-case detector artifacts so a Ctrl+C / fail-fast batch can continue.
    The two flags are mutually exclusive.
    """
    if overwrite and resume:
        raise PublicBenchmarkError("--overwrite 与 --resume 不能同时使用")
    output = path.expanduser().resolve()
    if output.exists() and any(output.iterdir()):
        if overwrite:
            shutil.rmtree(output)
        elif resume:
            return output
        else:
            raise PublicBenchmarkError(
                f"输出目录已存在且非空: {output}；使用新目录，或确认后传 --overwrite / --resume。",
            )
    output.mkdir(parents=True, exist_ok=True)
    return output
