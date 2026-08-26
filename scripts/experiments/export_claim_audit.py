#!/usr/bin/env python3
"""Export a human-reviewable C1 claim-quality audit package from one run.

The exporter intentionally imports the matching functions from
``testdata/claims_eval/evaluate.py`` so the audit uses exactly the same strict,
relaxed and key-only semantics as the headline P/R report. It does not call the
WeKnora service, modify PostgreSQL, or require an API key.

Example:

  python3 scripts/experiments/export_claim_audit.py \
      --run-dir experiments/runs/20260826T162227Z-c1_full-c1-433c19cc

Outputs default to ``<run-dir>/claim_audit/``:

  audit_rows.csv            one row per matched/unmatched gold/prediction
  contradiction_audit.csv   P1-P5/N1 claim/channel/final-conflict evidence
  audit_summary.json        machine-readable aggregate counts
  README.md                  reviewer protocol and category definitions
"""

from __future__ import annotations

import argparse
import csv
import importlib.util
import json
import shutil
import sys
from collections import Counter, defaultdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
EVALUATOR_PATH = ROOT / "testdata/claims_eval/evaluate.py"
GOLD_DIR = ROOT / "testdata/claims_eval/gold"
CONTRADICTIONS_PATH = ROOT / "testdata/claims_eval/contradictions.json"


class AuditError(RuntimeError):
    pass


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise AuditError(f"找不到文件: {path}") from exc
    except json.JSONDecodeError as exc:
        raise AuditError(f"JSON 无法解析: {path}: {exc}") from exc


def write_json(path: Path, data: Any) -> None:
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2, default=str) + "\n", encoding="utf-8")


