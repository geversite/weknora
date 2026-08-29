#!/usr/bin/env python3
"""Finalize a dual-scope gold-v2 candidate from reviewed scope decisions.

Inputs:
  * an immutable full gold-v2 candidate directory;
  * a completed broad/narrow/dedup scope review CSV.

Outputs:
  * a new broad-maintenance candidate corpus (gold-v1 + approved broad v2 rows);
  * a narrow-conflict manifest containing the existing P1-P5/N1 gold IDs plus
    approved narrow v2 additions.

The input candidate and gold-v1 are never modified. This is still a candidate
artifact until independently reviewed/promoted.
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
DEFAULT_CONTRADICTIONS = ROOT / "testdata/claims_eval/contradictions.json"
VALID_YES_NO = {"yes", "no"}
VALID_DEDUP = {"keep", "merge", "exclude"}


class FinalizeError(RuntimeError):
    pass


def normalized(value: str | None) -> str:
    return (value or "").strip()


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise FinalizeError(f"找不到文件: {path}") from exc
    except json.JSONDecodeError as exc:
        raise FinalizeError(f"JSON 无法解析: {path}: {exc}") from exc


def write_json(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def read_scope(path: Path) -> dict[str, dict[str, str]]:
    try:
        with path.open(encoding="utf-8-sig", newline="") as handle:
            rows = list(csv.DictReader(handle))
    except FileNotFoundError as exc:
        raise FinalizeError(f"找不到 scope review CSV: {path}") from exc
    required = {"gold_id", "broad_maintenance", "conflict_critical", "dedup_decision", "merge_into_gold_id"}
    if not rows:
        raise FinalizeError("scope review CSV 没有数据行")
    missing = required - set(rows[0])
    if missing:
        raise FinalizeError("scope review CSV 缺少列: " + ", ".join(sorted(missing)))
    out: dict[str, dict[str, str]] = {}
    for row in rows:
        gold_id = normalized(row.get("gold_id"))
        if not gold_id or gold_id in out:
            raise FinalizeError(f"scope review gold_id 缺失或重复: {gold_id!r}")
        row = {key: normalized(value) for key, value in row.items()}
        row["broad_maintenance"] = row["broad_maintenance"].lower()
        row["conflict_critical"] = row["conflict_critical"].lower()
        row["dedup_decision"] = row["dedup_decision"].lower()
        if row["broad_maintenance"] not in VALID_YES_NO or row["conflict_critical"] not in VALID_YES_NO:
            raise FinalizeError(f"{gold_id} 的 broad_maintenance/conflict_critical 必须为 yes/no")
        if row["dedup_decision"] not in VALID_DEDUP:
            raise FinalizeError(f"{gold_id} 的 dedup_decision 必须为 keep/merge/exclude")
        out[gold_id] = row
    return out


def conflict_base_gold_ids(path: Path) -> list[str]:
    raw = read_json(path)
    ids: list[str] = []
    for pair in raw.get("pairs") or []:
        for side_name in ("a", "b"):
            value = normalized((pair.get(side_name) or {}).get("gold_id"))
            if value and value not in ids:
                ids.append(value)
    return ids


def main() -> int:
    parser = argparse.ArgumentParser(description="Finalize broad gold-v2 candidate and narrow conflict manifest.")
    parser.add_argument("--candidate-dir", required=True, help="Full gold_v2_candidate_reviewed directory")
    parser.add_argument("--scope-review", required=True, help="Completed/recommended gold_v2_scope_review CSV")
    parser.add_argument("--broad-output", required=True, help="New broad gold-v2 candidate directory")
    parser.add_argument("--narrow-manifest", required=True, help="New narrow conflict JSON manifest path")
    parser.add_argument("--contradictions", default=str(DEFAULT_CONTRADICTIONS))
    parser.add_argument("--overwrite", action="store_true", help="Allow overwriting non-empty output paths")
    args = parser.parse_args()

    try:
        candidate_dir = Path(args.candidate_dir).expanduser().resolve()
        scope_path = Path(args.scope_review).expanduser().resolve()
        broad_dir = Path(args.broad_output).expanduser().resolve()
        manifest_path = Path(args.narrow_manifest).expanduser().resolve()
        contradictions_path = Path(args.contradictions).expanduser().resolve()
        provenance_path = candidate_dir / "provenance.json"
        if not candidate_dir.is_dir() or not provenance_path.is_file():
            raise FinalizeError(f"candidate/provenance 不存在: {candidate_dir}")
        if broad_dir.exists() and any(broad_dir.iterdir()) and not args.overwrite:
            raise FinalizeError(f"broad 输出目录已存在且非空: {broad_dir}（使用 --overwrite）")
        if manifest_path.exists() and not args.overwrite:
            raise FinalizeError(f"narrow manifest 已存在: {manifest_path}（使用 --overwrite）")

        source_provenance = read_json(provenance_path)
        entries = source_provenance.get("entries") or []
        scope = read_scope(scope_path)
        entry_by_id = {normalized(entry.get("gold_id")): entry for entry in entries}
        candidate_ids = set(entry_by_id)
        if not candidate_ids:
            raise FinalizeError("candidate provenance 没有 entries")
        if candidate_ids != set(scope):
            missing = sorted(candidate_ids - set(scope))
            extra = sorted(set(scope) - candidate_ids)
            detail = []
            if missing:
                detail.append("scope review 缺少: " + ", ".join(missing))
            if extra:
                detail.append("scope review 多出: " + ", ".join(extra))
            raise FinalizeError("; ".join(detail))

        # Validate merge targets and selection invariants before writing any
        # candidate files.
        for gold_id, decision in scope.items():
            action = decision["dedup_decision"]
            if action == "merge":
                target = decision["merge_into_gold_id"]
                if target not in scope:
                    raise FinalizeError(f"{gold_id} 的 merge target 不存在: {target}")
                target_decision = scope[target]
                if target_decision["dedup_decision"] != "keep" or target_decision["broad_maintenance"] != "yes":
                    raise FinalizeError(f"{gold_id} 的 merge target 必须是 broad keep: {target}")
                if decision["conflict_critical"] == "yes" and target_decision["conflict_critical"] != "yes":
                    raise FinalizeError(f"{gold_id} 是 narrow critical merge，目标也必须是 narrow critical: {target}")
            if decision["conflict_critical"] == "yes" and (
                decision["broad_maintenance"] != "yes" or action == "exclude"
            ):
                raise FinalizeError(f"{gold_id} 标为 narrow critical 时必须保留在 broad scope（不能 exclude）")

        broad_keep_ids = {
            gold_id for gold_id, decision in scope.items()
            if decision["broad_maintenance"] == "yes" and decision["dedup_decision"] == "keep"
        }
        narrow_keep_ids = {
            gold_id for gold_id, decision in scope.items()
            if decision["conflict_critical"] == "yes" and decision["dedup_decision"] == "keep"
        }
        merged_ids = {gold_id for gold_id, decision in scope.items() if decision["dedup_decision"] == "merge"}
        excluded_ids = {
            gold_id for gold_id, decision in scope.items()
            if decision["dedup_decision"] == "exclude" or decision["broad_maintenance"] == "no"
        }

        broad_dir.mkdir(parents=True, exist_ok=True)
        source_doc_paths = sorted(path for path in candidate_dir.glob("*.json") if path.name != "provenance.json")
        if not source_doc_paths:
            raise FinalizeError("candidate 目录没有 gold JSON 文档")
        v2_ids_written: set[str] = set()
        base_claim_count = 0
        for source_path in source_doc_paths:
            document = read_json(source_path)
            filtered_claims: list[dict[str, Any]] = []
            for claim in document.get("claims") or []:
                claim_id = normalized(claim.get("id"))
                if claim_id in candidate_ids:
                    if claim_id in broad_keep_ids:
                        filtered_claims.append(claim)
                        v2_ids_written.add(claim_id)
                    continue
                filtered_claims.append(claim)
                base_claim_count += 1
            document["claims"] = filtered_claims
            write_json(broad_dir / source_path.name, document)

        if v2_ids_written != broad_keep_ids:
            raise FinalizeError("部分 broad keep claim 未在 candidate JSON 中找到: " + ", ".join(sorted(broad_keep_ids - v2_ids_written)))

        narrow_base_ids = conflict_base_gold_ids(contradictions_path)
        narrow_entries = []
        for gold_id in sorted(narrow_keep_ids):
            entry = dict(entry_by_id[gold_id])
            entry["scope_decision"] = scope[gold_id]
            narrow_entries.append(entry)

        scope_sha256 = hashlib.sha256(scope_path.read_bytes()).hexdigest()
        broad_provenance = {
            "kind": "gold-v2-broad-candidate",
            "generated_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat(),
            "source_candidate_dir": str(candidate_dir),
            "source_candidate_provenance": str(provenance_path),
            "scope_review": str(scope_path),
            "scope_review_sha256": scope_sha256,
            "base_gold_v1_claims": base_claim_count,
            "broad_v2_additions_kept": sorted(broad_keep_ids),
            "broad_v2_addition_count": len(broad_keep_ids),
            "merged_v2_additions": {
                gold_id: scope[gold_id]["merge_into_gold_id"] for gold_id in sorted(merged_ids)
            },
            "excluded_v2_additions": sorted(excluded_ids),
            "entries": [
                {**entry_by_id[gold_id], "scope_decision": scope[gold_id]}
                for gold_id in sorted(broad_keep_ids)
            ],
        }
        write_json(broad_dir / "provenance.json", broad_provenance)
        (broad_dir / "README.md").write_text(
            "# Broad-maintenance gold-v2 candidate\n\n"
            "This candidate contains immutable gold-v1 plus review-approved, deduplicated broad-maintenance additions. "
            "It is not a replacement for gold-v1 until independently reviewed and promoted.\n\n"
            f"- Base gold-v1 claims: `{base_claim_count}`\n"
            f"- Broad v2 additions kept: `{len(broad_keep_ids)}`\n"
            f"- Merged v2 additions: `{len(merged_ids)}`\n"
            f"- Excluded v2 additions: `{len(excluded_ids)}`\n"
            f"- Scope review SHA-256: `{scope_sha256}`\n",
            encoding="utf-8",
        )

        narrow_manifest = {
            "kind": "gold-v2-narrow-conflict-manifest",
            "generated_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat(),
            "source_broad_candidate_dir": str(broad_dir),
            "scope_review": str(scope_path),
            "scope_review_sha256": scope_sha256,
            "base_gold_v1_ids": narrow_base_ids,
            "base_gold_v1_count": len(narrow_base_ids),
            "v2_additions": narrow_entries,
            "v2_addition_ids": sorted(narrow_keep_ids),
            "total_narrow_claims": len(narrow_base_ids) + len(narrow_keep_ids),
            "notes": [
                "Base IDs are the P1-P5/N1 claims declared in contradictions.json.",
                "This is a metric manifest, not a standalone evaluator gold directory; use a scope-aware metric calculator.",
            ],
        }
        write_json(manifest_path, narrow_manifest)

        print(f"Broad gold-v2 candidate written: {broad_dir}")
        print(f"  base gold-v1 claims: {base_claim_count}")
        print(f"  broad additions kept: {len(broad_keep_ids)}; merged: {len(merged_ids)}; excluded: {len(excluded_ids)}")
        print(f"Narrow conflict manifest written: {manifest_path}")
        print(f"  base narrow claims: {len(narrow_base_ids)}; v2 additions: {len(narrow_keep_ids)}; total: {len(narrow_base_ids) + len(narrow_keep_ids)}")
        return 0
    except FinalizeError as exc:
        print(f"[gold-v2-finalize] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
