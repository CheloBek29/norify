# Mega Pre-Defense Fix

Date: 2026-05-16

## Branch and Commit

- Branch: `fix/main-critical-issues`
- Base commit: `1de7a1b feat: dynamic worker pool, RabbitMQ reconnect, worker health UI`
- Worktree: `/private/tmp/norify-origin-main-review`
- This branch is based on `main`, not `test`.

## Problems Targeted

1. Campaign-service RabbitMQ publisher did not reconnect after broker restart.
2. Campaign start already had the verified atomic idempotency fix and had to remain intact.
3. Campaign-level retry targeted synthetic first-N users instead of real failed rows.
4. Sender retry could resend but fail to update rows already marked `failed`.
5. Frontend could fake success/progress in fallback mode and allowed duplicate action clicks.
6. User picker looked like exact recipient selection even though backend dispatch is still count-based.
7. Main branch missed small demo/test support present in previous audit work.
8. Stale Adminer test/README references did not match the real pgweb Compose service.

## Files Changed

- `services/campaign-service/main.go`
- `services/campaign-service/main_test.go`
- `services/sender-worker/main.go`
- `services/sender-worker/main_test.go`
- `apps/frontend/src/App.tsx`
- `apps/frontend/src/App.test.tsx`
- `apps/frontend/src/api.ts`
- `apps/frontend/src/styles.css`
- `apps/status-service/requirements-dev.txt`
- `docker-compose.demo.yml`
- `Makefile`
- `README.md`
- `tests/test_postgres_viewer.py`
- `docs/fixes/mega_pre_defense_fix.md`
- `docs/main_fix_plan.md`

## What Was Fixed

### Campaign-Service RabbitMQ Publisher Reconnect

`campaign-service` now uses the same reconnect pattern as dispatcher/sender:

- starts a reconnect-managed publisher loop via `appruntime.RunWithReconnect`;
- stores the active publisher channel behind a mutex;
- marks readiness false until Postgres and the publisher channel are available;
- resets/closes the publisher channel on publish failure so future publishes reconnect;
- logs publisher connection and publish failures with operation names.

Affected publish paths:

- campaign start dispatch publish;
- campaign progress events;
- error-group retry publishes;
- switch-channel retry publishes;
- error-group status events.

Runtime proof is still missing because the Docker daemon socket is not accessible in this environment.

### Row-Based Campaign Retry

`POST /campaigns/{id}/retry-failed` no longer retries a synthetic `FailedCount` audience. It now:

- selects actual failed `message_deliveries` rows for the campaign;
- marks those exact rows as `queued`;
- republishes send jobs for those exact `user_id/channel_code/idempotency_key` values;
- restores rows to `failed` if publishing fails before all retry jobs are queued.

This is a demo-safe improvement over count-based retry. It is not a full delivery-attempt model.

### Sender Retry Result Updates

`sender-worker` now allows a higher-attempt retry result to update an existing `failed` delivery row. Terminal `sent` and `cancelled` rows remain protected from later updates.

Counter updates now distinguish:

- new or queued delivery completion;
- failed -> sent repair after a higher attempt;
- stale same-attempt retry results, which are ignored.

### Frontend Truthfulness and Double-Click Safety

Frontend campaign actions now:

- disable Start/Stop/Cancel/Retry/Archive buttons while the backend command is pending;
- update campaign/error-group state only after WebSocket/backend confirmation;
- show a visible fallback warning when backend is unavailable;
- stop local fallback progress from simulating delivery success;
- record backend command failures as errors instead of silently mutating UI state.

The auth token decoder now supports the backend's two-part `payload.signature` token format instead of assuming JWT-style three-part tokens.

### User Picker Honest Degradation

Exact user selection is not made real end-to-end in this pass because that needs a backend recipient/job data model. The UI now labels selected users as demo sampling and uses selected-user count plus global campaign channels, matching what the backend can actually receive today.

### Demo/Test Support

- Added `docker-compose.demo.yml`:
  - maps Postgres to host `15432` to avoid local `5432` conflicts;
  - configures existing sender-worker dynamic pool to 5 workers.
- Added Makefile demo targets:
  - `demo-up`
  - `demo-down`
  - `demo-ps`
  - `demo-logs`
  - `demo-metrics`
- Added `apps/status-service/requirements-dev.txt`.
- Updated stale pgweb test and README PostgreSQL viewer docs.

## What Was Partially Fixed

- RabbitMQ reconnect is code-fixed for future campaign-service publishes, but not runtime-proven after broker restart.
- Retry now targets real failed rows and higher attempts can repair failed rows, but there is still no durable delivery-attempt table or transactional outbox.
- User picker is safely degraded, not made exact end-to-end.
- Demo Compose avoids the Postgres host-port conflict, but does not prove runtime health in this environment.

