#!/usr/bin/env python3
"""Offline tests for winner-policy ablations and LLM JSON parsing."""

from __future__ import annotations

import unittest

from llm_judge_client import parse_json_object
from score_winner_fact_families import (
    date_and_version_prediction,
    evaluate_prediction,
    issuer_and_date_prediction,
    issuer_only_prediction,
)


def meta(issuer: str, date: str, version: str) -> dict[str, str]:
    return {"issuer": issuer, "effective_date": date, "version": version}


class WinnerPolicyBaselineTests(unittest.TestCase):
    def test_issuer_mismatch_abstains(self) -> None:
        ids = ["a", "b"]
        metadata = {"a": meta("bank-a", "2024-01-01", "1.0"), "b": meta("bank-b", "2025-01-01", "2.0")}
        self.assertEqual(issuer_only_prediction(ids, metadata), "")
        self.assertEqual(issuer_and_date_prediction(ids, metadata), "")
        self.assertEqual(date_and_version_prediction(ids, metadata), "b")

    def test_issuer_and_date_ignores_version_disagreement(self) -> None:
        ids = ["old", "new"]
        metadata = {
            "old": meta("org", "2024-01-01", "9.0"),
            "new": meta("org", "2025-06-01", "1.0"),
        }
        self.assertEqual(issuer_and_date_prediction(ids, metadata), "new")
        self.assertEqual(date_and_version_prediction(ids, metadata), "")

    def test_unsafe_on_negative_if_any_winner(self) -> None:
        unsafe = evaluate_prediction("doc-a", "no_proposal", "")
        self.assertEqual(unsafe["unsafe"], 1)
        safe = evaluate_prediction("", "no_proposal", "")
        self.assertEqual(safe["unsafe"], 0)

    def test_parse_json_object_strips_fence(self) -> None:
        payload = parse_json_object("```json\n{\"conflict\": false, \"abstain\": true}\n```")
        self.assertFalse(payload["conflict"])
        self.assertTrue(payload["abstain"])


if __name__ == "__main__":
    unittest.main()
