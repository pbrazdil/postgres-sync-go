# PulseSync

PulseSync is a Go rewrite of the ElectricSQL sync service.

It exposes an Electric-compatible HTTP surface, can run as an embedded library inside another Go process, and can also run as a standalone binary.

## Status

PulseSync is usable for local development, protocol evaluation, and side-by-side shadow runs.

It is not cutover-ready yet.

What is implemented today:

- Electric-style `/v1/shape` and `/v1/health` endpoints
- in-memory and disk-backed shape storage
- embedded Go API and standalone binary
- eager runtime startup with readiness states
- logical replication startup and reconnect supervision
- PK-targeted live refresh for root-table and partition fanout paths
- long-poll and SSE live delivery
- `SYNC_FEATURE_FLAGS` parsing, including rejection of subquery shapes unless `allow_subqueries` is enabled
- dependent-shape live replay for refreshable subquery shapes, including move-in and move-out events
- Dockerized differential comparison for the current supported scenario set
- Dockerized PulseSync lifecycle validation for disk restart continuity, corrupt-shape recovery, and reconnect health transitions
- conservative invalidation and `must-refetch` behavior when correctness cannot be proven

What is still missing for parity signoff:

- broader dependent-shape tracking for complex nested, negated, or multi-hop subquery plans
- longer-running shadow validation for SSE behavior, client reconnects, storage growth, WAL retention, and production recovery drills
- production validation against unchanged compatible clients in shadow mode

## Goals

- keep the public Go API small and embeddable
- keep the HTTP surface compatible with existing clients
- prefer correctness over stale or guessed live results
- keep the default runtime lightweight and memory-backed
- support a durable `disk` mode without changing the public API

## Repository Layout

- `cmd/pulsesync`: standalone binary entrypoint
- `pkg/pulsesync`: public Go API
- `internal/config`: config defaults, validation, env loading
- `internal/httpapi`: HTTP router and health surface
- `internal/pg`: Postgres query and logical replication runtime
- `internal/protocol`: Electric-compatible request parsing and response delivery
- `internal/shapes`: shape identity, in-memory state, diffing, subscriptions
- `internal/storage`: memory and disk-backed persistence
- `ARCHITECTURE.md`: short physical codemap and architectural invariants
- `docs`: architecture, harness workflow, quality map, and technical debt tracker
- `scripts`: local validation and repository harness utilities
- `test/e2e`: seed data, manual curls, side-by-side runner, and normalized protocol compare harness

## Agent/Harness Workflow

PulseSync keeps repository knowledge local and executable so future agent runs can validate their own work.

- `AGENTS.md` is the short map for future agents.
- `ARCHITECTURE.md` describes package boundaries, runtime flow, and invariants.
- `docs/HARNESS_ENGINEERING.md` describes the feedback loop and artifact rules.
- `docs/QUALITY.md` maps change types to validation gates.
- `docs/tech-debt-tracker.md` keeps parity and hardening debt visible.

Default local validation:

```bash
./scripts/harness-check.sh
```

Heavier gates:

```bash
./scripts/harness-check.sh --docker-e2e
./scripts/harness-check.sh --lifecycle
./scripts/harness-check.sh --all
```

## E2E Harness

The repo includes a small end-to-end harness for exercising PulseSync against a seeded Postgres database and comparing protocol output.

Useful entrypoints:

- `./test/e2e/start-both.sh`
  Starts both sync services side by side against the same Postgres instance for manual inspection.
- `./test/e2e/start-both-docker.sh`
  Starts dockerized Postgres and both sync services side by side and streams container logs.
- `./test/e2e/manual_curls.sh`
  Ready-made curl commands for `/v1/health`, snapshots, subset requests, continuations, long-poll, SSE, and partitioned tables.
- `./test/e2e/compare.sh`
  Runs the current scenario set one implementation at a time, normalizes unstable headers and IDs, and diffs the results.
