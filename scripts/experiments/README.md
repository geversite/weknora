# 脚本化 C1 实验环境

本目录是冲突检测 V2 的**研究实验入口**。它通过真实的 WeKnora HTTP API 创建临时实验 KB、
注入 Markdown、等待 Asynq 任务完成，并只读导出 PostgreSQL 中的 `claims`、
`knowledge_conflicts`、`disputed_facts`、处理 spans 与 dead letters。

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
3. Docker/psql 是否能执行只读 `SELECT 1`；
4. C2 的 `conflict_detection_runs`、C4 的 `disputed_facts`、C4.5/C4.7 status 宽度、C3 suggestion、C4.6 winner proposal，以及 C4.8 durable adoption 列/表均已由 migration 创建。

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

### C2 级联消融

后端运行 C2 migration 000086 后，使用相同的 `c1_full` 场景运行：

```bash
make experiment-c2-rules   # C2-A：规则层 + C1 单对 LLM
make experiment-c2-batch   # C2-B：规则层 + 批量 LLM
```

每次 run 除原有 claims/conflicts 外，还输出：

```text
conflict_detection_runs.json
cascade_metrics.json
```

其中包含 claim/fallback 候选数、规则放行/直判数、LLM pair 数、batch/single 调用数、
token 与延迟。运行器会拒绝缺少 `conflict_detection_runs` 的环境；重启后端以执行
migration 000086 即可。

C2-B 的正例是 **proof-carrying** 的：每个 `conflict=true` 的 batch verdict 都必须给出
片段 A/B 各一段连续原文引文，运行时会核验引文确实来自对应片段。语义 fallback 仅表示
检索相关，文件未提及某事实、风险与防护措施、建议与计划、不同时间阶段的记录均不能单独
构成冲突。缺 ID、重复 ID、非 JSON 等结构性问题会让整个 batch 安全降级为 C1 单对裁决；
只有个别正例的引文缺失、改写或不属于对应片段时，会在一次 batch 重试后**仅降级该 pair**，
不让一个坏条目额外触发整批单对调用。

对于没有 exact `claim_key` 的 semantic fallback，C2-B 会从两侧 source chunk 中选取至多两组
高词槽相似的声明线索（例如“测试时间”与“计划开始时间”）。线索只帮助 batch 定位可能的
同一事实，不触发规则直判；正例仍必须由 A/B 原文引文落地，避免把 schema drift 修复成开放世界误报。

### C1/C2 离线消融比较

完成同一场景的 V1、C1、C2-A 和 C2-B run 后，显式传入四个 artifact 目录：

```bash
make experiment-c2-compare \
  RUNS="experiments/runs/<v1-run> experiments/runs/<c1-run> experiments/runs/<c2-rules-run> experiments/runs/<c2-batch-run>"
```

比较器不访问 API、数据库或模型，只读取每个 run 的 `manifest.json`、`metrics.json` 和
`cascade_metrics.json`，默认以 `variant=c1` 为成本基线，输出到 Git 忽略的：

```text
experiments/comparisons/<timestamp>-conflict-ablation/
├── comparison.json
└── comparison.md
```

报告将检测完整性（预期对、禁止对、死信、task failed）与 volatile 的 claim-extractor P/R
门槛分开呈现；它是可重现 artifact 汇总，不把独立生产模型运行误称为严格因果实验。

### C3-Lite 版本与发布机构建议

后端运行 PostgreSQL migration `000090` 后：

```bash
make experiment-c3
```

该场景注入：

```text
天穹财团 V1.0，生效日期 2148-01-01，餐补 100 元
天穹财团 V2.0，生效日期 2148-06-01，餐补 150 元
新弦工业 V3.0，生效日期 2149-01-01，餐补 200 元
```

预期：同发布机构的 V2 ↔ V1 raw conflict 出现 advisory：

```text
suggested_resolution = resolved_newer_wins
confidence >= 0.95
```

跨发布机构的冲突仍可被检出，但**不得**出现 winner suggestion。C3-Lite 只从 title/header
中的显式发布机构、生效日期、版本号解析 metadata；不从正文任意事实日期猜文档版本，也不
自动更新 status 或禁用 chunk。

每个 run 新增导出：

```text
version_suggestions.json
```

`metrics.json` 会记录 expected / missing / forbidden version suggestions；任何 suggestion
断言失败均以退出码 `2` 保留 artifact。

### C3/C4.6 DisputedFact 全局 winner proposal

