#!/usr/bin/env python3
"""Offline self-tests for public benchmark adapters.

No test fixture contains public dataset text. The temporary rows only exercise
release-schema aliases, split safety, manifest shape, and strict replicate
scoring. This script never calls WeKnora, a model provider, Docker, or a DB.
"""

from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
WFD_ADAPTER = ROOT / "scripts/experiments/prepare_public_wikifactdiff_eval.py"
VITAMINC_ADAPTER = ROOT / "scripts/experiments/prepare_public_vitaminc_eval.py"
PAIR_RUNNER = ROOT / "scripts/experiments/run_public_pair_eval.py"


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


class PublicBenchmarkAdapterTests(unittest.TestCase):
    maxDiff = None

    def test_wikifactdiff_published_vocab_and_two_snapshot_proposal_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "wikifactdiff.jsonl"
            with source.open("w", encoding="utf-8") as handle:
                for kind in ("replace", "static"):
                    for index in range(80):
                        objects = (
                            [
                                {"decision": "obsolete", "id": f"Qold{index}", "label": f"obsolete value {index}"},
                                {"decision": "new", "id": f"Qnew{index}", "label": f"new value {index}"},
                            ]
                            if kind == "replace"
                            else [
                                {"decision": "static", "id": f"Qstatic{index}", "label": f"static value {index}"},
                                {"decision": "new", "id": f"Qextra{index}", "label": f"extra changed value {index}"},
                            ]
                        )
                        row = {
                            "subject": {"id": f"Q{kind}{index}", "label": f"Example {kind} {index}"},
                            "relation": {"id": "P100", "label": "has example value"},
                            "update_prompt": f"Example {kind} {index} has value ____.",
                            "is_replace": kind == "replace",
                            "objects": objects,
                        }
                        handle.write(json.dumps(row) + "\n")
            output = root / "plan"
            run_ok(
                str(WFD_ADAPTER),
                "--input", str(source),
                "--output-dir", str(output),
                "--development-per-label", "1",
                "--holdout-per-label", "1",
                "--development-percent", "50",
            )
            source_manifest = json.loads((output / "source_manifest.json").read_text(encoding="utf-8"))
            pair_manifest = json.loads((output / "pair_eval_manifest.json").read_text(encoding="utf-8"))
            proposal_manifest = json.loads((output / "proposal_eval_manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(source_manifest["selected_pair_case_count"], 4)
            self.assertEqual(source_manifest["selected_proposal_case_count"], 2)
            self.assertEqual(source_manifest["selection"]["observed_object_decisions"]["obsolete"], 80)
            self.assertEqual(len(pair_manifest["cases"]), 4)
            self.assertEqual(
                {case["fact_family_id"] for case in pair_manifest["cases"] if case["split"] == "development"}
                & {case["fact_family_id"] for case in pair_manifest["cases"] if case["split"] == "holdout"},
                set(),
            )
            self.assertEqual(len(proposal_manifest["cases"]), 2)
            self.assertTrue(all(case["expected_winner_document"] == "snapshot_new" for case in proposal_manifest["cases"]))
            run_ok(str(PAIR_RUNNER), "--manifest", str(pair_manifest_path := output / "pair_eval_manifest.json"), "--split", "all", "--dry-run")
            run_ok(str(PAIR_RUNNER), "--manifest", str(output / "proposal_eval_manifest.json"), "--split", "all", "--dry-run")
            self.assertTrue(pair_manifest_path.is_file())

    def test_vitaminc_real_archive_auto_members_and_label_mapping(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            archive = root / "vitaminc_real.zip"
            with zipfile.ZipFile(archive, "w") as zipped:
                for split in ("dev", "test"):
                    lines = []
                    for label in ("SUPPORTS", "REFUTES"):
                        for index in range(20):
                            lines.append(json.dumps({
                                "id": f"{split}-{label}-{index}",
                                "claim": f"Example {split} {label} claim {index} is a factual statement.",
                                "evidence": f"Evidence for {split} {label} claim {index} is a factual statement.",
                                # Mirror the two upstream schema variants: the adapter
                                # must prefer gold_label when it is present.
                                **({"gold_label": label} if index == 0 else {"label": label}),
                            }))
                    zipped.writestr(f"{split}.jsonl", "\n".join(lines) + "\n")
            output = root / "plan"
            run_ok(
                str(VITAMINC_ADAPTER),
                "--development-input", str(archive),
                "--holdout-input", str(archive),
                "--output-dir", str(output),
                "--development-per-label", "1",
                "--holdout-per-label", "1",
            )
            manifest = json.loads((output / "pair_eval_manifest.json").read_text(encoding="utf-8"))
            source_manifest = json.loads((output / "source_manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(source_manifest["selected_case_count"], 4)
            self.assertEqual(len(manifest["cases"]), 4)
            self.assertEqual(
                {case["fact_family_id"] for case in manifest["cases"] if case["split"] == "development"}
                & {case["fact_family_id"] for case in manifest["cases"] if case["split"] == "holdout"},
                set(),
            )
            self.assertEqual({case["source_label"] for case in manifest["cases"]}, {"supports", "refutes"})
            self.assertEqual({case["expected_conflict"] for case in manifest["cases"]}, {True, False})
            run_ok(str(PAIR_RUNNER), "--manifest", str(output / "pair_eval_manifest.json"), "--split", "all", "--dry-run")

    def test_vitaminc_real_archive_train_fallback_for_development(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            archive = root / "vitaminc_real.zip"
            with zipfile.ZipFile(archive, "w") as zipped:
                for split in ("train", "test"):
                    lines = []
                    for label in ("SUPPORTS", "REFUTES"):
                        for index in range(20):
                            lines.append(json.dumps({
                                "id": f"{split}-{label}-{index}",
                                "claim": f"Example {split} {label} claim {index} is a factual statement.",
                                "evidence": f"Evidence for {split} {label} claim {index} is a factual statement.",
                                "label": label,
                            }))
                    zipped.writestr(f"vitaminc_real/{split}.jsonl", "\n".join(lines) + "\n")
            output = root / "plan"
            run_ok(
                str(VITAMINC_ADAPTER),
                "--development-input", str(archive),
                "--holdout-input", str(archive),
                "--output-dir", str(output),
                "--development-per-label", "1",
                "--holdout-per-label", "1",
            )
            source_manifest = json.loads((output / "source_manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(
                source_manifest["dataset"]["development"]["archive_member"],
                "vitaminc_real/train.jsonl",
            )
            self.assertEqual(
                source_manifest["dataset"]["holdout"]["archive_member"],
                "vitaminc_real/test.jsonl",
            )
            self.assertEqual(
                source_manifest["selection"]["split_policy"]["kind"],
                "separate_source_members",
            )

    def test_vitaminc_real_archive_test_only_disjoint_partition(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            archive = root / "vitaminc_real.zip"
            lines = []
            for label in ("SUPPORTS", "REFUTES"):
                for index in range(20):
                    lines.append(json.dumps({
                        "id": f"test-{label}-{index}",
                        "claim": f"Example test {label} claim {index} is a factual statement.",
                        "evidence": f"Evidence for test {label} claim {index} is a factual statement.",
                        "label": label,
                    }))
            with zipfile.ZipFile(archive, "w") as zipped:
                zipped.writestr("vitaminc_real/README.txt", "official real test-set package\n")
                zipped.writestr("vitaminc_real/test.jsonl", "\n".join(lines) + "\n")
            output = root / "plan"
            run_ok(
                str(VITAMINC_ADAPTER),
                "--development-input", str(archive),
                "--holdout-input", str(archive),
                "--output-dir", str(output),
                "--development-per-label", "1",
                "--holdout-per-label", "1",
            )
            source_manifest = json.loads((output / "source_manifest.json").read_text(encoding="utf-8"))
            manifest = json.loads((output / "pair_eval_manifest.json").read_text(encoding="utf-8"))
            policy = source_manifest["selection"]["split_policy"]
            self.assertEqual(policy["kind"], "adapter_defined_disjoint_partition_of_shared_source")
            self.assertFalse(policy["native_official_train_dev_test"])
            self.assertEqual(policy["shared_archive_member"], "vitaminc_real/test.jsonl")
            self.assertEqual(source_manifest["dataset"]["development"]["archive_member"], "vitaminc_real/test.jsonl")
            self.assertEqual(source_manifest["dataset"]["holdout"]["archive_member"], "vitaminc_real/test.jsonl")
            self.assertEqual(len(manifest["cases"]), 4)
            development_ids = {case["fact_family_id"] for case in manifest["cases"] if case["split"] == "development"}
            holdout_ids = {case["fact_family_id"] for case in manifest["cases"] if case["split"] == "holdout"}
            self.assertEqual(len(development_ids), 2)
            self.assertEqual(len(holdout_ids), 2)
            self.assertEqual(development_ids & holdout_ids, set())
            self.assertEqual({case["source_label"] for case in manifest["cases"]}, {"supports", "refutes"})
            run_ok(str(PAIR_RUNNER), "--manifest", str(output / "pair_eval_manifest.json"), "--split", "all", "--dry-run")

    def test_vitaminc_drops_same_text_opposite_label_families(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            archive = root / "vitaminc_real.zip"
            lines = []
            for label in ("SUPPORTS", "REFUTES"):
                for index in range(20):
                    lines.append(json.dumps({
                        "id": f"test-{label}-{index}",
                        "claim": f"Example test {label} claim {index} is a factual statement.",
                        "evidence": f"Evidence for test {label} claim {index} is a factual statement.",
                        "label": label,
                    }))
            colliding_claim = "The colliding VitaminC claim text is a factual statement."
            colliding_evidence = "The colliding VitaminC evidence text is a factual statement."
            for label in ("SUPPORTS", "REFUTES"):
                lines.append(json.dumps({
                    "id": f"collide-{label}",
                    "claim": colliding_claim,
                    "evidence": colliding_evidence,
                    "label": label,
                }))
            with zipfile.ZipFile(archive, "w") as zipped:
                zipped.writestr("vitaminc_real/test.jsonl", "\n".join(lines) + "\n")
            output = root / "plan"
            run_ok(
                str(VITAMINC_ADAPTER),
                "--development-input", str(archive),
                "--holdout-input", str(archive),
                "--output-dir", str(output),
                "--development-per-label", "1",
                "--holdout-per-label", "1",
            )
            source_manifest = json.loads((output / "source_manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(source_manifest["selection"]["development_inconsistent_families_dropped"], 1)
            self.assertEqual(source_manifest["selection"]["holdout_inconsistent_families_dropped"], 0)
            catalog = (output / "case_catalog.csv").read_text(encoding="utf-8")
            self.assertNotIn("collide-", catalog)

    def test_utf8_child_environment_overrides_non_utf8_locale(self) -> None:
        script_dir = ROOT / "scripts/experiments"
        sys.path.insert(0, str(script_dir))
        from public_benchmark_common import locale_is_utf8, prepare_output_dir, utf8_child_environment
        from public_benchmark_common import PublicBenchmarkError as CommonError

        self.assertFalse(locale_is_utf8("C"))
        self.assertTrue(locale_is_utf8("C.UTF-8"))
        self.assertTrue(locale_is_utf8("en_US.utf8"))
        ascii_env = utf8_child_environment({"LANG": "C", "LC_ALL": "C", "PATH": "/bin"})
        self.assertEqual(ascii_env["PYTHONUTF8"], "1")
        self.assertEqual(ascii_env["PYTHONIOENCODING"], "utf-8")
        self.assertEqual(ascii_env["LC_ALL"], "C.UTF-8")
        latin_env = utf8_child_environment({"LANG": "en_US", "PATH": "/bin"})
        self.assertEqual(latin_env["LANG"], "C.UTF-8")
        keep = utf8_child_environment({"LANG": "zh_CN.UTF-8", "LC_ALL": "zh_CN.UTF-8", "PATH": "/bin"})
        self.assertEqual(keep["LC_ALL"], "zh_CN.UTF-8")
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "out"
            root.mkdir()
            (root / "keep.txt").write_text("x", encoding="utf-8")
            resumed = prepare_output_dir(root, overwrite=False, resume=True)
            self.assertTrue((resumed / "keep.txt").is_file())
            with self.assertRaises(CommonError):
                prepare_output_dir(root, overwrite=True, resume=True)

    def test_failed_detector_error_is_surfaced_and_completed_dir_is_reusable(self) -> None:
        script_dir = ROOT / "scripts/experiments"
        sys.path.insert(0, str(script_dir))
        spec = importlib.util.spec_from_file_location("public_pair_eval_error_test", PAIR_RUNNER)
        assert spec and spec.loader
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as temp:
            failed = Path(temp) / "failed" / "detector"
            failed.mkdir(parents=True)
            (failed / "manifest.json").write_text(json.dumps({
                "status": "failed",
                "error": "'ascii' codec can't encode characters in position 29-32: ordinal not in range(128)",
                "knowledge_ids": {},
            }, ensure_ascii=False), encoding="utf-8")
            excerpt = module.detector_failure_excerpt(failed, None)
            self.assertIn("ascii", excerpt)
            self.assertFalse(module.detector_dir_reusable(failed))
            case = {
                "id": "vitaminc-supports-demo",
                "fact_family_id": "fam",
                "split": "holdout",
                "case_type": "pair",
                "source_label": "supports",
                "variant": "c2-rules",
                "expected_conflict": False,
                "expected_pair": {"left": "claim", "right": "evidence"},
                "expected_winner_document": "",
                "expected_winner_proposal_source_count": None,
                "scenario": "unused.json",
            }
            row = module.score_case(case, 1, failed, ["python", "run_claims_eval.py"], None, skip_exit_check=True)
            self.assertEqual(row["classification"], "UNEVALUABLE")
            self.assertTrue(any("detector error:" in item and "ascii" in item for item in row["issues"]))
            self.assertEqual(module.unevaluable_error_key(row)[:20], "detector error: 'asc")

            completed = Path(temp) / "completed" / "detector"
            completed.mkdir(parents=True)
            (completed / "manifest.json").write_text(json.dumps({"status": "completed", "knowledge_ids": {"claim": "k1"}}), encoding="utf-8")
            (completed / "metrics.json").write_text(json.dumps({"claim_count_total": 2, "dead_letter_count": 0, "cascade": {}}), encoding="utf-8")
            (completed / "conflict_document_pairs.json").write_text("[]", encoding="utf-8")
            self.assertTrue(module.detector_dir_reusable(completed))
            ok = module.score_case(case, 1, completed, ["python"], None, skip_exit_check=True, reused=True)
            self.assertEqual(ok["classification"], "TN")
            self.assertTrue(ok["detector_reused"])
            self.assertTrue(ok["detector_evaluable"])

    def test_strict_fact_family_and_proposal_scoring_do_not_pool_replicates(self) -> None:
        script_dir = ROOT / "scripts/experiments"
        sys.path.insert(0, str(script_dir))
        spec = importlib.util.spec_from_file_location("public_pair_eval_test", PAIR_RUNNER)
        assert spec and spec.loader
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        rows = [
            {
                "fact_family_id": "positive", "case_id": "positive", "split": "holdout", "case_type": "x",
                "source_label": "refutes", "expected_conflict": True, "classification": "TP",
                "detector_evaluable": True, "observed_conflict": True, "proposal_applicable": True,
                "expected_winner_document": "snapshot_new", "expected_winner_proposal_source_count": 2,
                "proposal_evaluable": True, "proposal_correct": True, "replicate": 1,
            },
            {
                "fact_family_id": "positive", "case_id": "positive", "split": "holdout", "case_type": "x",
                "source_label": "refutes", "expected_conflict": True, "classification": "TP",
                "detector_evaluable": True, "observed_conflict": True, "proposal_applicable": True,
                "expected_winner_document": "snapshot_new", "expected_winner_proposal_source_count": 2,
                "proposal_evaluable": True, "proposal_correct": True, "replicate": 2,
            },
            {
                "fact_family_id": "negative", "case_id": "negative", "split": "holdout", "case_type": "x",
                "source_label": "supports", "expected_conflict": False, "classification": "TN",
                "detector_evaluable": True, "observed_conflict": False, "proposal_applicable": False,
                "expected_winner_document": "", "expected_winner_proposal_source_count": None,
                "proposal_evaluable": False, "proposal_correct": None, "replicate": 1,
            },
            {
                "fact_family_id": "negative", "case_id": "negative", "split": "holdout", "case_type": "x",
                "source_label": "supports", "expected_conflict": False, "classification": "FP",
                "detector_evaluable": True, "observed_conflict": True, "proposal_applicable": False,
                "expected_winner_document": "", "expected_winner_proposal_source_count": None,
                "proposal_evaluable": False, "proposal_correct": None, "replicate": 2,
            },
        ]
        with self.assertRaises(module.PublicPairEvaluationError):
            module.template_kb_source("", {})
        self.assertEqual(module.template_kb_source("", {"WEKNORA_EXPERIMENT_TEMPLATE_KB": "configured"}), "environment")
        self.assertEqual(module.template_kb_source("configured", {}), "argument")

        strict = module.strict_fact_rows(rows, 2)
        by_family = {row["fact_family_id"]: row for row in strict}
        self.assertEqual(by_family["positive"]["classification"], "TP")
        self.assertTrue(by_family["positive"]["proposal_strictly_correct"])
        self.assertEqual(by_family["negative"]["classification"], "FP")
        metrics = module.confusion(strict, strict=True)
        self.assertEqual(metrics["true_positive"], 1)
        self.assertEqual(metrics["false_positive"], 1)
        proposal = module.aggregate_proposal_transfer(rows, strict)
        self.assertEqual(proposal["fact_family_strict_all_replicates"]["correct"], 1)

        cascade_rows = [
            {
                "detector_evaluable": True,
                "cascade": {
                    "run_count": 2,
                    "totals": {
                        "candidate_claim_pairs": 3,
                        "rule_direct_conflict": 2,
                        "llm_single_call_count": 1,
                        "duration_ms": 42,
                    },
                },
            },
            {
                "detector_evaluable": True,
                "cascade": {"candidate_claim_pairs": 4, "duration_ms": 8},
            },
        ]
        cascade = module.aggregate_cascade(cascade_rows)
        self.assertEqual(cascade["totals"]["candidate_claim_pairs"], 7)
        self.assertEqual(cascade["totals"]["rule_direct_conflict"], 2)
        self.assertEqual(cascade["totals"]["duration_ms"], 50)
        self.assertEqual(cascade["nested_totals_observations"], 1)
        self.assertEqual(cascade["flat_totals_compatibility_observations"], 1)

    def test_posthoc_resummarizer_reads_nested_cascade_without_service_access(self) -> None:
        script_dir = ROOT / "scripts/experiments"
        sys.path.insert(0, str(script_dir))
        spec = importlib.util.spec_from_file_location(
            "public_pair_resummarize_test", script_dir / "resummarize_public_pair_eval.py",
        )
        assert spec and spec.loader
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        rows = []
        for case_id, expected, observed in (("positive", True, True), ("negative", False, False)):
            rows.append({
                "case_id": case_id,
                "fact_family_id": case_id,
                "split": "development",
                "case_type": "test",
                "source_label": "test",
                "variant": "c2-rules",
                "replicate": 1,
                "expected_conflict": expected,
                "observed_conflict": observed,
                "classification": "TP" if expected else "TN",
                "correct": True,
                "detector_evaluable": True,
                "expected_winner_document": "",
                "expected_winner_proposal_source_count": None,
                "observed_winner_count": None,
                "observed_winner_documents": [],
                "observed_winner_source_counts": [],
                "proposal_applicable": False,
                "proposal_evaluable": False,
                "proposal_correct": None,
                "proposal_issues": [],
                "dead_letter_count": 0,
                "claim_count_total": 2,
                "conflict_count_total": 1 if expected else 0,
                "cascade": {"run_count": 2, "totals": {"candidate_claim_pairs": 3, "duration_ms": 11}},
            })
        metrics, strict = module.build_summary(
            {"replicates": 1, "task": "test", "variant": "c2-rules", "split": "development", "case_count": 2},
            rows,
        )
        self.assertEqual(len(strict), 2)
        self.assertTrue(metrics["complete"])
        self.assertEqual(metrics["cascade"]["totals"]["candidate_claim_pairs"], 6)
        self.assertEqual(metrics["cascade"]["nested_totals_observations"], 2)


if __name__ == "__main__":
    unittest.main(verbosity=2)
