GOCACHE ?= $(CURDIR)/.cache/go-build

.PHONY: test lint smoke go-test status-test frontend-test compose-test

test: go-test status-test compose-test

go-test:
	mkdir -p $(GOCACHE)
	GOCACHE=$(GOCACHE) go test ./...

lint:
	GOCACHE=$(GOCACHE) go vet ./...

status-test:
	cd apps/status-service && python3 -m pytest tests

compose-test:
	python3 -m unittest discover -s tests -p 'test_*.py'

frontend-test:
	cd apps/frontend && npm test

smoke:
	./tests/smoke/health.sh
