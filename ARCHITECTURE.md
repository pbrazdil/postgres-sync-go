# postgres-sync-go Architecture

postgres-sync-go is a Go implementation of the Electric sync-service surface. It accepts Electric-compatible shape requests, snapshots rows from Postgres, follows logical replication, and serves deterministic shape continuations over HTTP, long-poll, or SSE.

This file is a codemap. It should help answer "where does this behavior live?" without duplicating every implementation detail.

## Bird's Eye View

postgres-sync-go has one core engine and two ways to run it:

- embedded inside another Go process through `pkg/pgsync`;
- as the standalone `postgres-sync` binary in `cmd/postgres-sync`.

The engine is built around four durable concepts:

- Config: `SYNC_*` env loading plus Go-native configuration.
- Runtime: Postgres query pool, publication/slot lifecycle, and logical replication.
- Shapes: canonical definitions, handles, materialized state, offsets, and change logs.
- Protocol: Electric-compatible HTTP request/response behavior.

## Codemap

Start here when looking for code:

- `cmd/postgres-sync`: process entrypoint. This should stay boring: load config, create an engine, start HTTP, handle shutdown.
- `pkg/pgsync`: public API and internal wiring. `Engine`, `Config`, and exported status/types live here.
- `internal/config`: defaults, `SYNC_*` env parsing, validation, storage mode, and feature flags.
- `internal/httpapi`: route selection, CORS, health status, metrics route, and common HTTP headers that are not shape-specific.
- `internal/protocol`: `/v1/shape` behavior: auth, request parsing, validation, cache headers, overload admission, long-poll, SSE, and error mapping.
- `internal/pg`: Postgres-facing runtime: pools, snapshots, schema inspection, publication/slot reconciliation, pgoutput decoding, relation metadata cache, and live fanout.
- `internal/shapes`: shape identity and state: canonical definitions, handles, offsets, materialized baselines, diffing, subscribers, delete/invalidation semantics, and dependent-shape tags.
- `internal/storage`: persistence boundary. Memory and disk modes implement the same `Store` contract.
- `internal/telemetry`: metrics provider surface.
- `internal/sqlinspect`: shared SQL-clause inspection helpers used where full SQL parsing would be heavier than the current need.
- `test/compat`: stable black-box HTTP/router contracts.
- `test/e2e`: seeded Postgres workloads, manual curls, side-by-side scripts, and protocol differential comparison.

If you are changing a user-visible shape response, expect to touch `internal/protocol`, `internal/shapes`, tests, and probably `test/e2e`. If you are changing live update correctness, start in `internal/pg` and `internal/shapes`.

## Main Flows

Startup:

```text
cmd/postgres-sync or embedding app
  -> pkg/pgsync.New
  -> storage selection and shape hydration
  -> pg.Runtime.Start
  -> query pool, publication, slot, replication stream
  -> health moves starting -> waiting -> active
```

Initial snapshot:

```text
client GET /v1/shape?offset=-1
  -> httpapi router
  -> protocol validation/auth/cache setup
  -> pg snapshot query
  -> shapes.UpsertSnapshot
  -> JSON response with electric-handle/electric-offset/schema headers
```

Continuation/live:

```text
Postgres WAL commit
  -> pgoutput decode
  -> ChangeBatch by relation and primary key
  -> PK-targeted refresh for simple root/partition shapes
  -> dependent refresh for supported tagged subquery shapes
  -> shapes diff and append
  -> waiting long-poll/SSE requests wake and deliver messages
```

Disk continuity:

```text
shape state + materialized rows + checkpoint
  -> storage.Store
  -> restart hydration
  -> persistent slot resume in disk mode
  -> must-refetch if checkpoint, slot, identity, or per-shape state is unsafe
```

## Architectural Invariants

- Only postgres-sync-go packages participate in the runtime package graph.
- Public embedding API lives in `pkg/pgsync`; internals should not leak through it casually.
- `cmd/postgres-sync` stays thin. Process policy belongs there; engine behavior does not.
- `internal/pg` must not import HTTP/router/protocol delivery packages.
- `internal/storage` must not depend on runtime, protocol, or shape manager implementation details.
- Shape handles and offsets are owned by `internal/shapes`; protocol code should consume them, not invent them.
- Live correctness beats availability. If postgres-sync-go cannot prove a live projection is correct, invalidate or force refetch.
- Memory mode is disposable. Disk mode is the only mode expected to preserve continuity across restarts.
- Normalization in `test/e2e/cmd/syncdiff` may hide unstable IDs, but must preserve protocol semantics.

## Boundaries

The important boundary is between "how we know the database changed" and "how clients observe shape changes":

- `internal/pg` knows Postgres relations, primary keys, LSNs, XIDs, and snapshots.
- `internal/shapes` knows what a shape currently contains and how that state changes.
- `internal/protocol` knows how compatible clients ask for and receive shape data.

Keep those boundaries explicit. A tempting shortcut is to make replication code emit HTTP-shaped responses directly; avoid that. Another tempting shortcut is to let protocol code query Postgres directly for special cases; avoid that too.

## Cross-Cutting Concerns

Compatibility:

- Electric-compatible behavior is proven through differential tests.
- Prefer adding a differential E2E scenario before changing protocol semantics.
- Document unsupported behavior in `README.md` and `docs/tech-debt-tracker.md`.

Readiness and recovery:

- Health should reflect runtime state, especially replication disconnects.
- Disk recovery must be conservative when identity/checkpoint/state is incompatible.
- Truncate and unsupported dependency cases should force refetch rather than replay guessed events.

Feature flags:

- Electric-compatible feature flags are parsed in config and enforced at protocol boundaries.
- Subquery behavior is intentionally feature-flagged and conservative.

Testing:

- Unit tests cover local contracts.
- `test/compat` freezes high-signal HTTP behavior.
- `test/e2e/compare-docker.sh` is the current protocol parity harness.
- `test/e2e/validate-postgres-sync-go-docker.sh` covers postgres-sync-go lifecycle behavior that differential comparison does not.

## Where To Change Things

- Add or validate an env var: `internal/config`, `pkg/pgsync/config.go`, config tests, README.
- Add a `/v1/shape` query parameter: `internal/protocol/request.go`, validation tests, E2E curls/comparison.
- Change response headers or cache behavior: `internal/protocol/response.go`, protocol tests, compat tests, E2E.
- Change snapshot SQL: `internal/pg/runtime.go`, targeted unit tests, E2E snapshot scenario.
- Change live replication fanout: `internal/pg/replication.go`, `internal/pg/change_batch.go`, `internal/shapes/manager.go`, targeted Docker comparison.
- Change shape diffing or offsets: `internal/shapes`, protocol tests, E2E continuation scenarios.
- Change persistence: `internal/storage`, shape persistence tests, lifecycle validator.
- Add a protocol parity scenario: `test/e2e/lib.sh`, mutation SQL under `test/e2e/sql`, scenario lists in compare scripts, E2E README.

## Maintenance

Keep this file short and stable. It should describe the physical architecture and invariants, not every implementation detail. If a detail changes often, prefer code comments, focused tests, or a more specific doc under `docs/`.
