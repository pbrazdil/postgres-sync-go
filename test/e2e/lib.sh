#!/usr/bin/env bash

set -euo pipefail

E2E_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd -- "$E2E_DIR/../.." && pwd)
COMPARE_SYNC_DIR=${COMPARE_SYNC_DIR:-}
COMPARE_DOCKER_COMPOSE=${COMPARE_DOCKER_COMPOSE:-}
COMPARE_TELEMETRY_DIR=${COMPARE_TELEMETRY_DIR:-}
if [ -n "$COMPARE_SYNC_DIR" ] && [ -z "$COMPARE_DOCKER_COMPOSE" ]; then
  COMPARE_DOCKER_COMPOSE="$COMPARE_SYNC_DIR/dev/docker-compose.yml"
fi
if [ -n "$COMPARE_SYNC_DIR" ] && [ -z "$COMPARE_TELEMETRY_DIR" ] && [ -d "$COMPARE_SYNC_DIR/../electric-telemetry" ]; then
  COMPARE_TELEMETRY_DIR=$(cd "$COMPARE_SYNC_DIR/../electric-telemetry" && pwd)
fi

ARTIFACTS_DIR_DEFAULT="$E2E_DIR/_artifacts/$(date +%Y%m%d-%H%M%S)"
ARTIFACTS_DIR=${ARTIFACTS_DIR:-$ARTIFACTS_DIR_DEFAULT}

DB_PORT_ENV_SET=${DB_PORT+x}
SYNC_GO_PORT_ENV_SET=${SYNC_GO_PORT+x}
COMPARE_PORT_ENV_SET=${COMPARE_PORT+x}
DATABASE_URL_ENV_SET=${DATABASE_URL+x}
POOLED_DATABASE_URL_ENV_SET=${POOLED_DATABASE_URL+x}

DB_PORT=${DB_PORT:-54321}
SYNC_GO_PORT=${SYNC_GO_PORT:-3100}
COMPARE_PORT=${COMPARE_PORT:-3200}

DATABASE_URL=${DATABASE_URL:-postgresql://postgres:password@localhost:${DB_PORT}/postgres_sync_go?sslmode=disable}
POOLED_DATABASE_URL=${POOLED_DATABASE_URL:-$DATABASE_URL}
SECRET=${SECRET:-test-secret}

SYNC_GO_STREAM_ID=${SYNC_GO_STREAM_ID:-syncgocmp}
COMPARE_STREAM_ID=${COMPARE_STREAM_ID:-electriccmp}

SYNC_GO_STORAGE_MODE=${SYNC_GO_STORAGE_MODE:-memory}
SYNC_GO_STORAGE_DIR=${SYNC_GO_STORAGE_DIR:-$ARTIFACTS_DIR/postgres-sync-go-storage}
SYNC_GO_STORAGE_BIND_DIR=${SYNC_GO_STORAGE_BIND_DIR:-$ARTIFACTS_DIR/postgres-sync-go-storage}
CURL_MAX_TIME=${CURL_MAX_TIME:-20}
SCENARIO_MAX_CONCURRENT_REQUESTS=${SCENARIO_MAX_CONCURRENT_REQUESTS:-}
if [ -z "$SCENARIO_MAX_CONCURRENT_REQUESTS" ]; then
  SCENARIO_MAX_CONCURRENT_REQUESTS='{"initial":300,"existing":10000}'
fi
SCENARIO_LONG_POLL_TIMEOUT_MS=${SCENARIO_LONG_POLL_TIMEOUT_MS:-20000}
SCENARIO_SSE_TIMEOUT_MS=${SCENARIO_SSE_TIMEOUT_MS:-60000}
SCENARIO_FEATURE_FLAGS=${SCENARIO_FEATURE_FLAGS:-}
USED_SYNC_GO_PORTS=${USED_SYNC_GO_PORTS:-}
USED_COMPARE_PORTS=${USED_COMPARE_PORTS:-}

SYNCDIFF_BIN=${SYNCDIFF_BIN:-$ARTIFACTS_DIR/bin/syncdiff}

mkdir -p "$ARTIFACTS_DIR"

log() {
  printf '[e2e] %s\n' "$*" >&2
}

comparison_unavailable() {
  cat >&2 <<EOF
[e2e] comparison source unavailable
[e2e] Set COMPARE_SYNC_DIR to a local comparison sync-service checkout to run differential scenarios.
[e2e] Current COMPARE_SYNC_DIR: $COMPARE_SYNC_DIR
EOF
  exit 77
}

port_is_free() {
  local port=$1

  if command -v docker >/dev/null 2>&1; then
    if docker ps --format '{{.Ports}}' | grep -Fq "127.0.0.1:${port}->"; then
      return 1
    fi
  fi

  if command -v lsof >/dev/null 2>&1; then
    ! lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
    return
  fi

  if command -v ss >/dev/null 2>&1; then
    ! ss -ltn "sport = :$port" | tail -n +2 | grep -q .
    return
  fi

  ! (echo >/dev/tcp/127.0.0.1/"$port") >/dev/null 2>&1
}

find_free_port() {
  local start=$1
  local end=$2
  local port

  for ((port=start; port<=end; port++)); do
    if port_is_free "$port"; then
      printf '%s\n' "$port"
      return 0
    fi
  done

  echo "no free port available in range ${start}-${end}" >&2
  return 1
}

port_list_contains() {
  local list=$1
  local port=$2
  case " $list " in
    *" $port "*) return 0 ;;
    *) return 1 ;;
  esac
}

