# 冲突检测 V2：WikiFactDiff 公开时序事实 transfer 评估报告

> 状态：**pair development/holdout 与 two-snapshot proposal development/holdout 均已冻结。**
>
> 公开 temporal-fact transfer，不是 native document-header、multi-authority、C4.7/C4.8 或企业文档准确率。
> 不与 C4.9/C4.10 或 VitaminC 指标池化。不含语料正文。

## 1. 冻结对象

| 项 | 值 |
| --- | --- |
| 上游 | Hugging Face `Orange/WikiFactDiff`，config `20210104-20230227_legacy`，revision `bb17ffbff7b2d28e4cd12e251af3db50d7fa18ea` |
| Pair gold | strict `obsolete/forget → new/learn` replacement = conflict；`static/keep` 同文复制 = no-conflict control |
| Proposal gold | replacement only；expected winner = `snapshot_new`；source_count = 2 |
| Title metadata | 由 release snapshot date **派生**，非原生文档 header |
| variant | `c2-rules` |
| 计划目录（仓库外） | `$HOME/weknora-public-data/wikifactdiff-20210104-20230227-legacy-v1` |

## 2. 保留 / 排除的 run

| Run | 作用 |
| --- | --- |
| `runs/pair-development-smoke` | 缺模板 KB，10/10 UNEVALUABLE；**排除** |
| `runs/pair-development-smoke-retry1` | 有效 10-case 通路 |
| `runs/pair-development-r1` | **冻结 pair development**：20/20 |
| `runs/proposal-development-r1` | **冻结 proposal development**：10/10 |
| `runs/pair-holdout-r1` | **冻结 pair holdout**：60/60 |
| `runs/proposal-holdout-r1` | **冻结 proposal holdout**：30/30 可评 |

## 3. 冻结数字

Fact-family strict-all-replicates，`replicates=1`。

### 3.1 Pair conflict transfer

| Run | N | TP | TN | FP | FN | P | R | A | dead letters |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| pair-development-r1 | 20 | 10 | 10 | 0 | 0 | 1.0 | 1.0 | 1.0 | 20×0 |
| pair-holdout-r1 | 60 | 30 | 30 | 0 | 0 | 1.0 | 1.0 | 1.0 | total=2（1 个 TN keep-control 上的 `wiki:ingest`×2），all_zero=false |

pair-holdout 的 2 条死信均为 `wiki:ingest` 重试超限，不是 `claim:extract` / `conflict:detect`；该 case 仍为 TN。不得写成 all-zero。

### 3.2 Two-snapshot advisory proposal transfer

只报告 exact winner + exact source_count=2 的 success rate，不是 precision / abstention。

| Run | N | pair P/R/A | exact-proposal success | dead letters |
| --- | ---: | --- | ---: | --- |
| proposal-development-r1 | 10 | 1.0 / 1.0 / 1.0 | 1.000 | 10×0 |
| proposal-holdout-r1 | 30 | 1.0 / 1.0 / 1.0 | **0.833**（25/30） | 30×0 |

Holdout 上 30 条 replacement 的 pair conflict 全对，但 5 条 exact winner+source_count 未命中。这正说明 pair 检出 ≠ advisory proposal 成功，不得把 1.0 pair 数字写成 proposal 泛化。

## 4. 主张边界

可以写：

```text
public temporal fact pair transfer on WikiFactDiff-derived verbalizations
two-snapshot advisory-proposal development success rate 10/10
two-snapshot advisory-proposal holdout success rate 25/30
```

不可以写：

```text
native-header extraction accuracy
proposal precision / abstention precision
multi-authority safety
C4.7 adoption / C4.8 reopen
enterprise-document accuracy
与 VitaminC 或 C4.10 synthetic 池化后的总体准确率
provider seed-controlled causality
```

模板化 Wikidata 原子事实的 pair 1.0 不能用来解释 VitaminC holdout 的 0.75，也不能掩盖 proposal holdout 的 5 个 exact-match 失败。
