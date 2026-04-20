#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

COMPOSE_FILE=${COMPOSE_FILE:-$SCRIPT_DIR/docker-compose.yml}
COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-postgres-sync-go-e2e-$(date +%Y%m%d%H%M%S)}
PARALLEL_DIR="$ARTIFACTS_DIR/docker-parallel"

compose_cmd() {
  docker compose -p "$COMPOSE_PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
}

ensure_compare_compose_source() {
  if [ ! -d "$COMPARE_SYNC_DIR" ]; then
    comparison_unavailable
  fi
  if [ -z "$COMPARE_TELEMETRY_DIR" ] || [ ! -d "$COMPARE_TELEMETRY_DIR" ]; then
    cat >&2 <<EOF
[e2e] comparison telemetry source unavailable
[e2e] Set COMPARE_TELEMETRY_DIR to the comparison telemetry package checkout.
[e2e] Current COMPARE_TELEMETRY_DIR: ${COMPARE_TELEMETRY_DIR:-<empty>}
EOF
    exit 77
  fi
  export COMPARE_SYNC_DIR
  export COMPARE_TELEMETRY_DIR
}

cleanup() {
  mkdir -p "$PARALLEL_DIR"
  capture_pg_debug "$PARALLEL_DIR/final-state" || true
  compose_cmd logs --no-color postgres-sync-go >"$PARALLEL_DIR/postgres-sync-go.log" 2>&1 || true
  compose_cmd logs --no-color electric >"$PARALLEL_DIR/electric.log" 2>&1 || true
  compose_cmd down >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

main() {
  ensure_common_requirements
  require_cmd docker
  ensure_compare_compose_source
  configure_one_off_docker_ports

  log "using one-off host ports: postgres=$DB_PORT postgres-sync-go=$SYNC_GO_PORT electric=$COMPARE_PORT"
  log "using compose project: $COMPOSE_PROJECT_NAME"

  compose_cmd up -d postgres >/dev/null
  wait_for_postgres
  reset_database
  capture_pg_debug "$PARALLEL_DIR/db-before"

  compose_cmd up -d --build postgres-sync-go electric >/dev/null
  wait_for_active_health "http://127.0.0.1:${SYNC_GO_PORT}/v1/health"
  wait_for_active_health "http://127.0.0.1:${COMPARE_PORT}/v1/health"
  capture_pg_debug "$PARALLEL_DIR/db-after-start"

  cat <<EOF

postgres-sync-go and Electric are running in Docker.

postgres-sync-go:
  base url: http://127.0.0.1:${SYNC_GO_PORT}
  health:   http://127.0.0.1:${SYNC_GO_PORT}/v1/health

Electric:
  base url: http://127.0.0.1:${COMPARE_PORT}
  health:   http://127.0.0.1:${COMPARE_PORT}/v1/health

Manual curl helper:
  $SCRIPT_DIR/manual_curls.sh
  Example:
    BASE_URL=http://127.0.0.1:${SYNC_GO_PORT} $SCRIPT_DIR/manual_curls.sh initial-items

Artifacts:
  $PARALLEL_DIR

Streaming service logs below. Press Ctrl-C to stop both containers.
EOF

  compose_cmd logs -f postgres-sync-go electric
}

main
