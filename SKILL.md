---
name: postgres-sync-go
description: Use when Codex needs to help a user install, configure, run, embed, operate, query, or troubleshoot postgres-sync-go, a Go Electric-compatible Postgres sync service. Covers standalone binary usage, Docker usage, embedded Go usage, SYNC_* environment variables, /v1/shape and /v1/health requests, storage modes, logical replication requirements, client compatibility, and operational diagnostics. Do not use this skill for developing the postgres-sync-go repository itself unless the task is specifically about documenting user-facing usage.
---

# postgres-sync-go Usage

postgres-sync-go is a Go implementation of an Electric-compatible sync-service HTTP surface for Postgres. It can run as a standalone binary, as a Docker container, or embedded in another Go process.

Prefer user-facing help: installation, configuration, requests, deployment shape, diagnostics, and compatibility caveats. Avoid contributor workflow unless the user explicitly asks to modify the postgres-sync-go source repository.

## Core Requirements

- Use Postgres with logical replication enabled.
- Set `wal_level=logical`, `max_replication_slots > 0`, and `max_wal_senders > 0`.
- Use a direct Postgres connection for `DATABASE_URL`; do not point replication at a transaction-pooled proxy.
- Use a database role that can connect, query target tables, create/use logical replication slots, and create/reconcile the postgres-sync-go publication.
- Use `SYNC_SECRET` for `/v1/shape` auth unless intentionally running with `SYNC_INSECURE=true`.

## Configuration

Use environment variables for standalone and container usage:

```bash
DATABASE_URL='postgres://postgres:postgres@localhost:5432/app?sslmode=disable'
SYNC_POOLED_DATABASE_URL="$DATABASE_URL"
SYNC_SECRET='dev-secret'
SYNC_PORT=3000
SYNC_REPLICATION_STREAM_ID=default
SYNC_STORAGE_MODE=memory
```

Important variables:

- `DATABASE_URL`: required direct Postgres URL used for queries and replication.
- `SYNC_POOLED_DATABASE_URL`: optional query URL; defaults to `DATABASE_URL`.
- `SYNC_SECRET`: shared secret for shape requests.
- `SYNC_INSECURE`: bypasses secret checks when `true`; local development only.
- `SYNC_PORT`: HTTP listen port, default `3000`.
- `SYNC_REPLICATION_STREAM_ID`: publication/slot identity namespace.
- `SYNC_STORAGE_MODE`: `memory` or `disk`.
- `SYNC_STORAGE_DIR`: directory for disk mode metadata and chunks.
- `SYNC_FEATURE_FLAGS`: comma-separated flags such as `allow_subqueries,tagged_subqueries`.
- `SYNC_MAX_CONCURRENT_REQUESTS`: JSON limits such as `{"initial":300,"existing":10000}`.
- `SYNC_LONG_POLL_TIMEOUT_MS`: long-poll wait duration.
- `SYNC_SSE_TIMEOUT_MS`: SSE timeout/cache-related duration.
- `SYNC_ALLOW_SHAPE_DELETION`: enables `DELETE /v1/shape` when `true`.

## Run Standalone

The standalone binary is named `postgres-sync`.

```bash
go run github.com/pbrazdil/postgres-sync-go/cmd/postgres-sync@latest
```

For a locally checked-out source tree:

```bash
go run ./cmd/postgres-sync
```

Check health:

```bash
curl http://localhost:3000/v1/health
```

Health states:

- `starting`: process is booting.
- `waiting`: HTTP is up, but replication is not active.
- `active`: query and replication runtime are active.

`/v1/health` returns `200` when active and `202` while starting or waiting.

## Run With Docker

Build and run the local Compose stack:

```bash
docker compose up --build
```

Default local Compose ports:

- postgres-sync-go: `43100`
- Postgres: `45432`

The bundled local Compose database is named `postgres_sync_go`.

Example request:

```bash
curl 'http://127.0.0.1:43100/v1/shape?table=items&offset=-1&secret=dev-secret'
```

## Embed In Go

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

