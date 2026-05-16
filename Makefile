GOCACHE ?= $(CURDIR)/.cache/go-build
DEMO_COMPOSE ?= docker compose -f docker-compose.yml -f docker-compose.demo.yml
PYTHON ?= python3

.PHONY: test lint smoke go-test status-test frontend-test compose-test demo-up demo-down demo-ps demo-logs demo-metrics

test: go-test status-test compose-test

go-test:
	mkdir -p $(GOCACHE)
	GOCACHE=$(GOCACHE) go test ./...

lint:
	GOCACHE=$(GOCACHE) go vet ./...

status-test:
	PYTHONPATH=apps/status-service $(PYTHON) -m pytest apps/status-service/tests

compose-test:
	$(PYTHON) -m unittest discover -s tests -p 'test_*.py'

frontend-test:
	cd apps/frontend && npm test

smoke:
	./tests/smoke/health.sh

demo-up:
	$(DEMO_COMPOSE) up -d --build

demo-down:
	$(DEMO_COMPOSE) down

demo-ps:
	$(DEMO_COMPOSE) ps

demo-logs:
	$(DEMO_COMPOSE) logs --tail=100 campaign-service dispatcher-service sender-worker status-service

demo-metrics:
	curl -fsS http://localhost:8085/metrics
	curl -fsS http://localhost:8086/metrics
	curl -fsS http://localhost:8087/metrics
	curl -fsS http://localhost:8090/metrics
