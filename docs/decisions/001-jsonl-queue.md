# ADR-001: JSONL Queue Storage

**Status:** Accepted

## Context

xylem needs a persistent, crash-safe work queue to hold `Vessel` records across daemon restarts. The queue must support concurrent readers (e.g., status checks) and exclusive writers (e.g., enqueue, dequeue, update). Candidates considered:

- **SQLite / embedded relational DB** — full ACID guarantees, rich query support, binary format
- **JSONL file with file-level locking** — simple text format, human-readable, no embedded DB dependency

The workload is low-throughput: tens to low hundreds of vessels, infrequent writes, occasional compaction.

## Decision

Vessels are persisted in a single JSONL file at `<state_dir>/queue.jsonl`. File-level locking uses `gofrs/flock` with a `.lock` sidecar file. Two lock helpers provide appropriate access levels:

- `withLock` — exclusive write lock for all mutation operations
- `withRLock` — shared read lock for read-only operations (e.g., `List`)

Every write operation follows a full-rewrite pattern: `readAllVessels()` → modify in-memory slice → `writeAllVessels()` via `os.WriteFile`. There is no append-only mode; each write replaces the entire file.

## Rationale

- **Simplest correct solution** for the expected workload. No daemon process, no query planner, no schema migrations.
- **Human-readable** — operators can inspect and manually repair the queue with any text editor.
- **No embedded DB dependency** — avoids cgo (SQLite) or a Go-native DB binary embedded in the CLI.
- **File-level locking** prevents corruption without requiring a long-running server process. `gofrs/flock` is cross-platform and supports both exclusive and shared locking.
- **`os.WriteFile` atomicity** — on most filesystems, overwriting a file is atomic at the OS level for small files, which is sufficient for this use case.

## Consequences

- **Positive:** Simple to reason about, easy to inspect, zero external dependencies beyond `gofrs/flock`.
- **Positive:** The full-rewrite pattern means any operation that reads-then-writes sees a consistent snapshot under the exclusive lock.
- **Negative:** Full rewrite on every write does not scale to thousands of vessels or high write frequency. A high-throughput workload would require replacement with an append-only log or embedded DB.
- **Negative:** JSONL is not indexed; `List` and lookup operations are O(n) scans.
- **Negative:** Compaction (`Compact`) must also take an exclusive lock and rewrite the file, which blocks all other writers during cleanup.
