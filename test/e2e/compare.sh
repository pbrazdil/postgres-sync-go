#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

SCENARIOS=("$@")
if [ ${#SCENARIOS[@]} -eq 0 ]; then
  SCENARIOS=(
    health
    initial_snapshot
    filtered_snapshot
    columns_snapshot
    subset_get_snapshot
    subset_post_snapshot
    subset_subquery_rejected
    offset_now_then_insert
    offset_now_then_update
    offset_now_then_delete
    live_longpoll_insert
    live_sse_insert
    live_sse_keepalive
    truncate_then_must_refetch
    subquery_rejected_without_feature_flag
    subquery_move_in_live_replay
    subquery_move_out_live_replay
    subquery_nested_multi_hop_move_in_live_replay
    subquery_nested_multi_hop_move_out_live_replay
    subquery_negated_move_in_live_replay
    subquery_negated_move_out_live_replay
    handle_definition_mismatch_must_refetch
    log_full_offset_now_then_update
    log_changes_only_initial_snapshot
    log_changes_only_offset_now_then_update
    replica_full_offset_now_then_update
    overload_existing_live_request
    partition_root_snapshot
    partition_offset_now_then_insert
    partition_child_offset_now_then_insert
  )
fi

START_POSTGRES=${START_POSTGRES:-1}
FAILURES=0

cleanup() {
  stop_service "${CURRENT_SERVICE_PID:-}"
}
trap cleanup EXIT

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

run_impl() {
  local impl=$1
  local scenario=$2
  local out_dir=$3
  local base_url

  mkdir -p "$out_dir"
  reset_database
  capture_pg_debug "$out_dir/db-before"

  case "$impl" in
    postgres-sync-go)
      start_postgres_sync_go "$out_dir"
      CURRENT_SERVICE_PID=$SERVICE_PID
      base_url="http://127.0.0.1:${SYNC_GO_PORT}"
      ;;
    electric)
      start_electric "$out_dir"
      CURRENT_SERVICE_PID=$SERVICE_PID
      base_url="http://127.0.0.1:${COMPARE_PORT}"
      ;;
    *)
      echo "unknown implementation: $impl" >&2
      return 1
      ;;
  esac

  "scenario_${scenario}" "$base_url" "$out_dir/scenario"
  capture_pg_debug "$out_dir/db-after"
  stop_service "$CURRENT_SERVICE_PID"
  CURRENT_SERVICE_PID=""
  cleanup_replication_artifacts
}

compare_scenario() {
  local scenario=$1
  local scenario_dir="$ARTIFACTS_DIR/compare/$scenario"

  log "running scenario: $scenario"
  configure_scenario_runtime_config "$scenario"
  mkdir -p "$scenario_dir"

  run_impl postgres-sync-go "$scenario" "$scenario_dir/postgres-sync-go"
  run_impl electric "$scenario" "$scenario_dir/electric"

  if compare_step_files "$scenario_dir/postgres-sync-go/scenario" "$scenario_dir/electric/scenario" "$scenario_dir/diffs"; then
    log "scenario passed: $scenario"
    return 0
  fi

  log "scenario failed: $scenario"
  return 1
}

main() {
  ensure_common_requirements
  if [ "$START_POSTGRES" = "1" ]; then
    start_postgres_dev
  else
    wait_for_postgres
  fi

  local scenario
  for scenario in "${SCENARIOS[@]}"; do
    if ! compare_scenario "$scenario"; then
      FAILURES=$((FAILURES + 1))
    fi
  done

  if [ "$FAILURES" -gt 0 ]; then
    echo
    echo "comparison finished with $FAILURES failing scenario(s)" >&2
    echo "artifacts: $ARTIFACTS_DIR/compare" >&2
    exit 1
  fi

  echo
  echo "all scenarios matched" >&2
  echo "artifacts: $ARTIFACTS_DIR/compare" >&2
}

main
