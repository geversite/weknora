#!/usr/bin/env python3
"""Run a reproducible C1 claim-extraction experiment against a live WeKnora app.

The runner deliberately drives the public HTTP API and waits for the real
Asynq workers.  It never inserts rows into claims or knowledge_conflicts.
PostgreSQL is read only and is used solely to export the experiment evidence.

Typical use (on the Linux host where make dev-app is already running):

  export WEKNORA_BASE_URL=http://127.0.0.1:8080
  export WEKNORA_API_KEY='...'
  export WEKNORA_EXPERIMENT_TEMPLATE_KB='<kb id with working models>'
  export WEKNORA_DOCKER_BIN='sudo docker'  # only when docker requires sudo
  python3 scripts/experiments/run_claims_eval.py \
      --scenario scripts/experiments/scenarios/c1_full.json

Credentials are read only from environment variables and are never written to
run artifacts or echoed by this program.
"""

from __future__ import annotations

import argparse
import copy
import csv
import datetime as dt
import hashlib
import io
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any, Iterable


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_SCENARIO = ROOT / "scripts/experiments/scenarios/c1_full.json"
DEFAULT_RUNS_DIR = ROOT / "experiments/runs"
EVALUATOR = ROOT / "testdata/claims_eval/evaluate.py"


class ExperimentError(RuntimeError):
    """A run failed in a way that should be recorded in its artifacts."""


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def safe_git_sha() -> str:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=ROOT, check=True,
            capture_output=True, text=True,
        )
        return result.stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return "unknown"


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2, default=str) + "\n", encoding="utf-8")


def sql_literal(value: str) -> str:
    """Quote a value controlled by the runner for a PostgreSQL text literal."""
    return "'" + value.replace("'", "''") + "'"


