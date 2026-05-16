# Chaos Duplicate Testing

`tests/chaos/chaos_duplicate_test.py` is a human-runnable automated chaos/load/duplicate test pack for the current `main` codebase.

It is intentionally honest: unsupported API capabilities are skipped with an explanation instead of being faked.

## Why This Matters

Notification systems must avoid sending the same notification to the same recipient through the same channel more than once. The dangerous key is:

```text
campaign_id + user_id/recipient_id + channel_id
```

Retries, repeated starts, RabbitMQ redelivery, and worker restarts can all accidentally create duplicate visible deliveries if idempotency is weak.

## Safe Test

Runs readiness, N campaign happy path, parallel start duplicate checks, and HTTP delivery duplicate detection. It does not stop or restart containers.

```bash
make chaos-test-safe
```

Larger safe example:

```bash
CHAOS_CAMPAIGNS=5 CHAOS_USERS=20 CHAOS_CHANNELS=3 make chaos-test-safe
```

## Worker Fault Test

Stops and starts only `sender-worker`. This is the safest container fault scenario.

```bash
CHAOS_ALLOW_CONTAINER_CONTROL=true CHAOS_STOP_WORKER=true CHAOS_RESTART_WORKER=true make chaos-test-worker-fault
```

The script uses:

```bash
docker compose stop sender-worker
docker compose start sender-worker
```

It never deletes volumes.

## RabbitMQ Restart Test

This is intentionally gated because RabbitMQ restart is risky on current main unless tested in an isolated stack.

```bash
CHAOS_ALLOW_CONTAINER_CONTROL=true CHAOS_RESTART_RABBITMQ=true CHAOS_SKIP_UNSAFE_RABBITMQ=false make chaos-test-rabbitmq-after-restart
```

This test restarts RabbitMQ and then starts a new campaign after recovery. It does not prove active-load RabbitMQ safety unless the report explicitly says the restart happened during active load.

## Configuration

| Variable | Default | Meaning |
|---|---:|---|
| `CAMPAIGN_URL` | `http://localhost:8085` | campaign-service base URL |
| `DISPATCHER_URL` | `http://localhost:8086` | dispatcher-service base URL |
| `SENDER_URL` | `http://localhost:8087` | sender-worker base URL |
| `STATUS_URL` | `http://localhost:8090` | status-service base URL |
| `RABBITMQ_MGMT_URL` | `http://localhost:15672` | RabbitMQ management URL |
| `CHAOS_CAMPAIGNS` | `3` | campaigns to create |
| `CHAOS_USERS` | `10` | recipients per campaign |
| `CHAOS_CHANNELS` | `2` | selected channels per campaign |
| `CHAOS_TIMEOUT_SECONDS` | `180` | poll timeout |
| `CHAOS_POLL_INTERVAL_SECONDS` | `2` | poll interval |
| `CHAOS_PARALLEL_STARTS` | `true` | run parallel start duplicate scenario |
| `CHAOS_PARALLEL_RETRIES` | `true` | reserved for retry scenario |
| `CHAOS_FORCE_FAILURE` | `false` | forced failure/retry scenario opt-in |
| `CHAOS_ALLOW_CONTAINER_CONTROL` | `false` | permits Docker stop/start/restart |
| `CHAOS_STOP_WORKER` | `false` | stop/start sender-worker |
| `CHAOS_RESTART_WORKER` | `false` | alias flag for worker fault target |
| `CHAOS_RESTART_RABBITMQ` | `false` | restart RabbitMQ |
| `CHAOS_SKIP_UNSAFE_RABBITMQ` | `true` | prevents RabbitMQ restart by default |
| `COMPOSE_FILES` | `docker-compose.yml` | comma-separated Compose files |
| `CHAOS_REPORT_JSON` | `tests/chaos/results/latest_chaos_report.json` | JSON report path |
| `CHAOS_REPORT_MD` | `docs/chaos_duplicate_test_report.md` | Markdown report path |

## Interpreting Duplicate Counts

- `duplicate_success_count = 0`: no duplicate successful delivery was found through available data.
- `duplicate_delivery_rows_count > 0`: multiple delivery rows share the same campaign/user/channel key.
- `duplicate_success_count > 0`: visible duplicate delivery was detected.
- `duplicate_keys`: first examples, capped to 20.

Important limitation: current HTTP duplicate detection uses `/campaigns/{id}/deliveries`, which is limited to 500 rows. Larger campaigns need DB-level inspection.

## Pass / Fail Meaning

PASS means all executed scenarios completed without detected duplicate successful deliveries and without timeouts.

FAIL means a scenario timed out, a critical HTTP call failed, or duplicate deliveries were detected.

SKIP means the API or safety flag required for the scenario was not available.

## What This Does Not Prove

- It does not prove exactly-once external provider delivery.
- It does not prove 50k-user behavior unless that scenario is actually run.
- It does not prove active-load RabbitMQ recovery unless that scenario is explicitly enabled and passes.
- It does not prove duplicate absence beyond the HTTP delivery endpoint limit.
- It does not fix current main retry/idempotency gaps.

## Reports

Each run writes:

```text
tests/chaos/results/latest_chaos_report.json
docs/chaos_duplicate_test_report.md
```

The JSON is machine-readable and should be used for CI or comparison. The Markdown is for humans.
