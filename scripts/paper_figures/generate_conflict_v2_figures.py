#!/usr/bin/env python3
"""Generate editable SVG figures for the WeKnora Conflict Detection V2 paper.

The figures use only the Python standard library, so a Linux development host
can render them without Graphviz, Matplotlib, a browser, or an image model.
They are deliberately vector SVGs: edit labels/colors in Inkscape, Figma, or a
browser before converting them to a venue-specific PDF/PNG.

All numeric data are frozen controlled-study figures documented in:
  docs/冲突检测V2-论文初稿.md
  docs/冲突检测V2-C4.9-生命周期重复实验评估报告.md
  docs/冲突检测V2-C4.10-扩展SyntheticPolicy评估报告.md
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path
from typing import Any
from xml.sax.saxutils import escape


ROOT = Path(__file__).resolve().parents[2]
FIGURE_VERSION = "conflict-v2-paper-figures-v1"

COLORS = {
    "ink": "#17324D",
    "muted": "#5F6B7A",
    "line": "#8FA3B8",
    "light_line": "#D7E1EA",
    "paper": "#FFFFFF",
    "panel": "#F8FBFD",
    "blue": "#2878B5",
    "teal": "#1E9E89",
    "purple": "#7664C5",
    "orange": "#ED8A19",
    "red": "#D95D54",
    "green": "#2A9D5B",
    "gray": "#AAB7C4",
    "pale_blue": "#E8F3FB",
    "pale_teal": "#E5F7F2",
    "pale_purple": "#F0EDFB",
    "pale_orange": "#FFF1DF",
    "pale_red": "#FCEAE8",
    "pale_green": "#E9F7EE",
}
FONT = "'Noto Sans CJK SC','Microsoft YaHei','PingFang SC','Arial Unicode MS',Arial,sans-serif"


class FigureError(RuntimeError):
    """A requested paper figure cannot be generated safely."""


def safe_git_sha() -> str:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True,
        )
        return result.stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return "unknown"


def fmt_number(value: int | float) -> str:
    if isinstance(value, float) and not value.is_integer():
        return f"{value:.3f}"
    return f"{int(value):,}"


def svg_text(text: str) -> str:
    return escape(str(text), {'"': "&quot;"})


class SVG:
    def __init__(self, width: int, height: int) -> None:
        self.width = width
        self.height = height
        self.elements: list[str] = []

    def rect(
        self, x: float, y: float, width: float, height: float, *, fill: str = COLORS["paper"],
        stroke: str = COLORS["line"], stroke_width: float = 2, radius: float = 16,
        dash: str = "", opacity: float = 1.0,
    ) -> None:
        dash_attr = f' stroke-dasharray="{dash}"' if dash else ""
        self.elements.append(
            f'<rect x="{x}" y="{y}" width="{width}" height="{height}" rx="{radius}" '
            f'fill="{fill}" fill-opacity="{opacity}" stroke="{stroke}" stroke-width="{stroke_width}"{dash_attr}/>'
        )

    def line(
        self, x1: float, y1: float, x2: float, y2: float, *, color: str = COLORS["line"],
        width: float = 2.5, arrow: bool = False, dash: str = "",
    ) -> None:
        marker = ' marker-end="url(#arrow)"' if arrow else ""
        dash_attr = f' stroke-dasharray="{dash}"' if dash else ""
        self.elements.append(
            f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke="{color}" '
            f'stroke-width="{width}" stroke-linecap="round"{marker}{dash_attr}/>'
        )

    def path(
        self, d: str, *, color: str = COLORS["line"], width: float = 2.5,
        fill: str = "none", arrow: bool = False, dash: str = "",
    ) -> None:
        marker = ' marker-end="url(#arrow)"' if arrow else ""
        dash_attr = f' stroke-dasharray="{dash}"' if dash else ""
        self.elements.append(
            f'<path d="{d}" fill="{fill}" stroke="{color}" stroke-width="{width}" '
            f'stroke-linecap="round" stroke-linejoin="round"{marker}{dash_attr}/>'
        )

    def text(
        self, x: float, y: float, value: str, *, size: float = 24, color: str = COLORS["ink"],
        weight: str = "400", anchor: str = "start", italic: bool = False,
    ) -> None:
        style = ' font-style="italic"' if italic else ""
        self.elements.append(
            f'<text x="{x}" y="{y}" font-family="{FONT}" font-size="{size}" fill="{color}" '
            f'font-weight="{weight}" text-anchor="{anchor}"{style}>{svg_text(value)}</text>'
        )

    def multiline(
        self, x: float, y: float, values: list[str], *, size: float = 22, color: str = COLORS["ink"],
        weight: str = "400", anchor: str = "start", line_height: float | None = None,
    ) -> None:
        if not values:
            return
        line_height = line_height or size * 1.38
        spans = []
        for index, value in enumerate(values):
            dy = "0" if index == 0 else str(line_height)
            spans.append(f'<tspan x="{x}" dy="{dy}">{svg_text(value)}</tspan>')
        self.elements.append(
            f'<text x="{x}" y="{y}" font-family="{FONT}" font-size="{size}" fill="{color}" '
            f'font-weight="{weight}" text-anchor="{anchor}">' + "".join(spans) + "</text>"
        )

    def circle(self, cx: float, cy: float, radius: float, *, fill: str, stroke: str = COLORS["line"], width: float = 2) -> None:
        self.elements.append(
            f'<circle cx="{cx}" cy="{cy}" r="{radius}" fill="{fill}" stroke="{stroke}" stroke-width="{width}"/>'
        )

    def polygon(self, points: list[tuple[float, float]], *, fill: str, stroke: str = COLORS["line"], width: float = 2) -> None:
        value = " ".join(f"{x},{y}" for x, y in points)
        self.elements.append(f'<polygon points="{value}" fill="{fill}" stroke="{stroke}" stroke-width="{width}"/>')

    def render(self) -> str:
        body = "\n  ".join(self.elements)
        return f'''<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="{self.width}" height="{self.height}" viewBox="0 0 {self.width} {self.height}" role="img">
  <defs>
    <marker id="arrow" markerWidth="12" markerHeight="12" refX="10" refY="6" orient="auto" markerUnits="strokeWidth">
      <path d="M 0 1 L 10 6 L 0 11 z" fill="{COLORS['line']}"/>
    </marker>
  </defs>
  <rect width="100%" height="100%" fill="{COLORS['paper']}"/>
  {body}
</svg>
'''


def title(canvas: SVG, value: str, subtitle: str = "") -> None:
    canvas.text(70, 72, value, size=39, weight="700")
    if subtitle:
        canvas.text(70, 112, subtitle, size=20, color=COLORS["muted"])
    canvas.line(70, 132, canvas.width - 70, 132, color=COLORS["light_line"], width=2)


def footer(canvas: SVG, value: str) -> None:
    canvas.text(70, canvas.height - 34, value, size=17, color=COLORS["muted"])


def labeled_box(
    canvas: SVG, x: float, y: float, width: float, height: float, heading: str, lines: list[str],
    *, fill: str, stroke: str, title_color: str = COLORS["ink"], body_color: str = COLORS["muted"],
) -> None:
    canvas.rect(x, y, width, height, fill=fill, stroke=stroke, radius=18)
    canvas.text(x + width / 2, y + 38, heading, size=24, color=title_color, weight="700", anchor="middle")
    if lines:
        canvas.multiline(x + width / 2, y + 72, lines, size=18, color=body_color, anchor="middle", line_height=26)


def arrow_label(canvas: SVG, x1: float, y1: float, x2: float, y2: float, label: str = "", *, dash: str = "") -> None:
    canvas.line(x1, y1, x2, y2, arrow=True, dash=dash)
    if label:
        canvas.text((x1 + x2) / 2, (y1 + y2) / 2 - 10, label, size=17, color=COLORS["muted"], anchor="middle")


def figure_1_architecture() -> tuple[str, dict[str, Any]]:
    c = SVG(1800, 1080)
    title(c, "图 1  WeKnora Conflict Detection V2：事实级冲突治理架构", "从文档摄取到 explicit、可撤销的 winner lifecycle")

    sources = [(80, 220, "文档", ["PDF / DOCX / Markdown"]), (80, 430, "Wiki", ["页面修订 / 人工编辑"])]
    for x, y, heading, lines in sources:
        labeled_box(c, x, y, 210, 115, heading, lines, fill=COLORS["panel"], stroke=COLORS["blue"])

    labeled_box(c, 370, 285, 245, 150, "C1 声明抽取", ["subject / predicate", "value / qualifiers / quote"], fill=COLORS["pale_blue"], stroke=COLORS["blue"])
    labeled_box(c, 700, 205, 255, 118, "精确候选", ["same ClaimKey"], fill=COLORS["pale_teal"], stroke=COLORS["teal"])
    labeled_box(c, 700, 400, 255, 118, "语义 fallback", ["未覆盖 chunk", "HybridSearch"], fill=COLORS["pale_orange"], stroke=COLORS["orange"])
    labeled_box(c, 1040, 285, 260, 150, "C2 级联裁决", ["保守规则", "grounded LLM"], fill=COLORS["pale_purple"], stroke=COLORS["purple"])
    labeled_box(c, 1385, 285, 250, 150, "Raw Conflict", ["chunk-pair evidence", "不直接治理"], fill=COLORS["pale_red"], stroke=COLORS["red"])

    arrow_label(c, 290, 277, 370, 335)
    arrow_label(c, 290, 487, 370, 385)
    arrow_label(c, 615, 335, 700, 265)
    arrow_label(c, 615, 385, 700, 458)
    arrow_label(c, 955, 265, 1040, 335)
    arrow_label(c, 955, 458, 1040, 385)
    arrow_label(c, 1300, 360, 1385, 360)

    labeled_box(c, 280, 670, 290, 160, "C4 DisputedFact", ["按 fact anchor 聚类", "sources + values + members"], fill=COLORS["pale_blue"], stroke=COLORS["blue"])
    labeled_box(c, 690, 670, 295, 160, "C4.6 Winner Proposal", ["all-source metadata", "仅 advisory / 可 abstain"], fill=COLORS["pale_teal"], stroke=COLORS["teal"])
    labeled_box(c, 1110, 670, 260, 160, "C4.7 Adoption", ["explicit snapshot", "winner 保留 / loser disable"], fill=COLORS["pale_orange"], stroke=COLORS["orange"])
    labeled_box(c, 1470, 670, 245, 160, "C4.8 Reopen", ["durable record", "precise re-enable"], fill=COLORS["pale_purple"], stroke=COLORS["purple"])

    c.path("M 1510 435 L 1510 595 L 425 595 L 425 670", color=COLORS["line"], width=2.5, arrow=True, dash="8 8")
    c.text(970, 580, "聚类", size=17, color=COLORS["muted"], anchor="middle")
    arrow_label(c, 570, 750, 690, 750)
    arrow_label(c, 985, 750, 1110, 750, "人工/上层显式确认")
    arrow_label(c, 1370, 750, 1470, 750)

    c.rect(360, 900, 1080, 90, fill=COLORS["panel"], stroke=COLORS["light_line"], radius=14)
    c.text(900, 939, "持久化证据：claims · conflict_detection_runs · knowledge_conflicts · disputed_facts · durable winner adoptions", size=20, color=COLORS["muted"], anchor="middle")
    c.text(900, 970, "安全边界：proposal ≠ adoption；adoption ≠ deletion；reopen ≠ automatic re-adoption", size=19, color=COLORS["red"], anchor="middle", weight="700")
    footer(c, "图源：WeKnora Conflict Detection V2 实现与受控实验；建议在终稿中使用矢量 SVG/PDF。")
    return c.render(), {
        "id": "fig1_system_architecture",
        "caption": "WeKnora Conflict Detection V2 从声明抽取、候选与裁决到事实级聚类、proposal、adoption/reopen 的系统架构。",
        "evidence": "系统设计；不含外部准确率主张。",
    }


def figure_2_fact_lifecycle() -> tuple[str, dict[str, Any]]:
    c = SVG(1800, 1020)
    title(c, "图 2  从三条 raw conflicts 到一个可撤销的事实级治理动作", "局部 A/B 方向不会被直接解释为全局 winner")

    docs = [
        (90, 205, "V1 来源", ["100 元", "issuer A · V1"], COLORS["pale_blue"], COLORS["blue"]),
        (90, 410, "V2 来源", ["150 元", "issuer A · V2"], COLORS["pale_teal"], COLORS["teal"]),
        (90, 615, "V3 来源", ["200 元", "issuer A · V3"], COLORS["pale_purple"], COLORS["purple"]),
    ]
    for x, y, heading, lines, fill, stroke in docs:
        labeled_box(c, x, y, 230, 125, heading, lines, fill=fill, stroke=stroke)

    pairs = [
        (480, 230, "r12", ["V1 ↔ V2"]),
        (480, 425, "r13", ["V1 ↔ V3"]),
        (480, 620, "r23", ["V2 ↔ V3"]),
    ]
    for x, y, heading, lines in pairs:
        labeled_box(c, x, y, 205, 90, heading, lines, fill=COLORS["pale_red"], stroke=COLORS["red"])
    arrow_label(c, 320, 267, 480, 275)
    arrow_label(c, 320, 267, 480, 470)
    arrow_label(c, 320, 472, 480, 275)
    arrow_label(c, 320, 472, 480, 665)
    arrow_label(c, 320, 677, 480, 470)
    arrow_label(c, 320, 677, 480, 665)

    labeled_box(c, 820, 350, 330, 235, "一个 DisputedFact", ["claim_key：每日标准", "sources：3", "candidate values：100 / 150 / 200"], fill=COLORS["pale_blue"], stroke=COLORS["blue"])
    arrow_label(c, 685, 275, 820, 415)
    arrow_label(c, 685, 470, 820, 470)
    arrow_label(c, 685, 665, 820, 525)

    labeled_box(c, 1280, 260, 310, 160, "C4.6 Proposal", ["全来源 issuer/date/version", "唯一严格最大：V3", "advisory only"], fill=COLORS["pale_teal"], stroke=COLORS["teal"])
    labeled_box(c, 1280, 555, 310, 160, "Explicit Lifecycle", ["adopt：仅禁用 V1 / V2", "durable record", "reopen：精确恢复 V1 / V2"], fill=COLORS["pale_orange"], stroke=COLORS["orange"])
    arrow_label(c, 1150, 430, 1280, 340, "metadata 一致")
    arrow_label(c, 1435, 420, 1435, 555, "显式 snapshot")

    c.rect(1250, 780, 380, 95, fill=COLORS["pale_red"], stroke=COLORS["red"], radius=14)
    c.multiline(1440, 815, ["metadata missing / issuer mismatch / tie", "→ no proposal → HTTP 409 / no mutation"], size=19, color=COLORS["red"], anchor="middle", weight="700", line_height=27)
    c.path("M 1440 715 L 1440 780", color=COLORS["red"], width=2.5, arrow=True, dash="7 7")
    footer(c, "示例为受控三来源版本事实；raw rows 是检测证据，DisputedFact 才是 proposal/adoption 的治理单位。")
    return c.render(), {
        "id": "fig2_fact_lifecycle",
        "caption": "三个版本来源的 raw chunk-pair conflicts 被聚合为一个 DisputedFact；只有全来源 metadata 条件成立时才可 proposal，且治理动作必须显式且可撤销。",
        "evidence": "C4.6/C4.7/C4.8 设计与受控 lifecycle evidence。",
    }


def draw_bar_panel(
    c: SVG, x: float, y: float, width: float, height: float, heading: str, values: list[tuple[str, float, str]],
    *, formatter: callable = fmt_number,
) -> None:
    c.rect(x, y, width, height, fill=COLORS["panel"], stroke=COLORS["light_line"], radius=16)
    c.text(x + width / 2, y + 38, heading, size=23, weight="700", anchor="middle")
    chart_x, chart_y = x + 55, y + 78
    chart_w, chart_h = width - 90, height - 145
    c.line(chart_x, chart_y + chart_h, chart_x + chart_w, chart_y + chart_h, color=COLORS["line"], width=1.7)
    maximum = max(value for _, value, _ in values) or 1
    gap = chart_w / len(values)
    bar_w = min(74, gap * 0.55)
    for index, (name, value, color) in enumerate(values):
        bar_h = chart_h * value / maximum
        bx = chart_x + index * gap + (gap - bar_w) / 2
        by = chart_y + chart_h - bar_h
        c.rect(bx, by, bar_w, bar_h, fill=color, stroke=color, stroke_width=0, radius=7)
        c.text(bx + bar_w / 2, by - 12, formatter(value), size=17, weight="700", anchor="middle")
        c.multiline(bx + bar_w / 2, chart_y + chart_h + 30, name.split("\n"), size=16, color=COLORS["muted"], anchor="middle", line_height=20)


def figure_3_cascade_cost() -> tuple[str, dict[str, Any]]:
    c = SVG(1800, 900)
    title(c, "图 3  C2 cascade 的真实服务成本消融", "相同 C1 full scenario；C2-B4 保持场景完整性并减少 LLM 调用")
    variants = [("C1", COLORS["gray"]), ("C2-Rules", COLORS["teal"]), ("C2-B4", COLORS["purple"])]
    draw_bar_panel(c, 80, 205, 500, 510, "LLM 调用次数", [(name, value, color) for (name, color), value in zip(variants, [48, 47, 9])])
    draw_bar_panel(c, 650, 205, 500, 510, "Token 总量", [(name, value, color) for (name, color), value in zip(variants, [48762, 47293, 28528])])
    draw_bar_panel(c, 1220, 205, 500, 510, "检测时延（ms）", [(name, value, color) for (name, color), value in zip(variants, [145154, 149111, 75789])])
    c.rect(205, 760, 1390, 72, fill=COLORS["pale_teal"], stroke=COLORS["teal"], radius=12)
    c.text(900, 806, "C2-B4 相对 C1：LLM calls −81.25%（5.33× fewer）；Tokens −41.50%；Duration −47.79%", size=23, color=COLORS["teal"], weight="700", anchor="middle")
    footer(c, "数据来源：C2-B4 final matrix；此图为固定 C1 full scenario 的成本消融，不外推为所有真实文档的平均成本。")
    return c.render(), {
        "id": "fig3_cascade_cost",
        "caption": "C1、C2-Rules 和 C2-B4 在相同场景下的 LLM 调用、Token 和检测时延对比。",
        "evidence": "C2-B4 final cost ablation。",
    }


def figure_4_holdout_baselines() -> tuple[str, dict[str, Any]]:
    c = SVG(1800, 930)
    title(c, "图 4  C4.10 synthetic holdout：全局 proposal 与简化 baseline", "主口径：12 个 fact families，要求每个 family 的 3 次 independent replicates 全部正确")
    methods = [
        ("C4.6\nglobal", 1.000, 0, COLORS["teal"]),
        ("Latest\nupload", 0.167, 6, COLORS["gray"]),
        ("Date\nonly", 0.667, 3, COLORS["orange"]),
        ("Version\nonly", 0.667, 3, COLORS["purple"]),
        ("Raw C3\nlocal vote", 0.833, 2, COLORS["red"]),
    ]
    draw_bar_panel(c, 115, 205, 730, 535, "Fact-family policy accuracy", [(name, accuracy, color) for name, accuracy, _, color in methods], formatter=lambda v: f"{v:.3f}")
    draw_bar_panel(c, 955, 205, 730, 535, "Unsafe actions（越低越好）", [(name, unsafe, color) for name, _, unsafe, color in methods], formatter=fmt_number)
    c.rect(225, 785, 1350, 72, fill=COLORS["pale_teal"], stroke=COLORS["teal"], radius=12)
    c.text(900, 830, "C4.6：accuracy = 1.000，unsafe actions = 0；结果仅适用于构造的 controlled synthetic policy holdout。", size=22, color=COLORS["teal"], weight="700", anchor="middle")
    footer(c, "数据来源：C4.10 extended synthetic holdout（12 fact families × 3 replicates）；不是真实业务文档准确率。")
    return c.render(), {
        "id": "fig4_holdout_baselines",
        "caption": "构造的事实家族 holdout 上，C4.6 global proposal 与 latest-upload、date-only、version-only、raw C3 local vote 的 policy accuracy 和 unsafe actions 对比。",
        "evidence": "C4.10 controlled synthetic holdout；必须保留范围限定。",
    }


def diamond(c: SVG, cx: float, cy: float, width: float, height: float, label: list[str], *, fill: str, stroke: str) -> None:
    c.polygon([(cx, cy - height / 2), (cx + width / 2, cy), (cx, cy + height / 2), (cx - width / 2, cy)], fill=fill, stroke=stroke)
    c.multiline(cx, cy - 12, label, size=18, color=COLORS["ink"], anchor="middle", weight="700", line_height=25)


def figure_5_state_machine() -> tuple[str, dict[str, Any]]:
    c = SVG(1800, 1030)
    title(c, "图 5  Explicit adoption / durable reopen 的 fail-closed 状态机", "proposal 不自动改变检索可见性；任何 stale 或不完整状态均拒绝 mutation")

    labeled_box(c, 95, 390, 245, 130, "Pending DisputedFact", ["raw members pending", "chunks enabled"], fill=COLORS["pale_blue"], stroke=COLORS["blue"])
    diamond(c, 540, 455, 250, 160, ["所有 sources", "metadata 一致且", "唯一严格最大？"], fill=COLORS["pale_teal"], stroke=COLORS["teal"])
    labeled_box(c, 805, 225, 295, 145, "Advisory Proposal", ["winner + version + snapshot", "无副作用"], fill=COLORS["pale_teal"], stroke=COLORS["teal"])
    diamond(c, 1275, 300, 250, 160, ["显式 adoption", "snapshot / topology", "仍匹配？"], fill=COLORS["pale_orange"], stroke=COLORS["orange"])
    labeled_box(c, 1510, 225, 230, 145, "Adopted", ["winner enabled", "loser disabled", "durable record"], fill=COLORS["pale_purple"], stroke=COLORS["purple"])

    labeled_box(c, 805, 640, 295, 145, "No Proposal", ["abstain", "no side effect"], fill=COLORS["pale_red"], stroke=COLORS["red"])
    diamond(c, 1275, 700, 250, 160, ["显式 reopen", "active record /", "targets 匹配？"], fill=COLORS["pale_orange"], stroke=COLORS["orange"])
    labeled_box(c, 1510, 640, 230, 145, "Reopened", ["raw → pending", "record → revoked", "targets re-enabled"], fill=COLORS["pale_blue"], stroke=COLORS["blue"])

    arrow_label(c, 340, 455, 415, 455)
    arrow_label(c, 665, 410, 805, 300, "是")
    arrow_label(c, 665, 500, 805, 700, "否")
    arrow_label(c, 1100, 300, 1150, 300)
    arrow_label(c, 1400, 300, 1510, 300, "是")
    arrow_label(c, 1400, 700, 1510, 700, "是")
    arrow_label(c, 1625, 370, 1625, 640, "人工发现需重开")
    c.path("M 1625 785 L 1625 895 L 215 895 L 215 520", color=COLORS["line"], width=2.5, arrow=True, dash="8 8")
    c.text(900, 880, "回到 pending", size=17, color=COLORS["muted"], anchor="middle")

    c.rect(1135, 465, 390, 95, fill=COLORS["pale_red"], stroke=COLORS["red"], radius=14)
    c.multiline(1330, 500, ["否 / stale / partial / no active adoption", "→ HTTP 409 · no mutation"], size=19, color=COLORS["red"], anchor="middle", weight="700", line_height=27)
    c.path("M 1275 380 L 1275 465", color=COLORS["red"], width=2.5, arrow=True, dash="7 7")
    c.path("M 1275 620 L 1275 560", color=COLORS["red"], width=2.5, arrow=True, dash="7 7")

    footer(c, "C4.7/C4.8 设计：所有 side effects 都以显式 snapshot 和 durable record 为边界，拒绝不完整状态。")
    return c.render(), {
        "id": "fig5_fail_closed_state_machine",
        "caption": "winner proposal、explicit adoption 与 durable reopen 的状态机。任何 metadata、snapshot 或 member/target 状态不匹配均 fail closed。",
        "evidence": "C4.7/C4.8 safety protocol 与 C4.9/C4.10 lifecycle evidence。",
    }


def write_readme(path: Path, figures: list[dict[str, Any]], output: Path) -> None:
    rows = "\n".join(
        f"| `{item['id']}.svg` | {item['caption']} | {item['evidence']} |"
        for item in figures
    )
    text = f"""# WeKnora Conflict Detection V2 paper figures

