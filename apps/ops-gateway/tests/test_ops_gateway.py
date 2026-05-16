import json

from fastapi.testclient import TestClient
import pytest

from main import (
    ProgressSnapshot,
    app,
    apply_campaign_event,
    apply_message_event,
    campaign_totals,
    connections,
    handle_ops_command,
    message_states,
    ops_connections,
    read_keda_worker_scaling_policy,
    snapshots,
)


class FakeRedisClient:
    def __init__(self, store):
        self.store = store

    async def get(self, key):
        return self.store.get(key)

    async def setex(self, key, _ttl, value):
        self.store[key] = value

    async def scan_iter(self, match):
        prefix = match.rstrip("*")
        for key in list(self.store):
            if key.startswith(prefix):
                yield key

    async def aclose(self):
        return None


class FakeRedisModule:
    def __init__(self, store):
        self.store = store

    def from_url(self, *_args, **_kwargs):
        return FakeRedisClient(self.store)


def enable_fake_redis(monkeypatch, store):
    monkeypatch.setattr("main.REDIS_URL", "redis://fake:6379/0")
    monkeypatch.setattr("main.redis_async", FakeRedisModule(store))


def test_health_identifies_ops_gateway():
    client = TestClient(app)

    response = client.get("/health/ready")

    assert response.status_code == 200
    assert response.json()["service"] == "ops-gateway"


def test_snapshot_defaults_for_reconnect():
    client = TestClient(app)
    response = client.get("/campaigns/campaign-1/snapshot")
    assert response.status_code == 200
    assert response.json()["campaign_id"] == "campaign-1"
    assert response.json()["processed"] == 0


def test_health_ready_allows_frontend_cors():
    client = TestClient(app)
    response = client.options(
        "/health/ready",
        headers={
            "Origin": "http://localhost:3000",
            "Access-Control-Request-Method": "GET",
        },
    )

    assert response.status_code == 200
    assert response.headers["access-control-allow-origin"] == "*"


def test_ingest_updates_snapshot():
    client = TestClient(app)
    event = {
        "type": "campaign.progress",
        "campaign_id": "campaign-1",
        "total_messages": 100,
        "processed": 10,
        "success": 9,
        "failed": 1,
        "progress_percent": 10,
    }
    assert client.post("/events/status", json=event).status_code == 200
    snapshot = client.get("/campaigns/campaign-1/snapshot").json()
    assert snapshot["processed"] == 10


def test_snapshot_reads_redis_snapshot_when_memory_is_empty(monkeypatch):
    snapshots.clear()
    store = {
        "campaign-snapshot:campaign-redis": json.dumps({
            "type": "campaign.progress",
            "campaign_id": "campaign-redis",
            "status": "running",
            "total_messages": 100,
            "processed": 44,
            "success": 43,
            "failed": 1,
            "cancelled": 0,
            "p95_dispatch_ms": 12,
            "progress_percent": 44,
            "updated_at": "2026-05-16T00:00:00Z",
        })
    }
    enable_fake_redis(monkeypatch, store)

    client = TestClient(app)
    response = client.get("/campaigns/campaign-redis/snapshot")

    assert response.status_code == 200
    assert response.json()["processed"] == 44
    assert snapshots["campaign-redis"].processed == 44


@pytest.mark.asyncio
async def test_ops_overview_aggregates_health_workers_and_realtime_state(monkeypatch):
    snapshots.clear()
    connections.clear()
    ops_connections.clear()
    snapshots["campaign-1"] = ProgressSnapshot(campaign_id="campaign-1", status="running", total_messages=10, processed=4, success=3, failed=1)

    async def fake_health(name, base_url):
        return {"id": name, "name": name, "url": f"{base_url}/health/ready", "status": "ready", "latency_ms": 1, "checked_at": "2026-05-16T00:00:00Z", "detail": "ready"}

    async def fake_worker_status():
        return {
            "active_workers": 2,
            "container_workers": 1,
            "min_workers": 1,
            "max_workers": 1,
            "queue_depth": 7,
            "replicas": 2,
            "desired_replicas": 2,
            "min_replicas": 1,
            "max_replicas": 20,
            "control_mode": "memory",
            "control_enabled": True,
            "autoscaler": "ops-gateway-memory",
        }

    monkeypatch.setattr("main.check_service_health", fake_health)
    monkeypatch.setattr("main.build_worker_status", fake_worker_status)

    client = TestClient(app)
    response = client.get("/ops/overview")

    assert response.status_code == 200
    payload = response.json()
    assert payload["service"] == "ops-gateway"
    assert payload["summary"]["services_ready"] == payload["summary"]["services_total"]
    assert payload["summary"]["active_campaigns"] == 1
    assert payload["summary"]["failed_messages"] == 1
    assert payload["worker"]["queue_depth"] == 7


