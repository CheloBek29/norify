# Chaos Duplicate Test Report

## Executive Summary

- Worktree: `/private/tmp/norify-chaos-duplicates`
- Branch: `tests/chaos-duplicates-pack`
- Commit tested: `335eab7 test: add chaos duplicate testing pack`
- Compose project: `norifychaos`
- Port override: `/private/tmp/norify-chaos-ports.override.yml`
- Isolated stack started: yes
- Isolated stack cleanup: completed with `docker compose down` and no `-v`
- Existing demo stack touched: no
- Overall result: **PARTIAL**

Safe chaos and worker stop/start passed with zero duplicates detected through the HTTP delivery endpoint. RabbitMQ restart-after-recovery failed: services reported ready after RabbitMQ restart, but campaign-service could not publish a new campaign dispatch because its RabbitMQ channel stayed stale.

## Ports Used

| Service | Host URL |
|---|---|
| frontend | `http://localhost:13000` |
| auth-service | `http://localhost:18081` |
| user-service | `http://localhost:18082` |
| template-service | `http://localhost:18083` |
| channel-service | `http://localhost:18084` |
| campaign-service | `http://localhost:18085` |
| dispatcher-service | `http://localhost:18086` |
| sender-worker | `http://localhost:18087` |
| notification-error-service | `http://localhost:18088` |
| postgres-viewer | `http://localhost:18089` |
| status-service | `http://localhost:18090` |
| RabbitMQ management | `http://localhost:25672` |
| RabbitMQ AMQP | `localhost:25673` |
| PostgreSQL | `localhost:25432` |
| Redis | `localhost:26379` |

## Commands Run

```bash
COMPOSE_PROJECT_NAME=norifychaos docker compose -f docker-compose.yml -f /private/tmp/norify-chaos-ports.override.yml up -d --build

CAMPAIGN_URL=http://localhost:18085 \
DISPATCHER_URL=http://localhost:18086 \
SENDER_URL=http://localhost:18087 \
STATUS_URL=http://localhost:18090 \
RABBITMQ_MGMT_URL=http://localhost:25672 \
CHAOS_CAMPAIGNS=3 CHAOS_USERS=10 CHAOS_CHANNELS=2 CHAOS_TIMEOUT_SECONDS=180 \
make chaos-test-safe

CAMPAIGN_URL=http://localhost:18085 \
DISPATCHER_URL=http://localhost:18086 \
SENDER_URL=http://localhost:18087 \
STATUS_URL=http://localhost:18090 \
RABBITMQ_MGMT_URL=http://localhost:25672 \
COMPOSE_PROJECT_NAME=norifychaos \
COMPOSE_FILES=docker-compose.yml,/private/tmp/norify-chaos-ports.override.yml \
CHAOS_CAMPAIGNS=2 CHAOS_USERS=10 CHAOS_CHANNELS=2 CHAOS_TIMEOUT_SECONDS=180 \
make chaos-test-worker-fault

CAMPAIGN_URL=http://localhost:18085 \
DISPATCHER_URL=http://localhost:18086 \
SENDER_URL=http://localhost:18087 \
STATUS_URL=http://localhost:18090 \
RABBITMQ_MGMT_URL=http://localhost:25672 \
COMPOSE_PROJECT_NAME=norifychaos \
COMPOSE_FILES=docker-compose.yml,/private/tmp/norify-chaos-ports.override.yml \
CHAOS_CAMPAIGNS=1 CHAOS_USERS=5 CHAOS_CHANNELS=2 CHAOS_TIMEOUT_SECONDS=180 \
make chaos-test-rabbitmq-after-restart

COMPOSE_PROJECT_NAME=norifychaos docker compose -f docker-compose.yml -f /private/tmp/norify-chaos-ports.override.yml down
```

## Scenario Results

