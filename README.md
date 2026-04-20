# postgres-sync-go

**Fast agent platform built on sync  — built for humans and AI agents.**

Electric-compatible Shape sync for Postgres, written in Go.

`postgres-sync-go` is an independent Go implementation of the ElectricSQL sync service. It exposes an Electric-compatible HTTP surface, can run as an embedded library inside another Go process, and can also run as a standalone binary.

It targets the read path: syncing Shapes of Postgres data out to existing Electric-compatible clients. Writes continue to go through your application API, jobs, or database layer.

> Status: preview / shadow-ready. `postgres-sync-go` is usable for local development, protocol evaluation, and side-by-side shadow runs. It is not yet recommended as a primary production replacement without workload-specific parity and recovery validation.

## Standalone usage

Build the binary:

```bash
go build ./cmd/postgres-sync
```

Run with a minimal config:

```bash
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/app?sslmode=disable'
export SYNC_POOLED_DATABASE_URL="$DATABASE_URL"
export SYNC_SECRET='dev-secret'
export SYNC_PORT=3000
export SYNC_STORAGE_MODE=memory

go run ./cmd/postgres-sync
```

For local testing without a shared secret:

```bash
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/app?sslmode=disable'
export SYNC_INSECURE=true

go run ./cmd/postgres-sync
```

Check health:

```bash
curl http://localhost:3000/v1/health
```

Health states:

| State | Meaning |
| --- | --- |
| `starting` | process is booting |
| `waiting` | process is up, but replication is not currently active |
| `active` | query and replication runtime is active |

`/v1/health` returns:

| HTTP status | Runtime status |
| --- | --- |
| `200` | `active` |
| `202` | `starting` or `waiting` |

## Embedded usage

```go
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/pbrazdil/postgres-sync-go/pkg/pgsync"
)

func main() {
	cfg := pgsync.DefaultConfig()
	cfg.DatabaseURL = "postgres://postgres:postgres@localhost:5432/app?sslmode=disable"
	cfg.PooledDatabaseURL = cfg.DatabaseURL
	cfg.Secret = "dev-secret"
	cfg.Storage.Mode = pgsync.StorageModeMemory

	engine, err := pgsync.New(cfg)
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

```go
pgsync.New(Config) (*Engine, error)
(*Engine).Start(context.Context) error
(*Engine).Handler() http.Handler
(*Engine).Status() pgsync.Status
(*Engine).Close(context.Context) error
```

## Why postgres-sync-go?

- **Embeddable Go runtime**: use it inside an existing Go service instead of operating a separate sync process.
- **Standalone mode**: run it as a normal HTTP sync service when embedding is not desired.
- **Electric-compatible Shape API**: serves `/v1/shape` and `/v1/health` for compatible clients.
- **Logical replication runtime**: consumes Postgres changes and fans them out to registered Shapes.
- **Memory-first default**: starts lightweight for development and ephemeral deployments.
- **Durable disk mode**: persists shape metadata, rows, logs, and checkpoints for restart continuity.
- **Correctness-first live updates**: invalidates and forces refetch when it cannot prove a live result is safe.
- **Production-motivated performance**: in the production apps that motivated this implementation, the Go rewrite has been about 2x faster on average. Treat that as workload-specific and benchmark against your own traffic.

## Compatibility target

`postgres-sync-go` targets the open-source Electric sync-service HTTP surface, not Electric Cloud and not a new Go-native materialization API.

The main compatibility target is existing HTTP clients that consume Shape logs through `/v1/shape`, including initial snapshots, continuations, long-polling, and SSE live delivery.

## Current status

Implemented today:

- Electric-style `/v1/shape` and `/v1/health` endpoints
- in-memory and disk-backed shape storage
- embedded Go API and standalone binary
- eager runtime startup with readiness states
- logical replication startup and reconnect supervision
- primary-key-targeted live refresh for root-table and partition fanout paths
- long-poll and SSE live delivery
- `SYNC_FEATURE_FLAGS` parsing
- rejection of subquery Shapes unless `allow_subqueries` is enabled
- dependent-Shape live replay for refreshable subquery Shapes, including move-in and move-out events
- Dockerized differential comparison for the current supported scenario set
- Dockerized lifecycle validation for disk restart continuity, corrupt-Shape recovery, and reconnect health transitions
- Dockerized shadow-client validation with an unchanged compatible TypeScript client
- conservative invalidation and must-refetch behavior when correctness cannot be proven

Still missing before parity signoff:

- broader dependent-Shape tracking for complex nested, negated, or multi-hop subquery plans
- longer-running shadow validation for client reconnects, storage growth, WAL retention, and production recovery drills
- production-traffic shadow validation beyond the current seeded client matrix

## Design goals

- keep the public Go API small and embeddable
- keep the HTTP surface compatible with existing clients
- prefer correctness over stale or guessed live results
- keep the default runtime lightweight and memory-backed
- support a durable disk mode without changing the public API
- make validation local, repeatable, and friendly to agent-assisted development

## Repository layout

```text
cmd/postgres-sync        standalone binary entrypoint
pkg/pgsync               public Go API
internal/config          config defaults, validation, and env loading
internal/httpapi         HTTP router and health surface
internal/pg              Postgres query and logical replication runtime
internal/protocol        Electric-compatible request parsing and response delivery
internal/shapes          shape identity, in-memory state, diffing, subscriptions
internal/storage         memory and disk-backed persistence
ARCHITECTURE.md          physical codemap and architectural invariants
docs/                    architecture, harness workflow, quality map, debt tracker
scripts/                 local validation and repository harness utilities
test/e2e/                seeded data, curls, side-by-side runner, protocol compare harness
```

## Requirements

- Go 1.26 or newer to build from source
- PostgreSQL with logical replication enabled
- a database role that can:
  - connect and query tables
  - create the publication used by `postgres-sync-go`
  - create and use logical replication slots

Postgres must have logical replication capacity enabled, typically including:

```conf
wal_level = logical
max_replication_slots > 0
max_wal_senders > 0
```

Notes:

- `DATABASE_URL` should point to a direct Postgres connection, not a transaction-pooled proxy, because `postgres-sync-go` also uses it for logical replication.
- `SYNC_POOLED_DATABASE_URL` can point to a pooled query endpoint if you want separate query and replication connections.

## Docker

Build the container image:

```bash
docker build -t postgres-sync-go:local .
```

Run `postgres-sync-go` with the bundled local Compose stack:

```bash
docker compose up --build
```

That stack starts:

- Postgres with logical replication enabled
- `postgres-sync-go` on `http://127.0.0.1:43100`

