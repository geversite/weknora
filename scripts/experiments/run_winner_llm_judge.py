#!/usr/bin/env python3
"""LLM-as-judge winner baseline on a completed synthetic policy matrix.

Reads detector artifacts only. Calls an OpenAI-compatible chat endpoint.
Does not ingest documents or mutate WeKnora. Predictions are later scored by
score_winner_fact_families.py --llm-judge-file.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

import score_winner_fact_families as scorer
from llm_judge_client import JudgeError, chat_json


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def cluster_brief(row: dict[str, Any]) -> str:
    lines = ["Sources:"]
    metadata = row.get("metadata") or {}
    for document_id, meta in metadata.items():
        if not isinstance(meta, dict):
            continue
        lines.append(
            f"- id={document_id}; issuer={meta.get('issuer', '')}; "
            f"date={meta.get('effective_date', '')}; version={meta.get('version', '')}"
        )
    facts = []
    detector_dir = Path(str(row.get("detector_dir") or ""))
    facts_path = detector_dir / "disputed_facts.json"
    if facts_path.is_file():
        raw = scorer.read_json(facts_path)
        if isinstance(raw, list):
            facts = raw
    values: list[str] = []
    for fact in facts:
        if not isinstance(fact, dict):
            continue
        payload = fact.get("candidate_values")
        if isinstance(payload, str):
            try:
                payload = json.loads(payload)
            except json.JSONDecodeError:
                payload = [payload]
        if isinstance(payload, list):
            values.extend(str(item) for item in payload)
    if values:
        lines.append("Candidate values: " + " | ".join(dict.fromkeys(values)))
    return "\n".join(lines)


def judge_one(row: dict[str, Any]) -> dict[str, Any]:
    ids = sorted((row.get("metadata") or {}).keys())
    messages = [
        {
            "role": "system",
            "content": (
                "You select a document-version winner or abstain. "
                "Return JSON only: {\"winner_id\":\"<id or empty>\",\"abstain\":true|false,\"reason\":\"short\"}. "
                "Abstain if issuers differ, metadata is missing, dates/versions disagree, "
                "or the sources are parallel products rather than revisions of one document. "
                "Do not invent a winner."
            ),
        },
        {
            "role": "user",
            "content": cluster_brief(row) + "\nAllowed ids: " + ", ".join(ids),
        },
    ]
    payload = chat_json(messages)
    abstain = bool(payload.get("abstain"))
    winner = str(payload.get("winner_id") or payload.get("winner") or "").strip()
    if abstain or winner not in ids:
        winner = ""
        abstain = True
    return {
        "case_id": row["case_id"],
        "replicate": row["replicate"],
        "fact_family_id": row["fact_family_id"],
        "winner_document": winner,
        "abstain": abstain,
        "reason": str(payload.get("reason") or "")[:240],
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run an LLM winner judge on a completed policy matrix.")
    parser.add_argument("--matrix-run", required=True)
    parser.add_argument("--output", required=True, help="JSON prediction file (private, outside Git)")
    parser.add_argument("--split", default="holdout", choices=["all", "development", "holdout"])
    parser.add_argument("--max-cases", type=int, default=0)
    parser.add_argument("--resume", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        matrix_run = Path(args.matrix_run).expanduser().resolve()
        matrix_manifest = scorer.read_json(matrix_run / "matrix_manifest.json")
        matrix_results = scorer.read_json(matrix_run / "matrix_results.json")
        matrix = (matrix_manifest or {}).get("matrix") or {}
        cases = {str(item.get("id", "")): item for item in matrix.get("cases", []) if isinstance(item, dict)}
        output = Path(args.output).expanduser().resolve()
        done: dict[str, dict[str, Any]] = {}
        if args.resume and output.is_file():
            existing = json.loads(output.read_text(encoding="utf-8"))
            for row in existing.get("predictions", []):
                if isinstance(row, dict) and row.get("case_id"):
                    done[f"{row['case_id']}:{row.get('replicate')}"] = row
        selected = []
        for record in matrix_results:
            if not isinstance(record, dict):
                continue
            case = cases.get(str(record.get("case_id") or ""))
            if not case:
                continue
            if args.split != "all" and str(case.get("split") or "") != args.split:
                continue
            selected.append((record, case))
            if args.max_cases and len(selected) >= args.max_cases:
                break
        predictions = list(done.values())
        for record, case in selected:
            row = scorer.execution_row(record, case)
            key = f"{row['case_id']}:{row['replicate']}"
            if key in done:
                print(f"[llm-judge] reuse {key}")
                continue
            print(f"[llm-judge] {key}")
            item = judge_one(row)
            done[key] = item
            predictions.append(item)
            json_dump(output, {"schema_version": 1, "matrix_run": str(matrix_run), "predictions": predictions})
        json_dump(output, {"schema_version": 1, "matrix_run": str(matrix_run), "predictions": predictions})
        print(f"LLM winner judge complete: {output} ({len(predictions)} rows)")
        return 0
    except (scorer.FactFamilyScoreError, JudgeError) as exc:
        print(f"[llm-judge] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
