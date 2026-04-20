#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

COMPOSE_FILE=${COMPOSE_FILE:-$SCRIPT_DIR/docker-compose.yml}
COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-pulsesync-validate-$(date +%Y%m%d%H%M%S)}
VALIDATION_DIR="$ARTIFACTS_DIR/pulsesync-validate-docker"
SCENARIOS=("$@")
if [ ${#SCENARIOS[@]} -eq 0 ]; then
  SCENARIOS=(
    disk_restart_continuity
    disk_corrupt_shape_recovery
    reconnect_health_and_continuation
  )
fi

compose_cmd() {
  docker compose -p "$COMPOSE_PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
}

cleanup() {
  compose_cmd down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

assert_equals() {
  local actual=$1
  local expected=$2
  local message=$3
  if [ "$actual" != "$expected" ]; then
    echo "$message: expected '$expected', got '$actual'" >&2
    exit 1
  fi
}

assert_not_equals() {
  local left=$1
  local right=$2
  local message=$3
  if [ "$left" = "$right" ]; then
    echo "$message: both sides were '$left'" >&2
    exit 1
  fi
}

assert_contains() {
  local file=$1
  local pattern=$2
  local message=$3
  if ! grep -Fq -- "$pattern" "$file"; then
    echo "$message: missing pattern '$pattern' in $file" >&2
    exit 1
  fi
}

sanitize_storage_handle() {
  local handle=$1
  handle=$(printf '%s' "$handle" | tr -c '[:alnum:]' '_')
  handle=$(printf '%s' "$handle" | sed -E 's/^_+//; s/_+$//')
  if [ -z "$handle" ]; then
    handle=shape
  fi
  printf '%s\n' "$handle"
}

find_persisted_chunk() {
  local handle=$1
  local sanitized
  sanitized=$(sanitize_storage_handle "$handle")
  find "$PULSE_STORAGE_BIND_DIR/chunks/$sanitized" -name '*.json' | sort | head -n 1
}

set_pulsesync_storage() {
  local mode=$1
  local bind_dir=$2
  PULSE_STORAGE_MODE=$mode
  PULSE_STORAGE_DIR=/var/lib/pulsesync
  PULSE_STORAGE_BIND_DIR=$bind_dir
  export PULSE_STORAGE_MODE
  export PULSE_STORAGE_DIR
  export PULSE_STORAGE_BIND_DIR
}

reset_stack() {
  compose_cmd down -v >/dev/null 2>&1 || true
  rm -rf "$PULSE_STORAGE_BIND_DIR"
  mkdir -p "$PULSE_STORAGE_BIND_DIR"
  compose_cmd up -d postgres >/dev/null
  wait_for_postgres
  reset_database
}

start_pulsesync_compose() {
  local dir=$1
  mkdir -p "$dir"
  log "starting dockerized PulseSync on port $PULSE_PORT (storage=$PULSE_STORAGE_MODE)"
  compose_cmd up -d --build --no-deps pulsesync >/dev/null
  if ! wait_for_active_health "http://127.0.0.1:${PULSE_PORT}/v1/health"; then
    compose_cmd logs --no-color pulsesync >"$dir/service.log" 2>&1 || true
    return 1
  fi
}

stop_compose_service() {
  local service=$1
  local dir=$2
  local port=
  mkdir -p "$dir"
  compose_cmd logs --no-color "$service" >"$dir/service.log" 2>&1 || true
  compose_cmd rm -sf "$service" >/dev/null 2>&1 || true

  case "$service" in
    pulsesync)
      port=$PULSE_PORT
      ;;
    electric)
      port=$COMPARE_PORT
      ;;
  esac

  if [ -n "$port" ]; then
    wait_for_port_free "$port"
  fi
}

scenario_disk_restart_continuity() {
  local dir="$VALIDATION_DIR/disk_restart_continuity"
  local base_url="http://127.0.0.1:${PULSE_PORT}"
  mkdir -p "$dir"

  set_pulsesync_storage disk "$dir/storage"
  reset_stack
  capture_pg_debug "$dir/db-before"

  start_pulsesync_compose "$dir/01-start"
  capture_http "GET" "$base_url/v1/shape?table=items&offset=now&secret=$SECRET" "$dir/01-offset-now"
  local handle
  local offset
  local restarted_handle
  local restarted_offset
  handle=$(extract_header "$dir/01-offset-now/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-offset-now/headers.txt" "electric-offset")
  assert_equals "$offset" "0_inf" "initial offset=now should use 0_inf"

  stop_compose_service pulsesync "$dir/01-stop"

  start_pulsesync_compose "$dir/02-restart"
  capture_http "GET" "$base_url/v1/shape?table=items&offset=now&secret=$SECRET" "$dir/02-offset-now"
  restarted_handle=$(extract_header "$dir/02-offset-now/headers.txt" "electric-handle")
  restarted_offset=$(extract_header "$dir/02-offset-now/headers.txt" "electric-offset")
  assert_equals "$restarted_handle" "$handle" "disk restart should preserve shape handle"
  assert_equals "$restarted_offset" "$offset" "disk restart should preserve current offset"

  run_sql_file "$E2E_DIR/sql/insert_item.sql"
  sleep 1
  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=${offset}&secret=$SECRET" "$dir/03-continuation"
  assert_contains "$dir/03-continuation/normalized.json" '"status": 200' "continuation after restart should succeed"
  assert_contains "$dir/03-continuation/normalized.json" '"operation": "insert"' "continuation after restart should include insert event"
  assert_contains "$dir/03-continuation/normalized.json" '"control": "up-to-date"' "continuation after restart should flush up-to-date"

  capture_pg_debug "$dir/db-after"
  stop_compose_service pulsesync "$dir/final-stop"
}

scenario_disk_corrupt_shape_recovery() {
  local dir="$VALIDATION_DIR/disk_corrupt_shape_recovery"
  local base_url="http://127.0.0.1:${PULSE_PORT}"
  mkdir -p "$dir"

  set_pulsesync_storage disk "$dir/storage"
  reset_stack
  capture_pg_debug "$dir/db-before"

  start_pulsesync_compose "$dir/01-start"
  capture_http "GET" "$base_url/v1/shape?table=items&where=priority%20%3E%3D%201&offset=now&secret=$SECRET" "$dir/01-shape-a"
  capture_http "GET" "$base_url/v1/shape?table=items&where=priority%20%3E%3D%202&offset=now&secret=$SECRET" "$dir/02-shape-b"

  local handle_a
  local offset_a
  local handle_b
  local offset_b
  handle_a=$(extract_header "$dir/01-shape-a/headers.txt" "electric-handle")
  offset_a=$(extract_header "$dir/01-shape-a/headers.txt" "electric-offset")
  handle_b=$(extract_header "$dir/02-shape-b/headers.txt" "electric-handle")
  offset_b=$(extract_header "$dir/02-shape-b/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/insert_item.sql"
  sleep 1
  stop_compose_service pulsesync "$dir/01-stop"

  local chunk_a
  local chunk_b
  chunk_a=$(find_persisted_chunk "$handle_a")
  chunk_b=$(find_persisted_chunk "$handle_b")
  if [ -z "$chunk_a" ] || [ -z "$chunk_b" ]; then
    echo "expected persisted chunk files for both shapes" >&2
    exit 1
  fi
  printf '{broken' >"$chunk_a"

  start_pulsesync_compose "$dir/02-restart"
  capture_http "GET" "$base_url/v1/shape?table=items&where=priority%20%3E%3D%201&handle=${handle_a}&offset=${offset_a}&secret=$SECRET" "$dir/03-shape-a-after-restart"
  capture_http "GET" "$base_url/v1/shape?table=items&where=priority%20%3E%3D%202&handle=${handle_b}&offset=${offset_b}&secret=$SECRET" "$dir/04-shape-b-after-restart"

  assert_contains "$dir/03-shape-a-after-restart/normalized.json" '"status": 409' "corrupt shape should require refetch"
  assert_contains "$dir/03-shape-a-after-restart/normalized.json" '"control": "must-refetch"' "corrupt shape should emit must-refetch"
  local replacement_handle_a
  replacement_handle_a=$(extract_header "$dir/03-shape-a-after-restart/headers.txt" "electric-handle")
  assert_not_equals "$replacement_handle_a" "$handle_a" "corrupt shape should rotate handle"

  assert_contains "$dir/04-shape-b-after-restart/normalized.json" '"status": 200' "healthy shape should survive restart"
  assert_contains "$dir/04-shape-b-after-restart/normalized.json" '"operation": "insert"' "healthy shape should keep persisted continuation"
  local replacement_handle_b
  replacement_handle_b=$(extract_header "$dir/04-shape-b-after-restart/headers.txt" "electric-handle")
  assert_equals "$replacement_handle_b" "$handle_b" "healthy shape should preserve handle"

  capture_pg_debug "$dir/db-after"
  stop_compose_service pulsesync "$dir/final-stop"
}

scenario_reconnect_health_and_continuation() {
  local dir="$VALIDATION_DIR/reconnect_health_and_continuation"
  local base_url="http://127.0.0.1:${PULSE_PORT}"
  mkdir -p "$dir"

  set_pulsesync_storage memory "$dir/storage"
  reset_stack
  capture_pg_debug "$dir/db-before"

  start_pulsesync_compose "$dir/01-start"
  capture_http "GET" "$base_url/v1/shape?table=items&offset=now&secret=$SECRET" "$dir/01-offset-now"
  local handle
  local offset
  handle=$(extract_header "$dir/01-offset-now/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-offset-now/headers.txt" "electric-offset")

  compose_cmd stop postgres >/dev/null
  wait_for_health_state "$base_url/v1/health" "waiting" 45
  capture_http "GET" "$base_url/v1/health" "$dir/02-health-waiting"
  assert_contains "$dir/02-health-waiting/normalized.json" '"status": 202' "health should degrade to 202 while replication is disconnected"
  assert_contains "$dir/02-health-waiting/body.txt" '"status":"waiting"' "health body should report waiting"

  compose_cmd start postgres >/dev/null
  wait_for_postgres
  wait_for_active_health "$base_url/v1/health" 60
  capture_http "GET" "$base_url/v1/health" "$dir/03-health-active-again"
  assert_contains "$dir/03-health-active-again/normalized.json" '"status": 200' "health should return 200 after reconnect"
  assert_contains "$dir/03-health-active-again/body.txt" '"status":"active"' "health body should report active after reconnect"

  run_sql_file "$E2E_DIR/sql/insert_item.sql"
  sleep 1
  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=${offset}&secret=$SECRET" "$dir/04-continuation"
  assert_contains "$dir/04-continuation/normalized.json" '"status": 200' "continuation after reconnect should succeed"
  assert_contains "$dir/04-continuation/normalized.json" '"operation": "insert"' "continuation after reconnect should include insert event"
  assert_contains "$dir/04-continuation/normalized.json" '"control": "up-to-date"' "continuation after reconnect should flush up-to-date"

  capture_pg_debug "$dir/db-after"
  stop_compose_service pulsesync "$dir/final-stop"
}

run_scenario() {
  local scenario=$1
  log "running PulseSync docker validation: $scenario"
  "scenario_${scenario}"
  log "PulseSync docker validation passed: $scenario"
}

main() {
  ensure_common_requirements
  require_cmd docker
  configure_one_off_docker_ports

  log "using one-off host ports: postgres=$DB_PORT pulsesync=$PULSE_PORT electric=$COMPARE_PORT"
  log "using compose project: $COMPOSE_PROJECT_NAME"

  local scenario
  for scenario in "${SCENARIOS[@]}"; do
    run_scenario "$scenario"
  done

  echo
  echo "all PulseSync docker validations passed" >&2
  echo "artifacts: $VALIDATION_DIR" >&2
}

main
