# Main Critical Issues Fix Plan

Date: 2026-05-16

## Branch and Base

| Item | Value |
|---|---|
| Current working branch | `fix/main-critical-issues` |
| Base branch | latest `origin/main` |
| Base commit | `1de7a1b feat: dynamic worker pool, RabbitMQ reconnect, worker health UI` |
| Based on `test`? | No |
| Used `test` as merge base? | No |
| Local dirty `test` work preserved? | Yes; this branch was created in a separate worktree from `origin/main` |

This branch intentionally starts from the real product branch, `main`, not from the audit/metrics experiment branch, `test`.

Confirmed teammate commits included:

- `1de7a1b feat: dynamic worker pool, RabbitMQ reconnect, worker health UI`
- `6e4bf03 feat: user picker, deliveries overhaul, topbar cleanup`
- `4395742 fix for seva`
- `bf1efbf fix: campaign creation, error groups persistence, channel update normalization`

Source review for this plan:

- `docs/critical_issues_review_after_updates.md`

## Top P0 Issues

1. Campaign start is still not atomically idempotent.
   - Sequential double-start is partially blocked, but parallel start can still duplicate dispatch.
   - Evidence: `services/campaign-service/main.go` reads campaign status before updating, and the `UPDATE` does not restrict the old status.

2. Campaign-service RabbitMQ publisher still does not reconnect.
   - Dispatcher and sender-worker gained reconnect loops, but campaign-service still opens RabbitMQ once.
   - Evidence: `services/campaign-service/main.go` uses one-time `OpenRabbit()` and stores global `mq`.

3. Retry correctness is still unsafe.
   - Automatic retry likely cannot update rows already marked `failed`.
   - Campaign-level retry still retries synthetic first-N users instead of actual failed rows.

4. Frontend can still show fake/fallback progress and lacks pending locks.
   - Backend-down fallback can look like real delivery.
   - Start/retry buttons can still send duplicate commands.

5. User picker / individual recipient selection is not wired end-to-end.
   - Frontend picker uses mock users and selected IDs are not sent to campaign-service.
   - Dispatcher still generates synthetic `user-00001...` recipients.

6. Metrics and performance tooling should wait until core correctness is fixed.
   - Current `main` has shallow `/metrics` and no restored performance pack.
   - Meaningful benchmarks require correct idempotency/retry/recipient behavior first.

## Exact Fix Order

## Status After Mega Pre-Defense Fix Pass

| Priority item | Status | Evidence | Remaining work |
|---|---|---|---|
| 1. Campaign start idempotency | FIXED | `services/campaign-service/main.go` uses an atomic DB transition before dispatch publish; `go test ./services/campaign-service` and `go test ./...` pass. | Add a Docker/Postgres-backed concurrent HTTP integration test when runtime is available. |
| 2. RabbitMQ reconnect in campaign-service publisher | PARTIAL | `campaign-service` now has a reconnect-managed publisher channel and resets publisher state after publish failure; Go tests pass. | Runtime-proof RabbitMQ restart on a machine with Docker daemon access; outbox crash gap remains open. |
| 3. Retry correctness | PARTIAL / DEMO VERIFIED | `retry-failed` now atomically claims real failed rows with `UPDATE ... RETURNING`; sender-worker skips terminal/stale rows before provider send; forced-failure runtime retry repaired 2 failed rows to 2 sent rows at attempt 2. | Add delivery-attempt table, outbox, and active-load retry concurrency tests before production claims. |
| 4. Frontend fallback truthfulness and pending states | FIXED for demo safety | UI no longer applies optimistic campaign/error-group mutations without backend confirmation; pending buttons lock; fallback banner is visible; frontend build and 22 tests pass. | Production auth/RBAC and backend-down workflow still need real product decisions. |
| 5. Real user picker / recipient selection correctness | PARTIAL / SAFELY DEGRADED | UI now labels selected users as demo sampling and uses count plus global channels, matching the backend contract. | Implement exact selected recipient/channel persistence and dispatcher fan-out later. |
| 6. Metrics/performance after core logic | NOT ATTEMPTED | Only demo metrics curl target was added for existing shallow endpoints. | Restore honest metrics/performance pack after runtime proof and core delivery integration tests. |

