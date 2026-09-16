# C4.10 真实版本语料与双审阅协议

本目录不是默认实验数据集，而是将 C4.6/C4.7/C4.8 从 controlled policy matrix 推进到可复核真实语料的
**结构、split 和标注模板**。

- `sample_docs/` 与 `corpus.sample.json` 仅是可运行的 synthetic schema example；
- 真实、受许可或已匿名化的文档建议存放在仓库外的受控目录，并在 manifest 中使用绝对路径；
- `/secure/corpus/...` 在示例中只是占位符，不会自动存在；推荐在自己的 home 下创建私有目录；
- 不要把客户文档、未获许可法规全文、身份信息或 API Key 提交到 Git；
- 每个 case 是一个预期只聚成一个 `DisputedFact` 的事实家族；复杂多事实文档应拆成多个 case，或明确扩大
  `expected_disputed_fact_count` 并接受更复杂的标注协议。

## 0. 没有可用真实文档时：先运行 synthetic binary fixture

若现有真实文档质量不适合做首轮实验，可先使用仓库内置的
[`docreader_fixture/`](./docreader_fixture/)。它包含 9 个 DOCX 核心生命周期文件、1 个独立 PDF parse/claim
smoke 文件，以及 1 个可采纳正例和 3 个 fail-closed no-proposal 负例；每个文件只包含一条受控的虚构事实，避免大型真实文档的多事实噪声。

先校验 fixture 文件未被改变：

```bash
make experiment-c410-docreader-fixture
```

然后直接跑三文件的真实 multipart / DocReader smoke：

```bash
make experiment-c410-docreader-smoke
```

它会创建临时实验 KB、上传三份 DOCX、等待 DocReader/Asynq/claim/conflict 链路，并验证 C4.6
是否提出唯一 V3 winner；它**不会**执行 adoption/reopen 或改动你的真实 KB。随后可单独验证 PDF 入口：

```bash
make experiment-c410-docreader-pdf-smoke
```

若两项 smoke 均通过，再可运行：

```bash
make experiment-c410-docreader-lifecycle REPLICATES=1
```

后者在新临时 KB 中执行 1 个 adopt→reopen 正例以及 3 个拒绝 winner 的负例。此 fixture 仅用于 binary-file
integration/regression test；不能替代真实 corpus、双审阅或 holdout，也不能报告为真实业务文档准确率。

已记录的真实服务结果（2026-09-15）：DOCX lifecycle matrix 3 independent replicates 为 `12/12`，
`proposal precision/recall=1.0/1.0`、`lifecycle cycles=3/3`；独立 PDF claim smoke 为 `claims=1`。完整范围与限制见
[`C4.10 Synthetic DocReader 二进制集成评估报告`](../../docs/冲突检测V2-C4.10-Synthetic-DocReader集成评估报告.md)。

### 0.1 扩展 synthetic policy corpus（不使用真实文档）

若需要比 4-case fixture 更广的 controlled policy coverage，可生成一个私有的、确定性 DOCX corpus：

```bash
CORPUS_ROOT="$HOME/weknora-private-corpus/synthetic-policy-v1"

make experiment-c410-synthetic-corpus \
  OUTPUT="$CORPUS_ROOT"

make experiment-c410-plan \
  CORPUS="$CORPUS_ROOT/corpus.json" \
  OUTPUT="$CORPUS_ROOT/plan"
```

默认是 12 development + 12 holdout fact families、12 个 `adopt_reopen` 和 12 个 `no_proposal` case、64 份 DOCX。
`fact_family_catalog.csv` 说明每个 case 的 metadata topology。先用 development 运行一次并冻结规则；再运行 holdout
3 independent replicates，并使用：

```bash
make experiment-c410-fact-eval MATRIX_RUN="$CORPUS_ROOT/runs/holdout-r3"
```

