# ADR-004: Place runtime files under `.xylem/state/`

**Status:** Accepted

## Context

`xylem` now distinguishes between:

- **Control plane** — tracked repo assets such as `.xylem.yml`, workflow YAML, prompt templates, and `HARNESS.md`
- **Runtime state** — mutable operational files such as the queue, audit log, PID files, phase outputs, and other daemon artifacts

Earlier versions still kept some mutable files at the flat `.xylem/` root, especially:

- `queue.jsonl`
- `audit.jsonl`
- `daemon.pid`

That mixed tracked config and live daemon state in the same directory and made the productize spec's control-plane/runtime split incomplete.

## Decision

Canonical runtime locations live under `.xylem/state/`.

For the queue, audit log, and daemon PID file:

- `queue.jsonl` lives at `.xylem/state/queue.jsonl`
- `audit.jsonl` lives at `.xylem/state/audit.jsonl`
- `daemon.pid` lives at `.xylem/state/daemon.pid`

Daemon startup performs a one-release compatibility migration:

1. If only the legacy flat file exists, migrate it into `.xylem/state/`
2. Leave a zero-byte `<name>.migrated` breadcrumb at the old location
3. If both legacy and runtime locations exist, fail loudly instead of guessing

Queue and audit files move with `os.Rename`; their `.lock` sidecars move with them.
`daemon.pid` is rewritten at the new path and the legacy file is removed because PID files are process-owned lock files, not regular data blobs.

## Consequences

- Runtime files are grouped under a single rebuildable subtree
- The `.xylem/` root better reflects tracked control-plane assets
- Existing checkouts upgrade in place during daemon startup without silently forking queue or audit state
- Operators get a visible breadcrumb when a migration has occurred
