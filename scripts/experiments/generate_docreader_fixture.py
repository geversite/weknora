#!/usr/bin/env python3
"""Generate a small deterministic binary-file fixture for C4.10 / DocReader tests.

The fixture is deliberately fictional and controlled.  It gives a developer a
zero-private-data way to exercise the real multipart file upload path with a
mix of PDF and DOCX files before selecting any real business documents.

It does not call HTTP, a model, Asynq, Docker, or PostgreSQL.  The companion
scenario and matrix are generated alongside the source files; running them is
a separate explicit action on a configured WeKnora development environment.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
import zipfile
from pathlib import Path
from typing import Any
from xml.sax.saxutils import escape as xml_escape


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_OUTPUT = ROOT / "testdata/winner_lifecycle_corpus/docreader_fixture"
FIXTURE_VERSION = 1


class FixtureError(RuntimeError):
    """The synthetic fixture cannot be generated or verified safely."""


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
    """Use repo-relative paths for the committed fixture, absolute paths elsewhere."""
    resolved = path.resolve()
    try:
        return resolved.relative_to(ROOT).as_posix()
    except ValueError:
        return str(resolved)


def pdf_escape(text: str) -> bytes:
    # The fixture PDF intentionally uses ASCII-only content and Helvetica. It
    # therefore has no external font dependency and remains easy for common
    # DocReader/PDF text extractors to parse.
    try:
        encoded = text.encode("ascii")
    except UnicodeEncodeError as exc:
        raise FixtureError(f"fixture PDF text 必须是 ASCII: {text!r}") from exc
    return encoded.replace(b"\\", b"\\\\").replace(b"(", b"\\(").replace(b")", b"\\)")


def write_pdf(path: Path, lines: list[str]) -> None:
    if not lines:
        raise FixtureError(f"PDF 缺少正文行: {path}")
    commands = [b"BT", b"/F1 14 Tf", b"72 740 Td"]
    for index, line in enumerate(lines):
        commands.append(b"(" + pdf_escape(line) + b") Tj")
        if index + 1 < len(lines):
            commands.append(b"0 -22 Td")
    commands.append(b"ET")
    stream = b"\n".join(commands) + b"\n"
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "
        b"/Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        b"<< /Length " + str(len(stream)).encode("ascii") + b" >>\nstream\n" + stream + b"endstream",
    ]
    output = bytearray(b"%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
    offsets = [0]
    for object_id, body in enumerate(objects, start=1):
        offsets.append(len(output))
        output.extend(f"{object_id} 0 obj\n".encode("ascii"))
        output.extend(body)
        output.extend(b"\nendobj\n")
    xref_offset = len(output)
    output.extend(f"xref\n0 {len(objects) + 1}\n".encode("ascii"))
    output.extend(b"0000000000 65535 f \n")
    for offset in offsets[1:]:
        output.extend(f"{offset:010d} 00000 n \n".encode("ascii"))
    output.extend(
        f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\nstartxref\n{xref_offset}\n%%EOF\n".encode("ascii"),
    )
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(bytes(output))


def docx_member(name: str, content: bytes) -> zipfile.ZipInfo:
    # Fixed ZIP metadata makes generated source hashes stable across runs.
    info = zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0))
    info.compress_type = zipfile.ZIP_DEFLATED
    info.external_attr = 0o600 << 16
    return info


def write_docx(path: Path, lines: list[str]) -> None:
    if not lines:
        raise FixtureError(f"DOCX 缺少正文行: {path}")
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


def fixture_specs() -> list[dict[str, str]]:
    """Return one-fact controlled documents spanning proposal and abstention paths."""
    return [
        {
            "id": "fixture_ordered_v1",
            "format": "pdf",
            "title": "Issuer: Meridian Test Finance; Effective Date: 2031-01-10; Version: V1.0",
            "body": "The maximum financing limit for one domestic invoice is 100 CNY.",
        },
        {
            "id": "fixture_ordered_v2",
            "format": "docx",
            "title": "Issuer: Meridian Test Finance; Effective Date: 2031-06-10; Version: V2.0",
            "body": "The maximum financing limit for one domestic invoice is 150 CNY.",
        },
        {
            "id": "fixture_ordered_v3",
            "format": "pdf",
            "title": "Issuer: Meridian Test Finance; Effective Date: 2032-01-10; Version: V3.0",
            "body": "The maximum financing limit for one domestic invoice is 200 CNY.",
        },
        {
            "id": "fixture_cross_issuer_a",
            "format": "docx",
            "title": "Issuer: North Test Finance; Effective Date: 2033-01-10; Version: V1.0",
            "body": "The maximum financing limit for one domestic invoice is 300 CNY.",
        },
        {
            "id": "fixture_cross_issuer_b",
            "format": "pdf",
            "title": "Issuer: South Test Finance; Effective Date: 2034-01-10; Version: V2.0",
            "body": "The maximum financing limit for one domestic invoice is 400 CNY.",
        },
        {
            "id": "fixture_direction_date_newer",
            "format": "pdf",
            "title": "Issuer: Meridian Test Finance; Effective Date: 2036-01-10; Version: V1.0",
            "body": "The maximum financing limit for one domestic invoice is 500 CNY.",
        },
        {
            "id": "fixture_direction_version_newer",
            "format": "docx",
            "title": "Issuer: Meridian Test Finance; Effective Date: 2035-01-10; Version: V2.0",
            "body": "The maximum financing limit for one domestic invoice is 600 CNY.",
        },
        {
            "id": "fixture_tie_a",
            "format": "docx",
            "title": "Issuer: Meridian Test Finance; Effective Date: 2037-01-10; Version: V1.0",
            "body": "The maximum financing limit for one domestic invoice is 700 CNY.",
        },
        {
            "id": "fixture_tie_b",
            "format": "pdf",
            "title": "Issuer: Meridian Test Finance; Effective Date: 2037-01-10; Version: V1.0",
            "body": "The maximum financing limit for one domestic invoice is 800 CNY.",
        },
    ]


def write_source_document(path: Path, source_format: str, body: str) -> None:
    lines = ["Synthetic financing rule", body]
    if source_format == "pdf":
        write_pdf(path, lines)
    elif source_format == "docx":
        write_docx(path, lines)
    else:
        raise FixtureError(f"未知 fixture format: {source_format}")


def scenario_for_case(corpus_name: str, case: dict[str, Any]) -> dict[str, Any]:
    scenario: dict[str, Any] = {
        "schema_version": 1,
        "name": f"{corpus_name}_{case['id']}",
        "description": case["description"],
        "min_claims_per_document": 1,
        "documents": case["documents"],
        "expected_conflict_document_pairs": case["expected_conflict_document_pairs"],
        "expected_disputed_fact_count": 1,
        "expected_disputed_fact_anchor_kinds": {"claim_key": 1},
    }
    if case["expected_outcome"] == "adopt_reopen":
        scenario["expected_disputed_fact_winner_count"] = 1
        scenario["expected_disputed_fact_winners"] = [{
            "id": f"{case['id']}_winner",
            "winner_document": case["expected_winner_document"],
            "min_confidence": 0.0,
        }]
    else:
        scenario["expected_disputed_fact_winner_count"] = 0
    return scenario


def pair(pair_id: str, left: str, right: str) -> dict[str, str]:
    return {"id": pair_id, "left": left, "right": right}


def build_corpus(output: Path, sources: dict[str, dict[str, Any]]) -> dict[str, Any]:
    def docs(*identifiers: str) -> list[dict[str, Any]]:
        return [sources[identifier] for identifier in identifiers]

    return {
        "schema_version": 1,
        "name": "c410_docreader_fixture",
        "description": (
            "Fictional controlled PDF/DOCX fixture for C4.10 real multipart/DocReader integration. "
            "It is not a real-document corpus, a human-label study, or external-generalization evidence."
        ),
        "variant": "c2-rules",
        "cases": [
            {
                "id": "ordered_triplet",
                "fact_family_id": "fixture_invoice_limit_ordered",
                "split": "development",
                "description": "Controlled same-issuer PDF/DOCX triplet; V3 is the only strict metadata maximum.",
                "expected_outcome": "adopt_reopen",
                "expected_winner_document": "fixture_ordered_v3",
                "adoption_cycles": 1,
                # Deliberately upload V2 before V1, ensuring the fixture does
                # not rely on source insertion order to pick the global winner.
                "documents": docs("fixture_ordered_v2", "fixture_ordered_v1", "fixture_ordered_v3"),
                "expected_conflict_document_pairs": [
                    pair("ORDERED_V2_V1", "fixture_ordered_v2", "fixture_ordered_v1"),
                    pair("ORDERED_V3_V1", "fixture_ordered_v3", "fixture_ordered_v1"),
                    pair("ORDERED_V3_V2", "fixture_ordered_v3", "fixture_ordered_v2"),
                ],
                "expected_disputed_fact_count": 1,
                "expected_disputed_fact_anchor_kinds": {"claim_key": 1},
            },
            {
                "id": "cross_issuer_no_proposal",
                "fact_family_id": "fixture_invoice_limit_cross_issuer",
                "split": "development",
                "description": "Controlled conflict with different explicit issuers; no global winner may be proposed.",
                "expected_outcome": "no_proposal",
                "expected_winner_document": "",
                "adoption_cycles": 0,
                "documents": docs("fixture_cross_issuer_a", "fixture_cross_issuer_b"),
                "expected_conflict_document_pairs": [
                    pair("CROSS_ISSUER", "fixture_cross_issuer_a", "fixture_cross_issuer_b"),
                ],
                "expected_disputed_fact_count": 1,
                "expected_disputed_fact_anchor_kinds": {"claim_key": 1},
            },
            {
                "id": "date_version_disagreement_no_proposal",
                "fact_family_id": "fixture_invoice_limit_direction_disagreement",
                "split": "development",
                "description": "Controlled same-issuer conflict whose date and version directions disagree; proposal must abstain.",
                "expected_outcome": "no_proposal",
                "expected_winner_document": "",
                "adoption_cycles": 0,
                "documents": docs("fixture_direction_date_newer", "fixture_direction_version_newer"),
                "expected_conflict_document_pairs": [
                    pair("DIRECTION_DISAGREEMENT", "fixture_direction_date_newer", "fixture_direction_version_newer"),
                ],
                "expected_disputed_fact_count": 1,
                "expected_disputed_fact_anchor_kinds": {"claim_key": 1},
            },
            {
                "id": "metadata_tie_no_proposal",
                "fact_family_id": "fixture_invoice_limit_metadata_tie",
                "split": "development",
                "description": "Controlled same-issuer identical date/version tie; proposal must abstain.",
                "expected_outcome": "no_proposal",
                "expected_winner_document": "",
                "adoption_cycles": 0,
                "documents": docs("fixture_tie_a", "fixture_tie_b"),
                "expected_conflict_document_pairs": [
                    pair("METADATA_TIE", "fixture_tie_a", "fixture_tie_b"),
                ],
                "expected_disputed_fact_count": 1,
                "expected_disputed_fact_anchor_kinds": {"claim_key": 1},
            },
        ],
    }


def write_readme(path: Path) -> None:
    text = """# C4.10 synthetic DocReader fixture

