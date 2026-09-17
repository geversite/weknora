# IEEE conference manuscript draft — WeKnora Conflict Detection V2

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

`inkscape` is optional when the SVG package is allowed to invoke it during LaTeX compilation, but installing it is the easiest way to convert staged SVG figures to PDF.

## 2. Stage the already-generated figures

The figure generator writes editable SVG files outside the repository. Copy them into this paper project:

```bash
cd ~/weknora/paper/ieee

make figures \
  FIGURE_SOURCE="$HOME/weknora-paper-assets/conflict-v2-figures"
```

This copies:

```text
fig1_system_architecture.svg
fig2_fact_lifecycle.svg
fig3_cascade_cost_ablation.svg
fig4_holdout_baselines.svg
fig5_fail_closed_state_machine.svg
```

For a staged PDF fallback as well:

```bash
make figures \
  FIGURE_SOURCE="$HOME/weknora-paper-assets/conflict-v2-figures" \
  CONVERT_PDF=1
```

The copied figure files are intentionally Git-ignored. SVG is editable in Inkscape, Figma, Illustrator, diagrams.net, or a browser.

## 3. Build

```bash
make pdf
```

The build runs:

```text
latexmk -pdf -shell-escape -interaction=nonstopmode main.tex
```

Without pre-rendered PDFs, `svg` invokes Inkscape under shell escape. If the venue forbids shell escape, run `make figures CONVERT_PDF=1` first; `main.tex` automatically prefers `figures/<name>.pdf` when present.

## 4. Before venue submission

1. Replace the anonymous author block in `main.tex` according to the conference's anonymity policy.
2. Replace the generic `IEEEtran` class options and any copyright/funding boilerplate with the official conference template.
3. Check page limits, bibliography style, figure-resolution rules, and anonymization requirements.
4. Re-check all bibliography fields in `references.bib`; it is a curated starter list, not a final venue-formatted reference database.
5. Keep the controlled-evaluation limitations in the abstract, evaluation, figures, and conclusion. Do not upgrade synthetic results into real-corpus or human-study claims.

## 5. Evidence boundaries

The paper source is intentionally scoped as a controlled systems/prototype paper:

```text
C4.9: 15/15 controlled lifecycle executions, 9/9 cycles, 15 detector artifacts with dead_letter_count=0
C4.10: binary DocReader integration plus a 12-family synthetic holdout with 3 independent replicates per family
```

These results do not establish real-document generalization, cross-format PDF--DOCX clustering accuracy, human-review accuracy, or provider-seed-controlled causality.

See the frozen reports under `../../docs/` before editing numerical claims.
