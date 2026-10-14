# IEEE conference manuscript draft — WeKnora fact-level conflict governance

This is a generic IEEEtran conference-format manuscript project. It intentionally does **not** assume a specific conference, author block, page limit, copyright notice, or camera-ready template.

Main source:

```text
main.tex
references.bib
figures/
```

The initial manuscript is English because generic IEEE conference papers are normally submitted in English. The Chinese Markdown source remains available at:

```text
../../docs/冲突检测V2-论文初稿.md
```

## 1. Dependencies

You need a LaTeX installation containing:

```text
IEEEtran.cls
latexmk
cite
booktabs
svg
```

For example, on a Debian/Ubuntu host, install missing components only if necessary:

```bash
sudo apt-get install texlive-latex-extra texlive-publishers latexmk inkscape
```

For staging the plots, either `inkscape` or the `cairosvg` CLI is enough;
`stage_ieee_figures.py` tries Inkscape first and falls back to `cairosvg`:

```bash
pip install cairosvg
```

## 2. Stage the already-generated figures

Figure sourcing is split:

```text
Figs. 1, 2, 5  paper/ieee/fig1.png, fig2.png, fig5.png   (frozen case schematics, in Git)
Figs. 3, 4      figures/fig3_cascade_cost_ablation, fig4_holdout_baselines  (generated plots)
```

Figs. 1/2/5 are complete as committed and must not be regenerated. Only the two
data plots are restaged from the generator output:

```bash
# 1) regenerate just the bar charts (outside the repo)
cd ~/weknora
FIGURE_OUT="$HOME/weknora-paper-assets/conflict-v2-figures"
mkdir -p "$FIGURE_OUT"
make paper-figures \
  FIGURE_OUTPUT="$FIGURE_OUT" \
  ONLY=fig3_cascade_cost_ablation,fig4_holdout_baselines

# 2) stage them into the paper, rendering SVG to PDF on the way
cd paper/ieee
make figures-plots FIGURE_SOURCE="$FIGURE_OUT"
```

This copies `fig3_cascade_cost_ablation.{svg,pdf}` and
`fig4_holdout_baselines.{svg,pdf}` into `figures/` and leaves `fig1.png`,
`fig2.png`, and `fig5.png` untouched. PDF rendering uses Inkscape when present
and falls back to the `cairosvg` CLI otherwise.

The staged plot files are intentionally Git-ignored; the Figs. 1/2/5 PNGs are
tracked. SVG is editable in Inkscape, Figma, Illustrator, diagrams.net, or a
browser.

## 3. Build

```bash
make pdf
```

The build runs:

```text
latexmk -pdf -shell-escape -interaction=nonstopmode main.tex
```

`main.tex` uses `\casefigure` for Figs. 1/2/5 (the tracked PNGs) and
`\plotfigure` for Figs. 3/4 (which prefers `figures/<name>.pdf` and only falls
back to the `svg` package when no PDF was staged). After `make figures-plots`
the build therefore needs no shell escape:

```bash
make figures-plots \
  FIGURE_SOURCE="$HOME/weknora-paper-assets/conflict-v2-figures"

make pdf SHELL_ESCAPE=0
```

## 4. Before venue submission

1. Replace the anonymous author block in `main.tex` according to the conference's anonymity policy.
2. Replace the generic `IEEEtran` class options and any copyright/funding boilerplate with the official conference template.
3. Check page limits, bibliography style, figure-resolution rules, and anonymization requirements.
4. Re-check all bibliography fields in `references.bib`; it is a curated starter list, not a final venue-formatted reference database.
5. Keep the controlled-evaluation limitations in the abstract, evaluation, figures, and conclusion. Do not upgrade synthetic results into real-corpus or human-study claims.

## 5. Evidence boundaries

The paper source is intentionally scoped as a controlled systems/prototype paper:

```text
Lifecycle matrix: 15/15 controlled executions, 9/9 cycles, 15 detector artifacts with dead_letter_count=0
Synthetic holdout: binary DocReader integration plus a 12-family synthetic holdout with 3 independent replicates per family
Public transfer (independent tables): WikiFactDiff pair holdout 60/60 P/R/A=1.0 vs LLM pair judge 1.000/0.967/0.983; proposal exact success 25/30; VitaminC holdout P/R/A=0.750/0.800/0.767 vs LLM pair judge 0.926/0.833/0.883
Synthetic holdout same-task controls: all-source 1.000 unsafe=0; issuer-only 0.917/1; issuer+date 0.833/1; date+version 0.667/2; LLM winner judge 0.833/2
Native observational ingest (independent): 48 files / 6 KBs; parse 1.000; coverage 0.917; rule-routing 0.016; abstention 1.000; filename ablation disputed facts 45 → 31; dead letters 5 both runs; not labeled accuracy
```

These results do not establish real-document generalization, cross-format PDF--DOCX clustering accuracy, human-review accuracy, or provider-seed-controlled causality. Public WikiFactDiff/VitaminC numbers and native observational counts must not be pooled with the synthetic policy tables.

See the frozen reports under `../../docs/` before editing numerical claims.
