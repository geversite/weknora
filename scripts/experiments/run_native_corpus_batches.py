#!/usr/bin/env python3
"""Run stratified native-DocReader batches against a live WeKnora server.

Each batch uses its own temporary KB so pairwise detection stays bounded.
This is unlabeled observational ingest, not accuracy. Resume skips batches
whose detector manifest status starts with completed.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any

from public_benchmark_common import ROOT, force_utf8_stdio, json_dump, utf8_child_environment


RUNNER = ROOT / "scripts/experiments/run_claims_eval.py"
SUMMARIZER = ROOT / "scripts/experiments/summarize_native_corpus_eval.py"


class BatchError(RuntimeError):
    """A native batch plan cannot be executed."""


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise BatchError(f"missing JSON: {path}") from exc
    except json.JSONDecodeError as exc:
        raise BatchError(f"invalid JSON: {path}: {exc}") from exc


def reset_incomplete_run_dir(run_dir: Path) -> None:
    if run_dir.exists():
        shutil.rmtree(run_dir)
    run_dir.mkdir(parents=True, exist_ok=True)


def batch_status(run_dir: Path) -> str:
    manifest_path = run_dir / "manifest.json"
    if not manifest_path.is_file():
        return "missing"
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return "malformed"
    if not isinstance(manifest, dict):
        return "malformed"
    return str(manifest.get("status") or "unknown")


def run_batch(args: argparse.Namespace, scenario: Path, output: Path) -> int:
    command = [
        sys.executable, "-X", "utf8", str(RUNNER),
        "--scenario", str(scenario),
        "--variant", args.variant,
        "--output", str(output),
        "--file-upload-timeout-seconds", str(args.file_upload_timeout_seconds),
        "--timeout-seconds", str(args.timeout_seconds),
        "--claim-timeout-seconds", str(args.claim_timeout_seconds),
        "--conflict-timeout-seconds", str(args.conflict_timeout_seconds),
    ]
    if args.overwrite:
        command.append("--overwrite")
    result = subprocess.run(
        command,
        cwd=ROOT,
        env=utf8_child_environment(),
        check=False,
    )
    return int(result.returncode)


def summarize_batch(run_dir: Path) -> dict[str, Any]:
    result = subprocess.run(
        [sys.executable, str(SUMMARIZER), "--run-dir", str(run_dir), "--apply"],
        cwd=ROOT,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if result.returncode != 0:
        raise BatchError(result.stderr.strip() or f"summarizer failed for {run_dir}")
    summary_path = run_dir / "native_summary.json"
    if summary_path.is_file():
        payload = json.loads(summary_path.read_text(encoding="utf-8"))
        if isinstance(payload, dict):
            return payload
    return json.loads(result.stdout)


def aggregate(rows: list[dict[str, Any]]) -> dict[str, Any]:
    files = claims = conflicts = facts = proposals = dead = parsed = with_claims = 0
    tokens = duration = batch_calls = 0
    exact = fuzzy = chunk = 0
    for row in rows:
        obs = row.get("observational_metrics") or {}
        files += int(obs.get("files_selected") or row.get("document_count") or 0)
        parsed += int(obs.get("files_parsed") or 0)
        with_claims += int(obs.get("files_with_claims") or 0)
        claims += int(row.get("claim_count_total") or 0)
        conflicts += int(row.get("conflict_count_total") or 0)
        facts += int(row.get("disputed_fact_count") or 0)
        proposals += int(row.get("winner_proposal_count") or 0)
        dead += int(row.get("dead_letter_count") or 0)
        cascade = row.get("cascade") or {}
        tokens += int(cascade.get("token_total") or 0)
        duration += int(cascade.get("duration_ms") or 0)
        batch_calls += int(cascade.get("llm_batch_call_count") or 0)
        kinds = obs.get("anchor_kind_clusters") or {}
        exact += int(kinds.get("claim_key") or 0)
        fuzzy += int(kinds.get("fuzzy_slot") or 0)
        chunk += int(kinds.get("chunk_pair") or 0)
    return {
        "batches_aggregated": len(rows),
        "files_selected": files,
        "files_parsed": parsed,
        "files_with_claims": with_claims,
        "claim_count_total": claims,
        "conflict_count_total": conflicts,
        "disputed_fact_count_sum": facts,
        "global_proposals": proposals,
        "dead_letter_count": dead,
        "exact_key_clusters_sum": exact,
        "fuzzy_slot_clusters_sum": fuzzy,
        "chunk_pair_clusters_sum": chunk,
        "llm_batch_call_count": batch_calls,
        "token_total": tokens,
        "duration_ms": duration,
        "note": (
            "Sums across independent temporary KBs. Disputed-fact totals are "
            "not one global clustering. Not labeled accuracy."
        ),
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run native DocReader batches from a prepared batch_manifest.json.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--plan-dir", required=True, help="Directory containing batch_manifest.json")
    parser.add_argument("--output-dir", required=True, help="Private run directory outside Git")
    parser.add_argument("--variant", default="c2-batch")
    parser.add_argument("--max-batches", type=int, default=0, help="0 means all planned batches")
    parser.add_argument("--resume", action="store_true")
    parser.add_argument("--overwrite", action="store_true")
    parser.add_argument("--fail-fast", type=int, default=2)
    parser.add_argument("--file-upload-timeout-seconds", type=int, default=300)
    parser.add_argument("--timeout-seconds", type=int, default=600)
    parser.add_argument("--claim-timeout-seconds", type=int, default=600)
    parser.add_argument("--conflict-timeout-seconds", type=int, default=900)
    return parser.parse_args()


def main() -> int:
    force_utf8_stdio()
    args = parse_args()
    try:
        if not str(os.environ.get("WEKNORA_EXPERIMENT_TEMPLATE_KB", "")).isascii():
            raise BatchError("WEKNORA_EXPERIMENT_TEMPLATE_KB must be an ASCII UUID")
        plan_dir = Path(args.plan_dir).expanduser().resolve()
        manifest = read_json(plan_dir / "batch_manifest.json")
        if not isinstance(manifest, dict) or not isinstance(manifest.get("batches"), list):
            raise BatchError("batch_manifest.json missing batches[]")
        batches = list(manifest["batches"])
        if args.max_batches > 0:
            batches = batches[: args.max_batches]
        output = Path(args.output_dir).expanduser().resolve()
        output.mkdir(parents=True, exist_ok=True)
        summaries: list[dict[str, Any]] = []
        failures = 0
        for entry in batches:
            batch_id = str(entry.get("batch_id") or "")
            scenario = Path(str(entry.get("scenario_path") or ""))
            if not scenario.is_file():
                raise BatchError(f"{batch_id} scenario missing: {scenario}")
            run_dir = output / batch_id
            status = batch_status(run_dir)
            if args.resume and status.startswith("completed"):
                print(f"[native-batch] {batch_id}: reuse {status}")
                summaries.append(summarize_batch(run_dir))
                continue
            if run_dir.exists() and any(run_dir.iterdir()):
                if args.resume or args.overwrite:
                    print(f"[native-batch] {batch_id}: reset incomplete status={status}")
                    reset_incomplete_run_dir(run_dir)
                else:
                    raise BatchError(
                        f"{batch_id} output exists ({run_dir}); rerun with RESUME=1 to retry incomplete batches"
                    )
            print(f"[native-batch] {batch_id}: start {scenario}")
            code = run_batch(args, scenario, run_dir)
            status = batch_status(run_dir)
            if code not in {0, 2} or not status.startswith("completed"):
                failures += 1
                print(f"[native-batch] {batch_id}: FAILED status={status} exit={code}")
                if args.fail_fast and failures >= args.fail_fast:
                    raise BatchError(f"fail-fast after {failures} batch failures")
                continue
            summaries.append(summarize_batch(run_dir))
            print(f"[native-batch] {batch_id}: {status}")
        totals = aggregate(summaries)
        json_dump(output / "batch_results.json", {
            "plan_dir": str(plan_dir),
            "batches_planned": len(batches),
            "batches_summarized": len(summaries),
            "totals": totals,
        })
        print("Native batched ingest complete:", output)
        print(
            "  files parsed/with-claims:",
            f"{totals['files_parsed']}/{totals['files_selected']}",
            totals["files_with_claims"],
        )
        print("  claims / raw / facts:", totals["claim_count_total"], totals["conflict_count_total"], totals["disputed_fact_count_sum"])
        print("  proposals / dead letters:", totals["global_proposals"], totals["dead_letter_count"])
        return 0 if failures == 0 else 2
    except BatchError as exc:
        print(f"[native-batches] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
