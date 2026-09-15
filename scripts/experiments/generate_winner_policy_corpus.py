#!/usr/bin/env python3
"""Generate a deterministic, split-safe synthetic C4.10 policy corpus.

This generator is intentionally a *controlled policy corpus*, not a source of
real-document accuracy claims. It writes small fictional DOCX files plus a
C4.10 manifest whose fact families, metadata failure modes, expected winners,
and development/holdout split are explicit and auditable.

No HTTP API, model, Asynq, Docker, or database is contacted. A separate
`build_winner_corpus_matrix.py` invocation materializes executable matrices.
"""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import hashlib
import json
import sys
import zipfile
from pathlib import Path
from typing import Any
from xml.sax.saxutils import escape as xml_escape


ROOT = Path(__file__).resolve().parents[2]
GENERATOR_VERSION = "c410-synthetic-policy-v1"
DEFAULT_DEVELOPMENT_FAMILIES = 12
DEFAULT_HOLDOUT_FAMILIES = 12
MAX_FAMILIES_PER_SPLIT = 20
VALID_VARIANTS = {"v1", "c1", "c2-rules", "c2-batch"}

# The two pools deliberately share a controlled grammatical form but have no
# identical fact subject. This keeps each individual three-source case stable
# while ensuring development and holdout do not reuse an exact fact label.
DEVELOPMENT_FACTS = [
    "国内出差餐费补贴",
    "国内出差住宿补贴",
    "市内交通补贴",
    "出差通讯补贴",
    "外勤误餐补贴",
    "客户拜访餐费补贴",
    "驻场服务餐费补贴",
    "项目现场交通补贴",
    "员工培训餐费补贴",
    "商务接待餐费补贴",
    "会议保障餐费补贴",
    "外出巡检交通补贴",
    "采购现场餐费补贴",
    "客服外勤交通补贴",
    "售后服务餐费补贴",
    "技术支持交通补贴",
    "市场活动餐费补贴",
    "稽核出差住宿补贴",
    "区域走访交通补贴",
    "差旅保险补贴",
]
HOLDOUT_FACTS = [
    "跨城拜访餐费补贴",
    "业务调研住宿补贴",
    "网点巡查交通补贴",
    "客户培训餐费补贴",
    "驻点运维住宿补贴",
    "紧急现场交通补贴",
    "专项审计餐费补贴",
    "交付支持交通补贴",
    "业务督导住宿补贴",
    "合作机构拜访餐费补贴",
    "产品宣讲交通补贴",
    "渠道维护餐费补贴",
    "风险排查住宿补贴",
    "现场验收交通补贴",
    "账户核查餐费补贴",
    "业务联调住宿补贴",
    "区域培训交通补贴",
    "项目复盘餐费补贴",
    "远程支援住宿补贴",
    "签约拜访交通补贴",
]
POSITIVE_CASE_TYPES = (
    "date_version_ordered",
    "date_version_out_of_order",
    "date_only_ordered",
    "version_only_out_of_order",
)
NEGATIVE_CASE_TYPES = (
    "cross_issuer_pair",
    "mixed_issuer_triplet",
    "metadata_missing_triplet",
    "date_version_disagreement_pair",
    "metadata_tie_pair",
    "date_interval_overlap_pair",
)


class CorpusGenerationError(RuntimeError):
    """The requested synthetic corpus cannot be generated safely."""


def json_dump(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while block := handle.read(1024 * 1024):
            digest.update(block)
    return digest.hexdigest()


def portable_path(path: Path) -> str:
    """Prefer repo-relative references when output is under this checkout."""
    resolved = path.resolve()
    try:
        return resolved.relative_to(ROOT).as_posix()
    except ValueError:
        return str(resolved)


def docx_member(name: str, content: bytes) -> zipfile.ZipInfo:
    # Fixed ZIP metadata makes source SHA-256 deterministic across generations.
    info = zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0))
    info.compress_type = zipfile.ZIP_DEFLATED
    info.external_attr = 0o600 << 16
    return info