## Second Focused Hardening Pass

Additional changes after the mega review:

- Retry rows are claimed atomically before publish using a transaction plus `FOR UPDATE SKIP LOCKED` and `UPDATE ... RETURNING`.
- Sender-worker now checks existing delivery state before calling the provider stub and skips terminal/stale deliveries.
- Campaign-level switch-channel is disabled in backend and hidden in frontend because the previous endpoint still used synthetic dispatch.
- Runtime forced-failure retry was verified:
  - campaign `cmp-319eca0e952605c1` failed 2/2 messages with email success probability set to `0`;
  - two parallel `/retry-failed` requests were issued after email was restored to success probability `1`;
  - final result was exactly 2 delivery rows, both `sent`, both `attempt=2`, with no visible duplicate rows.

Remaining high-risk production gaps:

- No transactional outbox for dispatch/retry publish.
- No full provider exactly-once guarantee if duplicate messages for a new idempotency key arrive concurrently.
- No exact selected-user backend fan-out.

Checks completed in this pass:

- `go test ./services/campaign-service`: PASS
- `go test ./services/sender-worker`: PASS
- `go test ./services/dispatcher-service`: PASS
- `go test ./...`: PASS
- `make lint`: PASS
- `cd apps/frontend && npm ci && npm run build && npm test`: PASS
- `PYTHON=.venv312/bin/python make test`: PASS
- `docker compose config`: PASS
- `docker compose -f docker-compose.yml -f docker-compose.demo.yml config`: PASS
- Docker runtime: BLOCKED by daemon socket permission.

### 1. Campaign Start Idempotency

Goal: starting the same campaign twice, including two parallel requests, publishes dispatch exactly once.

Likely files:

- `services/campaign-service/main.go`
- `services/campaign-service/main_test.go`
- possibly `migrations/001_init.sql` if a durable dispatch marker is added

Implementation outline:

- Replace read-then-update start logic with atomic database transition:
  - `UPDATE campaigns SET status='running', started_at=COALESCE(started_at, now()), updated_at=now() WHERE id=$1 AND status IN ('created','stopped') RETURNING ...`
- Publish dispatch only if the update returns a row.
- Return `409 campaign_not_startable` or a no-op response for already-running/finished/cancelled campaigns.
- Avoid publishing dispatch before the DB transition is confirmed.

Checks after fix:

- `go test ./services/campaign-service`
- `go test ./...`
- Manual or integration check:
  - create campaign;
  - send two parallel `POST /campaigns/{id}/start`;
  - verify only one dispatch and expected message count.

### 2. RabbitMQ Reconnect in Campaign-Service Publisher

Goal: campaign-service can publish start/retry/progress events after RabbitMQ restarts without restarting campaign-service.

Likely files:

- `services/campaign-service/main.go`
- `packages/go-common/runtime/reconnect.go`
- `packages/go-common/runtime/env.go`

Implementation outline:

- Add a reconnect-managed publisher channel in campaign-service, similar to sender-worker `pubCh`.
- Protect the publisher channel with a mutex.
- Clear the channel when reconnect loop exits or broker connection closes.
- Make readiness reflect actual publisher availability where appropriate.
- Ensure topology is re-declared on reconnect.

Checks after fix:

- `go test ./services/campaign-service`
- `go test ./...`
- Runtime check with Docker:
  - start stack;
  - start one campaign;
  - restart RabbitMQ;
  - start another campaign without restarting campaign-service;
  - inspect logs for reconnect and successful publish.

### 3. Retry Correctness

Goal: retries target the correct failed deliveries and can update final delivery state correctly.

Likely files:

- `services/campaign-service/main.go`
- `services/sender-worker/main.go`
- `services/sender-worker/main_test.go`
- `migrations/001_init.sql` if delivery attempts are modeled

Implementation outline:

- Fix automatic retry:
  - allow a higher-attempt retry to update an existing failed row, or introduce a `delivery_attempts` table and derive final state.
- Fix campaign-level retry:
  - query actual `message_deliveries WHERE campaign_id=$1 AND status='failed'`;
  - republish exact failed user/channel/idempotency rows;
  - do not use `FailedCount` as synthetic recipient count.
- Keep error-group retry row-based and add tests around it.

