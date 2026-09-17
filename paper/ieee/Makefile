# Generic IEEE conference manuscript build helpers for WeKnora Conflict Detection V2.
# Run from paper/ieee/ or use: make -C paper/ieee <target>

PYTHON ?= python3
LATEXMK ?= latexmk
FIGURE_SOURCE ?= $(HOME)/weknora-paper-assets/conflict-v2-figures
FIGURE_DIR ?= figures

.PHONY: help references figures pdf clean

help:
	@echo "IEEE manuscript targets"
	@echo "  make references                         Sync BibTeX from docs/"
	@echo "  make figures FIGURE_SOURCE=<svg-dir>    Stage 5 SVG figures"
	@echo "  make figures CONVERT_PDF=1              Also convert SVG to PDF via Inkscape"
	@echo "  make pdf                                Build main.tex with latexmk + shell escape"
	@echo "  make clean                              Remove LaTeX build files"

references:
	cp ../../docs/冲突检测V2-论文参考文献.bib references.bib

figures:
	$(PYTHON) ../../scripts/paper_figures/stage_ieee_figures.py --source-dir "$(FIGURE_SOURCE)" --output-dir "$(FIGURE_DIR)" --overwrite $(if $(CONVERT_PDF),--convert-pdf)

pdf: references
	$(LATEXMK) -pdf -shell-escape -interaction=nonstopmode main.tex

clean:
	$(LATEXMK) -C main.tex
