# Critical Issues Review After Teammate Updates

Date: 2026-05-16  
Primary checkout: `/Users/lisix/Desktop/norify`  
Review worktree for teammate code: `/private/tmp/norify-origin-main-review`  
Baseline review: `docs/critical_issues_review.md`

This report is for the programmer. It is not a presentation guide.

## 1. Executive Summary

- Current local branch is `test` at `60711aa wowfix`.
- `origin/test` is also still at `60711aa`; the teammate updates are **not on `test`**.
- The reported teammate changes are on `origin/main`, latest commit `1de7a1b feat: dynamic worker pool, RabbitMQ reconnect, worker health UI`.
- New commits detected on `origin/main` since the previous reviewed base:
  - `bf1efbf fix: campaign creation, error groups persistence, channel update normalization`
  - `4395742 fix for seva`
  - `6e4bf03 feat: user picker, deliveries overhaul, topbar cleanup`
  - `1de7a1b feat: dynamic worker pool, RabbitMQ reconnect, worker health UI`
- I did **not** pull or merge those commits into local `test` because the working tree is dirty with many uncommitted product/docs/metrics changes.
- I reviewed teammate code in a detached temporary worktree at `origin/main@1de7a1b`.
- RabbitMQ reconnect is **partially fixed** for dispatcher/sender consumers and status-service, but **not fixed for campaign-service publishing**, and active-load duplicate/loss behavior was not runtime-verified.
- User picker / individual recipient selection is **mostly frontend-only**. It does not send exact selected user IDs/channels to campaign-service/dispatcher, so it does not prove real per-user recipient selection.
- Biggest remaining P0/P1 risks:
  - teammate fixes are on the wrong branch for the requested `test` branch;
  - campaign start is still race-prone and can duplicate dispatch;
  - campaign retry still targets synthetic first-N users;
  - automatic retry still likely cannot repair failed rows;
  - campaign-service RabbitMQ publisher does not reconnect;
  - dispatcher fan-out remains non-idempotent;
  - user picker selection is not honored by backend dispatch;
  - frontend fallback can still fake backend success;
  - unauthenticated WebSocket/API mutation paths remain;
  - metrics/performance pack from `test` is missing/regressed on `origin/main`.
- Safe to demo only with discipline:
  - create one campaign;
  - start once;
  - use default segment/channel setup;
  - do not claim exact recipient selection unless fixed;
  - do not demo RabbitMQ restart live.
- Must not demo yet:
  - double-click start;
  - campaign-level retry as correct;
  - exact individual-user fan-out;
  - RabbitMQ restart under active load;
  - production security;
  - full 50k benchmark from this branch without rerunning.

## 2. Git and Runtime Status

### Primary checkout state

Commands run:

```bash
git status
git branch --show-current
git remote -v
git log --oneline -5
git fetch --all --prune
git branch -a
git log --oneline --decorate --graph --all -20
```

Result:

| Item | Value |
|---|---|
| Current branch | `test` |
| Latest local commit | `60711aa wowfix` |
| `origin/test` | `60711aa wowfix` |
| `origin/main` | `1de7a1b feat: dynamic worker pool, RabbitMQ reconnect, worker health UI` |
| Relationship | `test...origin/main` = `0 4`; `origin/main` is 4 commits ahead of `test` |
| Teammate changes present on local `test`? | No |
| Teammate changes present on remote branch? | Yes, on `origin/main` |
| Pull attempted? | No `git pull` into `test`, because working tree is dirty and `origin/test` did not contain teammate fixes |
| Safe review method | Detached worktree at `/private/tmp/norify-origin-main-review` from `origin/main` |

Dirty files in primary checkout before this review:

```text
 M .DS_Store
 M Makefile
 M README.md
 M apps/status-service/main.py
 M apps/status-service/tests/test_status_service.py
 M packages/go-common/httpapi/server.go
 M packages/go-common/runtime/env.go
 M services/campaign-service/main.go
 M services/dispatcher-service/main.go
 M services/sender-worker/main.go
 M tests/test_postgres_viewer.py
?? docker-compose.demo.yml
?? docs/
?? services/.DS_Store
?? services/campaign-service/metrics_test.go
?? services/dispatcher-service/metrics_test.go
?? services/sender-worker/metrics_test.go
?? tests/performance/
?? tests/smoke/test_metrics_endpoints.py
```

Files changed by this review:

```text
docs/critical_issues_review_after_updates.md
```

### Teammate diff summary

Command:

```bash
git diff --stat test..origin/main -- services apps packages tests docker-compose.yml Makefile README.md deploy migrations
```

Summary:

```text
25 files changed, 1691 insertions(+), 197 deletions(-)
```

Important changed files:

```text
apps/frontend/src/App.tsx
apps/frontend/src/api.ts
apps/frontend/src/App.test.tsx
apps/status-service/main.py
docker-compose.yml
migrations/001_init.sql
packages/go-common/runtime/env.go
packages/go-common/runtime/reconnect.go
services/campaign-service/main.go
services/dispatcher-service/main.go
services/sender-worker/main.go
services/template-service/main.go
services/*/Dockerfile
```

### Runtime/check status on teammate worktree

| Command | Result | Evidence / exact failure |
|---|---|---|
| `docker compose config` | PASS | Base Compose renders |
| `docker compose -f docker-compose.yml -f docker-compose.demo.yml config` | FAIL | `open /private/tmp/norify-origin-main-review/docker-compose.demo.yml: no such file or directory` |
| `docker compose ps` | FAIL | Docker daemon socket unavailable: `dial unix /Users/lisix/.docker/run/docker.sock: connect: no such file or directory` |
| `docker ps` | FAIL | Same Docker socket failure |
| `docker run --rm ... golang:1.22-alpine go test ./...` | FAIL | Same Docker socket failure |
| `go test ./...` | FAIL | `zsh:1: command not found: go` |
| `make test` | FAIL | `/bin/sh: go: command not found` |
| `make lint` | FAIL | `/bin/sh: go: command not found` |
| `python3 -m unittest discover -s tests -p 'test_*.py'` | FAIL | stale Adminer expectation: expected `adminer:4.8.1`, Compose uses `sosedoff/pgweb:latest` |
| `python3 -m pytest apps/status-service/tests` | FAIL | `No module named pytest` |
| `PYTHON=.venv/bin/python make metrics-test` | FAIL | `No rule to make target 'metrics-test'` on `origin/main` |
| `make -n perf-test-fast` | FAIL | `No rule to make target 'perf-test-fast'` on `origin/main` |
| `npm ci` in `apps/frontend` | PASS | 141 packages installed |
| `npm run build` in `apps/frontend` | PASS | Vite production build completed |
| `npm test` in `apps/frontend` | PASS | 1 file, 22 tests |
| `python3 -m py_compile apps/status-service/main.py` | PASS | no syntax error |
| `curl http://localhost:8085/metrics` | FAIL | HTTP code `000`, services not running |
| `curl http://localhost:8090/metrics` | FAIL | HTTP code `000`, services not running |

Docker-dependent benchmark and fault tests were not run because Docker was unavailable.

## 3. Previous P0/P1 Issue Status Matrix

