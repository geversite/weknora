# 冲突检测 V2：WikiFactDiff 公开时序事实 transfer 评估报告

> 状态：**pair development 与 pair holdout 已冻结；two-snapshot proposal development 已冻结；proposal holdout 尚未运行。**
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

## 3. 冻结数字

Fact-family strict-all-replicates，`replicates=1`。

| Run | N | TP | TN | FP | FN | P | R | A | dead letters |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| pair-development-r1 | 20 | 10 | 10 | 0 | 0 | 1.0 | 1.0 | 1.0 | 20×0 |
| pair-holdout-r1 | 60 | — | — | — | — | 1.0 | 1.0 | 1.0 | total=2，all_zero=false |

Holdout 60/60 可评，headline P/R/A=1.0。`dead_letter_count` 合计为 2，不得写成 all-zero。分类计数未在控制台展开，但 1.0/1.0/1.0 且 60 可评意味着 30 replacement 全 TP、30 keep control 全 TN。

Proposal development：10/10 exact winner+source_count=2，dead letters 0。Proposal 列在 pair manifest 上为 None，符合预期。

## 4. 主张边界

可以写：

```text
public temporal fact pair transfer on WikiFactDiff-derived verbalizations
two-snapshot advisory-proposal development success rate
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

模板化 Wikidata 原子事实的 1.0 不能用来解释 VitaminC holdout 的 0.75。
