#!/usr/bin/env python3
"""Prepare a public VitaminC real-split C1/C2 transfer evaluation outside Git.

VitaminC is a claim--evidence verification resource, not a version-governance
corpus.  This adapter converts each released real example into two manual-text
documents and evaluates whether WeKnora flags their document pair as conflicting:

* REFUTES -> expected conflict
* SUPPORTS -> expected no conflict

It does *not* create C3/C4.6 metadata, a global winner label, adoption, or
reopen. Results must be described as public claim--evidence conflict-transfer
metrics, never as document-version governance accuracy.

The script only reads a user-downloaded public release and writes transformed
files/manifests outside Git. It never calls WeKnora HTTP APIs, model providers,
Asynq, Docker, or PostgreSQL.

Example (the official dedicated real archive observed in practice ships only
``test.jsonl``; the adapter then makes a recorded disjoint partition):

  python3 scripts/experiments/prepare_public_vitaminc_eval.py \
    --development-input "$HOME/weknora-public-data/vitaminc_real.zip" \
    --holdout-input "$HOME/weknora-public-data/vitaminc_real.zip" \
    --output-dir "$HOME/weknora-public-data/vitaminc-real-v1" \
    --development-per-label 10 --holdout-per-label 30

If automatic ZIP member detection is ambiguous, pass:

  --development-member <real-dev-or-train-or-test.jsonl-member> \
  --holdout-member <real-test.jsonl-member>
"""

from __future__ import annotations

import argparse
import collections
import json
import sys
import zipfile
from pathlib import Path
from typing import Any, Iterable

from public_benchmark_common import (
    PublicBenchmarkError,
    json_dump,
    is_json_like_name,
    iter_json_records,
    remove_tree_if_requested,
    require_document_text,
    safe_git_sha,
    scenario_document,
    sha256_file,
    stable_digest,
    stable_rank,
    utc_now,
    valid_variant,
    write_csv,
    write_manual_document,
    write_pair_scenario,
)


DEFAULT_SOURCE_URL = "https://github.com/TalSchuster/talschuster.github.io/raw/master/static/vitaminc_real.zip"
DEFAULT_DATA_LICENSE_URL = "https://github.com/TalSchuster/VitaminC/blob/main/DATA_LICENSE"
SUPPORT_LABELS = {"supports", "support", "entailment", "entails"}
REFUTE_LABELS = {"refutes", "refute", "contradiction", "contradicts"}


class VitaminCError(PublicBenchmarkError):
    """The released VitaminC data cannot safely become a pair evaluation."""


def text_value(value: Any) -> str:
    return ("" if value is None else str(value)).replace("\r\n", "\n").replace("\r", "\n").strip()


def dedicated_real_archive(path: Path) -> bool:
    """Recognize the official standalone ``vitaminc_real.zip`` release.

    The combined ``vitaminc.zip`` archive is intentionally not accepted as a
    substitute: its member names alone do not prove the examples are real-only.
    A user who has renamed the official archive can still pass explicit member
    names, but should retain its source URL and SHA-256 in the generated
    manifest.
    """
    return "vitaminc_real" in path.name.casefold()


def json_archive_members(path: Path) -> list[str]:
    with zipfile.ZipFile(path) as archive:
        return [name for name in archive.namelist() if not name.endswith("/") and is_json_like_name(name)]


def jsonl_stem(name: str) -> str:
    base = Path(name).name.casefold()
    for suffix in (".jsonl.gz", ".ndjson.gz", ".json.gz", ".jsonl", ".ndjson", ".json"):
        if base.endswith(suffix):
            return base[: -len(suffix)]
    return Path(name).stem.casefold()


def members_with_stems(members: list[str], stems: set[str], *, real_only: bool, archive_is_real: bool) -> list[str]:
    hits: list[str] = []
    for name in members:
        if jsonl_stem(name) not in stems:
            continue
        lower = name.casefold()
        if real_only and "synthetic" in lower:
            continue
        if real_only and not archive_is_real and "real" not in lower:
            continue
        hits.append(name)
    return hits