find_fresh_port() {
  local start=$1
  local end=$2
  local used_list=$3
  local port

  for ((port=start; port<=end; port++)); do
    if port_list_contains "$used_list" "$port"; then
      continue
    fi
    if port_is_free "$port"; then
      printf '%s\n' "$port"
      return 0
    fi
  done

  echo "no free fresh port available in range ${start}-${end}" >&2
  return 1
}

wait_for_port_free() {
  local port=$1
  local attempts=${2:-30}

  while (( attempts > 0 )); do
    if port_is_free "$port"; then
      return 0
    fi
    sleep 1
    attempts=$((attempts - 1))
  done

  echo "port did not become free: $port" >&2
  return 1
}

configure_one_off_docker_ports() {
  if [ -z "$DB_PORT_ENV_SET" ]; then
    DB_PORT=$(find_free_port "${E2E_DB_PORT_RANGE_START:-45432}" "${E2E_DB_PORT_RANGE_END:-45532}")
  fi
  configure_one_off_service_ports
  if [ -z "$DATABASE_URL_ENV_SET" ]; then
    DATABASE_URL="postgresql://postgres:password@localhost:${DB_PORT}/postgres_sync_go?sslmode=disable"
  fi
  if [ -z "$POOLED_DATABASE_URL_ENV_SET" ]; then
    POOLED_DATABASE_URL="$DATABASE_URL"
  fi

  export DB_PORT
  export SYNC_GO_PORT
  export COMPARE_PORT
  export DATABASE_URL
  export POOLED_DATABASE_URL
  export SECRET
  export SYNC_GO_STREAM_ID
  export COMPARE_STREAM_ID
  export SYNC_GO_STORAGE_MODE
  export SYNC_GO_STORAGE_DIR
  export SYNC_GO_STORAGE_BIND_DIR
  export CURL_MAX_TIME
  export SCENARIO_MAX_CONCURRENT_REQUESTS
  export SCENARIO_LONG_POLL_TIMEOUT_MS
  export SCENARIO_SSE_TIMEOUT_MS
  export SCENARIO_FEATURE_FLAGS
}

