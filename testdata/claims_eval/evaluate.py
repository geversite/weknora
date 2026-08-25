#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""C1 声明抽取质量评估（§10.2 编码前置门槛）。

用法: python3 evaluate.py [--run runs/run1.json]

实现内容（同时作为 claim_normalize.go 的算法原型）:
  1. 键规范化 norm() / claim_key（技术设计 §4）
  2. value_norm 数值/单位/日期归一
  3. 预测 vs 金标准匹配（strict: 键相等; relaxed: 键 bigram Jaccard >= 0.5）
  4. 指标: 抽取 P/R、quote 定位率、value_kind 一致率、预埋矛盾对通道召回
门槛: R >= 0.80 且 P >= 0.85（strict+relaxed 合并口径）
"""
import json, re, sys, unicodedata, argparse
from pathlib import Path

BASE = Path(__file__).parent

# ---------------- 规范化（§4 原型） ----------------

WRAP_PUNCT = "《》「」『』【】\"\"''\"'()（）"

# v1.1 校准（round1 诊断产物，对应 claim_normalize.go 设计）：
#   - 括号注释剔除（英文原名/补充说明不参与匹配）
#   - text 值归一：去"的"、去连接词(并在/并且/并/且/以及)、去肯定情态前缀
#     (必须/须/应当/应/需要/需)、去尾部量词(N家/N个/两家...)；绝不触碰否定词
#   - 谓词词缀表：前缀(项目/年度/完成/申请/选择/上岗/例行)、后缀(要求/方式/节点)
#   - 融合键 fused = norm(subject)+norm(predicate)：消除主谓切分边界的任意性
PAREN_RE = re.compile(r"[（(][^（）()]*[）)]")
CONNECTIVE_RE = re.compile(r"并在|并且|以及|并|且")
MODAL_PREFIX_RE = re.compile(r"^(必须|须|应当|应|需要|需)")
COUNT_SUFFIX_RE = re.compile(r"[零一两二三四五六七八九十百千\d]+[家个项条名位]$")
PRED_LEAD = ("项目", "年度", "完成", "申请", "选择", "上岗", "例行")
PRED_TAIL = ("要求", "方式", "节点")

def norm(s: str) -> str:
    s = unicodedata.normalize("NFKC", s or "")
    s = s.strip().lower()
    s = PAREN_RE.sub("", s)          # 括号注释剔除（先于边缘裁剪，保证配对括号整体移除）
    s = re.sub(r"\s+", " ", s)
    s = s.strip(WRAP_PUNCT)
    # 去除主体内部空格（中文场景 NFKC 后残留的分隔），保留字母数字间空格
    s = re.sub(r"(?<![a-z0-9]) | (?![a-z0-9])", "", s)
    return s

def norm_text_value(s: str) -> str:
    """text/enum 取值的深度归一（仅用于匹配，不改变存储表述）。"""
    s = norm(s)
    s = s.replace("的", "")
    s = CONNECTIVE_RE.sub("", s)
    s = re.sub(r"[、，,·]", "", s)
    s = MODAL_PREFIX_RE.sub("", s)
    s = COUNT_SUFFIX_RE.sub("", s)
    return s.strip(WRAP_PUNCT)

def norm_predicate(p: str) -> str:
    p = norm(p)
    for lead in PRED_LEAD:
        if p.startswith(lead) and len(p) > len(lead) + 1:
            p = p[len(lead):]
            break
    for tail in PRED_TAIL:
        if p.endswith(tail) and len(p) > len(tail) + 1:
            p = p[: -len(tail)]
            break
    return p

def claim_key(subject: str, predicate: str) -> str:
    return f"{norm(subject)}@{norm_predicate(predicate)}"

def fused_key(subject: str, predicate: str) -> str:
    """主谓融合键：容忍主/谓切分边界漂移（如 '幽能引擎@原型机测试时间'
    与 '幽能引擎原型机@测试时间' 融合后相等）。"""
    return norm(subject) + norm_predicate(predicate)

UNIT_ALIAS = {
    "日": "天", "自然日": "天", "个自然日": "天", "块": "元", "rmb": "元",
    "人民币": "元", "个工作日": "工作日", "名": "个", "位": "个", "人": "个",
}
CN_NUM = {"零":0,"一":1,"两":2,"二":2,"三":3,"四":4,"五":5,"六":6,"七":7,"八":8,"九":9,"十":10}
MULT = {"万": 1e4, "亿": 1e8}

NUM_RE = re.compile(r"(-?\d+(?:\.\d+)?)([万亿]?)\s*([%％]|[a-z℃°]+|[\u4e00-\u9fff]{1,4})?", re.I)
TIME_RE = re.compile(r"\b(\d{1,2}):(\d{2})\b")
YEAR_RE = re.compile(r"(\d{4})\s*年")
DATE_RE = re.compile(r"(\d{4})\s*年\s*(\d{1,2})\s*月(?:\s*(\d{1,2})\s*日)?")

def _clean_unit(u: str) -> str:
    u = (u or "").strip()
    u = UNIT_ALIAS.get(u, u)
    # 截断非单位尾词（如 "元，超出" 不会出现——单位由正则限长；这里再保守裁剪）
    for stop in ("内", "前", "后"):
        if u.endswith(stop) and len(u) > 1:
            u = u[:-1]
    return UNIT_ALIAS.get(u, u)

def cn_word_to_num(s: str):
    if s in CN_NUM:
        return CN_NUM[s]
    return None

def value_normalize(value: str, kind_hint: str):
    """返回 (value_norm, value_kind)。归一失败降级 text（§5.3）。"""
    raw = unicodedata.normalize("NFKC", value or "").strip()
    # 日期优先
    m = DATE_RE.search(raw)
    if m:
        y, mo, d = m.group(1), m.group(2), m.group(3)
        vn = f"{y}-{int(mo):02d}" + (f"-{int(d):02d}" if d else "")
        return vn, "date"
    m = YEAR_RE.search(raw)
    if m:
        return m.group(1), "date"
    # 时刻
    m = TIME_RE.search(raw)
    if m:
        return f"{int(m.group(1)):02d}:{m.group(2)}", "number"
    # 区间（两个数值经 至/~/- 连接）
    nums = NUM_RE.findall(raw)
    if len(nums) >= 2 and re.search(r"至|~|—|--", raw):
        parts = []
        unit = ""
        for n, mult, u in nums[:2]:
            v = float(n) * MULT.get(mult, 1)
            parts.append(f"{v:g}")
            if u:
                unit = _clean_unit(u)
        return f"{parts[0]}~{parts[1]}|{unit}", "number"
    # 单数值
    if nums:
        n, mult, u = nums[0]
        v = float(n) * MULT.get(mult, 1)
        if u in ("%", "％"):
            return f"{v/100:g}|", "number"
        return f"{v:g}|{_clean_unit(u)}", "number"
    # 中文数字（"两个工程师"）
    for ch in raw:
        cv = cn_word_to_num(ch)
        if cv is not None and any(k in raw for k in ("个", "名", "位", "人", "次")):
            return f"{cv}|个", "number"
    if kind_hint == "enum":
        return norm_text_value(raw), "enum"
    return norm_text_value(raw), "text"

def bigrams(s: str):
    s = s.replace("@", "")
    return {s[i:i+2] for i in range(len(s)-1)} if len(s) > 1 else {s}

def key_sim(k1: str, k2: str) -> float:
    b1, b2 = bigrams(k1), bigrams(k2)
    if not b1 or not b2:
        return 0.0
    return len(b1 & b2) / len(b1 | b2)

# ---------------- 评估 ----------------

def load_docs():
    docs = {}
    for p in sorted((BASE / "docs").glob("*.md")):
        docs[p.name] = p.read_text(encoding="utf-8")
    return docs

def quote_located(doc_text: str, quote: str) -> bool:
    if not quote:
        return False
    if quote in doc_text:
        return True
    # 空白折叠后匹配（§5.3 二级定位）
    fold = lambda s: re.sub(r"\s+", "", s)
    return fold(quote) in fold(doc_text)

def enrich(c):
    c["_key"] = claim_key(c["subject"], c["predicate"])
    c["_fused"] = fused_key(c["subject"], c["predicate"])
    c["_vn"], c["_vk"] = value_normalize(c["value"], c.get("value_kind", "text"))
    return c

def match_doc(preds, golds, relaxed_th=0.5):
    """贪心 1-1 匹配。返回 matches: list of (pred_idx, gold_idx, tier)。
    tier: strict(融合键相等+值相等) / relaxed(键相似+值相等) / key_only(键同值异)"""
    used_g, matches = set(), []
    # pass1: strict —— 融合键相等 + 值相等
    for pi, p in enumerate(preds):
        for gi, g in enumerate(golds):
            if gi in used_g: continue
            if p["_fused"] == g["_fused"] and p["_vn"] == g["_vn"]:
                matches.append((pi, gi, "strict")); used_g.add(gi); break
    matched_p = {m[0] for m in matches}
    # pass2: relaxed —— 键相似 + 值相等
    for pi, p in enumerate(preds):
        if pi in matched_p: continue
        best = (None, 0.0)
        for gi, g in enumerate(golds):
            if gi in used_g: continue
            s = key_sim(p["_key"], g["_key"])
            if s >= relaxed_th and p["_vn"] == g["_vn"] and s > best[1]:
                best = (gi, s)
        if best[0] is not None:
            matches.append((pi, best[0], "relaxed")); used_g.add(best[0]); matched_p.add(pi)
    # pass3: key_only —— 融合键相等但值不同（抽到了事实但值归一分歧）
    for pi, p in enumerate(preds):
        if pi in matched_p: continue
        for gi, g in enumerate(golds):
            if gi in used_g: continue
            if p["_fused"] == g["_fused"]:
                matches.append((pi, gi, "key_only")); used_g.add(gi); matched_p.add(pi); break
    return matches

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--run", default=str(BASE / "runs" / "run1.json"))
    ap.add_argument("--relaxed-th", type=float, default=0.5)
    args = ap.parse_args()

    docs = load_docs()
    run = json.loads(Path(args.run).read_text(encoding="utf-8"))
    golds_by_doc, gold_by_id = {}, {}
    for p in sorted((BASE / "gold").glob("*.json")):
        gd = json.loads(p.read_text(encoding="utf-8"))
        claims = [enrich(dict(c)) for c in gd["claims"]]
        golds_by_doc[gd["doc"]] = claims
        for c in claims:
            gold_by_id[c["id"]] = (gd["doc"], c)

    total_p = total_g = 0
    tier_count = {"strict": 0, "relaxed": 0, "key_only": 0}
    quote_ok = quote_total = 0
    kind_agree = kind_total = 0
    gold_to_pred = {}   # gold_id -> (pred_claim, tier)
    rows = []

    for doc, gclaims in golds_by_doc.items():
        pclaims = [enrich(dict(c)) for c in run["docs"].get(doc, [])]
        total_p += len(pclaims); total_g += len(gclaims)
        for c in pclaims:
            quote_total += 1
            if quote_located(docs[doc], c.get("quote", "")):
                quote_ok += 1
        ms = match_doc(pclaims, gclaims, args.relaxed_th)
        for pi, gi, tier in ms:
            tier_count[tier] += 1
            g = gclaims[gi]; p = pclaims[pi]
            gold_to_pred[g["id"]] = (p, tier)
            kind_total += 1
            if p["_vk"] == g["_vk"]:
                kind_agree += 1
        tp = sum(1 for m in ms if m[2] in ("strict", "relaxed"))
        strict_tp = sum(1 for m in ms if m[2] == "strict")
        unmatched_g = [g["id"] for gi, g in enumerate(gclaims)
                       if gi not in {m[1] for m in ms}]
        unmatched_p = [pclaims[pi]["_key"] for pi in range(len(pclaims))
                       if pi not in {m[0] for m in ms}]
        rows.append((doc, len(pclaims), len(gclaims), strict_tp, tp, unmatched_g, unmatched_p))

    tp_all = tier_count["strict"] + tier_count["relaxed"]
    strict_p = tier_count["strict"] / total_p if total_p else 0
    strict_r = tier_count["strict"] / total_g if total_g else 0
    P = tp_all / total_p if total_p else 0
    R = tp_all / total_g if total_g else 0

    print("=" * 76)
    print("C1 抽取质量评估  —  run:", Path(args.run).name)
    print("=" * 76)
    print(f"{'文档':34s} {'pred':>4s} {'gold':>4s} {'strictTP':>8s} {'TP':>4s}")
    for doc, np_, ng, stp, tp, ug, up in rows:
        print(f"{doc:34s} {np_:4d} {ng:4d} {stp:8d} {tp:4d}")
        if ug: print(f"    漏检 gold: {', '.join(ug)}")
        if up: print(f"    多抽 pred(键): {'; '.join(up)}")
    print("-" * 76)
    print(f"总体   pred={total_p} gold={total_g}")
    print(f"strict 匹配: {tier_count['strict']}  (P={strict_p:.3f} R={strict_r:.3f})")
    print(f"relaxed 追加: {tier_count['relaxed']}  key_only(键同值异): {tier_count['key_only']}")
    print(f"合并口径   P={P:.3f}  R={R:.3f}   [门槛 P>=0.85 R>=0.80]")
    print(f"quote 定位率: {quote_ok}/{quote_total} = {quote_ok/quote_total:.3f}")
    print(f"value_kind 一致率(匹配对): {kind_agree}/{kind_total} = {kind_agree/kind_total:.3f}")

    # ---------------- 预埋矛盾对通道召回 ----------------
    print("=" * 76)
    print("预埋矛盾对 — 声明键通道召回")
    print("=" * 76)
    contr = json.loads((BASE / "contradictions.json").read_text(encoding="utf-8"))
    caught_strict = caught_relaxed = n_conflict = 0
    for pair in contr["pairs"]:
        ga = gold_by_id[pair["a"]["gold_id"]][1]
        gb = gold_by_id[pair["b"]["gold_id"]][1]
        pa = gold_to_pred.get(pair["a"]["gold_id"])
        pb = gold_to_pred.get(pair["b"]["gold_id"])
        status = []
        if pair["conflict"]:
            n_conflict += 1
        if not pa or not pb:
            miss = [x for x, y in (("A", pa), ("B", pb)) if not y]
            print(f"[{pair['pair_id']}] {pair['fact']}: ✗ 未抽取到 {'/'.join(miss)} 侧声明")
            continue
        ka, kb = pa[0]["_key"], pb[0]["_key"]
        fa, fb = pa[0]["_fused"], pb[0]["_fused"]
        va, vb = pa[0]["_vn"], pb[0]["_vn"]
        sim = key_sim(fa, fb)
        if pair["conflict"]:
            if fa == fb and va != vb:
                caught_strict += 1; caught_relaxed += 1
                status.append("✓ strict 通道召回")
            elif sim >= args.relaxed_th and va != vb:
                caught_relaxed += 1
                status.append(f"◐ 仅 relaxed 召回 (keySim={sim:.2f})")
            elif va == vb:
                status.append(f"✗ 值归一后相同({va})——通道判不出矛盾")
            else:
                status.append(f"✗ 键不匹配 (sim={sim:.2f}) 且低于阈值")
        else:
            if fa == fb and va == vb:
                status.append("✓ 一致对照正确（同键同值,不误报）")
            elif fa == fb and va != vb:
                status.append(f"✗ 误报风险：同键但值归一不同 ({va} vs {vb})")
            else:
                status.append(f"— 键未对齐 (sim={sim:.2f})，对照无效")
        print(f"[{pair['pair_id']}] {pair['fact']}:")
        print(f"    A key={ka}  vn={va}")
        print(f"    B key={kb}  vn={vb}")
        print(f"    {' | '.join(status)}")
    print("-" * 76)
    print(f"矛盾对召回: strict 通道 {caught_strict}/{n_conflict}, "
          f"strict+relaxed {caught_relaxed}/{n_conflict}")
    verdict = "PASS" if (P >= 0.85 and R >= 0.80) else "FAIL"
    print(f"\n门槛判定: {verdict}  (P={P:.3f}, R={R:.3f})")
    return 0 if verdict == "PASS" else 1

if __name__ == "__main__":
    sys.exit(main())
