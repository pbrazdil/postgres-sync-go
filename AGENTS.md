# PulseSync Agent Map

This file is the short map for agents working in this repository. Keep durable knowledge in `docs/` and executable harnesses in `scripts/` or `test/e2e/`.

## First Reads

- `README.md`: current product status, usage, requirements, and known parity gaps.
- `ARCHITECTURE.md`: package codemap, core flows, boundaries, and invariants.
- `docs/HARNESS_ENGINEERING.md`: agent workflow, validation loops, and artifact rules.
- `docs/QUALITY.md`: quality gates by change type and current risk map.
- `docs/tech-debt-tracker.md`: prioritized cleanup and parity backlog.
- `test/e2e/README.md`: protocol comparison harness.

## Hard Boundaries

- Do not edit ignored/generated assets unless the user explicitly asks.
- Keep the public Go API in `pkg/pulsesync` stable unless the user explicitly asks for an API change.
- Keep Electric-compatible HTTP behavior in `internal/protocol` and `internal/httpapi`; do not leak HTTP details into `internal/pg`.
- Keep Postgres/runtime concerns in `internal/pg`; do not let it depend on HTTP packages.
- Keep shape identity, offsets, materialized state, and diff behavior in `internal/shapes`.
- Persist only through `internal/storage.Store`; do not write ad hoc state files from runtime or protocol code.

## Default Validation

Run the local harness before handing back code whenever practical:

```bash
./scripts/harness-check.sh
```

For protocol, replication, storage, or Docker changes, also run the matching heavier gate:

```bash
./scripts/harness-check.sh --docker-e2e
./scripts/harness-check.sh --lifecycle
```

Use targeted E2E scenarios while iterating, then run the full matrix when changing protocol semantics:

```bash
./test/e2e/compare-docker.sh subquery_move_in_live_replay subquery_move_out_live_replay
./test/e2e/compare-docker.sh
```

## Working Style

- Inspect the code and docs before changing behavior.
- Prefer small, explicit contracts over inferred behavior.
- If correctness cannot be proven for live replication, invalidate or force refetch rather than serving stale data.
- Update docs when behavior, workflow, or known limitations change.
- Keep tests real; do not add gated or skipped unit tests just because they need effort or external services.
- Use `apply_patch` for manual edits.

## Artifact Rules

- E2E artifacts belong under `test/e2e/_artifacts/` and stay untracked.
- Runtime scratch files belong under `test/e2e/_runtime/` or `.pulsesync/` and stay untracked.
- If a mismatch happens, preserve the artifact path in the final response so the next agent can continue from raw evidence.