The local Compose database is named `postgres_sync_go`.

The default host ports intentionally avoid common local sync-service and Postgres development ports:

| Service | Port |
| --- | ---: |
| `postgres-sync-go` | `43100` |
| Postgres | `45432` |

Useful environment overrides:

| Variable | Purpose |
| --- | --- |
| `POSTGRES_SYNC_GO_HTTP_PORT` | local Compose HTTP port |
| `POSTGRES_SYNC_GO_POSTGRES_PORT` | local Compose Postgres port |
| `SYNC_SECRET` | shared secret for `/v1/shape` |
| `SYNC_REPLICATION_STREAM_ID` | publication and slot naming seed |
| `SYNC_FEATURE_FLAGS` | comma-separated feature flags |
| `SYNC_STORAGE_MODE` | `memory` or `disk` |

The default Compose config uses durable disk storage in a Docker volume mounted at `/var/lib/postgres-sync-go`.

Example request:

```bash
curl 'http://127.0.0.1:43100/v1/shape?table=items&offset=-1&secret=dev-secret'
```

## HTTP surface

Implemented routes:

| Route | Description |
| --- | --- |
| `GET /` | basic service response |
| `GET /v1/health` | readiness and replication health |
| `GET /v1/shape` | Shape snapshot, continuation, long-poll, or SSE stream |
| `HEAD /v1/shape` | Shape metadata check |
| `POST /v1/shape` | Shape request with JSON subset body |
| `DELETE /v1/shape` | Shape deletion when explicitly enabled |
| `OPTIONS /v1/shape` | CORS preflight; unauthenticated |
| `GET /metrics` | minimal metrics surface |

`/metrics` currently exposes only a static `postgres_sync_go_info` metric. It is not a full Prometheus instrumentation surface yet.

## `/v1/shape` request support

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
- `POST` JSON subset body

Notes:

- `OPTIONS /v1/shape` is unauthenticated.
- `secret` and legacy `api_secret` query parameters are accepted.
- `live_sse=true` requires `live=true`.
- subset requests are snapshot-only; they do not long-poll and they do not stream SSE.
- Shape deletion via `DELETE /v1/shape` is available only when `SYNC_ALLOW_SHAPE_DELETION=true`.

## Example requests

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

Standalone config is currently loaded from environment variables.

