# 冲突检测 V2 — C3-Lite 版本与发布机构建议生产运行评估报告

> 状态：**C3-Lite 研究原型冻结（advisory 路径通过）**
>
> 评估完成日期：2026-09-01
>
> 实现提交：`c03afb8`；Suggestion version：`c3-v1`
>
> 运行环境：Linux dev / PostgreSQL + Redis + Asynq；SummaryModel ID：
> `ea37ef9f-31f1-48f1-8332-7cb9ceeffb23`

---

## 1. 结论

C3-Lite 已在真实服务中完成“显式 document metadata → advisory winner”的无副作用链路：

```text
manual Markdown title/header
→ 发布机构 / 生效日期 / 版本号解析
→ metadata snapshot 写入 raw knowledge_conflicts
→ 同发布机构、日期/版本方向一致时建议较新一侧
→ SuggestedResolution 持久化
→ Status 保持 pending、AutoResolved=false
```

最终运行验证了：

```text
同发布机构 V2.0 / 2148-06-01
vs
同发布机构 V1.0 / 2148-01-01

→ suggested_resolution = resolved_newer_wins
→ confidence = 0.99
→ 建议理由带发布机构、日期、版本证据
→ 不自动更新 raw status
→ 不禁用 chunk
```

同时，来自不同发布机构的两条 raw conflicts 没有产生任何 suggestion，即使其中一条 C2
LLM `conflict_type` 是 `version_update`。这说明 C3 的 source-authority gate 不会把 LLM
类型直接升级为建议胜方。

---

## 2. 真实运行证据

```text
run: 20260901T042446Z-c3_version_suggestion-c2-rules-c03afb87
KB:  3e587901-2428-4d00-9163-63b081a8037a
```

输入：

| 文档 | 发布机构 | 生效日期 | 版本 | 餐补 |
|---|---|---|---|---:|
| `c3_v1` | 天穹财团 | 2148-01-01 | V1.0 | 100 元 |
| `c3_v2` | 天穹财团 | 2148-06-01 | V2.0 | 150 元 |
| `c3_other` | 新弦工业 | 2149-01-01 | V3.0 | 200 元 |

服务级结果：

```text
claims：12
raw conflicts：4
DisputedFacts：2
C2 rules(no/direct/LLM)：0 / 1 / 3
C2 LLM(batch/single)：0 / 3
C3 suggestions：2（均为 resolved_newer_wins）
dead letters：0
experiment exit code：0
```

三组预期 raw conflict document pairs 均命中：

```text
C3_V2_V1：✅
C3_OTHER_V1：✅
C3_OTHER_V2：✅
```

### 2.1 同发布机构建议胜方

`c3_v2 ↔ c3_v1` 产生了两条 raw chunk-pair rows，均为：

```text
conflict_type: version_update
suggested_resolution: resolved_newer_wins
suggestion_confidence: 0.99
suggestion_version: c3-v1
auto_resolved: false
status: pending
```

持久化 `suggestion_reason`：

```text
同发布机构“天穹财团”；
文档 A 生效日期 2148-06-01 与文档 B 生效日期 2148-01-01 可比较；
版本号 2.0 与 1.0 的方向一致；
建议以文档 A 为较新版本（仅建议，不自动裁决）。
```

A/B metadata snapshot 包含逐字段原始 evidence：

```text
**发布机构**：天穹财团
**生效日期**：2148年6月1日 / 2148年1月1日
**版本号**：V2.0 / V1.0
```

### 2.2 跨发布机构拒绝建议

| Raw conflict | C2 type | 发行机构 | C3 suggestion |
|---|---|---|---|
| `c3_other ↔ c3_v1` | `fact_contradiction` | 新弦工业 vs 天穹财团 | 空 |
| `c3_other ↔ c3_v2` | `version_update` | 新弦工业 vs 天穹财团 | 空 |

这验证：

```text
不同 issuer
→ 即使日期和版本号都可比
→ 即使 C2 LLM 判为 version_update
→ C3 仍不建议 resolved_newer_wins / resolved_older_wins
```

---

## 3. 设计决策验证

### 3.1 不从正文事实年份猜 metadata

C3 parser 只读取标题以及 header 的显式标签。单测覆盖：

```text
“项目将在 2153 年进入测试阶段”
```

不成为文档生效日期。运行中 metadata evidence 也均来自明确 header，而不是 chunk 中的
冲突事实数值。

### 3.2 日期精度与版本方向

C3 将 `YYYY` / `YYYY-MM` / `YYYY-MM-DD` 视为时间区间；只有两个区间不重叠时才可比较。
本实验双方均精确到日，基线 confidence 为 0.96；版本号 2.0 与 1.0 同方向后提升为 0.99。

日期和版本方向若相反，C3 不产生 suggestion。发布机构为空或不一致时同样不产生 suggestion。

### 3.3 与 C2 / C4 的边界

- C2 决定是否存在 raw conflict，C3 不修改 C2 verdict；
- C3 metadata 只对 `claim_key`、`fuzzy_slot`、`document_singleton` 等稳定事实 anchor 提建议；
  `chunk_pair` 无锚定 row 不建议胜方；
- C4 将两个 `c3_v2 ↔ c3_v1` raw rows 聚为同一 `DisputedFact`，因此 C3 的两条重复 raw
  suggestions 不能被当作两次独立人工建议；
- C4.5 当前仅安全传播 `keep_both` / `not_conflict`，不会根据 C3 suggestion 自动禁用来源。

---

## 4. C3-Lite 关闭条件

**关闭条件：通过（advisory-only 研究原型）。**

- [x] PostgreSQL `000090` / SQLite `000011` metadata + suggestion schema；
- [x] title/header 显式 issuer / effective-date / version parser；
- [x] source-grounded A/B metadata snapshot；
- [x] 同 issuer、日期/版本同向的 `resolved_newer_wins` 建议；
- [x] 跨 issuer、即使 C2 type 为 `version_update` 仍拒绝建议；
- [x] `status=pending`、`auto_resolved=false` 的无副作用边界；
- [x] C3 scenario version-suggestion 正/负断言、artifact 导出；
- [x] C3 parser / interval / issuer / disagreement 单测已添加；
- [ ] 多 seed、真实法规/产品文档、多 issuer authority 层级与人工建议准确率；
- [ ] C3 suggestion 聚合为 cluster 全局 winner；
- [ ] 自动裁决、chunk 禁用、wiki dispute block / agent 写回。

---

## 5. 下一步

下一步不应直接把每条 raw `resolved_newer_wins` 当作全局禁用指令。应实现一个
**C3/C4 交叉的 cluster winner proposal**：

```text
同一 DisputedFact 的全部 source metadata
→ issuer 一致性检查
→ 日期 / 版本偏序
→ 求出唯一全局最大版本，或报告不可比较
→ 提供 proposal（默认无副作用）
→ 人工/API 明确采纳后，才允许 cluster 级 winner propagation
```

这将解决多来源三值 cluster 中 raw A/B 方向不同的问题，并为 wiki dispute block 的“胜方/旧值”
渲染提供可信的输入。
