# Task

Produce a systematic, evidence-backed gap assessment of xylem's current state
against three spec documents that define the objective state of a productized
SoTA Agent Harness. The deliverable is a report a maintainer can act on — not
an executive summary. Make invisible gaps visible with enough citation that
every claim can be verified in under 30 seconds by the reader.

You are not fixing anything. You are not filing issues. You are producing one
markdown report.

# The objective state — read these first, in full

1. `docs/best-practices/harness-engineering.md` — 15-topic vendor-synthesized
   best-practices guide. Distinguishes `[Established]` consensus from
   `[Emerging]` guidance. Treat `[Established]` as baseline; `[Emerging]` as
   directional.
2. `docs/design/sota-agent-harness-spec.md` — Normative spec with MUST/SHOULD/MAY
   language. Part I ("Validated SoTA") is build-today; Part II ("Predictions")
   is directional and must not be presented as ship-blockers.
3. `docs/design/productize-xylem-for-external-repos-spec.md` — xylem-specific
   productization spec. §4 enumerates a "current baseline" frozen as of
   2026-04-09; §5–§7 define the three-layer architecture (core profile /
   adapt-repo / self-hosting-xylem overlay); later sections cover workflow
   classes, daemon reload, PR triggers, recovery, observability, cost,
   migration, rollout.

Read each spec end-to-end before opening any code. Extract MUST/SHOULD
requirements as a working checklist. Note that the productize spec's §4 is a
frozen baseline — you must re-verify each "current state" claim against
origin/main, because the tree has moved since 2026-04-09.

# The current state — three independent evidence axes

Your assessment must triangulate across all three axes. Do not rely on any one
axis alone. A claim with code evidence but no operational evidence is weaker
than one with both; call out when axes disagree — that is the most valuable
signal this exercise produces.

**Axis A — Codebase (structural reality)**
- `cli/cmd/xylem/root.go` — authoritative list of wired CLI commands.
- `cli/internal/*/` — package by package, determine which packages are imported
  by `cli/internal/runner/*.go`, `cli/internal/scanner/*.go`, or a `cli/cmd/xylem/*.go`
  command. Packages with compiled code but zero production import sites are
  "dormant library code" in the productize spec's sense.
- `.xylem/workflows/*.yaml` and `.xylem/prompts/*/*.md` — the actual workflow
  inventory (not the inline constants in `init.go`).
- `.xylem.yml` — real source/gate/intent-policy configuration.
- Grep markers for the capabilities called out in the SoTA spec: `ctxmgr`,
  `catalog`, `mission`, `memory`, `signal`, `evaluator`, `bootstrap`,
  `intermediary`, `recovery`, `orchestrator`, `cost`, `evidence`, `surface`.

**Axis B — Issues, PRs, and recent history (behavioral reality)**
- `gh issue list --state open --limit 200 --json number,title,labels,createdAt,updatedAt`
  then cluster by label and by spec section. Look particularly for:
  `xylem-failed`, `harness-impl`, `[sota-gap]`, `failure-recovery`,
  `autonomy-gap`, `adapt-repo`, `workflow-class`, `harness-gap-analysis`.
- `gh pr list --state all --limit 50 --json number,title,state,mergedAt,labels` —
  which gap-class PRs landed, which stalled, which were superseded.
- `git log --since="3 weeks ago" --oneline` and `git log --oneline -80` —
  correlate shipped work against spec sections.

**Axis C — Daemon and vessel operational reality**
- `.daemon-root/.xylem/queue.jsonl` — every vessel's state-machine history.
  Count failures, timeouts, retry chains (`-retry-N-retry-M`), stranding
  patterns. Stranded vessels and repeated state transitions are evidence of
  harness-level failure modes the specs warn against.
- `.daemon-root/.xylem/audit.jsonl` — daemon lifecycle events: upgrades,
  reloads, drain-budget decisions, stall-detector firings.
- `.daemon-root/.xylem/state/daemon-health.json` — current health snapshot.
- `.daemon-root/.xylem/state/daemon-control-plane-current.json` — active
  workflow/config digest the daemon is running against.
- `.daemon-root/.xylem/state/daemon-reload-audit.jsonl` — reload history,
  relevant to productize spec §9 (daemon reload).
- `.daemon-root/.xylem/state/sota-gap-snapshot.json` plus its schema at
  `.daemon-root/.xylem/state/sota-gap-snapshot.schema.json` — existing
  machine-readable gap report. **This is prior art you must read and extend,
  not duplicate and not overwrite.** Note each capability it already covers
  and either confirm, update, or contradict it with current evidence.
- `.daemon-root/.xylem/phases/<id>/` — sample a handful of recent vessel phase
  outputs (`summary.json`, `cost.json`, `evidence.json`, `events.jsonl`) to see
  what phases actually do vs what the spec says they should do.
- If and only if non-destructive: `xylem status` and `xylem doctor` against the
  running daemon. Do not run `xylem doctor --fix` — it has historically been
  destructive.

# Methodology — work in phases, do not collapse them

**Phase 1 — Spec absorption.** Read all three specs end-to-end. Produce an
internal checklist of every MUST/SHOULD requirement in the SoTA spec and every
`[Established]` practice in the harness-engineering guide, each keyed to its
source section. Extract the productize spec's §4 baseline claims as a
re-verification checklist.

**Phase 2 — Baseline re-verification.** For each §4 claim ("X exists" /
"Y is dormant" / "Z is missing"), verify against origin/main. Flag drift
between the spec and reality — the tree has moved since 2026-04-09.