@pytest.mark.asyncio
async def test_message_events_do_not_mutate_authoritative_campaign_snapshot():
    snapshots.clear()
    campaign_totals.clear()
    message_states.clear()

    await apply_campaign_event({
        "campaign_id": "campaign-retry",
        "status": "running",
        "total_messages": 10,
        "processed": 4,
        "success": 3,
        "failed": 1,
        "cancelled": 0,
        "progress_percent": 40,
    })

    base = {
        "campaign_id": "campaign-retry",
        "total_messages": 10,
        "user_id": "user-1",
        "channel_code": "telegram",
        "idempotency_key": "campaign-retry:user-1:telegram",
    }

    await apply_message_event({**base, "status": "failed", "attempt": 1})
    await apply_message_event({**base, "status": "sent", "attempt": 2})

    assert message_states["campaign-retry"]["campaign-retry:user-1:telegram"] == "sent"
    assert snapshots["campaign-retry"].success == 3
    assert snapshots["campaign-retry"].failed == 1
    assert snapshots["campaign-retry"].processed == 4

    await apply_campaign_event({
        "campaign_id": "campaign-retry",
        "status": "running",
        "total_messages": 10,
        "processed": 5,
        "success": 4,
        "failed": 1,
        "cancelled": 0,
        "progress_percent": 50,
    })

    assert snapshots["campaign-retry"].success == 4
    assert snapshots["campaign-retry"].processed == 5

    await apply_campaign_event({
        "campaign_id": "campaign-retry",
        "status": "running",
        "total_messages": 10,
        "processed": 5,
        "success": 5,
        "failed": 0,
        "cancelled": 0,
        "progress_percent": 50,
    })

    assert snapshots["campaign-retry"].failed == 0


@pytest.mark.asyncio
async def test_message_events_do_not_overwrite_cached_progress_snapshot(monkeypatch):
    snapshots.clear()
    campaign_totals.clear()
    message_states.clear()
    store = {}
    enable_fake_redis(monkeypatch, store)

    await apply_campaign_event({
        "campaign_id": "campaign-cache",
        "status": "running",
        "total_messages": 5,
        "processed": 2,
        "success": 2,
        "failed": 0,
        "cancelled": 0,
        "progress_percent": 40,
    })

    await apply_message_event({
        "campaign_id": "campaign-cache",
        "total_messages": 5,
        "user_id": "user-1",
        "channel_code": "email",
        "idempotency_key": "campaign-cache:user-1:email",
        "status": "sent",
    })

    cached = json.loads(store["campaign-snapshot:campaign-cache"])
    assert cached["campaign_id"] == "campaign-cache"
    assert cached["processed"] == 2
    assert cached["success"] == 2


@pytest.mark.asyncio
async def test_ops_campaign_action_proxies_to_campaign_service(monkeypatch):
    calls = []

    async def fake_proxy(method, url, payload=None):
        calls.append((method, url, payload))
        return {"id": "cmp-1", "status": "running"}

    monkeypatch.setattr("main.proxy_json", fake_proxy)

    result = await handle_ops_command({
        "id": "cmd-1",
        "type": "campaign.action",
        "payload": {"campaign_id": "cmp-1", "action": "start"},
    })

    assert result["type"] == "campaign.upsert"
    assert result["request_id"] == "cmd-1"
    assert calls == [("POST", "http://campaign-service:8080/campaigns/cmp-1/start", None)]


