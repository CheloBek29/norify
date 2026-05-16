#!/usr/bin/env sh
set -eu

check() {
  name="$1"
  url="$2"
  curl -fsS "$url" >/dev/null
  echo "ok $name"
}

check auth-service http://localhost:8081/health/live
check user-service http://localhost:8082/health/live
check template-service http://localhost:8083/health/live
check channel-service http://localhost:8084/health/live
check campaign-service http://localhost:8085/health/live
check dispatcher-service http://localhost:8086/health/live
check sender-worker http://localhost:8087/health/live
check notification-error-service http://localhost:8088/health/live
check ops-gateway http://localhost:8090/health/live
check stats-service http://localhost:8092/health/live