C4.6 不执行 winner resolution，只在一个已聚类的 `DisputedFact` 上提出唯一全局胜方：

```bash
make experiment-c46
```

场景使用三份同发布机构、同一餐补事实、严格升序元数据的文档：

```text
V1.0 / 2148-01-01 / 100 元
V2.0 / 2148-06-01 / 150 元
V3.0 / 2149-01-01 / 200 元
```

预期一个 cluster 和一个 proposal：

```text
suggested_winner_knowledge_id → c46_v3
winner_proposal_confidence >= 0.95
winner_proposal_source_count = 3
raw member status 仍为 pending
auto_resolved 仍为 false
```

导出：

```text
winner_proposals.json
```

跨发布机构负例使用：

```bash
make experiment-c46-negative
```

它要求 exact conflict / cluster 仍存在，但：

```text
expected_disputed_fact_winner_count = 0
```

任何发布机构不一致、metadata 缺失/冲突、不可比较时间区间或并列最大版本都会得到空 proposal。

### C4.7 显式全局 winner adoption

真实正/负例与 artifact 审计见
[C4.7 显式全局胜方采纳评估报告](../../docs/冲突检测V2-C4.7-显式全局胜方采纳评估报告.md)。

C4.7 是 C4.6 proposal 的**唯一采纳入口**；C4.8 只允许回滚一条已有 adoption，不能创建新 winner。原始 C4.7 core 复用 C4.5 已验证的
`knowledge_conflicts.status VARCHAR(32)`；当前 C4.8 durable/reopen extension 要求 PostgreSQL `000092`
/ SQLite `000013`，以记录 adoption ID、成员和 chunk targets。它不自动执行：调用方必须先通过
`GET /conflicts/clusters` 审阅当前 proposal，再把完整快照提交到：

```text
POST /api/v1/knowledge-bases/:id/conflicts/clusters/adopt-winner
```

请求必须回显：

```json
{
  "disputed_fact_id": "<cluster id>",
  "expected_winner_knowledge_id": "<current suggested_winner_knowledge_id>",
  "expected_proposal_version": "c3-c4-v1",
  "expected_proposal_updated_at": "<current disputed fact updated_at>",
  "expected_proposal_source_count": 3,
  "note": "optional human audit note"
}
```

服务在一个数据库 transaction 中锁定并重新读取 DisputedFact、全部 member raw conflicts 和
全部 member chunks。以下任一情况均以 HTTP `409` 拒绝且不写入：proposal 缺失/改变、`updated_at`
快照过期、source/member count 不一致、任一 member 已被局部裁决、winner 不在当前 sources、任一
member chunk 已禁用，或 chunk ownership 与 raw evidence 不一致。

成功时仅允许 exact `claim_key` cluster：

```text
所有 pending raw members → status=resolved_global_winner
AutoResolved → false
winner source chunks → 保持 enabled
所有 non-winner source 的 member chunks → disabled
DisputedFact → status=resolved / pending_conflict_count=0
resolution_note → 包含 winner、proposal version、source count 与人工 note
```

`resolved_global_winner` 刻意不编码 raw A/B 方向，因为例如 V2 ↔ V1 member 本身可能不含全局
V3 winner。C4.7 不调用 generic `resolve` 或 C4.5 `/clusters/resolve`，不会把局部
`resolved_newer_wins` / `resolved_older_wins` 当成全局命令。它也不新增 wiki dispute block
或 agent 写回；那仍是后续工作。

在 fresh、尚未采纳的 C4.6 run 上运行：

```bash
make experiment-c46
POS_RUN="$(ls -td experiments/runs/*-c46_global_winner_triplet-c2-rules-* | head -1)"
make experiment-c47 RUN="$POS_RUN"
```

正例脚本首先故意发送一个错误 proposal version，要求 HTTP `409` 且 raw members/chunks 完全不变；
然后才发送从 live `GET /clusters` 读取的精确快照。成功后它只读 PostgreSQL 验证：全部 member
为 `resolved_global_winner`、winner chunk 未禁用、所有 loser member chunks 已禁用、aggregate
已收敛为 `resolved`。

跨 issuer / 无 proposal 负例：

```bash
make experiment-c46-negative
NEG_RUN="$(ls -td experiments/runs/*-c46_cross_issuer_no_proposal-c2-rules-* | head -1)"
make experiment-c47-negative RUN="$NEG_RUN"
```

它必须得到 HTTP `409`，并验证没有 raw status 或 chunk enable state 被改变。产物分别写入：

