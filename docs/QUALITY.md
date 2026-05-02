# postgres-sync-go Quality Map

This file is the current engineering quality snapshot for agents and humans. Update it when a meaningful gap is closed or a new risk is discovered.

## Current Grades

| Area | Grade | Current Signal | Main Risk |
| --- | --- | --- | --- |
| Public API and config | B+ | Unit tests and env parsing coverage | More `SYNC_*` flags may still be needed |
| HTTP protocol shell | B+ | Compat tests plus Docker diff matrix | Long-tail cache/header combinations |
| Live fanout | B+ | PK-targeted refresh plus nested, negated, and multi-hop dependent tracking tests pass | Long-tail SQL expressions and broader differential coverage |
| Disk continuity | B+ | Docker lifecycle validator covers restart, slot loss, corruption, reconnect, schema invalidation, compaction, and metrics cases | Storage growth and WAL retention under long runs |
| E2E harness | A- | Protocol differential matrix, lifecycle checks, and shadow-client validation are executable and artifacted | Matrix is still representative, not exhaustive |
| Telemetry | B- | Metrics cover replication lag, reconnects, invalidations, storage, checkpoint state, admission, and overloads | Operator dashboards and structured event-field polish |
| Packaging | B- | Dockerfile and Compose exist | Release packaging and deployment docs need hardening |

## Required Gates By Change Type

| Change Type | Minimum Gate |
| --- | --- |
| Any Go code | `./scripts/harness-check.sh` |
| Config/env parsing | `go test ./internal/config ./pkg/pgsync` plus harness check |
| HTTP protocol or request parsing | `go test ./internal/protocol ./test/compat` plus targeted Docker compare |
| Replication/live fanout | `go test ./internal/pg ./internal/shapes` plus targeted Docker compare |
| Storage/disk continuity | `go test ./internal/storage ./internal/shapes` plus `./scripts/harness-check.sh --lifecycle` |
| E2E harness changes | `bash -n ...` via harness check plus at least one targeted scenario |
| Client compatibility | `./scripts/harness-check.sh --shadow-client` |
| Docs-only changes | `./scripts/harness-check.sh --docs-only` |

## Current Full Validation Command

```bash
./scripts/harness-check.sh --all
```

This runs local checks, the Docker protocol comparison matrix, the postgres-sync-go lifecycle validator, and shadow-client validation.

## Signoff Bar For Replacement

- Supported Docker differential matrix passes.
- Disk lifecycle validation passes.
- Representative unchanged compatible TypeScript clients run against postgres-sync-go in shadow mode with no correctness diffs for the supported scenario matrix.
- Known unsupported dependent-subquery cases force refetch, wildcard invalidation, or documented conservative behavior.
- Operational concerns have evidence: metrics/log usefulness, WAL retention behavior, storage growth behavior, and recovery drills.