| Previous issue | Old severity | Status now | Evidence | Remaining risk | Recommended next action |
|---|---|---|---|---|---|
| Campaign start is not idempotent and can duplicate dispatch work | P0 | PARTIALLY FIXED | `services/campaign-service/main.go:254` rejects non-startable statuses; but `:249-264` reads campaign then updates without atomic status condition and publishes | Two parallel starts can both read `created` and both publish | Make start an atomic `UPDATE ... WHERE status IN (...) RETURNING` and publish only if row changed |
| Dispatcher fan-out is not idempotent on crash/requeue | P0 | PARTIALLY FIXED | `services/dispatcher-service/main.go:122-145` windows fan-out and continuation; no durable job table/checkpoint | Crash before ACK can replay same window and duplicate jobs | Add DB-backed delivery jobs/outbox with unique user-channel key |
| Automatic sender retry likely cannot repair failed delivery rows | P0 | STILL OPEN | `services/sender-worker/main.go:407-415` still updates only when existing status is `queued` | Initial failed row stays failed after automatic retry with same key | Fix retry upsert for higher attempts or add attempts table |
| Campaign-level retry targets synthetic first-N users | P0 | STILL OPEN | `services/campaign-service/main.go:347-369` still uses `FailedCount`; dispatcher still generates `userID(i)` in `services/dispatcher-service/main.go:240` | Wrong users retried | Retry exact failed rows only |
| RabbitMQ restart/reconnect partially self-healing | P0 | PARTIALLY FIXED | `packages/go-common/runtime/reconnect.go`; dispatcher/sender consumers use it; campaign-service still uses one-time `OpenRabbit()` at `services/campaign-service/main.go:73-79` | Start/retry publishing can remain broken after broker restart; runtime not verified | Add reconnecting publisher in campaign-service and active-load RabbitMQ restart test |
| Frontend fallback can fake successful demo | P0 | STILL OPEN | `apps/frontend/src/App.tsx:212-237`, `:327-374`, `:403-440` still mutate local optimistic/fallback state | UI can show progress while backend is down | Add explicit offline banner and disable real-claim actions in fallback mode |
| `make perf-test-fast` can mislabel results | P0 | REGRESSED | On `origin/main`, Makefile has no `perf-test-fast` target at all | Performance pack unavailable on teammate branch | Restore benchmark targets and add runtime worker-env verification |
| Unauthenticated status ops WebSocket mutates state | P0 | STILL OPEN | `apps/status-service/main.py:167-181`, `:210-287` no auth | Any local browser/client can create/start campaigns and mutate config | Require token on WebSocket and mutating backend calls |
| Most backend APIs lack auth/RBAC | P1 | STILL OPEN | Services expose write endpoints directly; no auth middleware in campaign/channel/template/user services | Unauthorized local mutations | Protect mutating routes |
| Hardcoded credentials/secret/non-expiring token | P1 | STILL OPEN | `services/auth-service/main.go:24-27`; `packages/go-common/auth/auth.go` has no expiry | Demo-only auth; token never expires | Add expiring tokens and required secrets |
| Wildcard CORS and broad host exposure | P1 | STILL OPEN | `packages/go-common/httpapi/server.go:66-68`; `apps/status-service/main.py:117-122` | Cross-origin local attacks possible | Restrict origins in demo/prod |
| pgweb/RabbitMQ/Postgres/Redis exposed | P1 | STILL OPEN | `docker-compose.yml:11-12`, `:37-39`, `:55-56`, `:28-29` | Local admin surfaces exposed | Use profiles/override/no host ports for dependencies |
| Template service in-memory vs DB | P1 | PARTIALLY FIXED | `services/template-service/main.go:45-72` now load/save DB, but delete at `:198-200` only removes memory | Delete/update consistency still weak | Persist delete and make DB source of truth |
| User service/campaign audience mismatch | P1 | STILL OPEN | `services/user-service/main.go:12-29` in-memory; `campaign-service countAudience` at `:858-861` counts all DB users; frontend picker uses `MOCK_USERS` | Recipient selection not honored by backend | Add backend selected user contract and dispatcher support |
| Disabled/missing channels silently fallback | P1 | STILL OPEN | `services/sender-worker/main.go:376-389` returns default enabled config on DB miss/disabled | Disabled channel can still send | Treat disabled/missing channel as failed delivery |
| Main delivery path lacks durable queued/processing rows | P1 | STILL OPEN | `apps/status-service/main.py:379-388` has queued-job endpoint, but dispatcher does not call it; sender inserts terminal rows | In-flight state not durable | Dispatcher should create queued rows/jobs before publish |
| Worker crash before DB write can duplicate provider send | P1 | STILL OPEN | `services/sender-worker/main.go:306-313` sends before `writeDelivery` | Real provider duplicate after crash/redelivery | Check/claim idempotency before provider call |
| Worker crash after DB write before ACK can duplicate provider send | P1 | STILL OPEN | Provider send still happens before conflict check; ACK after processing | DB may be single-row while user gets duplicate external message | Skip provider call if terminal delivery already exists |
| DLQ not observable/proven in scaled mode | P1 | STILL OPEN | DLQ publish exists at `services/sender-worker/main.go:342-343`; no aggregate metric/test | Cannot prove DLQ behavior | Add DLQ metric/test |
| Sender metrics unavailable/not aggregated under replicas | P1 | STILL OPEN | `/worker/stats` at `services/sender-worker/main.go:159-167` is per-process; Compose maps `8087:8080`, cannot scale cleanly with host port | No aggregate worker view | Aggregate via DB/status service or remove host port and document |
| p95 global/cumulative | P1 | PARTIALLY FIXED | Frontend label is now `p95 enqueue`; dispatcher reports `p95_dispatch_ms`; but it is dispatch enqueue p95, not delivery latency p95 | Could still be misrepresented as delivery latency | Rename docs/UI to "dispatch enqueue p95" everywhere |
| Queue depth ready-only | P1 | PARTIALLY FIXED | Worker pool uses management API `QueueDepth`; dispatcher backpressure still uses `QueueInspect` at `services/dispatcher-service/main.go:180-185` | Queue semantics differ by code path | Expose ready/unacked/total separately |
| Status-service readiness/metrics shallow | P1 | STILL OPEN | `apps/status-service/main.py:130-138` static ready and only websocket count metric | Green health can hide dependency failure | Add dependency checks and real metrics |
| Metrics smoke test can pass by skipping endpoints | P1 | REGRESSED | `origin/main` has no `metrics-test` target | No automated metrics verification | Restore strict metrics smoke |
| Health smoke incompatible with scaled demo | P1 | STILL OPEN | `tests/smoke/health.sh` still checks `localhost:8087`; base Compose maps sender port | 5-worker scaling conflicts with port exposure | Add scaled-mode smoke path |
| Missing high-value integration tests | P1 | PARTIALLY FIXED | New unit tests exist, but only lifecycle/window/status helpers | No idempotency/retry/RabbitMQ active-load coverage | Add integration tests for core pipeline |
| Docker runtime unavailable in final review | P1 | NOT ENOUGH EVIDENCE | Docker still unavailable in this review | Runtime claims not freshly verified | Run on presenter machine |
| Compose weak restart/readiness | P1 | PARTIALLY FIXED | `restart: unless-stopped` and health `depends_on` added | App-level reconnect still partial | Verify cold start and dependency restarts |
| K8s not production-ready | P1 | STILL OPEN | `deploy/k8s/config.yaml` still has demo DSNs/secrets; manifests incomplete | Do not claim production K8s | Treat as scaffold |
| Frontend hardcodes localhost URLs | P1 | STILL OPEN | `apps/frontend/src/api.ts:311-320` | Non-local/browser-hosted demos fail | Move to env/proxy |
| Start/retry UI lacks pending/debounce | P1 | STILL OPEN | `apps/frontend/src/App.tsx:403-440`, `:819`, `:969-970` no pending lock | Duplicate clicks possible | Add per-campaign pending state |
| Benchmark evidence stale | P1 | REGRESSED | No `tests/performance` or `perf-test-fast` on `origin/main`; Docker unavailable | Cannot claim fresh benchmark on teammate branch | Restore scripts/targets and rerun L2/L3 |

## 4. New / Remaining Critical Issues

### ISSUE-1: Teammate fixes are on `origin/main`, not the required `test` branch

Severity: P0  
Category: devops/testing  
Status: confirmed

Summary: The project instruction says work on branch `test`, but the reported fixes are on `origin/main`. Local `test` and `origin/test` are still at `60711aa`. The dirty local `test` checkout also contains uncommitted previous audit/metrics changes, so blindly merging would be risky.

Evidence:
- `git branch --show-current` returned `test`.
- `git status` reported `Your branch is up to date with 'origin/test'`.
- `git log --oneline --decorate --graph --all -20` shows `origin/main` at `1de7a1b`, while `test`/`origin/test` remain at `60711aa`.
- `git rev-list --left-right --count test...origin/main` returned `0 4`.

Why it matters:
- Demo impact: the presenter may run `test` and not have the teammate fixes.
- Production impact: fixes and audit docs are split across divergent working-tree state and remote branches.

How to reproduce:
```bash
git checkout test
git log --oneline -1
git log --oneline test..origin/main
```

Expected behavior: The branch intended for defense contains the latest fixes or the team clearly switches the intended branch.

Actual behavior: Latest fixes are on `origin/main`; `test` is unchanged but dirty locally.

Root cause hypothesis: Teammate pushed to `main` instead of `test`.

