# Technical Debt Tracker

This tracker is for debt that matters to replacement readiness. Keep entries actionable and delete them when done.

| ID | Area | Debt | Acceptance Signal |
| --- | --- | --- | --- |
| TD-001 | Dependent shapes | Expand dependent-Shape validation beyond unit-covered nested, negated, and multi-hop dependency plans into the Docker differential matrix. | Protocol differential matrix includes representative nested/negated/multi-hop cases with matching output or deterministic must-refetch. |
| TD-002 | Shadow clients | Broaden unchanged compatible TypeScript client shadow runs beyond the current seeded matrix. | Long-running client traffic diff shows no correctness differences for representative production scenarios. |
| TD-003 | Telemetry | Replace static metrics with useful runtime counters, gauges, and structured logs. | Operators can diagnose startup, replication lag, invalidations, overload, storage growth, and reconnects from logs/metrics. |
| TD-004 | WAL/storage operations | Document and validate long-running disk mode behavior, WAL retention, slot continuity, and cleanup. | Long-run lifecycle test demonstrates bounded storage growth and safe recovery after restart. |
| TD-005 | Protocol matrix | Expand cache/header, replica, column projection, deletion, and handle-rotation scenarios. | Docker differential matrix covers the broader matrix and remains green. |
| TD-006 | Architecture linting | Add mechanical checks for package boundaries if imports start drifting. | Harness fails with a clear remediation message when disallowed internal dependencies are introduced. |
