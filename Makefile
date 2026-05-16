GOCACHE ?= $(CURDIR)/.cache/go-build
DEMO_COMPOSE ?= docker compose -f docker-compose.yml -f docker-compose.demo.yml
PYTHON ?= python3

KUBECTL ?= kubectl
K8S_NAMESPACE ?= norify-demo
K8S_UI_PORT ?= 3000
K8S_FORWARD_PID ?= $(CURDIR)/.cache/k8s-frontend.port-forward.pid
K8S_FORWARD_PORT ?= $(CURDIR)/.cache/k8s-frontend.port-forward.port
K8S_FORWARD_LOG ?= $(CURDIR)/.cache/k8s-frontend.port-forward.log
REPLICAS ?= 2

.PHONY: test lint smoke go-test ops-test status-test frontend-test compose-test demo-up demo-down demo-ps demo-logs demo-metrics k8s-build k8s-up k8s-down k8s-apply k8s-migrate k8s-metrics k8s-wait k8s-smoke k8s-forward k8s-forward-stop k8s-stop-service k8s-start-service k8s-status k8s-url

test: go-test ops-test compose-test

go-test:
	mkdir -p $(GOCACHE)
	GOCACHE=$(GOCACHE) go test ./...

lint:
	GOCACHE=$(GOCACHE) go vet ./...

ops-test:
	PYTHONPATH=apps/ops-gateway $(PYTHON) -m pytest apps/ops-gateway/tests

status-test: ops-test

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
	$(DEMO_COMPOSE) logs --tail=100 campaign-service dispatcher-service sender-worker ops-gateway

demo-metrics:
	curl -fsS http://localhost:8085/metrics
	curl -fsS http://localhost:8086/metrics
	curl -fsS http://localhost:8087/metrics
	curl -fsS http://localhost:8090/metrics

k8s-build:
	docker build -t norify-auth-service:latest -f services/auth-service/Dockerfile .
	docker build -t norify-user-service:latest -f services/user-service/Dockerfile .
	docker build -t norify-template-service:latest -f services/template-service/Dockerfile .
	docker build -t norify-channel-service:latest -f services/channel-service/Dockerfile .
	docker build -t norify-campaign-service:latest -f services/campaign-service/Dockerfile .
	docker build -t norify-dispatcher-service:latest -f services/dispatcher-service/Dockerfile .
	docker build -t norify-sender-worker:latest -f services/sender-worker/Dockerfile .
	docker build -t norify-notification-error-service:latest -f services/notification-error-service/Dockerfile .
	docker build -t norify-stats-service:latest -f services/stats-service/Dockerfile .
	docker build -t norify-ops-gateway:latest apps/ops-gateway
	docker build -t norify-template-generator:latest services/template-generator
	docker build -t norify-frontend:latest apps/frontend

k8s-up: k8s-build k8s-apply k8s-migrate k8s-wait k8s-smoke k8s-forward k8s-status k8s-url

k8s-down: k8s-forward-stop
	-$(KUBECTL) delete namespace $(K8S_NAMESPACE)

k8s-apply:
	$(KUBECTL) apply -f deploy/k8s/namespace.yaml
	$(KUBECTL) -n $(K8S_NAMESPACE) create configmap postgres-migrations --from-file=migrations --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) apply -f deploy/k8s/config.yaml
	$(KUBECTL) apply -f deploy/k8s/stateful.yaml
	$(KUBECTL) apply -f deploy/k8s/services.yaml