def write_docx(path: Path, lines: list[str]) -> None:
    if not lines or any(not line.strip() for line in lines):
        raise CorpusGenerationError(f"DOCX 缺少可解析正文: {path}")
    paragraphs = "".join(
        "<w:p><w:r><w:t xml:space=\"preserve\">"
        + xml_escape(line)
        + "</w:t></w:r></w:p>"
        for line in lines
    )
    document_xml = f"""<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    {paragraphs}
    <w:sectPr>
      <w:pgSz w:w="12240" w:h="15840"/>
      <w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/>
    </w:sectPr>
  </w:body>
</w:document>
""".encode("utf-8")
    content_types = b"""<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>
"""
    relationships = b"""<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>
"""
    path.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        archive.writestr(docx_member("[Content_Types].xml", content_types), content_types)
        archive.writestr(docx_member("_rels/.rels", relationships), relationships)
        archive.writestr(docx_member("word/document.xml", document_xml), document_xml)


def chinese_date(value: dt.date, precision: str = "day") -> str:
    if precision == "year":
        return f"{value.year}年"
    if precision == "month":
        return f"{value.year}年{value.month}月"
    return f"{value.year}年{value.month}月{value.day}日"


def metadata_title(issuer: str, effective_date: str = "", version: str = "") -> str:
    fields = [f"发布机构：{issuer}"]
    if effective_date:
        fields.append(f"生效日期：{effective_date}")
    if version:
        fields.append(f"版本号：{version}")
    return "；".join(fields)


def conflict_pairs(case_id: str, documents: list[dict[str, Any]]) -> list[dict[str, str]]:
    pairs: list[dict[str, str]] = []
    for left_index in range(len(documents)):
        for right_index in range(left_index + 1, len(documents)):
            left = str(documents[left_index]["id"])
            right = str(documents[right_index]["id"])
            pairs.append({
                "id": f"{case_id}_P{left_index + 1}{right_index + 1}",
                "left": left,
                "right": right,
            })
    return pairs


def case_types_for_count(count: int, positive_count: int) -> list[str]:
    if positive_count < 0 or positive_count > count:
        raise CorpusGenerationError(f"非法 positive allocation: count={count}, positive={positive_count}")
    negative_count = count - positive_count
    return [
        *(POSITIVE_CASE_TYPES[index % len(POSITIVE_CASE_TYPES)] for index in range(positive_count)),
        *(NEGATIVE_CASE_TYPES[index % len(NEGATIVE_CASE_TYPES)] for index in range(negative_count)),
    ]


def balanced_positive_counts(development_count: int, holdout_count: int) -> tuple[int, int]:
    """Allocate positives globally, not independently per split.

    A 15 + 15 controlled pilot must remain 15 positives + 15 negatives rather
    than accidentally becoming 16 + 14 because both splits rounded upward.
    Development receives the extra positive when the combined count is odd;
    the holdout allocation remains within its requested size for every allowed
    4–20 split combination.
    """
    total_positive = (development_count + holdout_count + 1) // 2
    development_positive = (development_count + 1) // 2
    holdout_positive = total_positive - development_positive
    if not 0 <= holdout_positive <= holdout_count:
        raise CorpusGenerationError(
            f"无法平衡 development/holdout positives: dev={development_count}, holdout={holdout_count}",
        )
    return development_positive, holdout_positive


def is_positive_case(case_type: str) -> bool:
    return case_type in POSITIVE_CASE_TYPES


def semantic_sentence(subject: str, value: int) -> str:
    # The surface form is intentionally simple, Chinese, and stable inside a
    # family. It is based on the exact phrase used by the successful C4 fixture.
    return f"{subject}每日标准为 {value} 元。"


def date_for(serial: int, offset: int = 0) -> dt.date:
    # Spreading one case over Jan/Jun/next-Jan makes a strict metadata order
    # unambiguous and avoids month/year interval overlaps in positive cases.
    base_year = 2200 + serial
    if offset == 0:
        return dt.date(base_year, 1, 10)
    if offset == 1:
        return dt.date(base_year, 6, 10)
    return dt.date(base_year + 1, 1, 10)


