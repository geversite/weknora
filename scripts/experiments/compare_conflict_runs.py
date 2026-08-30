#!/usr/bin/env python3
"""Create a reproducible C1/C2 ablation comparison from explicit run artifacts.

This tool is deliberately offline: it reads the JSON artifacts produced by
run_claims_eval.py and never contacts WeKnora, PostgreSQL, Docker, or a model
provider.  Explicit --run arguments avoid accidentally comparing a user's
unrelated "latest" runs.

Example:

  python3 scripts/experiments/compare_conflict_runs.py \
    --run experiments/runs/<v1-run> \
    --run experiments/runs/<c1-run> \
    --run experiments/runs/<c2-rules-run> \
    --run experiments/runs/<c2-batch-run>

The output directory contains comparison.json and comparison.md.  It is a
cost/integrity summary, not a paper-quality causal measurement: production
LLM extraction and adjudication can vary between independent runs.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_OUTPUT_ROOT = ROOT / "experiments" / "comparisons"


class ComparisonError(RuntimeError):
    """An input artifact is missing or structurally unsuitable for comparison."""


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ComparisonError(f"缺少实验产物: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ComparisonError(f"JSON 无法解析: {path}: {exc}") from exc


def as_int(value: Any) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def as_float(value: Any) -> float | None:
    if value is None or value == "":
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def relative_path(path: Path) -> str:
    try:
        return str(path.relative_to(ROOT))
    except ValueError:
        return str(path)


def markdown_cell(value: Any) -> str:
    return str(value if value not in (None, "") else "-").replace("|", "\\|").replace("\n", " ")


def pct(value: float | None) -> str:
    return "-" if value is None else f"{value:.3f}"


def percent_delta(current: int, baseline: int) -> float | None:
    if baseline <= 0:
        return None
    return (current - baseline) / baseline


def signed_percent(value: float | None) -> str:
    if value is None:
        return "-"
    return f"{value * 100:+.1f}%"


def run_integrity(manifest: dict[str, Any], metrics: dict[str, Any], cascade: dict[str, Any]) -> tuple[bool, list[str]]:
    """Separate detector correctness/integrity from the volatile extractor gate."""
    problems: list[str] = []
    status = str(manifest.get("status", ""))
    if status != "completed":
        problems.append(f"manifest={status or 'missing'}")
    if metrics.get("dead_letter_count") not in (0, "0", None):
        problems.append(f"dead_letters={metrics.get('dead_letter_count')}")
    missing = metrics.get("missing_expected_conflict_document_pairs") or []
    if missing:
        problems.append(f"missing_expected={len(missing)}")
    forbidden = metrics.get("observed_forbidden_conflict_pairs") or []
    if forbidden:
        problems.append(f"forbidden_raw={len(forbidden)}")
    statuses = cascade.get("statuses") or {}
    if as_int(statuses.get("failed")) > 0:
        problems.append(f"detector_failed={statuses.get('failed')}")
    return not problems, problems


def extractor_gate(metrics: dict[str, Any]) -> str:
    result = metrics.get("evaluator")
    if not isinstance(result, dict):
        return "skipped"
    code = result.get("exit_code")
    if code == 0:
        return "pass"
    if code is None:
        return "unknown"
    return f"fail(exit={code})"


def load_run(raw_path: str) -> dict[str, Any]:
    run_dir = Path(raw_path).expanduser().resolve()
    if not run_dir.is_dir():
        raise ComparisonError(f"run 目录不存在: {run_dir}")

    manifest = load_json(run_dir / "manifest.json")
    metrics = load_json(run_dir / "metrics.json")
    cascade = load_json(run_dir / "cascade_metrics.json")
    if not isinstance(manifest, dict) or not isinstance(metrics, dict) or not isinstance(cascade, dict):
        raise ComparisonError(f"run 产物根节点必须为对象: {run_dir}")

    totals = cascade.get("totals") or {}
    if not isinstance(totals, dict):
        raise ComparisonError(f"cascade_metrics.json totals 必须为对象: {run_dir}")
    integrity_ok, integrity_problems = run_integrity(manifest, metrics, cascade)
    evaluator = metrics.get("evaluator") if isinstance(metrics.get("evaluator"), dict) else {}
    prompt_tokens = as_int(totals.get("llm_prompt_tokens"))
    completion_tokens = as_int(totals.get("llm_completion_tokens"))
    batch_calls = as_int(totals.get("llm_batch_call_count"))
    single_calls = as_int(totals.get("llm_single_call_count"))

    detector_versions: list[str] = []
    detection_runs_path = run_dir / "conflict_detection_runs.json"
    if detection_runs_path.is_file():
        detection_runs = load_json(detection_runs_path)
        if isinstance(detection_runs, list):
            detector_versions = sorted({
                str(row.get("detector_version", ""))
                for row in detection_runs
                if isinstance(row, dict) and row.get("detector_version")
            })

    return {
        "run_dir": relative_path(run_dir),
        "run_id": str(manifest.get("run_id") or run_dir.name),
        "variant": str(manifest.get("variant", "unknown")),
        "scenario": str(manifest.get("scenario_name", "unknown")),
        "git_commit": str(manifest.get("git_commit", "")),
        "manifest_status": str(manifest.get("status", "")),
        "detector_versions": detector_versions,
        "detector_integrity": {
            "pass": integrity_ok,
            "problems": integrity_problems,
        },
        "extractor_gate": extractor_gate(metrics),
        "extractor_metrics": {
            "scope": str(metrics.get("evaluator_scope", "skipped")),
            "precision": as_float(evaluator.get("combined_precision")),
            "recall": as_float(evaluator.get("combined_recall")),
        },
        "claims": as_int(metrics.get("claim_count_total")),
        "raw_conflicts": as_int(metrics.get("conflict_count_total")),
        "dead_letters": as_int(metrics.get("dead_letter_count")),
        "missing_expected_pairs": len(metrics.get("missing_expected_conflict_document_pairs") or []),
        "forbidden_raw_conflicts": len(metrics.get("observed_forbidden_conflict_pairs") or []),
        "cascade": {
            "candidate_claim_pairs": as_int(totals.get("candidate_claim_pairs")),
            "candidate_fallback_pairs": as_int(totals.get("candidate_fallback_pairs")),
            "candidate_after_dedupe": as_int(totals.get("candidate_after_dedupe")),
            "candidates_submitted": as_int(totals.get("candidates_submitted")),
            "rule_no_conflict": as_int(totals.get("rule_no_conflict")),
            "rule_direct_conflict": as_int(totals.get("rule_direct_conflict")),
            "rule_needs_llm": as_int(totals.get("rule_needs_llm")),
            "llm_pair_count": as_int(totals.get("llm_pair_count")),
            "llm_batch_call_count": batch_calls,
            "llm_single_call_count": single_calls,
            "llm_single_fallback_count": as_int(totals.get("llm_single_fallback_count")),
            "llm_call_count": batch_calls + single_calls,
            "llm_prompt_tokens": prompt_tokens,
            "llm_completion_tokens": completion_tokens,
            "llm_total_tokens": prompt_tokens + completion_tokens,
            "final_conflict_count": as_int(totals.get("final_conflict_count")),
            "duration_ms": as_int(totals.get("duration_ms")),
        },
    }


def select_baseline(rows: list[dict[str, Any]], selector: str) -> dict[str, Any] | None:
    for row in rows:
        if row["run_id"] == selector:
            return row
    for row in rows:
        if row["variant"] == selector:
            return row
    return None


def add_relative_metrics(rows: list[dict[str, Any]], baseline: dict[str, Any] | None) -> None:
    for row in rows:
        cascade = row["cascade"]
        relative: dict[str, Any] = {}
        if baseline is not None:
            base = baseline["cascade"]
            relative = {
                "baseline_run_id": baseline["run_id"],
                "llm_calls_delta": percent_delta(cascade["llm_call_count"], base["llm_call_count"]),
                "tokens_delta": percent_delta(cascade["llm_total_tokens"], base["llm_total_tokens"]),
                "duration_delta": percent_delta(cascade["duration_ms"], base["duration_ms"]),
            }
        row["relative_to_baseline"] = relative


def write_markdown(path: Path, rows: list[dict[str, Any]], baseline: dict[str, Any] | None) -> None:
    lines = [
        "# Conflict C1/C2 ablation comparison",
        "",
        "## Scope",
        "",
        f"- Runs: `{len(rows)}`",
        f"- Baseline: `{baseline['run_id']}` ({baseline['variant']})" if baseline else "- Baseline: not found; relative deltas omitted.",
        "- Detector integrity is separate from the claim-extraction evaluator gate.",
        "- Independent production-model runs are stochastic. These are reproducible artifact summaries, not controlled causal estimates.",
        "",
        "## Integrity and extraction",
        "",
        "| variant | run | detector | integrity | extractor gate | P / R | claims | raw conflicts | missing expected | forbidden raw | dead letters |",
        "|---|---|---|---|---|---|---:|---:|---:|---:|---:|",
    ]
    for row in rows:
        integrity = row["detector_integrity"]
        integrity_label = "PASS" if integrity["pass"] else "FAIL: " + ", ".join(integrity["problems"])
        extractor = row["extractor_metrics"]
        pr = "skipped" if extractor["scope"] == "skipped" else f"{pct(extractor['precision'])} / {pct(extractor['recall'])}"
        detector = ", ".join(row["detector_versions"]) or "-"
        lines.append(
            "| " + " | ".join([
                markdown_cell(row["variant"]),
                markdown_cell(row["run_id"]),
                markdown_cell(detector),
                markdown_cell(integrity_label),
                markdown_cell(row["extractor_gate"]),
                markdown_cell(pr),
                str(row["claims"]),
                str(row["raw_conflicts"]),
                str(row["missing_expected_pairs"]),
                str(row["forbidden_raw_conflicts"]),
                str(row["dead_letters"]),
            ]) + " |"
        )

    lines += [
        "",
        "## Cascade cost",
        "",
        "| variant | candidates (claim / fallback / total) | rules (no / direct / LLM) | LLM pairs | calls (batch + single) | batch→single fallback | prompt / completion / total tokens | duration | calls vs baseline | tokens vs baseline | time vs baseline |",
        "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for row in rows:
        cascade = row["cascade"]
        relative = row.get("relative_to_baseline") or {}
        lines.append(
            "| " + " | ".join([
                markdown_cell(row["variant"]),
                f"{cascade['candidate_claim_pairs']} / {cascade['candidate_fallback_pairs']} / {cascade['candidate_after_dedupe']}",
                f"{cascade['rule_no_conflict']} / {cascade['rule_direct_conflict']} / {cascade['rule_needs_llm']}",
                str(cascade["llm_pair_count"]),
                f"{cascade['llm_batch_call_count']} + {cascade['llm_single_call_count']} = {cascade['llm_call_count']}",
                str(cascade["llm_single_fallback_count"]),
                f"{cascade['llm_prompt_tokens']} / {cascade['llm_completion_tokens']} / {cascade['llm_total_tokens']}",
                f"{cascade['duration_ms']} ms",
                signed_percent(relative.get("llm_calls_delta")),
                signed_percent(relative.get("tokens_delta")),
                signed_percent(relative.get("duration_delta")),
            ]) + " |"
        )

    lines += [
        "",
        "## Interpretation guardrails",
        "",
        "- A nonzero `forbidden raw` is a document-pair precision regression signal in the closed synthetic scenario; raw chunk-pair duplicates are not independent errors before C4 clustering.",
        "- `extractor gate` comes from the legacy claim extractor P/R threshold and does not by itself measure conflict-adjudication precision or recall.",
        "- A nonzero `batch→single fallback` preserves correctness but reduces the realized batch cost advantage; inspect the matching run report and logs.",
        "",
    ]
    path.write_text("\n".join(lines), encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Compare explicit C1/C2 experiment run artifacts offline.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument(
        "--run", action="append", required=True,
        help="Experiment run directory; repeat once per run to compare.",
    )
    parser.add_argument(
        "--baseline", default="c1",
        help="Baseline variant or exact run_id for relative cost deltas; use an unmatched value to omit deltas.",
    )
    parser.add_argument(
        "--output-dir", default="",
        help="Output directory; defaults to experiments/comparisons/<UTC timestamp>-conflict-ablation.",
    )
    parser.add_argument("--overwrite", action="store_true", help="Allow a non-empty output directory.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        rows = [load_run(raw) for raw in args.run]
        run_ids = [row["run_id"] for row in rows]
        if len(set(run_ids)) != len(run_ids):
            raise ComparisonError("--run 中存在重复 run_id")
        baseline = select_baseline(rows, args.baseline)
        add_relative_metrics(rows, baseline)

        if args.output_dir:
            output_dir = Path(args.output_dir).expanduser().resolve()
        else:
            stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
            output_dir = DEFAULT_OUTPUT_ROOT / f"{stamp}-conflict-ablation"
        if output_dir.exists() and any(output_dir.iterdir()) and not args.overwrite:
            raise ComparisonError(f"输出目录已存在且非空: {output_dir}（需要 --overwrite）")
        output_dir.mkdir(parents=True, exist_ok=True)

        payload = {
            "schema_version": 1,
            "generated_at": utc_now(),
            "baseline_selector": args.baseline,
            "baseline_run_id": baseline["run_id"] if baseline else None,
            "runs": rows,
        }
        (output_dir / "comparison.json").write_text(
            json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8",
        )
        write_markdown(output_dir / "comparison.md", rows, baseline)
        print(f"比较完成: {output_dir}")
        print(f"  runs: {len(rows)}")
        print(f"  baseline: {baseline['run_id']} ({baseline['variant']})" if baseline else "  baseline: not found")
        return 0
    except ComparisonError as exc:
        print(f"[comparison] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
