# 脚本化 C1 实验环境

本目录是冲突检测 V2 的**研究实验入口**。它通过真实的 WeKnora HTTP API 创建临时实验 KB、
注入 Markdown 或上传经人工选择的文件、等待 Asynq 任务完成，并只读导出 PostgreSQL 中的 `claims`、
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

对于 C1/C2 的可控 Markdown 消融，仍建议使用 manual API，避免 DocReader 格式转换成为混杂变量。对于 C4.10 已人工审核的真实 `pdf/doc/docx` 小样本，场景可显式设置 `ingest_mode=file`；运行器会调用同一份真实 multipart 文件 API，并等待正常的 DocReader → Asynq → claim/detect 链路。它不是未审文件夹的一键批量上传入口。

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

真实正/负例和 durable artifact 审计见
[C4.8 显式胜方撤销重开评估报告](../../docs/冲突检测V2-C4.8-显式胜方撤销重开评估报告.md)。

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

### C4.9 lifecycle 多 replicate 矩阵

C4.9 不是另一个自动裁决功能，而是 C4.6/C4.7/C4.8 的**独立重复执行 + policy matrix**评估工具。
它明确使用 `replicate` 而不是 `seed`：当前 provider API 没有可移植 RNG seed 控制。

```bash
# 默认 5 个 policy cases × 3 个 independent replicates
make experiment-c49

# 可按模型成本调整
make experiment-c49 REPLICATES=5
```

默认 matrix 覆盖：同 issuer ordered 两轮 adopt→reopen、同 issuer out-of-order、cross issuer、
date/version direction disagreement、date/version tie。每个 replicate 均创建 fresh temporary KB，且
所有 mutation 都仍通过 public HTTP API；matrix driver 不直接写 PostgreSQL。任一 detector artifact 的
`dead_letter_count != 0` 会令该 case 失败并汇总到 matrix summary。

输出位于 Git-ignored 的：

```text
experiments/comparisons/<timestamp>-c49_winner_lifecycle_matrix-<commit>/
├── matrix_summary.json
├── matrix_summary.md
├── matrix_results.json
├── winner_lifecycle_review.csv
└── replicates/<case>/replicate-XX/
```

`winner_lifecycle_review.csv` 为双审阅与仲裁保留 `reviewer_1_*`、`reviewer_2_*`、`adjudicated_*` 列；
填写后可运行：

```bash
make experiment-c49-review REVIEW=experiments/comparisons/<c49-run>/winner_lifecycle_review.csv
```

默认 controlled policy matrix 的 P/R 只能描述已标注场景的 policy/integration correctness，不能称为真实语料
泛化或人类准确率。review summary 也不会把空白、uncertain 或 reviewer disagreement 偷偷计为正确。正式默认 matrix
已完成 `15/15`、`9/9` lifecycle cycles，并由后验查询确认 `15 × dead_letter_count=0`；详见
[C4.9 Winner Lifecycle 多 Replicate 评估报告](../../docs/冲突检测V2-C4.9-生命周期重复实验评估报告.md)。

### C4.10 synthetic PDF/DOCX fixture（先验通路检查）

若真实文档暂时不适合作为研究样本，先不用自己的文件夹。仓库内置了 9 份极小、虚构的 DOCX 核心文件和 1 份独立 PDF，覆盖：

```text
同 issuer 三版本唯一 winner（3 DOCX，乱序上传）
cross issuer → no proposal
date/version direction disagreement → no proposal
same date/version tie → no proposal
单 PDF → DocReader / claim ingress smoke
```

先检查 fixture 哈希，再依次跑严格 DOCX fact/winner smoke 和独立 PDF parse/claim smoke：

```bash
make experiment-c410-docreader-fixture
make experiment-c410-docreader-smoke
make experiment-c410-docreader-pdf-smoke
```

两个 smoke 都只执行检测和 proposal，所有内容均在新临时 KB 中；它们不会执行 adoption/reopen。需要继续验证完整
C4.6/C4.7/C4.8 binary-file lifecycle 时再执行：

```bash
make experiment-c410-docreader-lifecycle REPLICATES=1
```

已记录的真实服务结果：DOCX lifecycle fixture 3 independent replicates 为 `12/12`，
`proposal precision/recall=1.0/1.0`、`lifecycle cycles=3/3`；独立 PDF claim smoke 为 `claims=1`。详情见
[C4.10 Synthetic DocReader 二进制集成评估报告](../../docs/冲突检测V2-C4.10-Synthetic-DocReader集成评估报告.md)。