def read_json(path: Path) -> dict[str, Any]:
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ExperimentError(f"找不到文件: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ExperimentError(f"JSON 无法解析: {path}: {exc}") from exc
    if not isinstance(raw, dict):
        raise ExperimentError(f"JSON 根节点必须是对象: {path}")
    return raw


def parse_json_or_empty(value: str | None) -> dict[str, Any]:
    if not value:
        return {}
    try:
        parsed = json.loads(value)
    except json.JSONDecodeError:
        return {}
    return parsed if isinstance(parsed, dict) else {}


def slice_runes(content: str, start_raw: str | int | None, end_raw: str | int | None) -> str:
    """Extract a Go-rune-indexed claim quote from chunk content."""
    try:
        start = int(start_raw or 0)
        end = int(end_raw or 0)
    except (TypeError, ValueError):
        return ""
    runes = list(content or "")
    if start < 0 or end <= start or start >= len(runes):
        return ""
    return "".join(runes[start:min(end, len(runes))])


class APIClient:
    def __init__(self, base_url: str, api_key: str | None):
        self.base_url = base_url.rstrip("/")
        self.api_url = self.base_url + "/api/v1"
        self.api_key = api_key or ""

    def health(self) -> dict[str, Any]:
        return self._request("GET", self.base_url + "/health", use_api_key=False, unwrap=False)

    def get(self, path: str) -> Any:
        return self._request("GET", self.api_url + path, unwrap=True)

    def post(self, path: str, payload: dict[str, Any]) -> Any:
        return self._request("POST", self.api_url + path, payload, unwrap=True)

    def delete(self, path: str) -> Any:
        return self._request("DELETE", self.api_url + path, unwrap=True)

    def _request(
        self,
        method: str,
        url: str,
        payload: dict[str, Any] | None = None,
        *,
        use_api_key: bool = True,
        unwrap: bool = True,
    ) -> Any:
        headers = {"Accept": "application/json"}
        body: bytes | None = None
        if use_api_key:
            if not self.api_key:
                raise ExperimentError("缺少 WEKNORA_API_KEY；实验写入 API 需要 API key")
            headers["X-API-Key"] = self.api_key
        if payload is not None:
            headers["Content-Type"] = "application/json"
            body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        request = urllib.request.Request(url, data=body, headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                text = response.read().decode("utf-8", errors="replace")
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")[:3000]
            raise ExperimentError(f"HTTP {exc.code}: {method} {urllib.parse.urlparse(url).path}: {detail}") from exc
        except urllib.error.URLError as exc:
            raise ExperimentError(f"无法连接 WeKnora 服务 {self.base_url}: {exc.reason}") from exc

        try:
            response_json = json.loads(text)
        except json.JSONDecodeError as exc:
            raise ExperimentError(f"{method} {urllib.parse.urlparse(url).path} 返回非 JSON: {text[:500]}") from exc

        if not unwrap:
            return response_json
        if isinstance(response_json, dict) and response_json.get("success") is False:
            raise ExperimentError(f"{method} {urllib.parse.urlparse(url).path} 返回失败: {response_json}")
        if isinstance(response_json, dict) and "data" in response_json:
            return response_json["data"]
        return response_json


class PostgresExporter:
    """Read-only psql wrapper.

    Two connection modes are supported:
      1. WEKNORA_EXPERIMENT_PG_DSN + a host psql binary;
      2. docker exec into the dev postgres container (the default in this repo).

    Docker may require sudo on the user's host. Set WEKNORA_DOCKER_BIN="sudo docker"
    instead of putting a password or credential in the command line.
    """

    def __init__(self) -> None:
        self.dsn = os.environ.get("WEKNORA_EXPERIMENT_PG_DSN", "").strip()
        self.psql_bin = os.environ.get("WEKNORA_PSQL_BIN", "psql").strip() or "psql"
        self.docker_bin = shlex.split(os.environ.get("WEKNORA_DOCKER_BIN", "docker"))
        self.container = os.environ.get("WEKNORA_PG_CONTAINER", "WeKnora-postgres-dev").strip()
        self.user = os.environ.get("WEKNORA_PG_USER", "postgres").strip()
        self.database = os.environ.get("WEKNORA_PG_DATABASE", "WeKnora").strip()

    def describe_mode(self) -> str:
        return "host-psql" if self.dsn else f"docker:{self.container}"

    def check(self) -> None:
        self.query("SELECT 1 AS ok")

    def query(self, sql: str) -> list[dict[str, str]]:
        if self.dsn:
            if shutil.which(self.psql_bin) is None:
                raise ExperimentError(
                    f"找不到 psql ({self.psql_bin})；安装 PostgreSQL client 或配置 Docker 导出模式"
                )
            command = [
                self.psql_bin, self.dsn, "-X", "-v", "ON_ERROR_STOP=1", "--csv", "-P", "footer=off", "-c", sql,
            ]
        else:
            if not self.docker_bin:
                raise ExperimentError("WEKNORA_DOCKER_BIN 为空，无法通过 docker 导出数据库")
            command = self.docker_bin + [
                "exec", "-i", self.container,
                "psql", "-X", "-U", self.user, "-d", self.database,
                "-v", "ON_ERROR_STOP=1", "--csv", "-P", "footer=off", "-c", sql,
            ]
        try:
            result = subprocess.run(command, check=False, capture_output=True, text=True, timeout=60)
        except FileNotFoundError as exc:
            raise ExperimentError(
                "无法执行数据库导出命令；请设置 WEKNORA_DOCKER_BIN 或 WEKNORA_EXPERIMENT_PG_DSN"
            ) from exc
        except subprocess.TimeoutExpired as exc:
            raise ExperimentError("数据库导出超时（60 秒）") from exc
        if result.returncode != 0:
            # Do not print the command: a DSN could include a password.
            raise ExperimentError(
                f"数据库查询失败（模式 {self.describe_mode()}）: {result.stderr.strip()[:3000]}"
            )
        return list(csv.DictReader(io.StringIO(result.stdout)))


def load_scenario(path: Path) -> dict[str, Any]:
    scenario = read_json(path)
    if not scenario.get("name"):
        raise ExperimentError(f"场景缺少 name: {path}")
    documents = scenario.get("documents")
    if not isinstance(documents, list) or not documents:
        raise ExperimentError(f"场景 documents 必须是非空数组: {path}")
    seen: set[str] = set()
    for document in documents:
        if not isinstance(document, dict) or not document.get("id") or not document.get("path"):
            raise ExperimentError(f"场景文档必须包含 id/path: {document}")
        doc_id = str(document["id"])
        if doc_id in seen:
            raise ExperimentError(f"场景中存在重复文档 id: {doc_id}")
        seen.add(doc_id)
        source = ROOT / str(document["path"])
        if not source.is_file():
            raise ExperimentError(f"场景引用的文档不存在: {source}")
    return scenario


def apply_variant(strategy: dict[str, Any], scenario: dict[str, Any], variant: str) -> dict[str, Any]:
    result = copy.deepcopy(strategy)
    result.update(copy.deepcopy(scenario.get("indexing_strategy_overrides", {})))
    if variant == "v1":
        result["claim_extract_enabled"] = False
    elif variant == "c1":
        result["claim_extract_enabled"] = True
    else:
        raise ExperimentError(f"未知 variant: {variant}")

    # API UpdateKnowledgeBase rejects an entirely-empty indexing strategy. A
    # normal experiment template should already have vector/keyword enabled;
    # fail early rather than silently running a different pipeline.
    base_enabled = any(bool(result.get(k)) for k in (
        "vector_enabled", "keyword_enabled", "wiki_enabled", "graph_enabled",
    ))
    if not base_enabled:
        raise ExperimentError(
            "实验 KB 的 vector/keyword/wiki/graph 均为关闭；当前 API 不接受只有 claim_extract_enabled 的策略。"
        )
    return result


def clone_payload(template: dict[str, Any], name: str, scenario: dict[str, Any], variant: str) -> dict[str, Any]:
    # Keep this whitelist intentional. GET responses contain read-only counters,
    # tenant fields and store display metadata that should never be sent back to
    # POST /knowledge-bases.
    fields = (
        "type", "description", "chunking_config", "image_processing_config",
        "embedding_model_id", "summary_model_id", "vlm_config", "asr_config",
        "storage_provider_config", "storage_backend_id", "storage_config",
        "vector_store_id", "extract_config", "faq_config",
        "question_generation_config", "wiki_config",
    )
    payload: dict[str, Any] = {"name": name, "is_temporary": True}
    for field in fields:
        if field in template and template[field] is not None:
            payload[field] = copy.deepcopy(template[field])
    payload["indexing_strategy"] = apply_variant(
        template.get("indexing_strategy") or {}, scenario, variant,
    )
    payload["description"] = (
        f"Research experiment {scenario['name']} / {variant}. "
        "Created automatically by scripts/experiments/run_claims_eval.py."
    )
    return payload


def wait_for_parse(
    api: APIClient,
    knowledge_id: str,
    timeout_seconds: int,
    poll_seconds: float,
    spans_out: Path,
) -> dict[str, Any]:
    deadline = time.monotonic() + timeout_seconds
    last: dict[str, Any] | None = None
    while time.monotonic() < deadline:
        data = api.get(f"/knowledge/{knowledge_id}/spans")
        if not isinstance(data, dict):
            raise ExperimentError(f"知识 {knowledge_id} 的 spans 响应格式异常")
        last = data
        json_dump(spans_out, data)
        status = str(data.get("parse_status", ""))
        if status == "completed":
            return data
        if status in {"failed", "cancelled"}:
            error = data.get("last_error") or data.get("trace") or {}
            raise ExperimentError(f"知识 {knowledge_id} 处理失败，状态={status}: {error}")
        time.sleep(poll_seconds)
    if last is not None:
        json_dump(spans_out, last)
    raise ExperimentError(f"等待知识 {knowledge_id} 完成超时（{timeout_seconds}s）")


def wait_for_claims(
    db: PostgresExporter,
    knowledge_id: str,
    minimum: int,
    timeout_seconds: int,
    poll_seconds: float,
) -> int:
    deadline = time.monotonic() + timeout_seconds
    query = (
        "SELECT COUNT(*) AS count FROM claims "
        f"WHERE knowledge_id = {sql_literal(knowledge_id)}"
    )
    last_count = 0
    while time.monotonic() < deadline:
        rows = db.query(query)
        last_count = int((rows[0] if rows else {}).get("count", "0"))
        if last_count >= minimum:
            return last_count
        time.sleep(poll_seconds)
    raise ExperimentError(
        f"等待 knowledge={knowledge_id} 的 claims 超时；期望 >= {minimum}，实际 {last_count}"
    )


def claims_table_exists(db: PostgresExporter) -> bool:
    rows = db.query("SELECT to_regclass('public.claims') AS table_name")
    return bool(rows and rows[0].get("table_name"))


def query_claims(db: PostgresExporter, kb_id: str) -> list[dict[str, str]]:
    # A historical V1 binary/database predates migration 000085. Treat its
    # absence as the expected V1 condition rather than making baseline runs
    # fail during evidence export.
    if not claims_table_exists(db):
        return []
    return db.query(
        "SELECT c.id, c.tenant_id, c.knowledge_base_id, c.source_type, c.source_id, "
        "c.knowledge_id, c.span_start, c.span_end, c.subject, c.predicate, c.value, "
        "COALESCE(c.qualifiers::text, '{}') AS qualifiers, c.claim_key, c.value_norm, "
        "c.value_kind, c.extractor_version, c.extract_batch_id, c.created_at, "
        "COALESCE(ch.content, '') AS source_content "
        "FROM claims c "
        "LEFT JOIN chunks ch ON ch.id = c.source_id "
        f"WHERE c.knowledge_base_id = {sql_literal(kb_id)} "
        "ORDER BY c.knowledge_id, c.source_id, c.span_start, c.created_at"
    )


def query_conflicts(db: PostgresExporter, kb_id: str) -> list[dict[str, str]]:
    return db.query(
        "SELECT c.id, c.knowledge_id_a, c.knowledge_id_b, c.chunk_id_a, c.chunk_id_b, "
        "c.content_a, c.content_b, c.conflict_type, c.llm_reason, c.status, c.detected_by, "
        "c.created_at, c.updated_at, "
        "COALESCE(ka.title, '') AS title_a, COALESCE(kb.title, '') AS title_b "
        "FROM knowledge_conflicts c "
        "LEFT JOIN knowledges ka ON ka.id = c.knowledge_id_a "
        "LEFT JOIN knowledges kb ON kb.id = c.knowledge_id_b "
        f"WHERE c.knowledge_base_id = {sql_literal(kb_id)} "
        "ORDER BY c.created_at, c.id"
    )


def query_dead_letters(db: PostgresExporter, knowledge_ids: Iterable[str]) -> list[dict[str, str]]:
    ids = list(knowledge_ids)
    if not ids:
        return []
    in_values = ", ".join(sql_literal(value) for value in ids)
    return db.query(
        "SELECT id, tenant_id, task_type, scope, scope_id, related_id, payload::text AS payload, "
        "last_error, fail_count, failed_at "
        "FROM task_dead_letters "
        f"WHERE related_id IN ({in_values}) OR scope_id IN ({in_values}) "
        "ORDER BY failed_at"
    )


def observed_conflict_pairs(
    conflicts: list[dict[str, str]], knowledge_to_doc: dict[str, str],
) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for conflict in conflicts:
        left = knowledge_to_doc.get(conflict.get("knowledge_id_a", ""), conflict.get("knowledge_id_a", ""))
        right = knowledge_to_doc.get(conflict.get("knowledge_id_b", ""), conflict.get("knowledge_id_b", ""))
        rows.append({
            "conflict_id": conflict.get("id", ""),
            "left_document": left,
            "right_document": right,
            "type": conflict.get("conflict_type", ""),
            "status": conflict.get("status", ""),
            "reason": conflict.get("llm_reason", ""),
        })
    return rows


def has_document_pair(
    pairs: list[dict[str, Any]], left: str, right: str,
) -> bool:
    expected = {left, right}
    return any({str(pair["left_document"]), str(pair["right_document"])} == expected for pair in pairs)


def wait_for_expected_pairs(
    db: PostgresExporter,
    kb_id: str,
    expected: list[dict[str, Any]],
    knowledge_to_doc: dict[str, str],
    timeout_seconds: int,
    poll_seconds: float,
) -> tuple[list[dict[str, str]], list[dict[str, Any]]]:
    if not expected:
        conflicts = query_conflicts(db, kb_id)
        return conflicts, observed_conflict_pairs(conflicts, knowledge_to_doc)
    deadline = time.monotonic() + timeout_seconds
    last_conflicts: list[dict[str, str]] = []
    last_pairs: list[dict[str, Any]] = []
    while time.monotonic() < deadline:
        last_conflicts = query_conflicts(db, kb_id)
        last_pairs = observed_conflict_pairs(last_conflicts, knowledge_to_doc)
        if all(has_document_pair(last_pairs, str(item["left"]), str(item["right"])) for item in expected):
            return last_conflicts, last_pairs
        time.sleep(poll_seconds)
    return last_conflicts, last_pairs


def write_evaluator_run(
    output_path: Path,
    run_id: str,
    scenario: dict[str, Any],
    knowledge_ids: dict[str, str],
    claims: list[dict[str, str]],
    manifest: dict[str, Any],
) -> bool:
    """Write evaluate.py's JSON format. Returns false if scenario lacks gold docs."""
    documents = {str(item["id"]): item for item in scenario["documents"]}
    if not all(item.get("gold_doc") for item in documents.values()):
        return False

    claims_by_knowledge: dict[str, list[dict[str, str]]] = {}
    for claim in claims:
        claims_by_knowledge.setdefault(claim.get("knowledge_id", ""), []).append(claim)

    docs: dict[str, list[dict[str, Any]]] = {}
    for doc_id, knowledge_id in knowledge_ids.items():
        document = documents[doc_id]
        gold_doc = str(document["gold_doc"])
        exported: list[dict[str, Any]] = []
        for claim in claims_by_knowledge.get(knowledge_id, []):
            if claim.get("source_type") != "chunk":
                continue
            exported.append({
                "subject": claim.get("subject", ""),
                "predicate": claim.get("predicate", ""),
                "value": claim.get("value", ""),
                "value_kind": claim.get("value_kind", "text"),
                "qualifiers": parse_json_or_empty(claim.get("qualifiers")),
                "quote": slice_runes(
                    claim.get("source_content", ""), claim.get("span_start"), claim.get("span_end"),
                ),
            })
        docs[gold_doc] = exported

    payload = {
        "run": run_id,
        "extractor": f"WeKnora ClaimExtractService; model={manifest.get('summary_model_id', '')}; commit={manifest.get('git_commit', '')}",
        "date": utc_now()[:10],
        "docs": docs,
    }
    json_dump(output_path, payload)
    return True


def run_evaluator(run_file: Path, output_file: Path) -> dict[str, Any]:
    command = [sys.executable, str(EVALUATOR), "--run", str(run_file)]
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, check=False)
    output = (result.stdout or "") + ("\n[stderr]\n" + result.stderr if result.stderr else "")
    output_file.write_text(output, encoding="utf-8")
    merged = re.search(r"合并口径\s+P=([0-9.]+)\s+R=([0-9.]+)", output)
    return {
        "exit_code": result.returncode,
        "combined_precision": float(merged.group(1)) if merged else None,
        "combined_recall": float(merged.group(2)) if merged else None,
    }


def write_report(
    path: Path,
    manifest: dict[str, Any],
    claim_counts: dict[str, int],
    expected_pairs: list[dict[str, Any]],
    observed_pairs: list[dict[str, Any]],
    evaluator: dict[str, Any] | None,
) -> None:
    lines = [
        f"# C1 experiment: {manifest['run_id']}",
        "",
        f"- 状态：`{manifest.get('status', 'unknown')}`",
        f"- 场景：`{manifest['scenario_name']}`",
        f"- Variant：`{manifest['variant']}`",
        f"- Git commit：`{manifest['git_commit']}`",
        f"- KB：`{manifest.get('knowledge_base_id', '')}`",
        f"- Summary model：`{manifest.get('summary_model_id', '')}`",
        f"- 开始时间：`{manifest['started_at']}`",
        "",
        "## 每篇文档 claims 数",
        "",
        "| 文档 | knowledge_id | claims |",
        "|---|---|---:|",
    ]
    for doc_id, knowledge_id in manifest.get("knowledge_ids", {}).items():
        lines.append(f"| {doc_id} | `{knowledge_id}` | {claim_counts.get(doc_id, 0)} |")

    lines += ["", "## 预期与实际冲突文档对", ""]
    if expected_pairs:
        lines += ["| 标识 | 预期文档对 | 已观察到 |", "|---|---|---|"]
        for expected in expected_pairs:
            seen = has_document_pair(observed_pairs, str(expected["left"]), str(expected["right"]))
            lines.append(
                f"| {expected.get('id', '')} | {expected['left']} ↔ {expected['right']} | {'✅' if seen else '❌'} |"
            )
    else:
        lines.append("该场景没有配置冲突文档对断言。")

    lines += ["", "## 原始 conflict 文档对", ""]
    if observed_pairs:
        lines += ["| conflict_id | 文档对 | 类型 | 状态 |", "|---|---|---|---|"]
        for pair in observed_pairs:
            lines.append(
                f"| `{pair['conflict_id']}` | {pair['left_document']} ↔ {pair['right_document']} | "
                f"{pair['type']} | {pair['status']} |"
            )
    else:
        lines.append("未观察到 conflict 行。")

    if evaluator is not None:
        lines += ["", "## 抽取质量评估", ""]
        lines.append(f"- evaluate.py exit code：`{evaluator['exit_code']}`")
        lines.append(f"- 合并 P：`{evaluator['combined_precision']}`")
        lines.append(f"- 合并 R：`{evaluator['combined_recall']}`")

    lines += [
        "",
        "> 注意：knowledge_conflicts 目前仍是 chunk-pair 粒度；本报告的文档对命中只证明候选/裁决链路运行。",
        "> P3 fallback 是否真实命中应使用独立的 p3_fallback 场景验证，而不是仅凭 doc4/doc6 同块结果判断。",
        "",
    ]
    path.write_text("\n".join(lines), encoding="utf-8")


def check_environment(client: APIClient, check_db: bool) -> int:
    print("[check] Python:", sys.version.split()[0])
    try:
        health = client.health()
        if health.get("status") != "ok":
            raise ExperimentError(f"/health 返回异常: {health}")
        print("[check] WeKnora API: OK", client.base_url)
    except ExperimentError as exc:
        print("[check] WeKnora API: FAIL", exc, file=sys.stderr)
        return 1

    if not client.api_key:
        print("[check] WEKNORA_API_KEY: MISSING（health 可用，但实际实验无法写入）", file=sys.stderr)
    else:
        print("[check] WEKNORA_API_KEY: configured")

    if check_db:
        try:
            db = PostgresExporter()
            db.check()
            print("[check] PostgreSQL export: OK", db.describe_mode())
        except ExperimentError as exc:
            print("[check] PostgreSQL export: FAIL", exc, file=sys.stderr)
            return 1
    return 0


def make_run_id(scenario_name: str, variant: str) -> str:
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    return f"{stamp}-{scenario_name}-{variant}-{safe_git_sha()[:8]}"


def experiment_channel(run_id: str) -> str:
    """Build a stable correlation tag that fits knowledges.channel VARCHAR(50).

    The full run ID belongs in the JSON manifest. The database channel is only
    a short, queryable correlation hint, so storing a digest prevents long
    scenario names from making the manual-ingest request fail before any task
    is enqueued.
    """
    digest = hashlib.sha256(run_id.encode("utf-8")).hexdigest()[:16]
    return f"experiment:{digest}"


def run_experiment(args: argparse.Namespace) -> int:
    scenario_path = Path(args.scenario).resolve()
    scenario = load_scenario(scenario_path)
    run_id = args.run_id or make_run_id(str(scenario["name"]), args.variant)
    output_dir = Path(args.output).resolve() if args.output else DEFAULT_RUNS_DIR / run_id
    if output_dir.exists() and any(output_dir.iterdir()) and not args.overwrite:
        raise ExperimentError(f"输出目录已存在且非空: {output_dir}（需要 --overwrite）")
    output_dir.mkdir(parents=True, exist_ok=True)
    (output_dir / "spans").mkdir(exist_ok=True)

    client = APIClient(args.base_url, os.environ.get("WEKNORA_API_KEY"))
    manifest: dict[str, Any] = {
        "schema_version": 1,
        "run_id": run_id,
        "status": "running",
        "started_at": utc_now(),
        "git_commit": safe_git_sha(),
        "scenario_name": scenario["name"],
        "scenario_path": str(scenario_path.relative_to(ROOT)) if scenario_path.is_relative_to(ROOT) else str(scenario_path),
        "variant": args.variant,
        "base_url": client.base_url,
        "ingest_channel": experiment_channel(run_id),
        "knowledge_ids": {},
        "database_export_mode": PostgresExporter().describe_mode(),
    }
    json_dump(output_dir / "manifest.json", manifest)

    if args.dry_run:
        plan = {
            "run_id": run_id,
            "template_kb_id": args.template_kb_id,
            "documents": [
                {"id": doc["id"], "path": doc["path"], "gold_doc": doc.get("gold_doc")}
                for doc in scenario["documents"]
            ],
            "variant": args.variant,
            "output": str(output_dir),
        }
        json_dump(output_dir / "dry_run_plan.json", plan)
        manifest["status"] = "dry_run"
        manifest["finished_at"] = utc_now()
        json_dump(output_dir / "manifest.json", manifest)
        print(json.dumps(plan, ensure_ascii=False, indent=2))
        return 0

    if not args.template_kb_id:
        raise ExperimentError("真实运行需要 --template-kb-id 或 WEKNORA_EXPERIMENT_TEMPLATE_KB")

    db = PostgresExporter()
    try:
        health = client.health()
        if health.get("status") != "ok":
            raise ExperimentError(f"WeKnora /health 非 OK: {health}")
        db.check()

        template = client.get(f"/knowledge-bases/{args.template_kb_id}")
        if not isinstance(template, dict):
            raise ExperimentError("模板 KB 响应格式异常")
        kb_name = f"exp-{scenario['name']}-{run_id[-22:]}"
        payload = clone_payload(template, kb_name, scenario, args.variant)
        kb = client.post("/knowledge-bases", payload)
        if not isinstance(kb, dict) or not kb.get("id"):
            raise ExperimentError("创建实验 KB 的响应缺少 id")
        kb_id = str(kb["id"])
        strategy = kb.get("indexing_strategy") or payload["indexing_strategy"]
        expected_claim_enabled = args.variant == "c1"
        if bool(strategy.get("claim_extract_enabled")) != expected_claim_enabled:
            raise ExperimentError(
                "实验 KB 的 claim_extract_enabled 与 variant 不一致；请确认运行的后端已包含 C1 代码。"
            )
        manifest.update({
            "knowledge_base_id": kb_id,
            "knowledge_base_name": kb.get("name", kb_name),
            "summary_model_id": kb.get("summary_model_id", ""),
            "embedding_model_id": kb.get("embedding_model_id", ""),
            "indexing_strategy": strategy,
        })
        json_dump(output_dir / "manifest.json", manifest)

        claim_counts: dict[str, int] = {}
        documents_by_id = {str(doc["id"]): doc for doc in scenario["documents"]}
        for document in scenario["documents"]:
            doc_id = str(document["id"])
            source_path = ROOT / str(document["path"])
            content = source_path.read_text(encoding="utf-8")
            title = str(document.get("title") or source_path.stem)
            knowledge = client.post(
                f"/knowledge-bases/{kb_id}/knowledge/manual",
                {
                    "title": title,
                    "content": content,
                    "status": "publish",
                    "channel": manifest["ingest_channel"],
                },
            )
            if not isinstance(knowledge, dict) or not knowledge.get("id"):
                raise ExperimentError(f"上传 {doc_id} 返回缺少 knowledge id")
            knowledge_id = str(knowledge["id"])
            manifest["knowledge_ids"][doc_id] = knowledge_id
            json_dump(output_dir / "manifest.json", manifest)
            json_dump(output_dir / "uploads.json", {
                "run_id": run_id,
                "knowledge_base_id": kb_id,
                "documents": manifest["knowledge_ids"],
            })

            wait_for_parse(
                client, knowledge_id, args.timeout_seconds, args.poll_seconds,
                output_dir / "spans" / f"{doc_id}.json",
            )
            if args.variant == "c1":
                minimum = int(document.get("min_claims", scenario.get("min_claims_per_document", 1)))
                claim_counts[doc_id] = wait_for_claims(
                    db, knowledge_id, minimum, args.claim_timeout_seconds, args.poll_seconds,
                )
            else:
                claim_counts[doc_id] = 0

        knowledge_to_doc = {knowledge_id: doc_id for doc_id, knowledge_id in manifest["knowledge_ids"].items()}
        expected_pairs = scenario.get("expected_conflict_document_pairs", [])
        if not isinstance(expected_pairs, list):
            raise ExperimentError("expected_conflict_document_pairs 必须是数组")
        conflicts, observed_pairs = wait_for_expected_pairs(
            db, kb_id, expected_pairs, knowledge_to_doc,
            args.conflict_timeout_seconds, args.poll_seconds,
        )
        claims = query_claims(db, kb_id)
        dead_letters = query_dead_letters(db, manifest["knowledge_ids"].values())
        json_dump(output_dir / "claims.json", claims)
        json_dump(output_dir / "conflicts.json", conflicts)
        json_dump(output_dir / "dead_letters.json", dead_letters)
        json_dump(output_dir / "conflict_document_pairs.json", observed_pairs)

        evaluator_result: dict[str, Any] | None = None
        evaluator_run_path = output_dir / "claims_eval_run.json"
        if args.variant == "c1" and write_evaluator_run(
            evaluator_run_path, run_id, scenario, manifest["knowledge_ids"], claims, manifest,
        ):
            evaluator_result = run_evaluator(evaluator_run_path, output_dir / "evaluator_output.txt")
        else:
            reason = (
                "variant=v1: claims extraction is intentionally disabled; extraction evaluator skipped.\n"
                if args.variant == "v1"
                else "Scenario has no complete gold_doc mapping; evaluator intentionally skipped.\n"
            )
            (output_dir / "evaluator_output.txt").write_text(reason, encoding="utf-8")

        missing_pairs = [
            item for item in expected_pairs
            if not has_document_pair(observed_pairs, str(item["left"]), str(item["right"]))
        ]
        metrics = {
            "claim_count_total": len(claims),
            "claim_counts_by_document": claim_counts,
            "conflict_count_total": len(conflicts),
            "dead_letter_count": len(dead_letters),
            "expected_conflict_document_pairs": expected_pairs,
            "missing_expected_conflict_document_pairs": missing_pairs,
            "evaluator": evaluator_result,
        }
        json_dump(output_dir / "metrics.json", metrics)

        manifest["status"] = "completed" if not missing_pairs else "completed_with_missing_expectations"
        manifest["finished_at"] = utc_now()
        json_dump(output_dir / "manifest.json", manifest)
        write_report(
            output_dir / "report.md", manifest, claim_counts, expected_pairs, observed_pairs, evaluator_result,
        )

        print(f"实验完成: {output_dir}")
        print(f"  KB: {kb_id}")
        print(f"  claims: {len(claims)}")
        print(f"  conflicts: {len(conflicts)}")
        if missing_pairs:
            print("  缺失预期 conflict 文档对:", ", ".join(str(item.get("id", "?")) for item in missing_pairs))
        if evaluator_result:
            print(
                "  evaluator: "
                f"P={evaluator_result['combined_precision']} R={evaluator_result['combined_recall']} "
                f"exit={evaluator_result['exit_code']}"
            )

        # A failed metric is a valid experiment result, but exit non-zero by
        # default so unattended batches/CI cannot silently call it a pass.
        if missing_pairs or (evaluator_result and evaluator_result["exit_code"] != 0):
            return 2
        return 0

    except Exception as exc:
        manifest["status"] = "failed"
        manifest["finished_at"] = utc_now()
        manifest["error"] = str(exc)
        json_dump(output_dir / "manifest.json", manifest)
        (output_dir / "failure.txt").write_text(str(exc) + "\n", encoding="utf-8")
        raise


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Script-driven C1 experiment runner for a live WeKnora server.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--scenario", default=str(DEFAULT_SCENARIO), help="JSON scenario path")
    parser.add_argument("--variant", choices=("v1", "c1"), default="c1", help="Experiment variant")
    parser.add_argument(
        "--template-kb-id", default=os.environ.get("WEKNORA_EXPERIMENT_TEMPLATE_KB", ""),
        help="Existing KB whose model/storage/chunk configuration is cloned into a fresh temporary KB",
    )
    parser.add_argument("--base-url", default=os.environ.get("WEKNORA_BASE_URL", "http://127.0.0.1:8080"))
    parser.add_argument("--output", default="", help="Run artifact directory; default is experiments/runs/<run-id>")
    parser.add_argument("--run-id", default="", help="Stable custom run ID")
    parser.add_argument("--timeout-seconds", type=int, default=300, help="Per-document parse timeout")
    parser.add_argument("--claim-timeout-seconds", type=int, default=300, help="Per-document claims wait timeout")
    parser.add_argument("--conflict-timeout-seconds", type=int, default=180, help="Wait for expected conflict document pairs")
    parser.add_argument("--poll-seconds", type=float, default=2.0, help="Polling interval")
    parser.add_argument("--overwrite", action="store_true", help="Allow a non-empty output directory")
    parser.add_argument("--dry-run", action="store_true", help="Validate scenario and write a plan without contacting services")
    parser.add_argument("--check", action="store_true", help="Check API and database-export connectivity, then exit")
    parser.add_argument("--check-db", action="store_true", help="With --check, also require PostgreSQL export connectivity")
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    client = APIClient(args.base_url, os.environ.get("WEKNORA_API_KEY"))
    if args.check:
        return check_environment(client, args.check_db)
    try:
        return run_experiment(args)
    except ExperimentError as exc:
        print(f"[experiment] FAILED: {exc}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("[experiment] interrupted", file=sys.stderr)
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
