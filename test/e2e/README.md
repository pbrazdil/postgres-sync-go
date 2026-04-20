# E2E Harness

This directory contains a small but extensible end-to-end protocol comparison harness for PulseSync.

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
  Starts dockerized Postgres, PulseSync, and the comparison service, then streams container logs.
- `compare.sh`
  Runs the seeded scenarios against PulseSync and the comparison service one by one, then diffs normalized outputs.
- `compare-docker.sh`
  Runs the same compare flow against Docker containers instead of host processes.
- `validate-pulsesync-docker.sh`
  Runs PulseSync-only lifecycle validation for disk restart continuity, corrupt persisted-shape recovery, and reconnect health transitions.
- `cmd/pulsediff`
  Small helper used to normalize HTTP headers and bodies before diffing.
- `docker-compose.yml`
  Containerized side-by-side stack for Postgres, PulseSync, and the comparison service.

## Default Ports

- Host-run harness defaults:
  - Postgres: `54321`
  - PulseSync: `3100`
  - Comparison service: `3200`
- Dockerized harness defaults:
  - auto-selected free host ports in high one-off ranges
  - Postgres range: `45432-45532`
  - PulseSync range: `43100-43199`
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

`mix` is not required when you use `compare-docker.sh` or `start-both-docker.sh`.

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

Run the PulseSync lifecycle validator:

```bash
./test/e2e/validate-pulsesync-docker.sh
```

The Docker scripts choose free host ports by default and print the selected ports before startup. You can still pin them explicitly with `DB_PORT`, `PULSE_PORT`, and `COMPARE_PORT`.

The Docker compare runner rotates the PulseSync and comparison-service host ports per scenario inside those ranges so rapid container restarts do not contend with lingering Docker port allocations.

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
- `subset_get_snapshot`
- `subset_post_snapshot`
- `subset_subquery_rejected`
- `offset_now_then_insert`
- `offset_now_then_update`
- `offset_now_then_delete`
- `live_longpoll_insert`
- `live_sse_insert`
- `live_sse_keepalive`
- `truncate_then_must_refetch`
- `subquery_rejected_without_feature_flag`
- `subquery_move_in_live_replay`
- `subquery_move_out_live_replay`
- `handle_definition_mismatch_must_refetch`
- `log_full_offset_now_then_update`
- `log_changes_only_initial_snapshot`
- `log_changes_only_offset_now_then_update`
- `replica_full_offset_now_then_update`
- `overload_existing_live_request`
- `partition_root_snapshot`
- `partition_offset_now_then_insert`
- `partition_child_offset_now_then_insert`

## Current PulseSync Validation Scenarios

- `disk_restart_continuity`
- `disk_corrupt_shape_recovery`
- `reconnect_health_and_continuation`

## Notes

- The compare runner executes PulseSync and the comparison service one by one, not simultaneously. That makes the DB state deterministic across both runs.
- The side-by-side runner exists separately for manual inspection and debugging.
- The Docker compare runner keeps Postgres and both sync services in containers but still uses host `curl`, `psql`, and the Go normalizer helper.
- The Docker scripts also generate a unique Compose project name by default so they do not trample another long-running stack from the same repo checkout.
- Response normalization intentionally removes unstable values such as dynamic handles, cursors, and etags. The goal is to compare semantics, not instance-local IDs.
- The normalizer preserves the presence and count of dependent-shape tags, but replaces instance-local tag values before diffing.
- Raw request/response files and service logs are still stored so mismatches can be debugged from the underlying data.

## Future Expansion

The harness is intentionally small today, but it is structured to grow into a fuller parity suite. The next useful additions are:

- complex nested and negated dependent-shape matrices
- shape deletion scenarios
- a broader matrix for replica modes and column projections
- broader disk corruption and recovery matrices
