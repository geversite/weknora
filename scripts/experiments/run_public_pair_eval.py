#!/usr/bin/env python3
"""Run and score a prepared public pair-level conflict-transfer manifest.

The adapter accepts pair manifests emitted by prepare_public_vitaminc_eval.py
or prepare_public_wikifactdiff_eval.py.  It invokes the existing real-service
run_claims_eval.py once per fact family and writes immutable artifacts.  It
never writes directly to the database.

A detector run whose expected pair is missed (or whose forbidden pair appears)
still has a useful completed artifact and counts as FN/FP.  Only a missing,
failed, or malformed detector artifact is marked UNEVALUABLE; such cases cannot
be silently counted as correct negatives.  With multiple independent service
runs, the headline fact-family metric is strict-all-replicates rather than an
inflated execution-level sample count.
"""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import json
import os
import shlex
import subprocess
import sys
from collections import Counter, defaultdict
from pathlib import Path
from statistics import mean
from typing import Any

from public_benchmark_common import (
    ROOT,
    PublicBenchmarkError,
    json_dump,
    remove_tree_if_requested,
    resolve_path,
    safe_git_sha,
    sha256_file,
    utc_now,
    utc_stamp,
    utf8_child_environment,
    valid_variant,
    write_csv,
)


RUNNER = ROOT / "scripts/experiments/run_claims_eval.py"
VALID_SPLITS = {"all", "development", "holdout"}


class PublicPairEvaluationError(PublicBenchmarkError):
    """A pair manifest or its result artifacts are invalid."""


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise PublicPairEvaluationError(f"找不到 JSON 文件: {path}") from exc
    except json.JSONDecodeError as exc:
        raise PublicPairEvaluationError(f"JSON 无法解析: {path}: {exc}") from exc


def as_int(value: Any) -> int | None:
    if isinstance(value, bool):
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def ratio(numerator: int, denominator: int) -> float | None:
    return round(numerator / denominator, 6) if denominator else None


def document_pair_match(rows: Any, left: str, right: str) -> bool:
    if not isinstance(rows, list):
        return False
    expected = {left, right}
    for row in rows:
        if not isinstance(row, dict):
            continue
        observed = {str(row.get("left_document", "")), str(row.get("right_document", ""))}
        if observed == expected:
            return True
    return False


def scenario_has_assertion(scenario: dict[str, Any], pair: dict[str, str], expected_conflict: bool) -> bool:
    key = "expected_conflict_document_pairs" if expected_conflict else "forbidden_conflict_document_pairs"
    rows = scenario.get(key)
    return isinstance(rows, list) and any(
        isinstance(item, dict)
        and {str(item.get("left", "")), str(item.get("right", ""))}
        == {pair["left"], pair["right"]}
        for item in rows
    )


def scenario_has_winner_assertion(scenario: dict[str, Any], winner_document: str) -> bool:
    """Check that an optional public proposal gold label reaches the live runner."""
    if scenario.get("expected_disputed_fact_winner_count") != 1:
        return False
    rows = scenario.get("expected_disputed_fact_winners")
    return isinstance(rows, list) and any(
        isinstance(item, dict) and str(item.get("winner_document", "")) == winner_document
        for item in rows
    )