@pytest.mark.asyncio
async def test_ops_campaign_create_forwards_specific_recipients(monkeypatch):
    calls = []

    async def fake_proxy(method, url, payload=None):
        calls.append((method, url, payload))
        if url.endswith("/campaigns"):
            return {"id": "cmp-1", "status": "created"}
        return {"id": "cmp-1", "status": "running"}

    monkeypatch.setattr("main.proxy_json", fake_proxy)

    result = await handle_ops_command({
        "id": "cmd-1",
        "type": "campaign.create",
        "payload": {
            "name": "Exact",
            "template_id": "tpl-1",
            "selected_channels": ["email", "sms"],
            "total_recipients": 2,
            "specific_recipients": [
                {"user_id": "user-00042", "channels": ["email"]},
                {"user_id": "user-00077", "channels": ["sms"]},
            ],
        },
    })

    assert result["type"] == "campaign.upsert"
    assert calls[0][2]["specific_recipients"] == [
        {"user_id": "user-00042", "channels": ["email"]},
        {"user_id": "user-00077", "channels": ["sms"]},
    ]


@pytest.mark.asyncio
async def test_ops_campaign_stop_proxies_to_campaign_service(monkeypatch):
    calls = []

    async def fake_proxy(method, url, payload=None):
        calls.append((method, url, payload))
        return {"id": "cmp-1", "status": "stopped"}

    monkeypatch.setattr("main.proxy_json", fake_proxy)

    result = await handle_ops_command({
        "id": "cmd-stop",
        "type": "campaign.action",
        "payload": {"campaign_id": "cmp-1", "action": "stop"},
    })

    assert result["type"] == "campaign.upsert"
    assert result["campaign"]["status"] == "stopped"
    assert calls == [("POST", "http://campaign-service:8080/campaigns/cmp-1/stop", None)]


@pytest.mark.asyncio
async def test_ops_error_group_switch_uses_selected_channel(monkeypatch):
    calls = []

    async def fake_proxy(method, url, payload=None):
        calls.append((method, url, payload))
        return {"queued": 12, "campaign": {"id": "cmp-1", "status": "retrying"}}

    monkeypatch.setattr("main.proxy_json", fake_proxy)

    result = await handle_ops_command({
        "id": "cmd-2",
        "type": "error_group.action",
        "payload": {"campaign_id": "cmp-1", "group_id": "grp-1", "action": "switch_channel", "to_channel": "email"},
    })

    assert result["type"] == "error_group.resolved"
    assert result["group_id"] == "grp-1"
    assert calls == [(
        "POST",
        "http://campaign-service:8080/campaigns/cmp-1/error-groups/grp-1/switch-channel",
        {"to_channel": "email"},
    )]


@pytest.mark.asyncio
async def test_ops_health_check_returns_realtime_snapshot(monkeypatch):
    async def fake_proxy(method, url, payload=None):
        assert method == "GET"
        assert url.endswith("/health/ready")
        assert payload is None
        return {"status": "ready"}

    monkeypatch.setattr("main.proxy_json", fake_proxy)

    result = await handle_ops_command({"id": "cmd-health", "type": "health.check", "payload": {}})

    assert result["type"] == "health.snapshot"
    assert result["request_id"] == "cmd-health"
    assert len(result["services"]) >= 3
    assert {service["status"] for service in result["services"]} == {"ready"}


@pytest.mark.asyncio
async def test_worker_status_combines_replicas_and_sampled_worker_stats(monkeypatch):
    async def fake_proxy(method, url, payload=None):
        assert method == "GET"
        assert url.endswith("/worker/stats")
        return {"active_workers": 1, "min_workers": 1, "max_workers": 1, "queue_depth": 42}

    monkeypatch.setattr("main.proxy_json", fake_proxy)
    monkeypatch.setattr("main.read_worker_scaling_policy", lambda: {"replicas": 3, "desired_replicas": 4, "min_replicas": 2, "max_replicas": 8})

    client = TestClient(app)
    response = client.get("/workers/status")

    assert response.status_code == 200
    assert response.json()["container_workers"] == 1
    assert response.json()["active_workers"] == 3
    assert response.json()["replicas"] == 3
    assert response.json()["desired_replicas"] == 4
    assert response.json()["min_replicas"] == 2
    assert response.json()["max_replicas"] == 8
    assert response.json()["queue_depth"] == 42


