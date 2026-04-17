You are running the `assess_state` phase of the `lean-squad` tick coordinator.

🔬 **Lean Squad.** A progressive, optimistic application of Lean 4 formal verification to this repo.
Your job in this phase is to pick the **two highest-value next tasks** to dispatch this tick.
Zero formal-verification expertise is assumed — the prompts for each sub-workflow teach the
concepts. Think of this system as a coordinated fleet of agents that collectively build a Lean
model of this codebase over weeks. Build failures mid-proof are progress, not errors: failing
theorems get filed as GitHub issues (real spec/impl bugs), and `sorry`-guarded stubs are valid
interim output.

- Vessel ID: {{.Vessel.ID}}
- Tick ref: {{.Vessel.Ref}}
- Fired at: {{index .Vessel.Meta "schedule.fired_at"}}
- Gate output (opt-in signal): {{.PreviousOutputs.gate}}
- Bootstrap status: {{.PreviousOutputs.bootstrap_if_missing}}
- Merge log: {{.PreviousOutputs.merge_open_prs}}

## The task cluster

These 12 task workflows exist (13 if you count the focus/dispatch entrypoint); you pick any two:

| Task slug | Purpose | Prerequisites |
|---|---|---|
| `orient` | Survey the repo; write/update `formal-verification/{RESEARCH,TARGETS}.md` with 3-5 prioritised targets. | bootstrap done |
| `informal-spec` | Pick one target from TARGETS.md; write `formal-verification/specs/<target>_informal.md` in prose (pre/post-conditions, invariants, edge cases). | one target with status `[ ] pending` |
| `formal-spec` | Translate one informal spec into Lean 4 skeleton (`lean/FVSquad/<Target>.lean`, type defs + theorem decls with `sorry` bodies). | informal spec exists |
| `extract-impl` | Replace `sorry` function bodies with faithful Lean translations of the source code. | formal spec exists |
| `prove` | Try to prove one theorem. On failure, file a `[finding]` issue with a minimal counterexample. | extract-impl done for that target |
| `correspondence` | Maintain `formal-verification/CORRESPONDENCE.md` — source-function → Lean-definition map with divergence notes. | any target with formal spec |
| `critique` | Maintain `formal-verification/CRITIQUE.md` — honest assessment of what the proved properties actually guard. | any target with proofs attempted |
| `aeneas` | Rust-only. Auto-generate Lean from Rust via Charon+Aeneas. NOOPs on non-Rust repos. | Cargo.toml at repo root |
| `ci` | Create/maintain `.github/workflows/lean-ci.yml` (elan + mathlib cache + `lake build` on Lean-touching PRs). | formal-verification/lean/ exists |
| `report` | Update `formal-verification/REPORT.md` (always-on; dispatched unconditionally by this tick). | — |
| `status` | Maintain the rolling dashboard issue titled `[Lean Squad] Formal Verification Status` (always-on; dispatched unconditionally). | — |

`report` and `status` are dispatched unconditionally by the next phase (`dispatch`). **You pick the
other two**, and they are what drive real progress.

## How to pick

1. **Read `formal-verification/repo-memory.json`** if it exists. The `phase` field hints at which
   stage the system is in (`orient` | `specify` | `prove` | `mature`). The `runs` array lists
   recent ticks' choices — avoid picking a task that was picked in the last 1-2 ticks unless
   artefact state demands it.
2. **Count artefacts on disk** to understand actual progress:
   - `ls formal-verification/specs/ 2>/dev/null | wc -l` → informal specs
   - `ls formal-verification/lean/FVSquad/*.lean 2>/dev/null | wc -l` → formal specs
   - `grep -l 'sorry' formal-verification/lean/FVSquad/*.lean 2>/dev/null | wc -l` → unproven
   - `gh pr list --label lean-squad --state open --json number --jq 'length'` → in-flight work
3. **Apply phase-aware weighting.** Early ticks (empty `formal-verification/lean/`) favour
   `orient` and `informal-spec`. Once targets exist, shift toward `formal-spec`→`extract-impl`→
   `prove`. Once proofs accumulate, `correspondence` and `critique` become valuable. `ci` is a
   one-shot: pick it once, not repeatedly. `aeneas` is Rust-only.
4. **Distinct target slugs.** The two tasks you pick MUST target **different files** in
   `formal-verification/lean/FVSquad/<Target>.lean`. This is a hard constraint — two parallel
   vessels writing the same Lean file will conflict-merge. If you pick two spec/impl/prove
   tasks, they must have different `target_slug` values. Tasks that don't touch a specific
   target (e.g. `orient`, `correspondence`, `critique`, `ci`) can use the literal target slug
   `_global` (distinct by virtue of the slug itself).

## Handling bootstrap-pending state

If `bootstrap_if_missing` emitted `BOOTSTRAP-PENDING:`, the project is mid-bootstrap. Pick only
workflows that don't need `formal-verification/` yet: you may still pick `orient` (it's
idempotent and will no-op if `TARGETS.md` is already present). If nothing else is safe, emit
two `PLAN:` lines both naming `orient` on distinct pseudo-target slugs `_warmup-a` and
`_warmup-b` — the dispatch phase will de-dupe and the always-on `report`+`status` pair still
fire.

## Output format

Emit **exactly** this, in this order:

```
REASONING:
<1-3 sentences explaining why these two tasks this tick, referencing repo-memory.json state
 or artefact counts you observed>

PLAN: <task-slug> <target-slug>
PLAN: <task-slug> <target-slug>
```

- `<task-slug>` is one of: `orient`, `informal-spec`, `formal-spec`, `extract-impl`, `prove`,
  `correspondence`, `critique`, `aeneas`, `ci`.
- `<target-slug>` is a short filesystem-safe identifier (kebab-case, no spaces). Use the stem
  of the Lean file (e.g. `binary-search`, `parse-url`). Use `_global` for whole-repo tasks.
- The two PLAN: lines MUST have distinct `<target-slug>` values.
- DO NOT emit `PLAN: report` or `PLAN: status` — those are dispatched unconditionally.

End after the second `PLAN:` line. No other text after it.