def normalize_case(raw: Any, *, manifest_dir: Path, inherited_variant: str) -> dict[str, Any]:
    if not isinstance(raw, dict):
        raise PublicPairEvaluationError(f"case 必须是对象: {raw}")
    case_id = str(raw.get("id", "")).strip()
    family = str(raw.get("fact_family_id", "")).strip()
    split = str(raw.get("split", "")).strip()
    case_type = str(raw.get("case_type", "")).strip()
    source_label = str(raw.get("source_label", "")).strip()
    scenario_raw = str(raw.get("scenario", "")).strip()
    expected_conflict = raw.get("expected_conflict")
    pair_raw = raw.get("expected_pair")
    if not case_id or not family or split not in {"development", "holdout"}:
        raise PublicPairEvaluationError(f"case 缺少 id/fact_family_id 或 split 非法: {case_id!r}")
    if not isinstance(expected_conflict, bool):
        raise PublicPairEvaluationError(f"case {case_id}: expected_conflict 必须为布尔值")
    if not isinstance(pair_raw, dict):
        raise PublicPairEvaluationError(f"case {case_id}: expected_pair 必须是对象")
    left = str(pair_raw.get("left", "")).strip()
    right = str(pair_raw.get("right", "")).strip()
    if not left or not right or left == right:
        raise PublicPairEvaluationError(f"case {case_id}: expected_pair 必须含两个不同文档 ID")
    if not scenario_raw:
        raise PublicPairEvaluationError(f"case {case_id}: 缺少 scenario")
    scenario = resolve_path(scenario_raw, base=manifest_dir)
    if not scenario.is_file():
        raise PublicPairEvaluationError(f"case {case_id}: scenario 不存在: {scenario}")
    scenario_json = read_json(scenario)
    if not isinstance(scenario_json, dict):
        raise PublicPairEvaluationError(f"case {case_id}: scenario 根节点不是对象")
    if not scenario_has_assertion(scenario_json, {"left": left, "right": right}, expected_conflict):
        polarity = "expected" if expected_conflict else "forbidden"
        raise PublicPairEvaluationError(
            f"case {case_id}: scenario 缺少与 manifest 一致的 {polarity} conflict pair assertion",
        )
    documents = scenario_json.get("documents")
    if not isinstance(documents, list):
        raise PublicPairEvaluationError(f"case {case_id}: scenario 缺少 documents")
    document_ids = {str(item.get("id", "")) for item in documents if isinstance(item, dict)}
    if {left, right} - document_ids:
        raise PublicPairEvaluationError(f"case {case_id}: expected_pair 引用了 scenario 外文档")
    for document in documents:
        if not isinstance(document, dict):
            raise PublicPairEvaluationError(f"case {case_id}: scenario 文档不是对象")
        path = resolve_path(str(document.get("path", "")), base=scenario.parent)
        if not path.is_file():
            raise PublicPairEvaluationError(f"case {case_id}: scenario 文档不存在: {path}")
    variant = str(raw.get("variant") or inherited_variant).strip()
    valid_variant(variant)
    winner_document = str(raw.get("expected_winner_document", "")).strip()
    raw_source_count = raw.get("expected_winner_proposal_source_count")
    winner_source_count: int | None = None
    if winner_document:
        if winner_document not in document_ids:
            raise PublicPairEvaluationError(
                f"case {case_id}: expected_winner_document 必须引用 scenario 内文档",
            )
        if not scenario_has_winner_assertion(scenario_json, winner_document):
            raise PublicPairEvaluationError(
                f"case {case_id}: scenario 缺少与 manifest 一致的 winner proposal assertion",
            )
        if raw_source_count is None:
            raise PublicPairEvaluationError(
                f"case {case_id}: expected_winner_proposal_source_count 不能为空",
            )
        winner_source_count = as_int(raw_source_count)
        if winner_source_count is None or winner_source_count < 2:
            raise PublicPairEvaluationError(
                f"case {case_id}: expected_winner_proposal_source_count 必须是不小于 2 的整数",
            )
    elif raw_source_count is not None:
        raise PublicPairEvaluationError(
            f"case {case_id}: 未设置 expected_winner_document 时不得设置 winner source count",
        )
    rank = str(raw.get("selection_rank", "")).strip()
    return {
        "id": case_id,
        "fact_family_id": family,
        "split": split,
        "case_type": case_type,
        "source_label": source_label,
        "expected_conflict": expected_conflict,
        "expected_pair": {"left": left, "right": right},
        "expected_winner_document": winner_document,
        "expected_winner_proposal_source_count": winner_source_count,
        "scenario": str(scenario),
        "selection_rank": rank,
        "variant": variant,
    }


def load_manifest(path: Path) -> dict[str, Any]:
    raw = read_json(path)
    if not isinstance(raw, dict):
        raise PublicPairEvaluationError("pair manifest 根节点必须是对象")
    name = str(raw.get("name", "")).strip()
    task = str(raw.get("task", "")).strip()
    if not name or not task:
        raise PublicPairEvaluationError("pair manifest 必须包含 name 和 task")
    variant = valid_variant(str(raw.get("variant", "c2-rules")))
    raw_cases = raw.get("cases")
    if not isinstance(raw_cases, list) or not raw_cases:
        raise PublicPairEvaluationError("pair manifest cases 必须是非空数组")
    cases = [normalize_case(item, manifest_dir=path.parent, inherited_variant=variant) for item in raw_cases]
    ids = [str(item["id"]) for item in cases]
    families = [str(item["fact_family_id"]) for item in cases]
    if len(ids) != len(set(ids)):
        raise PublicPairEvaluationError("pair manifest case id 必须唯一")
    if len(families) != len(set(families)):
        raise PublicPairEvaluationError(
            "pair manifest 每个 fact_family_id 只能出现一次；多 replicate 由运行器创建，避免 split 泄漏。",
        )
    return {
        "schema_version": raw.get("schema_version", 1),
        "name": name,
        "task": task,
        "description": str(raw.get("description", "")),
        "variant": variant,
        "source_manifest": str(raw.get("source_manifest", "")),
        "cases": sorted(cases, key=lambda item: (str(item["split"]), str(item["selection_rank"]), str(item["id"]))),
    }


def select_cases(cases: list[dict[str, Any]], split: str, max_cases: int) -> list[dict[str, Any]]:
    selected = [item for item in cases if split == "all" or item["split"] == split]
    if not selected:
        raise PublicPairEvaluationError(f"所选 split={split} 没有 case")
    if max_cases:
        selected = selected[:max_cases]
    return selected


def write_command_log(path: Path, command: list[str], result: subprocess.CompletedProcess[str] | None, error: str = "") -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    rendered = shlex.join(command)
    if result is None:
        body = f"$ {rendered}\n\n[spawn_error]\n{error}\n"
    else:
        body = (
            f"$ {rendered}\n\n[exit_code={result.returncode}]\n\n"
            f"--- stdout ---\n{result.stdout}\n--- stderr ---\n{result.stderr}\n"
        )
    path.write_text(body, encoding="utf-8")


