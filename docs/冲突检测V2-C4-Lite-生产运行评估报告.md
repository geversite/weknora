# 冲突检测 V2 — C4-Lite DisputedFact 聚类生产运行评估报告

> 状态：**C4-Lite 研究原型冻结（场景级通过）**
>
> 评估完成日期：2026-08-31
>
> 最终 clusterer：`ConflictClustererVersion=c4-v5`（实现提交 `6913c97`）
>
> 运行环境：Linux dev / PostgreSQL + Redis + Asynq；SummaryModel ID：
> `ea37ef9f-31f1-48f1-8332-7cb9ceeffb23`

---

## 1. 结论

C4-Lite 已在真实服务、真实异步任务、真实 PostgreSQL 和生产 SummaryModel 环境中验证：

```text
raw knowledge_conflicts
→ detection-time fact anchor
→ idempotent cluster rebuild
→ disputed_facts
→ member conflict.cluster_id 回写
→ script-exported cluster metrics
```

本期解决的是 C1/C2 已知的“同一事实存在多个 raw chunk-pair conflict，人工需要重复裁决”问题。
它不改变 C1/C2 的候选或裁决，也不实现 cluster 级 Resolve、wiki dispute block 写回或前端。

最终通过了两条互补的事实身份路径：

| 场景 | Raw conflicts | Disputed facts | Anchor | 关键结果 |
|---|---:|---:|---|---|
| exact 三值同事实 | 3 | 1 | `claim_key` | 100 / 150 / 200 元的 3 个来源聚为一项事实争议 |
| schema-drift P3 fallback | 2 | 1 | `document_singleton` | 报销申请 30 个自然日 ↔ 报销单 45 天的两个 raw pair 聚为一项 |

两个 run 均满足：

```text
所有预期文档对命中
dead_letter_count = 0
expected_disputed_fact_count = observed_disputed_fact_count = 1
expected anchor kind 断言通过
```

---

## 2. 最终真实运行证据

### 2.1 Exact claim-key 三值聚类

```text
run: 20260831T113751Z-c4_cluster_triplet-c2-batch-6913c975
KB:  599d510a-9edc-4ea5-a369-4cf4da0aeeaa
```

输入按固定顺序注入：

```text
c4_a：国内出差餐费补贴每日标准 = 100 元
c4_b：国内出差餐费补贴每日标准 = 150 元
c4_c：国内出差餐费补贴每日标准 = 200 元
```

结果：

```text
C4_AB / C4_AC / C4_BC：全部命中
claims：3
raw conflicts：3
disputed facts：1
raw conflicts / disputed fact：3.000
review_units_saved：2
anchor_kind：claim_key
claim_key：国内出差餐费补贴每日标准
source_count：3
candidate_values：[100 元, 150 元, 200 元]
pending_conflict_count：3
clusterer_version：c4-v5
dead letters：0
exit code：0
```

三对数值冲突均由 C2-A 规则直接判定：

```text
rule_direct_conflict = 3
LLM batch/single calls = 0 / 0
```

因此该实验只测 C4 聚类，不将 LLM 随机性混入 exact cluster identity 回归。

### 2.2 P3 schema-drift / summary-child fallback 聚类

```text
run: 20260831T114146Z-c4_fuzzy_fallback-c2-batch-6913c975
KB:  32801d11-5984-43d0-ad00-29e16fe19d10
```

事实：

```text
报销申请：费用发生后 30 个自然日内
报销单：费用发生后 45 天内
```

结果：

```text
C4_P3_FALLBACK：命中
claims：2
raw conflicts：2
disputed facts：1
raw conflicts / disputed fact：2.000
review_units_saved：1
anchor_kind：document_singleton
source_count：2
candidate_values：[费用发生后 30 个自然日内, 费用发生后45天内]
pending_conflict_count：2
clusterer_version：c4-v5
dead letters：0
LLM batch/single calls = 1 / 0
exit code：0
```

这个场景的 raw conflict 引用了无直接 claim 的 summary/child chunk；两份原始文档各只有
一条 usable claim。C4 将其作为 **post-verdict identity fallback** 聚类为
`document_singleton`，而不是假装成 exact claim-key 或低质量 lexical fuzzy match。

---

## 3. Anchor 策略与迭代记录

### 3.1 最终锚点优先级

| Kind | 条件 | 合并范围 |
|---|---|---|
| `claim_key` | final conflict 有 exact `ClaimKeyHit`；或 fallback hint 两侧 ClaimKey 恰好相同后 canonicalize | 同一规范声明槽位跨 raw pair 合并 |
| `fuzzy_slot` | final conflict 的 source/document claims 存在唯一最佳高相似 slot pairing（≥0.35，值不同，第二名至少低 0.10） | 同一确定的 schema-drift slot pair 合并 |
| `document_singleton` | final conflict 两侧文档各恰有一条 usable claim，且值不同 | 只对该已检出冲突的文档对聚合重复 raw pair |
| `chunk_pair` | 无安全 claim anchor | 保守单例，不跨不同 chunk pair 合并 |