Recommended fix: Decide the source of truth immediately. Either merge/cherry-pick `origin/main` into `test` after preserving local uncommitted work, or officially switch defense branch to `main`.

Minimal safe fix before demo: Create a clean integration branch from `test`, commit/stash local review artifacts safely, merge `origin/main`, resolve conflicts, then run checks.

Proper fix after demo: Protect branches and require PRs into the selected branch.

Verification after fix:
```bash
git branch --show-current
git log --oneline -5
git merge-base --is-ancestor 1de7a1b HEAD
```

Owner suggestion: devops/lead.

Demo decision: unsafe until branch/source-of-truth is clarified.

### ISSUE-2: RabbitMQ reconnect is incomplete because campaign-service publisher does not reconnect

Severity: P0  
Category: backend/reliability  
Status: confirmed

Summary: Dispatcher and sender-worker use the new reconnect helper, but campaign-service still opens RabbitMQ once at startup and stores a single `mq` channel. If RabbitMQ restarts, campaign start/retry publishing can remain broken until campaign-service restarts.

Evidence:
- Reconnect helper: `packages/go-common/runtime/reconnect.go:17`.
- Dispatcher uses it: `services/dispatcher-service/main.go:39-49`.
- Sender publisher and consumers use it: `services/sender-worker/main.go:43-54`, `:182`.
- Campaign-service still uses one-shot `OpenRabbit()` at `services/campaign-service/main.go:73-79`.
- `publishDispatch`, `requeueErrorGroup`, `switchErrorGroup`, and `publishCampaignProgress` all depend on global `mq`.

Why it matters:
- Demo impact: after RabbitMQ restart, the dashboard/service may look alive but new campaign start/retry can fail with `rabbitmq_unavailable` or closed-channel errors.
- Production impact: broker restarts are normal; publisher reconnect is mandatory.

How to reproduce:
1. Start stack.
2. Restart RabbitMQ.
3. Do not restart campaign-service.
4. Try `POST /campaigns/{id}/start` or error-group retry.
5. Check response/logs.

Expected behavior: campaign-service reconnects and publishes after broker restart.

Actual behavior: Code has no campaign-service reconnect loop.

Root cause hypothesis: Reconnect helper was applied to long-running consumers but not shared publishers.

Recommended fix: Add a reconnect-managed publisher channel in campaign-service, like sender-worker `pubCh`, protected by mutex and used by publish functions.

Minimal safe fix before demo: Do not live-demo RabbitMQ restart. If RabbitMQ restarts accidentally, restart campaign-service after broker is healthy.

Proper fix after demo: Use a publisher abstraction with reconnect, confirms, and topology re-declare for every service that publishes.

Verification after fix:
```bash
docker compose restart rabbitmq
curl -X POST http://localhost:8085/campaigns/<id>/start
docker compose logs --tail=100 campaign-service
```

Owner suggestion: backend.

Demo decision: avoid RabbitMQ restart.

### ISSUE-3: Campaign start still has a race and can duplicate dispatch

Severity: P0  
Category: backend/reliability  
Status: confirmed

Summary: The teammate change added `canStartCampaign`, which helps sequential duplicate starts after status changes. It does not make start atomic. Two parallel requests can both read `created`, both update, and both publish dispatch.

Evidence:
- `services/campaign-service/main.go:248-264`:
  - reads campaign with `getCampaign`;
  - checks `canStartCampaign(campaign.Status)`;
  - runs `UPDATE campaigns SET status = running ... WHERE id = $1` without a status condition;
  - publishes dispatch.
- Test `services/campaign-service/main_test.go:48-60` only tests allowed statuses, not concurrent starts.

Why it matters:
- Demo impact: double-click or WebSocket retry can duplicate campaign delivery.
- Production impact: no exactly-once start boundary.

How to reproduce:
```bash
# Run these nearly simultaneously after creating campaign.
curl -X POST http://localhost:8085/campaigns/<id>/start &
curl -X POST http://localhost:8085/campaigns/<id>/start &
wait
```
Then inspect queue/jobs/deliveries against expected `unique users x channels`.

Expected behavior: one start wins; the other returns conflict/no-op and does not publish.

Actual behavior: race window remains.

Root cause hypothesis: status check is in application memory, not in the database state transition.

Recommended fix: Replace read-then-update with atomic `UPDATE ... WHERE id=$1 AND status IN ('created','stopped') RETURNING ...`; publish only if one row returned.

Minimal safe fix before demo: Add frontend pending lock and do not double-click.

Proper fix after demo: Add outbox/start event table with unique campaign start event.

Verification after fix: Add a concurrent start integration test that asserts exactly one dispatch event.

Owner suggestion: backend.

Demo decision: start once only; double-click start is unsafe.

### ISSUE-4: Dispatcher fan-out remains non-idempotent despite windowing/reconnect

Severity: P0  
Category: backend/reliability  
Status: confirmed

Summary: The dispatcher now processes windows and can reconnect, but it still publishes send jobs directly and ACKs the campaign dispatch after work. If it crashes before ACK, the same window can be replayed and jobs duplicated.

Evidence:
- Consume/ACK flow: `services/dispatcher-service/main.go:73-145`.
- Send job publish loop: `services/dispatcher-service/main.go:228-257`.
- Continuation publish before ACK: `services/dispatcher-service/main.go:134-143`.
- No delivery job table/checkpoint is created before publish.

Why it matters:
- Demo impact: dispatcher restart during campaign can duplicate a partial window.
- Production impact: at-least-once queue delivery requires idempotent fan-out.

How to reproduce:
1. Start a campaign with enough recipients.
2. Kill dispatcher after it publishes part/all of a window but before ACK.
3. Restart dispatcher.
4. Compare actual send messages/deliveries to expected.

Expected behavior: replay does not duplicate already-created user/channel jobs.

Actual behavior: no durable fan-out idempotency boundary is present.

Root cause hypothesis: RabbitMQ message ACK is used as the only checkpoint.

Recommended fix: Insert delivery jobs into Postgres with unique `(campaign_id,user_id,channel_code)` before publish. Publish only new/queued rows.

Minimal safe fix before demo: Do not restart dispatcher during active campaign.

Proper fix after demo: Implement transactional outbox for fan-out.

Verification after fix: Crash-injection integration test around dispatcher fan-out.

Owner suggestion: backend.

Demo decision: avoid dispatcher restart during active load.

### ISSUE-5: Automatic retry still likely cannot change failed rows to success

Severity: P0  
Category: backend/reliability  
Status: confirmed

Summary: The sender still updates existing delivery rows only when current status is `queued`. Initial failures are inserted as `failed`. A retry with the same idempotency key can resend but not update the failed row.

Evidence:
- Retry publish: `services/sender-worker/main.go:338-340`.
- DLQ publish: `services/sender-worker/main.go:342-343`.
- Upsert predicate: `services/sender-worker/main.go:407-415`, `WHERE message_deliveries.status = 'queued'`.

Why it matters:
- Demo impact: retry can look active while failed counts remain wrong.
- Production impact: retry state is not trustworthy.

How to reproduce:
1. Force a channel failure.
2. Let automatic retry run.
3. Query `message_deliveries` by `idempotency_key`.
4. Check whether status changes after a successful later attempt.

Expected behavior: higher-attempt retry updates failed row or creates a proper attempt record and final status.

Actual behavior: conflict update is blocked unless row is queued.

Root cause hypothesis: error-group retry marks rows queued first, but automatic retry does not.

Recommended fix: Allow update from `failed` when incoming attempt is greater, or create `delivery_attempts` table and derive final status.

Minimal safe fix before demo: Do not claim automatic retry repair. Use visible failures only.

Proper fix after demo: Model attempts separately and keep terminal delivery aggregate deterministic.

Verification after fix: Force fail-then-success test.

Owner suggestion: backend/tests.

Demo decision: unsafe to demo automatic retry as fixed.

### ISSUE-6: Campaign-level retry still retries synthetic first-N users instead of actual failed rows

Severity: P0  
Category: backend/product/reliability  
Status: confirmed

Summary: `retryFailed` still uses only `campaign.FailedCount`, then republishes a generic dispatch. Dispatcher generates `user-00001`, `user-00002`, etc., so campaign-level retry can target the wrong users/channels.

Evidence:
- `services/campaign-service/main.go:347-369` gets `retryCount := campaign.FailedCount`.
- `campaign.TotalRecipients = retryCount`; `campaign.TotalMessages = retryCount`.
- Dispatcher synthetic ID generation: `services/dispatcher-service/main.go:240`.

