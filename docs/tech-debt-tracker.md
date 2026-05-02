# Technical Debt Tracker

This tracker is for debt that matters to replacement readiness. Keep entries actionable and delete them when done.

| ID | Area | Debt | Acceptance Signal |
| --- | --- | --- | --- |
| TD-001 | Dependent shapes | Broaden dependent-Shape validation beyond the current nested, negated, and multi-hop Docker differential cases. | Protocol differential matrix includes longer dependency chains, aliases/quoted identifiers, and unsupported-expression fallbacks with matching output or deterministic must-refetch. |
| TD-002 | Shadow clients | Broaden unchanged compatible TypeScript client shadow runs beyond the current seeded restart/reconnect/refetch/concurrency matrix. | Long-running client traffic diff shows no correctness differences for representative production scenarios. |
| TD-003 | Telemetry | Turn the expanded runtime metrics into operator-facing dashboards and structured event guidance. | Operators can diagnose startup, replication lag, invalidations, overload, storage growth, and reconnects from documented dashboards/log fields. |
| TD-004 | WAL/storage operations | Extend current slot-loss, compaction, and WAL-retention gauges into long-running soak validation. | Long-run lifecycle test demonstrates bounded storage growth and safe recovery after restart. |
| TD-005 | Protocol matrix | Expand cache/header, replica, column projection, deletion, and handle-rotation scenarios. | Docker differential matrix covers the broader matrix and remains green. |
| TD-006 | Architecture linting | Add mechanical checks for package boundaries if imports start drifting. | Harness fails with a clear remediation message when disallowed internal dependencies are introduced. |
