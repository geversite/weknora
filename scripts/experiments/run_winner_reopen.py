#!/usr/bin/env python3
"""Exercise C4.8 explicit reopening of one durable C4.7 winner adoption.

This verifier starts from a fresh C4.7 experiment run. It reads the live
DisputedFact through the public clusters API, echoes its active adoption ID and
updated_at snapshot to POST /clusters/reopen-winner, and uses PostgreSQL only
for read-only verification. It never writes database rows directly.

Positive usage:

  python3 scripts/experiments/run_winner_reopen.py \
    --run-dir experiments/runs/<fresh-c46-run-already-used-by-c47>

Negative usage (a fresh C4.6 no-proposal run has no active adoption):

  python3 scripts/experiments/run_winner_reopen.py \
    --run-dir experiments/runs/<fresh-c46-cross-issuer-run> \
    --expect-no-active-adoption

The positive path first deliberately sends an old updated_at value and requires
HTTP 409/no mutation, then reopens the exact current adoption. C4.8 refuses to
re-enable a chunk unless the durable adoption record still owns it.
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
)
from run_winner_adoption import (
    GLOBAL_WINNER_STATUS,
    chunk_snapshot,
    db_false,
    load_json,
    member_snapshot,
    query_adoption,
    query_chunks,
    query_fact,
    query_members,
    require_http_conflict,
    resolve_run_dir,
)


def as_int(value: Any) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def parse_string_array(value: Any, field: str) -> list[str]:
    if isinstance(value, list):
        parsed = value
    else:
        try:
            parsed = json.loads(str(value or "[]"))
        except json.JSONDecodeError as exc:
            raise ExperimentError(f"winner adoption {field} 不是 JSON 数组: {value!r}") from exc
    if not isinstance(parsed, list) or any(not isinstance(item, str) or not item for item in parsed):
        raise ExperimentError(f"winner adoption {field} 必须是非空字符串数组")
    return sorted(parsed)


def live_facts(client: APIClient, kb_id: str, status: str) -> list[dict[str, Any]]:
    data = client.get(f"/knowledge-bases/{kb_id}/conflicts/clusters?status={status}&page=1&page_size=200")
    if not isinstance(data, dict) or not isinstance(data.get("list"), list):
        raise ExperimentError("clusters API 响应格式异常")
    return [item for item in data["list"] if isinstance(item, dict)]


def select_live_fact(
    client: APIClient,
    kb_id: str,
    explicit_id: str,
    expect_no_active_adoption: bool,
) -> dict[str, Any]:
    status = "pending" if expect_no_active_adoption else "resolved"
    facts = live_facts(client, kb_id, status)
    if explicit_id:
        matched = [item for item in facts if str(item.get("id", "")) == explicit_id]
    elif expect_no_active_adoption:
        matched = [item for item in facts if not str(item.get("active_winner_adoption_id", "")).strip()]
    else:
        matched = [item for item in facts if str(item.get("active_winner_adoption_id", "")).strip()]
    if len(matched) != 1:
        kind = "无 active adoption" if expect_no_active_adoption else "可 reopen 的 active adoption"
        raise ExperimentError(f"未指定 --cluster-id 时，run 中必须恰有一个 {kind} DisputedFact；当前为 {len(matched)} 个")
    fact = matched[0]
    if not str(fact.get("updated_at", "")).strip():
        raise ExperimentError("clusters API 未返回 updated_at，无法建立 optimistic reopen snapshot")
    return fact


def verify_reopen_unchanged(
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
        raise ExperimentError("被拒绝的 reopen 请求改变了 raw members")
    if chunk_snapshot(after_chunks) != chunk_snapshot(before_chunks):
        raise ExperimentError("被拒绝的 reopen 请求改变了 chunks")


def member_chunk_sets(members: list[dict[str, str]], winner_id: str) -> tuple[set[str], set[str], set[str]]:
    if not members:
        raise ExperimentError("cluster 没有 raw members")
    disabled: set[str] = set()
    winner: set[str] = set()
    all_chunks: set[str] = set()
    for row in members:
        if row.get("status") != GLOBAL_WINNER_STATUS:
            raise ExperimentError(f"reopen 前 raw member status 异常: {row.get('id')}={row.get('status')}")
        for knowledge_key, chunk_key in (("knowledge_id_a", "chunk_id_a"), ("knowledge_id_b", "chunk_id_b")):
            knowledge_id = str(row.get(knowledge_key, "")).strip()
            chunk_id = str(row.get(chunk_key, "")).strip()
            if not knowledge_id or not chunk_id:
                raise ExperimentError(f"reopen 前 raw member {row.get('id')} 缺少 knowledge/chunk")
            all_chunks.add(chunk_id)
            if knowledge_id == winner_id:
                winner.add(chunk_id)
            else:
                disabled.add(chunk_id)
    return disabled, winner, all_chunks


def payload_for_live_fact(fact: dict[str, Any], note: str) -> dict[str, Any]:
    adoption_id = str(fact.get("active_winner_adoption_id", "")).strip()
    updated_at = str(fact.get("updated_at", "")).strip()
    if not adoption_id or not updated_at:
        raise ExperimentError("live DisputedFact 没有可 reopen 的 active_winner_adoption_id / updated_at")
    return {
        "disputed_fact_id": str(fact.get("id", "")),
        "winner_adoption_id": adoption_id,
        "expected_disputed_fact_updated_at": updated_at,
        "note": note,
    }


def verify_reopen_positive(args: argparse.Namespace, run_dir: Path, kb_id: str, client: APIClient, db: PostgresExporter) -> int:
    fact_live = select_live_fact(client, kb_id, args.cluster_id, expect_no_active_adoption=False)
    cluster_id = str(fact_live["id"])
    payload = payload_for_live_fact(fact_live, args.note)
    adoption_id = payload["winner_adoption_id"]

    before_members = query_members(db, kb_id, cluster_id)
    if any(row.get("winner_adoption_id") != adoption_id for row in before_members):
        raise ExperimentError("reopen 前 raw members 未全部绑定 live active adoption ID")
    adoption_before = query_adoption(db, kb_id, adoption_id)
    if adoption_before.get("status") != "adopted":
        raise ExperimentError(f"reopen 前 durable adoption 不是 adopted: {adoption_before}")
    winner_id = str(adoption_before.get("winner_knowledge_id", "")).strip()
    if not winner_id:
        raise ExperimentError("reopen 前 durable adoption 缺少 winner_knowledge_id")
    disabled_chunks, winner_chunks, all_chunks = member_chunk_sets(before_members, winner_id)
    recorded_disabled = set(parse_string_array(adoption_before.get("disabled_chunk_ids"), "disabled_chunk_ids"))
    if disabled_chunks != recorded_disabled:
        raise ExperimentError("live non-winner chunks 与 durable adoption disabled_chunk_ids 不一致")
    before_chunks = query_chunks(db, kb_id, all_chunks)
    if len(before_chunks) != len(all_chunks):
        raise ExperimentError("reopen 前无法读取完整 member chunks")
    chunks_by_id = {row.get("id", ""): row for row in before_chunks}
    if any(not db_false(chunks_by_id[item].get("is_enabled")) for item in disabled_chunks):
        raise ExperimentError("reopen 前存在未禁用的 durable loser chunk")
    if any(db_false(chunks_by_id[item].get("is_enabled")) for item in winner_chunks):
        raise ExperimentError("reopen 前 winner member chunk 意外被禁用")

    stale_rejection = ""
    if not args.skip_stale_guard:
        stale_payload = dict(payload)
        stale_payload["expected_disputed_fact_updated_at"] = "1970-01-01T00:00:00Z"
        stale_rejection = require_http_conflict(
            "stale winner reopen",
            lambda: client.post(f"/knowledge-bases/{kb_id}/conflicts/clusters/reopen-winner", stale_payload),
        )
        verify_reopen_unchanged(db, kb_id, cluster_id, before_members, before_chunks)
        if query_adoption(db, kb_id, adoption_id).get("status") != "adopted":
            raise ExperimentError("被拒绝的 stale reopen 改变了 durable adoption")

    result = client.post(f"/knowledge-bases/{kb_id}/conflicts/clusters/reopen-winner", payload)
    if not isinstance(result, dict):
        raise ExperimentError("winner reopen API 响应格式异常")

    after_members = query_members(db, kb_id, cluster_id)
    after_chunks = query_chunks(db, kb_id, all_chunks)
    after_fact = query_fact(db, kb_id, cluster_id)
    adoption_after = query_adoption(db, kb_id, adoption_id)
    before_member_ids = {row.get("id", "") for row in before_members}
    result_member_ids = {str(item) for item in result.get("reopened_conflict_ids", [])}
    if before_member_ids != result_member_ids:
        raise ExperimentError(
            f"winner reopen 成员不完整：expected={sorted(before_member_ids)} actual={sorted(result_member_ids)}"
        )
    if result.get("winner_adoption_id") != adoption_id or result.get("winner_knowledge_id") != winner_id:
        raise ExperimentError("winner reopen API 返回的 adoption/winner 不一致")
    if result.get("reopen_version") != "c4-winner-reopen-v1":
        raise ExperimentError("winner reopen API 返回 reopen_version 异常")
    if {str(item) for item in result.get("reenabled_chunk_ids", [])} != disabled_chunks:
        raise ExperimentError("winner reopen reenabled_chunk_ids 与 durable targets 不一致")

    bad_members = [
        row.get("id", "") for row in after_members
        if row.get("status") != "pending" or row.get("winner_adoption_id", "") or not db_false(row.get("auto_resolved"))
    ]
    if bad_members:
        raise ExperimentError(f"winner reopen 后 raw members 未全部回到 pending: {sorted(bad_members)}")
    if any(adoption_id not in str(row.get("resolution_note", "")) for row in after_members):
        raise ExperimentError("winner reopen 后 raw member 缺少 adoption audit note")

    after_chunks_by_id = {row.get("id", ""): row for row in after_chunks}
    if set(after_chunks_by_id) != all_chunks or any(db_false(after_chunks_by_id[item].get("is_enabled")) for item in all_chunks):
        raise ExperimentError("winner reopen 后 member chunks 未全部 enabled")
    if after_fact.get("status") != "pending" or as_int(after_fact.get("pending_conflict_count")) != len(before_member_ids):
        raise ExperimentError(f"winner reopen 后 DisputedFact 未收敛为 pending: {after_fact}")
    if not str(after_fact.get("clusterer_version", "")).strip():
        raise ExperimentError("winner reopen 后 DisputedFact 缺少 clusterer_version 导出")
    if after_fact.get("active_winner_adoption_id", ""):
        raise ExperimentError("winner reopen 后 active_winner_adoption_id 未清空")
    if after_fact.get("suggested_winner_knowledge_id") != winner_id or \
            after_fact.get("winner_proposal_version") != adoption_before.get("proposal_version"):
        raise ExperimentError("winner reopen 后 C4.6 proposal 被意外丢失/改变")
    if adoption_after.get("status") != "revoked" or not str(adoption_after.get("revoked_at", "")).strip():
        raise ExperimentError(f"winner reopen 后 durable adoption 未标为 revoked: {adoption_after}")
    if adoption_after.get("winner_knowledge_id") != winner_id or \
            adoption_after.get("proposal_version") != adoption_before.get("proposal_version"):
        raise ExperimentError("winner reopen 改写了不可变 adoption evidence")

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
            "adoption_unchanged": not args.skip_stale_guard,
        },
        "members_before": before_members,
        "chunks_before": before_chunks,
        "durable_adoption_before": adoption_before,
        "api_result": result,
        "members_after": after_members,
        "chunks_after": after_chunks,
        "disputed_fact_after": after_fact,
        "durable_adoption_after": adoption_after,
    }
    output = Path(args.output).expanduser().resolve() if args.output else run_dir / "winner_reopen.json"
    json_dump(output, artifact)
    print(f"C4.8 winner reopen verified: {output}")
    print(f"  KB: {kb_id}")
    print(f"  disputed fact: {cluster_id}")
    print(f"  winner adoption: {adoption_id}")
    print(f"  reopened raw conflicts: {len(before_member_ids)}")
    print(f"  reenabled chunks: {len(disabled_chunks)}")
    print(f"  stale guard: {'skipped' if args.skip_stale_guard else 'HTTP 409 / no mutation'}")
    return 0


def verify_no_active_negative(args: argparse.Namespace, run_dir: Path, kb_id: str, client: APIClient, db: PostgresExporter) -> int:
    fact_live = select_live_fact(client, kb_id, args.cluster_id, expect_no_active_adoption=True)
    cluster_id = str(fact_live["id"])
    if str(fact_live.get("active_winner_adoption_id", "")).strip():
        raise ExperimentError("negative run 意外包含 active winner adoption")
    before_members = query_members(db, kb_id, cluster_id)
    if not before_members or any(row.get("status") != "pending" for row in before_members):
        raise ExperimentError("negative reopen run 必须有 pending raw members")
    all_chunks = {
        str(row.get(key, "")).strip()
        for row in before_members
        for key in ("chunk_id_a", "chunk_id_b")
        if str(row.get(key, "")).strip()
    }
    before_chunks = query_chunks(db, kb_id, all_chunks)
    if len(before_chunks) != len(all_chunks):
        raise ExperimentError("negative reopen 前无法读取完整 member chunks")
    if any(db_false(row.get("is_enabled")) for row in before_chunks):
        raise ExperimentError("negative reopen 前存在 disabled member chunk；请使用 fresh C4.6 run")
    payload = {
        "disputed_fact_id": cluster_id,
        "winner_adoption_id": "not-an-active-adoption",
        "expected_disputed_fact_updated_at": str(fact_live.get("updated_at", "")),
        "note": args.note,
    }
    rejection = require_http_conflict(
        "no-active-adoption winner reopen",
        lambda: client.post(f"/knowledge-bases/{kb_id}/conflicts/clusters/reopen-winner", payload),
    )
    verify_reopen_unchanged(db, kb_id, cluster_id, before_members, before_chunks)
    after_fact = query_fact(db, kb_id, cluster_id)
    if after_fact.get("status") != "pending" or after_fact.get("active_winner_adoption_id", ""):
        raise ExperimentError(f"negative reopen rejection 改变了 DisputedFact: {after_fact}")

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
    output = Path(args.output).expanduser().resolve() if args.output else run_dir / "winner_reopen_negative.json"
    json_dump(output, artifact)
    print(f"C4.8 no-active-adoption rejection verified: {output}")
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
        raise ExperimentError("knowledge_conflicts.status 宽度不足；请重启包含 migration 000089 的后端。")
    if not disputed_fact_winner_proposals_ready(db):
        raise ExperimentError("缺少 C3/C4.6 winner proposal 列；请重启包含 migration 000091 的后端。")
    if not disputed_fact_winner_adoptions_ready(db):
        raise ExperimentError("缺少 C4.8 durable winner adoption schema；请重启包含 migration 000092 的后端。")

    if args.expect_no_active_adoption:
        return verify_no_active_negative(args, run_dir, kb_id, client, db)
    return verify_reopen_positive(args, run_dir, kb_id, client, db)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Explicitly reopen one durable C4.7 winner adoption and verify C4.8 restoration.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--run-dir", required=True, help="Fresh C4.7-adopted run directory, or fresh no-adoption negative run")
    parser.add_argument("--cluster-id", default="", help="Optional explicit DisputedFact ID; default requires exactly one matching cluster")
    parser.add_argument("--expect-no-active-adoption", action="store_true", help="Expect HTTP 409/no mutation because the cluster has no active C4.7 adoption")
    parser.add_argument("--skip-stale-guard", action="store_true", help="Skip the positive path's deliberate stale-updated_at rejection check")
    parser.add_argument("--note", default="C4.8 script-driven explicit global winner reopen")
    parser.add_argument("--output", default="", help="Output JSON path; defaults to winner_reopen*.json in the run directory")
    parser.add_argument("--base-url", default=os.environ.get("WEKNORA_BASE_URL", "http://127.0.0.1:8080"))
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        return run(args)
    except ExperimentError as exc:
        print(f"[winner-reopen] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
