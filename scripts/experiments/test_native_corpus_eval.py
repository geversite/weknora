#!/usr/bin/env python3
"""Offline tests for the unlabeled native-DocReader plan.

No private enterprise text is included. Temporary files are tiny ASCII
placeholders. This script never calls WeKnora, Docker, or a database.
"""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
PLANNER = ROOT / "scripts/experiments/prepare_native_corpus_eval.py"
SUMMARIZER = ROOT / "scripts/experiments/summarize_native_corpus_eval.py"
RUNNER = ROOT / "scripts/experiments/run_claims_eval.py"


def run_ok(*args: str) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        [sys.executable, *args],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=False,
    )
    if result.returncode != 0:
        raise AssertionError(
            f"command failed ({result.returncode}): {args}\n--- stdout ---\n{result.stdout}\n--- stderr ---\n{result.stderr}",
        )
    return result


def write_docx(path: Path, body: str) -> None:
    """Write a tiny valid DOCX so ingest_mode=file keeps a real extension."""
    document_xml = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">'
        f"<w:body><w:p><w:r><w:t>{body}</w:t></w:r></w:p></w:body></w:document>"
    )
    with zipfile.ZipFile(path, "w") as archive:
        archive.writestr("[Content_Types].xml", (
            '<?xml version="1.0" encoding="UTF-8"?>'
            '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
            '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
            '<Default Extension="xml" ContentType="application/xml"/>'
            '<Override PartName="/word/document.xml" '
            'ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>'
            "</Types>"
        ))
        archive.writestr("_rels/.rels", (
            '<?xml version="1.0" encoding="UTF-8"?>'
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
            '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>'
            "</Relationships>"
        ))
        archive.writestr("word/document.xml", document_xml)


class NativeCorpusEvalTests(unittest.TestCase):
    maxDiff = None

    def test_plan_prefers_filename_families_and_file_ingest(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            docs = root / "standard-product"
            docs.mkdir()
            write_docx(docs / "policy_v1.docx", "limit 100")
            write_docx(docs / "policy_v2.docx", "limit 150")
            (docs / "notes.md").write_text("# notes\n" + ("x" * 1200), encoding="utf-8")
            (docs / "tiny.md").write_text("no", encoding="utf-8")
            out = root / "plan"
            run_ok(
                str(PLANNER),
                "--source-dir", str(docs),
                "--output-dir", str(out),
                "--max-files", "2",
                "--min-bytes", "200",
            )
            scenario = json.loads((out / "native_eval_scenario.json").read_text(encoding="utf-8"))
            self.assertEqual(len(scenario["documents"]), 2)
            names = {item["original_filename"] for item in scenario["documents"]}
            self.assertEqual(names, {"policy_v1.docx", "policy_v2.docx"})
            for document in scenario["documents"]:
                self.assertEqual(document["ingest_mode"], "file")
                self.assertTrue(document["upload_file_name"].startswith("doc-"))
                self.assertTrue(document["upload_file_name"].endswith(".docx"))
                self.assertEqual(len(document["source_sha256"]), 64)
            self.assertEqual(scenario["expected_conflict_document_pairs"], [])
            self.assertEqual(scenario["min_claims_per_document"], 0)
            dry_out = root / "dry-run"
            dry = run_ok(
                str(RUNNER),
                "--scenario", str(out / "native_eval_scenario.json"),
                "--dry-run",
                "--output", str(dry_out),
            )
            self.assertIn("native-", dry.stdout)

    def test_summarizer_counts_only(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            run_dir = Path(temp)
            (run_dir / "spans").mkdir()
            (run_dir / "spans" / "doc-0001.json").write_text("{}\n", encoding="utf-8")
            (run_dir / "manifest.json").write_text(json.dumps({
                "status": "completed",
                "scenario_name": "native-standard-product",
                "variant": "c2-batch",
                "knowledge_base_id": "kb-1",
                "knowledge_ids": {"doc-0001": "k-1"},
                "document_ingest": {"doc-0001": {"ingest_mode": "file", "source_bytes": 4096}},
            }), encoding="utf-8")
            (run_dir / "metrics.json").write_text(json.dumps({
                "claim_count_total": 3,
                "claim_counts_by_document": {"doc-0001": 3},
                "conflict_count_total": 1,
                "observed_disputed_fact_count": 1,
                "observed_disputed_fact_winner_count": 0,
                "clusters": {"cluster_count": 1},
                "cascade": {"totals": {"rule_direct_conflict": 1, "llm_batch_call_count": 0}},
            }), encoding="utf-8")
            (run_dir / "dead_letters.json").write_text("[]\n", encoding="utf-8")
            result = run_ok(str(SUMMARIZER), "--run-dir", str(run_dir), "--apply")
            payload = json.loads(result.stdout)
            self.assertEqual(payload["claim_count_total"], 3)
            self.assertEqual(payload["conflict_count_total"], 1)
            self.assertEqual(payload["winner_proposal_count"], 0)
            self.assertIn("not real-document accuracy", payload["note"].lower())
            self.assertTrue((run_dir / "native_summary.md").is_file())
            markdown = (run_dir / "native_summary.md").read_text(encoding="utf-8")
            self.assertNotIn("limit 100", markdown)


if __name__ == "__main__":
    unittest.main()
