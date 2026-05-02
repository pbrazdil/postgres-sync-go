# Changelog

All notable user-facing changes should be recorded here.

## v0.1.0-preview.1 - 2026-05-02

Initial public preview.

- Renamed the project to `postgres-sync-go`.
- Exposed the standalone `postgres-sync` binary.
- Published the embeddable Go API at `github.com/pbrazdil/postgres-sync-go/pkg/pgsync`.
- Added Electric-compatible `/v1/shape`, `/v1/health`, long-poll, SSE, and basic metrics surfaces.
- Added in-memory and disk-backed storage modes.
- Added Postgres logical replication runtime with root-table, partition, and refreshable dependent-shape live fanout.
- Added Docker packaging and local Compose defaults.
- Added differential, lifecycle, and shadow-client validation harnesses.

Known preview limits are documented in `README.md`.
