#!/usr/bin/env python3
"""HTTP-first chaos/load/duplicate smoke runner for Norify.

The script intentionally avoids inventing unsupported APIs. It uses campaign
HTTP endpoints where they exist, optionally controls Docker Compose when
explicitly enabled, and writes both JSON and Markdown reports.
"""

from __future__ import annotations

import concurrent.futures
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from collections import Counter
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


def env_bool(name: str, default: bool = False) -> bool:
    value = os.getenv(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "y", "on"}


def env_int(name: str, default: int) -> int:
    try:
        return int(os.getenv(name, str(default)))
    except ValueError:
        return default


ROOT = Path(__file__).resolve().parents[2]


@dataclass
class Config:
    campaign_url: str = os.getenv("CAMPAIGN_URL", "http://localhost:8085").rstrip("/")
    dispatcher_url: str = os.getenv("DISPATCHER_URL", "http://localhost:8086").rstrip("/")
    sender_url: str = os.getenv("SENDER_URL", "http://localhost:8087").rstrip("/")
    status_url: str = os.getenv("STATUS_URL", "http://localhost:8090").rstrip("/")
    rabbitmq_mgmt_url: str = os.getenv("RABBITMQ_MGMT_URL", "http://localhost:15672").rstrip("/")
    campaigns: int = env_int("CHAOS_CAMPAIGNS", 3)
    users: int = env_int("CHAOS_USERS", 10)
    channels: int = env_int("CHAOS_CHANNELS", 2)
    timeout_seconds: int = env_int("CHAOS_TIMEOUT_SECONDS", 180)
    poll_interval_seconds: int = env_int("CHAOS_POLL_INTERVAL_SECONDS", 2)
    stop_worker: bool = env_bool("CHAOS_STOP_WORKER")
    restart_worker: bool = env_bool("CHAOS_RESTART_WORKER")
    restart_rabbitmq: bool = env_bool("CHAOS_RESTART_RABBITMQ")
    parallel_starts: bool = env_bool("CHAOS_PARALLEL_STARTS", True)
    parallel_retries: bool = env_bool("CHAOS_PARALLEL_RETRIES", True)
    force_failure: bool = env_bool("CHAOS_FORCE_FAILURE")
    skip_unsafe_rabbitmq: bool = env_bool("CHAOS_SKIP_UNSAFE_RABBITMQ", True)
    compose_project_name: str = os.getenv("COMPOSE_PROJECT_NAME", "norify")
    compose_files: str = os.getenv("COMPOSE_FILES", "docker-compose.yml")
    allow_container_control: bool = env_bool("CHAOS_ALLOW_CONTAINER_CONTROL")
    postgres_dsn: str = os.getenv("POSTGRES_DSN", "")
    report_json: str = os.getenv("CHAOS_REPORT_JSON", "tests/chaos/results/latest_chaos_report.json")
    report_md: str = os.getenv("CHAOS_REPORT_MD", "docs/chaos_duplicate_test_report.md")

    @property
    def channel_codes(self) -> list[str]:
        available = ["custom_app", "email", "sms", "telegram", "whatsapp", "vk", "max"]
        return available[: max(1, min(self.channels, len(available)))]


def run(cmd: list[str], timeout: int = 60) -> tuple[int, str]:
    try:
        proc = subprocess.run(cmd, cwd=ROOT, text=True, capture_output=True, timeout=timeout)
        return proc.returncode, (proc.stdout + proc.stderr).strip()
    except Exception as exc:  # noqa: BLE001 - report tool failures as data
        return 1, str(exc)


def compose_command(config: Config, action: str, service: str | None = None) -> tuple[int, str]:
    cmd = ["docker", "compose"]
    for item in config.compose_files.split(","):
        item = item.strip()
        if item:
            cmd.extend(["-f", item])
    cmd.append(action)
    if service:
        cmd.append(service)
    return run(cmd, timeout=90)


def http_json(method: str, url: str, body: Any | None = None, timeout: int = 10) -> tuple[int, Any, str]:
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:  # noqa: S310 - local test URLs
            raw = response.read().decode("utf-8")
            parsed = json.loads(raw) if raw else None
            return response.status, parsed, raw
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", errors="replace")
        try:
            parsed = json.loads(raw) if raw else None
        except json.JSONDecodeError:
            parsed = None
        return exc.code, parsed, raw
    except Exception as exc:  # noqa: BLE001 - report HTTP failures as data
        return 0, None, str(exc)