Why it matters:
- Demo impact: retry button can repair the wrong messages.
- Production impact: users who failed may never be retried; users who succeeded may receive duplicates.

How to reproduce:
1. Run campaign with failures for users not in first N.
2. Trigger `/retry-failed`.
3. Inspect queued jobs.

Expected behavior: retry actual failed `message_deliveries` rows.

Actual behavior: retry count drives synthetic dispatch.

Root cause hypothesis: campaign-level retry is a shortcut and was not updated to use failed-row identity.

Recommended fix: Reuse the row-based error-group retry mechanism for campaign-wide failed rows.

Minimal safe fix before demo: Hide/avoid campaign-level retry; use error-group retry only after verifying it.

Proper fix after demo: Add campaign retry endpoint that selects failed row IDs in batches.

Verification after fix: Failed rows before retry exactly match queued retry rows.

Owner suggestion: backend/product.

Demo decision: avoid campaign-level retry.

### ISSUE-7: User picker is frontend-only and does not drive real backend recipient selection

Severity: P0  
Category: frontend/backend/data-model  
Status: confirmed

Summary: The new user picker creates/searches a 50k `MOCK_USERS` array in the frontend. Campaign creation still sends only `total_recipients` and `selected_channels`. It does not send exact selected user IDs or per-user channel selections to campaign-service/dispatcher.

Evidence:
- Frontend mock list: `apps/frontend/src/App.tsx:991-1002`.
- Client-side search/filter over mock data: `apps/frontend/src/App.tsx:1080-1085`.
- Specific selected users kept in wizard only: `apps/frontend/src/App.tsx:1056-1144`, `:1259-1261`.
- Campaign creation payload omits `specificUsers`: `apps/frontend/src/App.tsx:355-362`.
- Backend `createCampaignRequest` has no selected user field: `services/campaign-service/main.go:45-51`.
- Dispatcher still generates synthetic user IDs by numeric range: `services/dispatcher-service/main.go:240`.

Why it matters:
- Demo impact: presenter can show individual users selected, but backend sends to synthetic first-N users.
- Production impact: recipient selection is not correct.

How to reproduce:
1. In UI select users `user-01000` and `user-02000` only.
2. Create/start campaign.
3. Inspect dispatch/delivery rows.
4. Expected selected IDs are absent; jobs likely target `user-00001`, `user-00002`.

Expected behavior: backend receives and dispatches exact selected users/channels.

Actual behavior: backend receives only counts.

Root cause hypothesis: UI feature was added without a backend contract.

Recommended fix: Add `specific_recipients` to campaign create request, persist it or create a campaign_recipient table, and have dispatcher iterate those exact rows.

Minimal safe fix before demo: Do not claim individual recipient selection is backend-enforced. If showing UI, call it a UI prototype only.

Proper fix after demo: Implement campaign recipient table with unique `(campaign_id,user_id,channel_code)`.

Verification after fix: Select two specific users with different channel sets; DB deliveries match exactly.

Owner suggestion: frontend/backend.

Demo decision: unsafe to demo as real functionality.

### ISSUE-8: Specific-user per-channel counts disagree between frontend and backend

Severity: P0  
Category: data-model/product  
Status: confirmed

Summary: The frontend computes total messages for specific users as the sum of selected channels per user, but backend computes total messages as `total_recipients * selected_channels.length`. The actual per-user channel choices are not sent.

Evidence:
- Frontend specific total: `apps/frontend/src/App.tsx:1243-1247`.
- Payload sends global `selected_channels` and `total_recipients`: `apps/frontend/src/App.tsx:355-362`.
- Backend total: `services/campaign-service/main.go:158` uses `campaigns.TotalMessages(req.TotalRecipients, req.SelectedChannels)`.

Why it matters:
- Demo impact: UI expected message count can differ from backend.
- Production impact: campaign counts and benchmark claims become wrong.

How to reproduce:
1. Select 2 users.
2. Give one user 1 channel and another user 3 channels.
3. UI says 4 messages.
4. Backend receives `total_recipients=2` and global selected channels, likely computes a different total.

Expected behavior: backend total equals exact selected user-channel pairs.

Actual behavior: backend ignores per-user channel selection.

Root cause hypothesis: no recipient-channel pair model exists.

Recommended fix: Persist exact recipient-channel pairs for a campaign.

Minimal safe fix before demo: Disable per-user channel customization or label it as visual-only.

Proper fix after demo: Use `campaign_recipients`/`campaign_delivery_jobs` table.

Verification after fix: `total_messages = count(campaign_delivery_jobs)`.

Owner suggestion: backend/frontend.

Demo decision: avoid.

### ISSUE-9: Frontend still fakes progress, deliveries, and errors when backend is unavailable

Severity: P0  
Category: frontend/demo-safety  
Status: confirmed

Summary: Local fallback remains. If backend/WebSocket commands fail, the UI can still create optimistic campaigns, progress them on a timer, and generate local deliveries/error groups.

Evidence:
- Fallback state: `apps/frontend/src/App.tsx:123-127`.
- Local progress timer: `apps/frontend/src/App.tsx:212-237`.
- Optimistic campaign creation: `apps/frontend/src/App.tsx:327-374`.
- Optimistic campaign action updates: `apps/frontend/src/App.tsx:403-440`.
- Local fake deliveries/error groups: `apps/frontend/src/App.tsx:1972-2012`.

Why it matters:
- Demo impact: UI can look successful while backend is down.
- Production impact: users cannot distinguish real data from simulated data.

How to reproduce:
1. Do not start Docker stack.
2. Start frontend.
3. Login with local credentials.
4. Create/start campaign.
5. Watch progress update.

Expected behavior: offline mode is clearly flagged and cannot be mistaken for real delivery.

Actual behavior: fallback is integrated into primary product UI.

Root cause hypothesis: demo fallback was kept to make UI always usable.

Recommended fix: Add a blocking banner and mark all fallback campaigns as simulated; optionally disable campaign start if backend is unavailable.

Minimal safe fix before demo: Add obvious "local fallback / simulated" label.

Proper fix after demo: Separate mock/demo mode from production UI.

Verification after fix: With backend down, a screenshot clearly shows fallback mode.

Owner suggestion: frontend.

Demo decision: safe only if backend is live and fallback banner is visible when not.

### ISSUE-10: Real backend login likely fails in frontend because token format mismatch forces fallback auth

Severity: P1  
Category: security/frontend/backend  
Status: confirmed from code

Summary: Backend auth token has two parts: `payload.signature`. Frontend `decodeClaims` treats the second segment as the payload, like a JWT. Successful backend login can throw during decode and fall back to local demo credentials.

Evidence:
- Backend token: `packages/go-common/auth/auth.go:31-33` returns `encoded + "." + sig`.
- Frontend decode: `apps/frontend/src/api.ts:548-552` uses `token.split(".")[1]` as base64 payload.
- Login fallback catches backend errors and then accepts local credentials: `apps/frontend/src/App.tsx:607-620`.

Why it matters:
- Demo impact: backend auth may be broken while UI still logs in.
- Production impact: auth state is not trustworthy.

How to reproduce:
1. Start auth-service.
2. Login through frontend with valid backend credentials.
3. Observe frontend catch/decode failure and local fallback behavior.

Expected behavior: frontend decodes backend token correctly or backend returns standard JWT.

Actual behavior: token format mismatch.

Root cause hypothesis: custom token implementation changed independently from frontend decode expectations.

Recommended fix: Either return standard JWT `header.payload.signature` or change frontend to decode `split(".")[0]` for this custom format. Also store/use token for protected APIs.

Minimal safe fix before demo: Fix `decodeClaims` and add a test using a real backend-style token.

Proper fix after demo: Use standard JWT with expiry and authorization headers.

Verification after fix: Mock backend login returns a two-part token and frontend logs in without fallback.

Owner suggestion: frontend/security/backend.

Demo decision: unsafe to claim real auth until fixed.

### ISSUE-11: Status-service ops WebSocket remains unauthenticated and can mutate state

Severity: P0  
Category: security/backend  
Status: confirmed

Summary: `/ws/ops` accepts any WebSocket connection and forwards mutating commands to backend services.

