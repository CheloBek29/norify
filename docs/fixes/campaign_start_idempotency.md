# Campaign Start Idempotency Fix

## Problem

`POST /campaigns/{id}/start` used a read-check-update-publish sequence. Two concurrent requests could both read a startable status, both update the campaign to `running`, and both publish dispatch work.

## Root Cause

The old code checked startability in application memory before writing the new status. The database update did not require the previous status to still be startable, so the status check was not atomic.

## Files Changed

- `services/campaign-service/main.go`
- `services/campaign-service/main_test.go`
- `docs/fixes/campaign_start_idempotency.md`

## Behavior Before

- `created` and `stopped` campaigns could be started.
- Sequential duplicate starts were partially blocked because the second request usually saw `running`.
- Parallel duplicate starts could both pass the pre-update status check and both publish dispatch.
- Dispatch publish was not tied to whether the database transition actually won.

## Behavior After

- Campaign start uses one atomic database transition:
  - `created` or `stopped` can transition to `running`.
  - `running`, `retrying`, `finished`, and `cancelled` do not transition.
- Dispatch is published only after the `UPDATE ... WHERE status IN (...) RETURNING ...` returns a campaign row.
- If the transition returns no row, the handler verifies the campaign exists and returns `409 campaign_not_startable` without publishing dispatch.
- If dispatch publish fails after a successful transition, the existing rollback path restores the previous status and `started_at`.

## Tests Run

Attempted in this worktree:

- `gofmt -w services/campaign-service/main.go services/campaign-service/main_test.go`
  - Result: failed, `gofmt` is not installed on the host.
- `go test ./services/campaign-service`
  - Result: failed, `go` is not installed on the host.
- `go test ./...`
  - Result: failed, `go` is not installed on the host.
- Dockerized fallback:
  - Result: failed, Docker daemon socket is unavailable:
    `dial unix /Users/lisix/.docker/run/docker.sock: connect: no such file or directory`.

Required checks once Go or Docker is available:

```bash
go test ./services/campaign-service
go test ./...
```

If Go is unavailable on the host, run a Dockerized Go test when Docker is available:

```bash
docker run --rm -v "$PWD":/repo -w /repo golang:1.22-alpine sh -c "go test ./services/campaign-service && go test ./..."
```

## Remaining Risks

- This fix prevents duplicate dispatch from concurrent campaign start requests, but dispatcher fan-out is still not idempotent if the dispatcher crashes mid-window.
- Campaign-service RabbitMQ publisher still needs reconnect hardening.
- Retry correctness is still separate and not fixed here.
