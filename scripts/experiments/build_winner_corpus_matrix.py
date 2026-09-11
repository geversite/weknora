#!/usr/bin/env python3
"""Validate a human-labeled version corpus and build C4.9-compatible matrices.

The source corpus manifest is intentionally data-only. It describes fact
families, documents, explicit conflict pairs, expected winner/no-winner policy,
and a development/holdout split. This tool writes generated run_claims_eval
scenarios plus all/development/holdout C4.9 matrices and dual-reviewer sheets.
It never calls a model, HTTP API, Asynq, or database.

The manifest should contain only licensed/anonymized document paths. It may
point to absolute paths outside the Git checkout; the generated matrix will
preserve those paths. Raw customer documents and generated experiment artifacts
should remain outside Git unless their licenses explicitly permit publication.
"""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import json
import subprocess
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_OUTPUT_ROOT = ROOT / "experiments/corpus_plans"
VALID_OUTCOMES = {"adopt_reopen", "no_proposal"}
VALID_SPLITS = {"development", "holdout"}
VALID_VARIANTS = {"v1", "c1", "c2-rules", "c2-batch"}


class CorpusError(RuntimeError):
    """The source corpus cannot safely be materialized into an experiment."""


def utc_stamp() -> str:
    return dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")


def safe_git_sha() -> str:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=ROOT, check=True,
            capture_output=True, text=True,
        )
        return result.stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return "unknown"


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def read_json(path: Path) -> dict[str, Any]:
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise CorpusError(f"找不到 corpus manifest: {path}") from exc
    except json.JSONDecodeError as exc:
        raise CorpusError(f"corpus manifest JSON 无法解析: {path}: {exc}") from exc
    if not isinstance(raw, dict):
        raise CorpusError("corpus manifest 根节点必须是对象")
    return raw


def resolve_document_path(raw: str) -> Path:
    path = Path(raw).expanduser()
    return path.resolve() if path.is_absolute() else (ROOT / path).resolve()


def normalize_pair(item: Any, doc_ids: set[str], case_id: str) -> dict[str, str]:
    if not isinstance(item, dict):
        raise CorpusError(f"case {case_id}: expected_conflict_document_pairs 每项必须是对象")
    pair_id = str(item.get("id", "")).strip()
    left = str(item.get("left", "")).strip()
    right = str(item.get("right", "")).strip()
    if not pair_id or not left or not right or left == right or left not in doc_ids or right not in doc_ids:
        raise CorpusError(f"case {case_id}: 非法 expected conflict pair: {item}")
    return {"id": pair_id, "left": left, "right": right}