生成 C4.6 与 `latest_upload` / `date_only` / `version_only` / `raw_c3_local_vote` 的事实家族级对照。默认 corpus 已在
真实服务上完成 development `12/12` 与 holdout `36/36`（3 independent replicates）；完整 holdout 的 C4.6 accuracy/unsafe 为
`1.000 / 0`，raw C3 local vote 为 `0.833 / 2`。生成语料的事实 subject 在两个 split 间不复用，但句法框架刻意简单，因此它
仍只是 controlled synthetic policy evaluation。完整规范与结果见
[`C4.10 扩展 Synthetic Policy 技术设计`](../../docs/冲突检测V2-C4.10-扩展SyntheticPolicy技术设计.md) 和
[`C4.10 扩展 Synthetic Policy 评估报告`](../../docs/冲突检测V2-C4.10-扩展SyntheticPolicy评估报告.md)。

## 1. 什么时候达到论文可写程度？

可以**现在开始写论文草稿**的引言、问题定义、架构、C1/C2 成本消融和 C4.6–C4.9 controlled evidence；但在
没有真实 holdout 和双审阅前，不应把结果部分写成系统泛化结论。

建议分三级门槛：

| 阶段 | 最小证据 | 可以做什么 |
|---|---|---|
| Draft-ready（当前接近） | 冻结系统、可重复脚本、controlled policy matrix、明确限制 | 写 introduction/method/engineering evaluation 草稿 |
| Workshop/pilot-ready | 30–50 个事实家族；至少 15 个正例与 15 个 no-proposal/ambiguous 负例；development/holdout 按 fact family 分离；两位 reviewer 覆盖 holdout | 可信的 workshop、demo 或内部研究稿 |
| Full-paper-ready | 建议 100+ facts、至少 3 个来源的正例、多个 issuer/metadata failure 模式；双审阅/仲裁、agreement、holdout、baseline/ablation、95% CI、系统性 error analysis | 考虑正式 conference/journal 投稿 |

这些不是任何特定 venue 的硬性规则；实际样本量取决于论文主张与目标 venue。关键是**按事实家族而不是按 raw
chunk-pair 拆分**，避免同一文档版本同时进入 development 和 holdout。

## 2. 创建私有 corpus 目录

先在仓库外创建一个你自己可写、不会被 Git 跟踪的目录：

```bash
CORPUS_ROOT="$HOME/weknora-private-corpus"
mkdir -p "$CORPUS_ROOT/docs"
chmod 700 "$CORPUS_ROOT"

cp ~/weknora/testdata/winner_lifecycle_corpus/corpus.sample.json \
  "$CORPUS_ROOT/my_winner_corpus.json"
```

`/secure/corpus/...` 只是文档中的示例前缀；若你的机器没有该目录，使用上面的 `$CORPUS_ROOT` 即可。

## 3. 扫描已有文档文件夹（不移动、不上传）

真实文档已经在一个文件夹时，先递归盘点，而不是手工逐个复制或直接批量上传：

```bash
DOC_ROOT="/path/to/your/existing/documents"
INVENTORY_OUT="$CORPUS_ROOT/inventory"

cd ~/weknora
make experiment-c410-inventory \
  DOC_ROOT="$DOC_ROOT" \
  OUTPUT="$INVENTORY_OUT"
```

默认扫描 `pdf/doc/docx/rtf/html/md/markdown/txt`，只输出文件名、路径、大小、mtime、SHA-256、以及从**文件名**
猜测的 version/date/family candidate；不会把正文复制到 artifact、不会调用模型/API/Asynq/PostgreSQL。

重点查看：

```text
$INVENTORY_OUT/document_inventory.csv
$INVENTORY_OUT/family_candidates.csv
$INVENTORY_OUT/inventory_summary.json
```

`family_candidate`、`version_hint`、`date_hint` 只是分组建议；issuer/date/version 的正式证据仍须从文档标题/header
人工确认。先在 `family_candidates.csv` 中筛出少量真正属于同一事实的版本族，再写入 corpus manifest。