def make_document(
    output: Path,
    split: str,
    case_id: str,
    role: str,
    subject: str,
    value: int,
    title: str,
) -> dict[str, Any]:
    document_id = f"{case_id}_{role}"
    path = output / "docs" / split / case_id / f"{document_id}.docx"
    write_docx(path, [semantic_sentence(subject, value)])
    digest = sha256_file(path)
    return {
        "id": document_id,
        "path": portable_path(path),
        "title": title,
        "ingest_mode": "file",
        "source_sha256": digest,
        "source_document_id": document_id,
        "source_relative_path": portable_path(path),
        "metadata_evidence_location": "Synthetic generator scenario title",
    }


def rotate_documents(documents: list[dict[str, Any]], order: tuple[int, ...]) -> list[dict[str, Any]]:
    return [documents[index] for index in order]


def build_case(
    output: Path,
    split: str,
    sequence: int,
    case_type: str,
    subject: str,
) -> dict[str, Any]:
    case_id = f"{split}_{case_type}_{sequence + 1:02d}"
    family_id = f"{split}_fact_family_{sequence + 1:02d}"
    issuer = f"{('开发' if split == 'development' else '留出')}示例机构{sequence + 1:02d}"
    other_issuer = f"{('外部' if split == 'development' else '异源')}示例机构{sequence + 1:02d}"
    base_value = 100 + sequence * 30 + (0 if split == "development" else 700)
    values = (base_value, base_value + 50, base_value + 100)
    d1, d2, d3 = date_for(sequence + (0 if split == "development" else 100), 0), \
        date_for(sequence + (0 if split == "development" else 100), 1), \
        date_for(sequence + (0 if split == "development" else 100), 2)

    def doc(role: str, value: int, title: str) -> dict[str, Any]:
        return make_document(output, split, case_id, role, subject, value, title)

    if case_type == "date_version_ordered":
        documents = [
            doc("v1", values[0], metadata_title(issuer, chinese_date(d1), "V1.0")),
            doc("v2", values[1], metadata_title(issuer, chinese_date(d2), "V2.0")),
            doc("v3", values[2], metadata_title(issuer, chinese_date(d3), "V3.0")),
        ]
        upload_order = (1, 0, 2)  # V2 → V1 → V3
        winner = documents[2]["id"]
        description = "同发布机构、日期和版本号一致递增的三来源正例。"
    elif case_type == "date_version_out_of_order":
        documents = [
            doc("v1", values[0], metadata_title(issuer, chinese_date(d1), "V1.0")),
            doc("v2", values[1], metadata_title(issuer, chinese_date(d2), "V2.0")),
            doc("v3", values[2], metadata_title(issuer, chinese_date(d3), "V3.0")),
        ]
        upload_order = (2, 0, 1)  # V3 → V1 → V2; latest-upload baseline is wrong.
        winner = documents[2]["id"]
        description = "同发布机构三来源正例，故意让最新上传文件不是 metadata 严格最大版本。"
    elif case_type == "date_only_ordered":
        documents = [
            doc("d1", values[0], metadata_title(issuer, chinese_date(d1))),
            doc("d2", values[1], metadata_title(issuer, chinese_date(d2))),
            doc("d3", values[2], metadata_title(issuer, chinese_date(d3))),
        ]
        upload_order = (2, 0, 1)
        winner = documents[2]["id"]
        description = "同发布机构、仅有完整生效日期且严格递增的三来源正例。"
    elif case_type == "version_only_out_of_order":
        documents = [
            doc("v1", values[0], metadata_title(issuer, version="V1.0")),
            doc("v2", values[1], metadata_title(issuer, version="V2.0")),
            doc("v3", values[2], metadata_title(issuer, version="V3.0")),
        ]
        upload_order = (1, 2, 0)  # V2 → V3 → V1; latest-upload baseline is wrong.
        winner = documents[2]["id"]
        description = "同发布机构、仅有显式版本号且严格递增的三来源正例。"
    elif case_type == "cross_issuer_pair":
        documents = [
            doc("issuer_a", values[0], metadata_title(issuer, chinese_date(d1), "V1.0")),
            doc("issuer_b", values[1], metadata_title(other_issuer, chinese_date(d2), "V2.0")),
        ]
        upload_order = (0, 1)
        winner = ""
        description = "同一事实数值冲突但发布机构不同，必须 no_proposal。"
    elif case_type == "mixed_issuer_triplet":
        documents = [
            doc("issuer_a_v1", values[0], metadata_title(issuer, chinese_date(d1), "V1.0")),
            doc("issuer_a_v2", values[1], metadata_title(issuer, chinese_date(d2), "V2.0")),
            doc("issuer_b_v3", values[2], metadata_title(other_issuer, chinese_date(d3), "V3.0")),
        ]
        upload_order = (2, 0, 1)
        winner = ""
        description = "三来源中两个同 issuer、一个异 issuer；局部 C3 vote 可能有方向，但全局必须 no_proposal。"
    elif case_type == "metadata_missing_triplet":
        documents = [
            doc("full_v1", values[0], metadata_title(issuer, chinese_date(d1), "V1.0")),
            doc("full_v2", values[1], metadata_title(issuer, chinese_date(d2), "V2.0")),
            doc("missing", values[2], metadata_title(issuer)),
        ]
        upload_order = (0, 2, 1)
        winner = ""
        description = "第三来源缺少 date/version metadata；全局 proposal 必须 fail closed。"
    elif case_type == "date_version_disagreement_pair":
        documents = [
            doc("date_newer_v1", values[0], metadata_title(issuer, chinese_date(d2), "V1.0")),
            doc("version_newer_date_older", values[1], metadata_title(issuer, chinese_date(d1), "V2.0")),
        ]
        upload_order = (0, 1)
        winner = ""
        description = "同 issuer，但日期和版本号指向相反来源；必须 no_proposal。"
    elif case_type == "metadata_tie_pair":
        tied_title = metadata_title(issuer, chinese_date(d1), "V1.0")
        documents = [
            doc("tie_a", values[0], tied_title),
            doc("tie_b", values[1], tied_title),
        ]
        upload_order = (0, 1)
        winner = ""
        description = "同 issuer、相同完整日期和版本号的 metadata tie；必须 no_proposal。"
    elif case_type == "date_interval_overlap_pair":
        documents = [
            doc("year_precision", values[0], metadata_title(issuer, chinese_date(d1, "year"))),
            doc("day_precision", values[1], metadata_title(issuer, chinese_date(d2))),
        ]
        upload_order = (1, 0)
        winner = ""
        description = "同 issuer，年份区间与具体日期区间重叠；不得假设严格新旧关系。"
    else:
        raise CorpusGenerationError(f"未知 case_type: {case_type}")

    ordered_documents = rotate_documents(documents, upload_order)
    outcome = "adopt_reopen" if is_positive_case(case_type) else "no_proposal"
    return {
        "id": case_id,
        "fact_family_id": family_id,
        "split": split,
        "case_type": case_type,
        "description": description,
        "expected_outcome": outcome,
        "expected_winner_document": winner,
        "adoption_cycles": 1 if outcome == "adopt_reopen" else 0,
        "expected_disputed_fact_count": 1,
        "expected_disputed_fact_anchor_kinds": {"claim_key": 1},
        "documents": ordered_documents,
        "expected_conflict_document_pairs": conflict_pairs(case_id, ordered_documents),
    }