Evidence:
- WebSocket accepts without auth: `apps/status-service/main.py:167-181`.
- Campaign create/start forwarding: `apps/status-service/main.py:215-225`.
- Campaign actions: `apps/status-service/main.py:227-235`.
- Channel update/template save/manager add: `apps/status-service/main.py:252-279`.

Why it matters:
- Demo impact: any local client can interfere with demo state.
- Production impact: severe authorization bypass.

How to reproduce:
Use any WebSocket client:
```json
{"id":"x","type":"campaign.action","payload":{"campaign_id":"<id>","action":"start"}}
```

Expected behavior: unauthenticated WebSocket is rejected.

Actual behavior: code accepts and processes commands.

Root cause hypothesis: status-service is acting as a trusted local command proxy.

Recommended fix: Require token on WebSocket connection and validate role/action before forwarding.

Minimal safe fix before demo: Restrict status-service to localhost and do not expose publicly.

Proper fix after demo: Centralize auth and remove unauthenticated proxy mutations.

Verification after fix: unauthenticated WebSocket receives close/error and cannot start a campaign.

Owner suggestion: security/backend.

Demo decision: unsafe outside trusted local machine.

### ISSUE-12: Mutating backend APIs still lack auth/RBAC

Severity: P1  
Category: security/backend  
Status: confirmed

Summary: Auth-service exists, but campaign/channel/template/user services do not enforce auth on write endpoints.

Evidence:
- Campaign service mutating endpoints: `services/campaign-service/main.go:87-95`, `:191-212`.
- Channel mutations: `services/channel-service/main.go:77-113`, `:171-209`.
- Template mutations: `services/template-service/main.go:74-106`, `:171-203`.
- Common server only handles CORS/logging, not auth: `packages/go-common/httpapi/server.go`.

Why it matters:
- Demo impact: local unexpected writes can change state.
- Production impact: no authorization boundary.

How to reproduce:
```bash
curl -X POST http://localhost:8085/campaigns/<id>/start
curl -X POST http://localhost:8084/channels/sms/disable
```

Expected behavior: unauthorized writes return 401/403.

Actual behavior: no auth check visible.

Root cause hypothesis: services were built for local demo and frontend role gating only.

Recommended fix: Add shared auth middleware and protect mutating routes.

Minimal safe fix before demo: Keep stack local-only; do not expose ports to a network.

Proper fix after demo: Enforce service-side RBAC.

Verification after fix: unauthenticated mutating curls fail.

Owner suggestion: backend/security.

Demo decision: safe only on trusted local machine.

### ISSUE-13: Disabled/missing channel still falls back to an enabled default sender config

Severity: P1  
Category: backend/product  
Status: confirmed

Summary: Sender-worker still returns a default enabled config if DB lookup fails or channel is disabled. This hides disabled-channel behavior.

Evidence:
- Default config initialized with `Enabled: true`: `services/sender-worker/main.go:377-379`.
- DB query filters `WHERE code = $1 AND enabled = true`: `services/sender-worker/main.go:384-387`.
- On error/no row, function returns default config: `services/sender-worker/main.go:388-389`.
- Status-service worker config has same default fallback: `apps/status-service/main.py:534-557`.

Why it matters:
- Demo impact: disabling a channel may not cause visible failures.
- Production impact: channel kill switches are ineffective.

How to reproduce:
1. Disable channel in channel-service/DB.
2. Send campaign through that channel.
3. Observe sender still can send with fallback config.

Expected behavior: disabled channel should fail delivery with clear error.

Actual behavior: fallback permits send.

Root cause hypothesis: fallback default was used for degraded mode but now conflicts with channel control semantics.

Recommended fix: Return explicit disabled/missing config and fail job before provider send.

Minimal safe fix before demo: Do not demo disabled-channel behavior.

Proper fix after demo: Make channel config source authoritative; cache only positive DB configs.

Verification after fix: disable `sms`, send `sms`, see `CHANNEL_DISABLED` failures.

Owner suggestion: backend.

Demo decision: avoid disabled-channel demo.

### ISSUE-14: Metrics are shallow/regressed on teammate branch

Severity: P1  
Category: metrics/observability  
Status: confirmed

Summary: On `origin/main`, `/metrics` for Go services is only `service_live_total`. The richer metrics/benchmark work from local `test` dirty state is not present on teammate branch.

Evidence:
- Common metrics handler: `packages/go-common/httpapi/server.go:32-35` writes only `service_live_total{service="..."}`.
- Status-service metrics: `apps/status-service/main.py:135-138` writes only `websocket_connected_clients`.
- `make metrics-test` does not exist on `origin/main`.

Why it matters:
- Demo impact: cannot show real delivery metrics from teammate branch.
- Production impact: no meaningful observability for queue/dependency/delivery health.

How to reproduce:
1. Start teammate branch.
2. Curl service `/metrics`.
3. Observe only liveness-style metric.

Expected behavior: metrics include campaign/message/success/failed/queue/retry/dependency counters.

Actual behavior: shallow metrics only.

Root cause hypothesis: teammate branch diverged from local uncommitted metrics implementation.

Recommended fix: Merge metrics implementation from `test` working tree or reapply on top of teammate changes.

Minimal safe fix before demo: Use final metric docs/results only if merged and runtime-verified.

Proper fix after demo: Add Prometheus-compatible metrics with tests and aggregation.

Verification after fix:
```bash
curl http://localhost:8085/metrics
curl http://localhost:8086/metrics
curl http://localhost:8087/metrics
curl http://localhost:8090/metrics
```

Owner suggestion: backend/observability.

Demo decision: unsafe to claim metrics from `origin/main`.

### ISSUE-15: Benchmark/performance pack is absent on teammate branch

Severity: P1  
Category: performance/testing  
Status: confirmed

Summary: The teammate branch does not contain the `perf-test-fast` target or performance scripts/results expected by previous work.

Evidence:
- `make -n perf-test-fast` on `origin/main` fails: `No rule to make target 'perf-test-fast'`.
- `PYTHON=.venv/bin/python make metrics-test` fails: `No rule to make target 'metrics-test'`.
- `find tests` on `origin/main` shows only `tests/smoke/health.sh` and `tests/test_postgres_viewer.py`.

Why it matters:
- Demo impact: benchmark commands from previous reports cannot be run from teammate branch.
- Production impact: no reproducible performance evidence.

How to reproduce:
```bash
make -n perf-test-fast
find tests/performance -type f
```

Expected behavior: benchmark scripts/targets exist on the final branch.

Actual behavior: missing on `origin/main`.

Root cause hypothesis: branch divergence.

Recommended fix: Reconcile branches and restore benchmark pack after teammate logic changes.

Minimal safe fix before demo: Run benchmark only from a branch that contains both teammate fixes and benchmark tooling.

Proper fix after demo: Move benchmark into tracked CI/test suite.

Verification after fix: L2 fast benchmark runs and writes a fresh JSON result.

Owner suggestion: performance/QA/devops.

Demo decision: unsafe to claim fresh teammate-branch benchmark.

### ISSUE-16: Stale Adminer test still fails against pgweb Compose config

Severity: P1  
Category: tests/devops  
Status: confirmed

Summary: The Python unittest still expects Adminer, while Compose uses pgweb.

Evidence:
- Failing command:
  ```bash
  python3 -m unittest discover -s tests -p 'test_*.py'
  ```
- Failure:
  ```text
  AssertionError: '    image: adminer:4.8.1' not found
  ```
- Compose uses `sosedoff/pgweb:latest` at `docker-compose.yml:21-29`.
- Test expects Adminer at `tests/test_postgres_viewer.py`.

Why it matters:
- Demo impact: `make test`/compose tests are red.
- Production impact: stale tests hide real failures.

How to reproduce:
```bash
python3 -m unittest discover -s tests -p 'test_*.py'
```

Expected behavior: test matches pgweb or Compose uses Adminer.

Actual behavior: mismatch.

Root cause hypothesis: Compose viewer was changed without updating test.

Recommended fix: Update test to assert pgweb image, port `8089:8081`, and pgweb command/url.

Minimal safe fix before demo: Fix test only; no product logic change.

Proper fix after demo: Parse Compose YAML instead of raw string checks.

Verification after fix: Python unittest passes.

Owner suggestion: tests/devops.

Demo decision: fix before claiming tests are green.

### ISSUE-17: Docker demo override is missing on teammate branch, so known 5432/scaling conflicts remain

