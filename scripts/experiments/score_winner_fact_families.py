#!/usr/bin/env python3
"""Score C4.6 winner policy at fact-family granularity from C4.9 artifacts.

The C4.9 lifecycle runner already validates system actions. This companion
program reads its immutable JSON artifacts and evaluates the proposal policy at
one fact family (rather than one raw chunk pair) against four intentionally
simple baselines:

* latest_upload
* date_only
* version_only
* raw_c3_local_vote

It never calls a model/API/Asynq/Docker/PostgreSQL and never mutates a run.
Generated scores remain controlled-policy metrics unless the input corpus itself
is independently human-reviewed real data.
"""

from __future__ import annotations

import argparse
import calendar
import collections
import csv
import datetime as dt
import json
import re
import sys
import unicodedata
from pathlib import Path
from statistics import mean
from typing import Any, Callable


ROOT = Path(__file__).resolve().parents[2]
VALID_SPLITS = {"all", "development", "holdout"}
METHODS = (
    "c46_global_proposal",
    "latest_upload",
    "date_only",
    "version_only",
    "raw_c3_local_vote",
)
METHOD_LABELS = {
    "c46_global_proposal": "C4.6 global proposal",
    "latest_upload": "Latest upload",
    "date_only": "Date only",
    "version_only": "Version only",
    "raw_c3_local_vote": "Raw C3 local vote",
}
OUTCOME_POSITIVE = "adopt_reopen"
OUTCOME_NEGATIVE = "no_proposal"
MULTIPLE_PREFIX = "__multiple__:"
UNKNOWN_PREFIX = "__unknown__:"


class FactFamilyScoreError(RuntimeError):
    """The matrix artifacts cannot support a trustworthy fact-family score."""


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2, default=str) + "\n", encoding="utf-8")


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise FactFamilyScoreError(f"找不到 artifact: {path}") from exc
    except json.JSONDecodeError as exc:
        raise FactFamilyScoreError(f"artifact JSON 无法解析: {path}: {exc}") from exc


def as_int(value: Any) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def as_float(value: Any) -> float:
    try:
        return float(value or 0)
    except (TypeError, ValueError):
        return 0.0


def ratio(numerator: int, denominator: int) -> float | None:
    return round(numerator / denominator, 6) if denominator else None


def resolve_path(raw: str) -> Path:
    path = Path(raw).expanduser()
    return path.resolve() if path.is_absolute() else (ROOT / path).resolve()


def parse_json_object(value: Any) -> dict[str, Any]:
    if isinstance(value, dict):
        return value
    if not isinstance(value, str) or not value.strip():
        return {}
    try:
        parsed = json.loads(value)
    except json.JSONDecodeError:
        return {}
    return parsed if isinstance(parsed, dict) else {}


def normalize_metadata_label(value: str) -> str:
    out: list[str] = []
    for char in value.strip().lower():
        category = unicodedata.category(char)
        if category.startswith("Z") or category.startswith("P") or category.startswith("S"):
            continue
        out.append(char)
    return "".join(out)


def parse_title_date(value: str) -> tuple[str, str]:
    value = value.strip()
    full = re.search(r"(?<!\d)(\d{4})\s*(?:年|[-/.])\s*(\d{1,2})\s*(?:月|[-/.])\s*(\d{1,2})\s*(?:日)?", value)
    if full:
        year, month, day = map(int, full.groups())
        try:
            dt.date(year, month, day)
        except ValueError:
            return "", ""
        return f"{year:04d}-{month:02d}-{day:02d}", "day"
    month_match = re.search(r"(?<!\d)(\d{4})\s*(?:年|[-/.])\s*(\d{1,2})(?:\s*月)?(?!\d)", value)
    if month_match:
        year, month = map(int, month_match.groups())
        if 1 <= month <= 12:
            return f"{year:04d}-{month:02d}", "month"
        return "", ""
    year_match = re.search(r"(?<!\d)(\d{4})\s*年", value)
    if year_match:
        return year_match.group(1), "year"
    return "", ""


def parse_title_version(value: str) -> str:
    match = re.search(r"(?i)(?:^|[^A-Za-z0-9])v\s*(\d+(?:\.\d+){0,3})(?:$|[^A-Za-z0-9])", value)
    if not match:
        match = re.search(r"第\s*(\d+(?:\.\d+){0,3})\s*版", value)
    if not match:
        match = re.fullmatch(r"\s*(\d+(?:\.\d+){0,3})\s*(?:版)?\s*", value)
    if not match:
        return ""
    try:
        return ".".join(str(int(part)) for part in match.group(1).split("."))
    except ValueError:
        return ""