def git_value(args: list[str]) -> str:
    code, out = run(["git", *args])
    return out.strip() if code == 0 else "unknown"


def scenario(name: str, status: str, **extra: Any) -> dict[str, Any]:
    item = {"name": name, "status": status}
    item.update(extra)
    return item


def readiness(config: Config) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    services = {
        "campaign-service": f"{config.campaign_url}/health/ready",
        "dispatcher-service": f"{config.dispatcher_url}/health/ready",
        "sender-worker": f"{config.sender_url}/health/ready",
        "status-service": f"{config.status_url}/health/ready",
    }
    out: dict[str, Any] = {}
    failures = []
    for service, url in services.items():
        code, body, raw = http_json("GET", url, timeout=4)
        ok = 200 <= code < 300
        out[service] = {"url": url, "status_code": code, "ok": ok, "body": body if body is not None else raw[:300]}
        if not ok:
            failures.append({"service": service, "url": url, "status_code": code, "body": raw[:300]})
    return out, failures


def create_campaign(config: Config, name: str) -> tuple[int, dict[str, Any] | None, str]:
    body = {
        "name": name,
        "template_id": "tpl-reactivation",
        "filters": {},
        "selected_channels": config.channel_codes,
        "total_recipients": config.users,
    }
    code, parsed, raw = http_json("POST", f"{config.campaign_url}/campaigns", body)
    return code, parsed if isinstance(parsed, dict) else None, raw


def start_campaign(config: Config, campaign_id: str) -> tuple[int, Any, str]:
    return http_json("POST", f"{config.campaign_url}/campaigns/{campaign_id}/start")


def retry_failed(config: Config, campaign_id: str) -> tuple[int, Any, str]:
    return http_json("POST", f"{config.campaign_url}/campaigns/{campaign_id}/retry-failed")


def get_campaign(config: Config, campaign_id: str) -> tuple[int, dict[str, Any] | None, str]:
    code, parsed, raw = http_json("GET", f"{config.campaign_url}/campaigns/{campaign_id}")
    return code, parsed if isinstance(parsed, dict) else None, raw


def list_deliveries(config: Config, campaign_id: str) -> tuple[int, list[dict[str, Any]], str]:
    code, parsed, raw = http_json("GET", f"{config.campaign_url}/campaigns/{campaign_id}/deliveries")
    if isinstance(parsed, list):
        return code, [item for item in parsed if isinstance(item, dict)], raw
    return code, [], raw


def list_error_groups(config: Config, campaign_id: str) -> tuple[int, list[dict[str, Any]], str]:
    code, parsed, raw = http_json("GET", f"{config.campaign_url}/campaigns/{campaign_id}/error-groups")
    if isinstance(parsed, list):
        return code, [item for item in parsed if isinstance(item, dict)], raw
    return code, [], raw


def wait_for_campaign(config: Config, campaign_id: str) -> dict[str, Any]:
    started = time.time()
    last: dict[str, Any] | None = None
    while time.time() - started < config.timeout_seconds:
        code, campaign, raw = get_campaign(config, campaign_id)
        if code == 200 and campaign:
            last = campaign
            status = str(campaign.get("status", ""))
            sent = int(campaign.get("sent_count") or 0)
            expected = int(campaign.get("total_messages") or 0)
            if status in {"finished", "cancelled"} or (expected > 0 and sent >= expected):
                return {"timed_out": False, "campaign": campaign, "raw": raw, "elapsed_seconds": round(time.time() - started, 3)}
        time.sleep(config.poll_interval_seconds)
    return {"timed_out": True, "campaign": last, "elapsed_seconds": round(time.time() - started, 3)}


def duplicate_summary(config: Config, campaign_id: str) -> dict[str, Any]:
    code, deliveries, raw = list_deliveries(config, campaign_id)
    if code != 200:
        return {"available": False, "reason": f"deliveries_endpoint_http_{code}", "raw": raw[:500]}
    keys = []
    terminal_success_keys = []
    for item in deliveries:
        user_id = item.get("user_id")
        channel = item.get("channel_code") or item.get("channel")
        if not user_id or not channel:
            continue
        key = f"{campaign_id}:{user_id}:{channel}"
        keys.append(key)
        if item.get("status") in {"sent", "success", "delivered"}:
            terminal_success_keys.append(key)
    row_counts = Counter(keys)
    success_counts = Counter(terminal_success_keys)
    duplicate_rows = [key for key, count in row_counts.items() if count > 1]
    duplicate_success = [key for key, count in success_counts.items() if count > 1]
    expected = config.users * len(config.channel_codes)
    return {
        "available": True,
        "status_code": code,
        "total_delivery_rows": len(deliveries),
        "unique_delivery_keys": len(row_counts),
        "expected_delivery_keys": expected,
        "duplicate_delivery_rows_count": sum(row_counts[key] - 1 for key in duplicate_rows),
        "duplicate_success_count": sum(success_counts[key] - 1 for key in duplicate_success),
        "duplicate_keys": duplicate_success[:20] or duplicate_rows[:20],
        "limitations": "HTTP endpoint is limited to latest 500 rows; large campaigns need DB inspection.",
    }