def build_corpus(output: Path, development_families: int, holdout_families: int, variant: str) -> dict[str, Any]:
    cases: list[dict[str, Any]] = []
    development_positive, holdout_positive = balanced_positive_counts(development_families, holdout_families)
    for split, count, positive_count, labels in (
        ("development", development_families, development_positive, DEVELOPMENT_FACTS),
        ("holdout", holdout_families, holdout_positive, HOLDOUT_FACTS),
    ):
        for sequence, case_type in enumerate(case_types_for_count(count, positive_count)):
            cases.append(build_case(output, split, sequence, case_type, labels[sequence]))
    return {
        "schema_version": 1,
        "name": "c410_synthetic_policy_corpus",
        "description": (
            "Deterministic fictional C4.10 policy corpus. Fact labels are lexically split between development and holdout, "
            "but document form and expected labels remain controlled; it is not real-document or human-review evidence."
        ),
        "variant": variant,
        "generator_version": GENERATOR_VERSION,
        "cases": cases,
    }


def catalog_rows(corpus: dict[str, Any]) -> list[dict[str, str]]:
    rows: list[dict[str, str]] = []
    for case in corpus["cases"]:
        rows.append({
            "case_id": str(case["id"]),
            "fact_family_id": str(case["fact_family_id"]),
            "split": str(case["split"]),
            "case_type": str(case["case_type"]),
            "expected_outcome": str(case["expected_outcome"]),
            "expected_winner_document": str(case["expected_winner_document"]),
            "adoption_cycles": str(case["adoption_cycles"]),
            "document_count": str(len(case["documents"])),
            "expected_conflict_pair_count": str(len(case["expected_conflict_document_pairs"])),
            "document_ids": ";".join(str(item["id"]) for item in case["documents"]),
            "source_paths": ";".join(str(item["path"]) for item in case["documents"]),
        })
    return rows