def template_kb_source(explicit_template_kb_id: str, environment: dict[str, str]) -> str:
    """Fail before a batch when the child runner cannot create a temporary KB.

    `run_claims_eval.py` writes its initial manifest before checking this
    variable. Without an early guard, a missing template configuration makes
    every case look like a mysterious `running`/UNEVALUABLE artifact. Do not
    echo the actual ID; it is configuration, not an experiment result.
    """
    if explicit_template_kb_id.strip():
        return "argument"
    if str(environment.get("WEKNORA_EXPERIMENT_TEMPLATE_KB", "")).strip():
        return "environment"
    raise PublicPairEvaluationError(
        "缺少模板 KB 配置：请在同一 shell 设置 WEKNORA_EXPERIMENT_TEMPLATE_KB，"
        "或传 --template-kb-id。为避免批量制造无效 artifact，未启动任何 case。",
    )


def run_service_preflight(output: Path, environment: dict[str, str]) -> dict[str, Any]:
    """Run the existing read-only service/database/migration check once."""
    command = [sys.executable, str(RUNNER), "--check", "--check-db"]
    log_path = output / "service_preflight.log"
    try:
        result = subprocess.run(command, cwd=ROOT, text=True, capture_output=True, check=False, env=environment)
    except OSError as exc:
        write_command_log(log_path, command, None, str(exc))
        raise PublicPairEvaluationError(f"无法启动 public-eval service preflight: {exc}") from exc
    write_command_log(log_path, command, result)
    if result.returncode != 0:
        raise PublicPairEvaluationError(
            "public-eval service preflight 失败；未启动任何 case。"
            f"请查看 {log_path}，并确认 dev app、WEKNORA_BASE_URL、WEKNORA_API_KEY、"
            "WEKNORA_DOCKER_BIN/数据库导出与 migrations 均就绪。",
        )
    return {
        "command": command,
        "exit_code": result.returncode,
        "log": str(log_path),
        "checked_at": utc_now(),
    }


def read_result_json(path: Path, issues: list[str], label: str) -> Any:
    try:
        return read_json(path)
    except PublicPairEvaluationError as exc:
        issues.append(f"{label}: {exc}")
        return None


def run_case(
    case: dict[str, Any],
    replicate: int,
    output: Path,
    env: dict[str, str],
    template_kb_id: str,
    detector_conflict_timeout_seconds: int,
) -> dict[str, Any]:
    detector_dir = output / "replicates" / str(case["id"]) / f"replicate-{replicate:02d}" / "detector"
    command = [
        sys.executable,
        str(RUNNER),
        "--scenario", str(case["scenario"]),
        "--variant", str(case["variant"]),
        "--output", str(detector_dir),
        "--conflict-timeout-seconds", str(detector_conflict_timeout_seconds),
    ]
    if template_kb_id:
        command.extend(["--template-kb-id", template_kb_id])
    log_path = detector_dir.parent / "detector_command.log"
    try:
        result = subprocess.run(
            command, cwd=ROOT, text=True, encoding="utf-8", errors="replace",
            capture_output=True, check=False, env=env,
        )
    except OSError as exc:
        write_command_log(log_path, command, None, str(exc))
        return result_row(
            case, replicate, detector_dir, command, None, None, [f"无法启动 detector: {exc}"],
            proposal_issues=["detector 未启动，proposal artifact 不可评估"],
        )
    write_command_log(log_path, command, result)

    issues: list[str] = []
    detector_manifest = read_result_json(detector_dir / "manifest.json", issues, "detector manifest")
    detector_metrics = read_result_json(detector_dir / "metrics.json", issues, "detector metrics")
    pairs = read_result_json(detector_dir / "conflict_document_pairs.json", issues, "conflict_document_pairs")
    status = ""
    if isinstance(detector_manifest, dict):
        status = str(detector_manifest.get("status", ""))
    else:
        issues.append("detector manifest 根节点不是对象")
    if not isinstance(detector_metrics, dict):
        issues.append("detector metrics 根节点不是对象")
    if not isinstance(pairs, list):
        issues.append("conflict_document_pairs 根节点不是数组")
    if result.returncode not in {0, 2}:
        issues.append(f"detector exit={result.returncode}，预期为 0 或 2（评测命中失败仍会返回 2）")
    if not status.startswith("completed"):
        issues.append(f"detector manifest status={status!r}，预期 completed*")

    observed: bool | None = None
    if isinstance(pairs, list):
        expected_pair = case["expected_pair"]
        observed = document_pair_match(pairs, str(expected_pair["left"]), str(expected_pair["right"]))

    winner_proposals: Any = None
    proposal_issues: list[str] = []
    if case["expected_winner_document"]:
        winner_proposals = read_result_json(
            detector_dir / "winner_proposals.json", proposal_issues, "winner_proposals",
        )
        if not isinstance(winner_proposals, list):
            proposal_issues.append("winner_proposals 根节点不是数组")
    return result_row(
        case, replicate, detector_dir, command, result, detector_metrics, issues,
        observed=observed,
        manifest_status=status,
        winner_proposals=winner_proposals,
        proposal_issues=proposal_issues,
    )


