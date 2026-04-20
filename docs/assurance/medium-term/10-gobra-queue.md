# 10: Gobra Concurrency-Safety Specs for Queue

**Horizon:** Medium-term (2–3 months)
**Status:** Not started
**Estimated cost:** 3–4 weeks
**Depends on:** #06 (Dafny pipeline proven), #09 (retry-DAG verified)
**Unblocks:** nothing — this is the first real concurrency proof; future items depend on its success or failure.

## Context

Dafny cannot model concurrency. Queue is xylem's most concurrency-sensitive pure module: file lock on `state/daemon.pid`, in-memory mutex, JSONL append-and-fsync semantics, atomic rename. A real concurrency-safety proof requires a Go-native verifier. **Gobra** (ETH/Viper) is the only production-ish candidate — still prototype-grade, with the WireGuard case study having to strip concurrency-heavy features to prove memory safety.

Queue is a narrower surface than WireGuard. Lock order is simple: file lock ⟹ mutex. Critical sections are short. The hypothesis worth testing: Gobra can prove lock-order compliance, absence of data races on the in-memory structures, and atomicity of JSONL appends for this specific module.

## Scope

**In scope:**
- `.gobra` specs covering:
  - Lock-order invariant: file lock acquired before mutex; released in reverse.
  - Data-race freedom for all shared mutable fields on the `Queue` struct.
  - Atomicity of every JSONL append operation.
  - **`protectedFieldsEqual` structural correctness (backing I2 — terminal-record immutability):** Gobra proof that the function correctly compares all 19 I2-protected fields, including `*time.Time` (via `timePtrEqual`) and `map[string]string` (via `stringMapEqual`). Deferred here from #06 because Dafny cannot model these Go-native types without conversion shims that defeat extraction.
- CI integration — Gobra runs on every PR touching `cli/internal/queue/*`.

**Out of scope:**
- Runner concurrency. Scanner concurrency. Any other module.
- Proving crash recovery (durability across fsync and reboot) — Gobra does not model crash semantics.

## Deliverables

1. `cli/internal/queue/queue.gobra` (or Gobra annotations embedded in queue.go via comments — follow whichever convention Gobra tooling requires).
2. CI workflow step that invokes Gobra verify.
3. `docs/assurance/medium-term/10-gobra-queue-findings.md` — record of what Gobra proved, what it could not prove, and why (for honest scoping of future items).

## Acceptance criteria

- `gobra verify ./cli/internal/queue/...` returns clean in CI.
- `protectedFieldsEqual` Gobra spec proves all 19 I2-protected fields are compared correctly, with explicit coverage of the `*time.Time` and `map[string]string` delegates.
- Documented findings: every property Gobra could not prove is written down with a reason (typically "Gobra does not model X"), plus a pointer to the property test that compensates.

## Files to touch

- **New/Modified:** `cli/internal/queue/*.gobra` or embedded annotations.
- **Modified:** `.github/workflows/*.yml` (Gobra step).
- **New:** `docs/assurance/medium-term/10-gobra-queue-findings.md`.
- **Read-only:** `docs/invariants/queue.md` (target invariants — I2, I7).
- **Read-only:** `cli/internal/queue/queue.go` (`protectedFieldsEqual` at line 98, `timePtrEqual` at line 124, `stringMapEqual` nearby).

## Risks

- **Gobra is prototype.** Expect tooling rough edges. Follow WireGuard precedent: if a feature of the Go code blocks verification, consider whether the feature can be refactored out or whether Gobra's limitation is acceptable and documented.
- **Mixing Dafny kernels (#06, #09) with Gobra verification in the same package.** Gobra may not understand the Dafny-generated `interface{}` types. Mitigate by keeping Dafny kernels as leaf functions called from (but not structurally embedded in) the queue's concurrent body.
- **CI latency.** Gobra verification is slow. Budget 2–5 minutes per run; if higher, move to a separate CI job that runs only on relevant path changes.

## Kill criteria

- If after 6 weeks no non-toy specs verify, shelve and classify as aspirational.
- If verification requires structural changes to queue.go that break property tests, back out.

## Execution notes

**Same-LLM review concern:** Gobra specs are formal and should be human-reviewed. `pr-self-review` cannot meaningfully audit a Viper-level specification.

**Protected surfaces:** None directly, though updates to `docs/invariants/queue.md` to reference Gobra-verified properties would require governance notes.

## References

- Gobra: https://github.com/viperproject/gobra
- WireGuard case study (the reference for what Gobra can realistically prove).
- `docs/research/literature-review.md` §closing paragraph on spec-completeness.
