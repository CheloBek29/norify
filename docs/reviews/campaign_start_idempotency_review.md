# Campaign Start Idempotency Review

## Verdict

PASS

The campaign start fix is safe to commit for the narrow P0 it targets. The start path now uses a database-gated state transition before publishing dispatch, dispatch is only published when that transition wins, and the campaign-service plus full Go test suite pass after formatting.

## Exact Evidence

Reviewed files:

- `services/campaign-service/main.go`
- `services/campaign-service/main_test.go`
- `docs/fixes/campaign_start_idempotency.md`

Relevant code:

- `startCampaign` now calls `transitionCampaignToRunning` before publishing dispatch.
- `transitionCampaignToRunning` uses a single SQL statement with:
  - `SELECT ... WHERE id = $1 AND status IN ($4, $5) FOR UPDATE`
  - `UPDATE campaigns ... FROM candidate`
  - `RETURNING ...`
- Dispatch is only published after `started == true`.
- If the transition returns no row, the code verifies campaign existence via `getCampaign` and returns `409 campaign_not_startable`.

## Tool Installation / Usage Path

1. Host Go was checked first and was missing:

```bash
go version
gofmt -w services/campaign-service/main.go services/campaign-service/main_test.go
go test ./services/campaign-service
go test ./...
```

Initial result:

- `go`: `zsh:1: command not found: go`
- `gofmt`: `zsh:1: command not found: gofmt`

2. Dockerized Go fallback was attempted:

```bash
docker run --rm -v /private/tmp/norify-origin-main-review:/repo -w /repo golang:1.22-alpine sh -c "gofmt -w services/campaign-service/main.go services/campaign-service/main_test.go && go test ./services/campaign-service && go test ./..."
```

Result:

- failed because the Docker daemon socket was not accessible:
  `permission denied while trying to connect to the docker API at unix:///Users/lisix/.docker/run/docker.sock`

3. Homebrew was available and was used to install Go:

```bash
brew --version
brew install go
```

Result:

- `Homebrew 5.1.11`
- installed Go `1.26.3` at `/opt/homebrew/Cellar/go/1.26.3`

Final verification commands:

```bash
go version
gofmt -w services/campaign-service/main.go services/campaign-service/main_test.go
go test ./services/campaign-service
go test ./...
git diff --check
```

Final results:

- `go version`: passed, `go version go1.26.3 darwin/arm64`
- `gofmt -w services/campaign-service/main.go services/campaign-service/main_test.go`: passed
- `go test ./services/campaign-service`: passed
  - `ok  	github.com/norify/platform/services/campaign-service	0.426s`
- `go test ./...`: passed
  - `ok  	github.com/norify/platform/packages/go-common/auth	0.768s`
  - `ok  	github.com/norify/platform/packages/go-common/campaigns	0.385s`
  - `ok  	github.com/norify/platform/packages/go-common/channels	1.158s`
  - `ok  	github.com/norify/platform/packages/go-common/reliability	1.532s`
  - `ok  	github.com/norify/platform/packages/go-common/runtime	2.368s`
  - `ok  	github.com/norify/platform/packages/go-common/templates	2.002s`
  - `ok  	github.com/norify/platform/packages/go-common/users	2.764s`
  - `ok  	github.com/norify/platform/services/campaign-service	(cached)`
  - `ok  	github.com/norify/platform/services/dispatcher-service	3.114s`
  - `ok  	github.com/norify/platform/services/sender-worker	3.488s`
- `git diff --check`: passed

## Review Questions

### 1. Does campaign start now use a truly atomic DB transition?

Yes, at the SQL state transition level.

The new path moves the startability predicate into the database statement. Under PostgreSQL `READ COMMITTED`, `SELECT ... FOR UPDATE` will lock or wait on the candidate row. If another transaction changes the status from `created`/`stopped` to `running`, the waiting statement should re-check the predicate and return no candidate row.

### 2. Can two parallel `POST /campaigns/{id}/start` calls still publish dispatch twice?

At the campaign-service start transition boundary: no.

Only the request whose SQL statement returns an updated row proceeds to `publishDispatch`. The losing request receives `started == false` and returns `409` before publish.

Remaining duplicate risk outside this fix:

- Dispatcher fan-out is still not idempotent if dispatcher crashes/requeues after partial publish.
- RabbitMQ publisher reconnect is still separate.
- These are not part of this review.

