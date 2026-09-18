#!/usr/bin/env python3
"""Prepare a public WikiFactDiff transfer-evaluation corpus outside Git.

This adapter serves two deliberately separate purposes:

1. A balanced pair-level C1/C2 conflict-detection transfer set. Replacement
   updates are positive pairs; explicit WikiFactDiff static/`keep` facts
   duplicated across snapshots are narrow no-conflict controls.
2. A replacement-only two-snapshot C3/C4.6 advisory-proposal transfer set.
   Its document title metadata is *derived from WikiFactDiff's two public
   snapshot dates*, not extracted from native document headers. It therefore
   tests public temporal-fact and two-source proposal transfer, not native
   document-header extraction, multi-authority abstention, adoption, or reopen.

It only reads a public dataset and writes a local corpus plan/documents.  It
never calls WeKnora HTTP APIs, a model provider, Asynq, Docker, or PostgreSQL.
Raw downloads and generated texts should remain outside the repository.

Examples:

  # Stream the official Hugging Face release (requires: pip install datasets).
  python3 scripts/experiments/prepare_public_wikifactdiff_eval.py \
    --output-dir "$HOME/weknora-public-data/wikifactdiff-20210104-20230227-legacy" \
    --development-per-label 10 --holdout-per-label 30

  # Use an already exported local JSONL/JSON source without extra packages.
  python3 scripts/experiments/prepare_public_wikifactdiff_eval.py \
    --input /data/wikifactdiff.jsonl \
    --output-dir "$HOME/weknora-public-data/wikifactdiff-local" \
    --development-per-label 10 --holdout-per-label 30
"""

from __future__ import annotations

import argparse
import collections
import json
import re
import sys
from pathlib import Path
from typing import Any, Iterable, Iterator