- `./test/e2e/compare-docker.sh`
  Runs the same compare flow using Docker containers instead of host `go run` and `mix run`.
- `./test/e2e/validate-pulsesync-docker.sh`
  Runs PulseSync-specific lifecycle checks for disk restart continuity, deterministic `must-refetch` on corrupt persisted state, and health degradation/recovery across replication disconnects.

The current differential matrix covers health, snapshots, subset requests, subset subquery rejection, `offset=now` continuations, long-poll, SSE insert delivery and keepalives, truncate invalidation, subquery feature-flag rejection, dependent-shape move-in/move-out live replay, handle-definition mismatch, overload handling, `log=full`, `log=changes_only`, `replica=full`, partitioned-root fanout, and child-partition fanout.

Artifacts are written under `test/e2e/_artifacts/<timestamp>/` and include raw request/response files, normalized outputs, Postgres debug snapshots, and per-service logs.

## Requirements

- Go `1.26` or newer to build from source
- PostgreSQL with logical replication enabled
- a database role that can:
  - connect and query tables
  - create the publication used by PulseSync
  - create and use logical replication slots
- Postgres configured with logical replication capacity, typically including:
  - `wal_level=logical`
  - `max_replication_slots > 0`
  - `max_wal_senders > 0`

Notes:

- `DATABASE_URL` should point to a direct Postgres connection, not a transaction-pooled proxy, because PulseSync also uses it for logical replication.
- `SYNC_POOLED_DATABASE_URL` can point to a pooled query endpoint if you want separate query and replication connections.

## Standalone Usage

Build the binary:

```bash
go build ./cmd/pulsesync
```

Run it with a minimal config:

```bash
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/app?sslmode=disable'
export SYNC_POOLED_DATABASE_URL="$DATABASE_URL"
export SYNC_SECRET='dev-secret'
export SYNC_PORT=3000
export SYNC_STORAGE_MODE=memory

go run ./cmd/pulsesync
```

For local testing without a shared secret:

```bash
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/app?sslmode=disable'
export SYNC_INSECURE=true

go run ./cmd/pulsesync
```

Check health:

```bash
curl http://localhost:3000/v1/health
```

Health states:

- `starting`: process is booting
- `waiting`: process is up, but replication is not currently active
- `active`: query + replication runtime is active

`/v1/health` returns:

- `200` when status is `active`
- `202` when status is `starting` or `waiting`

## Docker

Build the container image:

```bash
docker build -t pulsesync:local .
```

Run PulseSync with the bundled local Compose stack:

```bash
docker compose up --build
```

That stack starts:

- Postgres with logical replication enabled
- PulseSync on `http://127.0.0.1:43100`

The default host ports intentionally avoid common local sync-service/Postgres development ports:

- PulseSync: `43100`
- Postgres: `45432`

Useful environment overrides:

- `PULSESYNC_HTTP_PORT`
- `PULSESYNC_POSTGRES_PORT`
- `SYNC_SECRET`
- `SYNC_REPLICATION_STREAM_ID`
- `SYNC_FEATURE_FLAGS`
- `SYNC_STORAGE_MODE`

The default Compose config uses durable `disk` storage in a Docker volume mounted at `/var/lib/pulsesync`.

Example request:

```bash
curl 'http://127.0.0.1:43100/v1/shape?table=items&offset=-1&secret=dev-secret'
```

## Embedded Usage

```go
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/petrbrazdil/pulsesync/pkg/pulsesync"
)

func main() {
	cfg := pulsesync.DefaultConfig()
	cfg.DatabaseURL = "postgres://postgres:postgres@localhost:5432/app?sslmode=disable"
	cfg.PooledDatabaseURL = cfg.DatabaseURL
	cfg.Secret = "dev-secret"
	cfg.Storage.Mode = pulsesync.StorageModeMemory

	engine, err := pulsesync.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	if err := engine.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	defer func() {
		_ = engine.Close(context.Background())
	}()

	log.Fatal(http.ListenAndServe(":3000", engine.Handler()))
}
```

