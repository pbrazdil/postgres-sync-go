# Harness Engineering

This repo applies the ideas from OpenAI's "Harness engineering" article to PulseSync: make the workspace legible to agents, keep repository knowledge versioned, and turn validation into executable feedback loops instead of tribal memory.

Source: https://openai.com/index/harness-engineering/

The root `ARCHITECTURE.md` follows Matklad's codemap guidance: it is a short physical map of the project, names important modules/types, and calls out invariants that are otherwise hard to infer from code.

Source: https://matklad.github.io/2021/02/06/ARCHITECTURE.md.html

## What It Means Here

- `AGENTS.md` is a map, not a manual.
- `ARCHITECTURE.md` is the stable codemap next to `README.md`.
- `docs/` is the source of truth for architecture, quality gates, and known debt.
- `scripts/harness-check.sh` is the default local validation loop.
- `test/e2e/` is the protocol comparison harness.
- Artifacts are preserved so a future agent can debug from evidence, not guesses.

## Agent Loop

1. Read `AGENTS.md`, then follow only the docs relevant to the task.
2. Inspect implementation and tests before changing behavior.
3. Make the smallest coherent code/doc/harness change that advances the requested outcome.
4. Run `./scripts/harness-check.sh`.
5. If protocol or replication behavior changed, run targeted `./test/e2e/compare-docker.sh <scenario...>`.
6. If storage/runtime lifecycle changed, run `./scripts/harness-check.sh --lifecycle`.
7. Record remaining caveats in `README.md`, `docs/QUALITY.md`, or `docs/tech-debt-tracker.md`.

## Validation Commands

Fast local gate:

```bash
./scripts/harness-check.sh
```

Full supported protocol comparison:

```bash
./scripts/harness-check.sh --docker-e2e
```

PulseSync lifecycle validation:

```bash
./scripts/harness-check.sh --lifecycle
```

Everything available from the harness:

```bash
./scripts/harness-check.sh --all
```

## Harness Design Rules

- Prefer deterministic one-by-one comparisons for diffing. Side-by-side runs are for manual inspection.
- Normalize unstable values only when they are semantically irrelevant: handles, etags, cursors, dynamic LSNs, txids, and instance-local tag values.
- Preserve semantic structure. For example, dependent-shape tags may be normalized, but their presence and count must remain visible.
- Keep Docker ports in high one-off ranges so local sync-service/Postgres development stacks are not disturbed.
- Keep default checks useful without Docker. Docker comparison gates are explicit flags because they are slower and need extra services.
- When a test fails, prefer improving the harness or implementation over weakening the assertion.

## Artifact Conventions

- Default artifact root: `test/e2e/_artifacts/<timestamp>/`.
- Include raw headers, bodies, normalized outputs, service logs, and Postgres debug snapshots when available.
- Final task summaries should include artifact paths for any E2E or lifecycle run.
- Do not commit generated artifacts.

## Entropy Control

When a recurring mistake appears:

- First, document the rule in the smallest relevant doc.
- If docs are not enough, add a harness check or focused unit test.
- If a pattern is duplicated, centralize it in a shared helper.
- If compatibility is ambiguous, add a protocol differential scenario before expanding implementation.