def result_row(
    case: dict[str, Any],
    replicate: int,
    detector_dir: Path,
    command: list[str],
    result: subprocess.CompletedProcess[str] | None,
    detector_metrics: Any,
    issues: list[str],
    *,
    observed: bool | None = None,
    manifest_status: str = "",
    winner_proposals: Any = None,
    proposal_issues: list[str] | None = None,
) -> dict[str, Any]:
    evaluable = not issues and isinstance(detector_metrics, dict) and observed is not None
    expected = bool(case["expected_conflict"])
    classification = "UNEVALUABLE"
    correct: bool | None = None
    if evaluable:
        correct = observed == expected
        if expected and observed:
            classification = "TP"
        elif expected and not observed:
            classification = "FN"
        elif not expected and observed:
            classification = "FP"
        else:
            classification = "TN"

    expected_winner = str(case.get("expected_winner_document", ""))
    expected_source_count = case.get("expected_winner_proposal_source_count")
    proposal_applicable = bool(expected_winner)
    proposal_issues = list(proposal_issues or [])
    proposal_evaluable = False
    proposal_correct: bool | None = None
    observed_winner_documents: list[str] = []
    observed_winner_source_counts: list[int | None] = []
    observed_winner_count: int | None = None
    if proposal_applicable:
        if not evaluable:
            proposal_issues.append("基础 detector artifact 不可评估，proposal 不计入指标")
        elif not isinstance(winner_proposals, list):
            proposal_issues.append("缺少可解析的 winner_proposals 数组")
        elif not all(isinstance(item, dict) for item in winner_proposals):
            proposal_issues.append("winner_proposals 包含非对象项")
        else:
            proposal_evaluable = True
            observed_winner_count = len(winner_proposals)
            observed_winner_documents = [str(item.get("winner_document", "")) for item in winner_proposals]
            observed_winner_source_counts = [as_int(item.get("source_count")) for item in winner_proposals]
            # For an advisory-proposal transfer case, an expected proposal is
            # correct only when the existing runner's pair/cluster/winner
            # assertions all passed as well as the exported winner/source count.
            # A technically complete but wrong topology is an incorrect result,
            # not an infrastructure-free success.
            proposal_correct = (
                manifest_status == "completed"
                and observed_winner_count == 1
                and observed_winner_documents == [expected_winner]
                and observed_winner_source_counts == [expected_source_count]
            )

    dead_letter_count = None
    claim_count = None
    conflict_count = None
    cascade: dict[str, Any] = {}
    if isinstance(detector_metrics, dict):
        dead_letter_count = as_int(detector_metrics.get("dead_letter_count"))
        claim_count = as_int(detector_metrics.get("claim_count_total"))
        conflict_count = as_int(detector_metrics.get("conflict_count_total"))
        raw_cascade = detector_metrics.get("cascade")
        if isinstance(raw_cascade, dict):
            cascade = raw_cascade
    return {
        "case_id": case["id"],
        "fact_family_id": case["fact_family_id"],
        "split": case["split"],
        "case_type": case["case_type"],
        "source_label": case["source_label"],
        "variant": case["variant"],
        "replicate": replicate,
        "expected_conflict": expected,
        "observed_conflict": observed,
        "classification": classification,
        "correct": correct,
        "detector_evaluable": evaluable,
        "expected_winner_document": expected_winner,
        "expected_winner_proposal_source_count": expected_source_count,
        "observed_winner_count": observed_winner_count,
        "observed_winner_documents": observed_winner_documents,
        "observed_winner_source_counts": observed_winner_source_counts,
        "proposal_applicable": proposal_applicable,
        "proposal_evaluable": proposal_evaluable,
        "proposal_correct": proposal_correct,
        "proposal_issues": proposal_issues,
        "detector_exit_code": result.returncode if result is not None else None,
        "detector_manifest_status": manifest_status,
        "detector_dir": str(detector_dir),
        "detector_command": shlex.join(command),
        "issues": issues,
        "dead_letter_count": dead_letter_count,
        "claim_count_total": claim_count,
        "conflict_count_total": conflict_count,
        "cascade": cascade,
    }

def confusion(rows: list[dict[str, Any]], *, strict: bool = False) -> dict[str, Any]:
    counts = Counter(str(row["classification"]) for row in rows)
    tp, tn, fp, fn = (counts["TP"], counts["TN"], counts["FP"], counts["FN"])
    evaluable = tp + tn + fp + fn
    total = len(rows)
    prefix = "fact_family" if strict else "execution"
    return {
        "unit": prefix,
        "observations": total,
        "evaluable_observations": evaluable,
        "unevaluable_observations": total - evaluable,
        "complete": evaluable == total,
        "expected_positive": tp + fn,
        "expected_negative": tn + fp,
        "true_positive": tp,
        "true_negative": tn,
        "false_positive": fp,
        "false_negative": fn,
        "precision": ratio(tp, tp + fp) if evaluable == total else None,
        "recall": ratio(tp, tp + fn) if evaluable == total else None,
        "accuracy": ratio(tp + tn, evaluable) if evaluable and evaluable == total else None,
        "conditional_precision": ratio(tp, tp + fp),
        "conditional_recall": ratio(tp, tp + fn),
        "conditional_accuracy": ratio(tp + tn, evaluable),
    }


