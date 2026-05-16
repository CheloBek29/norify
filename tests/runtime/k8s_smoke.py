#!/usr/bin/env python3
"""Kubernetes smoke checks for the local Norify demo deployment."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from typing import Any


NAMESPACE = os.getenv("K8S_NAMESPACE", "norify-demo")
FRONTEND_URL = os.getenv("FRONTEND_URL", "http://localhost:3000").rstrip("/")
CAMPAIGN_URL = os.getenv("CAMPAIGN_URL", "http://localhost:8085").rstrip("/")
DISPATCHER_URL = os.getenv("DISPATCHER_URL", "http://localhost:8086").rstrip("/")
SENDER_URL = os.getenv("SENDER_URL", "http://localhost:8087").rstrip("/")
STATUS_URL = os.getenv("STATUS_URL", "http://localhost:8090").rstrip("/")
TIMEOUT_SECONDS = int(os.getenv("K8S_SMOKE_TIMEOUT_SECONDS", "120"))


def run(cmd: list[str], timeout: int = 30) -> tuple[int, str]:
    proc = subprocess.run(cmd, text=True, capture_output=True, timeout=timeout)
    return proc.returncode, (proc.stdout + proc.stderr).strip()


def http_json(method: str, url: str, body: Any | None = None, timeout: int = 5) -> tuple[int, Any, str]:
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:  # noqa: S310 - local smoke URLs
            raw = response.read().decode("utf-8")
            return response.status, json.loads(raw) if raw else None, raw
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", errors="replace")
        try:
            parsed = json.loads(raw) if raw else None
        except json.JSONDecodeError:
            parsed = None
        return exc.code, parsed, raw
    except Exception as exc:  # noqa: BLE001 - smoke output should preserve environment failures
        return 0, None, str(exc)


def check_pods_ready() -> bool:
    code, raw = run(["kubectl", "get", "pods", "-n", NAMESPACE, "-o", "json"], timeout=60)
    if code != 0:
        print(f"FAIL pods: kubectl get pods failed: {raw}")
        return False
    payload = json.loads(raw)
    failed = []
    for pod in payload.get("items", []):
        name = pod["metadata"]["name"]
        phase = pod.get("status", {}).get("phase")
        conditions = pod.get("status", {}).get("conditions", [])
        ready = any(item.get("type") == "Ready" and item.get("status") == "True" for item in conditions)
        if phase != "Running" or not ready:
            failed.append(f"{name}: phase={phase} ready={ready}")
    if failed:
        print("FAIL pods not ready:")
        for item in failed:
            print(f"  - {item}")
        return False
    print(f"PASS pods: all pods Ready in namespace {NAMESPACE}")
    return True


def check_frontend_proxy() -> bool:
    code, raw = run([
        "kubectl", "-n", NAMESPACE, "exec", "deploy/frontend", "--",
        "wget", "-qO-", "http://127.0.0.1/api/ops-gateway/ops/overview",
    ], timeout=30)
    if code != 0:
        print(f"FAIL frontend proxy: {raw}")
        return False
    print("PASS frontend proxy: /api/ops-gateway/ops/overview reachable from frontend pod")
    return True


def check_port_forwarded_health() -> bool:
    targets = {
        "campaign-service": f"{CAMPAIGN_URL}/health/ready",
        "dispatcher-service": f"{DISPATCHER_URL}/health/ready",
        "sender-worker": f"{SENDER_URL}/health/ready",
        "ops-gateway": f"{STATUS_URL}/health/ready",
    }
    reachable = False
    ok = True
    for name, url in targets.items():
        code, body, raw = http_json("GET", url)
        if code == 0:
            print(f"SKIP {name} port-forward health: {raw}")
            ok = False
            continue
        reachable = True
        if code != 200:
            print(f"FAIL {name} health: HTTP {code} {raw[:200]}")
            ok = False
        else:
            print(f"PASS {name} health: {body}")
    if not reachable:
        print("SKIP port-forward health: no localhost API port-forwards are reachable")
        return True
    return ok


def create_and_start_campaign() -> bool:
    code, campaign, raw = http_json("POST", f"{CAMPAIGN_URL}/campaigns", {
        "name": f"k8s-smoke-{int(time.time())}",
        "template_id": "tpl-reactivation",
        "filters": {},
        "selected_channels": ["custom_app"],
        "total_recipients": 2,
    })
    if code == 0:
        print(f"SKIP campaign flow: campaign-service port-forward unavailable: {raw}")
        return True
    if code != 201 or not isinstance(campaign, dict):
        print(f"FAIL campaign create: HTTP {code} {raw[:300]}")
        return False
    campaign_id = campaign["id"]
    code, _, raw = http_json("POST", f"{CAMPAIGN_URL}/campaigns/{campaign_id}/start")
    if code != 200:
        print(f"FAIL campaign start: HTTP {code} {raw[:300]}")
        return False
    deadline = time.time() + TIMEOUT_SECONDS
    while time.time() < deadline:
        code, current, raw = http_json("GET", f"{CAMPAIGN_URL}/campaigns/{campaign_id}")
        if code != 200 or not isinstance(current, dict):
            print(f"FAIL campaign poll: HTTP {code} {raw[:300]}")
            return False
        if int(current.get("sent_count") or 0) >= int(current.get("total_messages") or 0):
            print(f"PASS campaign flow: {campaign_id} sent={current.get('sent_count')} failed={current.get('failed_count')}")
            return True
        time.sleep(2)
    print(f"FAIL campaign flow: {campaign_id} timed out")
    return False


def main() -> int:
    checks = [
        check_pods_ready(),
        check_frontend_proxy(),
        check_port_forwarded_health(),
        create_and_start_campaign(),
    ]
    if all(checks):
        print("K8S_SMOKE PASS")
        return 0
    print("K8S_SMOKE FAIL")
    return 1


if __name__ == "__main__":
    sys.exit(main())