### 3.1 大文件夹的省事选材表（推荐）

不要直接编辑 496 行 inventory，也不要上传所有文件。先生成只保留 canonical SHA-256、默认排除小于 1024 bytes
占位文件的 selection queue：

```bash
make experiment-c410-prepare \
  INVENTORY="$INVENTORY_OUT/document_inventory.csv" \
  OUTPUT="$CORPUS_ROOT/selection"
```

生成的文件：

```text
$CORPUS_ROOT/selection/corpus_selection.csv
$CORPUS_ROOT/selection/directory_triage.csv
$CORPUS_ROOT/selection/same_filename_families.csv
$CORPUS_ROOT/selection/selection_summary.json
```

先从 `directory_triage.csv` 选择很窄的主题，再在 `corpus_selection.csv` 中仅编辑相关行。每个准备入选的 row：

```text
include=yes
case_id=<同一事实的 case>
document_id=<稳定别名，默认可保留>
fact_family_id=<不会跨 split 的真实事实家族>
split=development | holdout
expected_outcome=adopt_reopen | no_proposal
winner=yes（仅 adopt_reopen，且同一 case 恰好一份）
adoption_cycles=1（仅 adopt_reopen，每行一致）
expected_conflict_with=<同 case 的 document_id；或明确的 *>
metadata_title=发布机构：...；生效日期：...；版本号：...
metadata_evidence_verified=yes
metadata_evidence_location=<封面/页码/标题行等可复核位置>
```

`*` 仅表示这份来源与同 case 的**每一份**来源都应有 conflict；不确定时必须逐个填写 partner。`metadata_title`
不是让脚本从文件名拼接：它必须是人工从原始 title/header 复核的显式证据。对于 `no_proposal`，也应填写已核实的
metadata title/证据位置（包括“确实缺失”或“不同 issuer”等负例理由），不能只凭文件名推断。即使两个 tie 来源的
issuer/date/version 完全相同，也无需在 `metadata_title` 人为编造差异；文件运行器会自动追加不参与 C3 解析的稳定
`文档标识` 段，确保 API 接收唯一 file name。

CSV 填好后，把它转为 JSON；过程仍不会读取正文或访问服务：

```bash
make experiment-c410-materialize \
  SELECTION="$CORPUS_ROOT/selection/corpus_selection.csv" \
  CORPUS="$CORPUS_ROOT/my_winner_corpus.json" \
  NAME=winner_lifecycle_real_pilot
```

materializer 会拒绝：跨 split 的 family/source 泄漏、重复 SHA-256 source、正例少于三份来源、无/多 winner、未核实
metadata、binary 误用 manual、或未明确 conflict pair。随后再运行 `experiment-c410-plan`。

## 4. Corpus manifest

复制 `corpus.sample.json`，每个 case 至少包含：

```json
{
  "id": "policy_triplet_001",
  "fact_family_id": "travel_allowance_001",
  "split": "development | holdout",
  "expected_outcome": "adopt_reopen | no_proposal",
  "expected_winner_document": "revision_v3",
  "adoption_cycles": 1,
  "documents": [
    {
      "id": "revision_v1",
      "path": "/secure/corpus/travel_v1.md",
      "title": "发布机构：某机构；生效日期：2025年1月1日；版本号：V1.0"
    }
  ],
  "expected_conflict_document_pairs": [
    {"id": "P1", "left": "revision_v2", "right": "revision_v1"}
  ],
  "expected_disputed_fact_count": 1,
  "expected_disputed_fact_anchor_kinds": {"claim_key": 1}
}
```

对于 `.pdf/.doc/.docx`，document 还必须显式写：

```json
{
  "ingest_mode": "file",
  "source_sha256": "<document_inventory.csv 中的 SHA-256>"
}
```