这些是 controlled synthetic integration fixtures，绝不能作为真实语料、人工审阅或外部泛化指标。

### C4.10 扩展 synthetic policy corpus（development / holdout）

若当前目标是扩展**受控 policy coverage**而不是清洗真实文档，可自动生成 12 development + 12 holdout 的虚构 DOCX
事实家族（默认 64 个 source 文件）：

```bash
CORPUS_ROOT="$HOME/weknora-private-corpus/synthetic-policy-v1"

make experiment-c410-synthetic-corpus \
  OUTPUT="$CORPUS_ROOT"

make experiment-c410-plan \
  CORPUS="$CORPUS_ROOT/corpus.json" \
  OUTPUT="$CORPUS_ROOT/plan"
```

生成器覆盖同 issuer winner、乱序上传、date-only、version-only、cross issuer、mixed issuer、metadata missing、
direction disagreement、tie 与 interval overlap。每个 case 是一个明确的 fact family；development/holdout 不复用
subject、family ID 或 document path。它仍保留刻意简单的受控句法，不能称为现实语义泛化。默认 24 个 families；若需要
30-family controlled pilot，可传 `DEV_FAMILIES=15 HOLDOUT_FAMILIES=15`（每个 split 最多 20）。

先只跑 development 一次；修复问题并冻结代码/模型/metadata policy 后才跑 holdout：

```bash
python3 scripts/experiments/run_winner_lifecycle_eval.py \
  --matrix "$CORPUS_ROOT/plan/winner_lifecycle_matrix.development.json" \
  --replicates 1 \
  --output-dir "$CORPUS_ROOT/runs/development-r1"

python3 scripts/experiments/run_winner_lifecycle_eval.py \
  --matrix "$CORPUS_ROOT/plan/winner_lifecycle_matrix.holdout.json" \
  --replicates 3 \
  --output-dir "$CORPUS_ROOT/runs/holdout-r3"
```

对任一完成的 matrix，生成 fact-family 级 baseline 报告：

```bash
make experiment-c410-fact-eval \
  MATRIX_RUN="$CORPUS_ROOT/runs/holdout-r3"
```

它输出 C4.6、`latest_upload`、`date_only`、`version_only`、`raw_c3_local_vote` 的 execution-level 与
strict-all-replicates fact-family 指标、replicate stability、cluster 形状和 cascade 成本。默认 corpus 的真实服务结果为：

development `12/12`，完整 holdout `12 families × 3 = 36/36`、`18/18` lifecycle cycles、有效 detector dead letters 为零。
在该构造 policy holdout 上，C4.6 fact-family accuracy 为 `1.000` / unsafe actions `0`；raw C3 local vote 为 `0.833` / `2`，
date-only/version-only 为 `0.667` / `3`，latest-upload 为 `0.167` / `6`。完整设计与范围见
[C4.10 扩展 Synthetic Policy 语料技术设计](../../docs/冲突检测V2-C4.10-扩展SyntheticPolicy技术设计.md) 和
[C4.10 扩展 Synthetic Policy 评估报告](../../docs/冲突检测V2-C4.10-扩展SyntheticPolicy评估报告.md)。

### 公开数据集外部迁移评测（WikiFactDiff + VitaminC）

公开评测用于补充当前的 controlled synthetic evidence，但两个数据集对应**不同任务**，不得合并为一个“总体准确率”：

- **WikiFactDiff**：公开 Wikidata 两个时间快照的事实更新。适配器仅选严格的 `obsolete/forget → new/learn` replacement 作为 conflict 正例，并从显式 `static/keep` fact 构造完全相同文本的窄 no-conflict control；实际 release decision 会记录在 source manifest，未知词不猜测。它产出 C1/C2 pair transfer manifest，以及一个单独的两快照 C3/C4.6 advisory proposal manifest。后者把 `Publisher: Wikidata`、日期和版本**派生自数据集 release 的快照时间**，因此只检验 public temporal-fact / two-source proposal transfer，不检验原生文档 header、multi-authority abstention、C4.7 adoption 或 C4.8 reopen。
- **VitaminC real split**：公开 Wikipedia revision 派生的 claim--evidence 样本。`REFUTES → conflict`、`SUPPORTS → no-conflict`，只评估 C1/C2 pair transfer；不为它伪造 C3/C4.6 winner metadata。

