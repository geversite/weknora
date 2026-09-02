# C4-Lite 技术设计：DisputedFact 事实级冲突聚类

> 所属系列：[冲突检测 V2 里程碑规划](冲突检测V2-里程碑规划.md)
>
> 状态：**C4-Lite 研究原型已冻结**。真实运行证据见
> [C4-Lite 生产运行评估报告](冲突检测V2-C4-Lite-生产运行评估报告.md)。
> C3/C4.6 的多来源全局 winner proposal 亦已完成真实服务验证，见
> [C4.6 全局胜方 Proposal 评估报告](冲突检测V2-C4.6-全局胜方Proposal评估报告.md)。C4.7 的
> 显式采纳路径也已完成真实服务验证，见
> [C4.7 显式全局胜方采纳评估报告](冲突检测V2-C4.7-显式全局胜方采纳评估报告.md)。
>
> 依赖：C1 claims 与 C2-Lite 最终 raw `knowledge_conflicts`。
>
> C4-Lite core frozen version：`c4-v5`；C3/C4.6 proposal extension：`c4-v6`。
> 基础聚类迁移：PostgreSQL `000088` / SQLite `000009`；proposal 扩展迁移：PostgreSQL `000091` / SQLite `000012`。

---

## 1. 问题与最小目标

当前 `knowledge_conflicts` 一行对应一个 raw chunk pair，而不是一个可人工裁决的事实。
例如三个来源分别声称同一补贴为 100 / 150 / 200 元时，增量检测会形成：

```text
B ↔ A
C ↔ A
C ↔ B
```

人工若仍按 raw row 操作，会重复审查同一事实。C4-Lite 先把这类行确定性聚合为：

```text
1 DisputedFact
  ├─ sources: A / B / C
  ├─ candidate values: 100 / 150 / 200
  └─ member raw conflicts: 3
```

本期只完成聚类、导出和可重复重建；不改变既有 raw pair 的 Resolve 副作用。

---

## 2. 非目标

本实现刻意不做：

- 基于 C4.6 winner proposal 的 cluster 级 `newer_wins` / `older_wins` 采纳与传播；
  （C4.5 已冻结 `keep_both` / `not_conflict` 的安全传播。）
- wiki dispute block 自动写回；
- ConflictQueue 前端两级视图；
- agent 正文叙事整合；
- 用 fuzzy matching 直接作出冲突判断；
- C3 的版本日期、来源权威性和自动裁决。

这些都应建立在已验证的 cluster identity 上，而不是阻塞 C4-Lite 的研究实验。

---

## 3. 数据模型

### 3.1 raw conflict 扩展

`knowledge_conflicts` 由 migration `000088` 增加：

```text
cluster_id
fact_key
fact_anchor_kind
claim_key
fact_subject
fact_predicate
fact_value_a
fact_value_b
```

`fact_key` 在 raw row 创建时生成；`cluster_id` 在 rebuild 后回写。这样 cluster 不必从
整个 LLM reason 或易变 chunk 文本重新猜测事实关系。

### 3.2 `disputed_facts`

新表的一行代表一个可人工审查的事实聚合，主要字段：

```text
id
(tenant_id, knowledge_base_id, fact_key) UNIQUE
clusterer_version
anchor_kind / claim_key
subject / predicate
conflict_type / status
conflict_count / pending_conflict_count
source_count / source_refs
candidate_value_count / candidate_values
suggested_winner_knowledge_id / winner_proposal_reason
winner_proposal_confidence / winner_proposal_version / winner_proposal_source_count
created_at / updated_at
```

`candidate_values` 与 `source_refs` 是从 member conflicts 确定性去重、排序后的摘要。它们
可支撑无 UI 的审计导出与以后 cluster 级 Resolve，而不是替代原始 A/B content snapshots。
C4.6 新增的 winner 字段只承载基于全来源 metadata 的 advisory proposal；空值代表没有足够
证据得到唯一全局最大来源，不代表任意一侧自动失败。

