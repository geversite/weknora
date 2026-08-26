# 脚本化 C1 实验环境

本目录是冲突检测 V2 的**研究实验入口**。它通过真实的 WeKnora HTTP API 创建临时实验 KB、
注入 Markdown、等待 Asynq 任务完成，并只读导出 PostgreSQL 中的 `claims`、
`knowledge_conflicts`、处理 spans 与 dead letters。

它不依赖前端 UI，也不会直接向 `claims` 或 `knowledge_conflicts` 写入数据。

## 为什么使用 manual Markdown API

`run_claims_eval.py` 默认调用：

```text
POST /api/v1/knowledge-bases/:id/knowledge/manual
```

并使用 `status=publish`。这仍然会经过真实链路：

```text
HTTP API → Knowledge 创建 → Asynq manual:process → chunking
         → claim:extract → conflict:detect → PostgreSQL
```

相比反复通过 UI 上传文件，manual Markdown 注入能固定原始文本、文档顺序和 chunk 配置，
避免 DocReader 的格式转换成为 C1/C2 算法实验的混杂变量。

文件上传 / DocReader 端到端行为应保留为少量独立 smoke test，而不是每次研究 run 的入口。

## 一次性准备

在 Linux 开发机上启动真实依赖和后端：

```bash
cd ~/weknora
git pull
make dev-start
# 新开终端，保持运行
make dev-app
```

后端启动时会自动运行数据库迁移。实验前应确认 `claims` 表已经存在，且模板 KB 配置了：

- 可用的 `summary_model_id`（C1 抽取与冲突 LLM 判定都需要）；
- 可用的 embedding/vector 配置（完整 V1/C1 对比需要）；
- 至少一个基础 indexing pipeline（vector/keyword/wiki/graph）启用。

准备一个**空的模板 KB**。运行器会复制它的模型、存储和 chunk 配置，创建一个新的
`is_temporary=true` 实验 KB，因此不会污染模板或已有实验结果。

不要在 shell history 中传入 API key；使用环境变量：

```bash
export WEKNORA_BASE_URL=http://127.0.0.1:8080
export WEKNORA_API_KEY='从 WeKnora 创建的实验 API key'
export WEKNORA_EXPERIMENT_TEMPLATE_KB='<模板 KB ID>'

# 你的 Docker 若需要 sudo（当前 dev 容器名默认正是 WeKnora-postgres-dev）
export WEKNORA_DOCKER_BIN='sudo docker'
```

可选 PostgreSQL 导出配置：

```bash
# 默认 Docker 导出模式，无需额外设置：
#   WeKnora-postgres-dev / postgres / WeKnora

# 若主机安装了 psql，也可以改用 DSN 模式：
# export WEKNORA_EXPERIMENT_PG_DSN='postgres://...'
```

## 环境检查

```bash
make experiment-check
```

或：

```bash
python3 scripts/experiments/run_claims_eval.py --check --check-db
```

检查项：

1. `GET /health` 返回 `{"status":"ok"}`；
2. API key 是否存在（不会输出其值）；
3. Docker/psql 是否能执行只读 `SELECT 1`。

## 运行场景

### 全量 C1 场景

```bash
make experiment-c1
```

等价于：

```bash
python3 scripts/experiments/run_claims_eval.py \
  --scenario scripts/experiments/scenarios/c1_full.json \
  --variant c1
```

它按固定顺序注入：

```text
doc1 → doc2 → doc3 → doc4 → doc5 → doc6
```

并输出：

- 生产模型 claims 快照；
- `evaluate.py` 的 P/R、quote、value_kind 结果；
- P1/P2/doc4↔doc6 的冲突文档对是否出现；
- raw conflicts、spans、dead letters；
- 可复现的 manifest（commit、模型 ID、KB、索引策略、文档 ID）。

### P2 claim→detect 时序隔离场景

```bash
make experiment-p2
```

该场景只含“工业级星晶供应实体”的同键异值对，用于验证：第二份文档的
`conflict:detect` 只会在其 `claim:extract` 成功落库后触发，避免多 worker 下
claim 与 detect 并发造成的漏检。

### P3 fallback 隔离场景

```bash
make experiment-p3
```

该场景只有两条事实：

```text
报销申请：30 个自然日
报销单：45 天
```

它不能依赖 doc4/doc6 中 P4/P5 的同 chunk 严格命中来“顺带”看到 P3，因此用于验证
claim-key 不命中时的语义 fallback 是否真的能产生冲突候选。

### V1 行为对照

```bash
make experiment-v1
```

`--variant v1` 在新实验 KB 上把 `claim_extract_enabled=false`，其余模板配置保持不变。
这使当前二进制回退到无 claims 的 HybridSearch 路径。它是 C1 消融的可重复对照；
若需要严格的历史二进制对照，可在另一个服务实例上启动 `main@9a7852d`，再让同一运行器指向
那个 `WEKNORA_BASE_URL`。

## 输出与退出码

每次 run 默认写入：

```text
experiments/runs/<timestamp>-<scenario>-<variant>-<commit>/
├── manifest.json
├── uploads.json
├── spans/
├── claims.json
├── claims_eval_run.json
├── evaluator_output.txt
├── conflicts.json
├── conflict_document_pairs.json
├── dead_letters.json
├── metrics.json
└── report.md
```

`experiments/runs/` 默认被 Git 忽略。只有确认过的小型汇总结果才应手工整理进
`testdata/claims_eval/runs/` 或论文图表目录。

退出码：

- `0`：预期文档对出现，且（若场景带完整 gold）`evaluate.py` 通过门槛；
- `2`：服务链路完成并导出了证据，但缺少预期冲突对或抽取 P/R 未达门槛；
- `1`：环境、API、任务或数据库导出失败；
- `130`：用户中断。

实验 KB 默认保留，便于针对其 `knowledge_id`、spans 和数据库记录排查。清理由人工确认后完成。

## 场景格式

场景使用 JSON，避免引入 PyYAML 等额外 Python 依赖：

```json
{
  "name": "scenario_name",
  "min_claims_per_document": 1,
  "documents": [
    {
      "id": "doc_a",
      "path": "testdata/.../doc_a.md",
      "gold_doc": "doc_a.md",
      "title": "doc_a"
    }
  ],
  "expected_conflict_document_pairs": [
    {"id": "P1", "left": "doc_a", "right": "doc_b"}
  ]
}
```

`gold_doc` 只在需要调用现有 `testdata/claims_eval/evaluate.py` 时填写。隔离回归场景可以
省略它，运行器仍会导出 claims/conflicts 并检查预期的文档对。

## 当前边界

第一版运行器负责“真实执行 + 可复现导出 + C1 抽取评分”。C1.5 的下一小步会在应用侧增加
结构化 `pair_channel=claim_key|fallback`、候选数、LLM 调用数与 token 指标；届时运行器将
把这些指标并入同一个 `metrics.json`，而不通过 UI 或脆弱的文本日志抓取获得它们。