```text
<positive-run>/winner_adoption.json
<negative-run>/winner_adoption_negative.json
```

### C4.8 显式撤销 / reopen winner adoption

C4.8 不会自动恢复任何 disabled chunk。它只能回滚 **migration `000092` 后由 C4.7 创建的 active
adoption record**，并要求 caller 从当前 resolved cluster 回显：

```text
active_winner_adoption_id
updated_at
```

API：

```text
POST /api/v1/knowledge-bases/:id/conflicts/clusters/reopen-winner
```

成功时只会重新启用该 durable record 当初禁用的 chunks，并把同一 adoption 的 raw members 恢复为
`pending`。record 本身会标记为 `revoked`，不会删除原始 winner / member / chunk evidence。任何 stale
snapshot、无 active adoption、member set 变化、target chunk 被另一个 global adoption 占用、chunk 在 adoption
后被改动，都会得到 HTTP `409` 且不写入。

```bash
# Fresh proposal → durable C4.7 adoption → C4.8 reopen
make experiment-c46
POS_RUN="$(ls -td experiments/runs/*-c46_global_winner_triplet-c2-rules-* | head -1)"
make experiment-c47 RUN="$POS_RUN"
make experiment-c48 RUN="$POS_RUN"

# A pending cross-issuer cluster has no active adoption and must be rejected.
make experiment-c46-negative
NEG_RUN="$(ls -td experiments/runs/*-c46_cross_issuer_no_proposal-c2-rules-* | head -1)"
make experiment-c48-negative RUN="$NEG_RUN"
```

正例 verifier 先故意发送 stale `expected_disputed_fact_updated_at`，要求 HTTP `409` / no mutation；随后
执行精确 reopen，并只读 PostgreSQL 验证：record `adopted → revoked`、全部 member `resolved_global_winner → pending`、
`winner_adoption_id` 清空、recorded loser chunks `disabled → enabled`、DisputedFact `resolved → pending`。

产物：

```text
<positive-run>/winner_reopen.json
<negative-run>/winner_reopen_negative.json
```

migration `000092` 前的 C4.7 adoption 没有 durable record，C4.8 会明确拒绝它，而不会尝试从
`resolution_note` 猜测应恢复哪些 chunks。

### C4-Lite 事实级聚类

后端运行 C4 migration `000088` 后，运行三文档同事实三取值场景：

```bash
make experiment-c4
```

场景依次注入 100 / 150 / 200 元三份“国内出差餐费补贴每日标准”文档。预期产生三个
跨文档 raw conflict 对，但聚为一个 `DisputedFact`：

```text
C4_AB / C4_AC / C4_BC：全部命中
expected_disputed_fact_count：1
expected_disputed_fact_anchor_kinds：{"claim_key": 1}
```

另一个 schema-drift fallback 回归（报销申请 30 个自然日 ↔ 报销单 45 天）使用：

```bash
make experiment-c4-fuzzy
```

它要求同一 semantic fallback 的 raw conflicts 聚为一条 `document_singleton` cluster：

```text
expected_disputed_fact_count：1
expected_disputed_fact_anchor_kinds：{"document_singleton": 1}
```

`document_singleton` 只在两份已检出冲突的文档各恰有一条 usable claim 时启用；它是
C4 的 post-verdict 身份锚点，不参与 C1/C2 的候选或判定。

每个 run 会调用一次幂等的：

```text
POST /api/v1/knowledge-bases/:id/conflicts/clusters/rebuild
```

然后导出：

```text
disputed_facts.json
cluster_rebuild.json
cluster_metrics.json
```

`cluster_metrics.json` 中的 `review_units_saved = raw_conflict_count - cluster_count` 是 C4-Lite
“可减少的人工裁决单元”口径。它不等于最终人工时间节省，且对无 claim anchor 的旧 row
会保守地使用 `chunk_pair` 单例 anchor，不做不安全的跨 chunk 合并。

### C4.5 安全 cluster 级裁决传播

C4.5 需要 PostgreSQL migration `000089_conflict_status_width`：旧 M3 的
`knowledge_conflicts.status VARCHAR(20)` 放不下 21 字符的 `resolved_not_conflict`；SQLite 的
`TEXT` 无宽度限制，对应版本标记为 SQLite migration `000010`。

C4.5 把一次**无副作用**裁决传播到一个 `DisputedFact` 的全部 pending raw members：

```bash
make experiment-c4-resolve RUN=experiments/runs/<c4-run>
```

默认 resolution 是 `resolved_keep_both`；也可验证 false-positive 关闭路径：

