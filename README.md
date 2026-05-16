# Norify Notification Platform

Production-like notification platform scaffold for managers and admins.

## Stack

- Go microservices with shared contracts and TDD-covered core logic.
- Python FastAPI `status-service` for WebSocket/status snapshots only.
- Vite React TypeScript frontend.
- PostgreSQL, RabbitMQ, Redis.
- Docker Compose for local dev and raw Kubernetes manifests for production-like deploy.

## Local Run

```bash
docker compose up --build
```

Local seed credentials:

- `admin@example.com` / `admin123`
- `manager@example.com` / `manager123`

## Validation

```bash
make test
make lint
make smoke
```

`make smoke` expects services to be running through Docker Compose.

Frontend is available at http://localhost:3000. It includes login, campaign creation, template editing, audience preview, channel administration, live campaign progress, delivery table, statistics, manager RBAC, health, and system logs.

## PostgreSQL Viewer

After `docker compose up --build`, open pgweb at http://localhost:8089.

Compose starts pgweb with this connection string:

```text
postgres://norify:norify@postgres:5432/norify?sslmode=disable
```

For desktop database clients use `localhost:5432` with the same username, password, and database.
