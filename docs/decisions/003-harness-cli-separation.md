# ADR-003: Harness/CLI Package Separation

**Status:** Accepted

## Context

The `cli/internal/` directory contains two distinct groups of packages that co-exist in the same Go module:

**CLI packages** (the control plane):
`queue`, `runner`, `scanner`, `source`, `workflow`, `gate`, `phase`, `worktree`, `reporter`, `config`, and `cmd/xylem`

**Harness packages** (standalone agent library):
`orchestrator`, `mission`, `memory`, `signal`, `evaluator`, `cost`, `ctxmgr`, `intermediary`, `bootstrap`, `catalog`, `observability`

The harness packages implement multi-agent orchestration primitives (topologies, task decomposition, typed memory, behavioral signals, cost tracking, context management, intent validation, tool catalogs, observability). They were built alongside the CLI but are conceptually independent composable libraries.

The question was whether to couple these groups — for example, having `runner` delegate directly to `orchestrator` for phase execution — or to maintain a hard import boundary between them.

## Decision

Zero cross-imports between the CLI group and the harness group. CLI packages may not import harness packages, and harness packages may not import CLI packages. This boundary is enforced by convention; no automated tooling guard is configured.

## Rationale

- **Separate evolvability** — the harness packages have their own abstractions, interfaces, and test suites. Coupling them to the CLI's `Vessel`, `Workflow`, or `Phase` types would make both groups harder to evolve independently.
- **No accidental coupling** — keeping the boundary explicit prevents the gradual accumulation of import edges that would eventually make it impossible to extract the harness into a separate module or repository.
- **Future migration path** — a future version of `runner` could delegate phase execution to `orchestrator` at the boundary (e.g., via a thin adapter or configuration), rather than requiring a big-bang refactor that interleaves the two call graphs.
- **Composability** — harness packages are designed to be usable independently of xylem's CLI. Importing CLI types would break this property.

## Consequences

- **Positive:** Each group can be understood, tested, and modified in isolation.
- **Positive:** Harness packages remain suitable for extraction into a standalone module if needed.
- **Positive:** Contributors working only on the CLI do not need to understand the harness internals, and vice versa.
- **Negative:** Some primitives (e.g., context handling, error types) may be duplicated across the two groups rather than shared.
- **Negative:** The boundary is enforced only by convention. Without a tooling guard (e.g., `go-import-boss`, `depguard`, or a CI `grep`-based check), a contributor can accidentally violate it without immediate feedback.
- **Negative:** Integrating the harness into the runner in the future will require designing an explicit adapter layer at the boundary rather than direct function calls.