def load_evaluator() -> Any:
    spec = importlib.util.spec_from_file_location("weknora_claims_evaluator", EVALUATOR_PATH)
    if spec is None or spec.loader is None:
        raise AuditError(f"无法加载 evaluator: {EVALUATOR_PATH}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def resolve_run_dir(raw: str) -> Path:
    path = Path(raw).expanduser().resolve()
    if path.is_file():
        if path.name != "claims_eval_run.json":
            raise AuditError("--run-dir 若传文件，只接受 claims_eval_run.json")
        path = path.parent
    if not path.is_dir():
        raise AuditError(f"run 目录不存在: {path}")
    if not (path / "claims_eval_run.json").is_file():
        raise AuditError(f"run 目录缺少 claims_eval_run.json: {path}")
    return path


def text(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, (dict, list)):
        return json.dumps(value, ensure_ascii=False, sort_keys=True)
    return str(value)


def match_status(tier: str) -> str:
    return {
        "strict": "matched",
        "relaxed": "matched",
        "key_only": "same_slot_value_mismatch",
    }.get(tier, "unknown")


def suggested_action(status: str) -> str:
    return {
        "matched": "confirm_tp_or_schema_equivalent",
        "same_slot_value_mismatch": "review_value_or_temporal_difference",
        "unmatched_gold": "classify_fn",
        "unmatched_prediction": "classify_fp",
    }.get(status, "review")


def priority_for(gold_id: str, contradiction_gold_ids: set[str], status: str) -> str:
    if gold_id in contradiction_gold_ids:
        return "critical"
    if status in {"unmatched_gold", "same_slot_value_mismatch"}:
        return "high"
    if status == "unmatched_prediction":
        return "medium"
    return "low"


def evidence_fields(prefix: str, claim: dict[str, Any] | None) -> dict[str, str]:
    claim = claim or {}
    return {
        f"{prefix}_id": text(claim.get("id", "")),
        f"{prefix}_subject": text(claim.get("subject", "")),
        f"{prefix}_predicate": text(claim.get("predicate", "")),
        f"{prefix}_value": text(claim.get("value", "")),
        f"{prefix}_value_kind": text(claim.get("value_kind", claim.get("_vk", ""))),
        f"{prefix}_qualifiers": text(claim.get("qualifiers", "")),
        f"{prefix}_quote": text(claim.get("quote", "")),
        f"{prefix}_display_key": text(claim.get("_key", "")),
        f"{prefix}_fused_key": text(claim.get("_fused", "")),
        f"{prefix}_value_norm": text(claim.get("_vn", "")),
    }


def load_gold(evaluator: Any) -> tuple[dict[str, list[dict[str, Any]]], dict[str, dict[str, Any]]]:
    by_doc: dict[str, list[dict[str, Any]]] = {}
    by_id: dict[str, dict[str, Any]] = {}
    for path in sorted(GOLD_DIR.glob("*.json")):
        raw = load_json(path)
        doc = text(raw.get("doc", ""))
        claims = [evaluator.enrich(dict(claim)) for claim in raw.get("claims", [])]
        by_doc[doc] = claims
        for claim in claims:
            by_id[text(claim.get("id", ""))] = claim
    return by_doc, by_id


def build_claim_audit(
    evaluator: Any,
    run: dict[str, Any],
    gold_by_doc: dict[str, list[dict[str, Any]]],
    doc_text: dict[str, str],
    contradiction_gold_ids: set[str],
    relaxed_th: float,
) -> tuple[list[dict[str, str]], dict[str, tuple[dict[str, Any], str]], dict[str, Any]]:
    rows: list[dict[str, str]] = []
    gold_to_prediction: dict[str, tuple[dict[str, Any], str]] = {}
    totals: Counter[str] = Counter()
    per_doc: dict[str, Counter[str]] = {}

    predicted_docs = run.get("docs", {})
    for doc, golds in gold_by_doc.items():
        predictions = [evaluator.enrich(dict(claim)) for claim in predicted_docs.get(doc, [])]
        matches = evaluator.match_doc(predictions, golds, relaxed_th)
        pred_match: dict[int, tuple[int, str]] = {pi: (gi, tier) for pi, gi, tier in matches}
        gold_match: dict[int, tuple[int, str]] = {gi: (pi, tier) for pi, gi, tier in matches}
        doc_counter: Counter[str] = Counter()

        for pi, gi, tier in matches:
            prediction = predictions[pi]
            gold = golds[gi]
            status = match_status(tier)
            sim = evaluator.key_sim(prediction["_key"], gold["_key"])
            row: dict[str, str] = {
                "row_kind": "match",
                "document": doc,
                "priority": priority_for(text(gold.get("id", "")), contradiction_gold_ids, status),
                "match_status": status,
                "match_tier": tier,
                "pred_index": str(pi),
                "key_similarity": f"{sim:.4f}",
                "fused_key_equal": str(prediction["_fused"] == gold["_fused"]).lower(),
                "value_norm_equal": str(prediction["_vn"] == gold["_vn"]).lower(),
                "quote_located": str(evaluator.quote_located(doc_text.get(doc, ""), prediction.get("quote", ""))).lower(),
                "value_kind_agree": str(prediction["_vk"] == gold["_vk"]).lower(),
                "suggested_review_action": suggested_action(status),
                "review_label": "",
                "review_note": "",
            }
            row.update(evidence_fields("gold", gold))
            row.update(evidence_fields("pred", prediction))
            rows.append(row)
            gold_to_prediction[text(gold.get("id", ""))] = (prediction, tier)
            totals[status] += 1
            doc_counter[status] += 1

        for gi, gold in enumerate(golds):
            if gi in gold_match:
                continue
            status = "unmatched_gold"
            row = {
                "row_kind": "gold_only",
                "document": doc,
                "priority": priority_for(text(gold.get("id", "")), contradiction_gold_ids, status),
                "match_status": status,
                "match_tier": "",
                "pred_index": "",
                "key_similarity": "",
                "fused_key_equal": "",
                "value_norm_equal": "",
                "quote_located": "",
                "value_kind_agree": "",
                "suggested_review_action": suggested_action(status),
                "review_label": "",
                "review_note": "",
            }
            row.update(evidence_fields("gold", gold))
            row.update(evidence_fields("pred", None))
            rows.append(row)
            totals[status] += 1
            doc_counter[status] += 1

        for pi, prediction in enumerate(predictions):
            if pi in pred_match:
                continue
            status = "unmatched_prediction"
            row = {
                "row_kind": "prediction_only",
                "document": doc,
                "priority": priority_for("", contradiction_gold_ids, status),
                "match_status": status,
                "match_tier": "",
                "pred_index": str(pi),
                "key_similarity": "",
                "fused_key_equal": "",
                "value_norm_equal": "",
                "quote_located": str(evaluator.quote_located(doc_text.get(doc, ""), prediction.get("quote", ""))).lower(),
                "value_kind_agree": "",
                "suggested_review_action": suggested_action(status),
                "review_label": "",
                "review_note": "",
            }
            row.update(evidence_fields("gold", None))
            row.update(evidence_fields("pred", prediction))
            rows.append(row)
            totals[status] += 1
            doc_counter[status] += 1

        per_doc[doc] = doc_counter

    summary = {
        "counts": dict(totals),
        "per_document": {doc: dict(counts) for doc, counts in per_doc.items()},
        "total_rows": len(rows),
    }
    return rows, gold_to_prediction, summary


def conflict_observation_by_doc_pair(
    manifest: dict[str, Any], conflicts: list[dict[str, Any]],
) -> dict[frozenset[str], list[dict[str, Any]]]:
    knowledge_ids = manifest.get("knowledge_ids", {})
    inverse = {text(knowledge_id): text(doc) for doc, knowledge_id in knowledge_ids.items()}
    out: dict[frozenset[str], list[dict[str, Any]]] = defaultdict(list)
    for conflict in conflicts:
        left = inverse.get(text(conflict.get("knowledge_id_a", "")), "")
        right = inverse.get(text(conflict.get("knowledge_id_b", "")), "")
        if left and right:
            out[frozenset({left, right})].append(conflict)
    return out


def build_contradiction_audit(
    evaluator: Any,
    contradictions: dict[str, Any],
    gold_by_id: dict[str, dict[str, Any]],
    gold_to_prediction: dict[str, tuple[dict[str, Any], str]],
    observed: dict[frozenset[str], list[dict[str, Any]]],
    relaxed_th: float,
) -> list[dict[str, str]]:
    rows: list[dict[str, str]] = []
    for pair in contradictions.get("pairs", []):
        side_a = pair.get("a", {})
        side_b = pair.get("b", {})
        gold_a = gold_by_id.get(text(side_a.get("gold_id", "")), {})
        gold_b = gold_by_id.get(text(side_b.get("gold_id", "")), {})
        pred_a = gold_to_prediction.get(text(side_a.get("gold_id", "")))
        pred_b = gold_to_prediction.get(text(side_b.get("gold_id", "")))
        predicted_channel = "missing_prediction"
        similarity = ""
        values_differ = ""
        if pred_a and pred_b:
            pa, tier_a = pred_a
            pb, tier_b = pred_b
            similarity_value = evaluator.key_sim(pa["_fused"], pb["_fused"])
            similarity = f"{similarity_value:.4f}"
            values_differ = str(pa["_vn"] != pb["_vn"]).lower()
            if pa["_fused"] == pb["_fused"] and pa["_vn"] != pb["_vn"]:
                predicted_channel = "strict_claim_key"
            elif similarity_value >= relaxed_th and pa["_vn"] != pb["_vn"]:
                predicted_channel = "relaxed_claim_key"
            elif pa["_vn"] == pb["_vn"]:
                predicted_channel = "same_value"
            else:
                predicted_channel = "no_claim_key_match"
        elif pred_a or pred_b:
            predicted_channel = "one_side_missing"

        doc_pair = frozenset({text(side_a.get("doc", "")), text(side_b.get("doc", ""))})
        final_conflicts = observed.get(doc_pair, [])
        row: dict[str, str] = {
            "pair_id": text(pair.get("pair_id", "")),
            "fact": text(pair.get("fact", "")),
            "expected_conflict": str(bool(pair.get("conflict"))).lower(),
            "predicted_channel": predicted_channel,
            "key_similarity": similarity,
            "predicted_values_differ": values_differ,
            "side_a_match_tier": pred_a[1] if pred_a else "",
            "side_b_match_tier": pred_b[1] if pred_b else "",
            "final_conflict_document_pair_observed": str(bool(final_conflicts)).lower(),
            "final_conflict_count": str(len(final_conflicts)),
            "final_conflict_ids": ";".join(text(item.get("id", "")) for item in final_conflicts),
            "final_conflict_types": ";".join(text(item.get("conflict_type", "")) for item in final_conflicts),
            "final_conflict_reasons": " || ".join(text(item.get("llm_reason", "")) for item in final_conflicts),
            "review_label": "",
            "review_note": "",
        }
        row.update({f"a_{key}": value for key, value in evidence_fields("gold", gold_a).items()})
        row.update({f"b_{key}": value for key, value in evidence_fields("gold", gold_b).items()})
        row.update({f"a_pred_{key}": value for key, value in evidence_fields("pred", pred_a[0] if pred_a else None).items()})
        row.update({f"b_pred_{key}": value for key, value in evidence_fields("pred", pred_b[0] if pred_b else None).items()})
        rows.append(row)
    return rows


def write_csv(path: Path, rows: list[dict[str, str]]) -> None:
    fieldnames: list[str] = []
    for row in rows:
        for key in row:
            if key not in fieldnames:
                fieldnames.append(key)
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fieldnames, extrasaction="ignore")
        writer.writeheader()
        writer.writerows(rows)