This directory contains nine **fictional**, deterministic PDF/DOCX files. They are intentionally tiny: every case has one controlled financing-limit fact, while issuer/date/version metadata is supplied through the scenario title in exactly the same way the real file runner carries human-verified metadata. The body text is deliberately ASCII English so the generated PDFs require no host-specific CJK font; C3 also supports the explicit English `Issuer` / `Effective Date` / `Version` labels used here.

It is useful for checking the newly added binary-file route:

```text
multipart HTTP file upload → DocReader → Asynq → claims → conflicts → C4.6 proposal
```

It is **not** a real corpus, a substitute for real-document review, a human-label study, or evidence of external generalization.

## Contents

- `docs/`: 5 PDFs and 4 DOCX files; no private content.
- `corpus.json`: C4.10 corpus manifest for the four controlled cases.
- `scenarios/`: direct `run_claims_eval.py` scenarios.
- `winner_lifecycle_matrix.json`: one positive adopt/reopen case and three fail-closed no-proposal cases.
- `fixture_manifest.json`: source hashes and generation metadata.

## Recommended run order

First validate the generated bytes locally:

```bash
make experiment-c410-docreader-fixture-check
```

Then run only the three-file positive DocReader smoke test against a live configured app:

```bash
make experiment-c410-docreader-smoke
```

