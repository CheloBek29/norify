from __future__ import annotations

import asyncio
import json
import logging
import os
import socket
import uuid
import time
import ssl
import inspect
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from contextlib import asynccontextmanager
from typing import Any, Optional

from fastapi import FastAPI, HTTPException, WebSocket, WebSocketDisconnect
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel, Field

try:
    import aio_pika
except ImportError:  # Local unit tests can run before optional RabbitMQ dependency is installed.
    aio_pika = None

try:
    import psycopg
except ImportError:
    psycopg = None

try:
    import redis.asyncio as redis_async
except ImportError:
    redis_async = None


class ProgressSnapshot(BaseModel):
    type: str = "campaign.progress"
    campaign_id: str
    status: str = "running"
    total_messages: int = 0
    processed: int = 0
    success: int = 0
    failed: int = 0
    cancelled: int = 0
    p95_dispatch_ms: int = 0
    progress_percent: float = 0
    updated_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))


class SendMessageJob(BaseModel):
    campaign_id: str
    user_id: str
    channel_code: str
    message_body: str
    attempt: int = 1
    idempotency_key: str


class MessageSendResult(BaseModel):
    campaign_id: str
    user_id: str
    channel_code: str
    status: str
    error_code: Optional[str] = None
    error_message: Optional[str] = None
    attempt: int = 1
    retryable: bool = False
    idempotency_key: str
    finished_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))


class WorkerChannelConfig(BaseModel):
    code: str
    enabled: bool = True
    success_probability: float = 0.92
    min_delay_seconds: int = 2
    max_delay_seconds: int = 300
    max_parallelism: int = 100
    retry_limit: int = 3
    source: str = "default"


class WorkerBoundsRequest(BaseModel):
    min_replicas: int = Field(ge=1)
    max_replicas: int = Field(ge=1)


snapshots: dict[str, ProgressSnapshot] = {}
connections: dict[str, set[WebSocket]] = {}
ops_connections: set[WebSocket] = set()
campaign_totals: dict[str, int] = {}
message_states: dict[str, dict[str, str]] = {}
processed_result_keys: set[str] = set()
queued_jobs: dict[str, SendMessageJob] = {}
SERVICE_NAME = "ops-gateway"
logger = logging.getLogger(SERVICE_NAME)
CAMPAIGN_SERVICE_URL = os.getenv("CAMPAIGN_SERVICE_URL", "http://campaign-service:8080")
CHANNEL_SERVICE_URL = os.getenv("CHANNEL_SERVICE_URL", "http://channel-service:8080")
TEMPLATE_SERVICE_URL = os.getenv("TEMPLATE_SERVICE_URL", "http://template-service:8080")
SENDER_SERVICE_URL = os.getenv("SENDER_SERVICE_URL", "http://sender-worker:8080")
POSTGRES_DSN = os.getenv("POSTGRES_DSN", "")
REDIS_URL = os.getenv("REDIS_URL", "")
worker_replica_state = {"desired_replicas": int(os.getenv("SENDER_WORKER_REPLICAS", "1"))}
worker_bounds_state = {
    "min_replicas": int(os.getenv("WORKER_CONTROL_MIN_REPLICAS", os.getenv("SENDER_WORKER_REPLICAS", "1"))),
    "max_replicas": int(os.getenv("WORKER_CONTROL_MAX_REPLICAS", "20")),
}
CAMPAIGN_SNAPSHOT_TTL_SECONDS = int(os.getenv("CAMPAIGN_SNAPSHOT_TTL_SECONDS", "3600"))
WORKER_POLICY_TTL_SECONDS = int(os.getenv("WORKER_POLICY_TTL_SECONDS", "86400"))
WORKER_POLICY_KEY = "worker-scaling-policy:sender-worker"
HEALTH_TARGETS = [
    ("auth-service", os.getenv("AUTH_SERVICE_URL", "http://auth-service:8080")),
    ("user-service", os.getenv("USER_SERVICE_URL", "http://user-service:8080")),
    ("template-service", TEMPLATE_SERVICE_URL),
    ("channel-service", CHANNEL_SERVICE_URL),
    ("campaign-service", CAMPAIGN_SERVICE_URL),
    ("dispatcher-service", os.getenv("DISPATCHER_SERVICE_URL", "http://dispatcher-service:8080")),
    ("sender-worker", SENDER_SERVICE_URL),
    ("notification-error-service", os.getenv("NOTIFICATION_ERROR_SERVICE_URL", "http://notification-error-service:8080")),
    (SERVICE_NAME, os.getenv("OPS_GATEWAY_URL", os.getenv("STATUS_SERVICE_URL", "http://ops-gateway:8080"))),
]


@asynccontextmanager
async def lifespan(_: FastAPI):
    rabbitmq_url = os.getenv("RABBITMQ_URL")
    if rabbitmq_url and aio_pika is not None:
        asyncio.create_task(consume_rabbitmq(rabbitmq_url))
    yield


