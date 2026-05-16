# Mega Pre-Defense Fix Review

Date: 2026-05-16

## 1. Verdict

**PARTIAL: safe to commit for pre-defense hardening, safe to merge only with the limitations below documented and accepted.**

The patch materially improves demo safety. It passes Go, frontend, Python, Compose config, and runtime smoke checks. The campaign start idempotency fix did not regress, and the new campaign-service RabbitMQ publisher reconnect was runtime-proven after a real RabbitMQ restart.

It is not a production-grade reliability fix. Retry is now demo-verified but still lacks a durable attempt/outbox model, provider send idempotency is only guarded for existing terminal/stale rows, campaign-level switch-channel is disabled rather than fully implemented, and there is still no transactional outbox for the DB-transition-to-RabbitMQ publish gap.

### Second Focused Hardening Update

After this review, a second hardening pass improved the previous PARTIAL items:

- Retry now atomically claims failed rows before publish with `FOR UPDATE SKIP LOCKED` and `UPDATE ... RETURNING`.
- Sender-worker now skips terminal/stale delivery rows before provider stub send.
- Campaign-level switch-channel is disabled in backend and hidden in frontend.
- Forced-failure retry was runtime-verified: 2 failed rows retried through two parallel retry requests and finished as exactly 2 sent rows at attempt 2.

The verdict remains **PARTIAL**, not PASS, because there is still no transactional outbox and no full provider exactly-once mechanism for duplicate new messages.

## 2. Executive Summary

- Campaign start is still atomically gated by a DB transition and runtime parallel start returned one `200` plus one `409`.
- Campaign-service RabbitMQ reconnect is code-present and runtime-proven for a new campaign after broker restart.
- Sender-worker stop/start recovered and a new campaign completed.
- Frontend no longer mutates campaigns into fake success/progress when backend commands fail.
- User picker is honestly degraded as demo sampling, not exact backend recipient selection.
- Retry improved from synthetic first-N retry to atomic real-row claiming and was runtime-verified with forced failures, but it is still not a production outbox/attempt model.
- Metrics on `main` are still shallow; `/metrics` is useful only as liveness smoke, not load evidence.
- Full stack is currently running through `docker-compose.demo.yml`.

## 3. Patch Summary

| Area | Files | Claimed fix | Review verdict | Evidence |
|---|---|---|---|---|
| Campaign publisher reconnect | `services/campaign-service/main.go` | Reconnect-managed RabbitMQ publisher | PASS for future starts after broker restart | Lines 92-160 add reconnect loop and mutex-protected publisher; runtime RabbitMQ restart completed and a fresh campaign finished 2/2. |
| Campaign start idempotency | `services/campaign-service/main.go`, `main_test.go` | Atomic start transition remains | PASS | Lines 324-372 use `UPDATE ... status IN created/stopped ... RETURNING`; runtime parallel start returned `200` and `409`. |
| Campaign retry | `services/campaign-service/main.go` | Retry real failed rows | PARTIAL / IMPROVED | Retry now atomically claims failed rows before publish and was runtime-verified with two parallel retry requests. |
| Sender retry result write | `services/sender-worker/main.go`, `main_test.go` | Higher-attempt retry can update failed rows | PARTIAL / IMPROVED | Higher attempts can repair failed rows; terminal/stale rows are skipped before provider send. |
| Frontend truthfulness | `apps/frontend/src/App.tsx`, tests, styles | No fake progress; pending locks | PASS for demo safety | Removed fallback progress timer; added fallback banner and pending keys; frontend build and 22 tests passed. |
| User picker | `apps/frontend/src/App.tsx` | Honest degradation | PASS as degradation, not exact selection | UI text says backend receives count/common channels, not exact IDs. |
| Demo Compose/Makefile | `docker-compose.demo.yml`, `Makefile`, `README.md` | Safe local demo commands | PASS | Override maps Postgres to `15432` and keeps sender-worker single container with internal pool 5; Compose config passed. |
| pgweb stale test | `tests/test_postgres_viewer.py`, `README.md` | Adminer mismatch fixed | PASS | Root unittest passed. |

## 4. Detailed Findings

### REVIEW-ISSUE-1: Campaign-service RabbitMQ reconnect works for future starts, but not active in-flight guarantees

Severity: P1  
Status: partial

Evidence:

- `services/campaign-service/main.go:92-160` starts `appruntime.RunWithReconnect`, stores the active channel under `mqMu`, and resets the channel on publish failure.
- Runtime log after `docker compose -f docker-compose.yml -f docker-compose.demo.yml restart rabbitmq`:
  - `rabbitmq connection lost, reconnecting service=campaign-service-publisher`
  - `rabbitmq unavailable, will retry service=campaign-service-publisher`
  - `rabbitmq publisher connected service=campaign-service`
- Fresh campaign after broker restart:
  - `cmp-ed48a23be7f1ccee`
  - final status `finished`
  - `sent_count=2`, `success_count=2`, `failed_count=0`

Impact:

- Demo: safe to say campaign-service recovered for a new campaign after RabbitMQ restart in this local run.
- Production: not enough to claim exactly-once dispatch or active-load broker recovery.

Recommendation:

- Commit this reconnect improvement.
- Do not claim RabbitMQ restart is fully safe under active campaign load until an active-load broker restart test passes.
- Add a transactional outbox for campaign start dispatch after defense.

### REVIEW-ISSUE-2: Crash gap after campaign DB transition remains open

Severity: P1  
Status: open

Evidence:

- `services/campaign-service/main.go:324-341` transitions campaign to `running`, then calls `publishDispatch`.
- If the process dies after line 326 and before/during line 335, the DB can show `running` with no dispatch event.
- The rollback at lines 335-338 only runs if the process is alive and `publishDispatch` returns an error.

Impact:

- Demo: low probability unless killing campaign-service exactly during start.
- Production: campaign can get stuck `running` with zero deliveries.

Recommendation:

- Proper fix: transactional outbox table plus background publisher/reconciler.
- Demo guidance: do not kill campaign-service during campaign start.

### REVIEW-ISSUE-3: Campaign start idempotency did not regress

Severity: P0 previously, now fixed  
Status: fixed

Evidence:

- `services/campaign-service/main.go:344-372` uses a single DB transition restricted to `status IN ('created', 'stopped')`.
- Dispatch publish happens only after `transitionCampaignToRunning` returns `started=true`.
- Runtime double-start probe for `cmp-17725b7547b40b64`:
  - one response `HTTP:200`
  - one response `HTTP:409 {"error":"campaign_not_startable"}`
  - final deliveries: exactly 2 rows for 2 expected messages.

Impact:

- Demo: double-click Start is now safe at backend level.
- Production: start endpoint is no longer the main duplicate dispatch source.

Recommendation:

- Keep this fix.
- Add a Docker/Postgres-backed concurrent integration test later.

### REVIEW-ISSUE-4: Retry is row-based and demo-verified, but not production exactly-once

Severity: P1  
Status: partial / improved

Evidence:

- `services/campaign-service/main.go` now claims rows with `FOR UPDATE SKIP LOCKED` and `UPDATE ... RETURNING`.
- Runtime forced-failure campaign `cmp-319eca0e952605c1` produced 2 failed rows at attempt 1.
- Two parallel `/retry-failed` calls were issued.
- Final deliveries remained exactly 2 rows, both `sent`, both `attempt=2`, with no visible duplicate delivery rows.

Impact:

- Demo: retry is now safe enough to show once, including the forced-failure flow documented in `docs/fixes/retry_and_provider_idempotency_hardening.md`.
- Production: no outbox/attempt table means publish and provider exactly-once are still not guaranteed.

Recommendation:

- Keep the atomic claim.
- Add a delivery attempts table and retry outbox after defense.

### REVIEW-ISSUE-5: Sender now has a pre-send terminal guard, but exactly-once provider send is still not proven

Severity: P1  
Status: partial / improved

Evidence:

- `services/sender-worker/main.go` now calls `shouldSendDelivery` before provider send.
- Terminal `sent` and `cancelled` rows are skipped before calling the provider stub.
- Unit tests cover sent/cancelled/stale retry skip cases.

Impact:

- Demo: stale redelivery of already terminal rows is safer.
- Production: two duplicate messages for a never-before-seen idempotency key can still race because there is no atomic processing lease.

Recommendation:

- Add a processing lease or delivery-attempt record before real providers are integrated.

### REVIEW-ISSUE-6: Campaign-level switch-channel is disabled instead of fixed

Severity: P1  
Status: fixed for demo safety / product gap remains

Evidence:

- Backend now returns HTTP `409` with `campaign_switch_channel_disabled`.
- Frontend no longer renders campaign-level switch-channel as a clickable button.
- Runtime `POST /campaigns/{id}/switch-channel` returned `409`.

Impact:

- Demo: no misleading clickable campaign-level switch remains.
- Product: whole-campaign switch-channel remains unimplemented.

Recommendation:

- Use error-group switch only.
- Implement row-based campaign switch later if required.

### REVIEW-ISSUE-7: Frontend fallback is now honest, but create semantics are still "create and start"

Severity: P2  
Status: partial

Evidence:

- `apps/frontend/src/App.tsx:210-216` no longer runs a local progress timer and emits a fallback warning.
- `apps/frontend/src/App.tsx:483-489` shows a visible fallback banner.
- `apps/status-service/main.py:217-226` handles `campaign.create` by POSTing `/campaigns`, then immediately POSTing `/campaigns/{id}/start`.

Impact:

- Demo: UI is safer because backend failure is visible.
- Product: "create campaign" still means "create and start" through status-service, so there is no separate frontend review-before-start flow.

Recommendation:

- Accept for demo if presenter says the wizard starts immediately.
- Later split create and start into separate UI actions if product requires approval flow.

### REVIEW-ISSUE-8: User picker is honest but not real exact recipient selection

Severity: P1 product gap  
Status: partial

Evidence:

- `apps/frontend/src/App.tsx:1277-1278` labels selected users as demo sample.
- `apps/frontend/src/App.tsx:1306` states backend dispatch does not receive selected IDs.
- `createCampaign` sends `total_recipients`, not selected user IDs.

Impact:

- Demo: honest enough if presenter does not claim exact user targeting.
- Product: exact recipient selection is not implemented end-to-end.

Recommendation:

- Presenter wording: "For this demo, individual picker changes the sample/count. Exact selected-recipient fan-out is a known next backend contract."
- Proper fix: persist selected recipients and dispatch exact rows.

### REVIEW-ISSUE-9: Metrics are still liveness-level on main

Severity: P2  
Status: open

Evidence:

- Runtime `/metrics` samples:
  - campaign-service: `service_live_total{service="campaign-service"} 1`
  - dispatcher-service: `service_live_total{service="dispatcher-service"} 1`
  - sender-worker: `service_live_total{service="sender-worker"} 1`
  - status-service: JSON string output `"websocket_connected_clients 0\n"`

Impact:

- Demo: metrics curl can show services are alive.
- Performance/reliability: cannot claim meaningful queue depth, p95, throughput, or failure metrics from this branch.

Recommendation:

- Do not use these metrics as benchmark proof.
- Restore or reimplement the metrics pack only after core delivery semantics are fixed.

### REVIEW-ISSUE-10: Runtime verification covered happy path and broker recovery, not active-load fault tolerance

Severity: P2  
Status: partial

Evidence:

- Runtime checks used 2-recipient smoke campaigns.
- RabbitMQ was restarted while no campaign was actively processing.
- Retry was not runtime-tested because smoke campaigns had no failed deliveries.

Impact:

- Demo: safe for smoke demo and a controlled RabbitMQ reconnect explanation.
- Production: still no proof under active load, backlog, failure, or retry pressure.

Recommendation:

- Before claiming reliability, run active-load RabbitMQ restart and retry scenarios with forced failures.

## 5. Test Results

| Command | Result | Notes |
|---|---:|---|
| `git diff --check` | PASS | No whitespace/conflict-marker issues. |
| `gofmt -w services/campaign-service/main.go services/campaign-service/main_test.go services/sender-worker/main.go services/sender-worker/main_test.go` | PASS | No resulting compile issues. |
| `go test ./services/campaign-service` | PASS | Cached. |
| `go test ./services/sender-worker` | PASS | Cached. |
| `go test ./services/dispatcher-service` | PASS | Cached. |
| `go test ./...` | PASS | All Go packages passed. |
| `make lint` | PASS | `go vet ./...` passed. |
| `npm ci` in `apps/frontend` | PASS | One deprecation warning only. |
| `npm run build` in `apps/frontend` | PASS | TypeScript and Vite build passed. |
| `npm test` in `apps/frontend` | PASS | 22 tests passed. |
| `python3 -m unittest discover -s tests -p 'test_*.py'` | PASS | 2 tests passed. |
| `PYTHON=.venv312/bin/python make status-test` | PASS | 8 tests passed, 1 warning. |
| `PYTHON=.venv312/bin/python make test` | PASS | Go, status pytest, and root unittest all passed. |
| `docker compose config` | PASS | Base Compose validates. |
| `docker compose -f docker-compose.yml -f docker-compose.demo.yml config` | PASS | Demo override validates and maps Postgres to host `15432`. |