### 3. Is dispatch published only after `UPDATE ... RETURNING` wins?

Yes.

`publishDispatch` is called only after:

```go
campaign, rollback, started, err := transitionCampaignToRunning(...)
...
if !started {
    return 409
}
publishDispatch(...)
```

### 4. Are non-startable statuses handled correctly?

Yes for the statuses covered by the code and tests.

`running`, `retrying`, `finished`, and `cancelled` do not satisfy the SQL `status IN ('created', 'stopped')` predicate, so no transition occurs and dispatch is not published.

The helper then calls `getCampaign` to distinguish "not found" from "exists but not startable":

- missing campaign should still return lookup error / 404 path;
- existing non-startable campaign returns conflict.

### 5. Is rollback after publish failure safe?

Mostly same safety as before, with one improvement.

Improvement:

- The code now captures previous status and previous `started_at` from the locked candidate row, so rollback can restore `created` vs `stopped` correctly.

Remaining caveat:

- Rollback still only checks `WHERE id = $1 AND status = running`.
- If another valid action changes status after the start transition and before publish failure rollback, the rollback will not apply if the status is no longer `running`. That is acceptable for this narrow fix and avoids clobbering `stopped`/`cancelled`.
- There is still no outbox pattern, so a process crash after DB transition but before publish can leave a campaign running without dispatch. That existed before and is outside this P0 duplicate-start fix.

### 6. Are tests meaningful?

Partially meaningful, and now verified to compile/pass.

Good:

- `TestCanStartCampaign` now explicitly checks `created`, `stopped`, `running`, `retrying`, `finished`, and `cancelled`.
- The whole Go repository test suite passes with the new code.

Remaining test gap:

- No database-backed test covers `transitionCampaignToRunning`.
- No test directly proves two concurrent HTTP requests result in exactly one publish.
- No test validates rollback with the new previous-state capture.

Recommendation:

- Add a database-backed campaign-service test using a test Postgres or sqlmock-style abstraction if introduced later.
- At minimum, add an integration test around two parallel start requests once Docker/test DB is available.

### 7. Could the new code break existing API responses?

Low risk.

Response behavior should remain mostly compatible:

- successful start still returns the campaign after publish;
- non-startable campaign still returns `409 {"error":"campaign_not_startable"}`;
- missing campaign still follows `writeLookupError`.

Possible compatibility issue:

- `transitionCampaignToRunning` duplicates part of `scanCampaign` logic for the pre-publish campaign object. The successful HTTP response is re-read through `getCampaign`, so the API response itself should remain consistent. The duplicated scan mainly affects the campaign passed to `publishDispatch`, specifically selected channels and total recipient/message fields.

### 8. Are there compile/gofmt issues?

No.

`gofmt`, `go test ./services/campaign-service`, `go test ./...`, and `git diff --check` all pass after installing Go through Homebrew.

## Duplicate Dispatch From Parallel Start

Code-level verdict: fixed for the campaign-service start handler.

The losing parallel request should not publish dispatch because the database transition returns no row after the first request changes status to `running`.

Runtime integration verdict: not directly load-tested with two real HTTP requests against Postgres in this verification pass.

Unit/compile verdict: verified.

## New Risks Introduced

1. Duplicated campaign scanning logic.
   - `transitionCampaignToRunning` manually scans fields instead of using `scanCampaign`.
   - This is not immediately dangerous, but it can drift if `Campaign` scan fields change later.

2. Crash gap remains.
   - If campaign-service crashes after DB transition but before dispatch publish, campaign can be left `running` without dispatch.
   - This is an outbox/reconnect problem and should be the next architectural fix, not part of this one.

## Safe to Commit/Merge?

Yes, for this specific fix.

The patch is formatted and the Go test suite passes. It addresses duplicate dispatch from parallel campaign start at the campaign-service boundary without touching unrelated RabbitMQ, retry, frontend, metrics, or performance code.

## Next Recommended Fix

Fix campaign-service RabbitMQ publisher reconnect and/or outbox behavior.

Reason:

- This idempotency fix prevents duplicate dispatch from parallel starts.
- The next risk is the opposite failure mode: campaign transitions to `running`, but dispatch publish fails or RabbitMQ restarts and campaign-service cannot recover cleanly.
