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


def original_names_from_scenario(manifest: dict[str, Any]) -> dict[str, str]:
    raw_path = str(manifest.get("scenario_path") or "").strip()
    if not raw_path:
        return {}
    path = Path(raw_path).expanduser()
    if not path.is_file():
        return {}
    try:
        scenario = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    names: dict[str, str] = {}
    for document in scenario.get("documents") or []:
        if not isinstance(document, dict):
            continue
        doc_id = str(document.get("id") or "")
        name = str(document.get("original_filename") or document.get("source_relative_path") or "")
        if doc_id and name:
            names[doc_id] = name
    return names


def parse_json_list(value: Any) -> list[Any]:
    if isinstance(value, list):
        return value
    if isinstance(value, str) and value.strip():
        try:
            parsed = json.loads(value)
        except json.JSONDecodeError:
            return []
        return parsed if isinstance(parsed, list) else []
    return []


def knowledge_id_from_ref(ref: Any) -> str:
    if isinstance(ref, dict):
        ref = ref.get("knowledge_id") or ref.get("id") or ""
    text = str(ref or "").strip()
    if text.startswith("knowledge:"):
        text = text.split(":", 1)[1]
    return text


def ratio(numerator: int, denominator: int) -> float | None:
    if denominator <= 0:
        return None
    return numerator / denominator


def observational_metrics(documents: list[dict[str, Any]], facts: list[dict[str, Any]]) -> dict[str, Any]:
    parsed = sum(1 for item in documents if item.get("parse_span_artifact"))
    with_claims = sum(1 for item in documents if as_int(item.get("claims")) > 0)
    by_ext: dict[str, dict[str, int]] = {}
    for item in documents:
        ext = Path(str(item.get("original_filename") or item.get("id") or "")).suffix.lower() or "(none)"
        bucket = by_ext.setdefault(ext, {"files": 0, "with_claims": 0, "claims": 0})
        bucket["files"] += 1
        claims = as_int(item.get("claims"))
        bucket["claims"] += claims
        if claims > 0:
            bucket["with_claims"] += 1
    same_stem_pdf_docx: list[dict[str, Any]] = []
    by_stem: dict[str, dict[str, dict[str, Any]]] = {}
    for item in documents:
        name = str(item.get("original_filename") or "")
        stem = Path(name).stem
        ext = Path(name).suffix.lower()
        if stem and ext in {".pdf", ".docx"}:
            by_stem.setdefault(stem, {})[ext] = item
    for stem, pair in sorted(by_stem.items()):
        if ".pdf" in pair and ".docx" in pair:
            same_stem_pdf_docx.append({
                "stem": stem,
                "docx_claims": as_int(pair[".docx"].get("claims")),
                "pdf_claims": as_int(pair[".pdf"].get("claims")),
            })

    kind_clusters = {"claim_key": 0, "fuzzy_slot": 0, "chunk_pair": 0, "document_singleton": 0, "other": 0}
    kind_raw = {key: 0 for key in kind_clusters}
    exact_ge2_values = 0
    exact_single_value = 0
    exact_ge2_sources = 0
    for fact in facts:
        kind = str(fact.get("anchor_kind") or "")
        key = str(fact.get("fact_key") or "")
        if not kind:
            if key.startswith("claim_key:"):
                kind = "claim_key"
            elif key.startswith("fuzzy_slot:"):
                kind = "fuzzy_slot"
            elif key.startswith("chunk_pair:"):
                kind = "chunk_pair"
            elif key.startswith("document_singleton:"):
                kind = "document_singleton"
        if kind not in kind_clusters:
            kind = "other"
        raw = as_int(fact.get("conflict_count"))
        kind_clusters[kind] += 1
        kind_raw[kind] += raw
        if kind == "claim_key":
            values = [str(item) for item in parse_json_list(fact.get("candidate_values"))]
            unique_values = len(set(values))
            sources = as_int(fact.get("source_count")) or len(parse_json_list(fact.get("source_refs")))
            if unique_values >= 2:
                exact_ge2_values += 1
            elif unique_values == 1:
                exact_single_value += 1
            if sources >= 2:
                exact_ge2_sources += 1

    cluster_count = len(facts)
    raw_total = sum(kind_raw.values()) or sum(as_int(item.get("conflict_count")) for item in facts)
    proposals = sum(1 for fact in facts if str(fact.get("suggested_winner_knowledge_id") or "").strip())
    return {
        "files_selected": len(documents),
        "files_parsed": parsed,
        "files_with_claims": with_claims,
        "parse_success_rate": ratio(parsed, len(documents)),
        "nonzero_claim_rate": ratio(with_claims, len(documents)),
        "claims_by_extension": by_ext,
        "same_stem_pdf_docx_pairs": same_stem_pdf_docx,
        "raw_conflicts": raw_total,
        "disputed_facts": cluster_count,
        "raw_per_disputed_fact": ratio(raw_total, cluster_count),
        "anchor_kind_clusters": kind_clusters,
        "anchor_kind_raw_conflicts": kind_raw,
        "exact_key_cluster_share": ratio(kind_clusters["claim_key"], cluster_count),
        "exact_key_raw_share": ratio(kind_raw["claim_key"], raw_total),
        "exact_key_clusters": kind_clusters["claim_key"],
        "exact_key_clusters_with_ge2_values": exact_ge2_values,
        "exact_key_clusters_with_1_value": exact_single_value,
        "exact_key_clusters_with_ge2_sources": exact_ge2_sources,
        "global_proposals": proposals,
        "proposal_rate": ratio(proposals, cluster_count),
        "abstention_rate": ratio(cluster_count - proposals, cluster_count),
        "note": (
            "Structural observational metrics only. Abstention is not proposal "
            "precision. Exact-key multi-value clusters are not human-labeled TPs."
        ),
    }