def metadata_from_title(title: str) -> dict[str, str]:
    meta: dict[str, str] = {"title": title, "origin": "scenario_title_fallback"}
    for segment in re.split(r"[；;|]", title):
        if "：" in segment:
            label, value = segment.split("：", 1)
        elif ":" in segment:
            label, value = segment.split(":", 1)
        else:
            continue
        label = normalize_metadata_label(label)
        value = value.strip()
        if not value:
            continue
        if label in {"发布机构", "发布单位", "编制单位", "发文单位", "发布者", "issuer", "publisher", "issuingorganization"}:
            meta.setdefault("issuer", value)
        elif label in {"生效日期", "生效时间", "发布日期", "发布日", "更新日期", "修订日期", "版本日期", "effectivedate", "publicationdate", "releasedate", "updateddate"}:
            date, precision = parse_title_date(value)
            if date:
                meta["effective_date"] = date
                meta["effective_date_precision"] = precision
        elif label in {"版本", "版本号", "修订版本", "edition", "version"}:
            version = parse_title_version(value)
            if version:
                meta.setdefault("version", version)
    return meta


def normalized_meta(raw: dict[str, Any]) -> dict[str, str]:
    return {
        "issuer": str(raw.get("issuer", "") or "").strip(),
        "effective_date": str(raw.get("effective_date", "") or "").strip(),
        "effective_date_precision": str(raw.get("effective_date_precision", "") or "").strip(),
        "version": str(raw.get("version", "") or "").strip(),
        "title": str(raw.get("title", "") or "").strip(),
        "origin": str(raw.get("origin", "artifact_doc_meta") or "artifact_doc_meta"),
    }


def metadata_signature(meta: dict[str, str]) -> tuple[str, str, str]:
    issuer = "".join(
        char.lower() for char in meta.get("issuer", "")
        if not unicodedata.category(char).startswith(("Z", "P", "S"))
    )
    return issuer, meta.get("effective_date", ""), meta.get("version", "")


def date_interval(meta: dict[str, str]) -> tuple[dt.date, dt.date] | None:
    raw = meta.get("effective_date", "")
    match = re.fullmatch(r"(\d{4})(?:-(\d{2})(?:-(\d{2}))?)?", raw)
    if not match:
        return None
    year = int(match.group(1))
    month_text, day_text = match.group(2), match.group(3)
    if not month_text:
        return dt.date(year, 1, 1), dt.date(year, 12, 31)
    month = int(month_text)
    if not 1 <= month <= 12:
        return None
    if not day_text:
        return dt.date(year, month, 1), dt.date(year, month, calendar.monthrange(year, month)[1])
    day = int(day_text)
    try:
        instant = dt.date(year, month, day)
    except ValueError:
        return None
    return instant, instant


def compare_dates(left: dict[str, str], right: dict[str, str]) -> int | None:
    left_interval = date_interval(left)
    right_interval = date_interval(right)
    if left_interval is None or right_interval is None:
        return None
    if left_interval[0] > right_interval[1]:
        return 1
    if right_interval[0] > left_interval[1]:
        return -1
    return 0


def version_parts(value: str) -> list[int] | None:
    if not re.fullmatch(r"\d+(?:\.\d+){0,3}", value):
        return None
    try:
        return [int(part) for part in value.split(".")]
    except ValueError:
        return None


def compare_versions(left: dict[str, str], right: dict[str, str]) -> int | None:
    left_parts = version_parts(left.get("version", ""))
    right_parts = version_parts(right.get("version", ""))
    if left_parts is None or right_parts is None:
        return None
    length = max(len(left_parts), len(right_parts))
    for index in range(length):
        left_value = left_parts[index] if index < len(left_parts) else 0
        right_value = right_parts[index] if index < len(right_parts) else 0
        if left_value > right_value:
            return 1
        if right_value > left_value:
            return -1
    return 0


def unique_strict_max(
    document_ids: list[str],
    metadata: dict[str, dict[str, str]],
    compare: Callable[[dict[str, str], dict[str, str]], int | None],
) -> str:
    if len(document_ids) < 2 or any(document_id not in metadata for document_id in document_ids):
        return ""
    winners: list[str] = []
    for candidate in document_ids:
        if all(
            compare(metadata[candidate], metadata[other]) == 1
            for other in document_ids if other != candidate
        ):
            winners.append(candidate)
    return winners[0] if len(winners) == 1 else ""


def proposal_prediction(metrics: dict[str, Any]) -> str:
    proposals = metrics.get("winner_proposals", [])
    if not isinstance(proposals, list) or not proposals:
        return ""
    winners: list[str] = []
    for proposal in proposals:
        if not isinstance(proposal, dict):
            continue
        winner = str(proposal.get("winner_document", "") or "").strip()
        if winner:
            winners.append(winner)
    winners = sorted(set(winners))
    if len(winners) == 1:
        return winners[0]
    return MULTIPLE_PREFIX + ",".join(winners) if winners else ""


def raw_c3_vote_prediction(conflicts: list[dict[str, Any]], knowledge_to_document: dict[str, str]) -> str:
    votes: collections.Counter[str] = collections.Counter()
    for conflict in conflicts:
        resolution = str(conflict.get("suggested_resolution", "") or "").strip()
        if resolution == "resolved_newer_wins":
            knowledge_id = str(conflict.get("knowledge_id_a", "") or "")
        elif resolution == "resolved_older_wins":
            knowledge_id = str(conflict.get("knowledge_id_b", "") or "")
        else:
            continue
        document = knowledge_to_document.get(knowledge_id)
        if document:
            votes[document] += 1
    if not votes:
        return ""
    maximum = max(votes.values())
    winners = sorted(document for document, count in votes.items() if count == maximum)
    return winners[0] if len(winners) == 1 else ""


