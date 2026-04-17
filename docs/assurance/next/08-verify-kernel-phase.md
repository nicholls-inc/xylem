# 08: `verify-kernel` Workflow Phase

**Horizon:** Next (4–8 weeks)
**Status:** Not started
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

## References

- Crosscheck plugin: `~/.claude/plugins/cache/nicholls/crosscheck/2.1.0/`
- MCP tools: `mcp__plugin_crosscheck_dafny__dafny_verify`, `dafny_compile`, `dafny_cleanup`
- Dafny Go compilation: https://dafny.org/latest/Compilation/Go
- `docs/assurance/next/06-queue-dafny-kernel.md` (the dependency)