def claims_by_knowledge(run_dir: Path) -> dict[str, int]:
    path = run_dir / "claims.json"
    if not path.is_file():
        return {}
    rows = read_json(path)
    if not isinstance(rows, list):
        raise SummaryError("claims.json root must be an array")
    counts: dict[str, int] = {}
    for row in rows:
        if not isinstance(row, dict):
            continue
        knowledge_id = str(row.get("knowledge_id") or "")
        if not knowledge_id:
            continue
        counts[knowledge_id] = counts.get(knowledge_id, 0) + 1
    return counts


def summarize(run_dir: Path) -> dict[str, Any]:
    manifest = read_json(run_dir / "manifest.json")
    if not isinstance(manifest, dict):
        raise SummaryError("manifest.json root must be an object")
    metrics = read_json(run_dir / "metrics.json") if (run_dir / "metrics.json").is_file() else {}
    if metrics and not isinstance(metrics, dict):
        raise SummaryError("metrics.json root must be an object")
    wait_counts = metrics.get("claim_counts_by_document") or {}
    final_by_knowledge = claims_by_knowledge(run_dir)
    original_names = original_names_from_scenario(manifest)
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
        wait_count = as_int(wait_counts.get(doc_id, 0))
        final_count = as_int(final_by_knowledge.get(str(knowledge_id), wait_count))
        documents.append({
            "id": doc_id,
            "knowledge_id": knowledge_id,
            "original_filename": original_names.get(doc_id, ""),
            "ingest_mode": detail.get("ingest_mode", ""),
            "source_bytes": detail.get("source_bytes"),
            "claims_wait": wait_count,
            "claims": final_count,
            "parse_span_artifact": (run_dir / "spans" / f"{doc_id}.json").is_file(),
        })
    final_total = sum(item["claims"] for item in documents)
    reported_total = as_int(metrics.get("claim_count_total", final_total))
    facts: list[dict[str, Any]] = []
    if (run_dir / "disputed_facts.json").is_file():
        raw_facts = read_json(run_dir / "disputed_facts.json")
        if not isinstance(raw_facts, list):
            raise SummaryError("disputed_facts.json root must be an array")
        facts = [row for row in raw_facts if isinstance(row, dict)]
    paper = observational_metrics(documents, facts)
    return {
        "schema_version": 1,
        "kind": "native_docreader_observational_summary",
        "run_dir": str(run_dir),
        "status": manifest.get("status", "unknown"),
        "scenario_name": manifest.get("scenario_name", ""),
        "variant": manifest.get("variant", ""),
        "knowledge_base_id": manifest.get("knowledge_base_id", ""),
        "document_count": len(documents),
        "claim_count_total": reported_total if reported_total else final_total,
        "claim_count_by_exported_rows": final_total,
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
            "llm_prompt_tokens": as_int(cascade.get("llm_prompt_tokens")),
            "llm_completion_tokens": as_int(cascade.get("llm_completion_tokens")),
            "token_total": as_int(cascade.get("llm_prompt_tokens")) + as_int(cascade.get("llm_completion_tokens")),
            "duration_ms": as_int(cascade.get("duration_ms")),
        },
        "documents": documents,
        "observational_metrics": paper,
        "note": (
            "Observational native-DocReader counts only. Per-document claims "
            "come from exported claims.json, not the in-run wait snapshot. "
            "Not real-document accuracy, not human-review accuracy, not pooled "
            "with synthetic policy or public-transfer tables."
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
        "## Paper-facing observational metrics",
        "",
    ]
    paper = summary.get("observational_metrics") or {}
    if paper:
        lines += [
            f"- parse success: `{paper.get('files_parsed')}/{paper.get('files_selected')}`",
            f"- files with claims: `{paper.get('files_with_claims')}/{paper.get('files_selected')}`",
            f"- exact-key / fuzzy / chunk-pair clusters: "
            f"`{paper.get('anchor_kind_clusters', {}).get('claim_key')} / "
            f"{paper.get('anchor_kind_clusters', {}).get('fuzzy_slot')} / "
            f"{paper.get('anchor_kind_clusters', {}).get('chunk_pair')}`",
            f"- exact-key share of raw conflicts: `{paper.get('exact_key_raw_share')}`",
            f"- exact-key clusters with ≥2 values: `{paper.get('exact_key_clusters_with_ge2_values')}/{paper.get('exact_key_clusters')}`",
            f"- global proposals / abstention: `{paper.get('global_proposals')}/{paper.get('disputed_facts')}` / `{paper.get('abstention_rate')}`",
            "",
        ]
    lines += [
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