configure_one_off_service_ports() {
  if [ -z "$SYNC_GO_PORT_ENV_SET" ]; then
    SYNC_GO_PORT=$(find_fresh_port "${E2E_SYNC_GO_PORT_RANGE_START:-43100}" "${E2E_SYNC_GO_PORT_RANGE_END:-43199}" "$USED_SYNC_GO_PORTS")
    USED_SYNC_GO_PORTS="${USED_SYNC_GO_PORTS:+$USED_SYNC_GO_PORTS }$SYNC_GO_PORT"
  fi
  if [ -z "$COMPARE_PORT_ENV_SET" ]; then
    COMPARE_PORT=$(find_fresh_port "${E2E_COMPARE_PORT_RANGE_START:-43200}" "${E2E_COMPARE_PORT_RANGE_END:-43299}" "$USED_COMPARE_PORTS")
    USED_COMPARE_PORTS="${USED_COMPARE_PORTS:+$USED_COMPARE_PORTS }$COMPARE_PORT"
  fi

  export SYNC_GO_PORT
  export COMPARE_PORT
  export USED_SYNC_GO_PORTS
  export USED_COMPARE_PORTS
}

configure_scenario_runtime_config() {
  local scenario=$1

  CURL_MAX_TIME=20
  SCENARIO_MAX_CONCURRENT_REQUESTS='{"initial":300,"existing":10000}'
  SCENARIO_LONG_POLL_TIMEOUT_MS=20000
  SCENARIO_SSE_TIMEOUT_MS=60000
  SCENARIO_FEATURE_FLAGS=

  case "$scenario" in
    subquery_move_in_live_replay|subquery_move_out_live_replay)
      SCENARIO_FEATURE_FLAGS='allow_subqueries,tagged_subqueries'
      ;;
    subquery_move_in_must_refetch)
      SCENARIO_FEATURE_FLAGS='allow_subqueries'
      ;;
    live_sse_insert)
      CURL_MAX_TIME=3
      ;;
    live_sse_keepalive)
      CURL_MAX_TIME=25
      ;;
    overload_existing_live_request)
      CURL_MAX_TIME=5
      SCENARIO_LONG_POLL_TIMEOUT_MS=4000
      SCENARIO_MAX_CONCURRENT_REQUESTS='{"initial":10,"existing":1}'
      ;;
  esac

  export CURL_MAX_TIME
  export SCENARIO_MAX_CONCURRENT_REQUESTS
  export SCENARIO_LONG_POLL_TIMEOUT_MS
  export SCENARIO_SSE_TIMEOUT_MS
  export SCENARIO_FEATURE_FLAGS
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

ensure_common_requirements() {
  require_cmd curl
  require_cmd diff
  require_cmd go
  require_cmd pg_isready
  require_cmd psql
  mkdir -p "$(dirname "$SYNCDIFF_BIN")"
  if [ ! -x "$SYNCDIFF_BIN" ]; then
    log "building syncdiff helper"
    (cd "$ROOT_DIR" && go build -o "$SYNCDIFF_BIN" ./test/e2e/cmd/syncdiff)
  fi
}

ensure_electric_requirements() {
  if [ ! -d "$COMPARE_SYNC_DIR" ]; then
    comparison_unavailable
  fi
  require_cmd mix
  if [ ! -d "$COMPARE_SYNC_DIR/deps" ]; then
    log "fetching Electric mix dependencies"
    (cd "$COMPARE_SYNC_DIR" && mix deps.get)
  fi
}

start_postgres_dev() {
  if [ ! -f "$COMPARE_DOCKER_COMPOSE" ]; then
    comparison_unavailable
  fi
  require_cmd docker
  log "starting dev postgres via docker compose"
  docker compose -f "$COMPARE_DOCKER_COMPOSE" up -d postgres >/dev/null
  wait_for_postgres
}

wait_for_postgres() {
  local attempts=60
  while (( attempts > 0 )); do
    if pg_isready -d "$DATABASE_URL" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    attempts=$((attempts - 1))
  done
  echo "postgres did not become ready: $DATABASE_URL" >&2
  return 1
}

wait_for_active_health() {
  local url=$1
  local attempts=${2:-60}
  local health

  while (( attempts > 0 )); do
    if health=$(curl -sS "$url" 2>/dev/null); then
      if printf '%s' "$health" | grep -q '"status":"active"'; then
        return 0
      fi
    fi
    sleep 1
    attempts=$((attempts - 1))
  done

  echo "service did not become active: $url" >&2
  return 1
}

