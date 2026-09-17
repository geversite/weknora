#!/usr/bin/env python3
"""Stage generated Conflict V2 SVG figures for the IEEE LaTeX manuscript.

Copies the five editable SVG sources produced by generate_conflict_v2_figures.py
into paper/ieee/figures (or another caller-selected directory). With
--convert-pdf it invokes a caller-provided Inkscape executable once per figure,
so the IEEE source can use pre-rendered PDFs without relying on LaTeX shell
escape at build time.

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


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Copy generated Conflict V2 SVGs into an IEEE paper figures directory.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--source-dir", required=True, help="Output directory from generate_conflict_v2_figures.py")
    parser.add_argument("--output-dir", default=str(ROOT / "paper/ieee/figures"), help="LaTeX figures directory")
    parser.add_argument("--convert-pdf", action="store_true", help="Use Inkscape to create PDFs after copying SVGs")
    parser.add_argument("--inkscape", default="inkscape", help="Inkscape executable used only with --convert-pdf")
    parser.add_argument("--overwrite", action="store_true", help="Allow an existing non-empty output directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        source = Path(args.source_dir).expanduser().resolve()
        output = Path(args.output_dir).expanduser().resolve()
        if not source.is_dir():
            raise StageError(f"source directory 不存在: {source}")
        missing = [name for name in REQUIRED_SVGS if not (source / name).is_file()]
        if missing:
            raise StageError("source directory 缺少 SVG: " + ", ".join(missing))
        if output.exists() and any(output.iterdir()) and not args.overwrite:
            raise StageError(f"输出目录已存在且非空: {output}（需要 --overwrite）")
        output.mkdir(parents=True, exist_ok=True)

        staged: list[dict[str, str]] = []
        for name in REQUIRED_SVGS:
            src = source / name
            dst = output / name
            shutil.copy2(src, dst)
            record = {"svg": name, "source": str(src), "staged": str(dst)}
            if args.convert_pdf:
                pdf = dst.with_suffix(".pdf")
                command = [args.inkscape, str(dst), "--export-type=pdf", f"--export-filename={pdf}"]
                try:
                    result = subprocess.run(command, check=False, capture_output=True, text=True, timeout=120)
                except FileNotFoundError as exc:
                    raise StageError(
                        f"找不到 Inkscape ({args.inkscape})；安装 inkscape，或省略 --convert-pdf 并使用 latexmk -shell-escape。",
                    ) from exc
                except subprocess.TimeoutExpired as exc:
                    raise StageError(f"Inkscape 转换超时: {name}") from exc
                if result.returncode != 0 or not pdf.is_file():
                    raise StageError(
                        f"Inkscape 转换失败 {name}: {(result.stderr or result.stdout).strip()[:2000]}",
                    )
                record["pdf"] = str(pdf)
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
