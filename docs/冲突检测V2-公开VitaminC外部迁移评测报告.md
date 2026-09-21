# 冲突检测 V2：VitaminC 公开 claim--evidence transfer 评估报告

> 状态：**已冻结。** 这是 adapter-defined 公开 transfer，不是官方 VitaminC train/dev/test，也不是版本治理或真实企业文档准确率。
>
> 不与 C4.9/C4.10 controlled synthetic 指标池化。不含公开语料正文。

## 1. 冻结对象

| 项 | 值 |
| --- | --- |
| 任务 | public claim--evidence conflict-transfer detection |
| 上游 | 官方 dedicated `vitaminc_real.zip`（real test-set 包，仅 `vitaminc_real/test.jsonl`） |
| Gold | `REFUTES` → expected conflict；`SUPPORTS` → expected no-conflict |
| Split | adapter-defined disjoint partition of the sole official real test member |
| native train/dev/test | false |
| 丢弃同一文本相反标签 family | 6 |
| variant | `c2-rules` |
| 计划目录（仓库外） | `$HOME/weknora-public-data/vitaminc-real-v1-retry3` |

## 2. 保留 / 排除的 run

| Run | 作用 |
| --- | --- |
| `runs/development-smoke` | 10-case 通路；不作论文指标 |
| `runs/development-r1` | 18/20，2 条 0-claim 被误标 UNEVALUABLE；**保留并排除** |
| `runs/development-r1-retry1` | **冻结 development**：20/20 |
| `runs/holdout-r1` | **冻结 holdout**：60/60 |

0-claim 在 detect 已入队后视为可评测抽取结果，不再空等 `claims>=1`。

## 3. 冻结数字

Fact-family strict-all-replicates，`replicates=1`。

| Split | N | TP | TN | FP | FN | P | R | A | dead letters |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| development-r1-retry1 | 20 | 8 | 5 | 5 | 2 | 0.615 | 0.800 | 0.650 | 20×0 |
| holdout-r1 | 60 | 24 | 22 | 8 | 6 | 0.750 | 0.800 | 0.767 | 60×0 |

Holdout 正例 30 = 24 TP + 6 FN；负例 30 = 22 TN + 8 FP。proposal 列不适用。

Development 与 holdout 的 recall 均为 0.800；holdout precision 更高，不是根据 holdout 调参的结果。

## 4. 主张边界

可以写：

```text
public benchmark transfer on VitaminC-derived claim--evidence text under this adapter
adapter-defined partition of the official real test member
```

不可以写：

```text
官方 VitaminC train/dev/test 准确率
完整 fact-verification / NLI 任务准确率
document-version governance / C3 / C4.6 / C4.7 / C4.8
enterprise-document accuracy
native PDF/DOCX accuracy
human-review accuracy
与 C4.9/C4.10 synthetic 池化后的总体准确率
```

公开英文 claim--evidence 明显难于 WikiFactDiff 模板化原子事实；不得用 WikiFactDiff development 的 1.0 解释 VitaminC holdout。