| Scenario | Result | Expected deliveries | Actual completed | Duplicate rows | Duplicate successes | Notes |
|---|---:|---:|---:|---:|---:|---|
| Safe chaos: 3 campaigns x 10 users x 2 channels | PASS | 60 | 60 | 0 | 0 | Throughput 4.977 deliveries/sec. |
| Safe chaos: parallel start probe | PASS | 20 | 20 rows | 0 | 0 | Five concurrent start calls did not produce visible duplicate delivery rows in this run. |
| Worker fault: 2 campaigns x 10 users x 2 channels | PASS | 40 | 40 | 0 | 0 | Worker fault scenario also ran a separate 20-delivery parallel-start probe with zero duplicates. |
| Worker stop/start campaign | PASS | 20 | 20 rows | 0 | 0 | `sender-worker` was stopped and started through isolated Compose project. Campaign completed without duplicates. |
| RabbitMQ restart-after-recovery baseline campaign | PASS | 10 | 10 | 0 | 0 | Baseline before restart worked. |
| RabbitMQ restart-after-recovery parallel start probe | PASS | 10 | 10 rows | 0 | 0 | No duplicate rows detected before RabbitMQ restart. |
| RabbitMQ restart then new campaign | FAIL | 10 | 0 | 0 | 0 | Start returned HTTP 502. Campaign remained `created`; no dispatch happened. |
| Forced failure + parallel retry | SKIPPED | n/a | n/a | n/a | n/a | Current main has no deterministic safe forced-failure API. |

## RabbitMQ Failure Evidence

RabbitMQ restart action succeeded:

```text
Container norifychaos-rabbitmq-1 Restarting
Container norifychaos-rabbitmq-1 Started
```

After restart, readiness endpoints still returned `200` for campaign-service, dispatcher-service, sender-worker, and status-service.

New campaign start after restart failed:

```text
start_status: 502
start_body: {"error":"Exception (504) Reason: \"channel/connection is not open\""}
```

Final campaign state after 180 seconds:

```text
status: created
total_messages: 10
sent_count: 0
success_count: 0
failed_count: 0
delivery_rows: 0
timed_out: true
```

Dispatcher and sender-worker logs showed reconnect attempts after the broker restart, but campaign-service still held a stale publisher channel and could not publish dispatch for the new campaign.

## Duplicate Check Result

No duplicate rows or duplicate successful deliveries were detected in any completed scenario.

Important limitation: duplicate detection used `GET /campaigns/{id}/deliveries`, which returns at most 500 rows. These results are valid for the small scenarios above, but not proof for large campaigns.

## Fault Injection Result

| Fault | Result | Evidence |
|---|---:|---|
| Stop/start sender-worker | PASS | Fault campaign produced 20 delivery rows, 20 unique keys, 0 duplicate rows, 0 duplicate successes, no timeout. |
| Restart RabbitMQ, then create/start new campaign | FAIL | Start returned `502` with `channel/connection is not open`; campaign stayed `created`; no delivery rows were created. |
| RabbitMQ active-load restart | NOT RUN | The executed RabbitMQ scenario was idle restart followed by a new campaign. Do not claim active-load RabbitMQ recovery. |

## What Passed

- Isolated stack started without colliding with the existing demo stack.
- Readiness endpoints returned 200 on remapped ports.
- Safe N-campaign scenario completed all expected deliveries.
- Parallel starts did not create detected duplicate delivery rows in this run.
- Sender-worker stop/start recovered for a new campaign.
- HTTP duplicate detection found zero duplicates in completed small scenarios.

## What Failed

- RabbitMQ restart-after-recovery failed for campaign-service publishing.
- Readiness is misleading after RabbitMQ restart: campaign-service returned ready but could not publish dispatch.

## What Was Skipped

- Forced failure + retry: skipped because current main does not expose a deterministic safe forced-failure API.
- RabbitMQ active-load restart: skipped; only idle restart then new campaign was tested.
- DB-level duplicate inspection: not implemented in this pack; HTTP endpoint was enough for these small scenarios.

## Safe Demo Claims

- Safe small campaign delivery worked in an isolated runtime.
- Sender-worker stop/start is safe to demo on a prepared stack.
- In small HTTP-visible scenarios, duplicate rows were not observed.

## Unsafe Claims

- Do not claim RabbitMQ restart recovery for campaign-service on current main.
- Do not claim active-load RabbitMQ fault tolerance.
- Do not claim exactly-once external provider delivery.
- Do not claim duplicate-free behavior for campaigns larger than the delivery endpoint limit without DB inspection.

## Reports

- Latest machine-readable report: `tests/chaos/results/latest_chaos_report.json`
- Human-readable report: `docs/chaos_duplicate_test_report.md`

Note: `latest_chaos_report.json` contains the final RabbitMQ restart-after-recovery run. The safe and worker-fault numeric results above were captured from the immediately preceding runtime reports before the final run overwrote the `latest` file.