Severity: P1  
Category: devops/demo  
Status: confirmed

Summary: `origin/main` lacks `docker-compose.demo.yml`. Base Compose still publishes Postgres on host `5432` and sender-worker on `8087`, which conflicts with local Postgres and Compose scaling.

Evidence:
- `docker compose -f docker-compose.yml -f docker-compose.demo.yml config` fails: no file.
- Base Compose Postgres port: `docker-compose.yml:11-12`.
- Sender-worker port: `docker-compose.yml:161-162`.

Why it matters:
- Demo impact: startup can fail on presenter laptop with existing Postgres; scaling sender-worker replicas can fail due host port collision.
- Production impact: local runbook is fragile.

How to reproduce:
```bash
docker compose up -d --scale sender-worker=5
```
Expected port conflict with `8087` unless Compose is changed.

Expected behavior: final branch has a safe demo override or runbook.

Actual behavior: missing on teammate branch.

Root cause hypothesis: teammate branch did not include local demo-hardening files.

Recommended fix: Bring back `docker-compose.demo.yml` or create a safe profile that removes Postgres and sender-worker host ports.

Minimal safe fix before demo: Use only one sender-worker container or manually edit/override ports.

Proper fix after demo: Add documented Compose profiles for dev/demo/perf.

Verification after fix:
```bash
docker compose -f docker-compose.yml -f docker-compose.demo.yml config
docker compose -f docker-compose.yml -f docker-compose.demo.yml up -d --scale sender-worker=5
```

Owner suggestion: devops.

Demo decision: unsafe to run scaled demo from `origin/main` base Compose.

### ISSUE-18: Dynamic worker pool can create much higher concurrency than expected and is not tied to demo docs/benchmarks

Severity: P1  
Category: performance/reliability  
Status: confirmed from code

Summary: Sender-worker now has an internal dynamic pool. With Compose replicas plus internal workers plus prefetch, effective concurrency can multiply quickly and unpredictably relative to previous "5 workers" benchmark language.

Evidence:
- Defaults in Compose: `WORKER_MIN_POOL=2`, `WORKER_MAX_POOL=8`, `WORKER_PREFETCH=20` at `docker-compose.yml:154-158`.
- Each internal worker opens a consumer with prefetch: `services/sender-worker/main.go:207-213`.
- Pool scales by queue depth: `services/sender-worker/main.go:115-150`.
- Previous benchmark language used 5 sender-worker replicas, not 5 internal pools.

Why it matters:
- Demo impact: "5 workers" may actually mean up to 5 containers x 8 internal consumers x 20 prefetch = large unacked window.
- Production impact: can overload DB/RabbitMQ/provider stub and make queue metrics hard to interpret.

How to reproduce:
1. Start 5 sender-worker replicas.
2. Run large campaign.
3. Hit `/worker/stats` for one exposed worker or inspect logs.
4. Observe active internal workers and unacked behavior.

Expected behavior: concurrency model is explicit and benchmark records containers, internal workers, and prefetch.

Actual behavior: metadata/runbook not updated on teammate branch.

Root cause hypothesis: dynamic pool added after benchmark/reporting.

Recommended fix: Define one scaling model. For demo, prefer either single container with internal pool or fixed replicas with controlled pool size.

Minimal safe fix before demo: Set `WORKER_MIN_POOL=5`, `WORKER_MAX_POOL=5`, one container, or document exact replica x pool x prefetch mode.

Proper fix after demo: Add aggregated metrics for effective consumers/unacked jobs.

Verification after fix: benchmark JSON records `worker_containers`, `worker_min_pool`, `worker_max_pool`, `prefetch`, and observed active workers.

Owner suggestion: performance/backend.

Demo decision: safe only with explicit configuration.

### ISSUE-19: Status-service DB/Redis helper paths are not wired in Compose

Severity: P1  
Category: backend/data-model/devops  
Status: confirmed

Summary: status-service contains code to write queued jobs/results and read channel configs from Postgres/Redis, but Compose does not set `POSTGRES_DSN` or `REDIS_URL` for status-service. Those paths will silently degrade.

Evidence:
- status-service reads `POSTGRES_DSN`/`REDIS_URL`: `apps/status-service/main.py:93-94`.
- `write_queued_job_sync` returns true if `psycopg is None or not POSTGRES_DSN`: `apps/status-service/main.py:573-575`.
- `read_channel_config_from_redis` requires `REDIS_URL`: `apps/status-service/main.py:513-521`.
- Compose status-service env at `docker-compose.yml:181-191` lacks `POSTGRES_DSN` and `REDIS_URL`.

Why it matters:
- Demo impact: endpoints may report success without writing DB.
- Production impact: silent degradation creates false idempotency/state assumptions.

How to reproduce:
1. Start Compose.
2. Call `POST /worker/jobs` on status-service.
3. Query DB for queued row.

Expected behavior: status-service either writes DB or reports dependency missing.

Actual behavior: code can return success without DB write.

Root cause hypothesis: new status-service helper code was not wired into Compose.

Recommended fix: Add `POSTGRES_DSN` and `REDIS_URL` env or remove/disable endpoints when dependencies are absent.

Minimal safe fix before demo: Do not use status-service worker job endpoints.

Proper fix after demo: Make dependency absence explicit in readiness and endpoint responses.

Verification after fix: DB row appears after `/worker/jobs`.

Owner suggestion: backend/devops.

Demo decision: avoid those endpoints.

### ISSUE-20: Frontend start/retry buttons still lack pending locks

Severity: P1  
Category: frontend/reliability  
Status: confirmed

Summary: The UI sends WebSocket commands and immediately applies optimistic state, but it does not disable the clicked Start/Retry while the command is pending.

Evidence:
- Campaign action handler: `apps/frontend/src/App.tsx:403-440`.
- Start button: `apps/frontend/src/App.tsx:819-820`.
- Campaign list Start/Retry: `apps/frontend/src/App.tsx:969-970`.
- No `pendingCampaignActions` state exists.

Why it matters:
- Demo impact: user can double-click and trigger backend race.
- Production impact: duplicate mutations.

How to reproduce:
1. Open campaign list.
2. Double-click Start quickly.
3. Inspect WebSocket `campaign.action` messages.

Expected behavior: one command until response/timeout.

Actual behavior: code has no pending lock.

Root cause hypothesis: optimistic UI updates were prioritized over mutation safety.

Recommended fix: Track pending actions per campaign and disable all conflicting controls.

Minimal safe fix before demo: Add pending lock for Start and Retry only.

Proper fix after demo: Add mutation state for all campaign/error group actions.

Verification after fix: frontend test double-click sends one WebSocket message.

Owner suggestion: frontend.

Demo decision: click once; unsafe for double-click.

### ISSUE-21: User search is client-side over mock data, not server-side/paginated

Severity: P1  
Category: frontend/backend/performance  
Status: confirmed

Summary: Search scans a 50k frontend array. User-service does not expose text search by name/email/phone/telegram; it only filters age/gender/location/tags.

Evidence:
- Frontend mock data: `apps/frontend/src/App.tsx:991-1002`.
- Frontend search scans array: `apps/frontend/src/App.tsx:1080-1085`.
- User-service filters: `services/user-service/main.go:44-52`; no `q`, email, phone, telegram search.

Why it matters:
- Demo impact: search result is not backend truth.
- Production impact: client-side 50k search does not scale to real datasets and cannot use DB indexes.

How to reproduce:
1. Search a user in picker.
2. Compare result to `GET /users` backend.
3. Observe picker does not call backend.

Expected behavior: user search calls backend with query/pagination.

Actual behavior: frontend mock only.

Root cause hypothesis: UI feature was built before backend contract.

Recommended fix: Add `/users?query=&limit=&offset=` backed by Postgres or user-service data, and use it from picker with debounce.

Minimal safe fix before demo: Label picker as local prototype or disable exact recipient claims.

Proper fix after demo: Server-side paginated search with deterministic sort.

Verification after fix: Network panel shows `/users?query=...`; selected IDs are passed to campaign-service.

Owner suggestion: frontend/backend.

Demo decision: unsafe as real search.

### ISSUE-22: Common `/metrics` can overwrite richer service-specific metrics unless carefully integrated

Severity: P1  
Category: metrics/backend  
Status: likely

Summary: The common mux registers `/metrics` with only `service_live_total`. If services need richer metrics, they must override or add before/after carefully. On `origin/main`, no richer handlers are present.