from public_benchmark_common import (
    PublicBenchmarkError,
    json_dump,
    iter_json_records,
    remove_tree_if_requested,
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


DEFAULT_HF_DATASET = "Orange/WikiFactDiff"
# The upstream dataset card marks the legacy config as the recommended release.
DEFAULT_HF_CONFIG = "20210104-20230227_legacy"
DEFAULT_HF_SPLIT = "train"
# Hugging Face dataset revision observed on 2026-09-18. Pinning prevents a
# mutable `main` branch from silently changing a paper's sample selection.
DEFAULT_HF_REVISION = "bb17ffbff7b2d28e4cd12e251af3db50d7fa18ea"
DEFAULT_SOURCE_URL = "https://huggingface.co/datasets/Orange/WikiFactDiff"
DEFAULT_DATA_LICENSE = "CC-BY-SA-4.0 (per current upstream dataset card; verify at use time)"
DEFAULT_OLD_DATE = "2021-01-04"
DEFAULT_NEW_DATE = "2023-02-27"

# Older build code uses keep / learn / forget, while the currently published
# dataset card documents static / new / obsolete. Accept both explicit release
# vocabularies, record observed values, and reject anything not structurally
# unambiguous instead of guessing a direction.
OLD_DECISIONS = {"forget", "old", "obsolete", "removed", "remove", "deleted", "delete"}
NEW_DECISIONS = {"learn", "new", "added", "add"}
STATIC_DECISIONS = {"keep", "static", "unchanged", "stable", "same"}
PLACEHOLDER_RE = re.compile(r"_{3,}|\[MASK\]|<mask>", re.IGNORECASE)
DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
GIT_SHA_RE = re.compile(r"^[0-9a-f]{40}$", re.IGNORECASE)


class WikiFactDiffError(PublicBenchmarkError):
    """A source row cannot safely become a public transfer case."""


def as_bool(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return bool(value)
    return str(value).strip().casefold() in {"1", "true", "yes", "y"}


def text_value(value: Any) -> str:
    return ("" if value is None else str(value)).replace("\r\n", "\n").replace("\r", "\n").strip()


def nested_label(value: Any) -> str:
    if isinstance(value, dict):
        for field in ("label", "name", "value", "id", "description"):
            result = text_value(value.get(field))
            if result:
                return result
        return ""
    return text_value(value)


def nested_id(value: Any) -> str:
    if isinstance(value, dict):
        for field in ("id", "qid", "entity_id", "label", "name"):
            result = text_value(value.get(field))
            if result:
                return result
        return ""
    return text_value(value)


def decision(value: Any) -> str:
    if isinstance(value, dict):
        return text_value(value.get("decision")).casefold()
    return ""


def validate_date(value: str, flag: str) -> str:
    if not DATE_RE.fullmatch(value):
        raise WikiFactDiffError(f"{flag} 必须为 YYYY-MM-DD，实际为 {value!r}")
    return value


def render_statement(prompt: str, object_label: str) -> str:
    """Fill exactly one released WikiFactDiff verbalization blank."""
    prompt = text_value(prompt)
    object_label = text_value(object_label)
    if not prompt or not object_label:
        raise WikiFactDiffError("缺少 update_prompt 或 object.label")
    matches = list(PLACEHOLDER_RE.finditer(prompt))
    if len(matches) != 1:
        raise WikiFactDiffError("update_prompt 必须恰好包含一个对象占位符（____、[MASK] 或 <mask>）")
    statement = PLACEHOLDER_RE.sub(object_label, prompt, count=1).strip()
    if len(statement) < 8 or len(statement) > 1200:
        raise WikiFactDiffError(f"verbalized statement 长度异常: {len(statement)}")
    return statement.rstrip() + ("" if statement.endswith((".", "!", "?")) else ".")


def object_identity(value: Any) -> str:
    if not isinstance(value, dict):
        return text_value(value)
    return "|".join(
        text_value(value.get(key))
        for key in ("id", "label", "description", "decision")
    )


def canonical_row_identity(row: dict[str, Any]) -> str:
    subject = row.get("subject")
    relation = row.get("relation")
    objects = row.get("objects")
    object_values = []
    if isinstance(objects, list):
        object_values = sorted(object_identity(item) for item in objects)
    return "\x1f".join([
        nested_id(subject), nested_id(relation), text_value(row.get("update_prompt")), *object_values,
    ])


def build_candidate(row: dict[str, Any], row_index: int) -> tuple[dict[str, Any] | None, str]:
    """Return a strict replacement/keep-control candidate or a skip reason."""
    subject = row.get("subject")
    relation = row.get("relation")
    subject_label = nested_label(subject)
    relation_label = nested_label(relation)
    subject_id = nested_id(subject)
    relation_id = nested_id(relation)
    prompt = text_value(row.get("update_prompt"))
    objects = row.get("objects")
    if not subject_label or not relation_label or not subject_id or not relation_id:
        return None, "missing_subject_or_relation"
    if not isinstance(objects, list) or not objects or not all(isinstance(item, dict) for item in objects):
        return None, "invalid_objects"
    if not prompt:
        return None, "missing_update_prompt"

    identity = canonical_row_identity(row)
    family_id = "wikifactdiff:" + stable_digest(identity)[:32]
    source_fingerprint = stable_digest(identity)
    indexed_objects = [
        {
            "decision": decision(item),
            "label": nested_label(item),
            "id": nested_id(item),
        }
        for item in objects
    ]
    if any(not item["decision"] or not item["label"] for item in indexed_objects):
        return None, "missing_object_decision_or_label"

    is_replace = as_bool(row.get("is_replace"))
    if is_replace:
        old = [item for item in indexed_objects if item["decision"] in OLD_DECISIONS]
        new = [item for item in indexed_objects if item["decision"] in NEW_DECISIONS]
        unknown = [
            item for item in indexed_objects
            if item["decision"] not in OLD_DECISIONS | NEW_DECISIONS
        ]
        if len(old) != 1 or len(new) != 1 or unknown or len(indexed_objects) != 2:
            return None, "not_strict_one_old_one_new_replace"
        if old[0]["label"].casefold() == new[0]["label"].casefold():
            return None, "replace_values_identical"
        try:
            old_text = render_statement(prompt, old[0]["label"])
            new_text = render_statement(prompt, new[0]["label"])
        except WikiFactDiffError:
            return None, "invalid_verbalization"
        return {
            "kind": "conflict",
            "family_id": family_id,
            "source_fingerprint": source_fingerprint,
            "source_row_index": row_index,
            "subject_id": subject_id,
            "subject_label": subject_label,
            "relation_id": relation_id,
            "relation_label": relation_label,
            "old_object_id": old[0]["id"],
            "old_object_label": old[0]["label"],
            "new_object_id": new[0]["id"],
            "new_object_label": new[0]["label"],
            "old_text": old_text,
            "new_text": new_text,
        }, ""

    static = [item for item in indexed_objects if item["decision"] in STATIC_DECISIONS]
    # A non-replacement row can retain one or more explicit static/keep facts
    # beside changed facts. Select one by stable ID/label order and write that
    # same unchanged fact twice. This is deliberately only an identical-
    # statement no-conflict control; other row objects are not uploaded.
    if not static:
        return None, "no_static_keep_control"
    static.sort(key=lambda item: (str(item["id"]), str(item["label"])))
    selected_static = static[0]
    try:
        statement = render_statement(prompt, selected_static["label"])
    except WikiFactDiffError:
        return None, "invalid_verbalization"
    return {
        "kind": "no_conflict",
        "family_id": family_id,
        "source_fingerprint": source_fingerprint,
        "source_row_index": row_index,
        "subject_id": subject_id,
        "subject_label": subject_label,
        "relation_id": relation_id,
        "relation_label": relation_label,
        "old_object_id": selected_static["id"],
        "old_object_label": selected_static["label"],
        "new_object_id": selected_static["id"],
        "new_object_label": selected_static["label"],
        "old_text": statement,
        "new_text": statement,
    }, ""


def load_hf_rows(args: argparse.Namespace) -> tuple[Iterable[dict[str, Any]], dict[str, Any]]:
    try:
        from datasets import load_dataset  # type: ignore[import-not-found]
    except ImportError as exc:
        raise WikiFactDiffError(
            "未安装 Hugging Face datasets。执行 `python3 -m pip install --user datasets`，"
            "或先导出 JSON/JSONL 后通过 --input 运行。",
        ) from exc

    kwargs: dict[str, Any] = {
        "path": args.hf_dataset,
        "split": args.hf_split,
        "streaming": args.streaming,
    }
    if args.hf_config:
        kwargs["name"] = args.hf_config
    if args.hf_revision:
        kwargs["revision"] = args.hf_revision
    try:
        dataset = load_dataset(**kwargs)
    except Exception as exc:  # dataset library has multiple backend exception types.
        raise WikiFactDiffError(f"无法加载 Hugging Face 数据集: {exc}") from exc

    resolved_revision = ""
    revision_resolution = "not_attempted"
    try:
        from huggingface_hub import HfApi  # type: ignore[import-not-found]
        info = HfApi().dataset_info(args.hf_dataset, revision=args.hf_revision or None)
        resolved_revision = str(getattr(info, "sha", "") or "")
        revision_resolution = "hub_api" if resolved_revision else "hub_api_empty"
    except Exception:
        revision_resolution = "unavailable"
    return dataset, {
        "mode": "huggingface_datasets",
        "dataset": args.hf_dataset,
        "config": args.hf_config,
        "split": args.hf_split,
        "requested_revision": args.hf_revision,
        "resolved_revision": resolved_revision,
        "revision_resolution": revision_resolution,
        "streaming": args.streaming,
    }


def load_rows(args: argparse.Namespace) -> tuple[Iterable[dict[str, Any]], dict[str, Any]]:
    if args.input:
        source = Path(args.input).expanduser().resolve()
        return iter_json_records(source, archive_member=args.archive_member), {
            "mode": "local_json",
            "input": str(source),
            "archive_member": args.archive_member,
            "sha256": sha256_file(source),
            "bytes": source.stat().st_size,
        }
    return load_hf_rows(args)


def assign_split(family_id: str, seed: str, development_percent: int) -> str:
    number = int(stable_digest(f"{seed}\x1fsplit\x1f{family_id}")[:8], 16) % 100
    return "development" if number < development_percent else "holdout"


def choose_candidates(
    candidates: Iterable[dict[str, Any]],
    *,
    seed: str,
    development_percent: int,
    development_per_label: int,
    holdout_per_label: int,
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    by_bucket: dict[tuple[str, str], list[dict[str, Any]]] = collections.defaultdict(list)
    seen: dict[str, dict[str, Any]] = {}
    duplicate_same = 0
    duplicate_inconsistent = 0
    invalid_families: set[str] = set()
    for item in candidates:
        family = str(item["family_id"])
        prior = seen.get(family)
        if prior is not None:
            if prior["kind"] == item["kind"] and prior["old_text"] == item["old_text"] and prior["new_text"] == item["new_text"]:
                duplicate_same += 1
                continue
            invalid_families.add(family)
            duplicate_inconsistent += 1
            continue
        seen[family] = item

    for family in invalid_families:
        seen.pop(family, None)
    for item in seen.values():
        split = assign_split(str(item["family_id"]), seed, development_percent)
        item = dict(item)
        item["split"] = split
        item["selection_rank"] = stable_rank(seed, str(item["family_id"]))
        by_bucket[(split, str(item["kind"]))].append(item)

    desired = {
        ("development", "conflict"): development_per_label,
        ("development", "no_conflict"): development_per_label,
        ("holdout", "conflict"): holdout_per_label,
        ("holdout", "no_conflict"): holdout_per_label,
    }
    chosen: list[dict[str, Any]] = []
    available: dict[str, int] = {}
    for bucket, count in desired.items():
        ranked = sorted(by_bucket[bucket], key=lambda item: (str(item["selection_rank"]), str(item["family_id"])))
        available[f"{bucket[0]}:{bucket[1]}"] = len(ranked)
        if len(ranked) < count:
            raise WikiFactDiffError(
                f"符合严格条件的 {bucket[0]}/{bucket[1]} 只有 {len(ranked)} 个，"
                f"不足请求的 {count} 个。可降低 --*-per-label，或检查数据版本/decision 标签。",
            )
        chosen.extend(ranked[:count])
    selected_families = [str(item["family_id"]) for item in chosen]
    if len(selected_families) != len(set(selected_families)):
        raise WikiFactDiffError("选择结果出现 fact_family_id 重复，拒绝生成可能泄漏的评测集")
    return sorted(chosen, key=lambda item: (str(item["split"]), str(item["kind"]), str(item["selection_rank"]))), {
        "available_by_bucket": available,
        "duplicate_same_family_rows_dropped": duplicate_same,
        "inconsistent_duplicate_families_dropped": duplicate_inconsistent,
    }


def case_id(item: dict[str, Any]) -> str:
    kind = "replace" if item["kind"] == "conflict" else "keep"
    return f"wfd-{kind}-{stable_digest(str(item['family_id']))[:14]}"


def write_pair_case(output: Path, item: dict[str, Any]) -> dict[str, Any]:
    cid = case_id(item)
    split = str(item["split"])
    folder = output / "pair_documents" / split / cid
    left_path = write_manual_document(folder / "snapshot_old.md", str(item["old_text"]) + "\n")
    right_path = write_manual_document(folder / "snapshot_new.md", str(item["new_text"]) + "\n")
    source_relative = f"WikiFactDiff/{item['source_fingerprint']}"
    left = scenario_document(
        document_id="snapshot_old",
        path=left_path,
        title=f"WikiFactDiff old snapshot {cid}",
        source_document_id=f"{item['source_fingerprint']}:old",
        source_relative_path=source_relative,
    )
    right = scenario_document(
        document_id="snapshot_new",
        path=right_path,
        title=f"WikiFactDiff new snapshot {cid}",
        source_document_id=f"{item['source_fingerprint']}:new",
        source_relative_path=source_relative,
    )
    scenario_path = output / "pair_scenarios" / f"{cid}.json"
    expected_conflict = item["kind"] == "conflict"
    write_pair_scenario(
        scenario_path,
        name=f"wikifactdiff_pair_{cid}",
        description=(
            "Public WikiFactDiff transfer case. An explicit obsolete/forget-to-new/learn replacement means a conflict; "
            "an explicit static/keep fact duplicated at two snapshots is a narrow no-conflict control. "
            "No document metadata or winner-governance assertion is made in this pair task."
        ),
        fact_family_id=str(item["family_id"]),
        split=split,
        case_type="wikifactdiff_replace" if expected_conflict else "wikifactdiff_keep_control",
        left_document=left,
        right_document=right,
        expected_conflict=expected_conflict,
    )
    return {
        "id": cid,
        "fact_family_id": item["family_id"],
        "split": split,
        "case_type": "wikifactdiff_replace" if expected_conflict else "wikifactdiff_keep_control",
        "source_label": "replace" if expected_conflict else "keep_control",
        "expected_conflict": expected_conflict,
        "scenario": str(scenario_path.resolve()),
        "expected_pair": {"left": "snapshot_old", "right": "snapshot_new"},
        "selection_rank": item["selection_rank"],
    }


def write_proposal_case(output: Path, item: dict[str, Any], old_date: str, new_date: str) -> dict[str, Any]:
    """Write a two-source public snapshot proposal case.

    WikiFactDiff publishes two source snapshots, not a third independently
    versioned source.  We preserve that topology instead of manufacturing a
    duplicate source merely to satisfy the three-source lifecycle matrix.
    """
    cid = case_id(item)
    split = str(item["split"])
    folder = output / "proposal_documents" / split / cid
    old_path = write_manual_document(folder / "snapshot_old.md", str(item["old_text"]) + "\n")
    new_path = write_manual_document(folder / "snapshot_new.md", str(item["new_text"]) + "\n")
    source_relative = f"WikiFactDiff/{item['source_fingerprint']}"
    # These are intentionally derived from release-level snapshot provenance.
    # Do not describe them as header extraction from an original document.
    old = scenario_document(
        document_id="snapshot_old",
        path=old_path,
        title=f"Publisher: Wikidata; Effective date: {old_date}; Version: {old_date.replace('-', '.')}",
        source_document_id=f"{item['source_fingerprint']}:old",
        source_relative_path=source_relative,
        metadata_evidence_location="Derived from the WikiFactDiff release's old snapshot date; not an original document header.",
    )
    new = scenario_document(
        document_id="snapshot_new",
        path=new_path,
        title=f"Publisher: Wikidata; Effective date: {new_date}; Version: {new_date.replace('-', '.')}",
        source_document_id=f"{item['source_fingerprint']}:new",
        source_relative_path=source_relative,
        metadata_evidence_location="Derived from the WikiFactDiff release's new snapshot date; not an original document header.",
    )
    scenario_path = output / "proposal_scenarios" / f"{cid}.json"
    scenario = {
        "schema_version": 1,
        "name": f"wikifactdiff_proposal_{cid}",
        "description": (
            "Public WikiFactDiff two-snapshot replacement transfer. The advisory winner is the new snapshot. "
            "Title dates/versions are derived release-level provenance, not native document headers."
        ),
        "fact_family_id": item["family_id"],
        "split": split,
        "case_type": "wikifactdiff_replace_snapshot_metadata",
        "min_claims_per_document": 1,
        "documents": [old, new],
        "expected_conflict_document_pairs": [{
            "id": "OLD_NEW_REPLACEMENT", "left": "snapshot_old", "right": "snapshot_new",
        }],
        "expected_disputed_fact_count": 1,
        "expected_disputed_fact_anchor_kinds": {"claim_key": 1},
        "expected_disputed_fact_winner_count": 1,
        "expected_disputed_fact_winners": [{
            "id": "NEW_SNAPSHOT_WINNER", "winner_document": "snapshot_new", "min_confidence": 0.0,
        }],
    }
    json_dump(scenario_path, scenario)
    return {
        "id": cid,
        "fact_family_id": item["family_id"],
        "split": split,
        "case_type": "wikifactdiff_replace_snapshot_metadata",
        "source_label": "replace",
        "expected_conflict": True,
        "expected_winner_document": "snapshot_new",
        "expected_winner_proposal_source_count": 2,
        "scenario": str(scenario_path.resolve()),
        "expected_pair": {"left": "snapshot_old", "right": "snapshot_new"},
        "selection_rank": item["selection_rank"],
    }


def write_readme(output: Path) -> None:
    text = """# WikiFactDiff public transfer evaluation plan

This directory is generated from a public WikiFactDiff release and must remain
outside the WeKnora Git repository. It contains transformed short Markdown
files, manifests, and plans; it is not a copy of a private corpus.

## Contents

- `pair_eval_manifest.json`: balanced C1/C2 pair-level transfer set.
  Obsolete/forget-to-new/learn replacements are conflict positives; explicit
  static/`keep`-fact duplication is a narrow no-conflict control.
- `proposal_eval_manifest.json`: replacement-only, two-snapshot C3/C4.6
  advisory-proposal transfer set.
- `source_manifest.json`: release/config/revision, selection seed, structural
  filters, source scan status, and the crucial provenance caveat.
- `case_catalog.csv`: IDs and structural metadata, without duplicating corpus
  text in a spreadsheet.

## Run order

1. Run a development pair smoke only (for example, 10 selected cases):

```bash
python3 scripts/experiments/run_public_pair_eval.py \\
  --manifest <this-dir>/pair_eval_manifest.json \\
  --split development --max-cases 10 \\
  --output-dir <this-dir>/runs/pair-development-smoke
```

2. Inspect development failures and freeze the source manifest. Do **not** tune
   against the holdout results.

3. Run the full holdout pair set once:

```bash
python3 scripts/experiments/run_public_pair_eval.py \\
  --manifest <this-dir>/pair_eval_manifest.json \\
  --split holdout \\
  --replicates 1 \\
  --output-dir <this-dir>/runs/pair-holdout-r1
```

For a separately reported, pre-ranked 10-family stability slice (do not pool
it with the full holdout):

```bash
python3 scripts/experiments/run_public_pair_eval.py \\
  --manifest <this-dir>/pair_eval_manifest.json \\
  --split holdout --max-cases 10 --replicates 3 \\
  --output-dir <this-dir>/runs/pair-holdout-stability-r3
```

4. The public two-snapshot advisory-proposal transfer is separate. Run a small
   development smoke first, then the untouched holdout:

```bash
python3 scripts/experiments/run_public_pair_eval.py \\
  --manifest <this-dir>/proposal_eval_manifest.json \\
  --split development --max-cases 10 \\
  --output-dir <this-dir>/runs/proposal-development-smoke

python3 scripts/experiments/run_public_pair_eval.py \\
  --manifest <this-dir>/proposal_eval_manifest.json \\
  --split holdout --replicates 1 \\
  --output-dir <this-dir>/runs/proposal-holdout-r1
```

The proposal report uses exact winner-plus-source-count success, not precision:
WikiFactDiff supplies only two snapshots and no public multi-authority or
no-proposal labels. It deliberately does **not** run adoption/reopen or pretend
to be the C4.9 three-source lifecycle matrix.

## Reporting boundary

WikiFactDiff represents facts from two public Wikidata snapshots and supplies
template verbalizations. This adapter derives `Publisher: Wikidata`, dates, and
versions from the release's snapshot dates only to exercise the explicit C3/C4.6
metadata contract. It does not establish performance on native document headers,
enterprise documents, multi-authority abstention, adoption/reopen, human review,
or arbitrary authority policies.
"""
    (output / "README.md").write_text(text, encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Prepare deterministic WikiFactDiff pair and two-snapshot advisory-proposal public-transfer plans.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    source = parser.add_argument_group("source (choose --input or Hugging Face loading)")
    source.add_argument("--input", default="", help="Local JSON/JSONL(.gz)/ZIP export. Avoids extra Python dependencies.")
    source.add_argument("--archive-member", default="", help="Required only when --input ZIP has multiple JSON members.")
    source.add_argument("--hf-dataset", default=DEFAULT_HF_DATASET, help="Hugging Face dataset ID when --input is omitted.")
    source.add_argument("--hf-config", default=DEFAULT_HF_CONFIG, help="Hugging Face config/subset.")
    source.add_argument("--hf-split", default=DEFAULT_HF_SPLIT, help="Hugging Face split.")
    source.add_argument("--hf-revision", default=DEFAULT_HF_REVISION, help="Pinned Hub revision by default; a resolved SHA is recorded when available.")
    source.add_argument("--streaming", action=argparse.BooleanOptionalAction, default=True, help="Use datasets streaming instead of caching the full release.")
    source.add_argument("--max-source-records", type=int, default=0, help="Cap source scan for a smoke-only selection; 0 scans the full source.")
    source.add_argument("--source-url", default=DEFAULT_SOURCE_URL, help="Canonical upstream release URL recorded in the manifest.")
    sampling = parser.add_argument_group("deterministic selection")
    sampling.add_argument("--development-per-label", type=int, default=10, help="Selected positives and negatives in the development split.")
    sampling.add_argument("--holdout-per-label", type=int, default=30, help="Selected positives and negatives in the holdout split.")
    sampling.add_argument("--development-percent", type=int, default=25, help="Hash-based source allocation percentage for development before ranking.")
    sampling.add_argument("--selection-seed", default="weknora-wikifactdiff-public-v1", help="Recorded stable hash salt; not a model RNG seed.")
    metadata = parser.add_argument_group("public snapshot metadata")
    metadata.add_argument("--old-snapshot-date", default=DEFAULT_OLD_DATE, help="Old public dataset snapshot date.")
    metadata.add_argument("--new-snapshot-date", default=DEFAULT_NEW_DATE, help="New public dataset snapshot date.")
    parser.add_argument("--variant", default="c2-rules", help="WeKnora detector variant stored in generated plans.")
    parser.add_argument("--output-dir", required=True, help="Dedicated external directory for transformed public data and plans.")
    parser.add_argument("--dry-run", action="store_true", help="Read/validate/select only; write no corpus files or plans.")
    parser.add_argument("--overwrite", action="store_true", help="Explicitly replace an existing output directory.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        variant = valid_variant(args.variant)
        if args.input and args.hf_dataset != DEFAULT_HF_DATASET:
            raise WikiFactDiffError("--input 与自定义 --hf-dataset 不能同时使用；请选择一种数据源。")
        for flag, count in (
            ("--development-per-label", args.development_per_label),
            ("--holdout-per-label", args.holdout_per_label),
        ):
            if count < 1:
                raise WikiFactDiffError(f"{flag} 必须为正整数")
        if not 1 <= args.development_percent <= 99:
            raise WikiFactDiffError("--development-percent 必须在 1–99 之间")
        if args.max_source_records < 0:
            raise WikiFactDiffError("--max-source-records 不能为负数")
        old_date = validate_date(args.old_snapshot_date, "--old-snapshot-date")
        new_date = validate_date(args.new_snapshot_date, "--new-snapshot-date")
        if old_date >= new_date:
            raise WikiFactDiffError("old snapshot date 必须早于 new snapshot date")

        rows, source_info = load_rows(args)
        accepted: list[dict[str, Any]] = []
        skips: collections.Counter[str] = collections.Counter()
        observed_decisions: collections.Counter[str] = collections.Counter()
        scanned = 0
        for row in rows:
            if args.max_source_records and scanned >= args.max_source_records:
                break
            scanned += 1
            if not isinstance(row, dict):
                skips["non_object_record"] += 1
                continue
            raw_objects = row.get("objects")
            if isinstance(raw_objects, list):
                for raw_object in raw_objects:
                    value = decision(raw_object)
                    if value:
                        observed_decisions[value] += 1
            item, reason = build_candidate(row, scanned)
            if item is None:
                skips[reason] += 1
                continue
            accepted.append(item)
        source_scan_complete = args.max_source_records == 0
        source_identity_complete = (
            source_info.get("mode") == "local_json"
            or bool(str(source_info.get("resolved_revision", "")).strip())
            or bool(GIT_SHA_RE.fullmatch(str(source_info.get("requested_revision", "")).strip()))
        )
        selected, selection_stats = choose_candidates(
            accepted,
            seed=args.selection_seed,
            development_percent=args.development_percent,
            development_per_label=args.development_per_label,
            holdout_per_label=args.holdout_per_label,
        )
        selected_conflicts = [item for item in selected if item["kind"] == "conflict"]
        if not selected_conflicts:
            raise WikiFactDiffError("没有选择到 replacement 正例，无法生成 public snapshot proposal transfer plan")

        summary = {
            "schema_version": 1,
            "created_at": utc_now(),
            "git_commit": safe_git_sha(),
            "adapter": "prepare_public_wikifactdiff_eval.py",
            "dataset": {
                "name": "WikiFactDiff",
                "source_url": args.source_url,
                "data_license": DEFAULT_DATA_LICENSE,
                **source_info,
            },
            "decision_mapping": {
                "replacement_old_aliases": sorted(OLD_DECISIONS),
                "replacement_new_aliases": sorted(NEW_DECISIONS),
                "no_conflict_control_aliases": sorted(STATIC_DECISIONS),
                "no_conflict_control": "one explicit static/keep fact duplicated as identical text",
            },
            "selection": {
                "selection_seed": args.selection_seed,
                "development_percent": args.development_percent,
                "development_per_label": args.development_per_label,
                "holdout_per_label": args.holdout_per_label,
                "max_source_records": args.max_source_records,
                "source_scan_complete": source_scan_complete,
                "source_scan_status": "complete" if source_scan_complete else "partial_smoke_only",
                "release_identity_complete": source_identity_complete,
                "release_identity_status": "resolved" if source_identity_complete else "unresolved_revision_smoke_only",
                "scanned_record_count": scanned,
                "strict_candidate_count": len(accepted),
                "strict_candidate_count_by_kind": dict(collections.Counter(item["kind"] for item in accepted)),
                "observed_object_decisions": dict(sorted(observed_decisions.items())),
                "skipped_by_structural_reason": dict(sorted(skips.items())),
                **selection_stats,
            },
            "snapshot_metadata": {
                "old_snapshot_date": old_date,
                "new_snapshot_date": new_date,
                "policy": (
                    "Title Publisher/Effective date/Version fields are derived from WikiFactDiff release-level "
                    "snapshot provenance, not extracted from native document headers."
                ),
            },
            "evaluation_scope": {
                "pair_task": "Public temporal fact transfer: obsolete/forget-to-new/learn replacement versus static/keep duplication control.",
                "proposal_task": "Replacement-only two-snapshot C3/C4.6 advisory proposal transfer.",
                "not_supported": [
                    "native document-header extraction accuracy",
                    "real enterprise-document accuracy",
                    "multi-authority abstention precision",
                    "C4.7 adoption or C4.8 reopen correctness",
                    "human-review accuracy",
                    "provider-RNG-seed-controlled causal claims",
                ],
            },
            "variant": variant,
            "selected_pair_case_count": len(selected),
            "selected_proposal_case_count": len(selected_conflicts),
        }
        if args.dry_run:
            print(json.dumps(summary, ensure_ascii=False, indent=2))
            return 0

        output = remove_tree_if_requested(Path(args.output_dir), args.overwrite)
        pair_cases = [write_pair_case(output, item) for item in selected]
        proposal_cases = [write_proposal_case(output, item, old_date, new_date) for item in selected_conflicts]
        for case in proposal_cases:
            case["variant"] = variant
        pair_manifest = {
            "schema_version": 1,
            "name": "wikifactdiff_public_pair_transfer",
            "task": "public_temporal_fact_conflict_detection_transfer",
            "description": (
                "Pair-level public WikiFactDiff transfer evaluation. Obsolete/forget-to-new/learn replacements are positives; "
                "explicit static/keep facts duplicated across snapshots are narrow no-conflict controls."
            ),
            "variant": variant,
            "source_manifest": str((output / "source_manifest.json").resolve()),
            "cases": pair_cases,
        }
        proposal_manifest = {
            "schema_version": 1,
            "name": "wikifactdiff_public_two_snapshot_proposal_transfer",
            "task": "public_two_snapshot_advisory_winner_proposal_transfer",
            "description": (
                "Replacement-only two-snapshot C3/C4.6 proposal transfer. Snapshot title metadata is derived "
                "from public release provenance; this is not a multi-authority or lifecycle experiment."
            ),
            "variant": variant,
            "source_manifest": str((output / "source_manifest.json").resolve()),
            "cases": proposal_cases,
        }
        json_dump(output / "source_manifest.json", summary)
        json_dump(output / "pair_eval_manifest.json", pair_manifest)
        json_dump(output / "proposal_eval_manifest.json", proposal_manifest)
        catalog_rows = []
        proposal_case_ids = {str(case["id"]) for case in proposal_cases}
        for item in selected:
            cid = case_id(item)
            catalog_rows.append({
                "case_id": cid,
                "fact_family_id": item["family_id"],
                "split": item["split"],
                "source_label": "replace" if item["kind"] == "conflict" else "keep_control",
                "expected_conflict": str(item["kind"] == "conflict").lower(),
                "has_two_snapshot_proposal_case": str(cid in proposal_case_ids).lower(),
                "source_row_index": item["source_row_index"],
                "source_fingerprint": item["source_fingerprint"],
                "subject_id": item["subject_id"],
                "relation_id": item["relation_id"],
                "old_object_id": item["old_object_id"],
                "new_object_id": item["new_object_id"],
                "selection_rank": item["selection_rank"],
                "statement_characters_old": len(item["old_text"]),
                "statement_characters_new": len(item["new_text"]),
            })
        write_csv(
            output / "case_catalog.csv",
            catalog_rows,
            [
                "case_id", "fact_family_id", "split", "source_label", "expected_conflict",
                "has_two_snapshot_proposal_case", "source_row_index", "source_fingerprint", "subject_id",
                "relation_id", "old_object_id", "new_object_id", "selection_rank",
                "statement_characters_old", "statement_characters_new",
            ],
        )
        write_readme(output)
        print(f"WikiFactDiff public transfer plan generated: {output}")
        print(
            "  pair cases: "
            f"{len(pair_cases)} (development={sum(case['split'] == 'development' for case in pair_cases)}, "
            f"holdout={sum(case['split'] == 'holdout' for case in pair_cases)})",
        )
        print(f"  two-snapshot proposal cases: {len(proposal_cases)}")
        print(f"  source scan: {'complete' if source_scan_complete else 'PARTIAL / smoke only'} ({scanned} records)")
        print(f"  release identity: {'resolved' if source_identity_complete else 'UNRESOLVED / smoke only'}")
        print("  HTTP/model/Asynq/Docker/PostgreSQL: not contacted")
        return 0
    except PublicBenchmarkError as exc:
        print(f"[wikifactdiff-public] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