def normalize_case(raw_case: Any, inherited_variant: str, allow_missing_documents: bool) -> dict[str, Any]:
    if not isinstance(raw_case, dict):
        raise CorpusError(f"corpus case 必须是对象: {raw_case}")
    case_id = str(raw_case.get("id", "")).strip()
    family_id = str(raw_case.get("fact_family_id", "")).strip()
    split = str(raw_case.get("split", "")).strip()
    outcome = str(raw_case.get("expected_outcome", "")).strip()
    if not case_id or not family_id:
        raise CorpusError("每个 corpus case 必须有 id 和 fact_family_id")
    if split not in VALID_SPLITS:
        raise CorpusError(f"case {case_id}: split 必须为 {sorted(VALID_SPLITS)}")
    if outcome not in VALID_OUTCOMES:
        raise CorpusError(f"case {case_id}: expected_outcome 必须为 {sorted(VALID_OUTCOMES)}")
    variant = str(raw_case.get("variant", inherited_variant)).strip()
    if variant not in VALID_VARIANTS:
        raise CorpusError(f"case {case_id}: variant 非法 {variant!r}")

    documents_raw = raw_case.get("documents")
    if not isinstance(documents_raw, list) or len(documents_raw) < 2:
        raise CorpusError(f"case {case_id}: documents 至少需要两份文档")
    documents: list[dict[str, str]] = []
    doc_ids: set[str] = set()
    for raw_doc in documents_raw:
        if not isinstance(raw_doc, dict):
            raise CorpusError(f"case {case_id}: document 必须是对象")
        doc_id = str(raw_doc.get("id", "")).strip()
        path = str(raw_doc.get("path", "")).strip()
        title = str(raw_doc.get("title", "")).strip()
        if not doc_id or not path or not title or doc_id in doc_ids:
            raise CorpusError(f"case {case_id}: document 必须有唯一 id/path/title")
        resolved = resolve_document_path(path)
        if not allow_missing_documents and not resolved.is_file():
            raise CorpusError(f"case {case_id}: document 不存在: {resolved}")
        documents.append({"id": doc_id, "path": str(resolved), "title": title})
        doc_ids.add(doc_id)

    pairs_raw = raw_case.get("expected_conflict_document_pairs")
    if not isinstance(pairs_raw, list) or not pairs_raw:
        raise CorpusError(f"case {case_id}: 必须显式声明 expected_conflict_document_pairs")
    pairs = [normalize_pair(item, doc_ids, case_id) for item in pairs_raw]
    pair_ids = [item["id"] for item in pairs]
    unordered = [tuple(sorted((item["left"], item["right"]))) for item in pairs]
    if len(set(pair_ids)) != len(pair_ids) or len(set(unordered)) != len(unordered):
        raise CorpusError(f"case {case_id}: conflict pairs 存在重复 id 或重复无向文档对")

    expected_fact_count = raw_case.get("expected_disputed_fact_count", 1)
    if isinstance(expected_fact_count, bool) or not isinstance(expected_fact_count, int) or expected_fact_count < 1:
        raise CorpusError(f"case {case_id}: expected_disputed_fact_count 必须为正整数")
    anchor_kinds = raw_case.get("expected_disputed_fact_anchor_kinds", {"claim_key": expected_fact_count})
    if not isinstance(anchor_kinds, dict) or not anchor_kinds:
        raise CorpusError(f"case {case_id}: expected_disputed_fact_anchor_kinds 必须是非空对象")

    winner = str(raw_case.get("expected_winner_document", "")).strip()
    cycles = raw_case.get("adoption_cycles", 0)
    if outcome == "adopt_reopen":
        if len(documents) < 3:
            raise CorpusError(f"case {case_id}: adopt_reopen 正例至少需要 3 个 sources")
        if winner not in doc_ids:
            raise CorpusError(f"case {case_id}: expected_winner_document 必须引用 case 内 document")
        if isinstance(cycles, bool) or not isinstance(cycles, int) or cycles < 1 or cycles > 3:
            raise CorpusError(f"case {case_id}: adoption_cycles 必须为 1–3")
    else:
        if winner or cycles not in {0, None}:
            raise CorpusError(f"case {case_id}: no_proposal case 不得设置 winner 或 adoption_cycles")
        cycles = 0

    return {
        "id": case_id,
        "fact_family_id": family_id,
        "split": split,
        "variant": variant,
        "description": str(raw_case.get("description", "")).strip(),
        "documents": documents,
        "expected_conflict_document_pairs": pairs,
        "expected_disputed_fact_count": expected_fact_count,
        "expected_disputed_fact_anchor_kinds": anchor_kinds,
        "expected_outcome": outcome,
        "expected_winner_document": winner,
        "adoption_cycles": cycles,
    }


def normalize_corpus(raw: dict[str, Any], allow_missing_documents: bool) -> dict[str, Any]:
    name = str(raw.get("name", "")).strip()
    if not name:
        raise CorpusError("corpus manifest 缺少 name")
    variant = str(raw.get("variant", "c2-rules")).strip()
    if variant not in VALID_VARIANTS:
        raise CorpusError(f"corpus variant 非法 {variant!r}")
    raw_cases = raw.get("cases")
    if not isinstance(raw_cases, list) or not raw_cases:
        raise CorpusError("corpus cases 必须是非空数组")

    cases = [normalize_case(item, variant, allow_missing_documents) for item in raw_cases]
    ids = [case["id"] for case in cases]
    if len(set(ids)) != len(ids):
        raise CorpusError("corpus case id 必须唯一")

    family_splits: dict[str, str] = {}
    path_splits: dict[str, str] = {}
    for case in cases:
        family = case["fact_family_id"]
        if family in family_splits and family_splits[family] != case["split"]:
            raise CorpusError(f"fact_family_id {family!r} 同时出现在 development 和 holdout，存在泄漏")
        family_splits[family] = case["split"]
        for document in case["documents"]:
            path = document["path"]
            if path in path_splits and path_splits[path] != case["split"]:
                raise CorpusError(f"document path 跨 split 重用，存在泄漏: {path}")
            path_splits[path] = case["split"]

    return {
        "schema_version": raw.get("schema_version", 1),
        "name": name,
        "description": str(raw.get("description", "")).strip(),
        "variant": variant,
        "cases": cases,
    }