生成的 live scenario 会通过真实 multipart API 上传**该 case 选中的**文件，并经 DocReader/Asynq；不会把二进制
内容作为 Markdown 读取。`title` 会成为 file-mode 的实验知识显示名，且保留源文件扩展名，因此应是从正文 title/header
核实后的简短 metadata 标签，不应含路径或换行。

约束：

```text
adopt_reopen：至少 3 个 documents，winner 必须是 case 内 document，adoption_cycles=1..3
no_proposal：expected_winner_document 为空，adoption_cycles=0
同一 fact_family_id 不能跨 development/holdout
同一个 document path 不能跨 development/holdout
每个 expected conflict pair 必须显式列出
```

建议 first pilot corpus：

```text
开发集：15–25 fact families，用于发现 schema/metadata 问题
holdout：15–25 fact families，只在规则/脚本冻结后运行
正例：每项尽量 3 sources
负例：cross issuer、metadata missing、date/version disagreement、tie、时间区间不可比
```

## 5. 生成 C4.9 matrices 与盲审 sheet

```bash
cd ~/weknora

python3 scripts/experiments/build_winner_corpus_matrix.py \
  --corpus /secure/corpus/my_winner_corpus.json \
  --output-dir experiments/corpus_plans/my-winner-corpus
```

生成：

```text
generated_scenarios/
winner_lifecycle_matrix.development.json
winner_lifecycle_matrix.holdout.json
winner_lifecycle_matrix.all.json
reviewer_1_blind.csv
reviewer_2_blind.csv
gold_adjudication.csv
normalized_corpus.json
README.md
```

先在 development matrix 上做必要的、预先记录的校准；冻结代码/metadata rules 后，才运行 holdout：

```bash
python3 scripts/experiments/run_winner_lifecycle_eval.py \
  --matrix experiments/corpus_plans/my-winner-corpus/winner_lifecycle_matrix.holdout.json \
  --replicates 3
```

不要用 `.all.json` 得出 holdout 结论；它仅用于本地便利检查。

## 6. 双审阅协议

两位 reviewer 分别填写自己的 blind CSV，**不先查看 gold_adjudication.csv**。

每位 reviewer 应记录：

```text
reviewer_label:
  correct_winner | correct_no_proposal | wrong_winner |
  missed_winner | unsafe_action | uncertain | exclude

reviewer_winner_document:
  仅正确/错误 winner 相关 case 填写

reviewer_evidence:
  issuer/date/version 的连续原文证据或可定位段落

reviewer_note:
  不可比、授权层级、版本语义、事实是否同一等理由
```

两位 reviewer 完成后，使用 `gold_adjudication.csv` 记录最终仲裁：

```text
adjudicated_label
adjudicated_winner_document
adjudicated_evidence
adjudicated_note
```

C4.9 runtime 的 `winner_lifecycle_review.csv` 可用：

```bash
make experiment-c49-review REVIEW=<run>/winner_lifecycle_review.csv
```

汇总 runtime outcome 的双审阅 agreement / Cohen's kappa / manual policy accuracy。对于 corpus-level
盲审 sheet，保留原 CSV 和仲裁版本；不要让脚本自动覆盖人工原始标注。

## 7. 建议的论文实验表

至少保留以下四类结果：

1. **C1/C2 detection + cost ablation**：V1/C1/C2-Rules/C2-B4 的 calls、tokens、duration、integrity；
2. **C4 clustering**：raw pairs → DisputedFacts、anchor-kind、review-units-saved；
3. **C4.6 proposal decision**：winner/no-proposal precision、recall、abstention、错误类型；
4. **C4.7/C4.8 lifecycle safety**：stale rejection、no-action negatives、adopt/reopen/re-adopt 成功率、unsafe action count。

对 holdout 中以 fact family 为单位的指标报告 95% bootstrap CI。保留所有 failed case，而不是只报告成功
replicates。当前 C4.9 的 controlled matrix 只能作为 integration evidence；真实 corpus 的 reviewer
agreement 和 error analysis 才能支撑外部有效性讨论。
