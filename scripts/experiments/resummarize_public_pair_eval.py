#!/usr/bin/env python3
"""Read-only/post-hoc re-summarize a completed public pair-evaluation run.

This utility exists for reporting-code repairs only. It reads immutable per-case
artifacts already exported by ``run_public_pair_eval.py`` and recomputes summary
files; it never creates a KB, calls HTTP, contacts a model, starts Asynq work,
or accesses PostgreSQL/Docker.

By default it is a dry run. ``--apply`` preserves the old summary files under
``resummarization_backups/`` before writing corrected metrics/report/fact-family
summaries. It must never be used to alter raw detector artifacts or labels.
"""

from __future__ import annotations

import argparse
import json
import shutil
import sys
from pathlib import Path
from typing import Any

from public_benchmark_common import (
    ROOT,
    PublicBenchmarkError,
    json_dump,
    safe_git_sha,
    sha256_file,
    utc_now,
    utc_stamp,
    write_csv,
)
from run_public_pair_eval import (
    PublicPairEvaluationError,
    aggregate_basic_counts,
    aggregate_cascade,
    aggregate_dead_letters,
    aggregate_proposal_transfer,
    as_int,
    confusion,
    read_json,
    strict_fact_rows,
    write_report,
)


SUMMARY_VERSION = "public-pair-resummarize-v1"


class ResummarizeError(PublicBenchmarkError):
    """A completed public-pair run lacks immutable evidence required to rescore."""


def require_object(path: Path, label: str) -> dict[str, Any]:
    data = read_json(path)
    if not isinstance(data, dict):
        raise ResummarizeError(f"{label} 根节点必须是对象: {path}")
    return data


def require_rows(path: Path, label: str) -> list[dict[str, Any]]:
    data = read_json(path)
    if not isinstance(data, list) or not all(isinstance(item, dict) for item in data):
        raise ResummarizeError(f"{label} 必须是对象数组: {path}")
    if not data:
        raise ResummarizeError(f"{label} 为空，不能重新汇总: {path}")
    return list(data)


def build_summary(run_manifest: dict[str, Any], results: list[dict[str, Any]]) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    replicates = as_int(run_manifest.get("replicates"))
    if replicates is None or replicates < 1:
        raise ResummarizeError("run manifest 缺少合法的 replicates")
    strict_rows = strict_fact_rows(results, replicates)
    execution_metrics = confusion(results)
    strict_metrics = confusion(strict_rows, strict=True)
    proposal_transfer = aggregate_proposal_transfer(results, strict_rows)
    complete = (
        bool(execution_metrics["complete"])
        and bool(proposal_transfer["execution_level"]["complete"])
        and bool(proposal_transfer["fact_family_strict_all_replicates"]["complete"])
    )
    metrics = {
        "schema_version": 1,
        "summary_version": SUMMARY_VERSION,
        "task": str(run_manifest.get("task", "")),
        "variant": str(run_manifest.get("variant", "")),
        "split": str(run_manifest.get("split", "")),
        "replicates_requested": replicates,
        "case_definitions": as_int(run_manifest.get("case_count")) or len(strict_rows),
        "fact_family_definitions": len(strict_rows),
        "execution_level": execution_metrics,
        "fact_family_strict_all_replicates": strict_metrics,
        "proposal_transfer": proposal_transfer,
        "dead_letter_count": aggregate_dead_letters(results),
        "claim_count": aggregate_basic_counts(results, "claim_count_total"),
        "raw_conflict_count": aggregate_basic_counts(results, "conflict_count_total"),
        "cascade": aggregate_cascade(results),
        "complete": complete,
        "seed_control": "none; independent fresh-KB service executions only",
        "scope_note": (
            "Public pair transfer metric only; not real enterprise-document, human-review, native-header, "
            "end-to-end RAG, or provider-seed-controlled evidence."
        ),
    }
    return metrics, strict_rows