This creates a fresh temporary KB and executes detection/proposal only; it does not adopt or disable any source chunks.

After that succeeds, run the controlled binary-file lifecycle matrix once:

```bash
make experiment-c410-docreader-lifecycle REPLICATES=1
```

The lifecycle run creates fresh temporary KBs per case. It tests an out-of-order same-issuer triplet (one explicit adopt → reopen cycle), plus cross-issuer, date/version disagreement, and metadata-tie abstention cases.

The fixture can be regenerated deterministically with:

```bash
python3 scripts/experiments/generate_docreader_fixture.py --output-dir testdata/winner_lifecycle_corpus/docreader_fixture --overwrite
```

Do not report its results as a real-corpus metric. Use it only to verify the file/DocReader integration before selecting a small reviewed real-document development set.
"""
    path.write_text(text, encoding="utf-8")


def generate(output: Path, *, overwrite: bool) -> dict[str, Any]:
    if output.exists() and any(output.iterdir()) and not overwrite:
        raise FixtureError(f"输出目录已存在且非空: {output}（需要 --overwrite）")
    output.mkdir(parents=True, exist_ok=True)
    docs_dir = output / "docs"
    scenarios_dir = output / "scenarios"
    # `--overwrite` replaces only the deterministic fixture paths written
    # below. It deliberately never recursively clears the supplied directory.

    source_records: dict[str, dict[str, Any]] = {}
    generated_sources: list[dict[str, Any]] = []
    for spec in fixture_specs():
        source_path = docs_dir / f"{spec['id']}.{spec['format']}"
        write_source_document(source_path, spec["format"], spec["body"])
        digest = sha256_file(source_path)
        source = {
            "id": spec["id"],
            "path": portable_path(source_path),
            "title": spec["title"],
            "ingest_mode": "file",
            "source_sha256": digest,
            "source_document_id": spec["id"],
            "source_relative_path": portable_path(source_path),
            "metadata_evidence_location": "Controlled fixture scenario title",
        }
        source_records[spec["id"]] = source
        generated_sources.append({
            "id": spec["id"],
            "path": portable_path(source_path),
            "format": spec["format"],
            "bytes": source_path.stat().st_size,
            "sha256": digest,
        })

    corpus = build_corpus(output, source_records)
    json_dump(output / "corpus.json", corpus)
    scenario_paths: dict[str, str] = {}
    for case in corpus["cases"]:
        scenario_path = scenarios_dir / f"{case['id']}.json"
        json_dump(scenario_path, scenario_for_case(str(corpus["name"]), case))
        scenario_paths[str(case["id"])] = portable_path(scenario_path)
    matrix = {
        "schema_version": 1,
        "name": "c410_docreader_fixture_matrix",
        "description": (
            "Synthetic binary-file C4.10 integration fixture. All cases are controlled policy labels; "
            "replicates are independent executions and do not claim provider RNG seed control."
        ),
        "variant": "c2-rules",
        "cases": [
            {
                "id": case["id"],
                "scenario": scenario_paths[str(case["id"])],
                "expected_outcome": case["expected_outcome"],
                "expected_winner_document": case["expected_winner_document"],
                "adoption_cycles": case["adoption_cycles"],
                "variant": "c2-rules",
            }
            for case in corpus["cases"]
        ],
    }
    json_dump(output / "winner_lifecycle_matrix.json", matrix)
    fixture_manifest = {
        "schema_version": 1,
        "fixture_version": FIXTURE_VERSION,
        "generator": "scripts/experiments/generate_docreader_fixture.py",
        "source_document_count": len(generated_sources),
        "source_documents": generated_sources,
        "corpus": portable_path(output / "corpus.json"),
        "matrix": portable_path(output / "winner_lifecycle_matrix.json"),
        "note": "Synthetic fixture generation only; no HTTP/model/Asynq/PostgreSQL operation was performed.",
    }
    json_dump(output / "fixture_manifest.json", fixture_manifest)
    write_readme(output / "README.md")
    return fixture_manifest


def verify(output: Path) -> dict[str, Any]:
    manifest_path = output / "fixture_manifest.json"
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise FixtureError(f"找不到 fixture manifest: {manifest_path}") from exc
    except json.JSONDecodeError as exc:
        raise FixtureError(f"fixture manifest 无法解析: {manifest_path}: {exc}") from exc
    if not isinstance(manifest, dict) or manifest.get("fixture_version") != FIXTURE_VERSION:
        raise FixtureError("fixture manifest 版本不匹配；请重新生成 fixture")
    records = manifest.get("source_documents")
    if not isinstance(records, list) or len(records) != len(fixture_specs()):
        raise FixtureError("fixture manifest source_documents 不完整")
    for record in records:
        if not isinstance(record, dict):
            raise FixtureError("fixture manifest 含非法 source record")
        path = Path(str(record.get("path", "")))
        resolved = path if path.is_absolute() else ROOT / path
        if not resolved.is_file():
            raise FixtureError(f"fixture 文件不存在: {resolved}")
        expected = str(record.get("sha256", ""))
        observed = sha256_file(resolved)
        if observed != expected:
            raise FixtureError(f"fixture 文件 SHA-256 不匹配: {resolved.name}")
    corpus = output / "corpus.json"
    matrix = output / "winner_lifecycle_matrix.json"
    if not corpus.is_file() or not matrix.is_file():
        raise FixtureError("fixture corpus/matrix 文件不完整")
    return manifest


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Generate or verify a deterministic synthetic C4.10 PDF/DOCX DocReader fixture.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--output-dir", default=str(DEFAULT_OUTPUT), help="Fixture directory")
    parser.add_argument("--overwrite", action="store_true", help="Regenerate known docs/scenarios under the fixture directory")
    parser.add_argument("--verify", action="store_true", help="Verify existing fixture hashes without writing files")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        output = Path(args.output_dir).expanduser().resolve()
        if args.verify:
            manifest = verify(output)
            print(f"C4.10 DocReader fixture verified: {output}")
            print(f"  source documents: {manifest['source_document_count']}")
            print("  document body/API/model/database: not accessed")
            return 0
        manifest = generate(output, overwrite=bool(args.overwrite))
        print(f"C4.10 DocReader fixture generated: {output}")
        print(f"  source documents: {manifest['source_document_count']} (PDF/DOCX only)")
        print("  cases: 4 (1 adopt_reopen + 3 no_proposal)")
        print("  document body/API/model/database: not accessed")
        return 0
    except FixtureError as exc:
        print(f"[c4.10-docreader-fixture] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