def metadata_from_conflicts(
    conflicts: list[dict[str, Any]], knowledge_to_document: dict[str, str], scenario_documents: dict[str, dict[str, Any]],
) -> tuple[dict[str, dict[str, str]], list[str]]:
    metadata: dict[str, dict[str, str]] = {}
    issues: list[str] = []
    for conflict in conflicts:
        for side in ("a", "b"):
            knowledge_id = str(conflict.get(f"knowledge_id_{side}", "") or "")
            document_id = knowledge_to_document.get(knowledge_id)
            if not document_id:
                continue
            parsed = parse_json_object(conflict.get(f"doc_meta_{side}"))
            if not parsed:
                continue
            meta = normalized_meta(parsed)
            if not any(meta.get(field) for field in ("issuer", "effective_date", "version")):
                continue
            existing = metadata.get(document_id)
            if existing and metadata_signature(existing) != metadata_signature(meta):
                issues.append(f"document {document_id} has inconsistent doc_meta snapshots")
                continue
            metadata[document_id] = meta
    for document_id, document in scenario_documents.items():
        if document_id not in metadata:
            metadata[document_id] = normalized_meta(metadata_from_title(str(document.get("title", "") or "")))
    return metadata, issues


def evaluate_prediction(prediction: str, expected_outcome: str, expected_winner: str) -> dict[str, Any]:
    emitted = bool(prediction)
    if expected_outcome == OUTCOME_POSITIVE:
        if prediction == expected_winner:
            return {"label": "correct_winner", "correct": True, "tp": 1, "tn": 0, "fp": 0, "fn": 0, "unsafe": 0}
        if not emitted:
            return {"label": "missed_winner", "correct": False, "tp": 0, "tn": 0, "fp": 0, "fn": 1, "unsafe": 0}
        return {"label": "wrong_winner", "correct": False, "tp": 0, "tn": 0, "fp": 1, "fn": 1, "unsafe": 0}
    if expected_outcome == OUTCOME_NEGATIVE:
        if not emitted:
            return {"label": "correct_no_proposal", "correct": True, "tp": 0, "tn": 1, "fp": 0, "fn": 0, "unsafe": 0}
        return {"label": "unsafe_action", "correct": False, "tp": 0, "tn": 0, "fp": 1, "fn": 0, "unsafe": 1}
    raise FactFamilyScoreError(f"未知 expected_outcome: {expected_outcome}")


def empty_counts() -> dict[str, int]:
    return {"samples": 0, "tp": 0, "tn": 0, "fp": 0, "fn": 0, "unsafe_action_count": 0, "correct": 0, "abstentions": 0, "wrong_winner_count": 0}


def add_evaluation(counts: dict[str, int], evaluation: dict[str, Any], prediction: str) -> None:
    counts["samples"] += 1
    for field in ("tp", "tn", "fp", "fn"):
        counts[field] += as_int(evaluation.get(field))
    counts["unsafe_action_count"] += as_int(evaluation.get("unsafe"))
    counts["correct"] += 1 if evaluation.get("correct") else 0
    counts["abstentions"] += 1 if not prediction else 0
    counts["wrong_winner_count"] += 1 if evaluation.get("label") == "wrong_winner" else 0


def finish_counts(counts: dict[str, int]) -> dict[str, Any]:
    return {
        **counts,
        "precision": ratio(counts["tp"], counts["tp"] + counts["fp"]),
        "recall": ratio(counts["tp"], counts["tp"] + counts["fn"]),
        "policy_accuracy": ratio(counts["correct"], counts["samples"]),
    }


def cascade_totals(metrics: dict[str, Any]) -> dict[str, int]:
    totals = ((metrics.get("cascade") or {}).get("totals") or {}) if isinstance(metrics, dict) else {}
    fields = (
        "candidate_claim_pairs", "candidate_fallback_pairs", "candidate_after_dedupe", "candidates_submitted",
        "rule_no_conflict", "rule_direct_conflict", "rule_needs_llm", "llm_pair_count", "llm_batch_call_count",
        "llm_single_call_count", "llm_single_fallback_count", "llm_prompt_tokens", "llm_completion_tokens",
        "final_conflict_count", "duration_ms",
    )
    return {field: as_int(totals.get(field)) for field in fields}


def load_scenario(path_text: str) -> dict[str, Any]:
    path = resolve_path(path_text)
    data = read_json(path)
    if not isinstance(data, dict):
        raise FactFamilyScoreError(f"scenario 根节点不是对象: {path}")
    return data