wait_for_health_state() {
  local url=$1
  local expected_state=$2
  local attempts=${3:-60}
  local health

  while (( attempts > 0 )); do
    if health=$(curl -sS "$url" 2>/dev/null); then
      if printf '%s' "$health" | grep -q "\"status\":\"$expected_state\""; then
        return 0
      fi
    fi
    sleep 1
    attempts=$((attempts - 1))
  done

  echo "service did not become state $expected_state: $url" >&2
  return 1
}

run_sql_file() {
  local file=$1
  psql -v ON_ERROR_STOP=1 "$DATABASE_URL" -f "$file" >/dev/null
}

cleanup_replication_artifacts() {
  psql -v ON_ERROR_STOP=1 "$DATABASE_URL" <<SQL >/dev/null
DO \$\$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_publication WHERE pubname = 'electric_publication_${COMPARE_STREAM_ID}') THEN
    EXECUTE 'DROP PUBLICATION ' || quote_ident('electric_publication_${COMPARE_STREAM_ID}');
  END IF;
  IF EXISTS (SELECT 1 FROM pg_publication WHERE pubname = 'postgres_sync_go_${SYNC_GO_STREAM_ID}_pub') THEN
    EXECUTE 'DROP PUBLICATION ' || quote_ident('postgres_sync_go_${SYNC_GO_STREAM_ID}_pub');
  END IF;
END
\$\$;

SELECT pg_drop_replication_slot(slot_name)
FROM pg_replication_slots
WHERE NOT active
  AND (
    slot_name = 'electric_slot_${COMPARE_STREAM_ID}'
    OR slot_name = 'postgres_sync_go_${SYNC_GO_STREAM_ID}_slot'
    OR slot_name LIKE 'postgres_sync_go_${SYNC_GO_STREAM_ID}_slot_%'
  );
SQL
}

reset_database() {
  cleanup_replication_artifacts
  run_sql_file "$E2E_DIR/seed.sql"
}

capture_pg_debug() {
  local dir=$1
  mkdir -p "$dir"

  psql -v ON_ERROR_STOP=1 "$DATABASE_URL" <<SQL >"$dir/postgres-debug.txt"
\pset footer off
SELECT slot_name, plugin, slot_type, temporary, active, restart_lsn, confirmed_flush_lsn
FROM pg_replication_slots
ORDER BY slot_name;

SELECT pubname, puballtables, pubinsert, pubupdate, pubdelete, pubtruncate
FROM pg_publication
ORDER BY pubname;

SELECT pubname, schemaname, tablename
FROM pg_publication_tables
ORDER BY pubname, schemaname, tablename;

SELECT id, value, priority, archived, category
FROM items
ORDER BY id;

SELECT item_id, enabled
FROM item_flags
ORDER BY item_id;

SELECT tenant_id, seq, value
FROM partitioned_items
ORDER BY tenant_id, seq;
SQL
}

capture_http() {
  local method=$1
  local url=$2
  local dir=$3
  local body_file=${4:-}
  local allow_timeout=${5:-0}

  mkdir -p "$dir"
  printf '%s %s\n' "$method" "$url" >"$dir/request.txt"
  : >"$dir/headers.txt"
  : >"$dir/body.txt"

  local curl_args=(
    --silent
    --show-error
    --max-time "${CURL_MAX_TIME:-20}"
    --request "$method"
    --dump-header "$dir/headers.txt"
    --output "$dir/body.txt"
    "$url"
  )

  if [ -n "$body_file" ]; then
    cp "$body_file" "$dir/request-body.json"
    curl_args+=(--header "content-type: application/json" --data "@$body_file")
  fi

  local curl_rc=0
  set +e
  curl "${curl_args[@]}" >"$dir/curl-stdout.txt" 2>"$dir/curl-stderr.txt"
  curl_rc=$?
  set -e

  if [ "$curl_rc" -ne 0 ]; then
    if [ "$allow_timeout" != "1" ] || [ "$curl_rc" -ne 28 ]; then
      echo "curl failed for $url with exit code $curl_rc" >&2
      return "$curl_rc"
    fi
  fi

  "$SYNCDIFF_BIN" normalize-http --headers "$dir/headers.txt" --body "$dir/body.txt" >"$dir/normalized.json"
  printf '%s\n' "$curl_rc" >"$dir/curl-exit-code.txt"
}