| Variable | Required | Default | Notes |
| --- | --- | --- | --- |
| `DATABASE_URL` | yes | none | Direct Postgres connection. Used for replication. |
| `SYNC_POOLED_DATABASE_URL` | no | `DATABASE_URL` | Optional separate query connection. |
| `SYNC_SECRET` | yes unless insecure | none | Shared secret for `/v1/shape`. |
| `SYNC_INSECURE` | no | `false` | Disables secret enforcement. |
| `SYNC_PORT` | no | `3000` | HTTP listen port. |
| `SYNC_REPLICATION_STREAM_ID` | no | `default` | Used in publication and slot naming. |
| `SYNC_DB_POOL_SIZE` | no | `20` | Max query pool size. |
| `SYNC_MAX_CONCURRENT_REQUESTS` | no | `{"initial":300,"existing":10000}` | Admission limits for initial vs existing Shape requests. |
| `SYNC_CACHE_MAX_AGE` | no | `60` | Default cache max-age for non-live responses. |
| `SYNC_CACHE_STALE_AGE` | no | `300` | Default stale-while-revalidate for non-live responses. |
| `SYNC_STORAGE_MODE` | no | `memory` | `memory` or `disk`. |
| `SYNC_STORAGE_DIR` | no | `./.postgres-sync-go` in disk mode | Storage directory for SQLite metadata and chunk files. |
| `SYNC_LONG_POLL_TIMEOUT_MS` | no | `20000` | Wait duration for long-poll live requests. |
| `SYNC_SSE_TIMEOUT_MS` | no | `60000` | Used for SSE cache headers. SSE keepalive comments use the compatible 21s cadence. |
| `SYNC_ALLOW_SHAPE_DELETION` | no | `false` | Enables `DELETE /v1/shape`. |
| `SYNC_FEATURE_FLAGS` | no | none | Comma-separated feature flags. Recognizes `allow_subqueries` and `tagged_subqueries`; subquery Shapes are rejected unless `allow_subqueries` is present. |

Current gaps in standalone config loading:

- `ListenHost` is available in the Go config type, but not yet loaded from env.
- the telemetry metrics path is configurable in the Go config type, but not yet loaded from env.

## Storage modes

### `memory`

- default mode
- Shape state is ephemeral
- replication continuity is not preserved across restart
- replication slot is temporary

### `disk`

- persists Shape metadata, materialized rows, change logs, and runtime checkpoint
- uses SQLite for metadata
- uses append-only JSON chunk files for persisted change logs
- uses a named persistent replication slot derived from the replication stream ID
- attempts continuity from the last confirmed LSN on restart

Recovery behavior in disk mode:

- if persisted state is valid, handles and offsets are preserved
- if checkpoint, slot, or database identity is incompatible, `postgres-sync-go` invalidates persisted Shapes and forces client refetch
- if one persisted Shape is corrupt, `postgres-sync-go` tombstones that Shape instead of aborting the entire catalog load

## How it works

At a high level:

1. `postgres-sync-go` loads config and creates either a memory store or disk store.
2. `Start()` eagerly opens the query pool, verifies Postgres connectivity, reconciles publication state, opens logical replication, and begins supervising reconnects.
3. A Shape request is canonicalized into a stable hash from its definition. Identical definitions map to the same Shape handle.
4. Initial requests build a snapshot and materialized row set for that Shape.
5. The replication loop buffers row changes per transaction by relation and primary key.
6. On commit, `postgres-sync-go` refreshes only the changed primary keys for candidate Shapes instead of resnapshotting whole relations.
7. Long-poll and SSE consumers wait on Shape-local change notifications.
8. In disk mode, confirmed runtime checkpoints and Shape state are persisted for restart continuity.

Important runtime behavior:

- partition writes fan out to Shapes registered on the concrete partition and on the partition root
- `TRUNCATE` does not try to replay row-level semantics; affected Shapes are invalidated
- when `postgres-sync-go` cannot prove a live Shape update is correct, it prefers invalidation and must-refetch over stale results

## Validation workflow

`postgres-sync-go` keeps repository knowledge local and executable so future agent or maintainer runs can validate their own work.

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
./scripts/harness-check.sh --shadow-client
./scripts/harness-check.sh --all
```

## Roadmap

Near-term work:

- expand the protocol differential matrix with longer-running SSE, more replica modes, Shape deletion/rotation, and complex tagged dependent-Shape cases
- add more real integration coverage around unsupported live invalidation paths
- validate long-running restart continuity and slot behavior under representative shadow traffic
- add manual publication mode
- improve unsupported-Shape detection with a more robust dependency model
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

The current preview version is `v0.1.0-preview.1`.

## Naming reference

The repository and module are named `postgres-sync-go` for searchability and clarity. The command is named `postgres-sync`; the public Go package is named `pgsync` because Go package identifiers cannot contain hyphens and `pgsync.New(...)` keeps embedded usage readable.

| Surface | Name |
| --- | --- |
| GitHub repository | `github.com/pbrazdil/postgres-sync-go` |
| Go module | `github.com/pbrazdil/postgres-sync-go` |
| Public package | `github.com/pbrazdil/postgres-sync-go/pkg/pgsync` |
| Binary | `postgres-sync` |
| Docker image | `postgres-sync-go:local` |
| Metrics prefix | `postgres_sync_go` |

## License and attribution

`postgres-sync-go` is licensed under the Apache License 2.0. See `LICENSE` and `NOTICE`.

An engineering license and attribution audit is recorded in `docs/legal/attribution-audit.md`.

`postgres-sync-go` is an independent project. ElectricSQL and Electric are trademarks or names of their respective owners; this project describes compatibility with the open-source Electric Shape HTTP surface and is not an official Electric project.
