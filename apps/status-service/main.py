from __future__ import annotations

import asyncio
import json
import logging
import os
import uuid
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from contextlib import asynccontextmanager
from typing import Any, Optional

from fastapi import FastAPI, WebSocket, WebSocketDisconnect
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


snapshots: dict[str, ProgressSnapshot] = {}
connections: dict[str, set[WebSocket]] = {}
ops_connections: set[WebSocket] = set()
campaign_totals: dict[str, int] = {}
message_states: dict[str, dict[str, str]] = {}
processed_result_keys: set[str] = set()
queued_jobs: dict[str, SendMessageJob] = {}
logger = logging.getLogger("status-service")
CAMPAIGN_SERVICE_URL = os.getenv("CAMPAIGN_SERVICE_URL", "http://campaign-service:8080")
CHANNEL_SERVICE_URL = os.getenv("CHANNEL_SERVICE_URL", "http://channel-service:8080")
TEMPLATE_SERVICE_URL = os.getenv("TEMPLATE_SERVICE_URL", "http://template-service:8080")
POSTGRES_DSN = os.getenv("POSTGRES_DSN", "")
REDIS_URL = os.getenv("REDIS_URL", "")
HEALTH_TARGETS = [
    ("auth-service", os.getenv("AUTH_SERVICE_URL", "http://auth-service:8080")),
    ("user-service", os.getenv("USER_SERVICE_URL", "http://user-service:8080")),
    ("template-service", TEMPLATE_SERVICE_URL),
    ("channel-service", CHANNEL_SERVICE_URL),
    ("campaign-service", CAMPAIGN_SERVICE_URL),
    ("dispatcher-service", os.getenv("DISPATCHER_SERVICE_URL", "http://dispatcher-service:8080")),
    ("sender-worker", os.getenv("SENDER_SERVICE_URL", "http://sender-worker:8080")),
    ("notification-error-service", os.getenv("NOTIFICATION_ERROR_SERVICE_URL", "http://notification-error-service:8080")),
    ("status-service", os.getenv("STATUS_SERVICE_URL", "http://status-service:8080")),
]


@asynccontextmanager
async def lifespan(_: FastAPI):
    rabbitmq_url = os.getenv("RABBITMQ_URL")
    if rabbitmq_url and aio_pika is not None:
        asyncio.create_task(consume_rabbitmq(rabbitmq_url))
    yield


app = FastAPI(title="Norify Status Service", lifespan=lifespan)
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["GET", "POST", "OPTIONS"],
    allow_headers=["*"],
)


@app.get("/health/live")
async def live() -> dict[str, str]:
    return {"status": "live", "service": "status-service"}


@app.get("/health/ready")
async def ready() -> dict[str, str]:
    return {"status": "ready", "service": "status-service"}


@app.get("/metrics")
async def metrics() -> str:
    campaign_clients = sum(len(items) for items in connections.values())
    return f"websocket_connected_clients {campaign_clients + len(ops_connections)}\n"


@app.get("/campaigns/{campaign_id}/snapshot", response_model=ProgressSnapshot)
async def snapshot(campaign_id: str) -> ProgressSnapshot:
    return snapshots.get(campaign_id, ProgressSnapshot(campaign_id=campaign_id))


@app.post("/events/status", response_model=ProgressSnapshot)
async def ingest_status(event: ProgressSnapshot) -> ProgressSnapshot:
    snapshots[event.campaign_id] = event
    await broadcast(event.campaign_id, event.model_dump(mode="json"))
    return event


@app.websocket("/ws/campaigns/{campaign_id}")
async def campaign_ws(websocket: WebSocket, campaign_id: str) -> None:
    await websocket.accept()
    connections.setdefault(campaign_id, set()).add(websocket)
    try:
        await websocket.send_json(snapshots.get(campaign_id, ProgressSnapshot(campaign_id=campaign_id)).model_dump(mode="json"))
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
            command = json.loads(await websocket.receive_text())
            result = await handle_ops_command(command)
            await broadcast_ops(result)
    except (WebSocketDisconnect, json.JSONDecodeError):
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
            channel = payload.get("channel") if isinstance(payload.get("channel"), dict) else {}
            updated = await proxy_json("PATCH", f"{CHANNEL_SERVICE_URL}/channels/{code}", channel)
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
    current = snapshots.get(campaign_id, ProgressSnapshot(campaign_id=campaign_id))
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
    snapshots[campaign_id] = snapshot
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
    current = snapshots.get(campaign_id, ProgressSnapshot(campaign_id=campaign_id))
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
    snapshots[campaign_id] = snapshot
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