def scenario_for_case(corpus: dict[str, Any], case: dict[str, Any]) -> dict[str, Any]:
    scenario: dict[str, Any] = {
        "schema_version": 1,
        "name": f"{corpus['name']}_{case['id']}",
        "description": case["description"] or f"C4.10 corpus case {case['id']} ({case['split']})",
        "min_claims_per_document": 1,
        "documents": case["documents"],
        "expected_conflict_document_pairs": case["expected_conflict_document_pairs"],
        "expected_disputed_fact_count": case["expected_disputed_fact_count"],
        "expected_disputed_fact_anchor_kinds": case["expected_disputed_fact_anchor_kinds"],
    }
    if case["expected_outcome"] == "adopt_reopen":
        scenario["expected_disputed_fact_winner_count"] = 1
        scenario["expected_disputed_fact_winners"] = [{
            "id": f"{case['id']}_WINNER",
            "winner_document": case["expected_winner_document"],
            "min_confidence": 0.0,
        }]
    else:
        scenario["expected_disputed_fact_winner_count"] = 0
    return scenario


def matrix_for_cases(corpus: dict[str, Any], scenario_paths: dict[str, Path], cases: list[dict[str, Any]], suffix: str) -> dict[str, Any]:
    return {
        "schema_version": 1,
        "name": f"{corpus['name']}_{suffix}",
        "description": (
            f"Generated C4.10 {suffix} matrix from corpus {corpus['name']}. "
            "Replicates are independent executions; provider RNG seed control is not claimed."
        ),
        "variant": corpus["variant"],
        "cases": [
            {
                "id": case["id"],
                "scenario": str(scenario_paths[case["id"]]),
                "expected_outcome": case["expected_outcome"],
                "expected_winner_document": case["expected_winner_document"],
                "adoption_cycles": case["adoption_cycles"],
                "variant": case["variant"],
            }
            for case in cases
        ],
    }


def write_reviewer_sheets(output: Path, cases: list[dict[str, Any]]) -> None:
    blind_fields = [
        "case_id", "fact_family_id", "split", "document_ids", "document_paths", "document_titles",
        "reviewer_label", "reviewer_winner_document", "reviewer_evidence", "reviewer_note",
    ]
    gold_fields = blind_fields[:6] + [
        "gold_expected_outcome", "gold_expected_winner_document", "gold_adoption_cycles",
        "reviewer_1_label", "reviewer_1_winner_document", "reviewer_1_evidence", "reviewer_1_note",
        "reviewer_2_label", "reviewer_2_winner_document", "reviewer_2_evidence", "reviewer_2_note",
        "adjudicated_label", "adjudicated_winner_document", "adjudicated_evidence", "adjudicated_note",
    ]
    rows = []
    for case in cases:
        rows.append({
            "case_id": case["id"],
            "fact_family_id": case["fact_family_id"],
            "split": case["split"],
            "document_ids": ";".join(doc["id"] for doc in case["documents"]),
            "document_paths": ";".join(doc["path"] for doc in case["documents"]),
            "document_titles": " | ".join(doc["title"] for doc in case["documents"]),
        })
    for file_name, fields, include_gold in (
        ("reviewer_1_blind.csv", blind_fields, False),
        ("reviewer_2_blind.csv", blind_fields, False),
        ("gold_adjudication.csv", gold_fields, True),
    ):
        with (output / file_name).open("w", encoding="utf-8", newline="") as handle:
            writer = csv.DictWriter(handle, fieldnames=fields)
            writer.writeheader()
            for base, case in zip(rows, cases):
                row = dict(base)
                if include_gold:
                    row.update({
                        "gold_expected_outcome": case["expected_outcome"],
                        "gold_expected_winner_document": case["expected_winner_document"],
                        "gold_adoption_cycles": case["adoption_cycles"],
                    })
                writer.writerow(row)