def source_member_for_split(path: Path, explicit_member: str, split: str, real_only: bool) -> str:
    """Choose one JSON member from an upstream archive, fail closed by default.

    Official VitaminC processors use ``train.jsonl`` / ``dev.jsonl`` /
    ``test.jsonl``. The dedicated ``vitaminc_real.zip`` release observed in
    practice is a real *test-set* package: it may contain only ``test.jsonl``,
    or ``train`` + ``test`` without ``dev``. Development selection order for a
    dedicated-real archive is therefore:

    1. unique ``dev`` / ``valid`` / ``validation`` stem
    2. unique ``train`` stem
    3. unique ``test`` stem, and only when that is also the archive's sole JSON member

    Combined archives still require a member path marked ``real``. A later
    shared-member partition, not this function, is what keeps development and
    holdout families disjoint when both splits read that sole test member.
    """
    if path.suffix.lower() != ".zip":
        if explicit_member:
            raise VitaminCError(f"{split} 输入不是 ZIP，不能设置 archive member")
        return ""
    if explicit_member:
        return explicit_member

    archive_is_real = dedicated_real_archive(path)
    members = json_archive_members(path)
    test_only: list[str] = []
    if split == "development":
        preferred = members_with_stems(members, {"dev", "valid", "validation"}, real_only=real_only, archive_is_real=archive_is_real)
        fallback = members_with_stems(members, {"train"}, real_only=real_only, archive_is_real=archive_is_real) if archive_is_real else []
        if archive_is_real:
            test_only = members_with_stems(members, {"test"}, real_only=real_only, archive_is_real=archive_is_real)
        flag = "--development-member"
    else:
        preferred = members_with_stems(members, {"test"}, real_only=real_only, archive_is_real=archive_is_real)
        fallback = []
        flag = "--holdout-member"

    if len(preferred) == 1:
        return preferred[0]
    if not preferred and len(fallback) == 1:
        return fallback[0]
    if not preferred and not fallback and len(test_only) == 1 and len(members) == 1:
        return test_only[0]
    raise VitaminCError(
        f"无法为 {split} 自动选择 VitaminC ZIP 成员。"
        f"请传 {flag}。全部 JSON 成员: {members[:40]}；"
        f"preferred={preferred[:20]}；train-fallback={fallback[:20]}；"
        f"test-only-fallback={test_only[:20]}",
    )


def normalize_label(value: Any, supports: set[str], refutes: set[str]) -> tuple[str, bool] | None:
    label = text_value(value).casefold()
    if label in supports:
        return "supports", False
    if label in refutes:
        return "refutes", True
    return None


def source_label_value(row: dict[str, Any]) -> Any:
    """Mirror the upstream processor's gold_label-over-label precedence."""
    gold = row.get("gold_label")
    return gold if text_value(gold) else row.get("label")


def record_identifier(row: dict[str, Any], claim: str, evidence: str) -> str:
    for field in ("unique_id", "id", "example_id", "uid", "pair_id"):
        value = text_value(row.get(field))
        if value:
            return value
    return stable_digest(f"{claim}\x1f{evidence}")[:24]


def build_candidate(
    row: dict[str, Any],
    *,
    split: str,
    row_index: int,
    supports: set[str],
    refutes: set[str],
    min_chars: int,
    max_chars: int,
) -> tuple[dict[str, Any] | None, str]:
    label = normalize_label(source_label_value(row), supports, refutes)
    if label is None:
        return None, "unsupported_or_missing_label"
    normalized_label, expected_conflict = label
    try:
        claim = require_document_text(row.get("claim"), "claim", minimum=min_chars, maximum=max_chars)
        evidence = require_document_text(row.get("evidence"), "evidence", minimum=min_chars, maximum=max_chars)
    except PublicBenchmarkError as exc:
        reason = str(exc)
        if "长度" in reason:
            return None, "text_length_out_of_range"
        return None, "invalid_text"
    claim_identity = " ".join(claim.split())
    evidence_identity = " ".join(evidence.split())
    family_id = "vitaminc:" + stable_digest(f"{claim_identity}\x1f{evidence_identity}")[:32]
    return {
        "family_id": family_id,
        "source_record_id": record_identifier(row, claim_identity, evidence_identity),
        "source_row_index": row_index,
        "split": split,
        "source_label": normalized_label,
        "expected_conflict": expected_conflict,
        "claim": claim,
        "evidence": evidence,
    }, ""