app = FastAPI(title="Norify Ops Gateway", lifespan=lifespan)
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["GET", "POST", "OPTIONS"],
    allow_headers=["*"],
)


@app.get("/health/live")
async def live() -> dict[str, str]:
    return {"status": "live", "service": SERVICE_NAME}


@app.get("/health/ready")
async def ready() -> dict[str, str]:
    return {"status": "ready", "service": SERVICE_NAME}


@app.get("/ops/overview")
async def ops_overview() -> dict[str, Any]:
    services = await asyncio.gather(*(check_service_health(name, url) for name, url in HEALTH_TARGETS))
    worker = await build_worker_status()
    campaign_snapshots = await list_campaign_snapshots()
    ready_count = sum(1 for service in services if service["status"] == "ready")
    active_campaigns = sum(1 for snapshot in campaign_snapshots if snapshot.status in {"running", "retrying"})
    return {
        "service": SERVICE_NAME,
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "summary": {
            "services_ready": ready_count,
            "services_total": len(services),
            "active_campaigns": active_campaigns,
            "tracked_campaigns": len(campaign_snapshots),
            "processed_messages": sum(snapshot.processed for snapshot in campaign_snapshots),
            "failed_messages": sum(snapshot.failed for snapshot in campaign_snapshots),
            "websocket_clients": sum(len(items) for items in connections.values()) + len(ops_connections),
        },
        "services": services,
        "worker": worker,
        "campaigns": [snapshot.model_dump(mode="json") for snapshot in campaign_snapshots],
    }


@app.get("/workers/status")
async def workers_status() -> dict[str, Any]:
    return await build_worker_status()


@app.post("/workers/bounds")
async def update_worker_bounds(request: WorkerBoundsRequest) -> dict[str, Any]:
    if request.min_replicas > request.max_replicas:
        raise HTTPException(status_code=400, detail="min_replicas_must_not_exceed_max_replicas")
    if not worker_control_enabled():
        raise HTTPException(status_code=503, detail="worker_control_disabled")
    policy = await maybe_await(apply_worker_bounds(request.min_replicas, request.max_replicas))
    return await build_worker_status(policy)


@app.get("/metrics")
async def metrics() -> str:
    campaign_clients = sum(len(items) for items in connections.values())
    return f"websocket_connected_clients {campaign_clients + len(ops_connections)}\n"


@app.get("/campaigns/{campaign_id}/snapshot", response_model=ProgressSnapshot)
async def snapshot(campaign_id: str) -> ProgressSnapshot:
    return await read_campaign_snapshot(campaign_id)


@app.post("/events/status", response_model=ProgressSnapshot)
async def ingest_status(event: ProgressSnapshot) -> ProgressSnapshot:
    await remember_campaign_snapshot(event)
    await broadcast(event.campaign_id, event.model_dump(mode="json"))
    return event


@app.websocket("/ws/campaigns/{campaign_id}")
async def campaign_ws(websocket: WebSocket, campaign_id: str) -> None:
    await websocket.accept()
    connections.setdefault(campaign_id, set()).add(websocket)
    try:
        await websocket.send_json((await read_campaign_snapshot(campaign_id)).model_dump(mode="json"))
        while True:
            await websocket.receive_text()
    except WebSocketDisconnect:
        pass
    finally:
        connections.get(campaign_id, set()).discard(websocket)


@app.websocket("/ws/ops")
async def operations_ws(websocket: WebSocket) -> None:
    await websocket.accept()
    ops_connections.add(websocket)
    try:
        await websocket.send_json({"type": "ops.ready"})
        while True:
            raw = await websocket.receive_text()
            try:
                command = json.loads(raw)
            except json.JSONDecodeError:
                await websocket.send_json({"type": "command.error", "request_id": "", "error": "invalid_json"})
                continue
            result = await handle_ops_command(command)
            await broadcast_ops(result)
    except WebSocketDisconnect:
        pass
    finally:
        ops_connections.discard(websocket)


async def broadcast(campaign_id: str, payload: dict[str, Any]) -> None:
    stale: list[WebSocket] = []
    for websocket in connections.get(campaign_id, set()):
        try:
            await websocket.send_json(payload)
        except (RuntimeError, WebSocketDisconnect):
            stale.append(websocket)
    for websocket in stale:
        connections.get(campaign_id, set()).discard(websocket)


async def broadcast_ops(payload: dict[str, Any]) -> None:
    stale: list[WebSocket] = []
    for websocket in ops_connections:
        try:
            await websocket.send_json(payload)
        except (RuntimeError, WebSocketDisconnect):
            stale.append(websocket)
    for websocket in stale:
        ops_connections.discard(websocket)