k8s-migrate:
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/postgres --timeout=180s
	@pod="$$($(KUBECTL) -n $(K8S_NAMESPACE) get pod -l app=postgres -o jsonpath='{.items[0].metadata.name}')"; \
	for migration in migrations/*.sql; do \
		echo "applying $$migration"; \
		$(KUBECTL) -n $(K8S_NAMESPACE) exec -i "$$pod" -- psql -v ON_ERROR_STOP=1 -U norify -d norify < "$$migration"; \
	done

k8s-metrics:
	$(KUBECTL) apply -f deploy/k8s/metrics-server.yaml

k8s-wait:
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/postgres --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/rabbitmq --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/redis --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/auth-service --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/user-service --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/template-service --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/channel-service --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/campaign-service --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/dispatcher-service --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/notification-error-service --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/stats-service --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/ops-gateway --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/sender-worker --timeout=180s
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/frontend --timeout=180s

k8s-smoke:
	K8S_NAMESPACE=$(K8S_NAMESPACE) $(PYTHON) tests/runtime/k8s_smoke.py

k8s-forward:
	@mkdir -p $(dir $(K8S_FORWARD_PID))
	@if [ -f "$(K8S_FORWARD_PID)" ] && kill -0 "$$(cat "$(K8S_FORWARD_PID)")" 2>/dev/null; then \
		kill "$$(cat "$(K8S_FORWARD_PID)")" 2>/dev/null || true; \
	fi; \
	pkill -f "$(KUBECTL).* -n $(K8S_NAMESPACE) port-forward svc/frontend" 2>/dev/null || true; \
	rm -f "$(K8S_FORWARD_PID)" "$(K8S_FORWARD_PORT)"; \
	port="$(K8S_UI_PORT)"; \
	last_port=$$((port + 50)); \
	while lsof -nP -iTCP:$$port -sTCP:LISTEN >/dev/null 2>&1; do \
		port=$$((port + 1)); \
		if [ "$$port" -gt "$$last_port" ]; then \
			echo "no free frontend port found in $(K8S_UI_PORT)-$$last_port"; \
			exit 1; \
		fi; \
	done; \
	$(KUBECTL) -n $(K8S_NAMESPACE) wait --for=condition=ready pod -l app=frontend --timeout=180s; \
	rm -f "$(K8S_FORWARD_PID)" "$(K8S_FORWARD_PORT)" "$(K8S_FORWARD_LOG)"; \
	(nohup $(KUBECTL) -n $(K8S_NAMESPACE) port-forward svc/frontend $$port:80 >"$(K8S_FORWARD_LOG)" 2>&1 & echo $$! >"$(K8S_FORWARD_PID)"); \
	echo "$$port" >"$(K8S_FORWARD_PORT)"; \
	sleep 1; \
	if kill -0 "$$(cat "$(K8S_FORWARD_PID)")" 2>/dev/null && lsof -nP -iTCP:$$port -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "frontend available: http://localhost:$$port"; \
	else \
		cat "$(K8S_FORWARD_LOG)"; \
		rm -f "$(K8S_FORWARD_PID)" "$(K8S_FORWARD_PORT)"; \
		exit 1; \
	fi

k8s-forward-stop:
	@if [ -f "$(K8S_FORWARD_PID)" ] && kill -0 "$$(cat "$(K8S_FORWARD_PID)")" 2>/dev/null; then \
		kill "$$(cat "$(K8S_FORWARD_PID)")"; \
		rm -f "$(K8S_FORWARD_PID)" "$(K8S_FORWARD_PORT)"; \
		echo "frontend port-forward stopped"; \
	else \
		rm -f "$(K8S_FORWARD_PID)" "$(K8S_FORWARD_PORT)"; \
		pkill -f "$(KUBECTL).* -n $(K8S_NAMESPACE) port-forward svc/frontend" 2>/dev/null || true; \
		echo "frontend port-forward is not running"; \
	fi

k8s-stop-service:
	@test -n "$(SERVICE)" || (echo "Usage: make k8s-stop-service SERVICE=auth-service" && exit 1)
	$(KUBECTL) -n $(K8S_NAMESPACE) scale deployment/$(SERVICE) --replicas=0

k8s-start-service:
	@test -n "$(SERVICE)" || (echo "Usage: make k8s-start-service SERVICE=auth-service REPLICAS=2" && exit 1)
	$(KUBECTL) -n $(K8S_NAMESPACE) scale deployment/$(SERVICE) --replicas=$(REPLICAS)
	$(KUBECTL) -n $(K8S_NAMESPACE) rollout status deployment/$(SERVICE) --timeout=180s

k8s-status:
	$(KUBECTL) -n $(K8S_NAMESPACE) get pods
	$(KUBECTL) -n $(K8S_NAMESPACE) get hpa

k8s-url:
	@if [ -f "$(K8S_FORWARD_PID)" ] && kill -0 "$$(cat "$(K8S_FORWARD_PID)")" 2>/dev/null; then \
		port="$$(cat "$(K8S_FORWARD_PORT)" 2>/dev/null || echo "$(K8S_UI_PORT)")"; \
		if lsof -nP -iTCP:$$port -sTCP:LISTEN >/dev/null 2>&1; then \
			echo ""; \
			echo "frontend: http://localhost:$$port"; \
		else \
			rm -f "$(K8S_FORWARD_PID)" "$(K8S_FORWARD_PORT)"; \
			echo ""; \
			echo "frontend: port-forward is not running"; \
		fi; \
	else \
		rm -f "$(K8S_FORWARD_PID)" "$(K8S_FORWARD_PORT)"; \
		echo ""; \
		echo "frontend: port-forward is not running"; \
	fi