两个适配器都会记录输入 SHA-256、上游 URL/config/revision（可获得时）、确定性 selection seed、结构性过滤理由、fact-family split 和完整/partial source scan 状态。它们只读公开数据并写入仓库外目录；不会访问 API、模型、Asynq 或数据库。**不要**把下载压缩包、派生正文或运行 artifacts 提交到 Git。

先准备一个仓库外目录：

```bash
PUBLIC_ROOT="$HOME/weknora-public-data"
mkdir -p "$PUBLIC_ROOT"
chmod 700 "$PUBLIC_ROOT"
```

#### A. WikiFactDiff：公开时序事实 pair / two-snapshot proposal

官方 Hugging Face release 可由 `datasets` 流式读取。adapter 默认固定在 dataset revision `bb17ffbff7b2d28e4cd12e251af3db50d7fa18ea`，并使用 dataset card 标为 recommended 的 `20210104-20230227_legacy` config；不使用 card 标为 “DO NOT USE IT” 的改进中间 config。首次缺包时：

```bash
python3 -m pip install --user datasets
```

生成固定的 10 positive + 10 negative development、30 + 30 holdout pair cases；同一批 replacement 同时生成 two-snapshot proposal cases：

```bash
WFD_ROOT="$PUBLIC_ROOT/wikifactdiff-20210104-20230227-legacy-v1"

make experiment-public-wikifactdiff-plan \
  PUBLIC_OUTPUT="$WFD_ROOT" \
  DEV_PER_LABEL=10 \
  HOLDOUT_PER_LABEL=30 \
  PUBLIC_VARIANT=c2-rules
```

若已下载/导出 JSONL，则不需要 `datasets`，改为：

```bash
make experiment-public-wikifactdiff-plan \
  PUBLIC_OUTPUT="$WFD_ROOT" \
  WFD_INPUT="/path/to/wikifactdiff.jsonl" \
  DEV_PER_LABEL=10 \
  HOLDOUT_PER_LABEL=30 \
  PUBLIC_VARIANT=c2-rules
```

先只运行 development 的 10-case smoke；`dry-run` 不触发服务：

```bash
make experiment-public-pair-dry-run \
  PUBLIC_MANIFEST="$WFD_ROOT/pair_eval_manifest.json" \
  SPLIT=development \
  PUBLIC_MAX_CASES=10

make experiment-public-pair-eval \
  PUBLIC_MANIFEST="$WFD_ROOT/pair_eval_manifest.json" \
  SPLIT=development \
  PUBLIC_MAX_CASES=10 \
  PUBLIC_OUTPUT="$WFD_ROOT/runs/pair-development-smoke"
```

检查 `metrics.json`、`report.md`、失败 case 的 `detector_command.log`。只允许根据 development 修复 adapter/配置；随后固定 commit、model configuration 和 `source_manifest.json`，再运行 untouched holdout。首轮建议每个 fact family 一次，避免把 provider 的独立执行误当作独立数据样本：

```bash
make experiment-public-pair-eval \
  PUBLIC_MANIFEST="$WFD_ROOT/pair_eval_manifest.json" \
  SPLIT=holdout \
  PUBLIC_REPLICATES=1 \
  PUBLIC_OUTPUT="$WFD_ROOT/runs/pair-holdout-r1"

# 单独、预先由 selection_rank 固定的 10-family stability slice；不与 full holdout 池化。
make experiment-public-pair-eval \
  PUBLIC_MANIFEST="$WFD_ROOT/pair_eval_manifest.json" \
  SPLIT=holdout \
  PUBLIC_MAX_CASES=10 \
  PUBLIC_REPLICATES=3 \
  PUBLIC_OUTPUT="$WFD_ROOT/runs/pair-holdout-stability-r3"
```

C3/C4.6 public two-snapshot proposal transfer 也必须独立报告：

```bash
make experiment-public-pair-eval \
  PUBLIC_MANIFEST="$WFD_ROOT/proposal_eval_manifest.json" \
  SPLIT=development \
  PUBLIC_MAX_CASES=10 \
  PUBLIC_OUTPUT="$WFD_ROOT/runs/proposal-development-smoke"

# 仅在 development protocol 冻结后执行。
make experiment-public-pair-eval \
  PUBLIC_MANIFEST="$WFD_ROOT/proposal_eval_manifest.json" \
  SPLIT=holdout \
  PUBLIC_REPLICATES=1 \
  PUBLIC_OUTPUT="$WFD_ROOT/runs/proposal-holdout-r1"
```