Evidence:
- `packages/go-common/httpapi/server.go:32-35`.
- Search found no campaign/sender/dispatcher rich metrics on `origin/main`.

Why it matters:
- Demo impact: `/metrics` endpoints can be alive but useless.
- Production impact: no actionable monitoring.

How to reproduce:
```bash
curl http://localhost:8085/metrics
```

Expected behavior: useful counters/histograms.

Actual behavior: likely only `service_live_total`.

Root cause hypothesis: metrics implementation from previous local work is not merged.

Recommended fix: Restore rich metrics per service and tests.

Minimal safe fix before demo: Do not show `/metrics` from `origin/main` as performance evidence.

Proper fix after demo: Register metrics through composable handler that includes service-specific collectors.

Verification after fix: `/metrics` contains delivery counters and numeric values.

Owner suggestion: observability/backend.

Demo decision: avoid.

## 5. RabbitMQ Reconnect Review

### What is fixed

- New helper `RunWithReconnect` in `packages/go-common/runtime/reconnect.go`.
- It opens a fresh AMQP connection/channel, declares topology, watches `conn.NotifyClose`, and reconnects with exponential backoff up to 30 seconds.
- Dispatcher consumer uses it in `services/dispatcher-service/main.go:39-49`.
- Sender-worker consumers use it through the dynamic worker pool at `services/sender-worker/main.go:182`.
- Sender-worker publisher channel is also maintained with it at `services/sender-worker/main.go:43-54`.
- Status-service uses `aio_pika.connect_robust` in `apps/status-service/main.py:347-368`.

### What is not fixed

- Campaign-service publisher does not reconnect. It still uses one-time `OpenRabbit()` at startup.
- Campaign-service `Ready` depends on `mq != nil`, but if the channel later dies, the pointer can remain non-nil and stale.
- No publisher confirms are used by Go services.
- Dispatcher fan-out is still not idempotent, so reconnect/requeue can duplicate work.
- Runtime RabbitMQ restart was not tested in this review because Docker was unavailable.
- Metrics do not prove queue inspect/reconnect truth on `origin/main`.

### Active-load behavior risks

- RabbitMQ dies while campaign-service is publishing start/retry: publish fails; service may not recover without restart.
- RabbitMQ dies during dispatcher fan-out: unacked campaign dispatch can be redelivered; already-published send jobs may duplicate.
- RabbitMQ dies while sender-worker processes message: worker may finish DB write but fail event/retry/DLQ publish, return error, Nack/requeue, and then resend.

### Startup behavior

- `OpenRabbit()` in campaign-service tries for 30 attempts and then logs degraded; no later reconnect.
- Dispatcher/sender reconnect loops can tolerate RabbitMQ being unavailable at startup.

### Metric truthfulness

- On `origin/main`, `/metrics` does not expose real RabbitMQ health.
- Worker `/worker/stats` queue depth uses management API `QueueDepth`, but that endpoint is per-process and not aggregate.
- Dispatcher backpressure still uses AMQP `QueueInspect`, with different semantics from management API.

### Recommended final test

Run only after merging onto the final branch:

```bash
docker compose up -d --build
curl -X POST http://localhost:8085/campaigns/<id>/start
docker compose restart rabbitmq
sleep 20
curl -X POST http://localhost:8085/campaigns/<new-id>/start
docker compose logs --tail=200 campaign-service dispatcher-service sender-worker status-service rabbitmq
psql "$POSTGRES_DSN" -c "select status, count(*) from message_deliveries group by status;"
```

Pass requires:

- campaign-service publishes after RabbitMQ restart without service restart;
- dispatcher/sender reconnect without duplicate consumers;
- delivery counts do not exceed expected unique user-channel jobs;
- metrics/logs show down/up accurately.

## 6. User Search and Individual Recipient Review

### Correctness

Not correct end-to-end. The UI can select individual users, but backend campaign creation does not receive exact user IDs or per-user channel selections.

### Duplicates

The picker uses a `Map<string, Set<string>>`, so frontend duplicate selection is mostly prevented inside the modal. But overlap between segment audience and specific users is not meaningful because backend receives only counts.

### 50k behavior

The picker creates 50k users in browser memory and searches with `Array.filter`. That may be okay for a demo laptop, but it is not server-side search and not proof of production behavior.

### UI/UX

The UI shows selected counts and per-user channels, which can look real. This is dangerous because backend ignores those exact selections.

### Backend contract

Missing. Required fields like `specific_recipients` or `campaign_recipients` table do not exist.

### Risks

- wrong users receive campaign;
- total message count mismatch;
- selected users duplicated with segment users cannot be detected;
- no DB proof that selected users were used.

### Recommended fixes

1. Add backend `/users` search with `query`, `limit`, `offset`.
2. Add campaign create field:
   ```json
   "specific_recipients": [{"user_id":"user-00010","channels":["email","sms"]}]
   ```
3. Persist exact recipient-channel jobs.
4. Dispatcher iterates persisted jobs, not synthetic `userID(i)`.

## 7. Idempotency and Retry Review

| Area | Current result | Duplicate/loss risk |
|---|---|---|
| Campaign start | Sequential duplicate improved; parallel race still open | Duplicate dispatch |
| Dispatcher fan-out | Windowed, but not DB-idempotent | Duplicate jobs after crash/requeue |
| Sender-worker delivery | DB upsert exists, but after provider send | Duplicate external notification possible |
| Automatic retry | Still conflicts with failed rows | Retry may not repair state |
| Manual campaign retry | Still count-based synthetic redispatch | Wrong users retried |
| Error-group retry | More targeted; marks failed rows queued | Safer, but runtime proof needed |
| Disabled channel retry | Disabled channel fallback still sends | Failure not visible |

Most dangerous duplicate scenario: campaign start twice or dispatcher crash during fan-out. These can create many duplicate sends and are hard to explain during demo.

## 8. Metrics / Benchmark Trust Review

### Trustworthy on `origin/main`

- Service liveness metric `service_live_total` only proves HTTP handler is up.
- Status-service `websocket_connected_clients` only proves connected WebSocket count.
- Frontend p95 label now says `p95 enqueue`, which is less misleading than delivery p95.

### Misleading or missing

- No campaign/message/success/failed counters in `/metrics`.
- No queue depth metric exposed through `/metrics`.
- No RabbitMQ health metric exposed through `/metrics`.
- No p95 delivery latency.
- No benchmark scripts/targets.
- No sender-worker aggregate metrics under replicas.
- No metrics smoke target.

### Current benchmark validity

No fresh benchmark was run. On `origin/main`, the benchmark tooling is absent. Previous benchmark numbers from local `test` docs cannot be claimed for teammate branch until the code is merged and rerun.

### Numbers that can be claimed

Only with branch caveat:

- Previous saved benchmark numbers exist in local docs, not verified against `origin/main`.
- Frontend tests pass on `origin/main`.
- Compose config renders on `origin/main`.

### Numbers that cannot be claimed from this review

- L2/L3/L4 throughput after teammate changes.
- RabbitMQ restart recovery under active load.
- 50k estimate after dynamic worker pool.
- `/metrics` delivery counters.

## 9. Security Review

| Finding | Severity | Evidence | Minimum demo-safe framing |
|---|---|---|---|
| Ops WebSocket unauthenticated | P0 | `apps/status-service/main.py:167-181` | Local trusted demo only |
| Backend mutating APIs unauthenticated | P1 | campaign/channel/template services | Do not expose ports publicly |
| Frontend login falls back to local credentials | P1 | `apps/frontend/src/App.tsx:607-620` | Demo auth only |
| Token format mismatch | P1 | backend two-part token vs frontend JWT decode | Fix before claiming real login |
| Hardcoded credentials/secrets | P1 | `services/auth-service/main.go:24-27` | Demo credentials only |
| Wildcard CORS | P1 | `httpapi/server.go:66-68`, status CORS | Localhost only |
| Exposed DB/RabbitMQ/pgweb/Redis | P1 | `docker-compose.yml` ports | Trusted laptop only |
| No object-level auth | P2 | no campaign owner checks | Single-manager demo assumption |

## 10. Demo Safety Matrix