def scan_source(
    path: Path,
    *,
    archive_member: str,
    split: str,
    limit: int,
    supports: set[str],
    refutes: set[str],
    min_chars: int,
    max_chars: int,
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    accepted: list[dict[str, Any]] = []
    skips: collections.Counter[str] = collections.Counter()
    scanned = 0
    observed_labels: collections.Counter[str] = collections.Counter()
    for row in iter_json_records(path, archive_member=archive_member):
        if limit and scanned >= limit:
            break
        scanned += 1
        observed_labels[text_value(source_label_value(row)) or "<missing>"] += 1
        candidate, reason = build_candidate(
            row,
            split=split,
            row_index=scanned,
            supports=supports,
            refutes=refutes,
            min_chars=min_chars,
            max_chars=max_chars,
        )
        if candidate is None:
            skips[reason] += 1
            continue
        accepted.append(candidate)
    return accepted, {
        "input": str(path.resolve()),
        "archive_member": archive_member,
        "sha256": sha256_file(path),
        "bytes": path.stat().st_size,
        "scanned_record_count": scanned,
        "source_scan_complete": limit == 0,
        "source_scan_status": "complete" if limit == 0 else "partial_smoke_only",
        "strict_candidate_count": len(accepted),
        "skipped_by_structural_reason": dict(sorted(skips.items())),
        "observed_raw_labels": dict(sorted(observed_labels.items())),
    }


def unique_candidates(candidates: Iterable[dict[str, Any]], split: str) -> tuple[list[dict[str, Any]], int]:
    seen: dict[str, dict[str, Any]] = {}
    duplicates = 0
    for item in candidates:
        family = str(item["family_id"])
        prior = seen.get(family)
        if prior is None:
            seen[family] = item
            continue
        if prior["source_label"] != item["source_label"]:
            raise VitaminCError(
                f"{split} 内同一 claim/evidence fact_family_id 带有相反标签: {family}；拒绝静默去重。",
            )
        duplicates += 1
    return list(seen.values()), duplicates


def select_split(
    candidates: list[dict[str, Any]], *, split: str, per_label: int, seed: str,
) -> tuple[list[dict[str, Any]], dict[str, int]]:
    by_label: dict[str, list[dict[str, Any]]] = {"supports": [], "refutes": []}
    for item in candidates:
        item = dict(item)
        item["selection_rank"] = stable_rank(seed, f"{split}\x1f{item['family_id']}")
        by_label[str(item["source_label"])].append(item)
    selected: list[dict[str, Any]] = []
    availability: dict[str, int] = {}
    for label in ("supports", "refutes"):
        ranked = sorted(by_label[label], key=lambda item: (str(item["selection_rank"]), str(item["family_id"])))
        availability[label] = len(ranked)
        if len(ranked) < per_label:
            raise VitaminCError(
                f"{split}/{label} 可用严格样本为 {len(ranked)}，不足请求的 {per_label}；"
                "降低 --*-per-label、检查 ZIP member，或明确补充 label 映射。",
            )
        selected.extend(ranked[:per_label])
    return sorted(selected, key=lambda item: (str(item["source_label"]), str(item["selection_rank"]))), availability


def partition_shared_candidates(
    candidates: list[dict[str, Any]],
    *,
    development_per_label: int,
    holdout_per_label: int,
    seed: str,
) -> tuple[list[dict[str, Any]], list[dict[str, Any]], dict[str, int]]:
    """Deterministically split one shared real source into disjoint families.

    Official ``vitaminc_real.zip`` is a test-set package. Using its sole
    ``test.jsonl`` for both splits is valid only if development takes the
    rank prefix and holdout takes the next unused families. The rank hash
    does not include the split name, so the same family cannot be assigned
    to both sides.
    """
    needed = development_per_label + holdout_per_label
    by_label: dict[str, list[dict[str, Any]]] = {"supports": [], "refutes": []}
    for item in candidates:
        item = dict(item)
        item["selection_rank"] = stable_rank(seed, f"shared\x1f{item['family_id']}")
        by_label[str(item["source_label"])].append(item)
    selected_development: list[dict[str, Any]] = []
    selected_holdout: list[dict[str, Any]] = []
    availability: dict[str, int] = {}
    for label in ("supports", "refutes"):
        ranked = sorted(by_label[label], key=lambda item: (str(item["selection_rank"]), str(item["family_id"])))
        availability[label] = len(ranked)
        if len(ranked) < needed:
            raise VitaminCError(
                f"共享 real test member 的 {label} 可用严格样本为 {len(ranked)}，"
                f"不足 development+holdout 请求的 {needed}；"
                "降低 --*-per-label，或提供独立的 development member。",
            )
        for item in ranked[:development_per_label]:
            chosen = dict(item)
            chosen["split"] = "development"
            selected_development.append(chosen)
        for item in ranked[development_per_label:needed]:
            chosen = dict(item)
            chosen["split"] = "holdout"
            selected_holdout.append(chosen)
    return (
        sorted(selected_development, key=lambda item: (str(item["source_label"]), str(item["selection_rank"]))),
        sorted(selected_holdout, key=lambda item: (str(item["source_label"]), str(item["selection_rank"]))),
        availability,
    )


def case_id(item: dict[str, Any]) -> str:
    label = "refutes" if item["expected_conflict"] else "supports"
    return f"vitaminc-{label}-{stable_digest(str(item['family_id']))[:14]}"


def write_case(output: Path, item: dict[str, Any]) -> dict[str, Any]:
    cid = case_id(item)
    folder = output / "documents" / str(item["split"]) / cid
    claim_path = write_manual_document(folder / "claim.md", str(item["claim"]))
    evidence_path = write_manual_document(folder / "evidence.md", str(item["evidence"]))
    relative = f"VitaminC/{item['source_record_id']}"
    claim_document = scenario_document(
        document_id="claim",
        path=claim_path,
        title=f"VitaminC claim {cid}",
        source_document_id=f"{item['source_record_id']}:claim",
        source_relative_path=relative,
    )
    evidence_document = scenario_document(
        document_id="evidence",
        path=evidence_path,
        title=f"VitaminC evidence {cid}",
        source_document_id=f"{item['source_record_id']}:evidence",
        source_relative_path=relative,
    )
    scenario_path = output / "scenarios" / f"{cid}.json"
    write_pair_scenario(
        scenario_path,
        name=f"vitaminc_pair_{cid}",
        description=(
            "Public VitaminC real claim--evidence transfer case. REFUTES maps to an expected document-pair conflict; "
            "SUPPORTS maps to an expected no-conflict pair. No C3/C4.6 metadata or winner action is asserted."
        ),
        fact_family_id=str(item["family_id"]),
        split=str(item["split"]),
        case_type="vitaminc_refutes" if item["expected_conflict"] else "vitaminc_supports",
        left_document=claim_document,
        right_document=evidence_document,
        expected_conflict=bool(item["expected_conflict"]),
    )
    return {
        "id": cid,
        "fact_family_id": item["family_id"],
        "split": item["split"],
        "case_type": "vitaminc_refutes" if item["expected_conflict"] else "vitaminc_supports",
        "source_label": item["source_label"],
        "expected_conflict": bool(item["expected_conflict"]),
        "scenario": str(scenario_path.resolve()),
        "expected_pair": {"left": "claim", "right": "evidence"},
        "selection_rank": item["selection_rank"],
    }


def write_readme(output: Path) -> None:
    text = """# VitaminC public conflict-transfer evaluation plan

This directory is generated from a user-downloaded VitaminC public release and
must remain outside the WeKnora Git repository. It contains transformed
claim/evidence text, scenarios, and manifests.

## Scope

Each upstream real claim--evidence record becomes two documents:

- `REFUTES` is scored as an expected conflict.
- `SUPPORTS` is scored as an expected no-conflict.

This is a C1/C2 external transfer evaluation only. It does **not** test C3
metadata extraction, C4.6 global winner proposals, adoption/reopen, native
versioned documents, or document-authority policies.

## Run order

Run an explicitly limited development smoke first:

```bash
python3 scripts/experiments/run_public_pair_eval.py \\
  --manifest <this-dir>/pair_eval_manifest.json \\
  --split development --max-cases 10 \\
  --output-dir <this-dir>/runs/development-smoke
```

After documenting any development-only adjustments and freezing
`source_manifest.json`, run the untouched full holdout once:

```bash
python3 scripts/experiments/run_public_pair_eval.py \\
  --manifest <this-dir>/pair_eval_manifest.json \\
  --split holdout \\
  --replicates 1 \\
  --output-dir <this-dir>/runs/holdout-r1
```

If stability is needed, run a separately reported, pre-ranked 10-family slice:

```bash
python3 scripts/experiments/run_public_pair_eval.py \\
  --manifest <this-dir>/pair_eval_manifest.json \\
  --split holdout --max-cases 10 --replicates 3 \\
  --output-dir <this-dir>/runs/holdout-stability-r3
```

The runner reports execution-level results and a fact-family strict-all-
replicates score. Independent service runs are not provider RNG-seeded trials.

## License and attribution

VitaminC's data license incorporates Wikipedia material and points to the
applicable Wikipedia terms / CC BY-SA 3.0 fallback. Preserve upstream
attribution and check the release's current terms before redistributing any
text or derivative artifact. Do not add the release archive or generated
examples to Git.
"""
    (output / "README.md").write_text(text, encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Prepare deterministic real-split VitaminC public pair evaluation manifests.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    source = parser.add_argument_group("released source files")
    source.add_argument("--development-input", required=True, help="Original VitaminC real development JSON/JSONL(.gz) or ZIP archive.")
    source.add_argument("--holdout-input", required=True, help="Original VitaminC real test JSON/JSONL(.gz) or ZIP archive.")
    source.add_argument("--development-member", default="", help="Explicit real development JSON member when input is ZIP.")
    source.add_argument("--holdout-member", default="", help="Explicit real test JSON member when input is ZIP.")
    source.add_argument("--real-only", action=argparse.BooleanOptionalAction, default=True, help="Require an archive member path marked real and reject synthetic members.")
    source.add_argument("--source-url", default=DEFAULT_SOURCE_URL, help="Canonical upstream release page recorded in source_manifest.json.")
    source.add_argument("--data-license-url", default=DEFAULT_DATA_LICENSE_URL, help="Upstream data-license page recorded in source_manifest.json.")
    labels = parser.add_argument_group("label mapping")
    labels.add_argument("--supports-label", action="append", default=[], help="Additional literal upstream label that means SUPPORTS; repeatable.")
    labels.add_argument("--refutes-label", action="append", default=[], help="Additional literal upstream label that means REFUTES; repeatable.")
    sampling = parser.add_argument_group("deterministic selection")
    sampling.add_argument("--development-per-label", type=int, default=10, help="Selected SUPPORTS and REFUTES development records.")
    sampling.add_argument("--holdout-per-label", type=int, default=30, help="Selected SUPPORTS and REFUTES holdout records.")
    sampling.add_argument("--selection-seed", default="weknora-vitaminc-real-public-v1", help="Recorded sampling hash salt; not a model RNG seed.")
    sampling.add_argument("--min-chars", type=int, default=12, help="Minimum characters in both source texts.")
    sampling.add_argument("--max-chars", type=int, default=2400, help="Maximum characters in either source text before structural exclusion.")
    sampling.add_argument("--max-development-records", type=int, default=0, help="Cap development scan for smoke-only selection; 0 scans all.")
    sampling.add_argument("--max-holdout-records", type=int, default=0, help="Cap holdout scan for smoke-only selection; 0 scans all.")
    parser.add_argument("--variant", default="c2-rules", help="WeKnora detector variant stored in the manifest.")
    parser.add_argument("--output-dir", required=True, help="Dedicated external directory for generated docs and manifests.")
    parser.add_argument("--dry-run", action="store_true", help="Read/validate/select only; write no generated files.")
    parser.add_argument("--overwrite", action="store_true", help="Explicitly replace an existing output directory.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        variant = valid_variant(args.variant)
        for flag, count in (
            ("--development-per-label", args.development_per_label),
            ("--holdout-per-label", args.holdout_per_label),
        ):
            if count < 1:
                raise VitaminCError(f"{flag} 必须为正整数")
        if args.min_chars < 1 or args.max_chars < args.min_chars:
            raise VitaminCError("文本长度范围非法：需满足 1 <= min-chars <= max-chars")
        if args.max_development_records < 0 or args.max_holdout_records < 0:
            raise VitaminCError("--max-*-records 不能为负数")

        supports = SUPPORT_LABELS | {text_value(value).casefold() for value in args.supports_label if text_value(value)}
        refutes = REFUTE_LABELS | {text_value(value).casefold() for value in args.refutes_label if text_value(value)}
        if supports & refutes:
            raise VitaminCError("SUPPORTS 与 REFUTES label 映射重叠，拒绝不明确的 gold 映射")

        development_path = Path(args.development_input).expanduser().resolve()
        holdout_path = Path(args.holdout_input).expanduser().resolve()
        if not development_path.is_file() or not holdout_path.is_file():
            raise VitaminCError("development-input 与 holdout-input 都必须是存在的公开 release 文件")
        development_member = source_member_for_split(development_path, args.development_member, "development", args.real_only)
        holdout_member = source_member_for_split(holdout_path, args.holdout_member, "holdout", args.real_only)
        shared_source = development_path == holdout_path and development_member == holdout_member
        if shared_source:
            if args.max_development_records != args.max_holdout_records:
                raise VitaminCError(
                    "同一 VitaminC source member 做 adapter-defined disjoint split 时，"
                    "--max-development-records 与 --max-holdout-records 必须相同。",
                )
            shared_raw, shared_source_info = scan_source(
                development_path,
                archive_member=development_member,
                split="shared",
                limit=args.max_development_records,
                supports=supports,
                refutes=refutes,
                min_chars=args.min_chars,
                max_chars=args.max_chars,
            )
            shared, shared_duplicates = unique_candidates(shared_raw, "shared")
            selected_development, selected_holdout, shared_available = partition_shared_candidates(
                shared,
                development_per_label=args.development_per_label,
                holdout_per_label=args.holdout_per_label,
                seed=args.selection_seed,
            )
            development_source = dict(shared_source_info)
            holdout_source = dict(shared_source_info)
            development_duplicates = shared_duplicates
            holdout_duplicates = 0
            dev_available = dict(shared_available)
            holdout_available = dict(shared_available)
            split_policy = {
                "kind": "adapter_defined_disjoint_partition_of_shared_source",
                "reason": (
                    "development 与 holdout 读取同一 source member；"
                    "官方 dedicated vitaminc_real.zip 在实践中可能只含 test.jsonl。"
                    "development 取每个 label 的 selection_rank 前缀，holdout 取随后未使用的 family。"
                ),
                "shared_input": str(development_path),
                "shared_archive_member": development_member,
                "development_slice": "first development_per_label unique families per label by selection_rank",
                "holdout_slice": "next holdout_per_label unique families per label by selection_rank",
                "native_official_train_dev_test": False,
                "dedicated_real_archive": dedicated_real_archive(development_path),
            }
        else:
            development_raw, development_source = scan_source(
                development_path,
                archive_member=development_member,
                split="development",
                limit=args.max_development_records,
                supports=supports,
                refutes=refutes,
                min_chars=args.min_chars,
                max_chars=args.max_chars,
            )
            holdout_raw, holdout_source = scan_source(
                holdout_path,
                archive_member=holdout_member,
                split="holdout",
                limit=args.max_holdout_records,
                supports=supports,
                refutes=refutes,
                min_chars=args.min_chars,
                max_chars=args.max_chars,
            )
            development, development_duplicates = unique_candidates(development_raw, "development")
            holdout, holdout_duplicates = unique_candidates(holdout_raw, "holdout")
            development_families = {str(item["family_id"]) for item in development}
            holdout_families = {str(item["family_id"]) for item in holdout}
            overlap = development_families & holdout_families
            if overlap:
                raise VitaminCError(
                    f"原始 development/holdout 出现 {len(overlap)} 个完全相同 claim/evidence family；"
                    "拒绝自动去除，避免对 split 泄漏作无声处理。",
                )
            selected_development, dev_available = select_split(
                development,
                split="development",
                per_label=args.development_per_label,
                seed=args.selection_seed,
            )
            selected_holdout, holdout_available = select_split(
                holdout,
                split="holdout",
                per_label=args.holdout_per_label,
                seed=args.selection_seed,
            )
            split_policy = {
                "kind": "separate_source_members",
                "development_archive_member": development_member,
                "holdout_archive_member": holdout_member,
                "native_official_train_dev_test": bool(development_member) and bool(holdout_member) and development_member != holdout_member,
            }
        selected = selected_development + selected_holdout
        selected_families = [str(item["family_id"]) for item in selected]
        if len(selected_families) != len(set(selected_families)):
            raise VitaminCError("选择结果出现 fact_family_id 重复，拒绝生成可能泄漏的评测集")
        source_scan_complete = development_source["source_scan_complete"] and holdout_source["source_scan_complete"]
        summary = {
            "schema_version": 1,
            "created_at": utc_now(),
            "git_commit": safe_git_sha(),
            "adapter": "prepare_public_vitaminc_eval.py",
            "dataset": {
                "name": "VitaminC",
                "source_url": args.source_url,
                "data_license_url": args.data_license_url,
                "real_only_requested": args.real_only,
                "development_real_identity": (
                    "dedicated_vitaminc_real_archive_filename"
                    if dedicated_real_archive(development_path) else "member_path_or_user_assertion"
                ),
                "holdout_real_identity": (
                    "dedicated_vitaminc_real_archive_filename"
                    if dedicated_real_archive(holdout_path) else "member_path_or_user_assertion"
                ),
                "development": development_source,
                "holdout": holdout_source,
            },
            "label_mapping": {
                "supports": sorted(supports),
                "refutes": sorted(refutes),
                "policy": "REFUTES -> expected conflict; SUPPORTS -> expected no-conflict.",
            },
            "selection": {
                "selection_seed": args.selection_seed,
                "development_per_label": args.development_per_label,
                "holdout_per_label": args.holdout_per_label,
                "development_available_by_label": dev_available,
                "holdout_available_by_label": holdout_available,
                "development_duplicate_rows_dropped": development_duplicates,
                "holdout_duplicate_rows_dropped": holdout_duplicates,
                "source_scan_complete": source_scan_complete,
                "source_scan_status": "complete" if source_scan_complete else "partial_smoke_only",
                "split_policy": split_policy,
            },
            "evaluation_scope": {
                "task": "Public claim--evidence conflict-detection transfer.",
                "not_supported": [
                    "C3 metadata extraction accuracy",
                    "C4.6 global winner proposal accuracy",
                    "adoption or reopen correctness",
                    "native versioned-document accuracy",
                    "real enterprise-document accuracy",
                    "provider-RNG-seed-controlled causal claims",
                ],
            },
            "variant": variant,
            "selected_case_count": len(selected),
        }
        if args.dry_run:
            print(json.dumps(summary, ensure_ascii=False, indent=2))
            return 0

        output = remove_tree_if_requested(Path(args.output_dir), args.overwrite)
        cases = [write_case(output, item) for item in selected]
        manifest = {
            "schema_version": 1,
            "name": "vitaminc_real_public_pair_transfer",
            "task": "public_claim_evidence_conflict_detection_transfer",
            "description": (
                "Public VitaminC real claim--evidence transfer. REFUTES labels are conflict positives and SUPPORTS "
                "labels are no-conflict controls. This manifest intentionally has no winner-governance semantics."
            ),
            "variant": variant,
            "source_manifest": str((output / "source_manifest.json").resolve()),
            "cases": cases,
        }
        json_dump(output / "source_manifest.json", summary)
        json_dump(output / "pair_eval_manifest.json", manifest)
        catalog_rows = []
        for item in selected:
            cid = case_id(item)
            catalog_rows.append({
                "case_id": cid,
                "fact_family_id": item["family_id"],
                "split": item["split"],
                "source_label": item["source_label"],
                "expected_conflict": str(bool(item["expected_conflict"])).lower(),
                "source_record_id": item["source_record_id"],
                "source_row_index": item["source_row_index"],
                "selection_rank": item["selection_rank"],
                "claim_characters": len(item["claim"]),
                "evidence_characters": len(item["evidence"]),
            })
        write_csv(
            output / "case_catalog.csv",
            catalog_rows,
            [
                "case_id", "fact_family_id", "split", "source_label", "expected_conflict", "source_record_id",
                "source_row_index", "selection_rank", "claim_characters", "evidence_characters",
            ],
        )
        write_readme(output)
        print(f"VitaminC public pair transfer plan generated: {output}")
        print(
            "  cases: "
            f"{len(cases)} (development={len(selected_development)}, holdout={len(selected_holdout)})",
        )
        print(f"  split policy: {split_policy['kind']}")
        print(f"  native VitaminC train/dev/test: {str(split_policy.get('native_official_train_dev_test')).lower()}")
        print(f"  source scan: {'complete' if source_scan_complete else 'PARTIAL / smoke only'}")
        print("  HTTP/model/Asynq/Docker/PostgreSQL: not contacted")
        return 0
    except PublicBenchmarkError as exc:
        print(f"[vitaminc-public] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
