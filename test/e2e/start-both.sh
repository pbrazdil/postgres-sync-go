#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

START_POSTGRES=${START_POSTGRES:-1}
PARALLEL_DIR="$ARTIFACTS_DIR/parallel"
SYNC_GO_PID=""
COMPARE_PID=""

cleanup() {
  capture_pg_debug "$PARALLEL_DIR/final-state" || true
  stop_service "$SYNC_GO_PID"
  stop_service "$COMPARE_PID"
}
trap cleanup EXIT INT TERM

main() {
  ensure_common_requirements
  ensure_electric_requirements

  if [ "$START_POSTGRES" = "1" ]; then
    start_postgres_dev
  else
    wait_for_postgres
  fi

  reset_database
  capture_pg_debug "$PARALLEL_DIR/db-before"

  start_postgres_sync_go "$PARALLEL_DIR/postgres-sync-go"
  SYNC_GO_PID=$SERVICE_PID

  start_electric "$PARALLEL_DIR/electric"
  COMPARE_PID=$SERVICE_PID

  capture_pg_debug "$PARALLEL_DIR/db-after-start"

  cat <<EOF

postgres-sync-go and Electric are running side by side.

postgres-sync-go:
  base url: http://127.0.0.1:${SYNC_GO_PORT}
  health:   http://127.0.0.1:${SYNC_GO_PORT}/v1/health
  log:      $PARALLEL_DIR/postgres-sync-go/service.log

Electric:
  base url: http://127.0.0.1:${COMPARE_PORT}
  health:   http://127.0.0.1:${COMPARE_PORT}/v1/health
  log:      $PARALLEL_DIR/electric/service.log

Manual curl helper:
  $SCRIPT_DIR/manual_curls.sh

Artifacts:
  $PARALLEL_DIR

Press Ctrl-C to stop both services.
EOF

  while true; do
    sleep 1
  done
}

main