extract_header() {
  local headers_file=$1
  local header_name=$2
  "$SYNCDIFF_BIN" extract-header --headers "$headers_file" --name "$header_name"
}

start_postgres_sync_go() {
  local dir=$1
  local extra_env=()
  mkdir -p "$dir"

  extra_env+=(SYNC_MAX_CONCURRENT_REQUESTS="$SCENARIO_MAX_CONCURRENT_REQUESTS")
  extra_env+=(SYNC_LONG_POLL_TIMEOUT_MS="$SCENARIO_LONG_POLL_TIMEOUT_MS")
  extra_env+=(SYNC_SSE_TIMEOUT_MS="$SCENARIO_SSE_TIMEOUT_MS")
  extra_env+=(SYNC_FEATURE_FLAGS="$SCENARIO_FEATURE_FLAGS")

  log "starting postgres-sync-go on port $SYNC_GO_PORT"
  (
    cd "$ROOT_DIR"
      env \
      DATABASE_URL="$DATABASE_URL" \
      SYNC_POOLED_DATABASE_URL="$POOLED_DATABASE_URL" \
      SYNC_SECRET="$SECRET" \
      SYNC_PORT="$SYNC_GO_PORT" \
      SYNC_REPLICATION_STREAM_ID="$SYNC_GO_STREAM_ID" \
      SYNC_STORAGE_MODE="$SYNC_GO_STORAGE_MODE" \
      SYNC_STORAGE_DIR="$SYNC_GO_STORAGE_DIR" \
      "${extra_env[@]}" \
      go run ./cmd/postgres-sync
  ) >"$dir/service.log" 2>&1 &

  SERVICE_PID=$!
  wait_for_active_health "http://127.0.0.1:${SYNC_GO_PORT}/v1/health"
}

start_electric() {
  local dir=$1
  local extra_env=()
  mkdir -p "$dir"

  ensure_electric_requirements
  extra_env+=(ELECTRIC_MAX_CONCURRENT_REQUESTS="$SCENARIO_MAX_CONCURRENT_REQUESTS")
  extra_env+=(ELECTRIC_LONG_POLL_TIMEOUT_MS="$SCENARIO_LONG_POLL_TIMEOUT_MS")
  extra_env+=(ELECTRIC_SSE_TIMEOUT_MS="$SCENARIO_SSE_TIMEOUT_MS")
  extra_env+=(ELECTRIC_FEATURE_FLAGS="$SCENARIO_FEATURE_FLAGS")

  log "starting Electric on port $COMPARE_PORT"
  (
    cd "$COMPARE_SYNC_DIR"
    env \
      DATABASE_URL="$DATABASE_URL" \
      ELECTRIC_POOLED_DATABASE_URL="$POOLED_DATABASE_URL" \
      ELECTRIC_SECRET="$SECRET" \
      ELECTRIC_INSECURE=false \
      ELECTRIC_LOG_LEVEL=debug \
      ELECTRIC_PORT="$COMPARE_PORT" \
      ELECTRIC_REPLICATION_STREAM_ID="$COMPARE_STREAM_ID" \
      CLEANUP_REPLICATION_SLOTS_ON_SHUTDOWN=true \
      "${extra_env[@]}" \
      mix run --no-halt
  ) >"$dir/service.log" 2>&1 &

  SERVICE_PID=$!
  wait_for_active_health "http://127.0.0.1:${COMPARE_PORT}/v1/health"
}

stop_service() {
  local pid=$1
  if [ -z "$pid" ]; then
    return 0
  fi
  if kill -0 "$pid" >/dev/null 2>&1; then
    kill "$pid" >/dev/null 2>&1 || true
    wait "$pid" 2>/dev/null || true
  fi
}

scenario_health() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/health" "$dir/01-health"
}

scenario_initial_snapshot() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&secret=$SECRET" "$dir/01-initial-snapshot"
}

