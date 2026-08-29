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

### P1/P2 全上下文诊断场景

```bash
make experiment-p12
```

该场景只注入原始 `doc1`、`doc2`、`doc5` 三份完整文档，用于区分：

- strict claim-key 候选是否实际生成；
- fallback 是否返回 P1；
- LLM fine adjudication 是否将 P1/P2 判为冲突。

配合后端的 `[ConflictDetect] Coarse candidates` 与 `[ConflictDetect] Fine verdict` 日志，
它比完整六文档 run 更适合诊断候选通道和多事实上下文影响。

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

`evaluate.py` 的 P/R 是全六文档口径，因此运行器只会在场景覆盖全部 gold 文档时执行它。
P2/P3/P1-P2 这类部分语料诊断场景会明确跳过全局 P/R，避免未注入的 gold 文档被错误计为漏检。

## C1.6：导出人工审计包

完整六文档 `c1_full` run 完成后，可把 gold、prediction、严格/宽松匹配、FN、FP 与
P1-P5/N1 证据导出为人工审核包：

```bash
make experiment-audit RUN=experiments/runs/<run-id>
```

默认输出到：

```text
<run-id>/claim_audit/
├── audit_rows.csv
├── contradiction_audit.csv
├── audit_summary.json
└── README.md
```

`audit_rows.csv` 有空的 `review_label` / `review_note` 列。审阅者可标记
`schema_equivalent`、`gold_scope_mismatch`、`gold_missing_claim`、`genuine_fn`、
`low_value_fp`、`genuine_fp`、`duplicate`、`quote_failure` 或 `annotation_error`。先审查
`priority=critical` 的行，它们直接关联 P1-P5/N1。

标注完成后，先复制为 `audit_rows_reviewed.csv`，再汇总：

```bash
make experiment-audit-summary AUDIT=experiments/runs/<run-id>/claim_audit
```

汇总输出到 `claim_audit/review_summary/`：

```text
review_summary.json
review_report.md
gold_revision_candidates.csv
schema_equivalence_candidates.csv
model_improvement_cases.csv
semantic_link_review.csv
label_consistency_issues.csv
audit_rows_relabel.csv
prediction_semantic_review.csv
```

该汇总器不会把行级人工标签直接相加成 P/R；`schema_equivalent` 或 gold 漏标可能需要
prediction→gold 的显式链接，才可得到可发表的人类校正 P/R。它会将需要第二轮链接的少量行
列入 `semantic_link_review.csv`。

若审阅者把 prediction-side 标签写到了 `gold_only` 行（或反过来），汇总器会生成
`label_consistency_issues.csv`，并生成不修改原文件的 `audit_rows_relabel.csv`。在该副本中
筛选 `label_consistency_severity` 非空的行后，仅修正这些行的 `review_label` / `review_note`，
再用 `--reviewed-csv` 对该副本重跑汇总即可。

### 人工校正指标计算

完成 `prediction_semantic_review.csv` 后，使用：

```bash
make experiment-audit-metrics \
  AUDIT_CSV=experiments/runs/<run-id>/claim_audit/review_summary/audit_rows_relabel.csv \
  SEMANTIC_REVIEW=experiments/runs/<run-id>/claim_audit/review_summary_v2/prediction_semantic_review.csv
```

输出默认位于 `prediction_semantic_review.csv` 同级的 `reviewed_metrics/`：

```text
reviewed_metrics.json
reviewed_metrics.md
accepted_semantic_mappings.csv
gold_v2_additions.csv
metric_validation_issues.csv
```

指标计算器不会把 `schema_equivalent` 的双侧行重复计数：只有 reviewer 显式填写的
`equivalent_existing_gold` 链接会覆盖相应 gold。`add_gold_v2` 会作为拟议 gold-v2 条目输出，
不会自动修改仓库中的 gold 文件。仅当 `metric_validation_issues.csv` 为空且
`metric_ready=true` 时，才可将 human-adjusted P/R 作为正式研究指标。

### 生成待复核 gold-v2 候选集

在人工校正指标就绪后，先为 reviewer 标为 `add_gold_v2` 的 claims 准备 quote 补全表：

```bash
make experiment-gold-v2-review \
  ADDITIONS=experiments/runs/<run-id>/claim_audit/review_summary_v2/reviewed_metrics/gold_v2_additions.csv \
  REVIEW=experiments/runs/<run-id>/claim_audit/review_summary_v2/reviewed_metrics/gold_v2_additions_review.csv
```