async def handle_ops_command(command: dict[str, Any]) -> dict[str, Any]:
    request_id = str(command.get("id") or command.get("request_id") or "")
    command_type = str(command.get("type") or "")
    payload = command.get("payload") if isinstance(command.get("payload"), dict) else {}
    try:
        if command_type == "campaign.create":
            campaign = await proxy_json("POST", f"{CAMPAIGN_SERVICE_URL}/campaigns", {
                "name": payload.get("name"),
                "template_id": payload.get("template_id"),
                "filters": payload.get("filters", {}),
                "selected_channels": payload.get("selected_channels", []),
                "total_recipients": payload.get("total_recipients", 0),
                "specific_recipients": payload.get("specific_recipients", []),
            })
            campaign_id = str(campaign["id"])
            started = await proxy_json("POST", f"{CAMPAIGN_SERVICE_URL}/campaigns/{campaign_id}/start")
            return {"type": "campaign.upsert", "request_id": request_id, "campaign": started}

        if command_type == "campaign.action":
            campaign_id = str(payload.get("campaign_id") or "")
            action = str(payload.get("action") or "")
            path = campaign_action_path(action)
            body = None
            if action == "switch_channel":
                body = {"from": payload.get("from_channel") or "telegram", "to": payload.get("to_channel") or "email"}
            campaign = await proxy_json("POST", f"{CAMPAIGN_SERVICE_URL}/campaigns/{campaign_id}/{path}", body)
            return {"type": "campaign.upsert", "request_id": request_id, "campaign": campaign}

        if command_type == "error_group.action":
            campaign_id = str(payload.get("campaign_id") or "")
            group_id = str(payload.get("group_id") or "")
            action = str(payload.get("action") or "")
            path = error_group_action_path(action)
            body = {"to_channel": payload.get("to_channel")} if action == "switch_channel" else None
            result = await proxy_json("POST", f"{CAMPAIGN_SERVICE_URL}/campaigns/{campaign_id}/error-groups/{group_id}/{path}", body)
            return {
                "type": "error_group.resolved",
                "request_id": request_id,
                "group_id": group_id,
                "queued": result.get("queued", 0),
                "campaign": result.get("campaign", {}),
            }

        if command_type == "channel.update":
            code = str(payload.get("code") or "")
            raw = payload.get("channel") if isinstance(payload.get("channel"), dict) else {}
            normalized = {
                "code": raw.get("code") or raw.get("Code") or code,
                "name": raw.get("name") or raw.get("Name") or code,
                "enabled": bool(raw.get("enabled") if "enabled" in raw else raw.get("Enabled", True)),
                "success_probability": float(raw.get("success_probability") or raw.get("successProbability") or raw.get("SuccessProbability") or 0.92),
                "min_delay_seconds": int(raw.get("min_delay_seconds") or raw.get("minDelaySeconds") or raw.get("MinDelaySeconds") or 2),
                "max_delay_seconds": int(raw.get("max_delay_seconds") or raw.get("maxDelaySeconds") or raw.get("MaxDelaySeconds") or 300),
                "max_parallelism": int(raw.get("max_parallelism") or raw.get("maxParallelism") or raw.get("MaxParallelism") or 100),
                "retry_limit": int(raw.get("retry_limit") or raw.get("retryLimit") or raw.get("RetryLimit") or 3),
            }
            updated = await proxy_json("PATCH", f"{CHANNEL_SERVICE_URL}/channels/{code}", normalized)
            return {"type": "channel.upsert", "request_id": request_id, "channel": updated}

        if command_type == "template.save":
            template = payload.get("template") if isinstance(payload.get("template"), dict) else {}
            template_id = str(template.get("id") or template.get("ID") or "")
            try:
                saved = await proxy_json("PUT", f"{TEMPLATE_SERVICE_URL}/templates/{template_id}", template)
            except RuntimeError:
                saved = await proxy_json("POST", f"{TEMPLATE_SERVICE_URL}/templates", template)
            return {"type": "template.upsert", "request_id": request_id, "template": saved}

        if command_type == "manager.add":
            return {"type": "manager.upsert", "request_id": request_id, "manager": payload}

        if command_type == "health.check":
            services = await asyncio.gather(*(check_service_health(name, url) for name, url in HEALTH_TARGETS))
            return {"type": "health.snapshot", "request_id": request_id, "services": services}

        if command_type == "worker.bounds":
            min_replicas = int(payload.get("min_replicas") or payload.get("minReplicas") or 0)
            max_replicas = int(payload.get("max_replicas") or payload.get("maxReplicas") or 0)
            if min_replicas < 1 or max_replicas < 1 or min_replicas > max_replicas:
                raise RuntimeError("invalid_worker_bounds")
            if not worker_control_enabled():
                raise RuntimeError("worker_control_disabled")
            policy = await maybe_await(apply_worker_bounds(min_replicas, max_replicas))
            status = await build_worker_status(policy)
            return {"type": "worker.status", "request_id": request_id, **status}

        raise RuntimeError(f"unknown_command:{command_type}")
    except Exception as exc:
        logger.warning("websocket operation failed: %s", exc)
        return {"type": "command.error", "request_id": request_id, "error": str(exc)}