def run_happy_path(config: Config) -> dict[str, Any]:
    campaigns = []
    failures = []
    started_at = time.time()
    for idx in range(config.campaigns):
        name = f"chaos-happy-{int(started_at)}-{idx + 1}"
        code, campaign, raw = create_campaign(config, name)
        if code != 201 or not campaign:
            failures.append({"step": "create_campaign", "name": name, "status_code": code, "body": raw[:500]})
            continue
        campaign_id = str(campaign["id"])
        start_code, start_body, start_raw = start_campaign(config, campaign_id)
        waited = wait_for_campaign(config, campaign_id)
        dupes = duplicate_summary(config, campaign_id)
        final = waited.get("campaign") or {}
        campaigns.append({
            "campaign_id": campaign_id,
            "create_status": code,
            "start_status": start_code,
            "start_body": start_body if start_body is not None else start_raw[:300],
            "timed_out": waited["timed_out"],
            "duration_seconds": waited["elapsed_seconds"],
            "expected_deliveries": config.users * len(config.channel_codes),
            "sent_count": int(final.get("sent_count") or 0),
            "success_count": int(final.get("success_count") or 0),
            "failed_count": int(final.get("failed_count") or 0),
            "status": final.get("status"),
            "duplicates": dupes,
        })
    duration = max(time.time() - started_at, 0.001)
    completed = sum(item["sent_count"] for item in campaigns)
    expected = sum(item["expected_deliveries"] for item in campaigns)
    status = "pass" if campaigns and not failures and all(not item["timed_out"] for item in campaigns) else "fail"
    return {
        "status": status,
        "campaigns": campaigns,
        "failures": failures,
        "expected_deliveries": expected,
        "completed_deliveries": completed,
        "duration_seconds": round(duration, 3),
        "throughput_deliveries_per_second": round(completed / duration, 4),
    }


def run_parallel_start(config: Config) -> dict[str, Any]:
    code, campaign, raw = create_campaign(config, f"chaos-parallel-start-{int(time.time())}")
    if code != 201 or not campaign:
        return {"status": "fail", "reason": "create_failed", "status_code": code, "body": raw[:500]}
    campaign_id = str(campaign["id"])
    with concurrent.futures.ThreadPoolExecutor(max_workers=5) as executor:
        responses = list(executor.map(lambda _: start_campaign(config, campaign_id), range(5)))
    waited = wait_for_campaign(config, campaign_id)
    dupes = duplicate_summary(config, campaign_id)
    final = waited.get("campaign") or {}
    expected = config.users * len(config.channel_codes)
    actual_rows = dupes.get("total_delivery_rows")
    duplicate_risk = bool(dupes.get("duplicate_success_count") or (isinstance(actual_rows, int) and actual_rows > expected))
    return {
        "status": "fail" if duplicate_risk or waited["timed_out"] else "pass",
        "campaign_id": campaign_id,
        "start_responses": [{"status_code": code, "body": body if body is not None else text[:300]} for code, body, text in responses],
        "final_campaign": final,
        "expected_deliveries": expected,
        "duplicates": dupes,
        "timed_out": waited["timed_out"],
        "note": "Current main uses read-then-update start logic, so this scenario is expected to detect risk under real concurrency if duplicate dispatch occurs.",
    }


def run_forced_failure_retry(config: Config) -> dict[str, Any]:
    if not config.force_failure:
        return {"status": "skipped", "reason": "CHAOS_FORCE_FAILURE=false"}
    return {
        "status": "skipped",
        "reason": "No deterministic force-failure API exists on current main. Channel PATCH can change success_probability, but this script does not mutate channel config unless a safe reset protocol is added.",
    }