def context_for_case(case: dict[str, Any], record: dict[str, Any]) -> dict[str, str]:
    scenario = load_scenario(str(case.get("scenario", record.get("scenario", ""))))
    case_id = str(record.get("case_id") or case.get("id") or "").strip()
    if not case_id:
        raise FactFamilyScoreError("matrix result 缺少 case_id")
    fact_family_id = str(
        record.get("fact_family_id") or case.get("fact_family_id") or scenario.get("fact_family_id") or case_id,
    ).strip()
    split = str(record.get("split") or case.get("split") or scenario.get("split") or "").strip()
    case_type = str(record.get("case_type") or case.get("case_type") or scenario.get("case_type") or "").strip()
    expected_outcome = str(record.get("expected_outcome") or case.get("expected_outcome") or "").strip()
    expected_winner = str(record.get("expected_winner_document") or case.get("expected_winner_document") or "").strip()
    if expected_outcome not in {OUTCOME_POSITIVE, OUTCOME_NEGATIVE}:
        raise FactFamilyScoreError(f"case {case_id} expected_outcome 非法: {expected_outcome!r}")
    if expected_outcome == OUTCOME_POSITIVE and not expected_winner:
        raise FactFamilyScoreError(f"case {case_id} 正例缺少 expected_winner_document")
    if expected_outcome == OUTCOME_NEGATIVE and expected_winner:
        raise FactFamilyScoreError(f"case {case_id} no_proposal 不得有 expected winner")
    return {
        "case_id": case_id,
        "fact_family_id": fact_family_id,
        "split": split,
        "case_type": case_type,
        "expected_outcome": expected_outcome,
        "expected_winner_document": expected_winner,
        "scenario_path": str(resolve_path(str(case.get("scenario", record.get("scenario", ""))))),
        "scenario": scenario,
    }


def execution_row(record: dict[str, Any], case: dict[str, Any]) -> dict[str, Any]:
    context = context_for_case(case, record)
    # Use scenario documents only to recover source order/title metadata. Do
    # not duplicate the full scenario (and its potentially private source
    # paths) into every scorer output row.
    scenario = context.pop("scenario")
    detector_dir = Path(str(record.get("detector_dir", ""))).expanduser().resolve()
    detector_manifest = read_json(detector_dir / "manifest.json")
    metrics = read_json(detector_dir / "metrics.json")
    conflicts = read_json(detector_dir / "conflicts.json")
    facts = read_json(detector_dir / "disputed_facts.json")
    if not isinstance(detector_manifest, dict) or not isinstance(metrics, dict) or not isinstance(conflicts, list) or not isinstance(facts, list):
        raise FactFamilyScoreError(f"case {context['case_id']} detector artifact 类型异常: {detector_dir}")

    knowledge_ids = detector_manifest.get("knowledge_ids", {})
    if not isinstance(knowledge_ids, dict) or not knowledge_ids:
        raise FactFamilyScoreError(f"case {context['case_id']} detector manifest 缺少 knowledge_ids")
    upload_order = [str(document_id) for document_id in knowledge_ids]
    knowledge_to_document = {str(knowledge_id): str(document_id) for document_id, knowledge_id in knowledge_ids.items()}
    scenario_docs_raw = scenario.get("documents", [])
    if not isinstance(scenario_docs_raw, list):
        raise FactFamilyScoreError(f"case {context['case_id']} scenario documents 非数组")
    scenario_documents = {
        str(document.get("id", "")): document
        for document in scenario_docs_raw
        if isinstance(document, dict) and document.get("id")
    }
    if set(upload_order) != set(scenario_documents):
        raise FactFamilyScoreError(
            f"case {context['case_id']} scenario/manifest 文档集合不一致: "
            f"scenario={sorted(scenario_documents)} manifest={sorted(upload_order)}",
        )
    metadata, metadata_issues = metadata_from_conflicts(conflicts, knowledge_to_document, scenario_documents)
    predictions = {
        "c46_global_proposal": proposal_prediction(metrics),
        "latest_upload": upload_order[-1] if upload_order else "",
        "date_only": unique_strict_max(upload_order, metadata, compare_dates),
        "version_only": unique_strict_max(upload_order, metadata, compare_versions),
        "raw_c3_local_vote": raw_c3_vote_prediction(conflicts, knowledge_to_document),
    }
    evaluations = {
        method: evaluate_prediction(prediction, context["expected_outcome"], context["expected_winner_document"])
        for method, prediction in predictions.items()
    }
    return {
        **context,
        "replicate": as_int(record.get("replicate")),
        "automation_pass": bool(record.get("passed")),
        "automation_issues": list(record.get("issues", [])) if isinstance(record.get("issues"), list) else [],
        "detector_dir": str(detector_dir),
        "raw_conflict_count": as_int(metrics.get("conflict_count_total")),
        "disputed_fact_count": as_int(metrics.get("observed_disputed_fact_count")),
        "dead_letter_count": as_int(metrics.get("dead_letter_count")),
        "cascade": cascade_totals(metrics),
        "metadata": metadata,
        "metadata_issues": metadata_issues,
        "predictions": predictions,
        "evaluations": evaluations,
        "observed_winner_proposals": metrics.get("winner_proposals", []),
    }


