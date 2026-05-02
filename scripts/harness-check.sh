#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

RUN_DOCKER_E2E=0
RUN_LIFECYCLE=0
DOCS_ONLY=0

usage() {
  cat <<'USAGE'
Usage: ./scripts/harness-check.sh [--docs-only] [--docker-e2e] [--lifecycle] [--all]

Runs the local PulseSync validation harness.

Options:
  --docs-only    Validate repository knowledge-map files only.
  --docker-e2e   Also run the Docker Electric-vs-PulseSync comparison matrix.
  --lifecycle    Also run PulseSync Docker lifecycle validation.
  --all          Run local checks, Docker comparison, and lifecycle validation.
  -h, --help     Show this help.
USAGE
}

while (($# > 0)); do
  case "$1" in
    --docs-only)
      DOCS_ONLY=1
      ;;
    --docker-e2e)
      RUN_DOCKER_E2E=1
      ;;
    --lifecycle)
      RUN_LIFECYCLE=1
      ;;
    --all)
      RUN_DOCKER_E2E=1
      RUN_LIFECYCLE=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

log() {
  printf '[harness] %s\n' "$*" >&2
}

fail() {
  printf '[harness] error: %s\n' "$*" >&2
  exit 1
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "missing required command: $1"
  fi
}

require_file() {
  if [ ! -f "$1" ]; then
    fail "missing required file: $1"
  fi
}

run() {
  log "$*"
  "$@"
}

check_docs() {
  log "checking repository knowledge map"

  require_file AGENTS.md
  require_file ARCHITECTURE.md
  require_file README.md
  require_file docs/ARCHITECTURE.md
  require_file docs/HARNESS_ENGINEERING.md
  require_file docs/QUALITY.md
  require_file docs/tech-debt-tracker.md
  require_file docs/exec-plans/README.md
  require_file test/e2e/README.md

  grep -Fq 'electric/' .gitignore || fail ".gitignore must keep local comparison sources untracked"
  grep -Fq './scripts/harness-check.sh' AGENTS.md || fail "AGENTS.md must point agents at the harness check"
  grep -Fq 'ARCHITECTURE.md' AGENTS.md || fail "AGENTS.md must point at architecture docs"
  grep -Fq "## Bird's Eye View" ARCHITECTURE.md || fail "ARCHITECTURE.md must start with a bird's eye view"
  grep -Fq '## Codemap' ARCHITECTURE.md || fail "ARCHITECTURE.md must include a codemap"
  grep -Fq '## Architectural Invariants' ARCHITECTURE.md || fail "ARCHITECTURE.md must call out invariants"
  grep -Fq '## Cross-Cutting Concerns' ARCHITECTURE.md || fail "ARCHITECTURE.md must cover cross-cutting concerns"
  grep -Fq 'test/e2e/compare-docker.sh' docs/HARNESS_ENGINEERING.md || fail "harness docs must mention Docker comparison"
  grep -Fq 'Dependent shapes' docs/tech-debt-tracker.md || fail "tech debt tracker must keep dependent-shape parity visible"
  local old_env_refs
  old_env_refs=$(grep -R -n -E 'ELECTRIC_[A-Z0-9_]+' Dockerfile docker-compose.yml internal pkg README.md ARCHITECTURE.md docs 2>/dev/null || true)
  if [ -n "$old_env_refs" ]; then
    printf '%s\n' "$old_env_refs" >&2
    fail "PulseSync-owned config must use SYNC_* env vars"
  fi
}

check_shell() {
  log "checking shell syntax"

  run bash -n \
    scripts/harness-check.sh \
    test/e2e/lib.sh \
    test/e2e/compare.sh \
    test/e2e/compare-docker.sh \
    test/e2e/manual_curls.sh \
    test/e2e/start-both.sh \
    test/e2e/start-both-docker.sh \
    test/e2e/validate-pulsesync-docker.sh
}

check_go_format() {
  log "checking gofmt"

  local files
  files=$(find cmd internal pkg test -name '*.go' -print | sort)
  if [ -z "$files" ]; then
    return
  fi

  local unformatted
  unformatted=$(gofmt -l $files)
  if [ -n "$unformatted" ]; then
    printf '%s\n' "$unformatted" >&2
    fail "Go files need gofmt"
  fi
}

check_go() {
  require_cmd go

  check_go_format
  run go test ./...
  run go vet ./...
}

check_git_whitespace() {
  if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    run git diff --check
  fi
}

run_docker_e2e() {
  require_cmd docker
  require_cmd go
  require_cmd curl
  require_cmd psql

  run ./test/e2e/compare-docker.sh
}

run_lifecycle() {
  require_cmd docker
  require_cmd go
  require_cmd curl
  require_cmd psql

  run ./test/e2e/validate-pulsesync-docker.sh
}

check_docs

if [ "$DOCS_ONLY" -eq 1 ]; then
  log "docs-only checks passed"
  exit 0
fi

check_shell
check_go
check_git_whitespace

if [ "$RUN_DOCKER_E2E" -eq 1 ]; then
  run_docker_e2e
fi

if [ "$RUN_LIFECYCLE" -eq 1 ]; then
  run_lifecycle
fi

log "all requested checks passed"