def write_output(corpus: dict[str, Any], output: Path) -> None:
    scenarios_dir = output / "generated_scenarios"
    scenario_paths: dict[str, Path] = {}
    for case in corpus["cases"]:
        path = scenarios_dir / f"{case['id']}.json"
        json_dump(path, scenario_for_case(corpus, case))
        scenario_paths[case["id"]] = path.resolve()

    all_cases = corpus["cases"]
    development = [case for case in all_cases if case["split"] == "development"]
    holdout = [case for case in all_cases if case["split"] == "holdout"]
    json_dump(output / "winner_lifecycle_matrix.all.json", matrix_for_cases(corpus, scenario_paths, all_cases, "all"))
    if development:
        json_dump(output / "winner_lifecycle_matrix.development.json", matrix_for_cases(corpus, scenario_paths, development, "development"))
    if holdout:
        json_dump(output / "winner_lifecycle_matrix.holdout.json", matrix_for_cases(corpus, scenario_paths, holdout, "holdout"))
    json_dump(output / "normalized_corpus.json", corpus)
    write_reviewer_sheets(output, all_cases)

    readme = """# Generated C4.10 corpus plan

- `winner_lifecycle_matrix.development.json`: calibration/pilot split only.
- `winner_lifecycle_matrix.holdout.json`: frozen evaluation split only.
- `winner_lifecycle_matrix.all.json`: convenience file; do not use it for a holdout claim.
- `reviewer_1_blind.csv` / `reviewer_2_blind.csv`: independent blind case-label sheets.
- `gold_adjudication.csv`: use only after both blind sheets are complete; it carries the pre-run gold labels for adjudication.

Run a split with:

```bash
python3 scripts/experiments/run_winner_lifecycle_eval.py \\
  --matrix <generated matrix> --replicates 3
```

Do not add raw licensed/private documents or this generated plan to Git unless their data policy permits it.
"""
    (output / "README.md").write_text(readme, encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Validate a C4.10 corpus manifest and generate split C4.9 matrices/reviewer sheets.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--corpus", required=True, help="Labeled corpus JSON manifest")
    parser.add_argument("--output-dir", default="", help="Defaults to experiments/corpus_plans/<timestamp>-<name>")
    parser.add_argument("--dry-run", action="store_true", help="Validate and print plan without writing matrices")
    parser.add_argument("--allow-missing-documents", action="store_true", help="Only for drafting a manifest before private documents are mounted")
    parser.add_argument("--overwrite", action="store_true", help="Allow a non-empty output directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        corpus_path = Path(args.corpus).expanduser().resolve()
        corpus = normalize_corpus(read_json(corpus_path), args.allow_missing_documents)
        output = Path(args.output_dir).expanduser().resolve() if args.output_dir else \
            DEFAULT_OUTPUT_ROOT / f"{utc_stamp()}-{corpus['name']}-{safe_git_sha()[:8]}"
        plan = {
            "corpus": corpus,
            "corpus_path": str(corpus_path),
            "output_dir": str(output),
            "git_commit": safe_git_sha(),
            "document_count": sum(len(case["documents"]) for case in corpus["cases"]),
            "case_count": len(corpus["cases"]),
            "development_cases": sum(1 for case in corpus["cases"] if case["split"] == "development"),
            "holdout_cases": sum(1 for case in corpus["cases"] if case["split"] == "holdout"),
            "note": "Validated manifest only; no API/model/database operation was performed.",
        }
        if args.dry_run:
            print(json.dumps(plan, ensure_ascii=False, indent=2))
            return 0
        if output.exists() and any(output.iterdir()) and not args.overwrite:
            raise CorpusError(f"输出目录已存在且非空: {output}（需要 --overwrite）")
        output.mkdir(parents=True, exist_ok=True)
        write_output(corpus, output)
        json_dump(output / "build_manifest.json", plan)
        print(f"C4.10 corpus plan generated: {output}")
        print(f"  cases: {plan['case_count']} (development={plan['development_cases']}, holdout={plan['holdout_cases']})")
        print(f"  documents: {plan['document_count']}")
        print("  API/model/database: not contacted")
        return 0
    except CorpusError as exc:
        print(f"[c4.10-corpus] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