def aggregate_execution_metrics(rows: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    result = {method: empty_counts() for method in METHODS}
    for row in rows:
        for method in METHODS:
            add_evaluation(result[method], row["evaluations"][method], row["predictions"][method])
    return {method: finish_counts(counts) for method, counts in result.items()}


def strict_family_evaluation(rows: list[dict[str, Any]], method: str) -> tuple[dict[str, Any], str, bool]:
    first = rows[0]
    expected_outcome = first["expected_outcome"]
    expected_winner = first["expected_winner_document"]
    predictions = [str(row["predictions"][method]) for row in rows]
    evaluations = [row["evaluations"][method] for row in rows]
    stable = len(set(predictions)) == 1
    if expected_outcome == OUTCOME_POSITIVE:
        if all(item.get("correct") for item in evaluations):
            return evaluate_prediction(expected_winner, expected_outcome, expected_winner), predictions[0], stable
        any_wrong_proposal = any(item.get("label") == "wrong_winner" for item in evaluations)
        return {
            # Preserve the publishable error taxonomy while exposing instability
            # independently in prediction_stable / prediction_sequences.
            "label": "wrong_winner" if any_wrong_proposal else "missed_winner",
            "correct": False,
            "tp": 0,
            "tn": 0,
            "fp": 1 if any_wrong_proposal else 0,
            "fn": 1,
            "unsafe": 0,
        }, (predictions[0] if stable else MULTIPLE_PREFIX + "unstable"), stable
    if all(item.get("correct") for item in evaluations):
        return evaluate_prediction("", expected_outcome, expected_winner), predictions[0], stable
    any_proposal = any(bool(prediction) for prediction in predictions)
    return {
        "label": "unstable_or_failed_negative",
        "correct": False,
        "tp": 0,
        "tn": 0,
        "fp": 1 if any_proposal else 0,
        "fn": 0,
        "unsafe": 1 if any_proposal else 0,
    }, (predictions[0] if stable else MULTIPLE_PREFIX + "unstable"), stable


def aggregate_fact_family_rows(execution_rows: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], dict[str, dict[str, Any]]]:
    groups: dict[str, list[dict[str, Any]]] = collections.defaultdict(list)
    for row in execution_rows:
        groups[str(row["fact_family_id"])].append(row)
    family_rows: list[dict[str, Any]] = []
    counts = {method: empty_counts() for method in METHODS}
    for family_id, rows in sorted(groups.items()):
        rows.sort(key=lambda item: item["replicate"])
        first = rows[0]
        for row in rows[1:]:
            for field in ("case_id", "split", "case_type", "expected_outcome", "expected_winner_document"):
                if row[field] != first[field]:
                    raise FactFamilyScoreError(f"fact_family_id {family_id} 的 {field} 在 replicate 间不一致")
        predictions: dict[str, str] = {}
        evaluations: dict[str, dict[str, Any]] = {}
        stable: dict[str, bool] = {}
        sequences: dict[str, list[str]] = {}
        for method in METHODS:
            evaluation, prediction, is_stable = strict_family_evaluation(rows, method)
            predictions[method] = prediction
            evaluations[method] = evaluation
            stable[method] = is_stable
            sequences[method] = [str(row["predictions"][method]) for row in rows]
            add_evaluation(counts[method], evaluation, prediction)
        family_rows.append({
            "fact_family_id": family_id,
            "case_id": first["case_id"],
            "split": first["split"],
            "case_type": first["case_type"],
            "expected_outcome": first["expected_outcome"],
            "expected_winner_document": first["expected_winner_document"],
            "replicate_count": len(rows),
            "all_automation_pass": all(row["automation_pass"] for row in rows),
            "raw_conflict_count_min": min(row["raw_conflict_count"] for row in rows),
            "raw_conflict_count_max": max(row["raw_conflict_count"] for row in rows),
            "disputed_fact_count_min": min(row["disputed_fact_count"] for row in rows),
            "disputed_fact_count_max": max(row["disputed_fact_count"] for row in rows),
            "dead_letter_count_total": sum(row["dead_letter_count"] for row in rows),
            "predictions": predictions,
            "prediction_sequences": sequences,
            "prediction_stable": stable,
            "evaluations": evaluations,
        })
    return family_rows, {method: finish_counts(item) for method, item in counts.items()}


