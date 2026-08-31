# claims_eval — C1 声明抽取质量评估套件

C1 技术设计 §10.2 的编码前置门槛评估（门槛：合并口径 P ≥ 0.85 且 R ≥ 0.80）。
评估报告见 `docs/冲突检测V2-C1-抽取质量评估报告.md`。

## 目录结构

```
docs/            6 份评估语料（doc1–3 复用 testdata/wiki_test，doc4–6 新增，
                 同一虚构世界观：天穹财团/星尘计划，规避 LLM 参数知识泄漏）
gold/            金标准声明标注（63 条），quote 为原文逐字引用
contradictions.json  预埋跨文档矛盾对 P1–P5 + 一致性对照 N1
runs/            被测抽取器的输出（按附录 A prompt 协议）
evaluate.py      评估脚本，同时是 claim_normalize.go 的算法原型
```

## 运行

```bash
python3 evaluate.py                 # 评估默认 runs/run1.json
python3 evaluate.py --run runs/xxx.json
```

退出码 0=PASS / 1=FAIL。输出四组指标：

1. 抽取 P/R（strict=融合键+值均相等；relaxed=键 bigram Jaccard≥0.5 且值相等）；
2. quote 定位率（精确匹配 → 空白折叠二级匹配，对应设计 §5.3）；
3. value_kind 一致率；
4. 预埋矛盾对的声明键通道召回（strict / strict+relaxed）+ 一致对照不误报检查。

## 新抽取器接入

按 `runs/run1.json` 的格式产出预测（对 `docs/` 逐篇执行设计文档附录 A 的 prompt），
放入 `runs/` 后用 `--run` 指定。生产模型（KB SummaryModelID 对应模型）接入
ClaimExtractService 后，应以本套件复跑并归档结果。

## 注意

- gold 的 quote 必须是文档逐字子串（改动语料后需同步校验）；
- 语料含中文引号/括号等标点陷阱，属故意设计（测试规范化健壮性），修改时保留;
- 预埋矛盾对的措辞分歧程度是刻意分层的：P4/P5 同措辞（必中 strict）、
  P2 同键异值、P1 主谓边界漂移、P3 主体同义词（预期 strict 漏、走兜底通道）。
- `c4_meal_allowance_a/b/c.md` 不属于六文档 claim P/R gold；它们仅由
  `scripts/experiments/scenarios/c4_cluster_triplet.json` 使用，验证三条 raw conflict
  是否聚合为一条 `DisputedFact`。