scenario_filtered_snapshot() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&where=priority%20%3E%3D%202&secret=$SECRET" "$dir/01-filtered-snapshot"
}

scenario_columns_snapshot() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&columns=id,value&secret=$SECRET" "$dir/01-columns-snapshot"
}

scenario_subset_get_snapshot() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&subset__order_by=priority%20ASC&subset__limit=2&secret=$SECRET" "$dir/01-subset-get"
}

scenario_subset_post_snapshot() {
  local base_url=$1
  local dir=$2
  capture_http "POST" "$base_url/v1/shape?table=items&offset=-1&secret=$SECRET" "$dir/01-subset-post" "$E2E_DIR/subset-post.json"
}

scenario_subset_subquery_rejected() {
  local base_url=$1
  local dir=$2
  local encoded_where
  encoded_where='id%20IN%20%28SELECT%20item_id%20FROM%20item_flags%29'

  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&subset__where=${encoded_where}&secret=$SECRET" "$dir/01-rejected"
}

scenario_offset_now_then_insert() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=now&secret=$SECRET" "$dir/01-offset-now"

  local handle
  local offset
  handle=$(extract_header "$dir/01-offset-now/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-offset-now/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/insert_item.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=${offset}&secret=$SECRET" "$dir/02-continuation"
}

scenario_live_longpoll_insert() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&secret=$SECRET" "$dir/01-bootstrap"

  local handle
  handle=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-handle")

  (
    sleep 1
    run_sql_file "$E2E_DIR/sql/insert_item.sql"
  ) &

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=0_0&live=true&secret=$SECRET" "$dir/02-live-longpoll"
}

scenario_live_sse_insert() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&secret=$SECRET" "$dir/01-bootstrap"

  local handle
  handle=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-handle")

  (
    sleep 1
    run_sql_file "$E2E_DIR/sql/insert_item.sql"
  ) &

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=0_0&live=true&live_sse=true&secret=$SECRET" "$dir/02-live-sse" "" 1
}

scenario_live_sse_keepalive() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&secret=$SECRET" "$dir/01-bootstrap"

  local handle
  handle=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-handle")

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=0_0&live=true&live_sse=true&secret=$SECRET" "$dir/02-live-sse-keepalive" "" 1
}

scenario_offset_now_then_update() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=now&secret=$SECRET" "$dir/01-offset-now"

  local handle
  local offset
  handle=$(extract_header "$dir/01-offset-now/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-offset-now/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/update_item.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=${offset}&secret=$SECRET" "$dir/02-continuation"
}

scenario_offset_now_then_delete() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=now&secret=$SECRET" "$dir/01-offset-now"

  local handle
  local offset
  handle=$(extract_header "$dir/01-offset-now/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-offset-now/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/delete_item.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=${offset}&secret=$SECRET" "$dir/02-continuation"
}

scenario_truncate_then_must_refetch() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&secret=$SECRET" "$dir/01-bootstrap"

  local handle
  local offset
  handle=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/truncate_items.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=${offset}&secret=$SECRET" "$dir/02-after-truncate"
}

scenario_subquery_rejected_without_feature_flag() {
  local base_url=$1
  local dir=$2
  local encoded_where
  encoded_where='id%20IN%20%28SELECT%20item_id%20FROM%20item_flags%29'

  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&where=${encoded_where}&secret=$SECRET" "$dir/01-rejected"
}

scenario_subquery_move_in_live_replay() {
  local base_url=$1
  local dir=$2
  local encoded_where
  encoded_where='id%20IN%20%28SELECT%20item_id%20FROM%20item_flags%20WHERE%20enabled%20%3D%20true%29'

  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&where=${encoded_where}&secret=$SECRET" "$dir/01-bootstrap"

  local handle
  local offset
  handle=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/update_item_flag.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=items&where=${encoded_where}&handle=${handle}&offset=${offset}&secret=$SECRET" "$dir/02-after-related-update"
}