proposal manifest 使用“exact expected winner + exact source count”的成功率，而不是 precision/abstention：WikiFactDiff 公开 release 只有两快照，且没有 multi-authority/no-proposal 标签。

#### B. VitaminC：公开 Wikipedia revision claim--evidence pair

从 VitaminC 官方项目的 release link 下载 archive 到仓库外路径，例如：

```bash
VITAMINC_ZIP="$PUBLIC_ROOT/vitaminc.zip"
curl -L --fail --retry 3 \
  -o "$VITAMINC_ZIP" \
  "https://github.com/TalSchuster/talschuster.github.io/raw/master/static/vitaminc.zip"

# 仅用于确认 archive 中 real dev/test JSONL 的成员名，不打印正文。
unzip -Z1 "$VITAMINC_ZIP" | grep -Ei 'real.*(dev|valid|test).*jsonl'
```

适配器会尝试自动选择包含 `real` 的 dev/test JSONL；若 archive 布局变动，明确传入 member name，而不要让脚本猜测：

```bash
VITAMINC_ROOT="$PUBLIC_ROOT/vitaminc-real-v1"

make experiment-public-vitaminc-plan \
  VITAMINC_DEVELOPMENT="$VITAMINC_ZIP" \
  VITAMINC_HOLDOUT="$VITAMINC_ZIP" \
  PUBLIC_OUTPUT="$VITAMINC_ROOT" \
  DEV_PER_LABEL=10 \
  HOLDOUT_PER_LABEL=30 \
  PUBLIC_VARIANT=c2-rules

# 仅在自动探测失败时追加，例如：
# VITAMINC_DEVELOPMENT_MEMBER='.../real/dev.jsonl' \
# VITAMINC_HOLDOUT_MEMBER='.../real/test.jsonl'
```

运行顺序与 WikiFactDiff 一致：development 10-case smoke → 固定 protocol → 一次完整 holdout；若需稳定性，再把预定的前 10 个 holdout fact families 独立跑 3 次，不与完整 holdout 池化：

```bash
make experiment-public-pair-eval \
  PUBLIC_MANIFEST="$VITAMINC_ROOT/pair_eval_manifest.json" \
  SPLIT=development \
  PUBLIC_MAX_CASES=10 \
  PUBLIC_OUTPUT="$VITAMINC_ROOT/runs/development-smoke"

# development 结论冻结后：
make experiment-public-pair-eval \
  PUBLIC_MANIFEST="$VITAMINC_ROOT/pair_eval_manifest.json" \
  SPLIT=holdout \
  PUBLIC_REPLICATES=1 \
  PUBLIC_OUTPUT="$VITAMINC_ROOT/runs/holdout-r1"
```

`run_public_pair_eval.py` 会先检查模板 KB 配置，并执行一次已有的只读 `run_claims_eval.py --check --check-db` service/migration preflight；preflight 失败时不会启动任何 case，日志写入 `<output>/service_preflight.log`。随后它在每个 case 创建 fresh temporary KB，复用真实 HTTP API → Asynq → PostgreSQL 导出链路。它会分别输出 execution-level 指标、primary strict-all-replicates fact-family 指标、dead-letter 完整性、cascade aggregate 和每个 case 的 immutable artifact 路径。任何缺失/失败 artifact 都是 `UNEVALUABLE`，不会被误计为 TN；完整 headline P/R/accuracy 会置为 `null`，仅保留 conditional 指标。

若早期 public-pair run 的 `metrics.json.cascade.totals` 异常全为零，但每个 detector 的 `metrics.json.cascade.totals` 实际存在数值，不要重跑模型。使用只读、保留旧 summary 备份的修复工具：

```bash
make experiment-public-pair-resummarize \
  PUBLIC_RUN="$WFD_ROOT/runs/pair-development-r1"
```

它只读取 `execution_results.json` 的原始 per-case cascade artifact，先备份原 `metrics/report/fact-family` summary 到 `resummarization_backups/`，再重算聚合；不访问 HTTP、模型、Asynq、Docker 或数据库。

论文里只能写为：**public benchmark transfer on WikiFactDiff/VitaminC-derived text under this adapter**。它仍不是 enterprise-document accuracy、native PDF/DOCX accuracy、human-review accuracy、end-to-end RAG QA accuracy 或 seed-controlled causal study。VitaminC 的上游 license/attribution 必须随发布版核验并保留；WikiFactDiff 的上游 release/config/revision 也必须写入 appendix/replication package。

### C4.10 真实语料 corpus / holdout plan

