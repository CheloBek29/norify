# Automated Testing Pack

Date: 2026-05-16  
Branch: `tests/main-automation-pack`  
Base: latest `origin/main`

## Existing Test Map

| Area | Existing coverage |
|---|---|
| Go packages | Unit tests for auth, campaigns helpers, channels, reliability, runtime publishing, templates, users filters. |
| Go services | Basic campaign-service rollback/startability tests, dispatcher window/throttle tests, sender-worker campaign-status tests. |
| Frontend | Vitest/Testing Library coverage for login, navigation, campaign actions, templates, channels, health, delivery/status views. |
| Status service | Pytest coverage for snapshots, status events, ops proxy commands, health snapshots. |
| Root Python tests | Compose/readme check for Postgres viewer. |
| Smoke | Shell health smoke script under `tests/smoke/health.sh`. |

## What Was Added

### Go

- Expanded campaign-service startability table to cover running, retrying, cancelled, empty, and unknown statuses.
- Added explicit skipped known-risk tests documenting current `main` gaps:
  - campaign retry still uses synthetic `failed_count` dispatch rather than real failed rows;
  - campaign-level switch-channel still republishes synthetic dispatch;
  - sender-worker still calls provider stub before a terminal/stale delivery guard.
- Expanded sender-worker campaign status table.
- Expanded dispatcher-service tests for tiny message budgets, missing channel defaults, and synthetic user ID formatting.

### Frontend

- Added expected-failure tests for current `main` gaps:
  - backend create failure should not create fake local campaign success;
  - unsafe campaign-level switch-channel should be hidden/disabled with warning text.

These are `it.fails(...)` tests so the suite remains runnable while still documenting the expected fixed behavior.

### Python / Compose

- Added Compose contract tests for required services, required host ports, pgweb/Postgres viewer config, and dependency healthchecks.
- Updated stale Postgres viewer test from Adminer to pgweb.

### Runtime

- Added `tests/runtime/runtime_smoke.py`.
- Added `make runtime-smoke`.

The runtime smoke checks readiness, creates a 2-recipient `custom_app` campaign, starts it, waits for completion, and verifies delivery rows do not contain duplicate user/channel pairs when the endpoint is available.

## How To Run

All local non-runtime checks:

```bash
go test ./...
make lint
cd apps/frontend && npm ci && npm run build && npm test
python3 -m unittest discover -s tests -p 'test_*.py'
PYTHON=.venv312/bin/python make status-test
docker compose config
```

Go only:

```bash
go test ./...
make lint
```

Frontend only:

```bash
cd apps/frontend
npm ci
npm run build
npm test
```

Python only:

```bash
python3 -m unittest discover -s tests -p 'test_*.py'
PYTHON=.venv312/bin/python make status-test
```

Compose config:

```bash
docker compose config
```

If a demo override is added later:

```bash
docker compose -f docker-compose.yml -f docker-compose.demo.yml config
```

Runtime smoke:

```bash
docker compose up -d --build
python3 tests/runtime/runtime_smoke.py
```

Configurable runtime environment:

```bash
BASE_URL_CAMPAIGN=http://localhost:8085 \
BASE_URL_DISPATCHER=http://localhost:8086 \
BASE_URL_SENDER=http://localhost:8087 \
BASE_URL_STATUS=http://localhost:8090 \
RUNTIME_TIMEOUT_SECONDS=120 \
python3 tests/runtime/runtime_smoke.py
```

## What These Tests Prove

- Core service helper logic still compiles and behaves consistently.
- Compose still declares the expected services and public ports.
- Frontend still renders and test-runs under Vitest.
- Status-service pytest coverage still passes.
- A running Compose stack can process a tiny campaign through campaign-service, dispatcher, sender-worker, and delivery persistence.

## What These Tests Do Not Prove

- They do not prove production-grade exactly-once delivery.
- They do not prove campaign start is atomically idempotent on current `main`; that requires a DB-backed concurrent start test after the backend fix.
- They do not prove retry correctness on current `main`; current `main` still needs row-based retry claiming.
- They do not prove campaign-level switch-channel safety; current `main` still exposes synthetic whole-campaign switch behavior.
- They do not prove RabbitMQ/Postgres active-load fault tolerance.
- They do not prove 50k-user performance.

## Remaining Missing Tests

1. DB-backed concurrent `POST /campaigns/{id}/start` test.
2. DB-backed row-claim retry tests using `FOR UPDATE SKIP LOCKED` after retry is fixed on `main`.
3. Sender-worker provider-send guard tests after a test seam or processing lease exists.
4. Error-group switch-channel runtime test.
5. Active-load RabbitMQ restart test.
6. Frontend tests should be changed from `it.fails` to normal passing tests after demo-safety frontend fixes land.

## Recommended CI Pipeline

```bash
go test ./...
make lint
cd apps/frontend && npm ci && npm run build && npm test
python3 -m unittest discover -s tests -p 'test_*.py'
PYTHON=.venv312/bin/python make status-test
docker compose config
```

Optional nightly/runtime job:

```bash
docker compose up -d --build
python3 tests/runtime/runtime_smoke.py
```
