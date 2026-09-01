# C3-Lite 技术设计：文档版本与发布机构的建议胜方判定

> 所属系列：[冲突检测 V2 里程碑规划](冲突检测V2-里程碑规划.md)
>
> 状态：**C3-Lite 研究原型已冻结（advisory-only）**。真实运行见
> [C3-Lite 生产运行评估报告](冲突检测V2-C3-Lite-生产运行评估报告.md)。
>
> 依赖：C1/C2 的 final raw conflict、C4/C4.5 的事实聚类与安全传播。
>
> Suggestion version：`c3-v1`；迁移：PostgreSQL `000090` / SQLite `000011`。

---

## 1. 目标与边界

C2 已能输出 `version_update`，但其类型仍主要由 LLM 从 chunk 内容判断。例如同一事实的
两个来源可能被误标为 `version_update`，却没有明确的文档版本或发布机构证据。C3-Lite
只回答一个更窄的问题：

```text
在同一发布机构、同一已检出事实冲突上，
若两份文档有明确且可比较的生效日期或版本号，
哪一侧是“建议较新版本”？
```

输出是 advisory，不是自动裁决：

```text
SuggestedResolution = resolved_newer_wins | resolved_older_wins
AutoResolved = false
raw conflict Status 不变
不禁用 chunk
不调用 Resolve
```

非目标：

- 不从正文任意事实年份推断文档发布日期；
- 不为发布机构缺失、不同或 metadata 方向冲突的文档建议胜方；
- 不自动执行 `newer_wins` / `older_wins`；
- 不做前端建议徽标；
- 不做跨机构权威性排序、法规层级、签发人可信度或 NLI；
- 不改 C2 的 `ConflictType`，尤其不把 LLM 的 `version_update` 直接等同为 C3 建议。

---

## 2. 数据模型

Migration `000090_conflict_version_suggestions` 为 `knowledge_conflicts` 新增：

```text
doc_meta_a JSONB
doc_meta_b JSONB
suggested_resolution VARCHAR(32)
suggestion_reason TEXT
suggestion_confidence DOUBLE PRECISION
suggestion_version VARCHAR(32)
auto_resolved BOOLEAN
```

`doc_meta_a/b` 的结构：

```json
{
  "parser_version": "c3-v1",
  "knowledge_id": "...",
  "title": "国内出差餐费补贴标准（V2.0）",
  "issuer": "天穹财团",
  "issuer_evidence": "**发布机构**：天穹财团",
  "effective_date": "2148-06-01",
  "effective_date_precision": "day",
  "effective_date_evidence": "**生效日期**：2148年6月1日",
  "version": "2.0",
  "version_evidence": "**版本号**：V2.0"
}
```

metadata 是 detection-time snapshot，后续源文档变更不会静默改变历史 conflict 的建议依据。

---

## 3. Metadata 解析

实现：`internal/application/service/conflict_version.go`。

优先使用 manual document 的原始 Markdown；其他来源回退到冲突 chunk snapshot。只扫描：

```text
标题
前 48 行 / 前 2400 runes 的 header 区域
```

仅接受明确标签：

| 元数据 | 中文标签 | 英文标签 |
|---|---|---|
| 发布机构 | 发布机构、发布单位、编制单位、发文单位、发布者 | issuer、publisher、issuing organization |
| 生效日期 | 生效日期、生效时间、发布日期、发布日、更新日期、修订日期、版本日期 | effective date、publication date、release date、updated date |
| 版本 | 版本、版本号、修订版本 | edition、version |

标题中的日期仅在“xx 年 xx 月版”或 edition/version 语境下作为版本日期；普通正文中的
`2153 年`、项目年份、设备型号等不会成为文档 metadata。

日期归一化为：

```text
YYYY
YYYY-MM
YYYY-MM-DD
```

低精度日期被视为区间：年为全年、月为整月。两个区间重叠时不建议胜方；只有一个区间严格
晚于另一个区间才可比较。

---

## 4. 建议规则

前提：final raw conflict 已有稳定 C4 fact anchor（`claim_key`、`fuzzy_slot` 或
`document_singleton`）；保守 `chunk_pair` 不产生建议。

```text
发布机构缺失 / 不同                    → 无建议
日期与版本均不可比较                    → 无建议
日期与版本方向相反                      → 无建议
同发布机构 + A 日期确定晚于 B           → resolved_newer_wins
同发布机构 + B 日期确定晚于 A           → resolved_older_wins
同发布机构 + 无可比日期但版本号更高     → 同上
```

置信度：

| 证据 | confidence |
|---|---:|
| 两侧精确到日的生效日期 | 0.96 |
| 两侧精确到月 | 0.91 |
| 至少一侧只有年，但区间不重叠 | 0.86 |
| 仅明确 numeric version | 0.90 |
| 日期与版本方向一致 | 在日期基础上 +0.03，上限 0.99 |

`suggestion_reason` 明确记录比较的发行机构、日期/版本与“仅建议，不自动裁决”边界。

---

## 5. 运行与实验

C3 不新增前端。raw conflict API 已直接返回 C3 fields；研究 runner 导出：

```text
conflicts.json        含 doc_meta_a/b 与 suggestion 字段
version_suggestions.json
metrics.json          含 expected/missing/forbidden suggestion assertions
report.md             含 C3 suggestion 摘要
```

C3 场景：

```bash
make experiment-c3
```

固定测试：

```text
天穹财团 V1.0 / 2148-01-01 / 100 元
天穹财团 V2.0 / 2148-06-01 / 150 元
新弦工业 V3.0 / 2149-01-01 / 200 元
```

验收：

```text
C3_V2_V1 conflict 出现
C3_SUGGEST_V2_WINS = resolved_newer_wins，confidence >= 0.95
C3_OTHER_V1 / C3_OTHER_V2 仍可能是 conflict，但不得产生 suggestion
AutoResolved = false
```

---

## 6. 与 C4.5 的关系

C4.5 当前只允许：

```text
resolved_keep_both
resolved_not_conflict
```

C3-Lite 不会自动开放 cluster-level `newer_wins`。它先提供可审计、无副作用的建议证据。
只有在后续元数据 precision、issuer authority、跨多 member consistency 和人工审计都通过后，
才可设计全局 winner selection 并安全接入 C4.5。

---

## 7. 测试覆盖

`internal/application/service/conflict_version_test.go` 覆盖：

- 显式 header 的发布机构、生效日期、版本解析；
- edition title 日期解析；
- 正文事实年份不会升级为 metadata；
- 同机构且日期/版本同向的建议；
- 跨发布机构拒绝建议；
- 日期与版本方向冲突时拒绝建议；
- 低精度日期区间重叠时保守拒绝；
- unanchored `chunk_pair` 不生成 C3 建议。