def write_csv(path: Path, rows: list[dict[str, str]]) -> None:
    fields = [
        "case_id", "fact_family_id", "split", "case_type", "expected_outcome", "expected_winner_document",
        "adoption_cycles", "document_count", "expected_conflict_pair_count", "document_ids", "source_paths",
    ]
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        writer.writerows(rows)


def write_readme(path: Path, summary: dict[str, Any]) -> None:
    text = f"""# C4.10 expanded synthetic policy corpus

Generator: `{GENERATOR_VERSION}`

This corpus has `{summary['case_count']}` fictional fact families:

```text
development: {summary['development_case_count']}
holdout:     {summary['holdout_case_count']}
positive adopt_reopen: {summary['positive_case_count']}
negative no_proposal:  {summary['no_proposal_case_count']}
source DOCX files:     {summary['document_count']}
```

All files are generated deterministically and contain one controlled fact. Development and holdout use non-identical Chinese fact subjects, but share a deliberately simple sentence form. The corpus is appropriate for **controlled policy coverage** and regression testing, not real-document generalization or human-label accuracy claims. The default 24 families are cost-conscious; request `15 + 15` families when a 30-family controlled pilot is required (the per-split cap is 20).

## Files

- `corpus.json`: input to `build_winner_corpus_matrix.py`.
- `fact_family_catalog.csv`: one row per fact family, split and expected policy topology.
- `generator_manifest.json`: source counts, SHA-256 values and generator settings.
- `docs/`: generated fictional DOCX sources.

## Recommended execution

Materialize split-safe matrices without calling a service:

```bash
make experiment-c410-plan \\
  CORPUS="{summary['corpus_path']}" \\
  OUTPUT="{summary['plan_suggestion']}"
```

Run **development** once while debugging only:

```bash
python3 scripts/experiments/run_winner_lifecycle_eval.py \\
  --matrix "{summary['plan_suggestion']}/winner_lifecycle_matrix.development.json" \\
  --replicates 1 \\
  --output-dir "{summary['run_suggestion']}/development-r1"
```

Freeze code/configuration before evaluating holdout. Then run holdout independently:

```bash
python3 scripts/experiments/run_winner_lifecycle_eval.py \\
  --matrix "{summary['plan_suggestion']}/winner_lifecycle_matrix.holdout.json" \\
  --replicates 3 \\
  --output-dir "{summary['run_suggestion']}/holdout-r3"

make experiment-c410-fact-eval \\
  MATRIX_RUN="{summary['run_suggestion']}/holdout-r3"
```

The fact-family scorer compares C4.6 with `latest_upload`, `date_only`, `version_only`, and raw C3 local-vote baselines. Its outputs remain controlled synthetic policy metrics.
"""
    path.write_text(text, encoding="utf-8")