def write_fact_family_csv(path: Path, strict_rows: list[dict[str, Any]]) -> None:
    write_csv(
        path,
        [
            {
                **row,
                "replicate_classifications": ";".join(str(item) for item in row["replicate_classifications"]),
                "replicate_proposal_correct": ";".join(
                    "" if item is None else str(item).lower()
                    for item in row["replicate_proposal_correct"]
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


def backup_existing(run_dir: Path, stamp: str) -> Path:
    backup = run_dir / "resummarization_backups" / stamp
    backup.mkdir(parents=True, exist_ok=False)
    for name in ("metrics.json", "report.md", "fact_family_results.json", "fact_family_results.csv", "manifest.json"):
        source = run_dir / name
        if source.is_file():
            shutil.copy2(source, backup / name)
    return backup


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Recompute public pair-evaluation summaries from existing immutable detector artifacts.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--run-dir", required=True, help="Completed run_public_pair_eval output directory")
    parser.add_argument("--apply", action="store_true", help="Write corrected summary files after preserving originals under resummarization_backups/")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        run_dir = Path(args.run_dir).expanduser().resolve()
        if not run_dir.is_dir():
            raise ResummarizeError(f"run-dir 不存在或不是目录: {run_dir}")
        run_manifest = require_object(run_dir / "manifest.json", "run manifest")
        results = require_rows(run_dir / "execution_results.json", "execution_results")
        previous_metrics_path = run_dir / "metrics.json"
        previous_metrics = require_object(previous_metrics_path, "当前 metrics")
        metrics, strict_rows = build_summary(run_manifest, results)
        plan = {
            "summary_version": SUMMARY_VERSION,
            "run_dir": str(run_dir),
            "run_status": str(run_manifest.get("status", "")),
            "execution_result_sha256": sha256_file(run_dir / "execution_results.json"),
            "previous_metrics_sha256": sha256_file(previous_metrics_path),
            "previous_cascade": previous_metrics.get("cascade"),
            "recomputed_cascade": metrics.get("cascade"),
            "recomputed_complete": metrics["complete"],
            "fact_family_count": len(strict_rows),
            "note": "Read-only plan; no HTTP/model/Asynq/Docker/PostgreSQL operation was performed.",
        }
        if not args.apply:
            print(json.dumps(plan, ensure_ascii=False, indent=2))
            return 0

        stamp = utc_stamp()
        backup = backup_existing(run_dir, stamp)
        metrics["posthoc_resummarized_at"] = utc_now()
        metrics["posthoc_resummarizer_commit"] = safe_git_sha()
        metrics["source_execution_results_sha256"] = plan["execution_result_sha256"]
        json_dump(run_dir / "metrics.json", metrics)
        json_dump(run_dir / "fact_family_results.json", strict_rows)
        write_fact_family_csv(run_dir / "fact_family_results.csv", strict_rows)

        report_metadata = dict(run_manifest)
        report_metadata["artifact_complete"] = metrics["complete"]
        write_report(
            run_dir / "report.md",
            report_metadata,
            metrics["execution_level"],
            metrics["fact_family_strict_all_replicates"],
            metrics["dead_letter_count"],
            metrics["proposal_transfer"],
        )
        history = run_manifest.get("posthoc_resummarizations")
        if not isinstance(history, list):
            history = []
        event = {
            "summary_version": SUMMARY_VERSION,
            "at": utc_now(),
            "resummarizer_commit": safe_git_sha(),
            "backup_dir": str(backup),
            "source_execution_results_sha256": plan["execution_result_sha256"],
            "previous_metrics_sha256": plan["previous_metrics_sha256"],
            "reason": "Correct nested metrics.cascade.totals aggregation without rerunning detector cases.",
        }
        history.append(event)
        run_manifest["posthoc_resummarizations"] = history
        run_manifest["artifact_complete"] = metrics["complete"]
        json_dump(run_dir / "manifest.json", run_manifest)
        json_dump(run_dir / "resummarizations" / f"{stamp}.json", {**plan, "applied": True, **event})
        print(f"Public pair run re-summarized: {run_dir}")
        print(f"  originals preserved: {backup}")
        print(f"  cascade totals: {metrics['cascade']['totals']}")
        print(f"  complete: {metrics['complete']}")
        return 0
    except (PublicBenchmarkError, PublicPairEvaluationError) as exc:
        print(f"[public-pair-resummarize] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