| Demo action | Safe? | Why | What can break | Recovery | Recommendation |
|---|---|---|---|---|---|
| Create campaign | Medium | Backend path exists | frontend fallback can fake; recipient selection mismatch | refresh backend data/check DB | Safe only with backend live |
| Search users | Unsafe as real backend feature | UI mock search only | selected users not sent to backend | explain as prototype | Avoid or label prototype |
| Add individual user | Unsafe | exact IDs ignored by backend | wrong recipients dispatched | none quick | Do not claim real |
| Start campaign once | Medium | code can start/publish | campaign-service RabbitMQ stale channel after broker restart | restart campaign-service | Safe only once, no RabbitMQ fault |
| Double-click start | Unsafe | race remains | duplicate dispatch | hard cleanup | Do not demo |
| Retry failed | Unsafe | campaign retry wrong; auto retry broken | wrong users/retry state | use error-group only if verified | Avoid campaign-level retry |
| Stop/start sender-worker | Medium | sender reconnect exists | duplicate external send possible under real providers; runtime not tested now | restart worker | Safe only idle or stub demo |
| Restart dispatcher | Unsafe under active load | fan-out non-idempotent | duplicate jobs | restart + inspect DB | Avoid active load |
| Restart campaign-service | Medium | app restart should recover if deps healthy | frontend fallback may hide outage | start service | Explain only |
| Restart status-service | Medium | delivery pipeline should continue | dashboard disconnects | start service | Safer than broker faults |
| Restart RabbitMQ | Unsafe | campaign publisher no reconnect, duplicate risks | start RabbitMQ + restart clients | Do not live-demo |
| Restart Postgres | Unsafe | source of truth unavailable | writes/metrics fail | restart Postgres + services | Do not live-demo |
| Run L2 benchmark | Not enough evidence | benchmark missing on origin/main | no target/scripts | restore benchmark pack | Do not claim fresh |
| Run 50k benchmark | Unsafe | not tested, no tooling on origin/main | long run/state pollution | dedicated env | Do not run live |

## 11. Programmer Fix Queue

### P0 Today

1. Fix branch integration/source of truth.
   - Files: git branches, conflict resolution.
   - Estimated time: 30m-2h depending conflicts.
   - Verification: final branch contains `1de7a1b` plus local metrics/docs/test artifacts.

2. Fix campaign start idempotency.
   - Files: `services/campaign-service/main.go`, tests.
   - Estimated time: 30m-1h.
   - Verification: parallel start test publishes once.

3. Fix campaign-service RabbitMQ reconnect.
   - Files: `services/campaign-service/main.go`, `packages/go-common/runtime`.
   - Estimated time: 1h.
   - Verification: campaign start works after RabbitMQ restart without campaign-service restart.

4. Fix retry correctness.
   - Files: `services/campaign-service/main.go`, `services/sender-worker/main.go`.
   - Estimated time: 1-2h.
   - Verification: failed row retry targets exact row and final status updates.

5. Either wire individual recipients end-to-end or disable honest claims.
   - Files: `apps/frontend/src/App.tsx`, `services/campaign-service/main.go`, `services/dispatcher-service/main.go`, migration.
   - Estimated time: 2h+ if implemented; 15m to disable/label.
   - Verification: selected user IDs equal delivery user IDs.

### P1 Before Defense

1. Fix frontend token decode / login fallback truthfulness.
   - Files: `apps/frontend/src/api.ts`, `apps/frontend/src/App.tsx`, auth tests.
   - Estimated time: 30m.
   - Verification: backend-style token login works without fallback.

2. Add pending locks for Start/Retry.
   - Files: `apps/frontend/src/App.tsx`.
   - Estimated time: 30m.
   - Verification: double-click sends one command.

3. Restore safe demo Compose override and benchmark/metrics targets.
   - Files: `docker-compose.demo.yml`, `Makefile`, `tests/performance`, `tests/smoke`.
   - Estimated time: 1h.
   - Verification: `docker compose -f docker-compose.yml -f docker-compose.demo.yml config`, `make -n perf-test-fast`, strict metrics test.

4. Fix stale pgweb unittest.
   - Files: `tests/test_postgres_viewer.py`.
   - Estimated time: 10m.
   - Verification: Python unittest passes.

5. Make disabled channels fail visibly.
   - Files: `services/sender-worker/main.go`, `apps/status-service/main.py`.
   - Estimated time: 30m.
   - Verification: disabled channel creates failed delivery.

6. Add strict metrics smoke.
   - Files: `Makefile`, `tests/smoke`.
   - Estimated time: 30m.
   - Verification: stack down fails, stack up passes.

### P2 After Defense

1. Replace mock user picker with backend search.
   - Files: `services/user-service/main.go`, `apps/frontend/src/App.tsx`.
   - Verification: network search and exact recipients.

2. Add true delivery job table/outbox.
   - Files: migrations, campaign/dispatcher/sender.
   - Verification: crash/replay tests.

3. Add auth/RBAC to all mutating routes.
   - Files: shared middleware and services.
   - Verification: unauthenticated writes fail.

4. Add production metrics aggregation.
   - Files: service metrics handlers, status/campaign aggregation.
   - Verification: Prometheus text includes accurate counters.

5. Harden Kubernetes manifests.
   - Files: `deploy/k8s`.
   - Verification: clean namespace deploy passes smoke.

## 12. Exact Prompts for the Programmer's Coding Agent

1. Fix ISSUE-1 and branch integration:
   ```text
   We need to reconcile Norify branch state. The required branch is test, but teammate fixes are on origin/main at 1de7a1b and local test has uncommitted metrics/docs changes. Create a safe integration plan, preserve local uncommitted work, merge/cherry-pick origin/main into test without losing docs/performance/metrics work, and run git status plus diff summary. Do not reset or discard anything.
   ```

2. Fix ISSUE-2 and ISSUE-3:
   ```text
   Fix campaign-service RabbitMQ publishing and campaign start idempotency. In services/campaign-service/main.go, add a reconnect-managed RabbitMQ publisher channel and make /campaigns/{id}/start an atomic DB transition from created/stopped to running. Publish dispatch only if the transition succeeds. Add tests for non-startable statuses and parallel double-start. Verify RabbitMQ restart does not permanently break campaign start.
   ```

3. Fix ISSUE-5 and ISSUE-6:
   ```text
   Fix retry correctness. Automatic sender retry must update failed delivery state when a higher attempt succeeds, and campaign-level retry must target exact failed message_deliveries rows instead of FailedCount synthetic users. Update services/sender-worker/main.go and services/campaign-service/main.go, add unit/integration tests, and prove failed row IDs are the only retried jobs.
   ```

4. Fix ISSUE-7 and ISSUE-8:
   ```text
   Make individual recipient selection real or clearly disable it. The frontend user picker currently uses MOCK_USERS and campaign create sends only total_recipients. Add a backend contract for selected user/channel pairs, persist them, and make dispatcher fan out exact selected pairs. If too large for demo, remove/label the picker as prototype and prevent misleading claims.
   ```

5. Fix ISSUE-9, ISSUE-10, and ISSUE-20:
   ```text
   Harden frontend truthfulness. Fix backend token decoding in apps/frontend/src/api.ts, make backend login failures visible instead of silently falling back, add a clear local-fallback/simulated-data banner, and add pending locks so double-clicking Start/Retry sends only one WebSocket command. Add frontend tests for backend login token decode, fallback banner, and double-click prevention.
   ```

6. Fix ISSUE-14 and ISSUE-15:
   ```text
   Restore real metrics and benchmark tooling after teammate branch changes. Reintroduce useful Prometheus-style metrics endpoints, make metrics-test strict when required, restore tests/performance/benchmark_campaign.py and Makefile perf-test-fast, and ensure the benchmark records actual worker runtime config rather than only Python env variables.
   ```

7. Fix ISSUE-16 and ISSUE-17:
   ```text
   Fix demo/devops tests and startup. Update tests/test_postgres_viewer.py to match pgweb, add or restore docker-compose.demo.yml to avoid host Postgres 5432 and sender-worker scaling port conflicts, and verify docker compose config for base and demo override.
   ```

8. Add missing integration tests:
   ```text
   Add high-value integration tests for Norify delivery correctness: double start publishes once, dispatcher crash/replay does not duplicate jobs, sender redelivery does not call provider after terminal success, error-group retry targets exact failed rows, disabled channel creates visible failures, and RabbitMQ restart recovers publishers/consumers. Keep tests focused and runnable from Makefile.
   ```

