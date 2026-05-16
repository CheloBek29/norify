# Chaos Test API Contract

This document records the current `main` branch API surface used by `tests/chaos/chaos_duplicate_test.py`.

## Services and Endpoints

| Capability | Method | Endpoint | Sample request | Sample response shape | Used by |
|---|---|---|---|---|---|
| Campaign readiness | GET | `http://localhost:8085/health/ready` | none | service health JSON from common mux | readiness |
| Dispatcher readiness | GET | `http://localhost:8086/health/ready` | none | service health JSON from common mux | readiness |
| Sender readiness | GET | `http://localhost:8087/health/ready` | none | service health JSON from common mux | readiness |
| Status readiness | GET | `http://localhost:8090/health/ready` | none | service health JSON | readiness |
| Create campaign | POST | `/campaigns` | `{"name":"chaos","template_id":"tpl-reactivation","filters":{},"selected_channels":["custom_app","email"],"total_recipients":10}` | campaign object with `id`, `status`, counts, selected channels | happy path, duplicate tests, fault tests |
| Get campaign | GET | `/campaigns/{id}` | none | campaign object with `status`, `total_messages`, `sent_count`, `success_count`, `failed_count` | polling, consistency checks |
| Start campaign | POST | `/campaigns/{id}/start` | none | campaign object or error JSON | happy path, parallel start |
| List deliveries | GET | `/campaigns/{id}/deliveries` | none | array of latest delivery rows: `id`, `campaign_id`, `user_id`, `channel_code`, `status`, `attempt` | duplicate detection |
| List error groups | GET | `/campaigns/{id}/error-groups` | none | array of error groups | retry discovery |
| Retry failed campaign | POST | `/campaigns/{id}/retry-failed` | none | campaign object | forced failure/retry, if failure setup exists |
| Campaign switch-channel | POST | `/campaigns/{id}/switch-channel` | `{"from":"telegram","to":"email"}` | campaign object | not used by chaos script because current main path is known unsafe/synthetic |
| Error-group retry | POST | `/campaigns/{id}/error-groups/{group_id}/retry` | none | action result with queued count | documented, not used by default |
| Error-group switch-channel | POST | `/campaigns/{id}/error-groups/{group_id}/switch-channel` | `{"to_channel":"email"}` | action result with queued count | documented, not used by default |
| Channel list | GET | `/channels` | none | channel config with delivery counters | API discovery only |
| Channel patch | PATCH | `/channels/{code}` | channel config payload | updated channel config | not used by default; mutating channel reliability needs a reset protocol |
| Worker stats | GET | `/worker/stats` | none | active worker count, queue depth | optional manual inspection |

## Important Contract Limitations

- `/campaigns/{id}/deliveries` returns `LIMIT 500`, so HTTP duplicate detection is incomplete for large scenarios.
- Current `main` campaign creation accepts `total_recipients` and selected channel codes. It does not accept exact selected user IDs for end-to-end fan-out.
- Current `main` dispatcher generates synthetic user IDs as `user-00001`, `user-00002`, etc.
- Current `main` has no deterministic force-failure endpoint. Channel mutation could lower `success_probability`, but the chaos script does not mutate channel reliability by default because there is no safe reset protocol.
- Current `main` campaign-level `/switch-channel` republishes synthetic dispatch and should not be used as a safe duplicate/retry proof.
- Retry and switch-channel scenarios are treated as skipped unless explicit safe support is added.

## Docker Control Contract

Container fault scenarios use only safe Compose commands and only when `CHAOS_ALLOW_CONTAINER_CONTROL=true`:

```bash
docker compose stop sender-worker
docker compose start sender-worker
docker compose restart rabbitmq
```

The script never runs `docker compose down -v` and never deletes volumes.