---

## 4. 稳定事实锚点

C4-Lite 以保守优先级生成 `fact_key`：

| Anchor kind | key | 合并语义 |
|---|---|---|
| `claim_key` | `claim_key:<exact ClaimKeyHit>` | 同一 exact 声明槽位的所有 raw rows 合并 |
| `fuzzy_slot` | `fuzzy_slot:<sorted slot key pair>` | source claims 的 schema drift 提示；若 raw chunk 是无 claim 的 summary/child，则 post-verdict 可从文档 claims 选择唯一最佳、高相似 slot pair（≥0.35，且无接近竞争项），两侧值必须不同 |
| `document_singleton` | `document_singleton:<sorted document claim keys>` | 两份已检出冲突的文档各恰有一条 usable claim 时的 post-verdict anchor；允许极端 schema drift，但不参与检测判定 |
| `chunk_pair` | `chunk_pair:<sorted chunk IDs>` | 无任何可用声明时的保守单例；不跨 chunk 合并 |

所有 key 限制在 512 runes 内；超长 key 改为 SHA-256 形式。方向无关：A↔B 与 B↔A 的
anchor 相同。若 fallback pair 的最终 selected hint 恰好两侧 `ClaimKey` 完全相同，C4-v4
会 canonicalize 为 `claim_key:<k>`，而不会保留冗余的 `fuzzy_slot:<k>|<k>` cluster；候选
来源是 fallback 不应改变事实身份。

`fuzzy_slot` 只在 **已经被 C1/C2 判为 conflict 的 raw row** 上帮助聚类。它不会反向影响
candidate generation、规则层或 LLM verdict，因此不会把 C2-B4 的 prompt-only fuzzy hint
升级成危险的 direct rule。若 raw pair 的 source chunk 恰好是没有 claim 的 summary/child，C4
会优先在两份文档各恰有一条 usable claim 时使用 `document_singleton`；否则只在文档级候选中
存在唯一最佳 slot pairing（score ≥0.35、值不同、第二名低至少 0.10）时使用 `fuzzy_slot`。
多个接近的多事实解释仍保持 `chunk_pair` 单例，宁可少合并也不错误合并。

---

## 5. Rebuild 生命周期

`ConflictClusterService.Rebuild(tenant, kb)`：

```text
读取该 KB 的全部 raw conflicts（所有 status）
→ 旧 row 无 fact_key 时，以 chunk_pair 保守回填
→ 按 fact_key 分组
→ 计算 source/value/type/status 聚合
→ C4.6：对全部 member 的 C3 metadata 尝试求唯一全局 winner proposal
→ upsert disputed_facts（tenant + KB + fact_key 唯一）
→ 回写 member conflict.cluster_id
→ 删除当前无 member raw conflict 的 orphan cluster
```

实现以进程内 mutex 串行化 rebuild，避免自动 detect 和手动 rebuild 在同一开发服务中互相
删除对方刚写入的 aggregate。数据库唯一约束仍保证多进程情况下 cluster ID 的唯一边界。

触发点：

1. 新 `knowledge_conflicts` 批量落库后，best-effort 自动 rebuild；
2. 既有 raw conflict Resolve 成功后，best-effort rebuild 更新 cluster status/count；
3. 文档删除并删除关联 raw conflicts 后，best-effort rebuild 清理/刷新 aggregate；
4. 实验 runner 在所有 Asynq detector task 完成后，显式调用一次 rebuild，获得确定性 artifact。

KB 删除时直接删除该 KB 的 `disputed_facts`。

自动 rebuild 出错不会撤销已成功写入的 raw conflict 或既有 Resolve 副作用；管理员/实验脚本
可调用显式 endpoint 收敛。

---

## 6. API 与实验出口

无 UI，但保留两个受现有 KB 权限保护的 API：

