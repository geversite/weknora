#!/usr/bin/env python3
"""Export a human-review CSV for native DocReader disputed facts.

Unit of review is the DisputedFact (tens of rows), not every claim.
Output stays in a private directory. This does not score accuracy until a
reviewer fills the blank columns. Recall cannot be estimated from this CSV
alone because missed conflicts are not enumerated.
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from pathlib import Path
from typing import Any

from summarize_native_corpus_eval import knowledge_id_from_ref, parse_json_list


class ExportError(RuntimeError):
    """The review CSV cannot be exported."""


REVIEW_COLUMNS = (
    "same_fact",
    "human_conflict",
    "cluster_quality",
    "reviewer_id",
    "notes",
)


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ExportError(f"missing JSON: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ExportError(f"invalid JSON: {path}: {exc}") from exc


def source_names(fact: dict[str, Any], kid_to_name: dict[str, str]) -> str:
    names = []
    for ref in parse_json_list(fact.get("source_refs")):
        kid = knowledge_id_from_ref(ref)
        names.append(kid_to_name.get(kid, kid))
    return " | ".join(names)


def values_text(fact: dict[str, Any]) -> str:
    values = [str(item) for item in parse_json_list(fact.get("candidate_values"))]
    return " | ".join(values)


def kid_to_filename(run_dir: Path) -> dict[str, str]:
    manifest = read_json(run_dir / "manifest.json")
    if not isinstance(manifest, dict):
        return {}
    knowledge_ids = manifest.get("knowledge_ids") or {}
    names: dict[str, str] = {}
    scenario_path = Path(str(manifest.get("scenario_path") or ""))
    doc_to_name: dict[str, str] = {}
    if scenario_path.is_file():
        scenario = read_json(scenario_path)
        for document in scenario.get("documents") or []:
            if isinstance(document, dict) and document.get("id"):
                doc_to_name[str(document["id"])] = str(
                    document.get("original_filename") or document.get("source_relative_path") or document["id"]
                )
    for doc_id, knowledge_id in knowledge_ids.items():
        names[str(knowledge_id)] = doc_to_name.get(str(doc_id), str(doc_id))
    return names


def rows_for_run(run_dir: Path, batch_id: str) -> list[dict[str, str]]:
    facts_path = run_dir / "disputed_facts.json"
    if not facts_path.is_file():
        return []
    facts = read_json(facts_path)
    if not isinstance(facts, list):
        raise ExportError(f"{facts_path} root must be an array")
    names = kid_to_filename(run_dir)
    rows = []
    for index, fact in enumerate(facts, start=1):
        if not isinstance(fact, dict):
            continue
        kind = str(fact.get("anchor_kind") or "")
        key = str(fact.get("fact_key") or "")
        if not kind:
            kind = key.split(":", 1)[0] if ":" in key else ""
        rows.append({
            "review_id": f"{batch_id}-{index:03d}",
            "batch_id": batch_id,
            "fact_id": str(fact.get("id") or ""),
            "anchor_kind": kind,
            "fact_key": key[:200],
            "subject": str(fact.get("subject") or "")[:120],
            "predicate": str(fact.get("predicate") or "")[:120],
            "source_count": str(fact.get("source_count") or ""),
            "conflict_count": str(fact.get("conflict_count") or ""),
            "candidate_values": values_text(fact)[:500],
            "source_files": source_names(fact, names)[:500],
            "system_conflict": "yes",
            "same_fact": "",
            "human_conflict": "",
            "cluster_quality": "",
            "reviewer_id": "",
            "notes": "",
        })
    return rows


def write_readme(path: Path) -> None:
    path.write_text(
        """# Native disputed-fact review

Fill only these columns:

```text
same_fact        yes | no | uncertain
human_conflict   conflict | no_conflict | not_comparable | uncertain
cluster_quality  good | overmerged | undermerged | uncertain
reviewer_id      your id
notes            optional
```

Guidance:

- same_fact=yes: the values are about one fact.
- human_conflict=conflict: values genuinely disagree.
- not_comparable: different institutions/products, not a version fight.
- Do not paste filled quotes back into chat.

After filling, score locally:

```bash
python3 scripts/experiments/score_native_review_csv.py --review <this.csv>
```

This estimates precision of system disputed facts among same-fact rows.
It cannot estimate recall.
""",
        encoding="utf-8",
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Export a blank human-review CSV for native disputed facts.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--runs-dir", required=True, help="Directory containing batch_001, batch_002, ...")
    parser.add_argument("--output", required=True, help="Private CSV path outside Git")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        runs_dir = Path(args.runs_dir).expanduser().resolve()
        if not runs_dir.is_dir():
            raise ExportError(f"runs dir missing: {runs_dir}")
        batches = sorted(path for path in runs_dir.glob("batch_*") if path.is_dir())
        if not batches:
            raise ExportError(f"no batch_* directories in {runs_dir}")
        rows: list[dict[str, str]] = []
        for batch_dir in batches:
            rows.extend(rows_for_run(batch_dir, batch_dir.name))
        if not rows:
            raise ExportError("no disputed_facts.json rows found")
        output = Path(args.output).expanduser().resolve()
        output.parent.mkdir(parents=True, exist_ok=True)
        fieldnames = [
            "review_id", "batch_id", "fact_id", "anchor_kind", "fact_key",
            "subject", "predicate", "source_count", "conflict_count",
            "candidate_values", "source_files", "system_conflict",
            *REVIEW_COLUMNS,
        ]
        with output.open("w", encoding="utf-8", newline="") as handle:
            writer = csv.DictWriter(handle, fieldnames=fieldnames)
            writer.writeheader()
            writer.writerows(rows)
        write_readme(output.with_suffix(".README.md"))
        print(f"Native review CSV: {output}")
        print(f"  rows: {len(rows)}")
        print("  fill same_fact / human_conflict / cluster_quality; do not commit this file")
        return 0
    except ExportError as exc:
        print(f"[native-review-export] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
