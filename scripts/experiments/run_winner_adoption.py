#!/usr/bin/env python3
"""Exercise C4.7 explicit global-winner adoption through the public API.

The script starts from an existing C4.6 experiment artifact, obtains the live
DisputedFact through the public clusters API, and echoes its current optimistic
proposal snapshot to POST /clusters/adopt-winner. It never writes PostgreSQL
directly: PostgreSQL is read only and used only to verify the propagated raw
members and chunk enable state.

Positive usage:

  python3 scripts/experiments/run_winner_adoption.py \
    --run-dir experiments/runs/<fresh-c46_global_winner_triplet-run>

Negative (cross-issuer/no-proposal) usage:

  python3 scripts/experiments/run_winner_adoption.py \
    --run-dir experiments/runs/<fresh-c46_cross_issuer_no_proposal-run> \
    --expect-no-proposal

The default positive path first submits a deliberately stale version token and
requires HTTP 409 with no mutation, then submits the exact live snapshot. This
makes stale-proposal rejection part of the reproducible research evidence.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from typing import Any

from run_claims_eval import (
    APIClient,
    ExperimentError,
    PostgresExporter,
    conflict_status_width_is_sufficient,
    disputed_fact_winner_adoptions_ready,
    disputed_fact_winner_proposals_ready,
    json_dump,
    sql_literal,
)


GLOBAL_WINNER_STATUS = "resolved_global_winner"


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


def as_int(value: Any) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def db_false(value: Any) -> bool:
    return str(value).strip().lower() in {"", "0", "f", "false"}


def live_pending_facts(client: APIClient, kb_id: str) -> list[dict[str, Any]]:
    data = client.get(f"/knowledge-bases/{kb_id}/conflicts/clusters?status=pending&page=1&page_size=200")
    if not isinstance(data, dict) or not isinstance(data.get("list"), list):
        raise ExperimentError("clusters API 响应格式异常")
    return [item for item in data["list"] if isinstance(item, dict)]


def select_live_fact(client: APIClient, kb_id: str, explicit_id: str, expect_no_proposal: bool) -> dict[str, Any]:
    facts = live_pending_facts(client, kb_id)
    if explicit_id:
        matching = [item for item in facts if str(item.get("id", "")) == explicit_id]
    elif expect_no_proposal:
        matching = [item for item in facts if not str(item.get("suggested_winner_knowledge_id", "")).strip()]
    else:
        matching = [item for item in facts if str(item.get("suggested_winner_knowledge_id", "")).strip()]
    if len(matching) != 1:
        kind = "无 proposal" if expect_no_proposal else "可采纳 proposal"
        raise ExperimentError(f"未指定 --cluster-id 时，run 中必须恰有一个 pending {kind} DisputedFact；当前为 {len(matching)} 个")
    fact = matching[0]
    if str(fact.get("status", "")) != "pending":
        raise ExperimentError(f"DisputedFact 不是 pending: {fact.get('id')}")
    if not str(fact.get("updated_at", "")).strip():
        raise ExperimentError("clusters API 未返回 updated_at，无法建立 optimistic adoption snapshot")
    return fact


def query_members(db: PostgresExporter, kb_id: str, cluster_id: str) -> list[dict[str, str]]:
    return db.query(
        "SELECT id, knowledge_id_a, knowledge_id_b, chunk_id_a, chunk_id_b, cluster_id, fact_key, "
        "status, auto_resolved, winner_adoption_id, resolved_by, resolved_at, resolution_note "
        "FROM knowledge_conflicts "
        f"WHERE knowledge_base_id = {sql_literal(kb_id)} AND cluster_id = {sql_literal(cluster_id)} "
        "ORDER BY created_at, id"
    )


def query_fact(db: PostgresExporter, kb_id: str, cluster_id: str) -> dict[str, str]:
    rows = db.query(
        "SELECT id, clusterer_version, status, conflict_count, pending_conflict_count, source_count, candidate_values, "
        "suggested_winner_knowledge_id, winner_proposal_confidence, winner_proposal_version, "
        "winner_proposal_source_count, active_winner_adoption_id, winner_proposal_reason, updated_at "
        "FROM disputed_facts "
        f"WHERE knowledge_base_id = {sql_literal(kb_id)} AND id = {sql_literal(cluster_id)}"
    )
    if len(rows) != 1:
        raise ExperimentError(f"未找到唯一 DisputedFact {cluster_id}，查询结果={len(rows)}")
    return rows[0]


def query_adoption(db: PostgresExporter, kb_id: str, adoption_id: str) -> dict[str, str]:
    rows = db.query(
        "SELECT id, disputed_fact_id, winner_knowledge_id, proposal_version, proposal_confidence, "
        "proposal_source_count, member_conflict_ids::text AS member_conflict_ids, "
        "disabled_chunk_ids::text AS disabled_chunk_ids, disabled_knowledge_ids::text AS disabled_knowledge_ids, "
        "status, adopted_by, adopted_at, adoption_note, revoked_by, revoked_at, revoke_note, updated_at "
        "FROM disputed_fact_winner_adoptions "
        f"WHERE knowledge_base_id = {sql_literal(kb_id)} AND id = {sql_literal(adoption_id)}"
    )
    if len(rows) != 1:
        raise ExperimentError(f"未找到唯一 winner adoption {adoption_id}，查询结果={len(rows)}")
    return rows[0]


def query_chunks(db: PostgresExporter, kb_id: str, chunk_ids: set[str]) -> list[dict[str, str]]:
    if not chunk_ids:
        return []
    ids = ", ".join(sql_literal(item) for item in sorted(chunk_ids))
    return db.query(
        "SELECT id, knowledge_id, is_enabled "
        "FROM chunks "
        f"WHERE knowledge_base_id = {sql_literal(kb_id)} AND id IN ({ids}) "
        "ORDER BY id"
    )


def member_snapshot(rows: list[dict[str, str]]) -> list[dict[str, str]]:
    return [
        {
            key: row.get(key, "")
            for key in (
                "id", "knowledge_id_a", "knowledge_id_b", "chunk_id_a", "chunk_id_b",
                "cluster_id", "status", "auto_resolved", "winner_adoption_id", "resolved_by", "resolved_at", "resolution_note",
            )
        }
        for row in rows
    ]


def chunk_snapshot(rows: list[dict[str, str]]) -> list[dict[str, str]]:
    return [{key: row.get(key, "") for key in ("id", "knowledge_id", "is_enabled")} for row in rows]


def expected_chunk_sets(members: list[dict[str, str]], winner_id: str) -> tuple[set[str], set[str], set[str]]:
    if not members:
        raise ExperimentError("cluster 没有 raw members")
    loser_chunks: set[str] = set()
    winner_chunks: set[str] = set()
    all_chunks: set[str] = set()
    sources: set[str] = set()
    for row in members:
        if row.get("status") != "pending":
            raise ExperimentError(f"adoption 前 raw member 非 pending: {row.get('id')}={row.get('status')}")
        if not db_false(row.get("auto_resolved")):
            raise ExperimentError(f"adoption 前 raw member auto_resolved 非 false: {row.get('id')}")
        for knowledge_key, chunk_key in (("knowledge_id_a", "chunk_id_a"), ("knowledge_id_b", "chunk_id_b")):
            knowledge_id = str(row.get(knowledge_key, "")).strip()
            chunk_id = str(row.get(chunk_key, "")).strip()
            if not knowledge_id or not chunk_id:
                raise ExperimentError(f"raw member {row.get('id')} 缺少 knowledge/chunk，C4.7 应保守拒绝")
            sources.add(knowledge_id)
            all_chunks.add(chunk_id)
            if knowledge_id == winner_id:
                winner_chunks.add(chunk_id)
            else:
                loser_chunks.add(chunk_id)
    if winner_id not in sources:
        raise ExperimentError("live proposal winner 不在 cluster raw member sources 中")
    if not loser_chunks:
        raise ExperimentError("cluster 没有可禁用的 loser chunks")
    return loser_chunks, winner_chunks, all_chunks


def payload_for_live_fact(fact: dict[str, Any], note: str) -> dict[str, Any]:
    winner_id = str(fact.get("suggested_winner_knowledge_id", "")).strip()
    version = str(fact.get("winner_proposal_version", "")).strip()
    updated_at = str(fact.get("updated_at", "")).strip()
    source_count = as_int(fact.get("winner_proposal_source_count"))
    if not winner_id or not version or not updated_at or source_count < 2:
        raise ExperimentError("live DisputedFact 没有完整、可采纳的 C4.6 proposal")
    return {
        "disputed_fact_id": str(fact.get("id", "")),
        "expected_winner_knowledge_id": winner_id,
        "expected_proposal_version": version,
        "expected_proposal_updated_at": updated_at,
        "expected_proposal_source_count": source_count,
        "note": note,
    }


def require_http_conflict(action: str, callback: Any) -> str:
    try:
        callback()
    except ExperimentError as exc:
        message = str(exc)
        if "HTTP 409:" not in message:
            raise ExperimentError(f"{action} 应返回 HTTP 409，实际为: {message}") from exc
        return message
    raise ExperimentError(f"{action} 意外成功，拒绝继续，以免把失败保护误记为通过")


def verify_unchanged(
    db: PostgresExporter,
    kb_id: str,
    cluster_id: str,
    before_members: list[dict[str, str]],
    before_chunks: list[dict[str, str]],
) -> None:
    after_members = query_members(db, kb_id, cluster_id)
    chunk_ids = {row.get("id", "") for row in before_chunks if row.get("id")}
    after_chunks = query_chunks(db, kb_id, chunk_ids)
    if member_snapshot(after_members) != member_snapshot(before_members):
        raise ExperimentError("被拒绝的 adoption 请求改变了 raw members")
    if chunk_snapshot(after_chunks) != chunk_snapshot(before_chunks):
        raise ExperimentError("被拒绝的 adoption 请求改变了 chunks")


def run_positive(args: argparse.Namespace, run_dir: Path, kb_id: str, client: APIClient, db: PostgresExporter) -> int:
    fact = select_live_fact(client, kb_id, args.cluster_id, expect_no_proposal=False)
    cluster_id = str(fact["id"])
    payload = payload_for_live_fact(fact, args.note)
    winner_id = payload["expected_winner_knowledge_id"]

    before_members = query_members(db, kb_id, cluster_id)
    loser_chunks, winner_chunks, all_chunks = expected_chunk_sets(before_members, winner_id)
    before_chunks = query_chunks(db, kb_id, all_chunks)
    if len(before_chunks) != len(all_chunks):
        raise ExperimentError("adoption 前无法读取完整的 member chunks")
    if any(db_false(row.get("is_enabled")) for row in before_chunks):
        raise ExperimentError("adoption 前存在 disabled member chunk；请用 fresh C4.6 run 进行实验")

    stale_rejection = ""
    if not args.skip_stale_guard:
        stale_payload = dict(payload)
        stale_payload["expected_proposal_version"] = payload["expected_proposal_version"] + "-stale"
        stale_rejection = require_http_conflict(
            "stale winner proposal adoption",
            lambda: client.post(f"/knowledge-bases/{kb_id}/conflicts/clusters/adopt-winner", stale_payload),
        )
        verify_unchanged(db, kb_id, cluster_id, before_members, before_chunks)

    result = client.post(f"/knowledge-bases/{kb_id}/conflicts/clusters/adopt-winner", payload)
    if not isinstance(result, dict):
        raise ExperimentError("winner adoption API 响应格式异常")

    after_members = query_members(db, kb_id, cluster_id)
    after_chunks = query_chunks(db, kb_id, all_chunks)
    after_fact = query_fact(db, kb_id, cluster_id)
    expected_member_ids = {row.get("id", "") for row in before_members}
    result_member_ids = {str(item) for item in result.get("updated_conflict_ids", [])}
    if expected_member_ids != result_member_ids:
        raise ExperimentError(
            f"winner adoption 更新成员不完整：expected={sorted(expected_member_ids)} actual={sorted(result_member_ids)}"
        )
    if result.get("resolution") != GLOBAL_WINNER_STATUS or result.get("winner_knowledge_id") != winner_id:
        raise ExperimentError(f"winner adoption API 返回的 resolution/winner 异常: {result}")
    if str(result.get("proposal_version", "")) != payload["expected_proposal_version"]:
        raise ExperimentError("winner adoption API 返回 proposal_version 与已审阅快照不一致")
    if as_int(result.get("proposal_source_count")) != payload["expected_proposal_source_count"]:
        raise ExperimentError("winner adoption API 返回 proposal_source_count 与已审阅快照不一致")
    if {str(item) for item in result.get("disabled_chunk_ids", [])} != loser_chunks:
        raise ExperimentError("winner adoption disabled_chunk_ids 与非 winner source chunks 不一致")
    if {str(item) for item in result.get("clear_penalty_chunk_ids", [])} != all_chunks:
        raise ExperimentError("winner adoption clear_penalty_chunk_ids 与全部 resolved members 不一致")

    after_by_id = {row.get("id", ""): row for row in after_members}
    bad_status = [
        item for item in expected_member_ids
        if after_by_id.get(item, {}).get("status") != GLOBAL_WINNER_STATUS
        or not db_false(after_by_id.get(item, {}).get("auto_resolved"))
    ]
    if bad_status:
        raise ExperimentError(f"winner adoption 后 raw member status/auto_resolved 异常: {sorted(bad_status)}")
    if any(winner_id not in str(row.get("resolution_note", "")) for row in after_members):
        raise ExperimentError("winner adoption 后 raw member 缺少 global winner audit note")

    chunk_by_id = {row.get("id", ""): row for row in after_chunks}
    if set(chunk_by_id) != all_chunks:
        raise ExperimentError("winner adoption 后无法读取完整 member chunks")
    disabled_wrong = [item for item in loser_chunks if not db_false(chunk_by_id[item].get("is_enabled"))]
    winner_wrong = [item for item in winner_chunks if db_false(chunk_by_id[item].get("is_enabled"))]
    if disabled_wrong or winner_wrong:
        raise ExperimentError(
            f"winner adoption chunk state 异常: loser_not_disabled={sorted(disabled_wrong)} "
            f"winner_disabled={sorted(winner_wrong)}"
        )

    if after_fact.get("status") != "resolved" or as_int(after_fact.get("pending_conflict_count")) != 0:
        raise ExperimentError(f"winner adoption 后 DisputedFact 未收敛为 resolved: {after_fact}")
    if after_fact.get("suggested_winner_knowledge_id") != winner_id or \
            after_fact.get("winner_proposal_version") != payload["expected_proposal_version"]:
        raise ExperimentError("winner adoption 后 DisputedFact proposal 被意外更改")
    adoption_id = str(result.get("winner_adoption_id", "")).strip()
    if not adoption_id:
        raise ExperimentError("winner adoption API 未返回 durable winner_adoption_id")
    if after_fact.get("active_winner_adoption_id") != adoption_id:
        raise ExperimentError("winner adoption 后 DisputedFact active_winner_adoption_id 不一致")
    if any(row.get("winner_adoption_id") != adoption_id for row in after_members):
        raise ExperimentError("winner adoption 后 raw members 未绑定同一 durable adoption ID")
    adoption = query_adoption(db, kb_id, adoption_id)
    if adoption.get("status") != "adopted" or adoption.get("disputed_fact_id") != cluster_id or \
            adoption.get("winner_knowledge_id") != winner_id or \
            adoption.get("proposal_version") != payload["expected_proposal_version"]:
        raise ExperimentError(f"winner adoption durable audit row 异常: {adoption}")

    artifact = {
        "schema_version": 1,
        "run_dir": str(run_dir),
        "knowledge_base_id": kb_id,
        "disputed_fact_id": cluster_id,
        "reviewed_payload": payload,
        "stale_guard": {
            "attempted": not args.skip_stale_guard,
            "rejection": stale_rejection,
            "members_unchanged": not args.skip_stale_guard,
            "chunks_unchanged": not args.skip_stale_guard,
        },
        "members_before": before_members,
        "chunks_before": before_chunks,
        "api_result": result,
        "durable_adoption": adoption,
        "members_after": after_members,
        "chunks_after": after_chunks,
        "disputed_fact_after": after_fact,
    }
    output = Path(args.output).expanduser().resolve() if args.output else run_dir / "winner_adoption.json"
    json_dump(output, artifact)
    print(f"C4.7 winner adoption verified: {output}")
    print(f"  KB: {kb_id}")
    print(f"  disputed fact: {cluster_id}")
    print(f"  winner knowledge: {winner_id}")
    print(f"  updated raw conflicts: {len(expected_member_ids)}")
    print(f"  disabled loser chunks: {len(loser_chunks)}")
    print(f"  stale guard: {'skipped' if args.skip_stale_guard else 'HTTP 409 / no mutation'}")
    return 0


def run_no_proposal_negative(args: argparse.Namespace, run_dir: Path, kb_id: str, client: APIClient, db: PostgresExporter) -> int:
    fact = select_live_fact(client, kb_id, args.cluster_id, expect_no_proposal=True)
    cluster_id = str(fact["id"])
    if str(fact.get("suggested_winner_knowledge_id", "")).strip():
        raise ExperimentError("negative run 意外包含可采纳 winner proposal")
    before_members = query_members(db, kb_id, cluster_id)
    if not before_members or any(row.get("status") != "pending" for row in before_members):
        raise ExperimentError("negative run 必须保留 pending raw members")
    all_chunks = {
        str(row.get(key, "")).strip()
        for row in before_members
        for key in ("chunk_id_a", "chunk_id_b")
        if str(row.get(key, "")).strip()
    }
    before_chunks = query_chunks(db, kb_id, all_chunks)
    if len(before_chunks) != len(all_chunks):
        raise ExperimentError("negative adoption 前无法读取完整的 member chunks")
    if any(not db_false(row.get("auto_resolved")) for row in before_members):
        raise ExperimentError("negative run 的 raw member auto_resolved 必须为 false")
    if any(db_false(row.get("is_enabled")) for row in before_chunks):
        raise ExperimentError("negative adoption 前存在 disabled member chunk；请使用 fresh C4.6 run")
    payload = {
        "disputed_fact_id": cluster_id,
        "expected_winner_knowledge_id": "not-a-current-winner",
        "expected_proposal_version": "c3-c4-v1",
        "expected_proposal_updated_at": str(fact.get("updated_at", "")),
        "expected_proposal_source_count": max(as_int(fact.get("source_count")), 2),
        "note": args.note,
    }
    rejection = require_http_conflict(
        "no-proposal winner adoption",
        lambda: client.post(f"/knowledge-bases/{kb_id}/conflicts/clusters/adopt-winner", payload),
    )
    verify_unchanged(db, kb_id, cluster_id, before_members, before_chunks)
    after_fact = query_fact(db, kb_id, cluster_id)
    if after_fact.get("status") != "pending" or after_fact.get("suggested_winner_knowledge_id", "") or \
            after_fact.get("active_winner_adoption_id", ""):
        raise ExperimentError(f"negative adoption rejection 改变了 DisputedFact: {after_fact}")

    artifact = {
        "schema_version": 1,
        "run_dir": str(run_dir),
        "knowledge_base_id": kb_id,
        "disputed_fact_id": cluster_id,
        "attempt_payload": payload,
        "expected_http_status": 409,
        "rejection": rejection,
        "members_before_after": before_members,
        "chunks_before_after": before_chunks,
        "disputed_fact_after": after_fact,
    }
    output = Path(args.output).expanduser().resolve() if args.output else run_dir / "winner_adoption_negative.json"
    json_dump(output, artifact)
    print(f"C4.7 no-proposal rejection verified: {output}")
    print(f"  KB: {kb_id}")
    print(f"  disputed fact: {cluster_id}")
    print("  expected rejection: HTTP 409 / no mutation")
    return 0


def run(args: argparse.Namespace) -> int:
    run_dir = resolve_run_dir(args.run_dir)
    manifest = load_json(run_dir / "manifest.json")
    if not isinstance(manifest, dict):
        raise ExperimentError("manifest.json 根节点必须是对象")
    kb_id = str(manifest.get("knowledge_base_id", "")).strip()
    if not kb_id:
        raise ExperimentError("manifest.json 缺少 knowledge_base_id")

    client = APIClient(args.base_url, os.environ.get("WEKNORA_API_KEY"))
    db = PostgresExporter()
    db.check()
    if not conflict_status_width_is_sufficient(db):
        raise ExperimentError("knowledge_conflicts.status 宽度不足；请重启包含 C4.5/C4.7 所依赖 migration 000089 的后端。")
    if not disputed_fact_winner_proposals_ready(db):
        raise ExperimentError("缺少 C3/C4.6 winner proposal 列；请重启包含 migration 000091 的后端。")
    if not disputed_fact_winner_adoptions_ready(db):
        raise ExperimentError("缺少 C4.8 durable winner adoption schema；请重启包含 migration 000092 的后端。")

    if args.expect_no_proposal:
        return run_no_proposal_negative(args, run_dir, kb_id, client, db)
    return run_positive(args, run_dir, kb_id, client, db)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Explicitly adopt one current C4.6 winner proposal and verify C4.7 propagation.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--run-dir", required=True, help="Existing fresh C4.6 experiment run directory")
    parser.add_argument("--cluster-id", default="", help="Optional explicit DisputedFact ID; default requires exactly one matching pending cluster")
    parser.add_argument("--expect-no-proposal", action="store_true", help="Expect the API to reject a cross-issuer/no-proposal cluster with HTTP 409")
    parser.add_argument("--skip-stale-guard", action="store_true", help="Skip the positive path's deliberate stale-snapshot rejection check")
    parser.add_argument("--note", default="C4.7 script-driven explicit global winner adoption")
    parser.add_argument("--output", default="", help="Output JSON path; defaults to a winner_adoption*.json artifact in the run directory")
    parser.add_argument("--base-url", default=os.environ.get("WEKNORA_BASE_URL", "http://127.0.0.1:8080"))
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        return run(args)
    except ExperimentError as exc:
        print(f"[winner-adoption] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
