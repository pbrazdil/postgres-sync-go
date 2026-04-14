#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

COMPOSE_FILE=${COMPOSE_FILE:-$SCRIPT_DIR/docker-compose.yml}
COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-pulsesync-e2e-$(date +%Y%m%d%H%M%S)}
SCENARIOS=("$@")
if [ ${#SCENARIOS[@]} -eq 0 ]; then
  SCENARIOS=(
    health
    initial_snapshot
    filtered_snapshot
    columns_snapshot
    subset_get_snapshot
    subset_post_snapshot
    offset_now_then_insert
    offset_now_then_update
    offset_now_then_delete
    live_longpoll_insert
    live_sse_insert
    truncate_then_must_refetch
    subquery_move_in_must_refetch
    log_changes_only_initial_snapshot
    log_changes_only_offset_now_then_update
    overload_existing_live_request
    partition_root_snapshot
    partition_offset_now_then_insert
  )
fi

FAILURES=0

compose_cmd() {
  docker compose -p "$COMPOSE_PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
}

compare_step_files() {
  local left_dir=$1
  local right_dir=$2
  local diff_dir=$3
  local ok=0
  local relative
  local left_file
  local right_file

  mkdir -p "$diff_dir"
  while IFS= read -r relative; do
    [ -n "$relative" ] || continue
    relative=${relative#./}
    left_file="$left_dir/$relative"
    right_file="$right_dir/$relative"

    if [ ! -f "$left_file" ]; then
      log "missing left-hand normalized output: $relative"
      ok=1
      continue
    fi

    if [ ! -f "$right_file" ]; then
      log "missing right-hand normalized output: $relative"
      ok=1
      continue
    fi

    if ! diff -u "$left_file" "$right_file" >"$diff_dir/${relative//\//__}.diff"; then
      log "diff detected in $relative"
      ok=1
    else
      rm -f "$diff_dir/${relative//\//__}.diff"
    fi
  done < <(
    {
      (cd "$left_dir" && find . -name 'normalized.json' | sort)
      (cd "$right_dir" && find . -name 'normalized.json' | sort)
    } | sort -u
  )

  return "$ok"
}

start_postgres_compose() {
  log "starting dockerized Postgres"
  compose_cmd up -d postgres >/dev/null
  wait_for_postgres
}

start_pulsesync_compose() {
  log "starting dockerized PulseSync on port $PULSE_PORT"
  compose_cmd up -d --build --no-deps pulsesync >/dev/null
  if ! wait_for_active_health "http://127.0.0.1:${PULSE_PORT}/v1/health"; then
    compose_cmd logs --no-color pulsesync >&2 || true
    return 1
  fi
}

start_electric_compose() {
  log "starting dockerized Electric on port $ELECTRIC_PORT"
  compose_cmd up -d --build --no-deps electric >/dev/null
  if ! wait_for_active_health "http://127.0.0.1:${ELECTRIC_PORT}/v1/health"; then
    compose_cmd logs --no-color electric >&2 || true
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
      port=$ELECTRIC_PORT
      ;;
  esac

  if [ -n "$port" ]; then
    wait_for_port_free "$port"
  fi
}

cleanup() {
  compose_cmd down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

run_impl() {
  local impl=$1
  local scenario=$2
  local out_dir=$3
  local base_url
  local service_name

  mkdir -p "$out_dir"
  reset_database
  capture_pg_debug "$out_dir/db-before"

  case "$impl" in
    pulsesync)
      service_name="pulsesync"
      start_pulsesync_compose
      base_url="http://127.0.0.1:${PULSE_PORT}"
      ;;
    electric)
      service_name="electric"
      start_electric_compose
      base_url="http://127.0.0.1:${ELECTRIC_PORT}"
      ;;
    *)
      echo "unknown implementation: $impl" >&2
      return 1
      ;;
  esac

  "scenario_${scenario}" "$base_url" "$out_dir/scenario"
  capture_pg_debug "$out_dir/db-after"
  stop_compose_service "$service_name" "$out_dir"
  cleanup_replication_artifacts
}

compare_scenario() {
  local scenario=$1
  local scenario_dir="$ARTIFACTS_DIR/compare-docker/$scenario"

  log "running docker scenario: $scenario"
  configure_scenario_runtime_config "$scenario"
  configure_one_off_service_ports
  log "scenario ports: pulsesync=$PULSE_PORT electric=$ELECTRIC_PORT"
  mkdir -p "$scenario_dir"

  run_impl pulsesync "$scenario" "$scenario_dir/pulsesync"
  run_impl electric "$scenario" "$scenario_dir/electric"

  if compare_step_files "$scenario_dir/pulsesync/scenario" "$scenario_dir/electric/scenario" "$scenario_dir/diffs"; then
    log "docker scenario passed: $scenario"
    return 0
  fi

  log "docker scenario failed: $scenario"
  return 1
}

main() {
  ensure_common_requirements
  require_cmd docker
  configure_one_off_docker_ports

  log "using one-off host ports: postgres=$DB_PORT pulsesync=$PULSE_PORT electric=$ELECTRIC_PORT"
  log "using compose project: $COMPOSE_PROJECT_NAME"

  start_postgres_compose

  local scenario
  for scenario in "${SCENARIOS[@]}"; do
    if ! compare_scenario "$scenario"; then
      FAILURES=$((FAILURES + 1))
    fi
  done

  if [ "$FAILURES" -gt 0 ]; then
    echo
    echo "docker comparison finished with $FAILURES failing scenario(s)" >&2
    echo "artifacts: $ARTIFACTS_DIR/compare-docker" >&2
    exit 1
  fi

  echo
  echo "all docker scenarios matched" >&2
  echo "artifacts: $ARTIFACTS_DIR/compare-docker" >&2
}

main
