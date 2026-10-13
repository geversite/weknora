#!/usr/bin/env python3
"""LLM-as-judge pair baseline on a prepared public pair manifest.

Reads the two source texts from the adapter-written documents. Does not call
WeKnora ingest. Compare against expected_conflict with pair P/R/A.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

from llm_judge_client import JudgeError, chat_json
from run_public_pair_eval import PublicPairEvaluationError, normalize_case, ratio


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def document_text(scenario: dict[str, Any], doc_id: str, scenario_path: Path) -> str:
    for document in scenario.get("documents") or []:
        if not isinstance(document, dict) or str(document.get("id")) != doc_id:
            continue
        path = Path(str(document.get("path") or "")).expanduser()
        if not path.is_absolute():
            path = (scenario_path.parent / path).resolve()
        return path.read_text(encoding="utf-8", errors="replace")[:4000]
    return ""


def judge_pair(left: str, right: str) -> bool:
    payload = chat_json(
        [
            {
                "role": "system",
                "content": (
                    "You are a conservative conflict judge for two short texts. "
                    "Return JSON only: {\"conflict\": true|false, \"reason\":\"short\"}. "
                    "conflict=true only if they state the same fact with mutually exclusive values. "
                    "Different entities, complementary details, or missing information are not conflicts."
                ),
            },
            {"role": "user", "content": f"Text A:\n{left}\n\nText B:\n{right}\n"},
        ]
    )
    return bool(payload.get("conflict"))


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="LLM pair judge on a public pair_eval_manifest.json")
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--split", default="holdout", choices=["all", "development", "holdout"])
    parser.add_argument("--max-cases", type=int, default=0)
    parser.add_argument("--resume", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        manifest_path = Path(args.manifest).expanduser().resolve()
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        cases_raw = manifest.get("cases") if isinstance(manifest, dict) else None
        if not isinstance(cases_raw, list):
            raise JudgeError("manifest missing cases[]")
        variant = str(manifest.get("variant") or "c2-batch")
        selected = []
        for raw in cases_raw:
            case = normalize_case(raw, manifest_dir=manifest_path.parent, inherited_variant=variant)
            if args.split != "all" and case["split"] != args.split:
                continue
            selected.append(case)
            if args.max_cases and len(selected) >= args.max_cases:
                break
        output = Path(args.output).expanduser().resolve()
        done: dict[str, dict[str, Any]] = {}
        if args.resume and output.is_file():
            existing = json.loads(output.read_text(encoding="utf-8"))
            for row in existing.get("rows", []):
                if isinstance(row, dict) and row.get("id"):
                    done[str(row["id"])] = row
        rows = list(done.values())
        tp = fp = tn = fn = 0
        for case in selected:
            if case["id"] in done:
                row = done[case["id"]]
            else:
                scenario_path = Path(case["scenario"])
                scenario = json.loads(scenario_path.read_text(encoding="utf-8"))
                pair = case["expected_pair"]
                left = document_text(scenario, pair["left"], scenario_path)
                right = document_text(scenario, pair["right"], scenario_path)
                predicted = judge_pair(left, right)
                expected = bool(case["expected_conflict"])
                row = {
                    "id": case["id"],
                    "fact_family_id": case["fact_family_id"],
                    "split": case["split"],
                    "expected_conflict": expected,
                    "predicted_conflict": predicted,
                    "correct": predicted == expected,
                }
                done[case["id"]] = row
                rows.append(row)
                json_dump(output, {"schema_version": 1, "rows": rows})
                print(f"[pair-judge] {case['id']}: expected={expected} predicted={predicted}")
            expected = bool(row["expected_conflict"])
            predicted = bool(row["predicted_conflict"])
            if expected and predicted:
                tp += 1
            elif expected and not predicted:
                fn += 1
            elif (not expected) and predicted:
                fp += 1
            else:
                tn += 1
        metrics = {
            "n": len(rows),
            "tp": tp,
            "tn": tn,
            "fp": fp,
            "fn": fn,
            "precision": ratio(tp, tp + fp) if tp + fp else None,
            "recall": ratio(tp, tp + fn) if tp + fn else None,
            "accuracy": ratio(tp + tn, len(rows)) if rows else None,
            "note": "LLM pair judge on adapter texts. Not WeKnora cascade accuracy. Not official VitaminC/WFD protocol.",
        }
        json_dump(output, {"schema_version": 1, "metrics": metrics, "rows": rows})
        print("Public pair LLM judge complete:", output)
        print("  P/R/A:", metrics["precision"], metrics["recall"], metrics["accuracy"], "n=", metrics["n"])
        return 0
    except (JudgeError, PublicPairEvaluationError) as exc:
        print(f"[pair-judge] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