Checks after fix:

- `go test ./services/sender-worker`
- `go test ./services/campaign-service`
- Integration/manual checks:
  - force failures;
  - retry one error group;
  - retry all campaign failures;
  - verify only failed row identities are retried;
  - verify final status changes after successful retry.

### 4. Frontend Fallback Truthfulness and Pending States

Goal: UI cannot accidentally fake backend success, and user actions cannot double-submit campaign mutations.

Likely files:

- `apps/frontend/src/App.tsx`
- `apps/frontend/src/api.ts`
- `apps/frontend/src/App.test.tsx`
- `apps/frontend/src/styles.css`

Implementation outline:

- Add visible "local fallback / simulated data" banner when backend/WebSocket commands fail.
- Do not present fallback progress as real delivery evidence.
- Add per-campaign pending action state for start/retry/cancel/archive/error-group actions.
- Disable Start/Retry buttons immediately while request is pending.
- Fix backend token decoding or remove silent login fallback for real backend mode.

Checks after fix:

- `cd apps/frontend && npm run build`
- `cd apps/frontend && npm test`
- UI/manual checks:
  - backend down shows obvious fallback warning;
  - double-click Start sends one command;
  - failed login does not silently hide real auth breakage.

### 5. Real User Picker / Recipient Selection Correctness

Goal: individual selected users and per-user channel choices are honored by backend dispatch.

Likely files:

- `apps/frontend/src/App.tsx`
- `apps/frontend/src/api.ts`
- `services/user-service/main.go`
- `services/campaign-service/main.go`
- `services/dispatcher-service/main.go`
- `packages/contracts/contracts.go`
- `migrations/001_init.sql`

Implementation outline:

- Add server-side user search endpoint with query, limit, and offset.
- Add campaign create contract for exact selected recipients, for example:
  - `specific_recipients: [{ user_id, channels }]`
- Persist selected recipient/channel pairs, preferably in a campaign delivery job table.
- Dispatcher should dispatch exact rows when specific recipients exist.
- Keep segment/bulk selection separate and deduplicate overlap.

Checks after fix:

- `go test ./services/user-service`
- `go test ./services/campaign-service`
- `go test ./services/dispatcher-service`
- Frontend build/tests.
- Manual check:
  - select two specific users with different channel sets;
  - start campaign;
  - verify DB delivery rows exactly match selected pairs.

### 6. Metrics and Performance After Core Logic

Goal: only after delivery correctness is stable, restore useful metrics and benchmark tooling on top of `main`.

Likely files:

- `Makefile`
- `packages/go-common/httpapi/server.go`
- `packages/go-common/runtime/env.go`
- `services/campaign-service/main.go`
- `services/dispatcher-service/main.go`
- `services/sender-worker/main.go`
- `apps/status-service/main.py`
- `tests/smoke/test_metrics_endpoints.py`
- `tests/performance/benchmark_campaign.py`

Implementation outline:

- Restore Prometheus-style metrics for delivery counts, queue state, dependency state, retry/DLQ where honest.
- Restore strict `metrics-test`.
- Restore `perf-test-fast` only after it can verify worker runtime config.
- Re-run L1/L2 after idempotency and recipient fixes.

Checks after fix:

- `docker compose config`
- `make metrics-test`
- `make -n perf-test-fast`
- Runtime metrics curls:
  - `curl http://localhost:8085/metrics`
  - `curl http://localhost:8086/metrics`
  - `curl http://localhost:8087/metrics`
  - `curl http://localhost:8090/metrics`
- L1/L2 benchmark only after stack is healthy.

## General Check Sequence

Run after each backend change where possible:

```bash
go test ./...
python3 -m unittest discover -s tests -p 'test_*.py'
docker compose config
```

Run after each frontend change:

```bash
cd apps/frontend
npm run build
npm test
```

Run before any demo claim:

```bash
docker compose up -d --build
docker compose ps
curl -s http://localhost:8085/health/ready
curl -s http://localhost:8086/health/ready
curl -s http://localhost:8087/health/ready
curl -s http://localhost:8090/health/ready
```

Do not run RabbitMQ/Postgres live fault demos until campaign-service reconnect and idempotency are fixed and runtime-tested.