## 6. Runtime Verification

Docker available: yes, with escalated daemon access.  
Stack started: yes.  
Campaign flow tested: yes.  
Sender-worker restart tested: yes.  
RabbitMQ restart tested: yes, for future campaign starts after broker recovery.  
Postgres restart tested: no.  
Retry tested: no failed item was produced in smoke campaigns.

Runtime results:

- `docker compose -f docker-compose.yml -f docker-compose.demo.yml up -d --build`: PASS.
- Services were up; Postgres, RabbitMQ, Redis healthy.
- Health endpoints:
  - `8085/health/ready`: `200`
  - `8086/health/ready`: `200`
  - `8087/health/ready`: `200`
  - `8090/health/ready`: `200`
- Baseline campaign `cmp-5721eda0e0176302`: finished, `2/2` sent.
- After sender-worker stop/start, campaign `cmp-a75bd1fa02c520bc`: finished, `2/2` sent.
- After RabbitMQ restart, campaign-service logged reconnect and campaign `cmp-ed48a23be7f1ccee`: finished, `2/2` sent.
- Parallel start probe `cmp-17725b7547b40b64`: one start returned `200`, one returned `409`; final deliveries exactly 2 rows.

The Compose stack was left running.

## 7. Merge Recommendation

Safe to commit: **yes**, as a demo-hardening patch.  
Safe to push: **yes**, if the team accepts the limitations below.  
Safe to merge to main: **PARTIAL**, acceptable for hackathon defense but not as a production reliability claim.

Required before merge if the team wants stricter correctness:

1. Add an outbox/attempt table for retry publish and provider send.
2. Add active-load retry/RabbitMQ fault tests.
3. Add a note in README/runbook that metrics are liveness-only on this branch.

Can wait until after defense:

1. Transactional outbox.
2. Processing/attempt table before provider send.
3. Exact selected-recipient backend contract.
4. Real metrics/performance pack.

## 8. Demo Recommendation

| Demo action | Safe? | Notes |
|---|---:|---|
| Create campaign through frontend | Safe | It immediately starts through status-service; say "create and launch". |
| Start campaign once | Safe | Backend atomic transition verified. |
| Double-click Start | Safe | Backend runtime probe returned one `200`, one `409`; frontend also locks pending action. |
| Retry one failed error group | Caution | It shares the atomic claim helper, but the runtime retry proof used campaign-level retry. Use campaign-level retry for the live proof unless error-group retry is separately checked. |
| Campaign-level retry | Safe with caveat | Forced-failure campaign retry repaired 2 failed rows to 2 sent rows after two parallel retry calls. Still not production outbox-grade. |
| Campaign-level switch channel | Disabled | Backend returns `409`; frontend shows explanatory text instead of a button. |
| User picker | Safe with wording | Present it as demo sampling/count-based, not exact recipient dispatch. |
| Sender-worker stop/start | Safe | Runtime verified. |
| RabbitMQ restart | Medium | Runtime verified for future campaign after restart, but not under active load. |
| Postgres restart | Avoid | Not tested; can break core persistence during demo. |
| L2 benchmark | Avoid as proof on this branch | Metrics/performance pack is not present on main. |

## 9. Exact Next Actions

Because the verdict is PARTIAL:

1. Commit this patch only if the team accepts it as demo hardening, not production correctness.
2. Before defense, run:

```bash
docker compose -f docker-compose.yml -f docker-compose.demo.yml ps
curl -fsS http://localhost:8085/health/ready
curl -fsS http://localhost:8086/health/ready
curl -fsS http://localhost:8087/health/ready
curl -fsS http://localhost:8090/health/ready
```

3. Avoid live-demoing Postgres restart.
4. Next coding fix should be transactional outbox or delivery attempt/processing lease.

Suggested commit command if accepted:

```bash
git add .gitignore Makefile README.md docker-compose.demo.yml apps/status-service/requirements-dev.txt services/campaign-service/main.go services/campaign-service/main_test.go services/sender-worker/main.go services/sender-worker/main_test.go apps/frontend/src/App.tsx apps/frontend/src/App.test.tsx apps/frontend/src/api.ts apps/frontend/src/styles.css tests/test_postgres_viewer.py docs/main_fix_plan.md docs/fixes/mega_pre_defense_fix.md docs/reviews/mega_pre_defense_fix_review.md
git commit -m "fix: harden campaign dispatch retry and demo safety"
```