`fuzzy_slot` 的纯函数和 ambiguous-document 拒绝路径有 Go 单测覆盖；本轮真实服务重点验证了
exact `claim_key` 与实际 summary/child 缺 claim 的 `document_singleton` 路径。

### 3.2 发现并修复的真实问题

| 迭代 | 现象 | 修复 |
|---|---|---|
| C4 v1 | P3 two raw rows 各自成为 `chunk_pair` | 识别出检索 candidate chunk 与 claim source chunk 可能不同 |
| C4 v2 | P3 policy 有适用范围等第二条上下文 claim，严格“双方各一 claim”条件无法触发 | 引入 document claim 的唯一最佳 slot 选择 |
| C4 v3 | exact triplet 的一个 fallback row 形成 `fuzzy_slot:<k>|<k>`，与 `claim_key:<k>` 分裂 | 发现候选通道不应改变事实身份 |
| C4 v4 | canonicalize 相同 ClaimKey 的 fallback hint | `fuzzy_slot:<k>|<k>` → `claim_key:<k>` |
| C4 v5 | P3 schema 表述可能低于 lexical fuzzy 阈值，但每个文档仅一条 claim | 新增透明的 `document_singleton` post-verdict anchor |

每次修复均保留 `chunk_pair` 的保守退出路径；没有降低 C2 的冲突判定门槛，也没有把 fuzzy
或 document-level evidence 升级为 direct conflict rule。

---

## 4. 运行接口与研究指标

C4-Lite 新增：

```text
GET  /api/v1/knowledge-bases/:id/conflicts/clusters
POST /api/v1/knowledge-bases/:id/conflicts/clusters/rebuild
```

`rebuild` 是幂等的，执行：

```text
读取全部 raw conflicts
→ 为旧 row 回填安全 chunk_pair anchor
→ 按 fact_key 分组
→ upsert disputed_facts（tenant + KB + fact_key 唯一）
→ 回写每个 member conflict.cluster_id
→ 删除 orphan clusters
```

`run_claims_eval.py` 在所有 detector tasks 完成后显式调用一次 rebuild，并导出：

```text
disputed_facts.json
cluster_rebuild.json
cluster_metrics.json
```

关键指标定义：

```text
raw_conflict_count              当前 KB raw knowledge_conflicts 行数
cluster_count                   DisputedFact 数
raw_conflicts_per_disputed_fact raw_conflict_count / cluster_count
review_units_saved              raw_conflict_count - cluster_count
```

`review_units_saved` 是理论上可减少的人工裁决单元，不是已经测得的人工时间或用户体验指标。

---

## 5. C4-Lite 关闭条件

**关闭条件：通过（研究原型聚类层）。**

- [x] `disputed_facts` 持久化模型、唯一 identity、候选值与来源摘要；
- [x] raw conflict 的 fact key、anchor kind、cluster ID 回写；
- [x] automatic best-effort rebuild + 显式幂等 rebuild API；
- [x] Resolve 后 rebuild、文档/KB 删除后的 aggregate cleanup；
- [x] exact 三值、三 raw pair → 一 cluster 的真实运行；
- [x] P3 schema-drift / summary-child two raw pairs → 一 cluster 的真实运行；
- [x] script-exported cluster metrics、count / anchor-kind assertions；
- [x] C4 Go unit tests 已添加；Python runner 场景校验与 SQLite migration up/down 语法验证已完成；
- [ ] 多 seed / 真实多文档集合上的 cluster precision/recall 与人工裁决时间测量；
- [x] C4.5 `keep_both` / `not_conflict` cluster 级传播（见 [C4.5 报告](冲突检测V2-C4.5-安全裁决传播评估报告.md)）；
- [ ] `newer_wins` / `older_wins` 的全局胜方传播、wiki dispute block 写回、前端聚合视图。

---

## 6. 限制与下一步

1. `document_singleton` 是特意狭窄的 post-verdict identity bridge；它不能用于多事实文档；
2. `fuzzy_slot` 是 lexical slot signal，不能替代实体消歧或 NLI；
3. 一条 legacy raw chunk-pair row 若本身混有多条事实，C4 不能无损拆开；
4. C4.5 已让 `keep_both` / `not_conflict` 一次传播到全部 pending member；但
   `newer_wins` / `older_wins` 仍不能根据各 raw pair 的局部 A/B 方向安全传播；
5. wiki dispute block / agent 写回仍未实现，不能宣称已形成端到端知识治理闭环。

下一项最有研究价值的最小扩展是 **C3-Lite 版本与权威元数据**：先以脚本/API 为入口，
输出 `version_update` 的建议胜方，默认只建议、不自动禁用。C3 明确全局胜方后，才能安全
扩展 C4.5 的 `newer_wins` / `older_wins` cluster 级裁决。