Public API:

- `pulsesync.New(Config) (*Engine, error)`
- `(*Engine).Start(context.Context) error`
- `(*Engine).Handler() http.Handler`
- `(*Engine).Status() pulsesync.Status`
- `(*Engine).Close(context.Context) error`

## HTTP Surface

Implemented routes:

- `GET /`
- `GET /v1/health`
- `GET|HEAD|POST|DELETE /v1/shape`
- `OPTIONS /v1/shape`
- `GET /metrics`

`/metrics` currently exposes only a static `pulsesync_info` metric. It is not a full Prometheus instrumentation surface yet.

### `/v1/shape` Request Surface

Supported request fields today:

- `table`
- `offset`
- `handle`
- `live`
- `live_sse`
- `experimental_live_sse`
- `where`
- `params`
- `columns`
- `replica`
- `log`
- `subset__*` query fields
- POST JSON subset body

Notes:

- `OPTIONS /v1/shape` is unauthenticated.
- `secret` and legacy `api_secret` query parameters are accepted.
- `live_sse=true` requires `live=true`.
- subset requests are snapshot-only; they do not long-poll and they do not stream SSE.
- shape deletion via `DELETE /v1/shape` is available only when `SYNC_ALLOW_SHAPE_DELETION=true`.

### Example Requests

Initial snapshot:

```bash
curl 'http://localhost:3000/v1/shape?table=items&offset=-1&secret=dev-secret'
```

Continue from a handle and offset:

```bash
curl 'http://localhost:3000/v1/shape?table=items&handle=<handle>&offset=0_0&secret=dev-secret'
```

Start at the current continuation point without historical rows:

```bash
curl 'http://localhost:3000/v1/shape?table=items&offset=now&secret=dev-secret'
```

Live long-poll:

```bash
curl 'http://localhost:3000/v1/shape?table=items&handle=<handle>&offset=0_0&live=true&secret=dev-secret'
```

Live SSE:

```bash
curl -N 'http://localhost:3000/v1/shape?table=items&handle=<handle>&offset=0_0&live=true&live_sse=true&secret=dev-secret'
```

## Configuration

PulseSync currently loads its standalone config from environment variables.

| Variable | Required | Default | Notes |
| --- | --- | --- | --- |
| `DATABASE_URL` | yes | none | Direct Postgres connection. Used for replication. |
| `SYNC_POOLED_DATABASE_URL` | no | `DATABASE_URL` | Optional separate query connection. |
| `SYNC_SECRET` | yes unless insecure | none | Shared secret for `/v1/shape`. |
| `SYNC_INSECURE` | no | `false` | Disables secret enforcement. |
| `SYNC_PORT` | no | `3000` | HTTP listen port. |
| `SYNC_REPLICATION_STREAM_ID` | no | `default` | Used in publication and slot naming. |
| `SYNC_DB_POOL_SIZE` | no | `20` | Max query pool size. |
| `SYNC_MAX_CONCURRENT_REQUESTS` | no | `{"initial":300,"existing":10000}` | Admission limits for initial vs existing shape requests. |
| `SYNC_CACHE_MAX_AGE` | no | `60` | Default cache max-age for non-live responses. |
| `SYNC_CACHE_STALE_AGE` | no | `300` | Default `stale-while-revalidate` for non-live responses. |
| `SYNC_STORAGE_MODE` | no | `memory` | `memory` or `disk`. |
| `SYNC_STORAGE_DIR` | no | `./.pulsesync` in `disk` mode | Storage directory for SQLite metadata and chunk files. |
| `SYNC_LONG_POLL_TIMEOUT_MS` | no | `20000` | Wait duration for long-poll live requests. |
| `SYNC_SSE_TIMEOUT_MS` | no | `60000` | Used for SSE cache headers. PulseSync defaults SSE keepalive comments to the compatible 21s cadence. |
| `SYNC_ALLOW_SHAPE_DELETION` | no | `false` | Enables `DELETE /v1/shape`. |
| `SYNC_FEATURE_FLAGS` | no | none | Comma-separated feature flags. PulseSync recognizes `allow_subqueries` and `tagged_subqueries`; subquery shapes are rejected unless `allow_subqueries` is present. |