- `pgsync.New(Config) (*Engine, error)`
- `(*Engine).Start(context.Context) error`
- `(*Engine).Handler() http.Handler`
- `(*Engine).Status() pgsync.Status`
- `(*Engine).Close(context.Context) error`

## HTTP Requests

Initial snapshot:

```bash
curl 'http://localhost:3000/v1/shape?table=items&offset=-1&secret=dev-secret'
```

Continue with a handle and offset:

```bash
curl 'http://localhost:3000/v1/shape?table=items&handle=<handle>&offset=<offset>&secret=dev-secret'
```

Start at the current continuation point:

```bash
curl 'http://localhost:3000/v1/shape?table=items&offset=now&secret=dev-secret'
```

Live long-poll:

```bash
curl 'http://localhost:3000/v1/shape?table=items&handle=<handle>&offset=<offset>&live=true&secret=dev-secret'
```

Live SSE:

```bash
curl -N 'http://localhost:3000/v1/shape?table=items&handle=<handle>&offset=<offset>&live=true&live_sse=true&secret=dev-secret'
```

Common shape parameters:

- `table`: table name, optionally schema-qualified.
- `offset`: `-1`, `now`, or a continuation offset.
- `handle`: shape handle returned by `electric-handle`.
- `where`: SQL predicate for the shape.
- `params`: positional predicate params.
- `columns`: comma-separated projection; include primary key columns.
- `replica`: `default` or `full`.
- `log`: `full` or `changes_only`.
- `live`: enable long-poll/live behavior.
- `live_sse`: enable SSE; requires `live=true`.
- `subset__*`: subset snapshot parameters.

Read continuation headers from responses:

- `electric-handle`: shape identity.
- `electric-offset`: continuation offset.

## Storage Modes

Use `SYNC_STORAGE_MODE=memory` for disposable development and tests:

- state is ephemeral
- replication slot is temporary
- restart continuity is not preserved

Use `SYNC_STORAGE_MODE=disk` for restart continuity:

- shape metadata and materialized state are persisted
- change log chunks are persisted
- replication uses a persistent slot derived from `SYNC_REPLICATION_STREAM_ID`
- invalid or incompatible persisted state forces client refetch rather than serving uncertain data

Set `SYNC_STORAGE_DIR` to a durable mounted directory in production-like deployments.

## Compatibility And Limits

- postgres-sync-go targets the open-source Electric-compatible sync-service HTTP surface.
- Existing HTTP clients should use `/v1/shape` unchanged for the supported matrix.
- Unsupported or unsafe live cases should invalidate/force refetch instead of silently serving stale data.
- `TRUNCATE` invalidates affected shapes rather than replaying row-level truncate semantics.
- Complex nested, negated, or multi-hop dependent subquery behavior may require broader validation before production cutover.
- Metrics are currently minimal; treat production observability as an area to verify before cutover.

## Troubleshooting

If health stays `waiting`:

- Verify Postgres is reachable from the postgres-sync-go process.
- Verify logical replication settings and role privileges.
- Check publication and replication slot permissions.
- Check whether another process is using the same stream ID/slot.

If `/v1/shape` returns `401`:

- Pass `secret=<SYNC_SECRET>` or `api_secret=<SYNC_SECRET>`.
- For local-only testing, set `SYNC_INSECURE=true`.
- Do not expose insecure mode publicly.

If clients receive `409 must-refetch`:

- Treat it as intentional safety behavior.
- Restart the shape from `offset=-1` or `offset=now` depending on the client’s recovery model.
- Check for shape deletion, truncation, incompatible persisted state, or unsupported live dependency semantics.

If disk mode does not preserve continuity:

- Confirm `SYNC_STORAGE_DIR` is durable and writable.
- Confirm the same `SYNC_REPLICATION_STREAM_ID` is used after restart.
- Confirm the same database identity and compatible replication slot still exist.

When reporting issues, ask users to include:

- postgres-sync-go version or commit.
- Postgres version and replication settings.
- Storage mode and relevant `SYNC_*` config with secrets redacted.
- Exact `/v1/shape` request, response status, and relevant headers.
- Logs around startup, replication reconnect, invalidation, or overload.
