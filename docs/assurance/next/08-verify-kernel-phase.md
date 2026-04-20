# 08: `verify-kernel` Workflow Phase

**Horizon:** Next (4–8 weeks)
**Status:** Complete (2026-04-20)
**Estimated cost:** 2 days
**Depends on:** #06 (something to verify)
**Unblocks:** #09 (retry-DAG kernel integrates cleanly into an existing gate)

## Context

Once the Dafny kernel pattern is established (#06), every subsequent PR that touches `.dfy` files needs a gate that runs `dafny_verify` and fails on verification failure. This prevents accidental regression of verified properties — without it, a well-intentioned change to a `.dfy` spec could introduce a verification gap that ships silently, defeating the whole point of the kernel.

This is a thin phase. It only runs when a PR touches `.dfy` files. It invokes the Crosscheck plugin's `dafny_verify` MCP tool (Docker-isolated, 120s timeout per the plugin docs) and fails the gate if verification fails.

## Scope

**In scope:**
- New workflow phase `verify-kernel` defined in `.xylem/workflows/phases/verify-kernel.md`.
- Integration into `fix-bug`, `implement-feature`, `implement-harness` — placed **after** `implement` and **before** `test_critic` (or `verify` if no `test_critic`).
- The phase detects `.dfy` file changes vs `origin/main` and runs `dafny_verify` on each.
- If `dafny_verify` fails on any touched `.dfy`, the gate fails; the vessel does not proceed.

**Out of scope:**
- Creating new kernels (that is #06 and #09).
- Running Dafny on a whole-repo basis — only on changed files, because full-repo Dafny verification is too slow for a per-PR gate.
- Dafny-to-Go extraction — that remains manual per #06's pipeline.

## Deliverables

1. `.xylem/prompts/verify-kernel/verify.md` (prompt if needed — may be a command gate instead).
2. Phase block in three workflow YAMLs.
3. Gate logic — likely a command gate invoking a Go helper or shell script that detects changed `.dfy` files and calls the Dafny verifier.
4. Documentation in `docs/workflows.md`.

## Acceptance criteria

- Intentionally introduce a broken `.dfy` spec (e.g. a false post-condition). Gate fails.
- Restore the spec. Gate passes.
- On a PR that does not touch `.dfy` files, the gate exits cleanly (no-op) in under 1 second.
- The gate respects the plugin's 120s timeout and reports a meaningful failure message if it exceeds.

## Files to touch

- **New:** `.xylem/prompts/verify-kernel/verify.md` (or a shell script under `scripts/`)
- **Modified:** three workflow YAMLs (**PROTECTED**)
- **Modified:** `docs/workflows.md`

## Risks

- **Dafny toolchain in CI may be flaky or unavailable.** The Crosscheck plugin uses Docker-isolated Dafny; requires Docker in the CI environment. If unavailable, fall back to pre-commit-only enforcement (still valuable, just less thorough).
- **False positives from brittle specs.** A tight spec may fail verification after an unrelated refactor. Mitigate with stable module boundaries and small kernels per #06 kill criterion.
- **Latency.** 120s per `.dfy` file. If many files are touched, gate becomes slow. Mitigate by keeping kernels small and few.

## Kill criteria

- If Dafny fails intermittently in CI more than 10% of runs after 2 weeks of operation, fall back to pre-commit-only enforcement and document the CI gap.
- If any kernel requires more than 120s to verify, split it into smaller specs.

## Execution notes

**Protected surfaces:** Workflow YAML amendments require governance note.

**Same-LLM review concern:** Gate logic is mechanical. `pr-self-review` is sufficient.

## Progress

**2026-04-20 — Complete.**

- `scripts/verify-kernels.sh` — shell gate: 3-dot diff for changed `.dfy` files, Docker Dafny verify per file, 130s timeout, exits 0 with warning when Docker/image absent.
- `.xylem/workflows/fix-bug.yaml` — `verify_kernel` command phase inserted after `implement`, before `verify`. Governance amendment 2026-04-20.
- `.xylem/workflows/implement-feature.yaml` — same.
- `.xylem/workflows/implement-harness.yaml` — same (placed after implement gate, before verify and test_critic).
- `cli/internal/profiles/core/workflows/fix-bug.yaml` — embedded profile source updated to match. Required to prevent the #651 silent-revert pattern: without this, daemon auto-upgrade overwrites `.xylem/workflows/fix-bug.yaml` with the stale embedded copy on restart.
- `cli/internal/profiles/core/workflows/implement-feature.yaml` — same.
- `cli/internal/profiles/self-hosting-xylem/workflows/implement-harness.yaml` — same.
- `TestVerifyKernelPhaseEmbeddedInAllDeliveryWorkflows` in `cli/internal/profiles/implement_harness_guard_test.go` — regression guard: asserts that `verify_kernel` exists in all three embedded workflow copies and that phase order (implement → verify_kernel → verify) is preserved. Prevents future drift between `.xylem/` and `cli/internal/profiles/`.
- `cli/internal/profiles/profiles_test.go` — phase count assertions updated from 5 → 6 for fix-bug and implement-feature smoke tests.
- `docs/workflows.md` — `verify-kernel` section added to workflow reference.

**Docker image finding:** `crosscheck-dafny:latest` is absent from the daemon environment as of 2026-04-20. The xylem workflow gate soft-falls back to exit 0 with a warning on all current machines. There is no pre-commit hook for `verify-kernels.sh` (excluded intentionally — `git fetch` + Docker on every commit is too slow).

**CI enforcement (2026-04-20):** `verify-kernels` job added to `.github/workflows/ci.yml`. Uses `dafny-lang/setup-dafny@v1` at Dafny 4.11.0 (matching the version recorded in `state_machine.dfy`) to install Dafny natively — no Docker required. Hard failure on verification error. Triggered on any PR or push to main that touches `**/*.dfy`. This is the active enforcement gate for code committed outside xylem workflows.

**Deliverable delta vs spec:** The spec listed `.xylem/prompts/verify-kernel/verify.md` as a possible deliverable. The implementation is a `type: command` phase backed by `scripts/verify-kernels.sh` instead — a prompt file is unnecessary because the gate is fully deterministic. No LLM session is needed. The spec did not anticipate the embedded profile sync or regression guard, which were added to prevent the #651 pattern discovered after the spec was written.

## References

- `scripts/verify-kernels.sh` — the gate script
- Crosscheck plugin: `~/.claude/plugins/cache/nicholls/crosscheck/2.1.0/`
- MCP tools: `mcp__plugin_crosscheck_dafny__dafny_verify`, `dafny_compile`, `dafny_cleanup`
- Dafny Go compilation: https://dafny.org/latest/Compilation/Go
- `docs/assurance/next/06-queue-dafny-kernel.md` (the dependency)
- `docs/workflows.md` §verify-kernel — documentation