def strict_fact_rows(results: list[dict[str, Any]], replicates: int) -> list[dict[str, Any]]:
    groups: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for row in results:
        groups[str(row["fact_family_id"])].append(row)
    output: list[dict[str, Any]] = []
    for family, rows in sorted(groups.items()):
        expected_values = {bool(row["expected_conflict"]) for row in rows}
        proposal_flags = {bool(row["proposal_applicable"]) for row in rows}
        if len(expected_values) != 1:
            raise PublicPairEvaluationError(f"fact family {family} 的 expected_conflict 不一致")
        if len(proposal_flags) != 1:
            raise PublicPairEvaluationError(f"fact family {family} 的 proposal 适用性不一致")
        expected = expected_values.pop()
        proposal_applicable = proposal_flags.pop()
        result: dict[str, Any] = {
            "fact_family_id": family,
            "case_id": rows[0]["case_id"],
            "split": rows[0]["split"],
            "case_type": rows[0]["case_type"],
            "source_label": rows[0]["source_label"],
            "expected_conflict": expected,
            "replicates_expected": replicates,
            "replicates_observed": len(rows),
            "replicate_classifications": [str(row["classification"]) for row in sorted(rows, key=lambda item: int(item["replicate"]))],
            "classification": "UNEVALUABLE",
            "strictly_correct": None,
            "proposal_applicable": proposal_applicable,
            "expected_winner_document": rows[0]["expected_winner_document"],
            "expected_winner_proposal_source_count": rows[0]["expected_winner_proposal_source_count"],
            "replicate_proposal_correct": [row["proposal_correct"] for row in sorted(rows, key=lambda item: int(item["replicate"]))],
            "proposal_evaluable": False,
            "proposal_strictly_correct": None,
        }
        detector_complete = len(rows) == replicates and all(bool(row["detector_evaluable"]) for row in rows)
        if detector_complete:
            observed_values = [bool(row["observed_conflict"]) for row in rows]
            if expected:
                # One miss in any independent run makes this family a strict FN.
                result["classification"] = "TP" if all(observed_values) else "FN"
            else:
                # One false alarm in any independent run makes this family a strict FP.
                result["classification"] = "TN" if not any(observed_values) else "FP"
            result["strictly_correct"] = result["classification"] in {"TP", "TN"}

        if proposal_applicable:
            proposal_complete = len(rows) == replicates and all(bool(row["proposal_evaluable"]) for row in rows)
            result["proposal_evaluable"] = proposal_complete
            if proposal_complete:
                result["proposal_strictly_correct"] = all(bool(row["proposal_correct"]) for row in rows)
        output.append(result)
    return output


CASCADE_KEYS = (
    "candidate_claim_pairs", "candidate_fallback_pairs", "candidate_after_dedupe", "candidates_submitted",
    "rule_no_conflict", "rule_direct_conflict", "rule_needs_llm", "llm_pair_count", "llm_batch_call_count",
    "llm_single_call_count", "llm_single_fallback_count", "llm_prompt_tokens", "llm_completion_tokens",
    "duration_ms", "final_conflict_count",
)


def aggregate_cascade(results: list[dict[str, Any]]) -> dict[str, Any]:
    """Aggregate the existing runner's nested C2 cascade artifact shape.

    ``run_claims_eval.py`` exports ``metrics.cascade`` as a summary object with
    its numeric fields under ``cascade.totals``. Public pair rows preserve that
    object verbatim for auditability. Accept a flat shape as a compatibility
    fallback, but prefer the nested totals; otherwise every public run would
    misleadingly report an all-zero operational aggregate despite valid
    detector artifacts.
    """
    totals = {key: 0 for key in CASCADE_KEYS}
    observations = 0
    nested_total_observations = 0
    flat_total_observations = 0
    for row in results:
        if not row["detector_evaluable"]:
            continue
        cascade = row.get("cascade")
        if not isinstance(cascade, dict):
            continue
        nested = cascade.get("totals")
        if isinstance(nested, dict):
            values = nested
            nested_total_observations += 1
        else:
            values = cascade
            flat_total_observations += 1
        observations += 1
        for key in CASCADE_KEYS:
            value = as_int(values.get(key))
            if value is not None:
                totals[key] += value
    return {
        "evaluable_execution_observations": observations,
        "nested_totals_observations": nested_total_observations,
        "flat_totals_compatibility_observations": flat_total_observations,
        "totals": totals,
        "note": "Aggregate operational shape only; this public transfer set is not a C2 cost-ablation experiment.",
    }


def aggregate_dead_letters(results: list[dict[str, Any]]) -> dict[str, Any]:
    values = [row["dead_letter_count"] for row in results if row["dead_letter_count"] is not None]
    unknown = len(results) - len(values)
    return {
        "observations": len(results),
        "known_observations": len(values),
        "unknown_observations": unknown,
        "total": sum(int(value) for value in values),
        "max": max((int(value) for value in values), default=None),
        "all_zero": bool(values) and unknown == 0 and all(int(value) == 0 for value in values),
    }


def aggregate_basic_counts(results: list[dict[str, Any]], field: str) -> dict[str, Any]:
    values = [row[field] for row in results if row.get(field) is not None]
    return {
        "known_observations": len(values),
        "unknown_observations": len(results) - len(values),
        "total": sum(int(value) for value in values),
        "min": min((int(value) for value in values), default=None),
        "max": max((int(value) for value in values), default=None),
        "mean": round(mean(int(value) for value in values), 6) if values else None,
    }


