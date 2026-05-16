# Kubernetes Demo Runbook

Namespace: `norify-demo`

## Build Images

```bash
make k8s-build
```

This builds the local images referenced by the manifests, including frontend, ops-gateway, stats-service, and template-generator.

For Docker Desktop Kubernetes, `imagePullPolicy: IfNotPresent` can use local images. For kind/minikube, load images into the cluster runtime before applying.

## Apply Manifests

```bash
make k8s-apply
make k8s-migrate
make k8s-wait
```

`make k8s-apply` also creates `ConfigMap/postgres-migrations` from `migrations/*.sql`. `make k8s-migrate` applies migrations explicitly and is safe to rerun because migrations use idempotent DDL where practical.

One-command local flow:

```bash
make k8s-up
```

`make k8s-up` builds, applies, migrates, waits, runs smoke, starts frontend port-forward, and prints the frontend URL.

## Check Status

```bash
make k8s-status
kubectl -n norify-demo get all
kubectl -n norify-demo get events --sort-by=.lastTimestamp
```

## Port Forward

Frontend:

```bash
make k8s-forward
make k8s-url
```

API examples:

```bash
kubectl -n norify-demo port-forward svc/campaign-service 8085:8080
kubectl -n norify-demo port-forward svc/dispatcher-service 8086:8080
kubectl -n norify-demo port-forward svc/sender-worker 8087:8080
kubectl -n norify-demo port-forward svc/ops-gateway 8090:8080
```

## Smoke Test

```bash
make k8s-smoke
```

The smoke test verifies:

- all pods are Ready;
- frontend can reach ops-gateway through nginx proxy inside the cluster;
- health/ready endpoints if port-forwarded URLs are available;
- a tiny campaign create/start flow if campaign-service is reachable.

## Debug CrashLoopBackOff

```bash
kubectl -n norify-demo get pods
kubectl -n norify-demo describe pod <pod>
kubectl -n norify-demo logs <pod> --tail=100
kubectl -n norify-demo get events --sort-by=.lastTimestamp
```

Common causes:

- image not built or not available to cluster;
- missing Postgres migrations;
- RabbitMQ/Postgres not ready yet;
- wrong internal service URL in `norify-config`;
- probe path does not match the service.

## Debug Service DNS

From a running pod:

```bash
kubectl -n norify-demo exec deploy/frontend -- wget -qO- http://ops-gateway:8080/health/ready
kubectl -n norify-demo exec deploy/campaign-service -- wget -qO- http://rabbitmq:15672
```

If DNS fails, inspect Service selectors:

```bash
kubectl -n norify-demo get svc
kubectl -n norify-demo get endpoints
kubectl -n norify-demo get deploy --show-labels
```

## Debug Dependency Connections

PostgreSQL:

```bash
kubectl -n norify-demo exec deploy/postgres -- pg_isready -U norify -d norify
kubectl -n norify-demo exec -it deploy/postgres -- psql -U norify -d norify
```

RabbitMQ:

```bash
kubectl -n norify-demo exec deploy/rabbitmq -- rabbitmq-diagnostics ping
kubectl -n norify-demo logs deploy/rabbitmq --tail=100
```

Redis:

```bash
kubectl -n norify-demo exec deploy/redis -- redis-cli ping
```

## Safe Demo Flow

1. Run `make k8s-up`.
2. Open the printed frontend URL.
3. Create/start a small campaign.
4. Use `make k8s-status` to show healthy pods.
5. Run `make k8s-smoke` to show automated verification.

## Unsafe Demo Actions

- Do not delete the namespace during the live demo unless you are intentionally resetting all demo data.
- Do not claim persistence after pod deletion; Postgres/RabbitMQ use `emptyDir`.
- Do not rely on HPA scale decisions unless `make k8s-metrics` has been installed and metrics are visible.
- Do not call this a production Kubernetes setup; it is a local demo deployment.

## Cleanup

```bash
make k8s-down
```

This deletes the `norify-demo` namespace. It does not touch Docker Compose stacks.
