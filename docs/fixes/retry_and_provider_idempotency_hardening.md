# Retry and Provider Idempotency Hardening

Date: 2026-05-16

## Problems Targeted

1. Retry row claiming was not atomic enough. Two retry requests could select the same failed deliveries and publish duplicate retry work.
2. Sender-worker called the provider stub before checking whether a delivery was already terminal.
3. Campaign-level switch-channel still used synthetic campaign dispatch and was unsafe for demo.
4. Retry had not been runtime-proven because previous smoke campaigns did not produce failures.

## Files Changed

- `services/campaign-service/main.go`
- `services/campaign-service/main_test.go`
- `services/sender-worker/main.go`
- `services/sender-worker/main_test.go`
- `apps/frontend/src/App.tsx`
- `apps/frontend/src/styles.css`
- `docs/main_fix_plan.md`
- `docs/reviews/mega_pre_defense_fix_review.md`
- `docs/fixes/retry_and_provider_idempotency_hardening.md`

## What Was Fixed

### Atomic Retry Claiming

`POST /campaigns/{id}/retry-failed` and error-group retry now claim failed rows before publishing retry work.

Implementation:

- Uses a transaction.
- Uses `UPDATE ... RETURNING` with `FOR UPDATE SKIP LOCKED`.
- Claims only rows with `status IN ('failed', 'error')`.
- Moves claimed rows to `queued`.
- Adjusts campaign counters using the actual number of rows returned.
- Publishes retry work only for the claimed rows.

This means two concurrent retry requests should not claim or publish the same failed delivery rows.

Remaining limitation:

- There is still no durable delivery-attempt table.
- If publishing partially succeeds and then fails, some retry messages may already be in RabbitMQ while the service restores rows to `failed`. The sender-side idempotency guard reduces visible DB duplicates, but this is still not a full outbox/attempt model.

### Sender-Worker Provider Send Guard

Sender-worker now checks existing delivery state before calling the provider stub.

It skips provider send when:

- the delivery is already `sent`;
- the delivery is `cancelled`;
- the existing failed/queued attempt is newer or equal to the incoming attempt;
- the state is otherwise not safely sendable.

This reduces duplicate external sends on stale redelivery.

Remaining limitation:

- This is a conservative pre-send guard, not exactly-once delivery. There is still a race if two duplicate messages for a never-before-seen idempotency key arrive at exactly the same time. A production-grade fix needs an atomic `queued -> processing` claim/lease or a delivery attempts table.

### Campaign-Level Switch-Channel Disabled

The unsafe campaign-level switch-channel endpoint now returns:

- HTTP `409`
- `campaign_switch_channel_disabled`
- Russian explanation that whole-campaign channel switching is disabled for demo because exact routing by failed delivery is required.

The frontend no longer renders the campaign-level switch-channel button. It shows:

`Смена канала для всей кампании отключена в демо: используйте группы ошибок.`

Error-group switch-channel remains available.

## Tests Run

### Go

```bash
GOCACHE=/private/tmp/norify-origin-main-review/.cache/go-build go test ./services/campaign-service
GOCACHE=/private/tmp/norify-origin-main-review/.cache/go-build go test ./services/sender-worker
GOCACHE=/private/tmp/norify-origin-main-review/.cache/go-build go test ./services/dispatcher-service
GOCACHE=/private/tmp/norify-origin-main-review/.cache/go-build go test ./...
make lint
```

Result: PASS.

Note: direct `go test ./services/campaign-service` and `go test ./services/sender-worker` first hit a macOS Go cache permission error under `/Users/lisix/Library/Caches/go-build`. Rerunning with repo-local `GOCACHE` passed.

### Frontend

```bash
cd apps/frontend
npm ci
npm run build
npm test
```

Result: PASS. `npm test` passed 22 tests.

### Python

```bash
python3 -m unittest discover -s tests -p 'test_*.py'
PYTHON=.venv312/bin/python make status-test
PYTHON=.venv312/bin/python make test
```

Result: PASS.

### Compose

```bash
docker compose config
docker compose -f docker-compose.yml -f docker-compose.demo.yml config
```

Result: PASS.

## Runtime Verification

Runtime used:

```bash
docker compose -f docker-compose.yml -f docker-compose.demo.yml up -d --build
```

Health checks passed:

