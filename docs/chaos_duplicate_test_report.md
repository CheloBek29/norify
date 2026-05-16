# Chaos Duplicate Test Report

No runtime chaos scenario has been executed from this branch yet.

This file is intentionally committed as the human-readable report destination for `tests/chaos/chaos_duplicate_test.py`. A real run overwrites it.

## How To Generate

Safe HTTP-only run:

```bash
make chaos-test-safe
```

Worker stop/start run:

```bash
CHAOS_ALLOW_CONTAINER_CONTROL=true CHAOS_STOP_WORKER=true CHAOS_RESTART_WORKER=true make chaos-test-worker-fault
```

RabbitMQ restart after recovery run:

```bash
CHAOS_ALLOW_CONTAINER_CONTROL=true CHAOS_RESTART_RABBITMQ=true CHAOS_SKIP_UNSAFE_RABBITMQ=false make chaos-test-rabbitmq-after-restart
```

## Current Status

- Runtime status: not run in this committed placeholder.
- Duplicate result: unavailable until a real run is executed.
- Fault result: unavailable until a real run is executed.

See `docs/chaos_duplicate_testing.md` and `docs/chaos_test_api_contract.md` for the test design and API contract.
