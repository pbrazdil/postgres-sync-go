# E2E Harness

This directory contains a small but extensible end-to-end protocol comparison harness for postgres-sync-go.

It is designed for two workflows:

- deterministic one-by-one comparison, where the database is reset between implementations
- manual side-by-side shadow runs, where both services run at the same time against the same Postgres instance

## What It Includes

- `seed.sql`
  Creates the base schema and seed rows used by the harness.
- `sql/`
  Mutation scripts used by the live and continuation scenarios.
- `manual_curls.sh`
  Ready-to-run curl commands for manual testing.
- `start-both.sh`
  Starts both sync services side by side for interactive debugging.
- `start-both-docker.sh`
  Starts dockerized Postgres, postgres-sync-go, and the comparison service, then streams container logs.
- `compare.sh`
  Runs the seeded scenarios against postgres-sync-go and the comparison service one by one, then diffs normalized outputs.
- `compare-docker.sh`
  Runs the same compare flow against Docker containers instead of host processes.
- `validate-postgres-sync-go-docker.sh`
  Runs postgres-sync-go-only lifecycle validation for disk restart continuity, corrupt persisted-shape recovery, and reconnect health transitions.
- `shadow-client-docker.sh`
  Runs an unchanged compatible TypeScript client against dockerized postgres-sync-go and asserts client-observed shape state plus dependent subquery stream events.
- `shadow-client.mjs`
  Node runner used by `shadow-client-docker.sh`.
- `cmd/syncdiff`
  Small helper used to normalize HTTP headers and bodies before diffing.
- `docker-compose.yml`
  Containerized side-by-side stack for Postgres, postgres-sync-go, and the comparison service.

## Default Ports

- Host-run harness defaults:
  - Postgres: `54321`
  - postgres-sync-go: `3100`
  - Comparison service: `3200`
- Dockerized harness defaults:
  - auto-selected free host ports in high one-off ranges
  - Postgres range: `45432-45532`
  - postgres-sync-go range: `43100-43199`
  - Comparison service range: `43200-43299`

## Requirements

- `go`
- `curl`
- `psql`
- `mix` for running the comparison service from source
- `docker` if you want the harness to start the comparison Postgres automatically

For the Dockerized E2E path:

- `docker`
- `go`
- `curl`
- `psql`
- `node`
- `npm` unless `SHADOW_CLIENT_IMPORT` or `SHADOW_CLIENT_DIR` points at an already-built client package

`mix` is not required when you use `compare-docker.sh` or `start-both-docker.sh`.

## Optional Comparison Source

The protocol comparison scripts require a local comparison sync-service checkout. The directory is ignored by git and is optional for public users who only want to run postgres-sync-go tests.

Set these when the comparison source is not at the default local path:

```bash
export COMPARE_SYNC_DIR=/path/to/sync-service
export COMPARE_TELEMETRY_DIR=/path/to/comparison-telemetry
```

If the comparison source is unavailable, comparison scripts exit with code `77` and print a clear setup message. postgres-sync-go-only checks such as `./scripts/harness-check.sh` and `./test/e2e/validate-postgres-sync-go-docker.sh` do not require it.

## Quick Start

Start both services in parallel:

```bash
./test/e2e/start-both.sh
```

Run the current compare suite:

```bash
./test/e2e/compare.sh
```

Run the Dockerized side-by-side stack:

```bash
./test/e2e/start-both-docker.sh
```

Run the Dockerized compare suite:

```bash
./test/e2e/compare-docker.sh
```

Run the postgres-sync-go lifecycle validator:

```bash
./test/e2e/validate-postgres-sync-go-docker.sh
```

Run the TypeScript shadow-client validator:

```bash
./test/e2e/shadow-client-docker.sh
```

By default, `shadow-client-docker.sh` installs `@electric-sql/client` into `test/e2e/_artifacts/.../shadow-client-package`. To use a local unchanged client checkout instead, set `SHADOW_CLIENT_DIR` to the package directory or `SHADOW_CLIENT_IMPORT` to a built ESM entrypoint.

The Docker scripts choose free host ports by default and print the selected ports before startup. You can still pin them explicitly with `DB_PORT`, `SYNC_GO_PORT`, and `COMPARE_PORT`.

The Docker compare runner rotates the postgres-sync-go and comparison-service host ports per scenario inside those ranges so rapid container restarts do not contend with lingering Docker port allocations.

If you already have a suitable Postgres instance running at `DATABASE_URL`, skip the Docker startup step:

```bash
START_POSTGRES=0 DATABASE_URL='postgresql://...' ./test/e2e/compare.sh
```

Artifacts are written under:

```text
test/e2e/_artifacts/<timestamp>/
```

## Current Compare Scenarios

- `health`
- `initial_snapshot`
- `filtered_snapshot`
- `columns_snapshot`
- `columns_offset_now_then_update`
- `subset_get_snapshot`
- `subset_post_snapshot`
- `subset_subquery_rejected`
- `offset_now_then_insert`
- `offset_now_then_update`
- `offset_now_then_delete`
- `live_longpoll_insert`
- `live_sse_insert`
- `experimental_live_sse_insert`
- `live_sse_keepalive`
- `live_sse_resume_after_update`
- `truncate_then_must_refetch`
- `subquery_rejected_without_feature_flag`
- `subquery_move_in_live_replay`
- `subquery_move_out_live_replay`
- `subquery_nested_multi_hop_move_in_live_replay`
- `subquery_nested_multi_hop_move_out_live_replay`
- `subquery_negated_move_in_live_replay`
- `subquery_negated_move_out_live_replay`
- `handle_definition_mismatch_must_refetch`
- `unknown_handle_must_refetch`
- `shape_delete_handle_rotation`
- `cache_if_none_match_304`
- `log_full_offset_now_then_update`
- `log_changes_only_initial_snapshot`
- `log_changes_only_offset_now_then_update`
- `replica_default_offset_now_then_update`
- `replica_full_offset_now_then_update`
- `overload_existing_live_request`
- `partition_root_snapshot`
- `partition_offset_now_then_insert`
- `partition_child_offset_now_then_insert`

## Current postgres-sync-go Validation Scenarios

- `disk_restart_continuity`
- `disk_corrupt_shape_recovery`
- `reconnect_health_and_continuation`

## Current Shadow-Client Scenarios

- `snapshot`
- `filtered_snapshot`
- `columns_snapshot`
- `live_longpoll_insert`
- `live_sse_update`
- `subquery_move_in_out` through `ShapeStream` messages
- `partition_root_live_insert`

## Notes

- The compare runner executes postgres-sync-go and the comparison service one by one, not simultaneously. That makes the DB state deterministic across both runs.
- The side-by-side runner exists separately for manual inspection and debugging.
- The Docker compare runner keeps Postgres and both sync services in containers but still uses host `curl`, `psql`, and the Go normalizer helper.
- The Docker scripts also generate a unique Compose project name by default so they do not trample another long-running stack from the same repo checkout.
- Response normalization intentionally removes unstable values such as dynamic handles, cursors, and etags. The goal is to compare semantics, not instance-local IDs.
- Response normalization ignores `content-type` on `304 Not Modified` because Go's `net/http` suppresses it for empty 304 responses while the comparison service emits it.
- The normalizer preserves the presence and count of dependent-shape tags, but replaces instance-local tag values before diffing.
- Raw request/response files and service logs are still stored so mismatches can be debugged from the underlying data.

## Future Expansion

The harness is intentionally small today, but it is structured to grow into a fuller parity suite. The next useful additions are:

- broader disk corruption and recovery matrices
- longer-running shadow-client runs with reconnect and restart cycles