def aggregate_proposal_transfer(
    execution_rows: list[dict[str, Any]], fact_rows: list[dict[str, Any]],
) -> dict[str, Any]:
    """Summarize optional exact-winner checks without inventing abstention P/R.

    WikiFactDiff exposes a two-snapshot replacement setting. Its public proposal
    transfer subset therefore has expected proposals only; reporting a binary
    precision estimate there would falsely imply tested no-proposal conditions.
    We report exact proposal success (winner plus source-count) instead.
    """
    applicable_execution = [row for row in execution_rows if row["proposal_applicable"]]
    evaluable_execution = [row for row in applicable_execution if row["proposal_evaluable"]]
    correct_execution = [row for row in evaluable_execution if row["proposal_correct"]]
    applicable_facts = [row for row in fact_rows if row["proposal_applicable"]]
    evaluable_facts = [row for row in applicable_facts if row["proposal_evaluable"]]
    correct_facts = [row for row in evaluable_facts if row["proposal_strictly_correct"]]
    return {
        "definition": "Exact expected winner document and exact expected source count.",
        "execution_level": {
            "applicable_observations": len(applicable_execution),
            "evaluable_observations": len(evaluable_execution),
            "unevaluable_observations": len(applicable_execution) - len(evaluable_execution),
            "complete": len(applicable_execution) == len(evaluable_execution),
            "correct": len(correct_execution),
            "incorrect": len(evaluable_execution) - len(correct_execution),
            "success_rate": ratio(len(correct_execution), len(evaluable_execution))
            if len(applicable_execution) == len(evaluable_execution) else None,
            "conditional_success_rate": ratio(len(correct_execution), len(evaluable_execution)),
        },
        "fact_family_strict_all_replicates": {
            "applicable_observations": len(applicable_facts),
            "evaluable_observations": len(evaluable_facts),
            "unevaluable_observations": len(applicable_facts) - len(evaluable_facts),
            "complete": len(applicable_facts) == len(evaluable_facts),
            "correct": len(correct_facts),
            "incorrect": len(evaluable_facts) - len(correct_facts),
            "success_rate": ratio(len(correct_facts), len(evaluable_facts))
            if len(applicable_facts) == len(evaluable_facts) else None,
            "conditional_success_rate": ratio(len(correct_facts), len(evaluable_facts)),
        },
        "scope_note": (
            "Not a proposal precision, abstention, multi-authority, native-header, or real-document governance estimate."
        ),
    }