scenario_subquery_move_out_live_replay() {
  local base_url=$1
  local dir=$2
  local encoded_where
  encoded_where='id%20IN%20%28SELECT%20item_id%20FROM%20item_flags%20WHERE%20enabled%20%3D%20true%29'

  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&where=${encoded_where}&secret=$SECRET" "$dir/01-bootstrap"

  local handle
  local offset
  handle=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/update_item_flag_false.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=items&where=${encoded_where}&handle=${handle}&offset=${offset}&secret=$SECRET" "$dir/02-after-related-update"
}

scenario_handle_definition_mismatch_must_refetch() {
  local base_url=$1
  local dir=$2

  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&where=priority%20%3E%3D%202&secret=$SECRET" "$dir/01-bootstrap"

  local handle
  local offset
  handle=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-offset")

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=${offset}&where=priority%20%3E%3D%203&secret=$SECRET" "$dir/02-mismatched-definition"
}

scenario_log_full_offset_now_then_update() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=now&log=full&secret=$SECRET" "$dir/01-offset-now"

  local handle
  local offset
  handle=$(extract_header "$dir/01-offset-now/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-offset-now/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/update_item.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=${offset}&log=full&secret=$SECRET" "$dir/02-continuation"
}

scenario_log_changes_only_initial_snapshot() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&log=changes_only&secret=$SECRET" "$dir/01-initial-snapshot"
}

scenario_log_changes_only_offset_now_then_update() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=now&log=changes_only&secret=$SECRET" "$dir/01-offset-now"

  local handle
  local offset
  handle=$(extract_header "$dir/01-offset-now/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-offset-now/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/update_item.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=${offset}&log=changes_only&secret=$SECRET" "$dir/02-continuation"
}

scenario_replica_full_offset_now_then_update() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=now&replica=full&secret=$SECRET" "$dir/01-offset-now"

  local handle
  local offset
  handle=$(extract_header "$dir/01-offset-now/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-offset-now/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/update_item.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=${offset}&replica=full&secret=$SECRET" "$dir/02-continuation"
}

scenario_overload_existing_live_request() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=items&offset=-1&secret=$SECRET" "$dir/01-bootstrap"

  local handle
  handle=$(extract_header "$dir/01-bootstrap/headers.txt" "electric-handle")

  (
    curl \
      --silent \
      --show-error \
      --max-time "${CURL_MAX_TIME:-5}" \
      --request GET \
      --output "$dir/02-blocking-live-body.txt" \
      "$base_url/v1/shape?table=items&handle=${handle}&offset=0_0&live=true&secret=$SECRET" \
      >"$dir/02-blocking-live-stdout.txt" 2>"$dir/02-blocking-live-stderr.txt"
  ) &
  local blocker_pid=$!

  sleep 1
  capture_http "GET" "$base_url/v1/shape?table=items&handle=${handle}&offset=0_0&live=true&secret=$SECRET" "$dir/03-overloaded"

  wait "$blocker_pid" || true
}

scenario_partition_root_snapshot() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=partitioned_items&offset=-1&secret=$SECRET" "$dir/01-partition-root-snapshot"
}

scenario_partition_offset_now_then_insert() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=partitioned_items&offset=now&secret=$SECRET" "$dir/01-offset-now"

  local handle
  local offset
  handle=$(extract_header "$dir/01-offset-now/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-offset-now/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/insert_partition_item.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=partitioned_items&handle=${handle}&offset=${offset}&secret=$SECRET" "$dir/02-continuation"
}

scenario_partition_child_offset_now_then_insert() {
  local base_url=$1
  local dir=$2
  capture_http "GET" "$base_url/v1/shape?table=partitioned_items_100&offset=now&secret=$SECRET" "$dir/01-offset-now"

  local handle
  local offset
  handle=$(extract_header "$dir/01-offset-now/headers.txt" "electric-handle")
  offset=$(extract_header "$dir/01-offset-now/headers.txt" "electric-offset")

  run_sql_file "$E2E_DIR/sql/insert_partition_item_100.sql"
  sleep 1

  capture_http "GET" "$base_url/v1/shape?table=partitioned_items_100&handle=${handle}&offset=${offset}&secret=$SECRET" "$dir/02-continuation"
}