若 `quote_review_status` 是 `needs_review_missing` 或 `needs_review_not_found`，从
`suggested_quote_candidates` 中选择/复制一个**原文逐字连续**的支持片段到 `review_quote`。
不要填整篇文档；quote 应尽量是一句或一个短段。已有 `ready_exact` 的行无需修改。

quote 全部复核后，才物化独立候选集：

```bash
make experiment-gold-v2 \
  ADDITIONS=experiments/runs/<run-id>/claim_audit/review_summary_v2/reviewed_metrics/gold_v2_additions_review.csv \
  OUTPUT=experiments/runs/<run-id>/claim_audit/review_summary_v2/reviewed_metrics/gold_v2_candidate
```

它会复制 immutable gold-v1 JSON 并附加新增 claims，逐条验证 quote 是否仍在原文中，输出
`provenance.json` 与 README。它**不会修改** `testdata/claims_eval/gold/`。候选集经第二位审阅者
复核后，才可推广为受版本控制的 `gold-v2`；评估器支持：

```bash
python3 testdata/claims_eval/evaluate.py \
  --run <claims_eval_run.json> \
  --gold-dir <gold_v2_candidate目录>
```

### broad-maintenance / narrow-conflict 双口径复核

gold-v2 candidate 的 quote 正确不代表每条都应算作 conflict-critical。生成只含新增条目的
双口径/去重复核表：

```bash
make experiment-gold-v2-scope-review \
  CANDIDATE=experiments/runs/<run-id>/claim_audit/review_summary_v2/reviewed_metrics/gold_v2_candidate_reviewed \
  REVIEW=experiments/runs/<run-id>/claim_audit/review_summary_v2/reviewed_metrics/gold_v2_scope_review.csv
```

对每一行填写：

```text
broad_maintenance = yes | no
conflict_critical = yes | no
dedup_decision = keep | merge | exclude
merge_into_gold_id = <仅 merge 时填写>
```

`scope_warnings` / `suggested_conflict_critical` 仅为提示，不会替代人工研究定义。典型地，
别名、项目代号、发布机构属于 broad-maintenance 或 metadata，但不应自动计入 narrow-conflict；
同一文档内同 subject+predicate 的多个新增项应检查是否只是重复表述。

若采用仓库内版本化的 C1 双口径推荐（透明 JSON，可审阅），无需逐行填写：

```bash
make experiment-gold-v2-apply-recommendations \
  REVIEW=experiments/runs/<run-id>/claim_audit/review_summary_v2/reviewed_metrics/gold_v2_scope_review.csv \
  OUTPUT=experiments/runs/<run-id>/claim_audit/review_summary_v2/reviewed_metrics/gold_v2_scope_review_recommended.csv
```

该推荐保留 7 条 broad-maintenance 独立新增事实、3 条 narrow-conflict 独立新增事实；
将“引发幻觉”合并到更具体的“导致极度幻觉”，并排除纯别名 `StarQuartz`。原 review CSV 永远不变。

根据完成的 scope review 生成最终派生物（仍不修改 full candidate 或 gold-v1）：

```bash
make experiment-gold-v2-finalize \
  CANDIDATE=<full-gold-v2-candidate目录> \
  SCOPE=<gold_v2_scope_review_recommended.csv> \
  BROAD_OUTPUT=<gold-v2-broad-candidate目录> \
  NARROW_MANIFEST=<gold-v2-conflict-manifest.json>
```

随后计算 scope/dedup 后的最终指标：

```bash
make experiment-dual-scope-metrics \
  METRICS=<reviewed_metrics.json> \
  MAPPINGS=<accepted_semantic_mappings.csv> \
  SCOPE=<gold_v2_scope_review_recommended.csv> \
  NARROW_MANIFEST=<gold-v2-conflict-manifest.json> \
  OUTPUT=<dual_scope_metrics.json>
```

`gold-v2-broad` 是可由 evaluator 使用的 JSON 目录；`gold-v2-conflict` 是仅含 P1-P5/N1
base IDs 和 narrow v2 additions 的指标 manifest。narrow precision 暂不计算，因为尚未为每一条
raw prediction 标记 critical/non-critical；narrow recall 则是严格可复现的。

## 当前边界

运行器负责“真实执行 + 可复现导出 + C1 抽取评分”。候选通道和 fine verdict 已输出到
后端结构化日志：`claim-pairs` / `fallback-pairs`、`channel=claim_key|fallback`、
`verdict=...`。后续 C2 会把规则层、批量裁决、LLM token 与 latency 纳入同一实验口径。