Generated by `scripts/paper_figures/generate_conflict_v2_figures.py`.

All outputs are editable SVG vectors. This directory is intentionally generated outside Git by default; copy only reviewed final figures into a venue package.

| File | Suggested paper role | Evidence boundary |
|---|---|---|
{rows}

## Use

Open an SVG in a browser, Inkscape, Figma, Illustrator, or diagrams.net. For PDF conversion, if Inkscape is available:

```bash
inkscape fig1_system_architecture.svg --export-type=pdf
```

Do not crop out the footer scope statements without preserving equivalent scope language in the figure caption or main text. Numeric figures come from the frozen controlled reports, not a real-corpus human study.

## Source reports

- `docs/冲突检测V2-C2-生产运行评估报告.md`
- `docs/冲突检测V2-C4.9-生命周期重复实验评估报告.md`
- `docs/冲突检测V2-C4.10-扩展SyntheticPolicy评估报告.md`
- `docs/冲突检测V2-论文初稿.md`

Output directory: `{output}`
"""
    path.write_text(text, encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Generate editable SVG figures for the WeKnora Conflict Detection V2 paper.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--output-dir", required=True, help="Private/output directory for generated SVG vectors")
    parser.add_argument("--overwrite", action="store_true", help="Allow a non-empty output directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        output = Path(args.output_dir).expanduser().resolve()
        if output.exists() and any(output.iterdir()) and not args.overwrite:
            raise FigureError(f"输出目录已存在且非空: {output}（需要 --overwrite）")
        output.mkdir(parents=True, exist_ok=True)
        generated: list[dict[str, Any]] = []
        for filename, builder in (
            ("fig1_system_architecture.svg", figure_1_architecture),
            ("fig2_fact_lifecycle.svg", figure_2_fact_lifecycle),
            ("fig3_cascade_cost_ablation.svg", figure_3_cascade_cost),
            ("fig4_holdout_baselines.svg", figure_4_holdout_baselines),
            ("fig5_fail_closed_state_machine.svg", figure_5_state_machine),
        ):
            content, metadata = builder()
            (output / filename).write_text(content, encoding="utf-8")
            generated.append({"file": filename, **metadata})
        manifest = {
            "schema_version": 1,
            "figure_version": FIGURE_VERSION,
            "git_commit": safe_git_sha(),
            "figures": generated,
            "note": "Generated vector figures from frozen controlled-study data; no API/model/database operation was performed.",
        }
        (output / "figures_manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        write_readme(output / "README.md", generated, output)
        print(f"Conflict V2 paper SVG figures generated: {output}")
        for item in generated:
            print(f"  {item['file']}")
        print("  API/model/database: not contacted")
        return 0
    except FigureError as exc:
        print(f"[paper-figures] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