@pytest.mark.asyncio
async def test_worker_bounds_update_sets_min_and_max(monkeypatch):
    calls = []

    async def fake_proxy(method, url, payload=None):
        return {"active_workers": 1, "min_workers": 1, "max_workers": 1, "queue_depth": 0}

    def fake_apply(min_replicas, max_replicas):
        calls.append((min_replicas, max_replicas))
        return {"replicas": 3, "desired_replicas": 3, "min_replicas": min_replicas, "max_replicas": max_replicas}

    monkeypatch.setenv("WORKER_CONTROL_MODE", "memory")
    monkeypatch.setattr("main.proxy_json", fake_proxy)
    monkeypatch.setattr("main.apply_worker_bounds", fake_apply)

    client = TestClient(app)
    response = client.post("/workers/bounds", json={"min_replicas": 2, "max_replicas": 6})

    assert response.status_code == 200
    assert calls == [(2, 6)]
    assert response.json()["desired_replicas"] == 3
    assert response.json()["min_replicas"] == 2
    assert response.json()["max_replicas"] == 6

    invalid = client.post("/workers/bounds", json={"min_replicas": 7, "max_replicas": 6})
    assert invalid.status_code == 400


def test_keda_worker_policy_reads_scaled_object_and_deployment(monkeypatch):
    def fake_scaled_object(method, payload=None):
        assert method == "GET"
        assert payload is None
        return {"spec": {"minReplicaCount": 3, "maxReplicaCount": 20}}

    def fake_deployment(method, payload=None):
        assert method == "GET"
        assert payload is None
        return {"spec": {"replicas": 8}, "status": {"readyReplicas": 7}}

    monkeypatch.setattr("main.kubernetes_scaled_object_request", fake_scaled_object)
    monkeypatch.setattr("main.kubernetes_deployment_request", fake_deployment)

    policy = read_keda_worker_scaling_policy()

    assert policy == {"replicas": 7, "desired_replicas": 8, "min_replicas": 3, "max_replicas": 20}


def test_keda_worker_policy_falls_back_to_dns_when_scaled_object_is_missing(monkeypatch):
    monkeypatch.setenv("WORKER_CONTROL_MODE", "keda")
    monkeypatch.setattr("main.kubernetes_scaled_object_request", lambda *_args, **_kwargs: (_ for _ in ()).throw(RuntimeError("404")))
    monkeypatch.setattr("main.socket.getaddrinfo", lambda *_args, **_kwargs: [
        (None, None, None, "", ("172.20.0.2", 0)),
        (None, None, None, "", ("172.20.0.3", 0)),
    ])

    import asyncio

    policy = asyncio.run(__import__("main").read_worker_scaling_policy())

    assert policy == {"replicas": 2, "desired_replicas": 2, "min_replicas": 2, "max_replicas": 2}


def test_keda_worker_bounds_fall_back_to_deployment_when_scaled_object_is_missing(monkeypatch):
    store = {}
    patches = []
    enable_fake_redis(monkeypatch, store)
    monkeypatch.setenv("WORKER_CONTROL_MODE", "keda")
    monkeypatch.setattr("main.kubernetes_scaled_object_request", lambda *_args, **_kwargs: (_ for _ in ()).throw(RuntimeError("404")))

    def fake_deployment(method, payload=None):
        if method == "PATCH":
            patches.append(payload)
            return {"spec": {"replicas": payload["spec"]["replicas"]}, "status": {"readyReplicas": payload["spec"]["replicas"]}}
        return {"spec": {"replicas": 3}, "status": {"readyReplicas": 3}}

    async def fake_proxy(_method, _url, payload=None):
        return {"active_workers": 1, "min_workers": 1, "max_workers": 1, "queue_depth": 0}

    monkeypatch.setattr("main.kubernetes_deployment_request", fake_deployment)
    monkeypatch.setattr("main.proxy_json", fake_proxy)

    client = TestClient(app)
    response = client.post("/workers/bounds", json={"min_replicas": 1, "max_replicas": 5})

    assert response.status_code == 200
    assert response.json()["replicas"] == 3
    assert response.json()["desired_replicas"] == 3
    assert response.json()["min_replicas"] == 1
    assert response.json()["max_replicas"] == 5
    assert patches == []
    assert json.loads(store["worker-scaling-policy:sender-worker"])["max_replicas"] == 5


