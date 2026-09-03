#!/usr/bin/env python3
"""Run reproducible C4.6/C4.7/C4.8 lifecycle replicates against a live app.

Every mutation goes through the public HTTP API via the existing experiment
runners. This driver only orchestrates those scripts and reads their JSON
artifacts; it never connects to PostgreSQL directly. Each replicate gets fresh
temporary KBs, so it can safely exercise adopt -> reopen cycles without
changing a template KB or a previous replicate.

"Replicate" is intentional terminology: WeKnora's configured model/provider
API does not expose a portable deterministic RNG seed. The runner records
independent executions, not falsely labelled seeded trials.

Typical usage:

  make experiment-c49 REPLICATES=3

For a custom, human-reviewed corpus, copy/edit the matrix JSON and point every
case at a normal run_claims_eval scenario with explicit expected winner/no-
winner assertions:

  python3 scripts/experiments/run_winner_lifecycle_eval.py \
    --matrix /path/to/my_matrix.json --replicates 3
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
from pathlib import Path
from statistics import mean
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_MATRIX = ROOT / "scripts/experiments/scenarios/c49_winner_lifecycle_matrix.json"
DEFAULT_OUTPUT_ROOT = ROOT / "experiments/comparisons"
RUNNER = ROOT / "scripts/experiments/run_claims_eval.py"
ADOPTION_RUNNER = ROOT / "scripts/experiments/run_winner_adoption.py"
REOPEN_RUNNER = ROOT / "scripts/experiments/run_winner_reopen.py"
VALID_OUTCOMES = {"adopt_reopen", "no_proposal"}
VALID_VARIANTS = {"v1", "c1", "c2-rules", "c2-batch"}


class LifecycleEvaluationError(RuntimeError):
    """A matrix/configuration failure before a case can be evaluated."""


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


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
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2, default=str) + "\n", encoding="utf-8")


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise LifecycleEvaluationError(f"找不到文件: {path}") from exc
    except json.JSONDecodeError as exc:
        raise LifecycleEvaluationError(f"JSON 无法解析: {path}: {exc}") from exc


def as_int(value: Any) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def db_false(value: Any) -> bool:
    return str(value).strip().lower() in {"", "0", "f", "false"}


def read_matrix(path: Path) -> dict[str, Any]:
    raw = load_json(path)
    if not isinstance(raw, dict) or not raw.get("name"):
        raise LifecycleEvaluationError("matrix 根节点必须是包含 name 的对象")
    variant = str(raw.get("variant", "c2-rules"))
    if variant not in VALID_VARIANTS:
        raise LifecycleEvaluationError(f"matrix variant 非法: {variant}")
    cases = raw.get("cases")
    if not isinstance(cases, list) or not cases:
        raise LifecycleEvaluationError("matrix cases 必须是非空数组")
    seen: set[str] = set()
    normalized: list[dict[str, Any]] = []
    for raw_case in cases:
        if not isinstance(raw_case, dict):
            raise LifecycleEvaluationError(f"matrix case 必须是对象: {raw_case}")
        case_id = str(raw_case.get("id", "")).strip()
        outcome = str(raw_case.get("expected_outcome", "")).strip()
        scenario = str(raw_case.get("scenario", "")).strip()
        if not case_id or case_id in seen:
            raise LifecycleEvaluationError(f"matrix case id 缺失或重复: {case_id!r}")
        if outcome not in VALID_OUTCOMES:
            raise LifecycleEvaluationError(f"matrix case {case_id} expected_outcome 非法: {outcome}")
        scenario_path = (ROOT / scenario).resolve()
        if not scenario or not scenario_path.is_file():
            raise LifecycleEvaluationError(f"matrix case {case_id} scenario 不存在: {scenario}")
        winner = str(raw_case.get("expected_winner_document", "")).strip()
        cycles = raw_case.get("adoption_cycles", 0)
        if outcome == "adopt_reopen":
            if not winner:
                raise LifecycleEvaluationError(f"matrix positive case {case_id} 缺少 expected_winner_document")
            if isinstance(cycles, bool) or not isinstance(cycles, int) or cycles < 1 or cycles > 3:
                raise LifecycleEvaluationError(f"matrix positive case {case_id} adoption_cycles 必须为 1–3")
        else:
            if winner or cycles not in {0, None}:
                raise LifecycleEvaluationError(f"matrix no_proposal case {case_id} 不得设置 winner/adoption_cycles")
            cycles = 0
        normalized.append({
            "id": case_id,
            "scenario": str(scenario_path),
            "scenario_display": scenario,
            "expected_outcome": outcome,
            "expected_winner_document": winner,
            "adoption_cycles": cycles,
            "variant": str(raw_case.get("variant", variant)),
        })
        if normalized[-1]["variant"] not in VALID_VARIANTS:
            raise LifecycleEvaluationError(f"matrix case {case_id} variant 非法: {normalized[-1]['variant']}")
        seen.add(case_id)
    return {
        "schema_version": raw.get("schema_version", 1),
        "name": str(raw["name"]),
        "description": str(raw.get("description", "")),
        "variant": variant,
        "cases": normalized,
    }


def command_log(path: Path, command: list[str], result: subprocess.CompletedProcess[str]) -> None:
    rendered = shlex.join(command)
    text = f"$ {rendered}\n\n[exit_code={result.returncode}]\n\n--- stdout ---\n{result.stdout}\n--- stderr ---\n{result.stderr}\n"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def invoke(label: str, command: list[str], log_path: Path, env: dict[str, str]) -> dict[str, Any]:
    try:
        result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, env=env, check=False)
    except OSError as exc:
        log_path.parent.mkdir(parents=True, exist_ok=True)
        log_path.write_text(f"$ {shlex.join(command)}\n\n[spawn_error]\n{exc}\n", encoding="utf-8")
        return {"label": label, "command": command, "exit_code": None, "spawn_error": str(exc)}
    command_log(log_path, command, result)
    return {"label": label, "command": command, "exit_code": result.returncode, "log": str(log_path)}


def read_artifact(path: Path, issues: list[str], label: str) -> dict[str, Any] | None:
    try:
        data = load_json(path)
    except LifecycleEvaluationError as exc:
        issues.append(f"{label}: {exc}")
        return None
    if not isinstance(data, dict):
        issues.append(f"{label}: JSON 根节点不是对象")
        return None
    return data


def http_409(artifact: dict[str, Any] | None) -> bool:
    if not artifact:
        return False
    try:
        return as_int(artifact.get("expected_http_status")) == 409 and "HTTP 409:" in str(artifact.get("rejection", ""))
    except AttributeError:
        return False


def status_all(rows: Any, status: str, *, adoption_id: str | None = None) -> bool:
    if not isinstance(rows, list) or not rows:
        return False
    for row in rows:
        if not isinstance(row, dict) or str(row.get("status", "")) != status:
            return False
        if not db_false(row.get("auto_resolved")):
            return False
        if adoption_id is not None and str(row.get("winner_adoption_id", "")) != adoption_id:
            return False
    return True


def chunks_all_enabled(rows: Any) -> bool:
    return isinstance(rows, list) and bool(rows) and all(
        isinstance(row, dict) and not db_false(row.get("is_enabled")) for row in rows
    )


def validate_detector(
    detector_dir: Path,
    case: dict[str, Any],
    detector_step: dict[str, Any],
    issues: list[str],
) -> tuple[dict[str, Any] | None, dict[str, Any] | None]:
    manifest = read_artifact(detector_dir / "manifest.json", issues, "detector manifest")
    metrics = read_artifact(detector_dir / "metrics.json", issues, "detector metrics")
    if detector_step.get("exit_code") != 0:
        issues.append(f"detector exit={detector_step.get('exit_code')}, expected 0")
    if not manifest or manifest.get("status") != "completed":
        issues.append(f"detector manifest status={manifest.get('status') if manifest else None}, expected completed")
    if not metrics:
        return manifest, metrics
    if metrics.get("missing_expected_conflict_document_pairs"):
        issues.append("detector missed expected conflict document pair(s)")
    if metrics.get("observed_forbidden_conflict_pairs"):
        issues.append("detector observed forbidden conflict pair(s)")
    if metrics.get("disputed_fact_count_matches") is not True or metrics.get("disputed_fact_anchor_kinds_match") is not True:
        issues.append("detector disputed-fact count/anchor assertion failed")
    dead_letter_count = as_int(metrics.get("dead_letter_count"))
    if dead_letter_count != 0:
        issues.append(f"detector dead_letter_count={dead_letter_count}, expected 0")
    observed = as_int(metrics.get("observed_disputed_fact_winner_count"))
    expected = 1 if case["expected_outcome"] == "adopt_reopen" else 0
    if observed != expected or metrics.get("disputed_fact_winner_count_matches") is not True:
        issues.append(f"winner proposal count expected={expected}, observed={observed}")
    winners = metrics.get("winner_proposals", [])
    if not isinstance(winners, list):
        issues.append("winner_proposals is not an array")
    elif expected:
        if len(winners) != 1 or str(winners[0].get("winner_document", "")) != case["expected_winner_document"]:
            issues.append(f"winner document expected={case['expected_winner_document']}, observed={winners}")
    elif winners:
        issues.append(f"no-proposal case unexpectedly produced winner(s): {winners}")
    return manifest, metrics


def validate_adoption(artifact: dict[str, Any] | None, issues: list[str], cycle: int) -> str:
    prefix = f"adoption cycle {cycle}"
    if not artifact:
        return ""
    stale = artifact.get("stale_guard", {})
    if not isinstance(stale, dict) or not stale.get("attempted") or "HTTP 409:" not in str(stale.get("rejection", "")) or \
            not stale.get("members_unchanged") or not stale.get("chunks_unchanged"):
        issues.append(prefix + " stale guard failed")
    result = artifact.get("api_result", {})
    durable = artifact.get("durable_adoption", {})
    fact = artifact.get("disputed_fact_after", {})
    adoption_id = str(result.get("winner_adoption_id", "")) if isinstance(result, dict) else ""
    if not adoption_id or result.get("resolution") != "resolved_global_winner":
        issues.append(prefix + " missing durable global-winner result")
        return ""
    if not isinstance(durable, dict) or durable.get("id") != adoption_id or durable.get("status") != "adopted":
        issues.append(prefix + " durable adoption record mismatch")
    if not isinstance(fact, dict) or fact.get("status") != "resolved" or as_int(fact.get("pending_conflict_count")) != 0 or \
            fact.get("active_winner_adoption_id") != adoption_id:
        issues.append(prefix + " aggregate did not converge to active resolved adoption")
    if not status_all(artifact.get("members_after"), "resolved_global_winner", adoption_id=adoption_id):
        issues.append(prefix + " raw member global-winner state mismatch")
    return adoption_id


def validate_reopen(artifact: dict[str, Any] | None, expected_adoption_id: str, issues: list[str], cycle: int) -> None:
    prefix = f"reopen cycle {cycle}"
    if not artifact:
        return
    stale = artifact.get("stale_guard", {})
    if not isinstance(stale, dict) or not stale.get("attempted") or "HTTP 409:" not in str(stale.get("rejection", "")) or \
            not stale.get("members_unchanged") or not stale.get("chunks_unchanged") or not stale.get("adoption_unchanged"):
        issues.append(prefix + " stale guard failed")
    result = artifact.get("api_result", {})
    fact = artifact.get("disputed_fact_after", {})
    durable = artifact.get("durable_adoption_after", {})
    if not isinstance(result, dict) or result.get("winner_adoption_id") != expected_adoption_id or \
            result.get("reopen_version") != "c4-winner-reopen-v1" or as_int(result.get("reopened_conflict_count")) < 1:
        issues.append(prefix + " API result mismatch")
    if not status_all(artifact.get("members_after"), "pending", adoption_id=""):
        issues.append(prefix + " raw members did not all return to pending")
    if not chunks_all_enabled(artifact.get("chunks_after")):
        issues.append(prefix + " member chunks did not all return enabled")
    if not isinstance(fact, dict) or fact.get("status") != "pending" or as_int(fact.get("pending_conflict_count")) < 1 or \
            str(fact.get("active_winner_adoption_id", "")):
        issues.append(prefix + " aggregate did not return to pending/no-active-adoption")
    if not isinstance(durable, dict) or durable.get("id") != expected_adoption_id or durable.get("status") != "revoked":
        issues.append(prefix + " durable adoption did not become revoked")


def validate_no_proposal_action(artifact: dict[str, Any] | None, issues: list[str], label: str) -> None:
    if not http_409(artifact):
        issues.append(label + " expected HTTP 409 rejection")
        return
    fact = artifact.get("disputed_fact_after", {})
    if not isinstance(fact, dict) or fact.get("status") != "pending" or str(fact.get("active_winner_adoption_id", "")):
        issues.append(label + " changed pending/no-active fact state")
    rows = artifact.get("members_before_after", [])
    if not status_all(rows, "pending", adoption_id=""):
        issues.append(label + " changed raw member state")
    if not chunks_all_enabled(artifact.get("chunks_before_after", [])):
        issues.append(label + " changed/enabled-state mismatch for chunks")


def execute_case(
    matrix_id: str,
    output_dir: Path,
    case: dict[str, Any],
    replicate: int,
    env: dict[str, str],
) -> dict[str, Any]:
    case_dir = output_dir / "replicates" / case["id"] / f"replicate-{replicate:02d}"
    detector_dir = case_dir / "detector"
    case_dir.mkdir(parents=True, exist_ok=True)
    issues: list[str] = []
    steps: list[dict[str, Any]] = []
    run_id = f"{matrix_id}-{case['id']}-r{replicate:02d}"
    detector_command = [
        sys.executable, str(RUNNER), "--scenario", case["scenario"], "--variant", case["variant"],
        "--run-id", run_id, "--output", str(detector_dir),
    ]
    steps.append(invoke("detector", detector_command, case_dir / "detector_output.txt", env))
    manifest, metrics = validate_detector(detector_dir, case, steps[-1], issues)

    action_artifacts: list[dict[str, Any]] = []
    if metrics is not None and steps[-1].get("exit_code") == 0:
        if case["expected_outcome"] == "adopt_reopen":
            for cycle in range(1, int(case["adoption_cycles"]) + 1):
                cycle_issue_start = len(issues)
                adoption_path = detector_dir / f"c49_adoption_cycle_{cycle}.json"
                adoption_command = [
                    sys.executable, str(ADOPTION_RUNNER), "--run-dir", str(detector_dir),
                    "--output", str(adoption_path),
                    "--note", f"C4.9 {case['id']} replicate {replicate} adoption cycle {cycle}",
                ]
                adoption_step = invoke(
                    f"adoption-{cycle}", adoption_command, case_dir / f"adoption_cycle_{cycle}.txt", env,
                )
                steps.append(adoption_step)
                adoption_artifact = read_artifact(adoption_path, issues, f"adoption cycle {cycle} artifact")
                if adoption_step.get("exit_code") != 0:
                    issues.append(f"adoption cycle {cycle} exit={adoption_step.get('exit_code')}, expected 0")
                adoption_id = validate_adoption(adoption_artifact, issues, cycle)

                reopen_path = detector_dir / f"c49_reopen_cycle_{cycle}.json"
                reopen_command = [
                    sys.executable, str(REOPEN_RUNNER), "--run-dir", str(detector_dir),
                    "--output", str(reopen_path),
                    "--note", f"C4.9 {case['id']} replicate {replicate} reopen cycle {cycle}",
                ]
                reopen_step = invoke(
                    f"reopen-{cycle}", reopen_command, case_dir / f"reopen_cycle_{cycle}.txt", env,
                )
                steps.append(reopen_step)
                reopen_artifact = read_artifact(reopen_path, issues, f"reopen cycle {cycle} artifact")
                if reopen_step.get("exit_code") != 0:
                    issues.append(f"reopen cycle {cycle} exit={reopen_step.get('exit_code')}, expected 0")
                validate_reopen(reopen_artifact, adoption_id, issues, cycle)
                action_artifacts.append({
                    "cycle": cycle,
                    "adoption": str(adoption_path),
                    "reopen": str(reopen_path),
                    "winner_adoption_id": adoption_id,
                    "passed": len(issues) == cycle_issue_start,
                })
        else:
            adoption_path = detector_dir / "c49_adoption_negative.json"
            adoption_command = [
                sys.executable, str(ADOPTION_RUNNER), "--run-dir", str(detector_dir), "--expect-no-proposal",
                "--output", str(adoption_path),
                "--note", f"C4.9 {case['id']} replicate {replicate} expected no proposal",
            ]
            adoption_step = invoke("adoption-negative", adoption_command, case_dir / "adoption_negative.txt", env)
            steps.append(adoption_step)
            adoption_artifact = read_artifact(adoption_path, issues, "no-proposal adoption artifact")
            if adoption_step.get("exit_code") != 0:
                issues.append(f"no-proposal adoption exit={adoption_step.get('exit_code')}, expected 0")
            validate_no_proposal_action(adoption_artifact, issues, "no-proposal adoption")

            reopen_path = detector_dir / "c49_reopen_negative.json"
            reopen_command = [
                sys.executable, str(REOPEN_RUNNER), "--run-dir", str(detector_dir),
                "--expect-no-active-adoption", "--output", str(reopen_path),
                "--note", f"C4.9 {case['id']} replicate {replicate} expected no active adoption",
            ]
            reopen_step = invoke("reopen-negative", reopen_command, case_dir / "reopen_negative.txt", env)
            steps.append(reopen_step)
            reopen_artifact = read_artifact(reopen_path, issues, "no-active-adoption reopen artifact")
            if reopen_step.get("exit_code") != 0:
                issues.append(f"no-active-adoption reopen exit={reopen_step.get('exit_code')}, expected 0")
            validate_no_proposal_action(reopen_artifact, issues, "no-active-adoption reopen")
            action_artifacts.append({"adoption_negative": str(adoption_path), "reopen_negative": str(reopen_path)})
    else:
        issues.append("action steps skipped because detector did not produce a passing artifact")

    winners = metrics.get("winner_proposals", []) if isinstance(metrics, dict) else []
    raw_conflicts = as_int(metrics.get("conflict_count_total")) if isinstance(metrics, dict) else 0
    clusters = as_int(metrics.get("observed_disputed_fact_count")) if isinstance(metrics, dict) else 0
    record = {
        "case_id": case["id"],
        "replicate": replicate,
        "expected_outcome": case["expected_outcome"],
        "expected_winner_document": case["expected_winner_document"],
        "adoption_cycles_expected": case["adoption_cycles"],
        "scenario": case["scenario_display"],
        "variant": case["variant"],
        "case_dir": str(case_dir),
        "detector_dir": str(detector_dir),
        "detector_manifest_status": manifest.get("status") if isinstance(manifest, dict) else "",
        "detector_exit_code": steps[0].get("exit_code"),
        "raw_conflict_count": raw_conflicts,
        "disputed_fact_count": clusters,
        "dead_letter_count": as_int(metrics.get("dead_letter_count")) if isinstance(metrics, dict) else 0,
        "winner_proposals": winners if isinstance(winners, list) else [],
        "action_artifacts": action_artifacts,
        "steps": steps,
        "issues": issues,
        "passed": not issues,
    }
    json_dump(case_dir / "case_result.json", record)
    return record


def ratio(numerator: int, denominator: int) -> float | None:
    return round(numerator / denominator, 6) if denominator else None


def summarize(records: list[dict[str, Any]], matrix: dict[str, Any], replicates: int) -> dict[str, Any]:
    expected_positive = [item for item in records if item["expected_outcome"] == "adopt_reopen"]
    expected_negative = [item for item in records if item["expected_outcome"] == "no_proposal"]
    tp = fp = fn = tn = 0
    cycle_expected = cycle_passed = 0
    raw_counts: list[int] = []
    dead_letter_counts: list[int] = []
    for item in records:
        winners = item.get("winner_proposals", [])
        predicted = bool(winners)
        correct_winner = predicted and len(winners) == 1 and \
            str(winners[0].get("winner_document", "")) == item["expected_winner_document"]
        if item["expected_outcome"] == "adopt_reopen":
            if correct_winner:
                tp += 1
            else:
                fn += 1
                if predicted:
                    fp += 1
            expected = int(item["adoption_cycles_expected"])
            cycle_expected += expected
            cycle_passed += sum(
                1 for artifact in item.get("action_artifacts", [])
                if artifact.get("passed")
            )
        elif predicted:
            fp += 1
        else:
            tn += 1
        if item.get("raw_conflict_count"):
            raw_counts.append(int(item["raw_conflict_count"]))
        dead_letter_counts.append(as_int(item.get("dead_letter_count")))
    return {
        "matrix_name": matrix["name"],
        "replicates_requested": replicates,
        "case_definitions": len(matrix["cases"]),
        "case_executions": len(records),
        "passed_case_executions": sum(1 for item in records if item.get("passed")),
        "failed_case_executions": sum(1 for item in records if not item.get("passed")),
        "case_pass_rate": ratio(sum(1 for item in records if item.get("passed")), len(records)),
        "proposal_policy_matrix": {
            "expected_positive": len(expected_positive),
            "expected_no_proposal": len(expected_negative),
            "true_positive": tp,
            "true_negative": tn,
            "false_positive": fp,
            "false_negative": fn,
            "precision": ratio(tp, tp + fp),
            "recall": ratio(tp, tp + fn),
            "note": "Controlled scenario policy metric; not a real-corpus or human-review accuracy estimate.",
        },
        "lifecycle_cycles": {
            "expected": cycle_expected,
            "fully_passing_case_cycles": cycle_passed,
            "pass_rate": ratio(cycle_passed, cycle_expected),
        },
        "raw_conflict_count": {
            "observations": len(raw_counts),
            "min": min(raw_counts) if raw_counts else None,
            "max": max(raw_counts) if raw_counts else None,
            "mean": round(mean(raw_counts), 6) if raw_counts else None,
            "note": "Raw chunk-pair count may vary between independent executions; fact-level assertions are the primary unit.",
        },
        "dead_letter_count": {
            "observations": len(dead_letter_counts),
            "total": sum(dead_letter_counts),
            "max": max(dead_letter_counts) if dead_letter_counts else None,
            "all_zero": all(value == 0 for value in dead_letter_counts),
        },
        "seed_control": "none; independent replicates only",
    }


def write_summary_markdown(path: Path, matrix_id: str, matrix: dict[str, Any], summary: dict[str, Any], records: list[dict[str, Any]]) -> None:
    lines = [
        "# C4.9 winner lifecycle replicate matrix", "",
        f"- matrix run: `{matrix_id}`",
        f"- matrix: `{matrix['name']}`",
        f"- independent replicates: `{summary['replicates_requested']}` (provider RNG seed is not controlled)",
        f"- pass rate: `{summary['passed_case_executions']}/{summary['case_executions']}`", "",
        "## Controlled policy metrics", "",
        "```json",
        json.dumps(summary["proposal_policy_matrix"], ensure_ascii=False, indent=2),
        "```", "",
        "## Lifecycle / integrity totals", "", "```json",
        json.dumps({
            "lifecycle_cycles": summary["lifecycle_cycles"],
            "raw_conflict_count": summary["raw_conflict_count"],
            "dead_letter_count": summary["dead_letter_count"],
        }, ensure_ascii=False, indent=2),
        "```", "",
        "## Per-case evidence", "",
        "| case | replicate | expected | winner(s) | raw | clusters | result |", "|---|---:|---|---|---:|---:|---|",
    ]
    for item in records:
        winners = ", ".join(str(row.get("winner_document", "")) for row in item.get("winner_proposals", [])) or "—"
        result = "✅" if item.get("passed") else "❌ " + "; ".join(item.get("issues", [])[:2])
        lines.append(
            f"| {item['case_id']} | {item['replicate']} | {item['expected_outcome']} | {winners} | "
            f"{item['raw_conflict_count']} | {item['disputed_fact_count']} | {result} |"
        )
    lines += [
        "", "## Interpretation boundary", "",
        "This matrix uses real HTTP API, Asynq, PostgreSQL and configured models, but its expected labels are a controlled policy corpus. "
        "It measures reproducible integration/policy behavior, not real-document generalization or human accuracy. "
        "`winner_lifecycle_review.csv` is intentionally blank in its reviewer columns for a separate reviewer to assess custom real-corpus cases.",
        "",
    ]
    path.write_text("\n".join(lines), encoding="utf-8")


def write_review_csv(path: Path, records: list[dict[str, Any]]) -> None:
    fields = [
        "case_id", "replicate", "scenario", "expected_outcome", "expected_winner_document",
        "observed_winner_documents", "raw_conflict_count", "disputed_fact_count", "automation_pass",
        "artifact_dir", "reviewer_1_label", "reviewer_1_note", "reviewer_2_label", "reviewer_2_note",
        "adjudicated_label", "adjudicated_note",
    ]
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for item in records:
            winners = ";".join(str(row.get("winner_document", "")) for row in item.get("winner_proposals", []))
            writer.writerow({
                "case_id": item["case_id"],
                "replicate": item["replicate"],
                "scenario": item["scenario"],
                "expected_outcome": item["expected_outcome"],
                "expected_winner_document": item["expected_winner_document"],
                "observed_winner_documents": winners,
                "raw_conflict_count": item["raw_conflict_count"],
                "disputed_fact_count": item["disputed_fact_count"],
                "automation_pass": "yes" if item.get("passed") else "no",
                "artifact_dir": item["detector_dir"],
                "reviewer_1_label": "",
                "reviewer_1_note": "",
                "reviewer_2_label": "",
                "reviewer_2_note": "",
                "adjudicated_label": "",
                "adjudicated_note": "",
            })


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run controlled C4.6/C4.7/C4.8 lifecycle replicates against a live WeKnora app.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--matrix", default=str(DEFAULT_MATRIX), help="C4.9 matrix JSON")
    parser.add_argument("--replicates", type=int, default=3, help="Independent executions per case; no provider RNG seed claim")
    parser.add_argument("--output-dir", default="", help="Output root; default is experiments/comparisons/<timestamp>-<matrix>")
    parser.add_argument("--run-id", default="", help="Stable matrix run identifier")
    parser.add_argument("--overwrite", action="store_true", help="Allow a non-empty output directory")
    parser.add_argument("--dry-run", action="store_true", help="Validate the matrix and write plan only")
    parser.add_argument("--base-url", default=os.environ.get("WEKNORA_BASE_URL", ""), help="Override WEKNORA_BASE_URL for child runners")
    parser.add_argument("--template-kb-id", default=os.environ.get("WEKNORA_EXPERIMENT_TEMPLATE_KB", ""), help="Override template KB for detector child runs")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if args.replicates < 1 or args.replicates > 20:
            raise LifecycleEvaluationError("--replicates 必须在 1–20 之间")
        matrix_path = Path(args.matrix).expanduser().resolve()
        matrix = read_matrix(matrix_path)
        matrix_id = args.run_id.strip() or (
            dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + matrix["name"] + "-" + safe_git_sha()[:8]
        )
        output_dir = Path(args.output_dir).expanduser().resolve() if args.output_dir else DEFAULT_OUTPUT_ROOT / matrix_id
        if output_dir.exists() and any(output_dir.iterdir()) and not args.overwrite:
            raise LifecycleEvaluationError(f"输出目录已存在且非空: {output_dir}（需要 --overwrite）")
        output_dir.mkdir(parents=True, exist_ok=True)

        plan = {
            "schema_version": 1,
            "matrix_id": matrix_id,
            "matrix_path": str(matrix_path),
            "matrix": matrix,
            "replicates": args.replicates,
            "seed_control": "none; independent replicates only",
            "output_dir": str(output_dir),
            "started_at": utc_now(),
            "git_commit": safe_git_sha(),
        }
        json_dump(output_dir / "matrix_manifest.json", plan)
        if args.dry_run:
            plan["status"] = "dry_run"
            plan["finished_at"] = utc_now()
            json_dump(output_dir / "matrix_manifest.json", plan)
            print(json.dumps(plan, ensure_ascii=False, indent=2))
            return 0

        env = os.environ.copy()
        if args.base_url:
            env["WEKNORA_BASE_URL"] = args.base_url
        if args.template_kb_id:
            env["WEKNORA_EXPERIMENT_TEMPLATE_KB"] = args.template_kb_id
        check_step = invoke(
            "environment-check", [sys.executable, str(RUNNER), "--check", "--check-db"],
            output_dir / "environment_check.txt", env,
        )
        if check_step.get("exit_code") != 0:
            plan.update({"status": "environment_check_failed", "finished_at": utc_now(), "environment_check": check_step})
            json_dump(output_dir / "matrix_manifest.json", plan)
            print(f"[c4.9] environment check failed; evidence: {output_dir / 'environment_check.txt'}", file=sys.stderr)
            return 1

        records: list[dict[str, Any]] = []
        for replicate in range(1, args.replicates + 1):
            for case in matrix["cases"]:
                print(f"[c4.9] replicate {replicate}/{args.replicates}: {case['id']}")
                record = execute_case(matrix_id, output_dir, case, replicate, env)
                records.append(record)
                print(
                    f"[c4.9] {'PASS' if record['passed'] else 'FAIL'} {case['id']} r{replicate}: "
                    f"raw={record['raw_conflict_count']} clusters={record['disputed_fact_count']}"
                )
                json_dump(output_dir / "matrix_results.json", records)

        summary = summarize(records, matrix, args.replicates)
        summary["environment_check"] = check_step
        summary["finished_at"] = utc_now()
        json_dump(output_dir / "matrix_summary.json", summary)
        write_summary_markdown(output_dir / "matrix_summary.md", matrix_id, matrix, summary, records)
        write_review_csv(output_dir / "winner_lifecycle_review.csv", records)
        plan.update({"status": "completed" if summary["failed_case_executions"] == 0 else "completed_with_failures", "finished_at": utc_now()})
        json_dump(output_dir / "matrix_manifest.json", plan)

        print(f"C4.9 lifecycle matrix complete: {output_dir}")
        print(f"  case executions: {summary['passed_case_executions']}/{summary['case_executions']}")
        print(f"  controlled proposal precision/recall: {summary['proposal_policy_matrix']['precision']} / {summary['proposal_policy_matrix']['recall']}")
        print(f"  lifecycle cycles: {summary['lifecycle_cycles']['fully_passing_case_cycles']}/{summary['lifecycle_cycles']['expected']}")
        return 0 if summary["failed_case_executions"] == 0 else 2
    except LifecycleEvaluationError as exc:
        print(f"[c4.9] FAILED: {exc}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("[c4.9] interrupted", file=sys.stderr)
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