```bash
make experiment-c4-resolve \
  RUN=experiments/runs/<c4-run> \
  RESOLUTION=resolved_not_conflict
```

脚本通过 public API 调用：

```text
POST /api/v1/knowledge-bases/:id/conflicts/clusters/resolve
```

并只读验证每个原本 pending 的 member row 已统一更新，`DisputedFact` 已 rebuild 为
`status=resolved`、`pending_conflict_count=0`。它不直接写数据库。

C4.5 **只允许**：

```text
resolved_keep_both
resolved_not_conflict
```

`resolved_newer_wins` / `resolved_older_wins` 目前会被明确拒绝：cluster 内 member 的 A/B
方向可能不同，不能在没有 C3 文档版本/权威元数据的情况下逐 pair 套用“新/旧胜出”。

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
├── version_suggestions.json
├── disputed_facts.json
├── cluster_rebuild.json
├── cluster_metrics.json
├── winner_proposals.json
├── dead_letters.json
├── metrics.json
└── report.md
```

`experiments/runs/` 默认被 Git 忽略。只有确认过的小型汇总结果才应手工整理进
`testdata/claims_eval/runs/` 或论文图表目录。

退出码：

- `0`：预期文档对出现、所有 `forbidden_conflict_document_pairs` 均未出现、C3 version suggestion 与 C4.6 winner proposal 断言（若配置）通过、`expected_disputed_fact_count`（若配置）匹配，且（若场景带完整 gold）`evaluate.py` 通过门槛；
- `2`：服务链路完成并导出了证据，但缺少预期冲突对、出现禁止的冲突文档对、C3/C4.6 suggestion/proposal 断言失败、聚类数量断言失败，或抽取 P/R 未达门槛；
- `1`：环境、API、任务或数据库导出失败；C4.5/C4.7/C4.8 action-verifier 对预期状态、HTTP rejection 或副作用断言失败也返回 `1`；
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
  ],
  "forbidden_conflict_document_pairs": [
    {"id": "N1", "left": "doc_a", "right": "doc_c"}
  ],
  "expected_disputed_fact_count": 1,
  "expected_disputed_fact_anchor_kinds": {
    "claim_key": 1
  },
  "expected_version_suggestions": [
    {"id": "S1", "left": "doc_b", "right": "doc_a", "resolution": "resolved_newer_wins", "min_confidence": 0.95}
  ],
  "forbidden_version_suggestion_document_pairs": [
    {"id": "S2", "left": "doc_c", "right": "doc_a"}
  ],
  "expected_disputed_fact_winner_count": 1,
  "expected_disputed_fact_winners": [
    {"id": "W1", "winner_document": "doc_b", "min_confidence": 0.95}
  ]
}
```

`gold_doc` 只在需要调用现有 `testdata/claims_eval/evaluate.py` 时填写。隔离回归场景可以
省略它，运行器仍会导出 claims/conflicts 并检查预期的文档对。

`forbidden_conflict_document_pairs` 是可选的闭集负例断言。若任何该文档对出现 raw
`knowledge_conflicts` 行，run 会保留全部证据、标记为 `completed_with_forbidden_conflicts`，并
以退出码 `2` 结束。它在 C4 去重/聚类之前按文档对报警，不把同一对的多条 chunk-pair 行误称为多项独立错误。

`expected_disputed_fact_count` 是可选 C4-Lite 聚类断言。它只适用于事实设计明确、预期 cluster
数量稳定的隔离场景；全量 `c1_full` 因 extractor 与 raw chunk-pair 输出有模型波动，不配置该断言。
`expected_disputed_fact_anchor_kinds` 可进一步断言 cluster 使用 `claim_key`、`fuzzy_slot`、
`document_singleton` 或 `chunk_pair` 的数量；它用于验证 C4 的 anchor 路径，而不是推断 LLM 裁决正确性。

`expected_version_suggestions` 是有方向的 C3 断言：`left` 对应 raw conflict A，`right` 对应 B，
因此 `resolved_newer_wins` 表示 A 是建议胜方；`forbidden_version_suggestion_document_pairs`
则按无方向文档对禁止任何 suggestion，用于跨发布机构或不可比较版本的负例。

`expected_disputed_fact_winner_count` / `expected_disputed_fact_winners` 是 C3/C4.6 的 cluster 级
断言。`winner_document` 不依赖 raw A/B 方向；它必须是该 DisputedFact 全部 source metadata
中的唯一严格最大版本。proposal 始终无副作用。

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