```text
GET  /api/v1/knowledge-bases/:id/conflicts/clusters
POST /api/v1/knowledge-bases/:id/conflicts/clusters/rebuild
```

前者返回分页 `DisputedFact` 列表；后者返回：

```json
{
  "raw_conflict_count": 3,
  "disputed_fact_count": 1,
  "assigned_conflict_count": 3,
  "unanchored_conflict_count": 0,
  "winner_proposal_count": 1,
  "anchor_kinds": {"claim_key": 1}
}
```

`run_claims_eval.py` 会导出：

```text
disputed_facts.json
cluster_rebuild.json
cluster_metrics.json
```

其中：

```text
review_units_saved = raw_conflict_count - cluster_count
raw_conflicts_per_disputed_fact = raw_conflict_count / cluster_count
```

它们是“可减少的人工裁决单元”代理指标，不是已经测得的人工时间节省。

---

## 7. C4 隔离实验

`make experiment-c4` 使用：

```text
scripts/experiments/scenarios/c4_cluster_triplet.json
```

固定注入三份短文档，分别断言同一国内出差餐补每日标准为 100 / 150 / 200 元。场景要求：

```text
C4_AB / C4_AC / C4_BC 三个文档对均出现
expected_disputed_fact_count = 1
expected_disputed_fact_anchor_kinds = {"claim_key": 1}
```

另有 `make experiment-c4-fuzzy`：复用 P3 的报销申请/报销单 schema drift。该文档对各有一条
usable claim，但 raw pair 可来自无 claim 的 summary/child，因此要求聚为一个
`document_singleton` cluster。`fuzzy_slot` 仍用于多 claim 文档中唯一最佳的高相似 slot pairing。
两者均没有全局 claim P/R evaluator；它们是针对 raw-pair → fact-cluster 语义的独立回归。

---

## 8. C4.5：安全 cluster 级裁决传播（已冻结）

真实运行证据见 [C4.5 安全裁决传播评估报告](冲突检测V2-C4.5-安全裁决传播评估报告.md)。

C4-Lite 的 identity 已冻结后，C4.5 增加无 UI 的：

```text
POST /api/v1/knowledge-bases/:id/conflicts/clusters/resolve
```

PostgreSQL migration `000089_conflict_status_width` 将旧 M3 的
`knowledge_conflicts.status VARCHAR(20)` 扩展为 `VARCHAR(32)`，因为
`resolved_not_conflict` 有 21 个字符。SQLite 的 `TEXT` 无宽度限制，对应版本标记为
SQLite migration `000010`。

请求：

```json
{
  "disputed_fact_id": "<cluster id>",
  "resolution": "resolved_keep_both | resolved_not_conflict",
  "note": "optional audit note"
}
```

它在一个 repository transaction 中更新该 cluster 的全部 **pending** raw members 的：

```text
status
resolved_by
resolved_at
resolution_note
```

随后 rebuild，令 `DisputedFact.status=resolved` 且 `pending_conflict_count=0`。脚本
`make experiment-c4-resolve RUN=<c4-run>` 通过 API 执行并读取 PostgreSQL 验证所有
member 都已传播。

C4.5 明确拒绝：

```text
resolved_newer_wins
resolved_older_wins
```

因为一个 cluster 内不同 raw member 的 A/B 方向可不同。即使 C4.6 已产生全局 winner
proposal，C4.5 也不会把它自动转换成 `newer_wins` / `older_wins`：`keep_both` 与
`not_conflict` 仍是该 generic resolver 唯一允许传播的安全子集。C4.7 另设显式 adoption
endpoint，并要求 current proposal snapshot；它不扩展 C4.5 的 `resolution` 参数。

---

## 9. C4.6：多来源全局 winner proposal（已冻结，advisory-only）

真实运行证据见
[C4.6 全局胜方 Proposal 评估报告](冲突检测V2-C4.6-全局胜方Proposal评估报告.md)。

