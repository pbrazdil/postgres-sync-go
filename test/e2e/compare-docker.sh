#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

COMPOSE_FILE=${COMPOSE_FILE:-$SCRIPT_DIR/docker-compose.yml}
COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-postgres-sync-go-e2e-$(date +%Y%m%d%H%M%S)}
SCENARIOS=("$@")
if [ ${#SCENARIOS[@]} -eq 0 ]; then
  SCENARIOS=(
    health
    initial_snapshot
    filtered_snapshot
    columns_snapshot
    columns_offset_now_then_update
    subset_get_snapshot
    subset_post_snapshot
    subset_subquery_rejected
    offset_now_then_insert
    offset_now_then_update
    offset_now_then_delete
    live_longpoll_insert
    live_sse_insert
    experimental_live_sse_insert
    live_sse_keepalive
    live_sse_resume_after_update
    truncate_then_must_refetch
    subquery_rejected_without_feature_flag
    subquery_move_in_live_replay
    subquery_move_out_live_replay
    subquery_nested_multi_hop_move_in_live_replay
    subquery_nested_multi_hop_move_out_live_replay
    subquery_negated_move_in_live_replay
    subquery_negated_move_out_live_replay
    handle_definition_mismatch_must_refetch
    unknown_handle_must_refetch
    shape_delete_handle_rotation
    cache_if_none_match_304
    log_full_offset_now_then_update
    log_changes_only_initial_snapshot
    log_changes_only_offset_now_then_update
    replica_default_offset_now_then_update
    replica_full_offset_now_then_update
    overload_existing_live_request
    partition_root_snapshot
    partition_offset_now_then_insert
    partition_child_offset_now_then_insert
  )
fi

FAILURES=0

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

start_postgres_sync_go_compose() {
  log "starting dockerized postgres-sync-go on port $SYNC_GO_PORT"
  compose_cmd up -d --build --no-deps postgres-sync-go >/dev/null
  if ! wait_for_active_health "http://127.0.0.1:${SYNC_GO_PORT}/v1/health"; then
    compose_cmd logs --no-color postgres-sync-go >&2 || true
    return 1
  fi
}

start_electric_compose() {
  log "starting dockerized Electric on port $COMPARE_PORT"
  compose_cmd up -d --build --no-deps electric >/dev/null
  if ! wait_for_active_health "http://127.0.0.1:${COMPARE_PORT}/v1/health"; then
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
    postgres-sync-go)
      port=$SYNC_GO_PORT
      ;;
    electric)
      port=$COMPARE_PORT
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
    postgres-sync-go)
      service_name="postgres-sync-go"
      start_postgres_sync_go_compose
      base_url="http://127.0.0.1:${SYNC_GO_PORT}"
      ;;
    electric)
      service_name="electric"
      start_electric_compose
      base_url="http://127.0.0.1:${COMPARE_PORT}"
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
  log "scenario ports: postgres-sync-go=$SYNC_GO_PORT electric=$COMPARE_PORT"
  mkdir -p "$scenario_dir"

  run_impl postgres-sync-go "$scenario" "$scenario_dir/postgres-sync-go"
  run_impl electric "$scenario" "$scenario_dir/electric"

  if compare_step_files "$scenario_dir/postgres-sync-go/scenario" "$scenario_dir/electric/scenario" "$scenario_dir/diffs"; then
    log "docker scenario passed: $scenario"
    return 0
  fi

  log "docker scenario failed: $scenario"
  return 1
}

main() {
  ensure_common_requirements
  require_cmd docker
  ensure_compare_compose_source
  configure_one_off_docker_ports

  log "using one-off host ports: postgres=$DB_PORT postgres-sync-go=$SYNC_GO_PORT electric=$COMPARE_PORT"
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