def campaign_action_path(action: str) -> str:
    mapping = {
        "start": "start",
        "stop": "stop",
        "retry": "retry-failed",
        "switch_channel": "switch-channel",
        "cancel_campaign": "cancel",
        "archive": "archive",
    }
    if action not in mapping:
        raise RuntimeError(f"unknown_campaign_action:{action}")
    return mapping[action]


def error_group_action_path(action: str) -> str:
    mapping = {
        "retry": "retry",
        "switch_channel": "switch-channel",
        "cancel_group": "cancel",
    }
    if action not in mapping:
        raise RuntimeError(f"unknown_error_group_action:{action}")
    return mapping[action]


async def proxy_json(method: str, url: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
    return await asyncio.to_thread(proxy_json_sync, method, url, payload)


async def check_service_health(name: str, base_url: str) -> dict[str, Any]:
    url = f"{base_url}/health/ready"
    started_at = time.perf_counter()
    checked_at = datetime.now(timezone.utc).isoformat()
    try:
        payload = await proxy_json("GET", url)
        latency_ms = round((time.perf_counter() - started_at) * 1000)
        detail = str(payload.get("status") or payload.get("ready") or "ready").lower()
        status = "ready" if detail in {"ready", "live", "true"} else "down"
        return {"id": name, "name": name, "url": url, "status": status, "latency_ms": latency_ms, "checked_at": checked_at, "detail": detail}
    except Exception as exc:
        latency_ms = round((time.perf_counter() - started_at) * 1000)
        return {"id": name, "name": name, "url": url, "status": "down", "latency_ms": latency_ms, "checked_at": checked_at, "detail": str(exc)}


async def build_worker_status(scaling_policy: dict[str, int] | None = None) -> dict[str, Any]:
    stats = await read_sender_worker_stats()
    replicas = scaling_policy or await maybe_await(read_worker_scaling_policy())
    container_workers = int(stats.get("active_workers", 0))
    active_replicas = int(replicas.get("replicas", replicas.get("desired_replicas", 1)))
    return {
        "active_workers": container_workers * active_replicas,
        "container_workers": container_workers,
        "min_workers": int(stats.get("min_workers", 0)),
        "max_workers": int(stats.get("max_workers", 0)),
        "queue_depth": int(stats.get("queue_depth", -1)),
        "replicas": active_replicas,
        "desired_replicas": int(replicas.get("desired_replicas", active_replicas)),
        "min_replicas": int(replicas.get("min_replicas", 1)),
        "max_replicas": int(replicas.get("max_replicas", worker_max_replicas())),
        "control_mode": worker_control_mode(),
        "control_enabled": worker_control_enabled(),
        "autoscaler": worker_autoscaler_name(),
    }


async def read_sender_worker_stats() -> dict[str, Any]:
    try:
        return await proxy_json("GET", f"{SENDER_SERVICE_URL}/worker/stats")
    except Exception as exc:
        logger.warning("sender worker stats unavailable: %s", exc)
        return {"active_workers": 0, "min_workers": 0, "max_workers": 0, "queue_depth": -1}


def worker_control_mode() -> str:
    return os.getenv("WORKER_CONTROL_MODE", "disabled").lower()


def worker_control_enabled() -> bool:
    return worker_control_mode() in {"kubernetes", "memory"}


def worker_autoscaler_name() -> str:
    if worker_control_mode() == "kubernetes":
        return "kubernetes-hpa"
    if worker_control_mode() == "memory":
        return "ops-gateway-memory"
    return "read-only"


def worker_max_replicas() -> int:
    try:
        value = int(os.getenv("WORKER_CONTROL_MAX_REPLICAS", "20"))
    except ValueError:
        return 20
    return max(1, value)


async def read_worker_scaling_policy() -> dict[str, int]:
    if worker_control_mode() == "kubernetes":
        return read_kubernetes_worker_scaling_policy()
    if worker_control_mode() == "disabled":
        return read_dns_worker_replicas()
    cached = await read_worker_scaling_policy_from_redis()
    if cached:
        worker_replica_state["desired_replicas"] = cached["desired_replicas"]
        worker_bounds_state["min_replicas"] = cached["min_replicas"]
        worker_bounds_state["max_replicas"] = cached["max_replicas"]
        return cached
    desired = max(1, int(worker_replica_state.get("desired_replicas", 1)))
    return {
        "replicas": desired,
        "desired_replicas": desired,
        "min_replicas": int(worker_bounds_state["min_replicas"]),
        "max_replicas": int(worker_bounds_state["max_replicas"]),
    }


def read_dns_worker_replicas() -> dict[str, int]:
    try:
        host = urllib.parse.urlparse(SENDER_SERVICE_URL).hostname or "sender-worker"
        addresses = {item[4][0] for item in socket.getaddrinfo(host, None, family=socket.AF_INET)}
        replicas = max(1, len(addresses))
    except Exception:
        replicas = max(1, int(os.getenv("SENDER_WORKER_REPLICAS", "1")))
    return {
        "replicas": replicas,
        "desired_replicas": replicas,
        "min_replicas": replicas,
        "max_replicas": replicas,
    }


async def apply_worker_bounds(min_replicas: int, max_replicas: int) -> dict[str, int]:
    if worker_control_mode() == "kubernetes":
        return apply_kubernetes_worker_bounds(min_replicas, max_replicas)
    if worker_control_mode() != "memory":
        raise RuntimeError("worker_control_disabled")
    worker_bounds_state["min_replicas"] = min_replicas
    worker_bounds_state["max_replicas"] = max_replicas
    desired = max(min_replicas, min(int(worker_replica_state.get("desired_replicas", min_replicas)), max_replicas))
    worker_replica_state["desired_replicas"] = desired
    policy = {
        "replicas": desired,
        "desired_replicas": desired,
        "min_replicas": min_replicas,
        "max_replicas": max_replicas,
    }
    await write_worker_scaling_policy_to_redis(policy)
    return policy


def read_kubernetes_worker_scaling_policy() -> dict[str, int]:
    payload = kubernetes_hpa_request("GET")
    spec = payload.get("spec", {})
    status = payload.get("status", {})
    min_replicas = int(spec.get("minReplicas") or 1)
    max_replicas = int(spec.get("maxReplicas") or worker_max_replicas())
    desired = int(status.get("desiredReplicas") or min_replicas)
    active = int(status.get("currentReplicas") or desired)
    return {"replicas": active, "desired_replicas": desired, "min_replicas": min_replicas, "max_replicas": max_replicas}


def apply_kubernetes_worker_bounds(min_replicas: int, max_replicas: int) -> dict[str, int]:
    payload = kubernetes_hpa_request("PATCH", {"spec": {"minReplicas": min_replicas, "maxReplicas": max_replicas}})
    spec = payload.get("spec", {})
    status = payload.get("status", {})
    current_min = int(spec.get("minReplicas") or min_replicas)
    current_max = int(spec.get("maxReplicas") or max_replicas)
    desired = int(status.get("desiredReplicas") or current_min)
    active = int(status.get("currentReplicas") or desired)
    return {"replicas": active, "desired_replicas": desired, "min_replicas": current_min, "max_replicas": current_max}


def kubernetes_hpa_request(method: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
    namespace = os.getenv("WORKER_K8S_NAMESPACE", os.getenv("POD_NAMESPACE", "norify"))
    hpa = os.getenv("WORKER_K8S_HPA", os.getenv("WORKER_K8S_DEPLOYMENT", "sender-worker"))
    host = os.getenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
    port = os.getenv("KUBERNETES_SERVICE_PORT", "443")
    url = f"https://{host}:{port}/apis/autoscaling/v2/namespaces/{namespace}/horizontalpodautoscalers/{hpa}"
    token_path = os.getenv("KUBERNETES_TOKEN_PATH", "/var/run/secrets/kubernetes.io/serviceaccount/token")
    ca_path = os.getenv("KUBERNETES_CA_PATH", "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
    with open(token_path, encoding="utf-8") as token_file:
        token = token_file.read().strip()
    body = None if payload is None else json.dumps(payload).encode()
    content_type = "application/merge-patch+json" if method == "PATCH" else "application/json"
    request = urllib.request.Request(url, data=body, headers={"Authorization": f"Bearer {token}", "Content-Type": content_type}, method=method)
    context = ssl.create_default_context(cafile=ca_path if os.path.exists(ca_path) else None)
    with urllib.request.urlopen(request, timeout=10, context=context) as response:
        raw = response.read()
        return json.loads(raw.decode()) if raw else {}


def proxy_json_sync(method: str, url: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
    data = None if payload is None else json.dumps(payload).encode()
    headers = {"Content-Type": "application/json"} if payload is not None else {}
    request = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            raw = response.read()
            return json.loads(raw.decode()) if raw else {}
    except urllib.error.HTTPError as exc:
        message = exc.read().decode() or exc.reason
        raise RuntimeError(message) from exc


async def consume_rabbitmq(rabbitmq_url: str) -> None:
    while True:
        try:
            connection = await aio_pika.connect_robust(rabbitmq_url)
            logger.info("connected to rabbitmq status events")
            async with connection:
                channel = await connection.channel()
                message_queue = await channel.declare_queue("message.status.events", durable=True)
                campaign_queue = await channel.declare_queue("campaign.status.events", durable=True)
                result_queue = await channel.declare_queue("message.send.result", durable=True)
                error_queue = await channel.declare_queue("message.send.error", durable=True)
                await message_queue.bind("message.status.events", routing_key="#")
                await campaign_queue.bind("campaign.status.events", routing_key="#")
                await asyncio.gather(
                    consume_queue(message_queue, apply_message_event),
                    consume_queue(campaign_queue, apply_campaign_event),
                    consume_queue(result_queue, apply_worker_result_payload),
                    consume_queue(error_queue, apply_worker_result_payload),
                )
        except Exception:
            logger.exception("rabbitmq status consumer failed")
            await asyncio.sleep(2)


async def consume_queue(queue: Any, handler: Any) -> None:
    async with queue.iterator() as iterator:
        async for message in iterator:
            async with message.process(requeue=True):
                payload = json.loads(message.body.decode())
                await handler(payload)


@app.post("/worker/jobs", response_model=SendMessageJob)
async def create_worker_job(job: SendMessageJob) -> SendMessageJob:
    existing = queued_jobs.get(job.idempotency_key)
    if existing:
        return existing
    inserted = await write_queued_job(job)
    queued_jobs[job.idempotency_key] = job
    if inserted:
        await publish_worker_job(job)
    return job


@app.get("/worker/config/{channel_code}")
async def worker_config(channel_code: str) -> dict[str, Any]:
    try:
        cached = await read_channel_config_from_redis(channel_code)
        if cached:
            cached["source"] = "redis"
            return cached
    except Exception as exc:
        logger.warning("redis config lookup failed: %s", exc)

    try:
        config = await read_channel_config_from_postgres(channel_code)
        await write_channel_config_to_redis(channel_code, config)
        return config
    except Exception as exc:
        logger.warning("postgres config lookup failed: %s", exc)
        return WorkerChannelConfig(code=channel_code).model_dump()


async def apply_message_event(payload: dict[str, Any]) -> None:
    campaign_id = payload["campaign_id"]
    current = await read_campaign_snapshot(campaign_id)
    total = int(payload.get("total_messages") or campaign_totals.get(campaign_id, max(current.total_messages, current.processed + 1)))
    campaign_totals[campaign_id] = total
    key = event_key(payload)
    message_states.setdefault(campaign_id, {})[key] = str(payload.get("status") or "")
    states = message_states[campaign_id].values()
    success = sum(1 for status in states if status == "sent")
    failed = sum(1 for status in states if status == "failed")
    cancelled = sum(1 for status in states if status == "cancelled")
    processed = success + failed + cancelled
    snapshot = ProgressSnapshot(
        campaign_id=campaign_id,
        status=current.status,
        total_messages=total,
        processed=processed,
        success=success,
        failed=failed,
        cancelled=cancelled,
        p95_dispatch_ms=current.p95_dispatch_ms,
        progress_percent=min(100, round(processed / total * 100, 2)) if total else 0,
        updated_at=datetime.now(timezone.utc),
    )
    await remember_campaign_snapshot(snapshot)
    await broadcast(campaign_id, snapshot.model_dump(mode="json"))


async def apply_worker_result_payload(payload: dict[str, Any]) -> bool:
    return await apply_worker_result_event(MessageSendResult.model_validate(payload))


async def apply_worker_result_event(result: MessageSendResult) -> bool:
    if result.idempotency_key in processed_result_keys:
        return False
    applied = await write_delivery_result(result)
    if not applied:
        processed_result_keys.add(result.idempotency_key)
        return False
    processed_result_keys.add(result.idempotency_key)
    await apply_message_event({
        "campaign_id": result.campaign_id,
        "total_messages": campaign_totals.get(result.campaign_id, 0),
        "user_id": result.user_id,
        "channel_code": result.channel_code,
        "idempotency_key": result.idempotency_key,
        "status": result.status,
        "attempt": result.attempt,
        "error_code": result.error_code,
        "error_message": result.error_message,
    })
    return True


async def apply_campaign_event(payload: dict[str, Any]) -> None:
    campaign_id = payload["campaign_id"]
    current = await read_campaign_snapshot(campaign_id)
    total = int(payload.get("total_messages") or current.total_messages)
    processed = int(payload.get("processed") or current.processed)
    snapshot = ProgressSnapshot(
        campaign_id=campaign_id,
        status=str(payload.get("status") or current.status),
        total_messages=total,
        processed=processed,
        success=int(payload.get("success") or current.success),
        failed=int(payload.get("failed") or current.failed),
        cancelled=int(payload.get("cancelled") or current.cancelled),
        p95_dispatch_ms=int(payload.get("p95_dispatch_ms") or current.p95_dispatch_ms),
        progress_percent=min(100, round(processed / total * 100, 2)) if total else current.progress_percent,
        updated_at=datetime.now(timezone.utc),
    )
    await remember_campaign_snapshot(snapshot)
    if total > 0:
        campaign_totals[campaign_id] = total
    await broadcast(campaign_id, snapshot.model_dump(mode="json"))


def event_key(payload: dict[str, Any]) -> str:
    if payload.get("idempotency_key"):
        return str(payload["idempotency_key"])
    return f"{payload.get('user_id', '')}:{payload.get('channel_code', '')}"


async def publish_worker_job(job: SendMessageJob) -> None:
    rabbitmq_url = os.getenv("RABBITMQ_URL")
    if not rabbitmq_url or aio_pika is None:
        return
    connection = await aio_pika.connect_robust(rabbitmq_url)
    async with connection:
        channel = await connection.channel(publisher_confirms=True)
        await channel.default_exchange.publish(
            aio_pika.Message(
                body=job.model_dump_json().encode(),
                content_type="application/json",
                delivery_mode=aio_pika.DeliveryMode.PERSISTENT,
                message_id=job.idempotency_key,
                correlation_id=job.campaign_id,
                headers={"x-idempotency-key": job.idempotency_key},
            ),
            routing_key="message.send.request",
        )


async def read_channel_config_from_redis(channel_code: str) -> dict[str, Any] | None:
    if not REDIS_URL or redis_async is None:
        return None
    client = redis_async.from_url(REDIS_URL, decode_responses=True)
    try:
        raw = await client.get(f"channel-config:{channel_code}")
    finally:
        await client.aclose()
    return json.loads(raw) if raw else None


def campaign_snapshot_key(campaign_id: str) -> str:
    return f"campaign-snapshot:{campaign_id}"


async def redis_get_json(key: str) -> dict[str, Any] | None:
    if not REDIS_URL or redis_async is None:
        return None
    client = redis_async.from_url(REDIS_URL, decode_responses=True)
    try:
        raw = await client.get(key)
        return json.loads(raw) if raw else None
    except Exception as exc:
        logger.warning("redis json get failed: %s", exc)
        return None
    finally:
        await client.aclose()


async def redis_set_json(key: str, value: dict[str, Any], ttl_seconds: int) -> None:
    if not REDIS_URL or redis_async is None:
        return
    client = redis_async.from_url(REDIS_URL, decode_responses=True)
    try:
        await client.setex(key, ttl_seconds, json.dumps(value, default=str))
    except Exception as exc:
        logger.warning("redis json set failed: %s", exc)
    finally:
        await client.aclose()


async def redis_scan_json(match: str) -> list[dict[str, Any]]:
    if not REDIS_URL or redis_async is None:
        return []
    client = redis_async.from_url(REDIS_URL, decode_responses=True)
    try:
        out: list[dict[str, Any]] = []
        async for key in client.scan_iter(match):
            raw = await client.get(key)
            if raw:
                out.append(json.loads(raw))
        return out
    except Exception as exc:
        logger.warning("redis json scan failed: %s", exc)
        return []
    finally:
        await client.aclose()


async def read_campaign_snapshot(campaign_id: str) -> ProgressSnapshot:
    current = snapshots.get(campaign_id)
    cached = await redis_get_json(campaign_snapshot_key(campaign_id))
    if cached:
        snapshot = ProgressSnapshot.model_validate(cached)
        if current is None or snapshot.updated_at >= current.updated_at:
            snapshots[campaign_id] = snapshot
            return snapshot
    return current or ProgressSnapshot(campaign_id=campaign_id)


async def remember_campaign_snapshot(snapshot: ProgressSnapshot) -> None:
    snapshots[snapshot.campaign_id] = snapshot
    await redis_set_json(campaign_snapshot_key(snapshot.campaign_id), snapshot.model_dump(mode="json"), CAMPAIGN_SNAPSHOT_TTL_SECONDS)


async def list_campaign_snapshots() -> list[ProgressSnapshot]:
    merged = dict(snapshots)
    for cached in await redis_scan_json("campaign-snapshot:*"):
        snapshot = ProgressSnapshot.model_validate(cached)
        current = merged.get(snapshot.campaign_id)
        if current is None or snapshot.updated_at >= current.updated_at:
            merged[snapshot.campaign_id] = snapshot
    return list(merged.values())


async def read_worker_scaling_policy_from_redis() -> dict[str, int] | None:
    cached = await redis_get_json(WORKER_POLICY_KEY)
    if not cached:
        return None
    try:
        return normalize_worker_scaling_policy(cached)
    except Exception as exc:
        logger.warning("redis worker policy invalid: %s", exc)
        return None


async def write_worker_scaling_policy_to_redis(policy: dict[str, int]) -> None:
    await redis_set_json(WORKER_POLICY_KEY, policy, WORKER_POLICY_TTL_SECONDS)


def normalize_worker_scaling_policy(raw: dict[str, Any]) -> dict[str, int]:
    min_replicas = max(1, int(raw.get("min_replicas") or 1))
    max_replicas = max(min_replicas, int(raw.get("max_replicas") or worker_max_replicas()))
    desired = max(min_replicas, min(int(raw.get("desired_replicas") or min_replicas), max_replicas))
    replicas = max(1, int(raw.get("replicas") or desired))
    return {"replicas": replicas, "desired_replicas": desired, "min_replicas": min_replicas, "max_replicas": max_replicas}


async def maybe_await(value: Any) -> Any:
    if inspect.isawaitable(value):
        return await value
    return value


async def write_channel_config_to_redis(channel_code: str, config: dict[str, Any]) -> None:
    if not REDIS_URL or redis_async is None:
        return
    client = redis_async.from_url(REDIS_URL, decode_responses=True)
    try:
        await client.setex(f"channel-config:{channel_code}", 60, json.dumps(config))
    finally:
        await client.aclose()


async def read_channel_config_from_postgres(channel_code: str) -> dict[str, Any]:
    return await asyncio.to_thread(read_channel_config_from_postgres_sync, channel_code)


def read_channel_config_from_postgres_sync(channel_code: str) -> dict[str, Any]:
    if psycopg is None:
        raise RuntimeError("psycopg is not installed")
    if not POSTGRES_DSN:
        raise RuntimeError("POSTGRES_DSN is empty")
    with psycopg.connect(POSTGRES_DSN) as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT code, enabled, success_probability::float8, min_delay_seconds,
                       max_delay_seconds, max_parallelism, retry_limit
                FROM channels
                WHERE code = %s AND enabled = true
                """,
                (channel_code,),
            )
            row = cur.fetchone()
    if not row:
        return WorkerChannelConfig(code=channel_code, source="default").model_dump()
    return WorkerChannelConfig(
        code=row[0],
        enabled=bool(row[1]),
        success_probability=float(row[2]),
        min_delay_seconds=int(row[3]),
        max_delay_seconds=int(row[4]),
        max_parallelism=int(row[5]),
        retry_limit=int(row[6]),
        source="postgres",
    ).model_dump()


async def write_queued_job(job: SendMessageJob) -> bool:
    return await asyncio.to_thread(write_queued_job_sync, job)


def write_queued_job_sync(job: SendMessageJob) -> bool:
    if psycopg is None or not POSTGRES_DSN:
        return True
    with psycopg.connect(POSTGRES_DSN) as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO message_deliveries (
                  id, campaign_id, user_id, channel_code, message_body, status,
                  attempt, idempotency_key, queued_at, created_at, updated_at
                ) VALUES (
                  %s, %s, %s, %s, %s, 'queued',
                  %s, %s, now(), now(), now()
                )
                ON CONFLICT (idempotency_key) DO NOTHING
                """,
                (
                    f"delivery-{uuid.uuid4().hex[:16]}",
                    job.campaign_id,
                    job.user_id,
                    job.channel_code,
                    job.message_body,
                    job.attempt,
                    job.idempotency_key,
                ),
            )
            inserted = cur.rowcount > 0
        conn.commit()
    return inserted


async def write_delivery_result(result: MessageSendResult) -> bool:
    return await asyncio.to_thread(write_delivery_result_sync, result)


def write_delivery_result_sync(result: MessageSendResult) -> bool:
    if psycopg is None or not POSTGRES_DSN:
        return True
    with psycopg.connect(POSTGRES_DSN) as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE message_deliveries
                SET status = %s,
                    error_code = %s,
                    error_message = %s,
                    attempt = %s,
                    sent_at = now(),
                    finished_at = %s,
                    updated_at = now()
                WHERE idempotency_key = %s
                  AND status = 'queued'
                """,
                (
                    result.status,
                    result.error_code,
                    result.error_message,
                    result.attempt,
                    result.finished_at,
                    result.idempotency_key,
                ),
            )
            applied = cur.rowcount > 0
            if applied:
                if result.status == "sent":
                    cur.execute(
                        """
                        UPDATE campaigns
                        SET sent_count = sent_count + 1,
                            success_count = success_count + 1,
                            status = CASE WHEN sent_count + 1 >= total_messages THEN 'finished' ELSE status END,
                            finished_at = CASE WHEN sent_count + 1 >= total_messages THEN now() ELSE finished_at END,
                            updated_at = now()
                        WHERE id = %s
                        """,
                        (result.campaign_id,),
                    )
                else:
                    cur.execute(
                        """
                        UPDATE campaigns
                        SET sent_count = sent_count + 1,
                            failed_count = failed_count + 1,
                            status = CASE WHEN sent_count + 1 >= total_messages THEN 'finished' ELSE status END,
                            finished_at = CASE WHEN sent_count + 1 >= total_messages THEN now() ELSE finished_at END,
                            updated_at = now()
                        WHERE id = %s
                        """,
                        (result.campaign_id,),
                    )
        conn.commit()
    return applied