C4.6 在同一事实 cluster 内读取全部 member 的 C3 `doc_meta_a/b` snapshot，按 knowledge ID
收集 source metadata 后才作判断。它不采用 raw member 的局部 A/B 方向，也不把
`ConflictType=version_update` 当作 winner 证据。

```text
全部 source metadata 完整
→ issuer 规范化后全部相等
→ 所有日期/version 对可比较且方向一致
→ 找到唯一一个严格晚于每个其他 source 的候选
→ 仅在 disputed_facts 写 advisory winner proposal
```

任意 metadata 缺失、issuer 不同、同 source snapshot 不一致、日期区间重叠、版本并列或
日期/version 方向不一致，均保守返回空 proposal。

```bash
make experiment-c46
make experiment-c46-negative
```

真实正例将同 issuer V1/V2/V3 的 4 条 raw chunk-pair conflicts 聚为 1 条 `claim_key`
DisputedFact，并通过 `c46_v3` 唯一 winner（confidence ≥0.95）断言；跨 issuer 负例以
零 proposal 断言通过。C4.6 本身只更新 aggregate 字段，不触发 Resolve、chunk 禁用、wiki
写回或 agent 改写。

---

## 10. C4.7：显式 global winner adoption（已冻结，explicit-only）

设计细节见 [C4.7 显式全局胜方采纳技术设计](冲突检测V2-C4.7-显式全局胜方采纳技术设计.md)，真实
运行见 [C4.7 显式全局胜方采纳评估报告](冲突检测V2-C4.7-显式全局胜方采纳评估报告.md)。

C4.7 不接受通用的 `newer_wins` / `older_wins` cluster resolution。它要求调用方从当前 cluster
读取并回显 winner knowledge ID、proposal version、proposal source count 与 `updated_at`。服务在
一个 database transaction 内锁定 aggregate、全部 current members 和 loser chunks，发现 proposal
或 member/source snapshot 变化时以 HTTP `409` fail closed。

成功时，所有 pending members 使用方向无关的 `resolved_global_winner`，而不是依赖某一 raw row 的
A/B 方向；只禁用 cluster member 中属于非 winner source 的 chunks，保留 winner chunks。当前只允许
`claim_key` anchor 采纳，`fuzzy_slot` / `document_singleton` / `chunk_pair` 仍保持 advisory-only。

```bash
make experiment-c46
make experiment-c47 RUN=experiments/runs/<fresh-c46-positive-run>
make experiment-c46-negative
make experiment-c47-negative RUN=experiments/runs/<fresh-c46-negative-run>
```

正例已在真实服务中验证 stale snapshot HTTP `409` / no mutation，再以精确 snapshot 一次更新 4 条
raw members 为 `resolved_global_winner`、保留 winner chunk 并禁用 3 个 loser member chunks；负例也已验证
跨 issuer 空 proposal 只能 HTTP `409` / no mutation。该路径不添加 UI、自动采纳、wiki dispute block 或
agent 写回。

---

## 11. 已知限制

1. 对已有历史 raw rows，若 C4 前没有保存 claim provenance，只能安全回填 `chunk_pair`，不能
   追溯地猜测跨 chunk 同一事实；
2. `ConflictType=version_update` 仍由 C2 LLM 给出，C4 只聚合，不纠正类型；C3/C4.6 只依赖
   显式 metadata snapshot，不把该类型直接升级为全局权威性结论；
3. 一个 raw chunk pair 若自身含多条矛盾事实，当前旧格式仍只携带一个 final verdict，C4 无法
   从中无损拆分；未来应在 candidate / verdict 层持久化细粒度 claim evidence；
4. C4.7 已冻结仅限 exact `claim_key` 的显式 proposal adoption；`fuzzy_slot` / `document_singleton` /
   `chunk_pair` 的 adoption、winner 撤销/重开、wiki 写回和 agent 叙事整合仍未实现。所有后续 winner
   行为仍必须使用全局 winner，而不是 raw A/B 方向。