def test_keda_worker_bounds_fallback_clamps_deployment_replicas(monkeypatch):
    store = {}
    patches = []
    enable_fake_redis(monkeypatch, store)
    monkeypatch.setenv("WORKER_CONTROL_MODE", "keda")
    monkeypatch.setattr("main.kubernetes_scaled_object_request", lambda *_args, **_kwargs: (_ for _ in ()).throw(RuntimeError("404")))

    def fake_deployment(method, payload=None):
        if method == "PATCH":
            patches.append(payload)
            return {"spec": {"replicas": payload["spec"]["replicas"]}, "status": {"readyReplicas": payload["spec"]["replicas"]}}
        return {"spec": {"replicas": 3}, "status": {"readyReplicas": 3}}

    async def fake_proxy(_method, _url, payload=None):
        return {"active_workers": 1, "min_workers": 1, "max_workers": 1, "queue_depth": 0}

    monkeypatch.setattr("main.kubernetes_deployment_request", fake_deployment)
    monkeypatch.setattr("main.proxy_json", fake_proxy)

    client = TestClient(app)
    response = client.post("/workers/bounds", json={"min_replicas": 4, "max_replicas": 8})

    assert response.status_code == 200
    assert response.json()["desired_replicas"] == 4
    assert patches == [{"spec": {"replicas": 4}}]


@pytest.mark.asyncio
async def test_rabbitmq_status_topology_declares_exchanges_before_binding():
    class FakeQueue:
        def __init__(self, name):
            self.name = name
            self.binds = []

        async def bind(self, exchange, routing_key):
            self.binds.append((exchange["name"], routing_key))

    class FakeChannel:
        def __init__(self):
            self.exchanges = []
            self.queues = {}

        async def declare_exchange(self, name, **kwargs):
            self.exchanges.append((name, kwargs))
            return {"name": name}

        async def declare_queue(self, name, **_kwargs):
            queue = FakeQueue(name)
            self.queues[name] = queue
            return queue

    channel = FakeChannel()
    message_queue, campaign_queue, result_queue, error_queue = await __import__("main").declare_status_topology(channel)

    assert [name for name, _ in channel.exchanges] == ["message.status.events", "campaign.status.events"]
    assert message_queue.binds == [("message.status.events", "#")]
    assert campaign_queue.binds == [("campaign.status.events", "#")]
    assert result_queue.name == "message.send.result"
    assert error_queue.name == "message.send.error"


@pytest.mark.asyncio
async def test_memory_worker_bounds_roundtrip_through_redis(monkeypatch):
    store = {}
    enable_fake_redis(monkeypatch, store)
    monkeypatch.setenv("WORKER_CONTROL_MODE", "memory")

    main = __import__("main")
    policy = await main.apply_worker_bounds(3, 9)
    main.worker_bounds_state["min_replicas"] = 1
    main.worker_bounds_state["max_replicas"] = 20
    main.worker_replica_state["desired_replicas"] = 1
    restored = await main.read_worker_scaling_policy()

    assert policy == {"replicas": 3, "desired_replicas": 3, "min_replicas": 3, "max_replicas": 9}
    assert restored == policy


def test_worker_bounds_rejects_disabled_control(monkeypatch):
    monkeypatch.setenv("WORKER_CONTROL_MODE", "disabled")

    client = TestClient(app)
    response = client.post("/workers/bounds", json={"min_replicas": 1, "max_replicas": 2})

    assert response.status_code == 503


def test_disabled_worker_status_counts_dns_replicas(monkeypatch):
    monkeypatch.setenv("WORKER_CONTROL_MODE", "disabled")
    monkeypatch.setattr("main.socket.getaddrinfo", lambda *_args, **_kwargs: [
        (None, None, None, "", ("172.20.0.2", 0)),
        (None, None, None, "", ("172.20.0.3", 0)),
        (None, None, None, "", ("172.20.0.3", 0)),
    ])

    import asyncio

    assert asyncio.run(__import__("main").read_worker_scaling_policy()) == {"replicas": 2, "desired_replicas": 2, "min_replicas": 2, "max_replicas": 2}