**Phase 3 — Capability survey.** For every capability called out in SoTA spec
§4.1–§4.10 (repo/knowledge, context, tool plane, memory/state, orchestration,
evaluation, security, observability, lifecycle, cost), locate the xylem
implementation if any, determine wiring status, and record `file:line`
citations. Do the same for productize spec §5–§14 capabilities (profile
filesystem, workflow classes, adapt-repo, daemon reload, PR triggers,
recovery phases 2–4, bootstrap CLI surface, parameterized PR workflows,
validation commands).

**Phase 4 — Operational evidence mapping.** For each capability, pull the
corresponding operational signal from queue.jsonl, audit.jsonl, phase outputs,
open issues, and recent PRs. A capability wired in code but with operational
evidence of repeated failure is a different gap class than one simply absent.

**Phase 5 — Classification.** Classify every gap on two axes:
- **Presence**: `missing` (no code) | `dormant` (code exists, zero production
  import sites) | `wired-partial` (wired on some paths, not others) |
  `wired-broken` (wired, operational evidence of failure) | `wired-healthy`
  (baseline — note briefly, do not belabor).
- **Spec weight**: `MUST` | `SHOULD` | `MAY` | `[Established]` | `[Emerging]`.
  MUST gaps are ship-blockers for productization; SHOULD gaps are quality gaps;
  `[Emerging]` gaps are optional and must not be surfaced as blockers.

**Phase 6 — Prioritization.** Rank gaps by: (a) spec weight, (b) blast radius
(does this block productization on external repos, or only degrade xylem-on-xylem?),
(c) smallest-next-step cost, (d) whether an open issue or in-flight PR already
addresses it. Do not invent new priority dimensions.

**Phase 7 — Synthesis.** Write the report.

You may spawn parallel Explore subagents for independent capability axes in
phases 3 and 4 to protect your main context. Do not delegate synthesis or
classification — those belong to the driving agent.

# Output

Write the assessment to `docs/assessments/sota-gap-assessment-<YYYY-MM-DD>.md`
(create the directory if needed). Do NOT overwrite `.xylem/state/sota-gap-snapshot.json`
or `.daemon-root/.xylem/state/sota-gap-snapshot.json` — those are
daemon-managed artifacts.

Required sections, in order:

1. **Executive summary** (≤400 words). Top-5 ship-blockers for productization,
   top-5 operational risks, top-5 fastest wins. One sentence each. No hedging.

2. **Methodology and evidence inventory.** What you read, what you ran, what
   the daemon state showed. This is the audit trail for the rest of the report.

3. **Spec-baseline drift table.** For each §4 claim in the productize spec that
   is no longer accurate, show `spec says` → `tree shows` with `file:line`
   evidence. If §4 is still fully accurate, say so explicitly.

4. **Capability matrix.** One row per capability, covering all SoTA spec
   §4.1–§4.10 and all productize spec §5–§14 capabilities. Columns:
   `spec section` | `spec language` | `presence` | `code evidence` |
   `operational evidence` | `gap class` | `smallest next step`.

5. **Harness-engineering best-practices coverage.** For each of the 15 topic
   areas in `harness-engineering.md`, a one-paragraph assessment: which
   `[Established]` practices xylem follows, misses, or partially implements.
   Cite the harness-engineering guide section and the xylem code or ops
   evidence.

6. **Productization-readiness assessment.** Against productize spec goals §2
   and non-goals §3, is xylem ready to ship to an external repo today? What is
   the shortest path to yes? Name the specific workflows, code surfaces, and
   open issues that gate this.

7. **Prioritized gap backlog** (≤50 items). Each item: title, gap class, spec
   cite, code evidence, ops evidence, smallest next step, existing issue/PR
   if any. Ordered by the phase-6 priority function.

8. **Risks and open questions.** Gaps where spec and reality diverge in ways
   that need human judgment, not engineering. These go to the maintainer, not
   to the backlog.

# Quality bar

- **Every factual claim must cite.** `file:line` for code, `#<num>` for
  issues/PRs, `audit.jsonl:<line>` or `queue.jsonl:<vessel-id>` for ops
  evidence, `§<section>` for spec citations. A claim without a citation is
  noise; delete it.
- **Verify paths before citing.** Example: phase outputs moved from
  `.xylem/phases/` to `.xylem/state/phases/` in a recent refactor. Re-grep
  before citing any path you took from the spec.
- **Distinguish MUST / SHOULD / MAY / [Emerging].** Do not present an
  `[Emerging]` recommendation as a ship-blocker. Do not launder `SHOULD` as
  `MUST`.
- **Triangulate.** Code + ops + issue is strong; code alone is weak; memory
  or prior conversation is not evidence. Call out disagreements between axes.
- **Do not rely on prior memory summaries or loop digests.** Re-read the
  current code and the current specs. Any summary you didn't produce this
  session is suspect.
- **No fabrication.** If you cannot find evidence for a claim, either find the
  evidence or drop the claim.
- **Be holistic.** Cover all 15 harness-engineering topic areas, all SoTA
  spec §4 subsections, and all productize spec §5–§14 sections. Silent
  omissions are themselves a quality flaw; if a topic is out of scope, say so.

# Scope and constraints

- **Read-only.** Do not modify any code, config, workflow YAML, or prompt
  template. The following are protected control surfaces and must not be
  edited under any circumstances:
  `.xylem/HARNESS.md`, `.xylem.yml`, `.xylem/workflows/*.yaml`,
  `.xylem/prompts/*/*.md`.
- **Do not perturb the running daemon.** No restarts, pauses, cancels,
  enqueues, or `xylem doctor --fix`. Read `.daemon-root/.xylem/*` artifacts
  only.
- **Do not file issues or comment on PRs.** The only deliverable is the
  markdown report at the path in the Output section.
- **Do not run destructive git or gh commands.** Reads only.
- **Depth over breadth when forced to choose.** If the choice is 20 more
  items cited shallowly vs. 5 items with triangulated evidence, choose depth.
