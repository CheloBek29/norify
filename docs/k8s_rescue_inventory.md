# Kubernetes Rescue Inventory

Branch: `fix/k8s-demo-rescue`
Namespace: `norify-demo`

## Services

| Component | Kind | Image | Container port | Kubernetes Service | Dependencies | Criticality | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| frontend | Deployment | `norify-frontend:latest` | 80 | `frontend:80` | API services through nginx proxy | Critical for demo UI | Expose locally with `make k8s-forward`. |
| auth-service | Deployment | `norify-auth-service:latest` | 8080 | `auth-service:8080` | PostgreSQL config/JWT secret | Critical | `/health/live`, `/health/ready`. |
| user-service | Deployment | `norify-user-service:latest` | 8080 | `user-service:8080` | PostgreSQL | Critical for recipients | `/health/live`, `/health/ready`. |
| template-service | Deployment | `norify-template-service:latest` | 8080 | `template-service:8080` | PostgreSQL | Critical for templates | Waits for Postgres. |
| channel-service | Deployment | `norify-channel-service:latest` | 8080 | `channel-service:8080` | PostgreSQL | Critical for channels | Waits for Postgres. |
| campaign-service | Deployment | `norify-campaign-service:latest` | 8080 | `campaign-service:8080` | PostgreSQL, RabbitMQ | Critical business core | Waits for Postgres and RabbitMQ. |
| dispatcher-service | Deployment | `norify-dispatcher-service:latest` | 8080 | `dispatcher-service:8080` | PostgreSQL, RabbitMQ | Critical delivery fan-out | Waits for RabbitMQ. |
| sender-worker | Deployment + HPA | `norify-sender-worker:latest` | 8080 | `sender-worker:8080` | PostgreSQL, RabbitMQ | Critical execution | Starts with 3 replicas; HPA max 20. |
| notification-error-service | Deployment | `norify-notification-error-service:latest` | 8080 | `notification-error-service:8080` | PostgreSQL | Important | Error/reporting API. |
| stats-service | Deployment | `norify-stats-service:latest` | 8080 | `stats-service:8080` | PostgreSQL, RabbitMQ | Important | Runtime statistics API. |
| ops-gateway | Deployment | `norify-ops-gateway:latest` | 8080 | `ops-gateway:8080` | Stats service, K8s API, services | Important | Frontend operational gateway. |
| template-generator | Deployment | `norify-template-generator:latest` | 8003 | `template-generator:8003` | Optional Mistral API key | Optional/demo helper | Added to K8s to match Compose/frontend expectations. |
| postgres | Deployment | `postgres:16-alpine` | 5432 | `postgres:5432` | ConfigMap migrations | Critical | Demo uses `emptyDir`; not persistent. |
| rabbitmq | Deployment | `rabbitmq:3.13-management-alpine` | 5672, 15672 | `rabbitmq:5672`, `rabbitmq:15672` | none | Critical queue | Demo uses `emptyDir`; not persistent. |
| redis | Deployment | `redis:7-alpine` | 6379 | `redis:6379` | none | Partial degradation | Runtime helper/cache. |

No Postgres viewer is deployed in Kubernetes. Use `kubectl exec` into the Postgres pod or local port-forwarding for database inspection.

## Configuration

| Object | Purpose |
| --- | --- |
| `ConfigMap/norify-config` | Internal service URLs and dependency DSNs using cluster DNS. |
| `Secret/norify-secret` | Demo JWT secret only. Not production-safe. |
| `ConfigMap/postgres-migrations` | Built by `make k8s-apply` from `migrations/*.sql` and mounted into Postgres init directory. |
| `ServiceAccount/ops-gateway` + Role/RoleBinding | Allows ops-gateway to patch the sender-worker HPA only. |

## Dependency Graph

```text
frontend
  -> auth-service
  -> user-service
  -> template-service
  -> channel-service
  -> campaign-service
  -> notification-error-service
  -> ops-gateway

campaign-service -> PostgreSQL
campaign-service -> RabbitMQ
dispatcher-service -> RabbitMQ
dispatcher-service -> PostgreSQL
sender-worker -> RabbitMQ
sender-worker -> PostgreSQL
stats-service -> PostgreSQL
stats-service -> RabbitMQ
ops-gateway -> stats-service
ops-gateway -> Kubernetes HPA API
template-generator -> external Mistral API only if configured
```

## Local Demo Ports

Kubernetes Services are internal ClusterIP services. Use port-forwarding:

| Local URL | Command |
| --- | --- |
| `http://localhost:3000` frontend | `make k8s-forward` |
| `http://localhost:8085` campaign-service | `kubectl -n norify-demo port-forward svc/campaign-service 8085:8080` |
| `http://localhost:8086` dispatcher-service | `kubectl -n norify-demo port-forward svc/dispatcher-service 8086:8080` |
| `http://localhost:8087` sender-worker | `kubectl -n norify-demo port-forward svc/sender-worker 8087:8080` |
| `http://localhost:8090` ops-gateway/status facade | `kubectl -n norify-demo port-forward svc/ops-gateway 8090:8080` |

## Criticality Levels

- Critical dependency failure: PostgreSQL, RabbitMQ, campaign-service, dispatcher-service, sender-worker.
- Partial degradation: Redis, stats-service, notification-error-service, ops-gateway.
- Safe degradation for core delivery: template-generator, frontend after API services keep running.

## Current Limitations

- This is a local demo deployment, not a production chart.
- PostgreSQL and RabbitMQ use `emptyDir`; data is lost when pods/namespaces are deleted.
- Images are local `norify-*:latest`; kind/minikube users may need to load images manually.
- HPA CPU metrics require metrics-server. `make k8s-up` no longer installs metrics-server automatically; use `make k8s-metrics` if autoscaling metrics are needed.