def run_worker_fault(config: Config) -> dict[str, Any]:
    if not config.allow_container_control or not (config.stop_worker or config.restart_worker):
        return {"status": "skipped", "reason": "container control disabled or worker fault flags false"}
    actions = []
    code, out = compose_command(config, "stop", "sender-worker")
    actions.append({"command": "docker compose stop sender-worker", "code": code, "output": out[-1000:]})
    time.sleep(2)
    code, campaign, raw = create_campaign(config, f"chaos-worker-fault-{int(time.time())}")
    if code != 201 or not campaign:
        compose_command(config, "start", "sender-worker")
        return {"status": "fail", "reason": "create_failed", "actions": actions, "body": raw[:500]}
    campaign_id = str(campaign["id"])
    start_code, _, start_raw = start_campaign(config, campaign_id)
    time.sleep(3)
    code, out = compose_command(config, "start", "sender-worker")
    actions.append({"command": "docker compose start sender-worker", "code": code, "output": out[-1000:]})
    waited = wait_for_campaign(config, campaign_id)
    dupes = duplicate_summary(config, campaign_id)
    status = "pass" if start_code in {200, 202} and not waited["timed_out"] and dupes.get("duplicate_success_count", 0) == 0 else "fail"
    return {"status": status, "campaign_id": campaign_id, "actions": actions, "start_status": start_code, "start_body": start_raw[:300], "wait": waited, "duplicates": dupes}


def run_rabbitmq_restart(config: Config) -> dict[str, Any]:
    if not config.allow_container_control or not config.restart_rabbitmq:
        return {"status": "skipped", "reason": "container control disabled or CHAOS_RESTART_RABBITMQ=false"}
    if config.skip_unsafe_rabbitmq:
        return {"status": "skipped", "reason": "CHAOS_SKIP_UNSAFE_RABBITMQ=true"}
    actions = []
    code, out = compose_command(config, "restart", "rabbitmq")
    actions.append({"command": "docker compose restart rabbitmq", "code": code, "output": out[-1000:]})
    time.sleep(20)
    readiness_after, failures = readiness(config)
    code, campaign, raw = create_campaign(config, f"chaos-rabbitmq-after-restart-{int(time.time())}")
    if code != 201 or not campaign:
        return {"status": "fail", "actions": actions, "readiness_after": readiness_after, "readiness_failures": failures, "reason": "create_failed", "body": raw[:500]}
    campaign_id = str(campaign["id"])
    start_code, _, start_raw = start_campaign(config, campaign_id)
    waited = wait_for_campaign(config, campaign_id)
    dupes = duplicate_summary(config, campaign_id)
    status = "pass" if start_code in {200, 202} and not waited["timed_out"] and dupes.get("duplicate_success_count", 0) == 0 else "fail"
    return {
        "status": status,
        "mode": "idle_restart_then_new_campaign",
        "campaign_id": campaign_id,
        "actions": actions,
        "readiness_after": readiness_after,
        "readiness_failures": failures,
        "start_status": start_code,
        "start_body": start_raw[:300],
        "wait": waited,
        "duplicates": dupes,
    }


