#!/usr/bin/env python3
"""Stage generated Conflict V2 SVG figures for the IEEE LaTeX manuscript.

Copies generated SVG sources into paper/ieee/figures (or another caller-selected
directory). Figs. 1/2/5 case schematics are frozen PNGs next to main.tex and
should be staged with --only fig3...,fig4... via `make figures-plots`. With
--convert-pdf it tries Inkscape first and falls back to the cairosvg CLI, so
the IEEE source can use pre-rendered PDFs without LaTeX shell escape.

This tool never calls a model/API/database and never removes source images.
"""

from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
REQUIRED_SVGS = (
    "fig1_system_architecture.svg",
    "fig2_fact_lifecycle.svg",
    "fig3_cascade_cost_ablation.svg",
    "fig4_holdout_baselines.svg",
    "fig5_fail_closed_state_machine.svg",
)


class StageError(RuntimeError):
    """Paper figure assets cannot be staged safely."""


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def convert_svg_to_pdf(svg: Path, pdf: Path, inkscape: str) -> str:
    """Render SVG to PDF. Prefer Inkscape; fall back to the cairosvg CLI."""
    inkscape_err = f"找不到 Inkscape ({inkscape})"
    try:
        result = subprocess.run(
            [inkscape, str(svg), "--export-type=pdf", f"--export-filename={pdf}"],
            check=False,
            capture_output=True,
            text=True,
            timeout=120,
        )
    except FileNotFoundError:
        result = None
    except subprocess.TimeoutExpired as exc:
        raise StageError(f"Inkscape 转换超时: {svg.name}") from exc
    else:
        if result.returncode == 0 and pdf.is_file():
            return "inkscape"
        inkscape_err = (result.stderr or result.stdout).strip()[:2000] or inkscape_err

    try:
        result = subprocess.run(
            ["cairosvg", str(svg), "-o", str(pdf)],
            check=False,
            capture_output=True,
            text=True,
            timeout=120,
        )
    except FileNotFoundError as exc:
        raise StageError(
            f"无法渲染 PDF: Inkscape 失败（{inkscape_err}）；"
            "也找不到 cairosvg CLI。安装 inkscape 或 `pip install cairosvg`。",
        ) from exc
    except subprocess.TimeoutExpired as exc:
        raise StageError(f"cairosvg 转换超时: {svg.name}") from exc
    if result.returncode != 0 or not pdf.is_file():
        detail = (result.stderr or result.stdout).strip()[:2000]
        raise StageError(f"cairosvg 转换失败 {svg.name}: {detail}")
    return "cairosvg"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Copy generated Conflict V2 SVGs into an IEEE paper figures directory.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--source-dir", required=True, help="Output directory from generate_conflict_v2_figures.py")
    parser.add_argument("--output-dir", default=str(ROOT / "paper/ieee/figures"), help="LaTeX figures directory")
    parser.add_argument("--convert-pdf", action="store_true", help="Render PDFs after copying SVGs (Inkscape, then cairosvg)")
    parser.add_argument("--inkscape", default="inkscape", help="Inkscape executable used only with --convert-pdf")
    parser.add_argument("--overwrite", action="store_true", help="Deprecated; existing figure files are overwritten in place")
    parser.add_argument(
        "--only",
        default="",
        help="Comma-separated SVG stems to copy, e.g. fig3_cascade_cost_ablation,fig4_holdout_baselines",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        source = Path(args.source_dir).expanduser().resolve()
        output = Path(args.output_dir).expanduser().resolve()
        if not source.is_dir():
            raise StageError(f"source directory 不存在: {source}")
        wanted = {item.strip() for item in args.only.split(",") if item.strip()}
        names = []
        for name in REQUIRED_SVGS:
            stem = name.rsplit(".", 1)[0]
            if wanted and stem not in wanted and name not in wanted:
                continue
            names.append(name)
        if not names:
            raise StageError(f"no figures matched --only {sorted(wanted)}")
        missing = [name for name in names if not (source / name).is_file()]
        if missing:
            raise StageError("source directory 缺少 SVG: " + ", ".join(missing))
        output.mkdir(parents=True, exist_ok=True)

        staged: list[dict[str, str]] = []
        for name in names:
            src = source / name
            dst = output / name
            shutil.copy2(src, dst)
            record = {"svg": name, "source": str(src), "staged": str(dst)}
            if args.convert_pdf:
                pdf = dst.with_suffix(".pdf")
                record["pdf"] = str(pdf)
                record["pdf_renderer"] = convert_svg_to_pdf(dst, pdf, args.inkscape)
            staged.append(record)

        manifest = {
            "schema_version": 1,
            "source_dir": str(source),
            "output_dir": str(output),
            "convert_pdf": bool(args.convert_pdf),
            "figures": staged,
            "note": "Copied generated paper figures only; no model/API/database operation was performed.",
        }
        json_dump(output / "asset_manifest.json", manifest)
        print(f"IEEE paper figures staged: {output}")
        print(f"  SVG files: {len(staged)}")
        if args.convert_pdf:
            print(f"  PDF files: {len(staged)}")
        print("  API/model/database: not contacted")
        return 0
    except StageError as exc:
        print(f"[stage-ieee-figures] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