- `http://localhost:8085/health/ready`
- `http://localhost:8086/health/ready`
- `http://localhost:8087/health/ready`
- `http://localhost:8090/health/ready`

### Normal Campaign

Campaign: `cmp-f0b99f4232734ecc`

- Expected messages: 2
- Final status: `finished`
- `sent_count`: 2
- `success_count`: 2
- `failed_count`: 0

### Forced Failure and Retry

Failure setup used existing channel configuration, not fake UI state:

```bash
curl -X PATCH http://localhost:8084/channels/email \
  -H 'Content-Type: application/json' \
  -d '{"code":"email","name":"Email","enabled":true,"success_probability":0,"min_delay_seconds":1,"max_delay_seconds":1,"max_parallelism":180,"retry_limit":1}'
```

Campaign: `cmp-319eca0e952605c1`

Before retry:

- Expected messages: 2
- Final status: `finished`
- `sent_count`: 2
- `success_count`: 0
- `failed_count`: 2
- Delivery rows: 2 failed rows, both `attempt=1`
- Error groups: 1 group, `failed_count=2`

Then email was restored to forced success for retry:

```bash
curl -X PATCH http://localhost:8084/channels/email \
  -H 'Content-Type: application/json' \
  -d '{"code":"email","name":"Email","enabled":true,"success_probability":1,"min_delay_seconds":1,"max_delay_seconds":1,"max_parallelism":180,"retry_limit":3}'
```

Two parallel retry requests were issued:

```bash
printf '%s\n%s\n' 1 2 | xargs -n1 -P2 -I{} \
  curl -sS -w '\nHTTP:%{http_code}\n' \
  -X POST http://localhost:8085/campaigns/cmp-319eca0e952605c1/retry-failed
```

After retry:

- Final status: `finished`
- `sent_count`: 2
- `success_count`: 2
- `failed_count`: 0
- Delivery rows: still exactly 2 rows
- Both rows are `sent`
- Both rows are `attempt=2`
- Error groups: empty list

Email channel was restored to normal demo config afterward:

```bash
curl -X PATCH http://localhost:8084/channels/email \
  -H 'Content-Type: application/json' \
  -d '{"code":"email","name":"Email","enabled":true,"success_probability":0.96,"min_delay_seconds":2,"max_delay_seconds":60,"max_parallelism":180,"retry_limit":3}'
```

### Campaign-Level Switch-Channel Disabled

Runtime check:

```bash
curl -X POST http://localhost:8085/campaigns/cmp-319eca0e952605c1/switch-channel \
  -H 'Content-Type: application/json' \
  -d '{"from":"email","to":"sms"}'
```

Result:

- HTTP `409`
- `campaign_switch_channel_disabled`

### Fault Checks

Sender-worker stop/start:

- `docker compose -f docker-compose.yml -f docker-compose.demo.yml stop sender-worker`
- `docker compose -f docker-compose.yml -f docker-compose.demo.yml start sender-worker`
- Campaign `cmp-0b05af310417cdbd` completed `2/2` after restart.

RabbitMQ restart:

- `docker compose -f docker-compose.yml -f docker-compose.demo.yml restart rabbitmq`
- Campaign-service, dispatcher, and sender-worker readiness returned `ready`.
- Campaign `cmp-6b4922e6535318e6` completed `2/2` after broker restart.

## Demo Instructions

Safe retry demo:

1. Start stack:

```bash
docker compose -f docker-compose.yml -f docker-compose.demo.yml up -d --build
```

2. Force email failure explicitly through channel-service.
3. Create and start a small email-only campaign.
4. Show failed rows and error group.
5. Restore email success probability.
6. Click retry once or call `/retry-failed`.
7. Show the same delivery rows become `sent` at `attempt=2`.

Do not present this as exactly-once external delivery. Present it as demo-safe retry claiming and visible delivery repair.

## Unsafe Actions After This Pass

- Do not claim full exactly-once provider delivery.
- Do not kill campaign-service exactly between DB transition and RabbitMQ publish.
- Do not demo Postgres restart under active load.
- Do not claim exact selected-user fan-out; user picker remains demo sampling/count-based.

## Next Production-Grade Fix

1. Add a transactional outbox for campaign dispatch and retry publish.
2. Add delivery attempt records or an atomic processing lease before provider send.
3. Add exact selected-recipient persistence and dispatcher fan-out.
4. Add integration tests for active-load RabbitMQ restart and retry concurrency.