def markdown(report: dict[str, Any]) -> str:
    rows = []
    for item in report["scenarios"]:
        rows.append(f"| {item['name']} | {item['status']} | {item.get('reason', item.get('note', ''))} |")
    failed_calls = report.get("failed_http_calls", [])
    duplicate_lines = []
    for item in report["scenarios"]:
        data = item.get("result")
        if isinstance(data, dict) and "duplicates" in data:
            duplicate_lines.append(f"- `{item['name']}`: `{data['duplicates']}`")
        if isinstance(data, dict) and "campaigns" in data:
            for campaign in data["campaigns"]:
                duplicate_lines.append(f"- `{item['name']}/{campaign.get('campaign_id')}`: `{campaign.get('duplicates')}`")
    return "\n".join([
        "# Chaos Duplicate Test Report",
        "",
        "## Executive Summary",
        "",
        f"- Branch: `{report['git']['branch']}`",
        f"- Commit: `{report['git']['commit']}`",
        f"- Overall result: `{report['overall_status']}`",
        f"- Runtime timestamp: `{report['timestamp']}`",
        f"- Container actions performed: `{len(report['container_actions'])}`",
        "",
        "## Commands Used",
        "",
        "```bash",
        "make chaos-test-safe",
        "make chaos-test-worker-fault",
        "make chaos-test-rabbitmq-after-restart",
        "```",
        "",
        "## Scenario Results",
        "",
        "| Scenario | Status | Notes |",
        "|---|---:|---|",
        *rows,
        "",
        "## Duplicate Check Result",
        "",
        *(duplicate_lines or ["- Duplicate check did not run or no delivery endpoint was available."]),
        "",
        "## Failed HTTP Calls",
        "",
        *(["- None recorded."] if not failed_calls else [f"- `{call}`" for call in failed_calls[:20]]),
        "",
        "## Fault Injection Result",
        "",
        "- Worker stop/start runs only when `CHAOS_ALLOW_CONTAINER_CONTROL=true` and worker flags are enabled.",
        "- RabbitMQ restart runs only when `CHAOS_ALLOW_CONTAINER_CONTROL=true`, `CHAOS_RESTART_RABBITMQ=true`, and `CHAOS_SKIP_UNSAFE_RABBITMQ=false`.",
        "- This report does not claim active-load RabbitMQ safety unless the RabbitMQ scenario says it ran under active load.",
        "",
        "## Safe Demo Claims",
        "",
        "- Safe claims require a passing JSON report from the exact branch and stack under test.",
        "- Duplicate count of zero means no duplicate was detected by the available HTTP delivery listing; it is not a DB-level proof for campaigns larger than the endpoint limit.",
        "",
        "## Unsafe Claims",
        "",
        "- Do not claim 50k-user behavior from this script unless a 50k run was actually executed.",
        "- Do not claim exactly-once provider delivery; current main still has known idempotency and retry risks.",
        "- Do not claim active-load RabbitMQ recovery unless that scenario was explicitly enabled and passed.",
        "",
        "## Limitations",
        "",
        *[f"- {item}" for item in report["limitations"]],
        "",
    ])


def main() -> int:
    config = Config()
    report: dict[str, Any] = {
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "git": {"branch": git_value(["branch", "--show-current"]), "commit": git_value(["rev-parse", "--short", "HEAD"])},
        "config": asdict(config),
        "services_readiness": {},
        "scenarios": [],
        "failed_http_calls": [],
        "container_actions": [],
        "limitations": [
            "HTTP deliveries endpoint returns at most 500 rows, so large duplicate checks need DB inspection.",
            "Current main has no deterministic force-failure API; forced retry scenario skips unless support is added.",
            "Optional DB duplicate inspection is not implemented without an approved dependency.",
            "Campaign start duplicate detection is black-box and depends on live queue/worker behavior.",
        ],
    }

    ready, failures = readiness(config)
    report["services_readiness"] = ready
    if failures:
        report["scenarios"].append(scenario("readiness", "fail", failures=failures, reason="critical readiness endpoint unavailable"))
        report["overall_status"] = "fail"
    else:
        report["scenarios"].append(scenario("readiness", "pass"))
        happy = run_happy_path(config)
        report["scenarios"].append(scenario("n_campaign_happy_path", happy["status"], result=happy))
        if config.parallel_starts:
            parallel = run_parallel_start(config)
            report["scenarios"].append(scenario("parallel_start_duplicate_test", parallel["status"], result=parallel))
        else:
            report["scenarios"].append(scenario("parallel_start_duplicate_test", "skipped", reason="CHAOS_PARALLEL_STARTS=false"))
        retry = run_forced_failure_retry(config)
        report["scenarios"].append(scenario("forced_failure_parallel_retry", retry["status"], result=retry, reason=retry.get("reason", "")))
        worker = run_worker_fault(config)
        report["scenarios"].append(scenario("worker_stop_start_fault", worker["status"], result=worker, reason=worker.get("reason", "")))
        rabbit = run_rabbitmq_restart(config)
        report["scenarios"].append(scenario("rabbitmq_restart_then_new_campaign", rabbit["status"], result=rabbit, reason=rabbit.get("reason", "")))
        statuses = [item["status"] for item in report["scenarios"]]
        report["overall_status"] = "fail" if "fail" in statuses else "pass"

    json_path = ROOT / config.report_json
    md_path = ROOT / config.report_md
    json_path.parent.mkdir(parents=True, exist_ok=True)
    md_path.parent.mkdir(parents=True, exist_ok=True)
    json_path.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    md_path.write_text(markdown(report), encoding="utf-8")
    print(json.dumps({
        "overall_status": report["overall_status"],
        "json_report": str(json_path),
        "markdown_report": str(md_path),
        "scenarios": [{"name": item["name"], "status": item["status"]} for item in report["scenarios"]],
    }, indent=2))
    return 0 if report["overall_status"] == "pass" else 1


if __name__ == "__main__":
    sys.exit(main())
