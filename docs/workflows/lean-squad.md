# Lean Squad: progressive formal verification for any xylem repo

> 🔬 Inspired by [agentics' lean-squad](https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md). Ported into xylem's `core` profile as a cluster of ~14 workflows that apply [Lean 4](https://lean-lang.org/) formal verification (FV) to a codebase progressively, optimistically, and without assuming prior FV expertise from the humans or agents involved.

## What it does

Every 8 hours, on any xylem-managed repo that uses the `core` profile, a **tick** fires. The tick first runs a **deterministic opt-in gate** — three plain shell checks that cost essentially nothing. On the vast majority of repos, the gate sees no signal and the tick terminates immediately via `XYLEM_NOOP`.

On a repo that **has opted in**, the tick does five things:

1. **Bootstrap** the Lean toolchain and `formal-verification/` scaffold if missing.
2. **Merge** any ready `lean-squad`-labelled PRs from earlier ticks.
3. **Assess** the current state of the FV artefact tree (what specs exist, what's proved, what's still `sorry`-guarded) and pick the **two highest-value next tasks** for this tick.
4. **Dispatch** those two tasks as independent vessels, plus two always-on observability workflows (`lean-squad-report` updates the status Markdown, `lean-squad-status` maintains a rolling GitHub dashboard issue).
5. **Retrospect** — record what this tick dispatched into `formal-verification/repo-memory.json` so future ticks can make better decisions.

Over weeks, the system progressively builds a Lean model of your codebase: proved theorems about pure functions, `sorry`-guarded specifications for the tricky bits, honest `CRITIQUE.md` about what the model does and doesn't capture, and filed GitHub issues when a proof fails in a way that signals a real spec or implementation bug.

## How to opt in

Any **one** of these signals enables Lean Squad on a repo:

1. A `formal-verification/` directory exists at the repo root (typically created by the first run of `lean-squad-bootstrap`).
2. A `.github/lean-squad.yml` file exists (created by a human who wants to opt in without bootstrapping yet).
3. Any open GitHub issue carries the `lean-squad-opt-in` label.

The simplest way to kick things off on a new repo:

```bash
gh issue create \
  --title "Enable Lean Squad formal verification" \
  --body "Bootstrap lean-squad on this repository." \
  --label lean-squad-opt-in
```

The next 8h tick will detect the label, enqueue `lean-squad-bootstrap`, and start building a `formal-verification/` scaffold. Subsequent ticks run the full cycle.

## How to opt out / stop

- Remove the three signals above (delete `formal-verification/`, delete `.github/lean-squad.yml`, close or unlabel the `lean-squad-opt-in` issues). The next tick emits `XYLEM_NOOP` and stops.
- Or: add the `no-bot` label to a specific issue to exclude just that issue from triggering `lean-squad-focus`.

## Running a focused task by slash-command analogue

To ask the system to work on a specific target right now (instead of waiting 8h), open an issue with the `lean-squad-focus` label:

```
Title: Formalise the parse_url function
Body:
Task: formal-spec
Target: parse-url
Notes: focus on the query-string extraction path; skip the fragment handling for now
Labels: lean-squad-focus
```

The `lean-squad-focus` workflow reads the issue body, parses the task+target, and enqueues the corresponding `lean-squad-<task>` workflow with that target pinned. Removes the label when done.

## The 12 sub-workflows (plus the tick)

| Workflow | Trigger | Output | Writes to |
|---|---|---|---|
| `lean-squad` (this tick) | Scheduled, 8h | Enqueues 2 tasks + report + status | `formal-verification/repo-memory.json` (runs[] only) |
| `lean-squad-bootstrap` | Enqueued by tick | Installs elan/Lean, writes scaffold, opens PR | `formal-verification/{README,RESEARCH,TARGETS,CORRESPONDENCE,CRITIQUE,REPORT}.md`, `lakefile.toml`, `lean-toolchain`, `lean/FVSquad/Stub.lean`, `repo-memory.json` (seed only) |
| `lean-squad-orient` | Enqueued by tick | Surveys repo, proposes 3-5 targets | `formal-verification/{RESEARCH,TARGETS}.md` |
| `lean-squad-informal-spec` | Enqueued by tick | Writes plain-prose spec for one target | `formal-verification/specs/<target>_informal.md` |
| `lean-squad-formal-spec` | Enqueued by tick | Writes Lean 4 skeleton with `sorry` bodies | `formal-verification/lean/FVSquad/<Target>.lean` |
| `lean-squad-extract-impl` | Enqueued by tick | Replaces `sorry` function bodies with Lean | same file (target-pinned) |
| `lean-squad-prove` | Enqueued by tick | Proves a theorem OR files an issue | same file, plus `[finding]`-labelled issues |
| `lean-squad-correspondence` | Enqueued by tick | Maintains source-to-Lean mapping table | `formal-verification/CORRESPONDENCE.md` |
| `lean-squad-critique` | Enqueued by tick | Honest assessment of coverage gaps | `formal-verification/CRITIQUE.md` |
| `lean-squad-aeneas` | Enqueued by tick | Rust-only: auto-generate Lean from Rust | `formal-verification/lean/FVSquad/Aeneas/Generated/*.lean` |
| `lean-squad-ci` | Enqueued by tick | Creates `.github/workflows/lean-ci.yml` | that file only |
| `lean-squad-report` | Enqueued by tick (always-on) | Updates status Markdown with mermaid diagram | `formal-verification/REPORT.md` |
| `lean-squad-status` | Enqueued by tick (always-on) | Maintains rolling GitHub dashboard issue | `[Lean Squad] Formal Verification Status` issue body |
| `lean-squad-focus` | `lean-squad-focus` label | Parses issue body, enqueues a specific task | — |

## State ownership (prevents parallel-vessel write conflicts)

Multiple `lean-squad-*` vessels can run concurrently. Each artefact below has exactly one **writer** workflow; all others are read-only. The tick's `assess_state` phase enforces that the two tasks it dispatches per tick target **distinct** `<target-slug>` values, preventing two vessels from racing on the same `lean/FVSquad/<Target>.lean` file.

| File / artefact | Sole writer |
|---|---|
| `formal-verification/repo-memory.json` | `lean-squad` (this tick's `retrospect` phase) |
| `formal-verification/REPORT.md` | `lean-squad-report` |
| `formal-verification/{RESEARCH,TARGETS}.md` | `lean-squad-orient` |
| `formal-verification/CORRESPONDENCE.md` | `lean-squad-correspondence` |
| `formal-verification/CRITIQUE.md` | `lean-squad-critique` |
| `formal-verification/specs/<target>_informal.md` | `lean-squad-informal-spec` (pinned to `<target>`) |
| `formal-verification/lean/FVSquad/<Target>.lean` | pinned per tick by `assess_state` |
| `formal-verification/lean/FVSquad/Aeneas/Generated/*.lean` | `lean-squad-aeneas` |
| `.github/workflows/lean-ci.yml` | `lean-squad-ci` |
| Dashboard issue body | `lean-squad-status` |

Task vessels emit a structured marker in their PR body so the next tick's `retrospect` phase can reconcile state cheaply:

```
LEAN_SQUAD_RUN: {"task":"<name>","target":"<slug>","status":"<ok|partial|blocked>","artefact":"<path>"}
```

## Configuration surface

Two sources are added to the core profile `xylem.yml` template by Unit 14:

```yaml
lean-squad-tick:
  type: schedule
  cadence: "8h"
  workflow: lean-squad

lean-squad-focus:
  type: github
  repo: "{{ .Repo }}"
  exclude: [no-bot]
  tasks:
    focus:
      labels: [lean-squad-focus]
      workflow: lean-squad-focus
```

There is deliberately **no config knob to disable** the tick short of removing the source from `.xylem.yml`. The deterministic opt-in gate makes an "off" knob unnecessary — on non-opted-in repos the tick costs one tiny shell call every 8h.

## Costs and guardrails

- **On non-opted-in repos:** ~1 short shell call every 8h. No LLM tokens, no GitHub API calls beyond `gh issue list --label lean-squad-opt-in` (one cheap call per tick).
- **On opted-in repos:** 1 LLM call for `assess_state` + 1 LLM call for `retrospect` + the dispatched 4 vessels (2 selected tasks + report + status). The `dispatch` phase hard-caps total enqueues per tick at 4, so no prompt hallucination can fan out an unbounded number of vessels.
- **Merge safety:** the tick's `merge_open_prs` phase merges only `MERGEABLE` + `CLEAN` PRs labelled `lean-squad` that do **NOT** have the `do-not-merge` label. Hold any PR indefinitely by adding `do-not-merge`.
- **Toolchain failures:** if `lake build` fails during bootstrap, the system emits a `TOOLING-GAP` marker and subsequent ticks skip tasks that require a working toolchain (`formal-spec`, `extract-impl`, `prove`, `aeneas`) until the gap is fixed.

## Adding this to a repo

`xylem init --profile core --force` scaffolds the workflows into `.xylem/workflows/` and the prompts into `.xylem/prompts/`. No other code change is required. The first tick that detects an opt-in signal will kick off `lean-squad-bootstrap`.

## Debugging a stuck tick

1. **Check the gate.** Look at `.xylem/phases/lean-squad-*/gate.output` — it should say either `OPT-IN: ...` or `XYLEM_NOOP: ...`.
2. **Check the dispatch.** `.xylem/phases/lean-squad-*/dispatch.output` lists what was enqueued. If the cap was hit, you'll see `DISPATCH-SKIP: cap 4 reached`.
3. **Check the daemon queue.** `xylem queue list` shows pending/running/waiting vessels. A stuck `lean-squad-bootstrap` usually means elan/Lean installation is asking for an interactive sudo password — the bootstrap prompt is supposed to use the non-interactive installer; file an issue if it doesn't.
4. **Check `repo-memory.json`.** If it's missing or malformed, `retrospect` will refuse to overwrite it. Manually restore from git history or delete and let bootstrap re-seed.

## Further reading

- [agentics lean-squad source](https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md) — the original system this port is modelled on.
- [Lean 4 reference](https://lean-lang.org/lean4/doc/) — the formal-verification language.
- [mathlib4](https://github.com/leanprover-community/mathlib4) — the community library pulled in by bootstrap.
- [Aeneas](https://github.com/AeneasVerif/aeneas) — the Rust-to-Lean translator used by the `lean-squad-aeneas` workflow.
- `.xylem/workflows/lean-squad.yaml` in any opted-in repo — the exact phases you see running.
