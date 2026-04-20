# PulseSync Quality Map

This file is the current engineering quality snapshot for agents and humans. Update it when a meaningful gap is closed or a new risk is discovered.

## Current Grades

| Area | Grade | Current Signal | Main Risk |
| --- | --- | --- | --- |
| Public API and config | B+ | Unit tests and env parsing coverage | More `SYNC_*` flags may still be needed |
| HTTP protocol shell | B+ | Compat tests plus Docker diff matrix | Long-tail cache/header combinations |
| Live fanout | B | PK-targeted refresh and current dependent replay scenarios pass | Complex nested, negated, or multi-hop dependent subqueries |
| Disk continuity | B | Docker lifecycle validator covers restart/corrupt/reconnect cases | Storage growth and WAL retention under long runs |
| E2E harness | B+ | Protocol differential matrix is executable and artifacted | Matrix is still representative, not exhaustive |
| Telemetry | C | Minimal metrics endpoint exists | Logs/metrics are not rich enough for production debugging |
| Packaging | B- | Dockerfile and Compose exist | Release packaging and deployment docs need hardening |

## Required Gates By Change Type

| Change Type | Minimum Gate |
| --- | --- |
| Any Go code | `./scripts/harness-check.sh` |
| Config/env parsing | `go test ./internal/config ./pkg/pulsesync` plus harness check |
| HTTP protocol or request parsing | `go test ./internal/protocol ./test/compat` plus targeted Docker compare |
| Replication/live fanout | `go test ./internal/pg ./internal/shapes` plus targeted Docker compare |
| Storage/disk continuity | `go test ./internal/storage ./internal/shapes` plus `./scripts/harness-check.sh --lifecycle` |
| E2E harness changes | `bash -n ...` via harness check plus at least one targeted scenario |
| Docs-only changes | `./scripts/harness-check.sh --docs-only` |

## Current Full Validation Command

```bash
./scripts/harness-check.sh --all
```

This runs local checks, the Docker protocol comparison matrix, and the PulseSync lifecycle validator.

## Signoff Bar For Replacement

- Supported Docker differential matrix passes.
- Disk lifecycle validation passes.
- Representative unchanged compatible TypeScript clients run against PulseSync in shadow mode with no correctness diffs.
- Known unsupported dependent-subquery cases force refetch or are documented with conservative behavior.
- Operational concerns have evidence: metrics/log usefulness, WAL retention behavior, storage growth behavior, and recovery drills.
