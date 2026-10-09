#!/usr/bin/env python3
"""Write an observational summary of a native-DocReader run.

Counts only. Does not copy claim quotes, document bodies, or private text
into the summary. This is not labeled accuracy.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any


class SummaryError(RuntimeError):
    """The native run cannot be summarized."""


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise SummaryError(f"missing artifact: {path}") from exc
    except json.JSONDecodeError as exc:
        raise SummaryError(f"invalid JSON: {path}: {exc}") from exc


def as_int(value: Any) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def summarize(run_dir: Path) -> dict[str, Any]:
    manifest = read_json(run_dir / "manifest.json")
    if not isinstance(manifest, dict):
        raise SummaryError("manifest.json root must be an object")
    metrics = read_json(run_dir / "metrics.json") if (run_dir / "metrics.json").is_file() else {}
    if metrics and not isinstance(metrics, dict):
        raise SummaryError("metrics.json root must be an object")
    claims_by_doc = metrics.get("claim_counts_by_document") or {}
    cascade = ((metrics.get("cascade") or {}).get("totals") or {})
    clusters = metrics.get("clusters") or {}
    dead_letters = 0
    if (run_dir / "dead_letters.json").is_file():
        rows = read_json(run_dir / "dead_letters.json")
        dead_letters = len(rows) if isinstance(rows, list) else 0
    documents = []
    ingest = manifest.get("document_ingest") or {}
    knowledge_ids = manifest.get("knowledge_ids") or {}
    for doc_id, knowledge_id in knowledge_ids.items():
        detail = ingest.get(doc_id) or {}
        documents.append({
            "id": doc_id,
            "knowledge_id": knowledge_id,
            "ingest_mode": detail.get("ingest_mode", ""),
            "source_bytes": detail.get("source_bytes"),
            "claims": as_int(claims_by_doc.get(doc_id, 0)),
            "parse_span_artifact": (run_dir / "spans" / f"{doc_id}.json").is_file(),
        })
    return {
        "schema_version": 1,
        "kind": "native_docreader_observational_summary",
        "run_dir": str(run_dir),
        "status": manifest.get("status", "unknown"),
        "scenario_name": manifest.get("scenario_name", ""),
        "variant": manifest.get("variant", ""),
        "knowledge_base_id": manifest.get("knowledge_base_id", ""),
        "document_count": len(documents),
        "claim_count_total": as_int(metrics.get("claim_count_total", sum(item["claims"] for item in documents))),
        "conflict_count_total": as_int(metrics.get("conflict_count_total")),
        "disputed_fact_count": as_int(clusters.get("cluster_count") or metrics.get("observed_disputed_fact_count")),
        "winner_proposal_count": as_int(metrics.get("observed_disputed_fact_winner_count")),
        "dead_letter_count": dead_letters,
        "cascade": {
            "rule_no_conflict": as_int(cascade.get("rule_no_conflict")),
            "rule_direct_conflict": as_int(cascade.get("rule_direct_conflict")),
            "rule_needs_llm": as_int(cascade.get("rule_needs_llm")),
            "llm_batch_call_count": as_int(cascade.get("llm_batch_call_count")),
            "llm_single_call_count": as_int(cascade.get("llm_single_call_count")),
            "token_total": as_int(cascade.get("token_total") or cascade.get("llm_total_tokens")),
        },
        "documents": documents,
        "note": (
            "Observational native-DocReader counts only. Not real-document "
            "accuracy, not human-review accuracy, not pooled with synthetic "
            "policy or public-transfer tables."
        ),
    }


def write_markdown(path: Path, summary: dict[str, Any]) -> None:
    lines = [
        f"# Native DocReader observational summary",
        "",
        f"- status: `{summary['status']}`",
        f"- scenario: `{summary['scenario_name']}`",
        f"- variant: `{summary['variant']}`",
        f"- documents: `{summary['document_count']}`",
        f"- claims: `{summary['claim_count_total']}`",
        f"- raw conflicts: `{summary['conflict_count_total']}`",
        f"- disputed facts: `{summary['disputed_fact_count']}`",
        f"- winner proposals: `{summary['winner_proposal_count']}`",
        f"- dead letters: `{summary['dead_letter_count']}`",
        "",
        "This is not labeled accuracy.",
        "",
        "| document | claims | parse span |",
        "|---|---:|---|",
    ]
    for item in summary["documents"]:
        lines.append(
            f"| `{item['id']}` | {item['claims']} | {'yes' if item['parse_span_artifact'] else 'no'} |"
        )
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Summarize a completed native-DocReader observational run.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--run-dir", required=True, help="Completed run_claims_eval output directory")
    parser.add_argument("--apply", action="store_true", help="Write summary.json and summary.md into the run directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        run_dir = Path(args.run_dir).expanduser().resolve()
        if not run_dir.is_dir():
            raise SummaryError(f"run directory does not exist: {run_dir}")
        summary = summarize(run_dir)
        text = json.dumps(summary, ensure_ascii=False, indent=2)
        print(text)
        if args.apply:
            (run_dir / "native_summary.json").write_text(text + "\n", encoding="utf-8")
            write_markdown(run_dir / "native_summary.md", summary)
        return 0
    except SummaryError as exc:
        print(f"[native-summary] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