## What Was Intentionally Not Fixed

- No full outbox pattern for the campaign start DB-transition-to-RabbitMQ publish gap.
- No dispatcher fan-out idempotency table/checkpoint.
- No exact selected-recipient persistence or exact user/channel fan-out.
- No production auth/RBAC hardening.
- No Prometheus/Grafana or large observability stack.
- No 50k benchmark run.

## Tests Run

### Go

```bash
gofmt -w services/campaign-service/main.go services/campaign-service/main_test.go services/sender-worker/main.go services/sender-worker/main_test.go
go test ./services/campaign-service
go test ./services/sender-worker
go test ./services/dispatcher-service
go test ./...
make lint
```

Results:

- `go test ./services/campaign-service`: PASS
- `go test ./services/sender-worker`: PASS
- `go test ./services/dispatcher-service`: PASS
- `go test ./...`: PASS
- `make lint`: PASS

### Frontend

```bash
cd apps/frontend
npm ci
npm run build
npm test
```

Results:

- `npm ci`: PASS
- `npm run build`: PASS
- `npm test`: PASS, 22 tests passed

### Python

```bash
python3 -m unittest discover -s tests -p 'test_*.py'
python3.12 -m venv .venv312
.venv312/bin/python -m pip install -r apps/status-service/requirements-dev.txt
PYTHON=.venv312/bin/python make status-test
PYTHON=.venv312/bin/python make test
```

Results:

- root unittest: PASS, 2 tests passed
- status-service pytest: PASS, 8 tests passed, 1 warning
- `PYTHON=.venv312/bin/python make test`: PASS

Notes:

- Python 3.13 could not install pinned `pydantic-core==2.18.4` because that version does not support Python 3.13.
- Python 3.12.13 was available and was used for the status-service test venv.

### Compose

```bash
docker compose config
docker compose -f docker-compose.yml -f docker-compose.demo.yml config
make -n demo-up
make -n demo-metrics
docker compose ps
```

Results:

- `docker compose config`: PASS
- `docker compose -f docker-compose.yml -f docker-compose.demo.yml config`: PASS
- `make -n demo-up`: PASS
- `make -n demo-metrics`: PASS
- `docker compose ps`: FAIL, Docker daemon socket permission denied:
  `permission denied while trying to connect to the docker API at unix:///Users/lisix/.docker/run/docker.sock`

## Tests Skipped and Why

- Full Docker runtime startup was not run because the Docker daemon socket was inaccessible.
- RabbitMQ restart runtime verification was not run for the same reason.
- Active campaign/fault/benchmark verification was not run.
- 50k benchmark was intentionally not run.

## Runtime Verification Status

Not runtime-proven in Docker in this environment.

Code-level checks and unit/build tests pass. Final runtime proof still must be done on a machine with Docker daemon access:

```bash
make demo-up
make demo-ps
make demo-metrics
```

Then verify RabbitMQ recovery:

```bash
docker compose -f docker-compose.yml -f docker-compose.demo.yml restart rabbitmq
# after rabbitmq is healthy, start a new campaign without restarting campaign-service
docker compose -f docker-compose.yml -f docker-compose.demo.yml logs --tail=100 campaign-service dispatcher-service sender-worker
```

## Remaining P0/P1 Risks

1. Crash gap remains: campaign can transition to `running` and then process can crash before dispatch publish.
2. Dispatcher fan-out is still not DB-idempotent if dispatcher crashes/requeues mid-window.
3. Exact selected users are not persisted or dispatched; UI now says this honestly.
4. No delivery-attempt table; retry correctness is improved but still not production-grade.
5. Docker runtime and RabbitMQ restart recovery are not proven in this environment.
6. Sender/provider exactly-once delivery is still not guaranteed after worker crash around provider call.
7. Auth/RBAC remains demo-level.
8. Metrics remain shallow on `main`.

## Demo Safety Notes

Safe after this pass, assuming Docker runtime is healthy:

- create one campaign;
- start once;
- show fallback warning if backend is down;
- use demo override with worker pool configured to 5;
- retry failed rows only with the row-based backend path.

Still avoid live demo:

- RabbitMQ restart unless verified on the demo machine;
- dispatcher restart during active campaign;
- exact individual user delivery claims;
- production security claims;
- 50k actual-run claims.

## Exact Commands for Final Verification

```bash
git status
git diff --check
go test ./...
make lint
cd apps/frontend && npm ci && npm run build && npm test
PYTHON=.venv312/bin/python make test
docker compose config
docker compose -f docker-compose.yml -f docker-compose.demo.yml config
make demo-up
make demo-ps
make demo-metrics
```
