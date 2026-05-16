#!/usr/bin/env python3
"""Runtime smoke test for a running Norify Docker Compose stack.

This script is intentionally conservative. It proves the core API path on a
small campaign and reports unsupported/unsafe steps instead of pretending they
passed.
"""

from __future__ import annotations

import json
import os
import sys
import time
from typing import Any

try:
    import requests
except ImportError as exc:  # pragma: no cover - exercised by missing local deps
    raise SystemExit("requests is required: python3 -m pip install requests") from exc


CAMPAIGN = os.getenv("BASE_URL_CAMPAIGN", "http://localhost:8085").rstrip("/")
DISPATCHER = os.getenv("BASE_URL_DISPATCHER", "http://localhost:8086").rstrip("/")
SENDER = os.getenv("BASE_URL_SENDER", "http://localhost:8087").rstrip("/")
STATUS = os.getenv("BASE_URL_STATUS", "http://localhost:8090").rstrip("/")
TIMEOUT_SECONDS = int(os.getenv("RUNTIME_TIMEOUT_SECONDS", "120"))


def main() -> int:
    print("Norify runtime smoke")
    check_ready("campaign-service", CAMPAIGN)
    check_ready("dispatcher-service", DISPATCHER)
    check_ready("sender-worker", SENDER)
    check_ready("status-service", STATUS)

    campaign = create_campaign()
    campaign_id = campaign["id"]
    print(f"created campaign: {campaign_id}")

    started = post_json(f"{CAMPAIGN}/campaigns/{campaign_id}/start", None)
    print(f"started campaign: {started.get('status')}")

    final = wait_for_campaign(campaign_id)
    expected = final.get("total_messages")
    processed = final.get("sent_count")
    success = final.get("success_count")
    failed = final.get("failed_count")
    if processed != expected:
        raise AssertionError(f"campaign processed {processed}, expected {expected}: {json.dumps(final, ensure_ascii=False)}")
    if failed:
        raise AssertionError(f"runtime smoke uses custom_app and expects no random failures, got failed_count={failed}")
    print(f"campaign finished: processed={processed} success={success} failed={failed}")

    deliveries = get_json(f"{CAMPAIGN}/campaigns/{campaign_id}/deliveries")
    if isinstance(deliveries, list):
        keys = [(row.get("user_id"), row.get("channel_code")) for row in deliveries]
        if len(keys) != len(set(keys)):
            raise AssertionError(f"duplicate delivery rows detected: {deliveries}")
        print(f"deliveries checked: {len(deliveries)} rows, no user/channel duplicates")
    else:
        print("SKIP deliveries duplicate check: endpoint did not return a list")

    print("SKIP forced failure + retry: current main has no safe deterministic runtime failure mode in this script")
    return 0


def check_ready(name: str, base_url: str) -> None:
    response = requests.get(f"{base_url}/health/ready", timeout=5)
    response.raise_for_status()
    print(f"ready: {name} {response.text.strip()}")


def create_campaign() -> dict[str, Any]:
    payload = {
        "name": f"runtime-smoke-{int(time.time())}",
        "template_id": "tpl-reactivation",
        "selected_channels": ["custom_app"],
        "total_recipients": 2,
    }
    result = post_json(f"{CAMPAIGN}/campaigns", payload, expected_status=201)
    if not result.get("id"):
        raise AssertionError(f"campaign create response lacks id: {result}")
    return result


def wait_for_campaign(campaign_id: str) -> dict[str, Any]:
    deadline = time.time() + TIMEOUT_SECONDS
    last: dict[str, Any] = {}
    while time.time() < deadline:
        last = get_json(f"{CAMPAIGN}/campaigns/{campaign_id}")
        if last.get("status") in {"finished", "cancelled"}:
            return last
        time.sleep(1)
    raise TimeoutError(f"campaign {campaign_id} did not finish in {TIMEOUT_SECONDS}s; last={last}")


def get_json(url: str) -> Any:
    response = requests.get(url, timeout=10)
    response.raise_for_status()
    return response.json()


def post_json(url: str, payload: dict[str, Any] | None, expected_status: int = 200) -> dict[str, Any]:
    response = requests.post(url, json=payload, timeout=10)
    if response.status_code != expected_status:
        raise AssertionError(f"POST {url} returned {response.status_code}, expected {expected_status}: {response.text}")
    return response.json()


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1)
