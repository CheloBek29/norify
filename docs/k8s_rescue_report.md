# Kubernetes Rescue Report

Branch: `fix/k8s-demo-rescue`
Base commit: `b9c1bb9 work for vasiliy`

## Summary

The local Kubernetes deployment was not reliably runnable from `main`. The largest blockers were missing database initialization parity with Docker Compose, an incomplete service set, and brittle startup/image assumptions. The rescue pass repaired the local demo manifests and verified a real rollout in Docker Desktop Kubernetes.

## What Was Broken

| Problem | Evidence | Impact |
| --- | --- | --- |
| Namespace was hardcoded as `norify` while the requested demo namespace is `norify-demo`. | `deploy/k8s/*.yaml` used `namespace: norify`. | Runbook and commands would not target the same namespace. |
| PostgreSQL migrations were not mounted/applied by Kubernetes. | Compose mounts `./migrations:/docker-entrypoint-initdb.d`; K8s did not. | Pods could be Ready while app APIs failed against an empty DB. |
| `template-generator` was present in Compose/frontend assumptions but missing from K8s. | Compose has `template-generator`; frontend uses template generator API path; K8s omitted it. | Template generation demo path could fail in Kubernetes. |
| K8s build path did not guarantee local image names matching manifests. | Existing `k8s-build` delegated to Compose build behavior. | Local cluster could fail with ImagePullBackOff for `norify-*:latest`. |
| `k8s-up` installed metrics-server by default. | `k8s-up` depended on `k8s-metrics`. | Demo startup could fail or mutate cluster-wide resources unnecessarily. |
| RabbitMQ wait init containers used `busybox:1.36`. | Runtime events showed repeated `ImagePullBackOff` / `unexpected EOF` pulling `busybox:1.36`. | Campaign, dispatcher, sender, and stats pods stayed in init state. |
| Smoke test was only an inline frontend wget check. | Makefile `k8s-smoke` did not verify pod readiness or campaign flow. | It could miss broken API/runtime behavior. |

## Fixes Made

- Changed K8s namespace to `norify-demo`.
- Added Postgres migrations ConfigMap creation in `make k8s-apply`.
- Added explicit `make k8s-migrate`.
- Added `template-generator` Deployment and Service.
- Added template-generator URL and worker control config to `norify-config`.
- Reworked `make k8s-build` to build every image name referenced by manifests.
- Made metrics-server optional via `make k8s-metrics`; it is no longer part of default `k8s-up`.
- Replaced RabbitMQ wait init image from `busybox:1.36` to `postgres:16-alpine`, which is already needed and available for the demo.
- Added `tests/runtime/k8s_smoke.py`.
- Updated `make k8s-smoke` to run the Python smoke.

## Commands Run

| Command | Result | Notes |
| --- | --- | --- |
| `git clone https://github.com/CheloBek29/norify.git /private/tmp/norify-k8s-rescue` | PASS | Fresh checkout used. |
| `git checkout main && git pull --ff-only && git checkout -b fix/k8s-demo-rescue` | PASS | Branch based on latest `origin/main`. |
| `kubectl version --client` | PASS | Client v1.34.1. |
| `kubectl cluster-info` | PASS | Local Docker Desktop Kubernetes available. |
| `kubectl get nodes` | PASS | `desktop-control-plane` Ready. |
| `kubectl apply --dry-run=client -f deploy/k8s` | PASS | Static client validation passed after fixes. |
| `python3 -m py_compile tests/runtime/k8s_smoke.py` | PASS | Smoke script compiles. |
| `git diff --check` | PASS | No whitespace errors. |
| `make -n k8s-build && make -n k8s-apply && make -n k8s-smoke` | PASS | Targets expand correctly. |
| `make k8s-build` | PASS | Local demo images built. |
| `make k8s-apply` | PASS | Manifests applied in `norify-demo`. |
| `make k8s-migrate` | PASS | Migrations applied. |
| `make k8s-wait` | FAIL then PASS | First failure caused by `busybox:1.36` pull errors; passed after init image fix. |
| `make k8s-status` | PASS | All pods Running/Ready; HPA CPU metrics unknown without metrics-server. |
| `make k8s-smoke` | PASS | Pods Ready, frontend proxy OK, readiness OK, tiny campaign flow OK. |

## Runtime Result

Kubernetes runtime was actually tested on Docker Desktop Kubernetes.

`make k8s-status` showed all pods Ready in namespace `norify-demo`, including:

- campaign-service: 2/2 pods Ready
- dispatcher-service: 2/2 pods Ready
- sender-worker: 3/3 pods Ready
- frontend: 2/2 pods Ready
- PostgreSQL, RabbitMQ, Redis: Ready

`make k8s-smoke` result:

```text
PASS pods: all pods Ready in namespace norify-demo
PASS frontend proxy: /api/ops-gateway/ops/overview reachable from frontend pod
PASS campaign-service health: {'service': 'campaign-service', 'status': 'ready'}
PASS dispatcher-service health: {'service': 'dispatcher-service', 'status': 'ready'}
PASS sender-worker health: {'service': 'sender-worker', 'status': 'ready'}
PASS ops-gateway health: {'status': 'ready', 'service': 'status-service'}
PASS campaign flow: cmp-093f37757444816c sent=2 failed=1
K8S_SMOKE PASS
```

## Remaining Risks

- This is still a local demo deployment, not production Kubernetes.
- No PVCs are configured; Postgres and RabbitMQ data are ephemeral.
- HPA shows `cpu: <unknown>/70%` unless metrics-server is installed with `make k8s-metrics`.
- Local images are tagged `latest`; a multi-node/kind/minikube setup may need explicit image loading.
- No Ingress is configured; frontend access uses port-forwarding.
- Template generation requires an external Mistral API key for real AI generation.
- The smoke test verifies a small campaign only; it is not a load or fault-tolerance proof.

## Next Steps

1. Keep `make k8s-up` as the default local demo entrypoint.
2. Add PVCs only if persistent demo data becomes necessary.
3. Add a chart/Kustomize overlay later if multiple environments are needed.
4. Add CI static validation for `kubectl apply --dry-run=client -f deploy/k8s`.
5. If autoscaling is presented, install metrics-server and verify HPA metrics first.
