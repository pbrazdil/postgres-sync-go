#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

COMPOSE_FILE=${COMPOSE_FILE:-$SCRIPT_DIR/docker-compose.yml}
COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-postgres-sync-go-shadow-client-$(date +%Y%m%d%H%M%S)}
SHADOW_CLIENT_PACKAGE_SPEC=${SHADOW_CLIENT_PACKAGE_SPEC:-@electric-sql/client}
SHADOW_CLIENT_WORKDIR=${SHADOW_CLIENT_WORKDIR:-$ARTIFACTS_DIR/shadow-client-package}
SHADOW_CLIENT_RESULT_FILE=${SHADOW_CLIENT_RESULT_FILE:-$ARTIFACTS_DIR/shadow-client/result.json}

compose_cmd() {
  docker compose -p "$COMPOSE_PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
}

cleanup() {
  compose_cmd logs --no-color postgres >"$ARTIFACTS_DIR/shadow-client/postgres.log" 2>&1 || true
  compose_cmd logs --no-color postgres-sync-go >"$ARTIFACTS_DIR/shadow-client/postgres-sync-go.log" 2>&1 || true
  compose_cmd down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

ensure_shadow_client() {
  require_cmd node

  if [ -n "${SHADOW_CLIENT_IMPORT:-}" ]; then
    export SHADOW_CLIENT_IMPORT
    return 0
  fi

  if [ -n "${SHADOW_CLIENT_DIR:-}" ]; then
    if [ ! -f "$SHADOW_CLIENT_DIR/dist/index.mjs" ]; then
      require_cmd pnpm
      local workspace=$SHADOW_CLIENT_DIR
      while [ "$workspace" != "/" ] && [ ! -f "$workspace/pnpm-workspace.yaml" ]; do
        workspace=$(dirname "$workspace")
      done
      if [ ! -f "$workspace/pnpm-workspace.yaml" ]; then
        echo "could not find pnpm workspace for SHADOW_CLIENT_DIR=$SHADOW_CLIENT_DIR" >&2
        exit 1
      fi
      local package_name
      package_name=$(node -e "const fs=require('fs'); const p=JSON.parse(fs.readFileSync(process.argv[1], 'utf8')); console.log(p.name)" "$SHADOW_CLIENT_DIR/package.json")
      log "building shadow client package $package_name"
      (cd "$workspace" && pnpm install --frozen-lockfile && pnpm --filter "$package_name" build)
    fi
    SHADOW_CLIENT_IMPORT="$SHADOW_CLIENT_DIR/dist/index.mjs"
    export SHADOW_CLIENT_IMPORT
    return 0
  fi

  if [ ! -f "$SHADOW_CLIENT_WORKDIR/node_modules/@electric-sql/client/dist/index.mjs" ]; then
    require_cmd npm
    log "installing shadow client package: $SHADOW_CLIENT_PACKAGE_SPEC"
    mkdir -p "$SHADOW_CLIENT_WORKDIR"
    npm install --prefix "$SHADOW_CLIENT_WORKDIR" "$SHADOW_CLIENT_PACKAGE_SPEC" >/dev/null
  fi

  SHADOW_CLIENT_IMPORT="$SHADOW_CLIENT_WORKDIR/node_modules/@electric-sql/client/dist/index.mjs"
  export SHADOW_CLIENT_IMPORT
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

main() {
  ensure_common_requirements
  require_cmd docker
  ensure_shadow_client

  configure_one_off_docker_ports
  SCENARIO_FEATURE_FLAGS='allow_subqueries,tagged_subqueries'
  export SCENARIO_FEATURE_FLAGS

  mkdir -p "$(dirname "$SHADOW_CLIENT_RESULT_FILE")"
  log "using one-off host ports: postgres=$DB_PORT postgres-sync-go=$SYNC_GO_PORT"
  log "using compose project: $COMPOSE_PROJECT_NAME"
  log "using shadow client: $SHADOW_CLIENT_IMPORT"

  start_postgres_compose
  reset_database
  capture_pg_debug "$ARTIFACTS_DIR/shadow-client/db-before"
  start_postgres_sync_go_compose

  env \
    ROOT_DIR="$ROOT_DIR" \
    E2E_DIR="$E2E_DIR" \
    BASE_URL="http://127.0.0.1:${SYNC_GO_PORT}" \
    DATABASE_URL="$DATABASE_URL" \
    SECRET="$SECRET" \
    SHADOW_CLIENT_IMPORT="$SHADOW_CLIENT_IMPORT" \
    SHADOW_CLIENT_RESULT_FILE="$SHADOW_CLIENT_RESULT_FILE" \
    node "$SCRIPT_DIR/shadow-client.mjs" "$@"

  capture_pg_debug "$ARTIFACTS_DIR/shadow-client/db-after"

  echo
  echo "shadow client validation passed" >&2
  echo "artifacts: $ARTIFACTS_DIR/shadow-client" >&2
}

main "$@"
