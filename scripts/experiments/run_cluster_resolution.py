#!/usr/bin/env python3
"""Exercise one C4.5 safe DisputedFact resolution through the public API.

The script reads an existing C4 experiment artifact to identify its temporary
KB and one pending cluster, calls the cluster-level resolve endpoint, then uses
read-only PostgreSQL queries to verify every pending member was updated. It
never writes database rows directly and never prints credentials.

Typical usage:

  python3 scripts/experiments/run_cluster_resolution.py \
    --run-dir experiments/runs/<c4_cluster_triplet-run> \
    --resolution resolved_keep_both

Only resolved_keep_both and resolved_not_conflict are accepted by C4.5. Global
newer/older-wins propagation deliberately waits for C3 authority semantics.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from typing import Any

from run_claims_eval import APIClient, ExperimentError, PostgresExporter, json_dump, sql_literal


SAFE_RESOLUTIONS = {
    "resolved_keep_both",
    "resolved_not_conflict",
}


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ExperimentError(f"找不到实验产物: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ExperimentError(f"JSON 无法解析: {path}: {exc}") from exc


def resolve_run_dir(raw: str) -> Path:
    path = Path(raw).expanduser().resolve()
    if not path.is_dir():
        raise ExperimentError(f"run 目录不存在: {path}")
    for name in ("manifest.json", "disputed_facts.json"):
        if not (path / name).is_file():
            raise ExperimentError(f"run 目录缺少 {name}: {path}")
    return path


def pending_cluster_id(run_dir: Path, explicit_id: str) -> str:
    if explicit_id:
        return explicit_id
    facts = load_json(run_dir / "disputed_facts.json")
    if not isinstance(facts, list):
        raise ExperimentError("disputed_facts.json 根节点必须是数组")
    pending = [
        item for item in facts
        if isinstance(item, dict) and str(item.get("status", "")) == "pending" and item.get("id")
    ]
    if len(pending) != 1:
        raise ExperimentError(
            "未指定 --cluster-id 时，run 中必须恰好有一个 pending DisputedFact；"
            f"当前为 {len(pending)} 个"
        )
    return str(pending[0]["id"])


def query_members(db: PostgresExporter, kb_id: str, cluster_id: str) -> list[dict[str, str]]:
    return db.query(
        "SELECT id, cluster_id, fact_key, status, resolved_by, resolution_note, resolved_at "
        "FROM knowledge_conflicts "
        f"WHERE knowledge_base_id = {sql_literal(kb_id)} AND cluster_id = {sql_literal(cluster_id)} "
        "ORDER BY created_at, id"
    )


def query_fact(db: PostgresExporter, kb_id: str, cluster_id: str) -> dict[str, str]:
    rows = db.query(
        "SELECT id, status, conflict_count, pending_conflict_count, source_count, candidate_values, source_refs "
        "FROM disputed_facts "
        f"WHERE knowledge_base_id = {sql_literal(kb_id)} AND id = {sql_literal(cluster_id)}"
    )
    if len(rows) != 1:
        raise ExperimentError(f"未找到唯一 DisputedFact {cluster_id}，查询结果={len(rows)}")
    return rows[0]


def run_resolution(args: argparse.Namespace) -> int:
    run_dir = resolve_run_dir(args.run_dir)
    manifest = load_json(run_dir / "manifest.json")
    if not isinstance(manifest, dict):
        raise ExperimentError("manifest.json 根节点必须是对象")
    kb_id = str(manifest.get("knowledge_base_id", ""))
    if not kb_id:
        raise ExperimentError("manifest.json 缺少 knowledge_base_id")
    if args.resolution not in SAFE_RESOLUTIONS:
        raise ExperimentError("C4.5 只允许 resolved_keep_both 或 resolved_not_conflict")

    cluster_id = pending_cluster_id(run_dir, args.cluster_id)
    client = APIClient(args.base_url, os.environ.get("WEKNORA_API_KEY"))
    db = PostgresExporter()
    db.check()
    before = query_members(db, kb_id, cluster_id)
    pending_before = [row for row in before if row.get("status") == "pending"]
    if not pending_before:
        raise ExperimentError(f"cluster {cluster_id} 没有 pending raw conflict，拒绝重复实验")

    result = client.post(
        f"/knowledge-bases/{kb_id}/conflicts/clusters/resolve",
        {
            "disputed_fact_id": cluster_id,
            "resolution": args.resolution,
            "note": args.note,
        },
    )
    if not isinstance(result, dict):
        raise ExperimentError("cluster resolve API 响应格式异常")

    after = query_members(db, kb_id, cluster_id)
    fact = query_fact(db, kb_id, cluster_id)
    expected_ids = {row.get("id", "") for row in pending_before}
    updated_ids = {str(item) for item in result.get("updated_conflict_ids", [])}
    if expected_ids != updated_ids:
        raise ExperimentError(
            "cluster resolve 更新成员不完整："
            f"expected={sorted(expected_ids)} actual={sorted(updated_ids)}"
        )
    after_by_id = {row.get("id", ""): row for row in after}
    wrong_status = [
        conflict_id for conflict_id in expected_ids
        if after_by_id.get(conflict_id, {}).get("status") != args.resolution
    ]
    if wrong_status:
        raise ExperimentError(f"cluster resolve 后仍有已处理成员状态不一致: {wrong_status}")
    if fact.get("status") != "resolved" or str(fact.get("pending_conflict_count", "")) not in {"0", "0.0"}:
        raise ExperimentError(f"cluster rebuild 后 DisputedFact 状态不正确: {fact}")

    artifact = {
        "schema_version": 1,
        "run_dir": str(run_dir),
        "knowledge_base_id": kb_id,
        "disputed_fact_id": cluster_id,
        "resolution": args.resolution,
        "note": args.note,
        "members_before": before,
        "api_result": result,
        "members_after": after,
        "disputed_fact_after": fact,
    }
    output = Path(args.output).expanduser().resolve() if args.output else run_dir / "cluster_resolution.json"
    json_dump(output, artifact)
    print(f"C4.5 cluster resolution verified: {output}")
    print(f"  KB: {kb_id}")
    print(f"  disputed fact: {cluster_id}")
    print(f"  resolution: {args.resolution}")
    print(f"  updated raw conflicts: {len(updated_ids)}")
    return 0


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Resolve one C4.5 DisputedFact safely and verify member propagation.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--run-dir", required=True, help="Existing C4 experiment run directory")
    parser.add_argument("--cluster-id", default="", help="Optional explicit DisputedFact ID; default requires exactly one pending cluster")
    parser.add_argument("--resolution", choices=sorted(SAFE_RESOLUTIONS), default="resolved_keep_both")
    parser.add_argument("--note", default="C4.5 script-driven safe cluster resolution")
    parser.add_argument("--output", default="", help="Output JSON path; defaults to <run-dir>/cluster_resolution.json")
    parser.add_argument("--base-url", default=os.environ.get("WEKNORA_BASE_URL", "http://127.0.0.1:8080"))
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        return run_resolution(args)
    except ExperimentError as exc:
        print(f"[cluster-resolution] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
