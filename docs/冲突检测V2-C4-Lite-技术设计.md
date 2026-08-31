# C4-Lite 技术设计：DisputedFact 事实级冲突聚类

> 所属系列：[冲突检测 V2 里程碑规划](冲突检测V2-里程碑规划.md)
>
> 状态：**实现完成，待真实服务 / migration / 实验场景验证**。
>
> 依赖：C1 claims 与 C2-Lite 最终 raw `knowledge_conflicts`。
>
> Clusterer version：`c4-v4`；迁移：PostgreSQL `000088` / SQLite `000009`。

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

- cluster 级 Resolve 及向全部 member conflict 传播裁决；
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
created_at / updated_at
```

`candidate_values` 与 `source_refs` 是从 member conflicts 确定性去重、排序后的摘要。它们
可支撑无 UI 的审计导出与以后 cluster 级 Resolve，而不是替代原始 A/B content snapshots。

---

## 4. 稳定事实锚点

C4-Lite 以保守优先级生成 `fact_key`：

| Anchor kind | key | 合并语义 |
|---|---|---|
| `claim_key` | `claim_key:<exact ClaimKeyHit>` | 同一 exact 声明槽位的所有 raw rows 合并 |
| `fuzzy_slot` | `fuzzy_slot:<sorted slot key pair>` | source claims 的 schema drift 提示；若 raw chunk 是无 claim 的 summary/child，则 post-verdict 可从文档 claims 选择唯一最佳、高相似 slot pair（≥0.35，且无接近竞争项），两侧值必须不同 |
| `chunk_pair` | `chunk_pair:<sorted chunk IDs>` | 无任何可用声明时的保守单例；不跨 chunk 合并 |

所有 key 限制在 512 runes 内；超长 key 改为 SHA-256 形式。方向无关：A↔B 与 B↔A 的
anchor 相同。若 fallback pair 的最终 selected hint 恰好两侧 `ClaimKey` 完全相同，C4-v4
会 canonicalize 为 `claim_key:<k>`，而不会保留冗余的 `fuzzy_slot:<k>|<k>` cluster；候选
来源是 fallback 不应改变事实身份。

`fuzzy_slot` 只在 **已经被 C1/C2 判为 conflict 的 raw row** 上帮助聚类。它不会反向影响
candidate generation、规则层或 LLM verdict，因此不会把 C2-B4 的 prompt-only fuzzy hint
升级成危险的 direct rule。若 raw pair 的 source chunk 恰好是没有 claim 的 summary/child，C4
只在文档级候选中存在唯一最佳 slot pairing（score ≥0.35、值不同、第二名低至少 0.10）时
才使用 document-level fallback；多个接近的多事实解释仍保持 `chunk_pair` 单例，宁可少合并
也不错误合并。

---

## 5. Rebuild 生命周期

`ConflictClusterService.Rebuild(tenant, kb)`：

```text
读取该 KB 的全部 raw conflicts（所有 status）
→ 旧 row 无 fact_key 时，以 chunk_pair 保守回填
→ 按 fact_key 分组
→ 计算 source/value/type/status 聚合
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

另有 `make experiment-c4-fuzzy`：复用 P3 的报销申请/报销单 schema drift，要求 semantic
fallback 产生的 raw rows 聚为一个 `fuzzy_slot` cluster。两者均没有全局 claim P/R evaluator；
它们是针对 raw-pair → fact-cluster 语义的独立回归。

---

## 8. 已知限制

1. 对已有历史 raw rows，若 C4 前没有保存 claim provenance，只能安全回填 `chunk_pair`，不能
   追溯地猜测跨 chunk 同一事实；
2. `ConflictType=version_update` 仍由 C2 LLM 给出，C4 只聚合，不纠正类型；C3 处理版本语义；
3. 一个 raw chunk pair 若自身含多条矛盾事实，当前旧格式仍只携带一个 final verdict，C4 无法
   从中无损拆分；未来应在 candidate / verdict 层持久化细粒度 claim evidence；
4. cluster 级 resolution、传播、wiki 写回必须在 cluster identity 和人工审计稳定后再实现。