Current gaps in standalone config loading:

- `ListenHost` is available in the Go config type, but not yet loaded from env.
- telemetry metrics path is configurable in the Go config type, but not yet loaded from env.

## Storage Modes

### `memory`

- default mode
- shape state is ephemeral
- replication continuity is not preserved across restart
- replication slot is temporary

### `disk`

- persists shape metadata, materialized rows, change logs, and runtime checkpoint
- uses SQLite for metadata
- uses append-only JSON chunk files for persisted change logs
- uses a named persistent replication slot derived from the replication stream ID
- attempts continuity from the last confirmed LSN on restart

Recovery behavior in `disk` mode:

- if persisted state is valid, handles and offsets are preserved
- if checkpoint, slot, or database identity is incompatible, PulseSync invalidates persisted shapes and forces client refetch
- if one persisted shape is corrupt, PulseSync tombstones that shape instead of aborting the entire catalog load

## How It Works

At a high level:

1. PulseSync loads config and creates either a memory store or disk store.
2. `Start()` eagerly opens the query pool, verifies Postgres connectivity, reconciles publication state, opens logical replication, and begins supervising reconnects.
3. A shape request is canonicalized into a stable hash from its definition. Identical definitions map to the same shape handle.
4. Initial requests build a snapshot and materialized row set for that shape.
5. The replication loop buffers row changes per transaction by relation and primary key.
6. On commit, PulseSync refreshes only the changed primary keys for candidate shapes instead of resnapshotting whole relations.
7. Long-poll and SSE consumers wait on shape-local change notifications.
8. In `disk` mode, confirmed runtime checkpoints and shape state are persisted for restart continuity.

Important runtime behavior:

- partition writes fan out to shapes registered on the concrete partition and on the partition root
- `TRUNCATE` does not try to replay row-level semantics; affected shapes are invalidated
- when PulseSync cannot prove a live shape update is correct, it prefers invalidation and `must-refetch`

## Constraints

- This is the open-source sync-service surface, not Electric Cloud.
- The query path still accepts raw `where` and related filter strings and passes them through to Postgres. There is no full Electric query planner or SQL validator yet.
- Publication handling is currently automatic and uses `FOR ALL TABLES`.
- Metrics are minimal.
- The main compatibility target is existing HTTP clients, not a new direct Go materialization API.

## Known Issues

- The repo now has a real protocol differential runner and a PulseSync lifecycle validator, but the covered matrix is still too small for cutover.
- Cross-table dependent live semantics are not fully implemented.
- `tagged_subqueries` is recognized for config compatibility. PulseSync emits stable dependency tags for refreshable subquery shapes, but complex nested or negated dependency plans still need broader parity coverage.
- Shapes that cannot be safely refreshed are handled conservatively and may still force refetch rather than risk stale results.
- `disk` mode restart continuity and corrupt-state recovery are covered by the Docker validator, but not yet by long-running shadow traffic with a representative workload.
- Standalone config still has a few env-loading gaps, notably listen host and metrics path.

## Future TODO

- expand the protocol differential matrix with longer-running SSE, more replica modes, shape deletion/rotation, and complex tagged dependent-shape cases
- add more real integration coverage around unsupported live invalidation paths
- validate long-running restart continuity and slot behavior under representative shadow traffic
- add manual publication mode
- improve unsupported-shape detection with a more robust dependency model
- expand telemetry and Prometheus metrics
- polish Docker packaging and add deployment examples

## Development

Run tests:

```bash
go test ./...
```

Run vet:

```bash
go vet ./...
```

The current development version is `0.1.0-dev`.