def write_readme(
    path: Path,
    run_dir: Path,
    audit_summary: dict[str, Any],
    contradiction_rows: list[dict[str, str]],
) -> None:
    counts = audit_summary["claims"]["counts"]
    expected_conflicts = sum(row["expected_conflict"] == "true" for row in contradiction_rows)
    conflict_observed = sum(
        row["expected_conflict"] == "true" and row["final_conflict_document_pair_observed"] == "true"
        for row in contradiction_rows
    )
    content = f"""# C1 Claim Audit Package

- Source run: `{run_dir}`
- Generated at: `{datetime.now(timezone.utc).replace(microsecond=0).isoformat()}`
- Claim-audit rows: `{audit_summary['claims']['total_rows']}`
- Strict/relaxed matched rows: `{counts.get('matched', 0)}`
- Same-slot value disagreement rows: `{counts.get('same_slot_value_mismatch', 0)}`
- Unmatched gold rows: `{counts.get('unmatched_gold', 0)}`
- Unmatched prediction rows: `{counts.get('unmatched_prediction', 0)}`
- Final conflict document-pair evidence: `{conflict_observed}/{expected_conflicts}` expected contradiction pairs

## Files

| File | Purpose |
|---|---|
| `audit_rows.csv` | One row per matched or unmatched gold/prediction claim; primary manual-review sheet. |
| `contradiction_audit.csv` | P1-P5/N1 claim-key channel and final conflict evidence. |
| `audit_summary.json` | Machine-readable aggregate counts and source metadata. |

## Reviewer protocol

Fill `review_label` and `review_note` in `audit_rows.csv`. Use one of:

| Label | Meaning |
|---|---|
| `confirm_tp` | Strict/relaxed match is semantically correct. |
| `schema_equivalent` | Prediction and gold express the same fact but subject/predicate ontology differs. |
| `gold_scope_mismatch` | Gold claim is outside the current extractor objective (e.g. optional procedure/context). |
| `genuine_fn` | Conflict-relevant gold claim should have been extracted but was missed. |
| `low_value_fp` | Prediction is grounded but not useful for conflict/maintenance. |
| `genuine_fp` | Prediction is unsupported, distorted, or should not exist. |
| `duplicate` | Prediction duplicates another prediction/evidence row. |
| `quote_failure` | Prediction is otherwise useful but its source quote/span is invalid. |
| `annotation_error` | Gold or contradiction annotation needs correction. |

Prioritize rows with `priority=critical` first: they are connected to P1-P5/N1.
Do **not** use a partial diagnostic scenario's global P/R as a publication metric. The exporter is
intended for a complete `c1_full` run; partial scenarios are for routing/adjudication diagnosis.
"""
    path.write_text(content, encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description="Export a C1 human-review audit package from an experiment run.")
    parser.add_argument("--run-dir", required=True, help="Experiment run directory or its claims_eval_run.json")
    parser.add_argument("--output", default="", help="Audit output directory; default <run-dir>/claim_audit")
    parser.add_argument("--relaxed-th", type=float, default=0.5, help="Must match evaluate.py relaxed key threshold")
    parser.add_argument("--overwrite", action="store_true", help="Allow writing into an existing non-empty audit directory")
    args = parser.parse_args()

    try:
        run_dir = resolve_run_dir(args.run_dir)
        output_dir = Path(args.output).expanduser().resolve() if args.output else run_dir / "claim_audit"
        if output_dir.exists() and any(output_dir.iterdir()) and not args.overwrite:
            raise AuditError(f"审计目录已存在且非空: {output_dir}（使用 --overwrite 覆盖）")
        output_dir.mkdir(parents=True, exist_ok=True)

        evaluator = load_evaluator()
        run = load_json(run_dir / "claims_eval_run.json")
        manifest = load_json(run_dir / "manifest.json") if (run_dir / "manifest.json").is_file() else {}
        conflicts = load_json(run_dir / "conflicts.json") if (run_dir / "conflicts.json").is_file() else []
        contradictions = load_json(CONTRADICTIONS_PATH)
        gold_by_doc, gold_by_id = load_gold(evaluator)
        doc_text = evaluator.load_docs()
        contradiction_gold_ids = {
            text(side.get("gold_id", ""))
            for pair in contradictions.get("pairs", [])
            for side in (pair.get("a", {}), pair.get("b", {}))
        }

        claim_rows, gold_to_prediction, claim_summary = build_claim_audit(
            evaluator, run, gold_by_doc, doc_text, contradiction_gold_ids, args.relaxed_th,
        )
        observed = conflict_observation_by_doc_pair(manifest, conflicts if isinstance(conflicts, list) else [])
        contradiction_rows = build_contradiction_audit(
            evaluator, contradictions, gold_by_id, gold_to_prediction, observed, args.relaxed_th,
        )
        summary = {
            "source_run": str(run_dir),
            "run_metadata": {
                "run": run.get("run", ""),
                "extractor": run.get("extractor", ""),
                "date": run.get("date", ""),
                "git_commit": manifest.get("git_commit", ""),
                "knowledge_base_id": manifest.get("knowledge_base_id", ""),
                "summary_model_id": manifest.get("summary_model_id", ""),
            },
            "relaxed_threshold": args.relaxed_th,
            "claims": claim_summary,
            "contradictions": {
                "total": len(contradiction_rows),
                "expected_conflicts": sum(row["expected_conflict"] == "true" for row in contradiction_rows),
                "document_pair_observed": sum(
                    row["expected_conflict"] == "true" and row["final_conflict_document_pair_observed"] == "true"
                    for row in contradiction_rows
                ),
            },
        }
        write_csv(output_dir / "audit_rows.csv", claim_rows)
        write_csv(output_dir / "contradiction_audit.csv", contradiction_rows)
        write_json(output_dir / "audit_summary.json", summary)
        write_readme(output_dir / "README.md", run_dir, summary, contradiction_rows)

        print(f"Audit package written: {output_dir}")
        print(f"  claim rows: {len(claim_rows)}")
        print(f"  contradiction rows: {len(contradiction_rows)}")
        print(f"  unmatched gold: {claim_summary['counts'].get('unmatched_gold', 0)}")
        print(f"  unmatched prediction: {claim_summary['counts'].get('unmatched_prediction', 0)}")
        return 0
    except AuditError as exc:
        print(f"[claim-audit] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
