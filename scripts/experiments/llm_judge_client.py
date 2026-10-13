#!/usr/bin/env python3
"""Minimal OpenAI-compatible chat client for offline comparison judges.

Does not talk to WeKnora ingest/Asynq/PostgreSQL. Credentials are read from
the environment and never written to artifacts.
"""

from __future__ import annotations

import json
import os
import re
import urllib.error
import urllib.request
from typing import Any


class JudgeError(RuntimeError):
    """The judge HTTP call or JSON parse failed."""


def judge_config() -> dict[str, str]:
    base = (
        os.environ.get("WEKNORA_JUDGE_BASE_URL")
        or os.environ.get("OPENAI_BASE_URL")
        or os.environ.get("OPENAI_API_BASE")
        or ""
    ).rstrip("/")
    key = (
        os.environ.get("WEKNORA_JUDGE_API_KEY")
        or os.environ.get("OPENAI_API_KEY")
        or os.environ.get("WEKNORA_API_KEY")
        or ""
    )
    model = os.environ.get("WEKNORA_JUDGE_MODEL") or os.environ.get("OPENAI_MODEL") or ""
    return {"base_url": base, "api_key": key, "model": model}


def chat_completions_url(base_url: str) -> str:
    if not base_url:
        raise JudgeError(
            "set WEKNORA_JUDGE_BASE_URL or OPENAI_BASE_URL to an OpenAI-compatible endpoint"
        )
    if base_url.endswith("/chat/completions"):
        return base_url
    if base_url.endswith("/v1"):
        return base_url + "/chat/completions"
    return base_url + "/v1/chat/completions"


def parse_json_object(text: str) -> dict[str, Any]:
    raw = text.strip()
    if raw.startswith("```"):
        raw = re.sub(r"^```(?:json)?\s*", "", raw)
        raw = re.sub(r"\s*```$", "", raw)
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError:
        match = re.search(r"\{.*\}", raw, re.S)
        if not match:
            raise JudgeError(f"judge reply is not JSON: {raw[:240]}")
        payload = json.loads(match.group(0))
    if not isinstance(payload, dict):
        raise JudgeError("judge JSON root must be an object")
    return payload


def chat_json(messages: list[dict[str, str]], *, timeout_seconds: int = 120) -> dict[str, Any]:
    cfg = judge_config()
    if not cfg["model"]:
        raise JudgeError("set WEKNORA_JUDGE_MODEL or OPENAI_MODEL")
    body = json.dumps(
        {"model": cfg["model"], "messages": messages, "temperature": 0},
        ensure_ascii=True,
    ).encode("utf-8")
    request = urllib.request.Request(
        chat_completions_url(cfg["base_url"]),
        data=body,
        method="POST",
        headers={"Content-Type": "application/json; charset=utf-8"},
    )
    if cfg["api_key"]:
        request.add_header("Authorization", "Bearer " + cfg["api_key"])
    try:
        with urllib.request.urlopen(request, timeout=timeout_seconds) as response:
            payload = json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")[:400]
        raise JudgeError(f"judge HTTP {exc.code}: {detail}") from exc
    except urllib.error.URLError as exc:
        raise JudgeError(f"judge request failed: {exc}") from exc
    try:
        content = payload["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError) as exc:
        raise JudgeError("judge response missing choices[0].message.content") from exc
    return parse_json_object(str(content or ""))