若真实文档已集中在一个目录，先不移动、不上传，做 filename/hash inventory：

```bash
DOC_ROOT="/path/to/your/existing/documents"
CORPUS_ROOT="$HOME/weknora-private-corpus"
mkdir -p "$CORPUS_ROOT"
chmod 700 "$CORPUS_ROOT"

make experiment-c410-inventory \
  DOC_ROOT="$DOC_ROOT" \
  OUTPUT="$CORPUS_ROOT/inventory"
```

它递归扫描 `pdf/doc/docx/rtf/html/md/markdown/txt`，输出 SHA-256 duplicate groups、filename-derived
version/date hints 和 `family_candidates.csv`；不导出正文，也不调用模型、API、Asynq 或数据库。filename hints
只能帮助分组，正式 issuer/date/version evidence 必须人工核对原始 title/header。

对于真实文件夹，下一步不应手工处理全部文件。先从盘点生成**去重、非占位文件的可编辑选材表**：

```bash
make experiment-c410-prepare \
  INVENTORY="$CORPUS_ROOT/inventory/document_inventory.csv" \
  OUTPUT="$CORPUS_ROOT/selection"
```

它输出 `corpus_selection.csv`、`directory_triage.csv` 和 `same_filename_families.csv`。默认排除 duplicate hash
以及小于 1024 bytes 的 canonical 文件；原 inventory 仍完整保留。打开 `corpus_selection.csv` 后，只对少量真正
相关的来源填写 `include=yes`、同一 `case_id` / `fact_family_id`、split、expected outcome、conflict partner、以及
**从文档 title/header 人工确认**的 `metadata_title` 和证据位置。该步骤不读取正文。

填写完成后，无需手写 JSON：

```bash
make experiment-c410-materialize \
  SELECTION="$CORPUS_ROOT/selection/corpus_selection.csv" \
  CORPUS="$CORPUS_ROOT/my_winner_corpus.json" \
  NAME=winner_lifecycle_real_pilot

make experiment-c410-plan \
  CORPUS="$CORPUS_ROOT/my_winner_corpus.json" \
  OUTPUT="$CORPUS_ROOT/generated-plan"
```

materializer 只接受已验证的显式标注；会拒绝跨 split 的 fact/source 泄漏、binary 文件误走 manual、未确认 metadata、
无 conflict pair、正例少于 3 sources 或多个 winner。`pdf/doc/docx` 会生成 `ingest_mode=file`，因此运行
C4.9 matrix 时 `run_claims_eval.py` 通过真实 multipart API 上传**仅选中的**文件，等待 DocReader / Asynq，且在
上传前核验 inventory 中保存的 SHA-256。它不会把二进制文件当 Markdown 直接读取。

真实文档可放在仓库外的受控路径；`experiments/corpus_plans/` 已被 Git 忽略。完整数据格式、双审阅协议和论文门槛见
[C4.10 真实版本语料与双审阅协议](../../testdata/winner_lifecycle_corpus/README.md)。

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

### 已人工选定的 PDF/DOC/DOCX 来源

默认 `ingest_mode` 是 `manual`，仅适用于 `.md/.markdown/.txt/.html/.htm`。对真实二进制文件必须显式写
`"ingest_mode": "file"`，例如：

```json
{
  "id": "revision_v3",
  "path": "/private/corpus/policy_v3.pdf",
  "title": "发布机构：某单位；生效日期：2026年3月25日；版本号：V3.0",
  "ingest_mode": "file",
  "source_sha256": "<inventory 中的 64 位 SHA-256>"
}
```

`file` 模式以流式 multipart 请求调用 `POST /knowledge-bases/:id/knowledge/file`，再等待正常的
DocReader → Asynq → claim extraction → conflict detection。它不会把 PDF/DOC/DOCX 字节当作 UTF-8 Markdown
读取；运行前会核验 `source_sha256`（若提供）以阻止已标注来源静默漂移。为让 C3/C4.6 得到可审计 metadata，
`title` 必须由人工从原始 title/header 核实后写成显式 `发布机构/生效日期/版本号` 标签；文件名或目录名本身不构成
该证据。运行器会保留原扩展名，并在未显式设置 `upload_file_name` 时追加非语义的 `；文档标识：<document_id>`，
避免 metadata tie 的两个不同来源因同名上传被 API 拒绝；C3 会忽略该未知标签，仍只读取 issuer/date/version。
若自行提供 `upload_file_name`，则必须保证同一实验 KB 内每份文件名唯一。

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