def write_report(
    path: Path,
    metadata: dict[str, Any],
    execution: dict[str, Any],
    strict: dict[str, Any],
    dead: dict[str, Any],
    proposals: dict[str, Any],
) -> None:
    text = f"""# Public pair-level conflict-transfer evaluation

- Manifest: `{metadata['manifest_path']}`
- Dataset task: `{metadata['task']}`
- Variant: `{metadata['variant']}`
- Selected split: `{metadata['split']}`
- Independent replicates requested: `{metadata['replicates']}`
- Source Git commit: `{metadata['git_commit']}`
- All required artifacts complete: `{metadata.get('artifact_complete', False)}`

## Execution-level result (not the primary replicate-level unit)

| Metric | Value |
| --- | ---: |
| Observations | {execution['observations']} |
| Evaluable / unevaluable | {execution['evaluable_observations']} / {execution['unevaluable_observations']} |
| TP / TN / FP / FN | {execution['true_positive']} / {execution['true_negative']} / {execution['false_positive']} / {execution['false_negative']} |
| Complete precision / recall / accuracy | {execution['precision']} / {execution['recall']} / {execution['accuracy']} |
| Conditional precision / recall / accuracy | {execution['conditional_precision']} / {execution['conditional_recall']} / {execution['conditional_accuracy']} |

## Primary fact-family strict-all-replicates result

A family is correct only if all requested independent replicates are evaluable
and correct. A missing artifact is never converted into a true negative.

| Metric | Value |
| --- | ---: |
| Fact families | {strict['observations']} |
| Evaluable / unevaluable | {strict['evaluable_observations']} / {strict['unevaluable_observations']} |
| TP / TN / FP / FN | {strict['true_positive']} / {strict['true_negative']} / {strict['false_positive']} / {strict['false_negative']} |
| Complete precision / recall / accuracy | {strict['precision']} / {strict['recall']} / {strict['accuracy']} |
| Conditional precision / recall / accuracy | {strict['conditional_precision']} / {strict['conditional_recall']} / {strict['conditional_accuracy']} |

## Optional public snapshot-proposal transfer

This section is populated only when a manifest supplies an exact expected C4.6
winner and source count. It reports success rather than precision because a
two-snapshot replacement corpus has no public no-proposal / multi-authority
negative cases.

| Metric | Execution | Fact-family strict-all-replicates |
| --- | ---: | ---: |
| Applicable cases | {proposals['execution_level']['applicable_observations']} | {proposals['fact_family_strict_all_replicates']['applicable_observations']} |
| Evaluable / unevaluable | {proposals['execution_level']['evaluable_observations']} / {proposals['execution_level']['unevaluable_observations']} | {proposals['fact_family_strict_all_replicates']['evaluable_observations']} / {proposals['fact_family_strict_all_replicates']['unevaluable_observations']} |
| Exact winner+source-count correct | {proposals['execution_level']['correct']} | {proposals['fact_family_strict_all_replicates']['correct']} |
| Complete / conditional success rate | {proposals['execution_level']['success_rate']} / {proposals['execution_level']['conditional_success_rate']} | {proposals['fact_family_strict_all_replicates']['success_rate']} / {proposals['fact_family_strict_all_replicates']['conditional_success_rate']} |

## Integrity

- Dead-letter observations / unknown: `{dead['known_observations']} / {dead['unknown_observations']}`
- Dead-letter total / max / all-zero: `{dead['total']} / {dead['max']} / {dead['all_zero']}`
- Complete headline metrics are null when any execution/family is unevaluable.

## Scope boundary

This is a public **claim--evidence or temporal-fact pair transfer** evaluation.
It does not establish real enterprise-document accuracy, human-review accuracy,
native document-header extraction accuracy, end-to-end RAG answer correctness,
or provider-RNG-seed-controlled causality. See the source manifest for the
upstream dataset-specific transformation and license notes.
"""
    path.write_text(text, encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run a prepared public pair conflict-transfer manifest against a live WeKnora service.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--manifest", required=True, help="pair_eval_manifest.json emitted by a public dataset adapter")
    parser.add_argument("--split", choices=sorted(VALID_SPLITS), default="holdout", help="Which predeclared split to run")
    parser.add_argument("--replicates", type=int, default=1, help="Independent fresh-KB executions per fact family")
    parser.add_argument("--max-cases", type=int, default=0, help="Run only the deterministic first N selected cases; development smoke only")
    parser.add_argument("--output-dir", default="", help="Output root; defaults to experiments/comparisons/<timestamp>-public-pair-<manifest>")
    parser.add_argument("--template-kb-id", default="", help="Optional override; otherwise run_claims_eval reads WEKNORA_EXPERIMENT_TEMPLATE_KB")
    parser.add_argument("--detector-conflict-timeout-seconds", type=int, default=60, help="Per-case positive-pair wait before completed detector artifacts are scored")
    parser.add_argument("--dry-run", action="store_true", help="Validate manifest and print planned cases without contacting services")
    parser.add_argument("--overwrite", action="store_true", help="Explicitly replace a non-empty output directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if args.replicates < 1 or args.replicates > 10:
            raise PublicPairEvaluationError("--replicates 必须在 1–10 之间")
        if args.max_cases < 0:
            raise PublicPairEvaluationError("--max-cases 不能为负数")
        if args.detector_conflict_timeout_seconds < 1:
            raise PublicPairEvaluationError("--detector-conflict-timeout-seconds 必须为正整数")
        manifest_path = Path(args.manifest).expanduser().resolve()
        manifest = load_manifest(manifest_path)
        cases = select_cases(manifest["cases"], args.split, args.max_cases)
        plan = {
            "manifest_path": str(manifest_path),
            "manifest_sha256": sha256_file(manifest_path),
            "name": manifest["name"],
            "task": manifest["task"],
            "variant": manifest["variant"],
            "split": args.split,
            "replicates": args.replicates,
            "case_count": len(cases),
            "fact_family_count": len({str(case['fact_family_id']) for case in cases}),
            "max_cases": args.max_cases,
            "detector_conflict_timeout_seconds": args.detector_conflict_timeout_seconds,
            "cases": [
                {"id": case["id"], "fact_family_id": case["fact_family_id"], "expected_conflict": case["expected_conflict"]}
                for case in cases
            ],
            "note": "Validation/plan only; no HTTP/model/Asynq/Docker/PostgreSQL operation was performed.",
        }
        if args.dry_run:
            print(json.dumps(plan, ensure_ascii=False, indent=2))
            return 0

        environment = utf8_child_environment()
        template_source = template_kb_source(args.template_kb_id, environment)
        default_dir = ROOT / "experiments/comparisons" / f"{utc_stamp()}-public-pair-{manifest['name']}"
        output = remove_tree_if_requested(Path(args.output_dir) if args.output_dir else default_dir, args.overwrite)
        run_manifest = {
            **{key: value for key, value in plan.items() if key != "note"},
            "created_at": utc_now(),
            "git_commit": safe_git_sha(),
            "source_manifest": manifest["source_manifest"],
            "template_kb_configuration_source": template_source,
            "status": "running",
            "note": "Live public pair evaluation. Each case is a fresh temporary KB created through run_claims_eval.py.",
        }
        json_dump(output / "manifest.json", run_manifest)
        try:
            run_manifest["service_preflight"] = run_service_preflight(output, environment)
        except PublicPairEvaluationError as exc:
            run_manifest["status"] = "failed_preflight"
            run_manifest["finished_at"] = utc_now()
            run_manifest["error"] = str(exc)
            json_dump(output / "manifest.json", run_manifest)
            raise
        json_dump(output / "manifest.json", run_manifest)
        results: list[dict[str, Any]] = []
        for case in cases:
            for replicate in range(1, args.replicates + 1):
                result = run_case(
                    case, replicate, output, environment, args.template_kb_id,
                    args.detector_conflict_timeout_seconds,
                )
                results.append(result)
                print(
                    f"[public-pair] {case['id']} r{replicate}: "
                    f"{result['classification']}"
                    + (f" ({'; '.join(result['issues'])})" if result["issues"] else ""),
                )

        strict_rows = strict_fact_rows(results, args.replicates)
        execution_metrics = confusion(results)
        strict_metrics = confusion(strict_rows, strict=True)
        dead_letters = aggregate_dead_letters(results)
        proposal_transfer = aggregate_proposal_transfer(results, strict_rows)
        metrics = {
            "schema_version": 1,
            "task": manifest["task"],
            "variant": manifest["variant"],
            "split": args.split,
            "replicates_requested": args.replicates,
            "case_definitions": len(cases),
            "fact_family_definitions": len(strict_rows),
            "execution_level": execution_metrics,
            "fact_family_strict_all_replicates": strict_metrics,
            "proposal_transfer": proposal_transfer,
            "dead_letter_count": dead_letters,
            "claim_count": aggregate_basic_counts(results, "claim_count_total"),
            "raw_conflict_count": aggregate_basic_counts(results, "conflict_count_total"),
            "cascade": aggregate_cascade(results),
            "seed_control": "none; independent fresh-KB service executions only",
            "scope_note": (
                "Public pair transfer metric only; not real enterprise-document, human-review, native-header, "
                "end-to-end RAG, or provider-seed-controlled evidence."
            ),
        }
        json_dump(output / "execution_results.json", results)
        json_dump(output / "fact_family_results.json", strict_rows)
        write_csv(
            output / "execution_results.csv",
            [
                {
                    **{
                        key: value for key, value in row.items()
                        if key not in {"cascade", "issues", "proposal_issues", "observed_winner_documents", "observed_winner_source_counts"}
                    },
                    "issues": " | ".join(str(value) for value in row["issues"]),
                    "proposal_issues": " | ".join(str(value) for value in row["proposal_issues"]),
                    "observed_winner_documents": ";".join(row["observed_winner_documents"]),
                    "observed_winner_source_counts": ";".join(
                        "" if value is None else str(value) for value in row["observed_winner_source_counts"]
                    ),
                    "cascade": json.dumps(row["cascade"], ensure_ascii=False, sort_keys=True),
                }
                for row in results
            ],
            [
                "case_id", "fact_family_id", "split", "case_type", "source_label", "variant", "replicate",
                "expected_conflict", "observed_conflict", "classification", "correct", "detector_evaluable",
                "expected_winner_document", "expected_winner_proposal_source_count", "observed_winner_count",
                "observed_winner_documents", "observed_winner_source_counts", "proposal_applicable",
                "proposal_evaluable", "proposal_correct", "detector_exit_code", "detector_manifest_status",
                "dead_letter_count", "claim_count_total", "conflict_count_total", "detector_dir", "detector_command",
                "issues", "proposal_issues", "cascade",
            ],
        )
        write_csv(
            output / "fact_family_results.csv",
            [
                {
                    **row,
                    "replicate_classifications": ";".join(row["replicate_classifications"]),
                    "replicate_proposal_correct": ";".join(
                        "" if value is None else str(value).lower() for value in row["replicate_proposal_correct"]
                    ),
                }
                for row in strict_rows
            ],
            [
                "fact_family_id", "case_id", "split", "case_type", "source_label", "expected_conflict",
                "replicates_expected", "replicates_observed", "replicate_classifications", "classification",
                "strictly_correct", "proposal_applicable", "expected_winner_document",
                "expected_winner_proposal_source_count", "replicate_proposal_correct", "proposal_evaluable",
                "proposal_strictly_correct",
            ],
        )
        run_complete = (
            bool(execution_metrics["complete"])
            and bool(proposal_transfer["execution_level"]["complete"])
            and bool(proposal_transfer["fact_family_strict_all_replicates"]["complete"])
        )
        metrics["complete"] = run_complete
        run_manifest["artifact_complete"] = run_complete
        json_dump(output / "metrics.json", metrics)
        write_report(
            output / "report.md", run_manifest, execution_metrics, strict_metrics, dead_letters, proposal_transfer,
        )
        run_manifest["status"] = "completed" if run_complete else "completed_incomplete_artifacts"
        run_manifest["finished_at"] = utc_now()
        json_dump(output / "manifest.json", run_manifest)
        print(f"Public pair evaluation complete: {output}")
        print(
            "  fact-family strict-all-replicates P/R/accuracy: "
            f"{strict_metrics['precision']} / {strict_metrics['recall']} / {strict_metrics['accuracy']}",
        )
        print(
            "  evaluable executions / facts: "
            f"{execution_metrics['evaluable_observations']}/{execution_metrics['observations']} / "
            f"{strict_metrics['evaluable_observations']}/{strict_metrics['observations']}",
        )
        print(
            "  proposal exact-success (strict facts): "
            f"{proposal_transfer['fact_family_strict_all_replicates']['success_rate']}",
        )
        print(
            "  dead letters total / all-zero: "
            f"{dead_letters['total']} / {dead_letters['all_zero']}",
        )
        return 0 if run_complete else 2
    except PublicBenchmarkError as exc:
        print(f"[public-pair] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
