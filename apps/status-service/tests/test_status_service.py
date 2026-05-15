from fastapi.testclient import TestClient
import pytest

from main import app, apply_message_event, campaign_totals, handle_ops_command, message_states, snapshots


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


@pytest.mark.asyncio
async def test_message_events_are_counted_by_idempotency_key():
    snapshots.clear()
    campaign_totals.clear()
    message_states.clear()

    base = {
        "campaign_id": "campaign-retry",
        "total_messages": 2,
        "user_id": "user-1",
        "channel_code": "telegram",
        "idempotency_key": "campaign-retry:user-1:telegram",
    }

    await apply_message_event({**base, "status": "failed", "attempt": 1})
    await apply_message_event({**base, "status": "failed", "attempt": 2})
    assert snapshots["campaign-retry"].failed == 1
    assert snapshots["campaign-retry"].processed == 1

    await apply_message_event({**base, "status": "sent", "attempt": 3})
    assert snapshots["campaign-retry"].success == 1
    assert snapshots["campaign-retry"].failed == 0
    assert snapshots["campaign-retry"].processed == 1


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