def split_metrics(execution_rows: list[dict[str, Any]], family_rows: list[dict[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for split in ("all", "development", "holdout", "unspecified"):
        selected_exec = execution_rows if split == "all" else [
            row for row in execution_rows
            if (row["split"] or "unspecified") == split
        ]
        selected_families = family_rows if split == "all" else [
            row for row in family_rows
            if (row["split"] or "unspecified") == split
        ]
        if not selected_exec:
            continue
        stability = {
            method: {
                "stable_fact_families": sum(1 for row in selected_families if row["prediction_stable"][method]),
                "fact_families": len(selected_families),
                "all_replicate_prediction_stable": all(row["prediction_stable"][method] for row in selected_families),
            }
            for method in METHODS
        }
        for item in stability.values():
            item["rate"] = ratio(item["stable_fact_families"], item["fact_families"])
        result[split] = {
            "execution_count": len(selected_exec),
            "fact_family_count": len(selected_families),
            "execution_level": aggregate_execution_metrics(selected_exec),
            "fact_family_strict_all_replicates": aggregate_fact_family_rows(selected_exec)[1],
            "replicate_stability": stability,
        }
    return result


def aggregate_cost(execution_rows: list[dict[str, Any]]) -> dict[str, Any]:
    fields = tuple(next(iter(execution_rows), {}).get("cascade", {}).keys())
    totals = {field: sum(as_int(row["cascade"].get(field)) for row in execution_rows) for field in fields}
    return {
        "execution_count": len(execution_rows),
        "totals": totals,
        "mean_per_execution": {
            field: round(value / len(execution_rows), 6) if execution_rows else None
            for field, value in totals.items()
        },
    }


def aggregate_cluster_shape(execution_rows: list[dict[str, Any]]) -> dict[str, Any]:
    raw = [row["raw_conflict_count"] for row in execution_rows]
    facts = [row["disputed_fact_count"] for row in execution_rows]
    dead_letters = [row["dead_letter_count"] for row in execution_rows]
    return {
        "execution_count": len(execution_rows),
        "raw_conflict_count": {
            "min": min(raw) if raw else None,
            "max": max(raw) if raw else None,
            "mean": round(mean(raw), 6) if raw else None,
            "total": sum(raw),
        },
        "disputed_fact_count": {
            "min": min(facts) if facts else None,
            "max": max(facts) if facts else None,
            "mean": round(mean(facts), 6) if facts else None,
            "total": sum(facts),
        },
        "raw_per_disputed_fact": round(sum(raw) / sum(facts), 6) if sum(facts) else None,
        "dead_letter_count": {
            "total": sum(dead_letters),
            "max": max(dead_letters) if dead_letters else None,
            "all_zero": all(value == 0 for value in dead_letters),
        },
    }


def write_execution_csv(path: Path, rows: list[dict[str, Any]]) -> None:
    fields = [
        "fact_family_id", "case_id", "split", "case_type", "replicate", "expected_outcome",
        "expected_winner_document", "automation_pass", "raw_conflict_count", "disputed_fact_count",
        "dead_letter_count", "detector_dir",
    ]
    for method in METHODS:
        fields.extend((f"{method}_prediction", f"{method}_label", f"{method}_correct"))
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for item in rows:
            row = {
                field: item.get(field, "")
                for field in fields[:12]
            }
            for method in METHODS:
                evaluation = item["evaluations"][method]
                row[f"{method}_prediction"] = item["predictions"][method]
                row[f"{method}_label"] = evaluation["label"]
                row[f"{method}_correct"] = "yes" if evaluation["correct"] else "no"
            writer.writerow(row)


def write_fact_family_csv(path: Path, rows: list[dict[str, Any]]) -> None:
    fields = [
        "fact_family_id", "case_id", "split", "case_type", "expected_outcome", "expected_winner_document",
        "replicate_count", "all_automation_pass", "raw_conflict_count_min", "raw_conflict_count_max",
        "disputed_fact_count_min", "disputed_fact_count_max", "dead_letter_count_total",
    ]
    for method in METHODS:
        fields.extend((
            f"{method}_prediction", f"{method}_prediction_sequence", f"{method}_stable",
            f"{method}_label", f"{method}_correct",
        ))
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for item in rows:
            row = {field: item.get(field, "") for field in fields[:13]}
            row["all_automation_pass"] = "yes" if item["all_automation_pass"] else "no"
            for method in METHODS:
                evaluation = item["evaluations"][method]
                row[f"{method}_prediction"] = item["predictions"][method]
                row[f"{method}_prediction_sequence"] = ";".join(item["prediction_sequences"][method])
                row[f"{method}_stable"] = "yes" if item["prediction_stable"][method] else "no"
                row[f"{method}_label"] = evaluation["label"]
                row[f"{method}_correct"] = "yes" if evaluation["correct"] else "no"
            writer.writerow(row)


def metric_table_lines(metrics: dict[str, dict[str, Any]]) -> list[str]:
    lines = [
        "| Method | N | TP | TN | FP | FN | Precision | Recall | Policy accuracy | Unsafe actions | Abstentions |",
        "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for method in METHODS:
        item = metrics[method]
        percent = lambda value: "-" if value is None else f"{float(value):.3f}"
        lines.append(
            f"| {METHOD_LABELS[method]} | {item['samples']} | {item['tp']} | {item['tn']} | {item['fp']} | {item['fn']} | "
            f"{percent(item['precision'])} | {percent(item['recall'])} | {percent(item['policy_accuracy'])} | "
            f"{item['unsafe_action_count']} | {item['abstentions']} |"
        )
    return lines


def write_report(path: Path, result: dict[str, Any]) -> None:
    all_metrics = result["metrics_by_split"].get("all", {})
    lines = [
        "# C4.10 fact-family winner policy evaluation", "",
        f"- Matrix run: `{result['matrix_run']}`",
        f"- Matrix: `{result['matrix_name']}`",
        f"- Input execution rows: `{result['execution_count']}`",
        f"- Fact families: `{result['fact_family_count']}`",
        f"- Split filter: `{result['split_filter']}`",
        f"- Matrix records all passing: `{result['all_automation_pass']}`", "",
        "## Input integrity", "", "```json",
        json.dumps(result.get("input_integrity", {}), ensure_ascii=False, indent=2), "```", "",
        "## Policy definition", "",
        "- Positive (`adopt_reopen`): only the expected document is a correct proposal.",
        "- Negative (`no_proposal`): abstention is correct; any proposal is counted as an unsafe action.",
        "- A wrong positive winner counts as both FP and FN; a missing positive winner counts as FN.",
        "- Fact-family rows use a strict replicate policy: all replicates must be correct for the family to count as correct.",
        "", "## Baseline definitions", "",
        "| Method | Rule |",
        "|---|---|",
        "| C4.6 global proposal | Observed `winner_proposals` from the detector artifact. |",
        "| Latest upload | Always selects the last document ingested by the scenario. |",
        "| Date only | Unique strict maximum effective-date interval; ignores issuer and version. |",
        "| Version only | Unique strict maximum numeric version; ignores issuer and date. |",
        "| Raw C3 local vote | One vote from each directional raw C3 suggestion; tie/no vote abstains. |",
        "", "## Execution-level controlled policy metrics", "",
    ]
    if all_metrics:
        lines.extend(metric_table_lines(all_metrics["execution_level"]))
    lines += ["", "## Fact-family strict-all-replicates metrics", ""]
    if all_metrics:
        lines.extend(metric_table_lines(all_metrics["fact_family_strict_all_replicates"]))
    lines += ["", "## Replicate stability", "", "| Method | Stable fact families | Total | Rate |", "|---|---:|---:|---:|"]
    if all_metrics:
        for method in METHODS:
            stability = all_metrics["replicate_stability"][method]
            rate = stability["rate"]
            lines.append(
                f"| {METHOD_LABELS[method]} | {stability['stable_fact_families']} | {stability['fact_families']} | "
                f"{'-' if rate is None else f'{float(rate):.3f}'} |"
            )
    shape = result["cluster_shape"]
    lines += [
        "", "## Detector / cluster integrity", "",
        "```json", json.dumps(shape, ensure_ascii=False, indent=2), "```", "",
        "## Cascade-cost aggregate", "", "```json", json.dumps(result["cascade_cost"], ensure_ascii=False, indent=2), "```", "",
        "## Interpretation boundary", "",
        "This report scores controlled policy labels at fact-family granularity. It does not convert a synthetic or unreviewed input corpus into real-document generalization, human-review accuracy, or provider-seed-controlled evidence. The date/version/latest/C3-vote rows are intentionally simple baselines, not claims that their policy semantics are authoritative.",
        "",
    ]
    path.write_text("\n".join(lines), encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Score C4.6 and simple winner baselines from a completed C4.9 matrix at fact-family granularity.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--matrix-run", required=True, help="C4.9 comparison directory containing matrix_manifest.json/results")
    parser.add_argument("--output-dir", default="", help="Defaults to <matrix-run>/fact_family_evaluation")
    parser.add_argument("--split", choices=sorted(VALID_SPLITS), default="all", help="Score all/development/holdout rows")
    parser.add_argument("--allow-failed", action="store_true", help="Export diagnostics even when matrix records failed")
    parser.add_argument("--overwrite", action="store_true", help="Allow a non-empty output directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        matrix_run = Path(args.matrix_run).expanduser().resolve()
        matrix_manifest = read_json(matrix_run / "matrix_manifest.json")
        matrix_results = read_json(matrix_run / "matrix_results.json")
        if not isinstance(matrix_manifest, dict) or not isinstance(matrix_results, list):
            raise FactFamilyScoreError("matrix_manifest.json 或 matrix_results.json 类型异常")
        matrix = matrix_manifest.get("matrix")
        if not isinstance(matrix, dict) or not isinstance(matrix.get("cases"), list):
            raise FactFamilyScoreError("matrix_manifest 缺少 matrix.cases")
        cases = {str(item.get("id", "")): item for item in matrix["cases"] if isinstance(item, dict) and item.get("id")}
        if not cases:
            raise FactFamilyScoreError("matrix_manifest.matrix.cases 为空")
        missing_cases = sorted({
            str(row.get("case_id", "")) if isinstance(row, dict) else "<non-object>"
            for row in matrix_results
            if not isinstance(row, dict) or str(row.get("case_id", "")) not in cases
        })
        if missing_cases:
            raise FactFamilyScoreError("matrix_results 含 matrix 未定义 case: " + ", ".join(missing_cases))
        selected_case_ids = {
            case_id for case_id, case in cases.items()
            if args.split == "all" or str(case.get("split", "")).strip() == args.split
        }
        if not selected_case_ids:
            raise FactFamilyScoreError(f"matrix 中没有 split={args.split} 的 case；legacy matrix 没有 split metadata 时请用 --split all")
        selected_records = [
            row for row in matrix_results
            if isinstance(row, dict) and str(row.get("case_id", "")) in selected_case_ids
        ]
        requested_replicates = as_int(matrix_manifest.get("replicates"))
        input_issues: list[str] = []
        if requested_replicates > 0:
            expected_execution_keys = {
                (case_id, replicate)
                for case_id in selected_case_ids
                for replicate in range(1, requested_replicates + 1)
            }
            observed_execution_keys = [
                (str(row.get("case_id", "")), as_int(row.get("replicate")))
                for row in selected_records
            ]
            observed_set = set(observed_execution_keys)
            missing_execution_keys = sorted(expected_execution_keys - observed_set)
            duplicate_execution_keys = sorted(
                key for key, count in collections.Counter(observed_execution_keys).items() if count > 1
            )
            unexpected_execution_keys = sorted(observed_set - expected_execution_keys)
            if missing_execution_keys:
                input_issues.append("missing executions: " + ", ".join(f"{case}#r{rep}" for case, rep in missing_execution_keys[:10]))
            if duplicate_execution_keys:
                input_issues.append("duplicate executions: " + ", ".join(f"{case}#r{rep}" for case, rep in duplicate_execution_keys[:10]))
            if unexpected_execution_keys:
                input_issues.append("unexpected executions: " + ", ".join(f"{case}#r{rep}" for case, rep in unexpected_execution_keys[:10]))
        else:
            expected_execution_keys = set()
            missing_execution_keys = []
            duplicate_execution_keys = []
            unexpected_execution_keys = []
        failed = [row for row in selected_records if not row.get("passed")]
        if failed:
            labels = ", ".join(f"{row.get('case_id', '?')}#r{row.get('replicate', '?')}" for row in failed[:10])
            input_issues.append(f"failed executions: {labels}")
        if input_issues and not args.allow_failed:
            raise FactFamilyScoreError(
                "matrix input 不完整或含失败 execution（" + " | ".join(input_issues) +
                "）；先修复，或显式传 --allow-failed 导出诊断。",
            )

        rows: list[dict[str, Any]] = []
        for record in selected_records:
            case_id = str(record.get("case_id", ""))
            row = execution_row(record, cases[case_id])
            # A matrix case's split is authoritative. The scenario fallback is
            # retained only for legacy matrices that have no split field.
            if args.split != "all" and row["split"] != args.split:
                continue
            rows.append(row)
        if not rows:
            raise FactFamilyScoreError(f"按 split={args.split} 过滤后没有 execution rows")

        input_integrity = {
            "requested_replicates": requested_replicates or None,
            "selected_case_definitions": len(selected_case_ids),
            "expected_execution_count": len(expected_execution_keys) if requested_replicates > 0 else None,
            "observed_execution_count": len(selected_records),
            "missing_execution_count": len(missing_execution_keys),
            "duplicate_execution_count": len(duplicate_execution_keys),
            "unexpected_execution_count": len(unexpected_execution_keys),
            "failed_execution_count": len(failed),
            "issues": input_issues,
        }

        output = Path(args.output_dir).expanduser().resolve() if args.output_dir else matrix_run / "fact_family_evaluation"
        if output.exists() and any(output.iterdir()) and not args.overwrite:
            raise FactFamilyScoreError(f"输出目录已存在且非空: {output}（需要 --overwrite）")
        output.mkdir(parents=True, exist_ok=True)
        family_rows, family_metrics = aggregate_fact_family_rows(rows)
        metrics_by_split = split_metrics(rows, family_rows)
        result = {
            "schema_version": 1,
            "matrix_run": str(matrix_run),
            "matrix_name": str(matrix.get("name", "")),
            "split_filter": args.split,
            "execution_count": len(rows),
            "fact_family_count": len(family_rows),
            "all_automation_pass": all(row["automation_pass"] for row in rows),
            "failed_execution_count_in_input": len(failed),
            "input_integrity": input_integrity,
            "methods": {method: METHOD_LABELS[method] for method in METHODS},
            "metrics_by_split": metrics_by_split,
            "fact_family_strict_all_replicates": family_metrics,
            "cluster_shape": aggregate_cluster_shape(rows),
            "cascade_cost": aggregate_cost(rows),
            "note": (
                "Controlled fact-family policy evaluation only. Does not establish real-corpus, human-review, "
                "or provider-seed-controlled accuracy."
            ),
        }
        json_dump(output / "baseline_policy_metrics.json", result)
        json_dump(output / "execution_results.json", rows)
        json_dump(output / "fact_family_results.json", family_rows)
        write_execution_csv(output / "execution_results.csv", rows)
        write_fact_family_csv(output / "fact_family_results.csv", family_rows)
        write_report(output / "report.md", result)

        all_fact_metrics = metrics_by_split["all"]["fact_family_strict_all_replicates"]
        print(f"C4.10 fact-family evaluation complete: {output}")
        print(f"  executions / fact families: {len(rows)} / {len(family_rows)}")
        for method in METHODS:
            item = all_fact_metrics[method]
            print(
                f"  {method}: P={item['precision']} R={item['recall']} "
                f"accuracy={item['policy_accuracy']} unsafe={item['unsafe_action_count']}",
            )
        return 0 if not failed or args.allow_failed else 2
    except FactFamilyScoreError as exc:
        print(f"[c4.10-fact-eval] FAILED: {exc}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("[c4.10-fact-eval] interrupted", file=sys.stderr)
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