def validate_args(args: argparse.Namespace) -> None:
    if len(DEVELOPMENT_FACTS) < MAX_FAMILIES_PER_SPLIT or len(HOLDOUT_FACTS) < MAX_FAMILIES_PER_SPLIT:
        raise CorpusGenerationError("synthetic fact catalog 小于声明的 per-split 上限")
    overlap = set(DEVELOPMENT_FACTS) & set(HOLDOUT_FACTS)
    if overlap:
        raise CorpusGenerationError("development/holdout synthetic subject 重复: " + ", ".join(sorted(overlap)))
    for field in ("development_families", "holdout_families"):
        value = int(getattr(args, field))
        if value < 4 or value > MAX_FAMILIES_PER_SPLIT:
            raise CorpusGenerationError(
                f"--{field.replace('_', '-')} 必须在 4–{MAX_FAMILIES_PER_SPLIT} 之间，实际为 {value}",
            )
    if args.variant not in VALID_VARIANTS:
        raise CorpusGenerationError(f"variant 非法: {args.variant}")


def generate(args: argparse.Namespace) -> dict[str, Any]:
    output = Path(args.output_dir).expanduser().resolve()
    if output.exists() and any(output.iterdir()) and not args.overwrite:
        raise CorpusGenerationError(f"输出目录已存在且非空: {output}（需要 --overwrite）")
    output.mkdir(parents=True, exist_ok=True)
    corpus = build_corpus(output, args.development_families, args.holdout_families, args.variant)
    corpus_path = output / "corpus.json"
    json_dump(corpus_path, corpus)
    rows = catalog_rows(corpus)
    write_csv(output / "fact_family_catalog.csv", rows)
    all_documents = [document for case in corpus["cases"] for document in case["documents"]]
    summary = {
        "schema_version": 1,
        "generator_version": GENERATOR_VERSION,
        "output_dir": str(output),
        "corpus_path": str(corpus_path),
        "plan_suggestion": str(output / "plan"),
        "run_suggestion": str(output / "runs"),
        "variant": args.variant,
        "development_case_count": sum(1 for case in corpus["cases"] if case["split"] == "development"),
        "holdout_case_count": sum(1 for case in corpus["cases"] if case["split"] == "holdout"),
        "case_count": len(corpus["cases"]),
        "positive_case_count": sum(1 for case in corpus["cases"] if case["expected_outcome"] == "adopt_reopen"),
        "no_proposal_case_count": sum(1 for case in corpus["cases"] if case["expected_outcome"] == "no_proposal"),
        "document_count": len(all_documents),
        "case_type_counts": {
            case_type: sum(1 for case in corpus["cases"] if case["case_type"] == case_type)
            for case_type in (*POSITIVE_CASE_TYPES, *NEGATIVE_CASE_TYPES)
            if any(case["case_type"] == case_type for case in corpus["cases"])
        },
        "source_documents": [
            {
                "id": document["id"],
                "path": document["path"],
                "sha256": document["source_sha256"],
            }
            for document in all_documents
        ],
        "note": (
            "Generated synthetic corpus only; no HTTP/model/Asynq/PostgreSQL operation was performed. "
            "Fact labels are explicit controlled policy labels, not real-document ground truth."
        ),
    }
    json_dump(output / "generator_manifest.json", summary)
    write_readme(output / "README.md", summary)
    return summary


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Generate deterministic split-safe synthetic DOCX fact-family corpus for C4.10 policy evaluation.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--output-dir", required=True, help="Dedicated private/generated corpus directory")
    parser.add_argument("--development-families", type=int, default=DEFAULT_DEVELOPMENT_FAMILIES)
    parser.add_argument("--holdout-families", type=int, default=DEFAULT_HOLDOUT_FAMILIES)
    parser.add_argument("--variant", choices=sorted(VALID_VARIANTS), default="c2-rules")
    parser.add_argument("--overwrite", action="store_true", help="Overwrite known generator files in an existing output directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        validate_args(args)
        summary = generate(args)
        print(f"C4.10 synthetic policy corpus generated: {summary['output_dir']}")
        print(
            "  fact families: "
            f"{summary['case_count']} (development={summary['development_case_count']}, "
            f"holdout={summary['holdout_case_count']})"
        )
        print(
            "  policy cases: "
            f"adopt_reopen={summary['positive_case_count']}, no_proposal={summary['no_proposal_case_count']}"
        )
        print(f"  DOCX documents: {summary['document_count']}")
        print("  document body/API/model/database: not accessed")
        return 0
    except CorpusGenerationError as exc:
        print(f"[c4.10-synthetic-corpus] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
