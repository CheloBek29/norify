GOCACHE ?= $(CURDIR)/.cache/go-build

PYTHON ?= python3

.PHONY: test lint smoke go-test status-test frontend-test compose-test chaos-test chaos-test-safe chaos-test-worker-fault chaos-test-rabbitmq-after-restart

test: go-test status-test compose-test

go-test:
	mkdir -p $(GOCACHE)
	GOCACHE=$(GOCACHE) go test ./...

lint:
	GOCACHE=$(GOCACHE) go vet ./...

status-test:
	cd apps/status-service && $(PYTHON) -m pytest tests

compose-test:
	$(PYTHON) -m unittest discover -s tests -p 'test_*.py'

frontend-test:
	cd apps/frontend && npm test

smoke:
	./tests/smoke/health.sh

chaos-test:
	$(PYTHON) tests/chaos/chaos_duplicate_test.py

chaos-test-safe:
	CHAOS_ALLOW_CONTAINER_CONTROL=false $(PYTHON) tests/chaos/chaos_duplicate_test.py

chaos-test-worker-fault:
	CHAOS_ALLOW_CONTAINER_CONTROL=true CHAOS_STOP_WORKER=true CHAOS_RESTART_WORKER=true $(PYTHON) tests/chaos/chaos_duplicate_test.py

chaos-test-rabbitmq-after-restart:
	CHAOS_ALLOW_CONTAINER_CONTROL=true CHAOS_RESTART_RABBITMQ=true CHAOS_SKIP_UNSAFE_RABBITMQ=false $(PYTHON) tests/chaos/chaos_duplicate_test.py
