# xylem Self-Building Autonomy & Velocity Review

**Date:** 2026-04-14
**Branch:** `review/autonomy-velocity-audit`
**Daemon binary:** `b009877ca4ac` (alive, last tick 17:04 UTC)
**Queue snapshot:** 505 entries total, last 50 analyzed

---

## 1. Current State Summary

### Active inventory

| Category | Count |
|---|---|
| Workflow YAML files | 17 |
| Source configs in `.xylem.yml` | 17 (bugs, features, triage, refinement, harness-impl, harness-pr-lifecycle, harness-merge, conflict-resolution, sota-gap, continuous-simplicity, continuous-improvement, release-cadence, backlog-refinement, doc-garden, harness-gap-analysis, harness-post-merge, diagnose-failures) |
| Scheduled sources | 7 (backlog-refinement 2h, diagnose-failures 1h, release-cadence 2h, harness-gap-analysis 4h, continuous-improvement 6h, continuous-simplicity 12h, doc-garden 24h) |
| Event-driven sources | 6 (bugs, features, triage, refinement, harness-impl, harness-pr-lifecycle) |
| PR-event sources | 3 (harness-merge, conflict-resolution, harness-post-merge) |
| Global concurrency | 3 |
| Eval scenarios | 6 |

### Throughput (last 50 entries, ~17 hours)

| Metric | Value |
|---|---|
| Total vessels | 50 |
| Completed | 17 (34%) |
| Failed | 23 (46%) |
| Cancelled | 10 (20%) |
| Avg duration (all) | 197s |
| Avg duration (completed) | ~300s |
| Effective throughput | ~1 completed vessel/hour |

### Top failure patterns

| Pattern | Count | Root cause |
|---|---|---|
| `diagnose-failures` max_turns exhaustion | 7/7 (100%) | Prompt asks agent to enumerate all failed vessels + read all outputs + cross-ref git in 20 turns — impossible at current failure volume |
| `backlog-refinement` jq parse error | 8/9 (89%) | Python sanitizer in `apply_actions` only handles `\n`, `\r`, `\t` — misses other U+0000–U+001F control chars in LLM-generated JSON |
| `doc-garden` missing workflow | 1/1 (100%) | `.xylem/workflows/doc-garden.yaml` does not exist but source references it |
| `unblock-wave` auto-cancelled | 9/9 (100%) | Expected: PRs merged before dequeue |

**Effective failure rate (excluding cancellations and expected noops):** 23/40 = **57.5%**. Stripping the two dominant patterns (`diagnose-failures` 7, `backlog-refinement` 8), the real failure rate on productive work drops to 8/25 = **32%** — still high but largely driven by scheduled busywork, not issue-driven work.

**Issue-driven success rate:** `fix-bug` 4/5 (80%), `implement-feature` 1/1 (100%), `implement-harness` — no recent runs in snapshot. This is the number that matters for self-building velocity.

---

## 2. Coverage Gap Analysis

### Gap 1: `diagnose-failures` is a self-referential failure generator

**What's missing:** The `diagnose-failures` workflow has a 20-turn budget for a prompt that asks the agent to: enumerate all failed vessels in the last 24h, read all their phase outputs, cross-reference git history, and produce a structured report. At current failure volume (23 failures in 17h), this exceeds what 20 turns can accomplish. Every run fails, which creates more failures, which makes the next run even harder.

**Impact:** 7 wasted vessels per day (14% of throughput). Zero diagnostic value produced. The `failure-review.json` always records `recovery_class: "unknown"`, `recovery_action: "human_escalation"`, `retry_suppressed: true`.

**Suggested fix:** Two changes:
1. Increase `max_turns` for the `diagnose` phase from 20 to 40 in `.xylem/workflows/diagnose-failures.yaml`
2. Modify `.xylem/prompts/diagnose-failures/diagnose.md` to scope the task: instead of "all failed vessels in the last 24 hours," limit to "the 3 most recent failed vessels that are NOT `diagnose-failures` or `backlog-refinement` workflows" — this prevents self-referential loops and focuses on actionable failures

### Gap 2: `backlog-refinement` jq sanitizer is incomplete

**What's missing:** The Python sanitizer in the `apply_actions` command phase (`.xylem/workflows/backlog-refinement.yaml`, phase `apply_actions`) only escapes `\n`, `\r`, `\t` in JSON string values. Other control characters in the U+0000–U+001F range (form feeds, vertical tabs, null bytes, etc.) pass through unescaped and cause `jq` to reject the file.

**Impact:** 8/9 runs fail. The 12:00 UTC run succeeded only because the LLM happened not to produce problematic characters. This is data-dependent: any comment body with exotic control chars triggers the bug.

**Suggested fix:** In the Python sanitizer within `backlog-refinement.yaml`'s `apply_actions` command, replace the narrow character-by-character check with a regex that strips all control characters:
```python
import re
content = re.sub(r'[\x00-\x08\x0b\x0c\x0e-\x1f]', '', content)
```
This preserves `\n` (0x0a), `\r` (0x0d), and `\t` (0x09) while stripping everything else in the range. Apply the same fix to the identical sanitizer in `triage.yaml`'s `apply_actions` phase.

### Gap 3: `doc-garden` workflow YAML does not exist

**What's missing:** `.xylem.yml` defines a `doc-garden` source with `schedule: "0 */24 * * *"` referencing workflow `doc-garden`, but `.xylem/workflows/doc-garden.yaml` does not exist. Every scheduled run fails immediately with `load workflow: no such file or directory`.

**Impact:** 1 wasted vessel per day + noise in failure logs.

**Suggested fix:** Either create `.xylem/workflows/doc-garden.yaml` (a documentation gardening workflow with phases: scan for stale docs, propose updates, create PR) or remove the `doc-garden` source from `.xylem.yml` until the workflow is ready.

### Gap 4: No self-review before PR creation

**What's missing:** `implement-harness` has a `test_critic` phase and a `smoke` phase before PR creation — these serve as self-review. But `fix-bug` and `implement-feature` go straight from `verify` → `pr` with no self-review step. The `verify` phase runs crosscheck verification but doesn't review the PR diff itself for scope creep, style issues, or unnecessary changes.

**Impact:** PRs from `fix-bug`/`implement-feature` may contain unnecessary changes that a reviewer (copilot-pull-request-reviewer) catches, requiring a `respond-to-pr-review` cycle that adds latency.

**Suggested fix:** Add a `test_critic` phase to `fix-bug.yaml` and `implement-feature.yaml` between `verify` and `pr`, reusing the existing `.xylem/prompts/implement-harness/test_critic.md` pattern. Gate: `go test ./...` with 1 retry. This adds ~2-3 minutes per vessel but should reduce PR review round-trips.

### Gap 5: `fix-bug` and `implement-feature` lack rebase-before-push

**What's missing:** `implement-harness.yaml` has a gate on `pr_draft` that verifies the branch is not behind `origin/main` (`git fetch origin main && git merge-base --is-ancestor origin/main HEAD`). `fix-bug.yaml` and `implement-feature.yaml` don't have this check. If `main` advances while a vessel runs, the PR may have merge conflicts from the start.

**Impact:** Creates conflict-resolution vessels that could have been avoided. The `resolve-conflicts` workflow exists but adds a full vessel cycle of latency.

**Suggested fix:** Add a rebase check gate to the `pr` phase of both `fix-bug.yaml` and `implement-feature.yaml`:
```yaml
gate:
  type: command
  command: |
    git fetch origin main &&
    git merge-base --is-ancestor origin/main HEAD ||
    (git rebase origin/main && echo "rebased")
  on_failure: retry
  retries: 1
```

### Gap 6: No post-merge CI monitoring

**What's missing:** After a PR is merged (via `merge-pr` or auto-merge), nothing monitors whether the merge broke CI on `main`. The `github-actions` source type exists and is configured for `harness-post-merge`, but that workflow focuses on running `harness-gap-analysis` — it doesn't check whether the merge itself caused CI failures.

**Impact:** A bad merge could break `main` and block all subsequent vessels (since they branch from `main`). Currently relies on human detection.

**Suggested fix:** Add a `ci-watchdog` scheduled source (every 30m) with a simple workflow: check `gh run list --branch main --limit 1 --json conclusion` — if the latest CI run failed, file an issue with label `bug` + `ready-for-work` and include the failing run URL. This closes the loop: bad merge → issue filed → `fix-bug` picks it up.

### Gap 7: Incomplete failure→retry loop

**What's missing:** When `diagnose-failures` runs, it's supposed to analyze failures and file issues. But since it always fails (Gap 1), the diagnosis→file→retry loop is completely broken. Even if it worked, the loop has a gap: `diagnose-failures` files issues, but those issues need labels (`bug` + `ready-for-work`) to be picked up by the `bugs` source. The `create_issues` phase prompt (`.xylem/prompts/diagnose-failures/create_issues.md`) does specify adding those labels, but since `diagnose` never completes, `create_issues` never runs.

**Impact:** Failed vessels accumulate without remediation. The self-healing loop is fully severed.

**Suggested fix:** Fix Gap 1 first. Then verify that the `create_issues` prompt actually applies the correct labels by adding a gate check:
```yaml
gate:
  type: command
  command: "test -f create_issues.output && grep -q 'ready-for-work' create_issues.output"
```

---

## 3. Throughput Recommendations

### 3.1 Concurrency: keep at 3

Current utilization shows slots are often idle (daemon frequently has 0 running vessels between scheduled bursts). The bottleneck is not concurrency — it's the failure rate of scheduled workflows consuming slots unproductively. Fixing Gaps 1-3 will reclaim ~16 wasted vessel-slots per day without changing concurrency.

**No change recommended.** Increasing concurrency would amplify cost without improving completed-vessel throughput until failure rate drops below 30%.

### 3.2 max_turns adjustments

| Workflow | Phase | Current | Recommended | Rationale |
|---|---|---|---|---|
| `diagnose-failures` | `diagnose` | 20 | 40 | Consistently hitting limit; task requires reading multiple vessel outputs |
| `diagnose-failures` | `create_issues` | 20 | 15 | Filing issues is mechanical once diagnosis exists; tighten to fail fast |
| `backlog-refinement` | `analyze` | 50 | 40 | Analysis rarely needs full budget (successful run used ~30) |
| `continuous-improvement` | `implement` | 80 | 60 | Matches `fix-bug` implement budget; CI prevents long-running implementations |
| `sota-gap-analysis` | `survey` | 60 | 40 | Survey is read-only research; 60 turns encourages wandering |

**Expected impact:** ~15% reduction in token spend on scheduled workflows, faster failure detection on over-scoped phases.

### 3.3 Timeouts: no changes

Default 45m and harness-impl 90m are appropriate. No vessels in the snapshot timed out — failures are gate/turn-budget driven, not wall-clock driven.

### 3.4 Gate retries

| Workflow | Phase | Current retries | Recommended | Rationale |
|---|---|---|---|---|
| `fix-bug` | `implement` | 2 | 2 | Evidence from phase outputs shows retries do recover (~60% success on retry) |
| `fix-bug` | `verify` | 1 | 2 | Verify failures are often flaky test ordering; one more retry is cheap |
| `implement-feature` | `implement` | 2 | 2 | No change needed |
| `implement-feature` | `verify` | 1 | 2 | Same rationale as fix-bug |
| `fix-pr-checks` | `fix` | 3 | 2 | 3 retries on CI fix is generous; if 2 don't work, the fix approach is wrong |

### 3.5 Scheduling frequency

| Source | Current | Recommended | Rationale |
|---|---|---|---|
| `diagnose-failures` | 1h | 2h | At 100% failure rate, running hourly just generates noise. After fixing Gap 1, 2h is sufficient given typical failure volume |
| `backlog-refinement` | 2h | 2h | No change — appropriate cadence once jq bug is fixed |
| `release-cadence` | 2h | 4h | 5/5 success but release-worthy PRs don't accumulate that fast; 4h reduces noop vessels |
| `harness-gap-analysis` | 4h | 6h | 5/5 success but findings are slow-changing; 6h is sufficient |
| `continuous-improvement` | 6h | 8h | Improvement opportunities don't accumulate in 6h windows |
| `continuous-simplicity` | 12h | 12h | No change — appropriate |
| `doc-garden` | 24h | 24h | No change (once workflow exists) |

**Expected impact:** ~30% fewer scheduled vessels per day (from ~48 to ~34), with the same or better output quality.

---

## 4. Quality Gate Improvements

### 4.1 Add `goimports` to all code-producing gates

Currently, `implement-harness` gates run `goimports -l . | head -1 && test -z "$(goimports -l .)"` but `fix-bug` and `implement-feature` gates only run `go test ./...`. Since CI checks `goimports`, PRs that pass local gates but fail CI formatting create `fix-pr-checks` vessels.

**Change:** Add `goimports` check to the gate commands in `fix-bug.yaml` (implement and verify phases) and `implement-feature.yaml`:
```yaml
gate:
  type: command
  command: "cd cli && goimports -l . | (! grep .) && go vet ./... && go build ./... && go test ./..."
```

**Cost:** ~5s per gate check. **Catches:** formatting failures before PR, eliminating ~1 `fix-pr-checks` vessel per bad PR.

### 4.2 Add `test_critic` to `fix-bug` and `implement-feature`

As described in Gap 4. The `test_critic` phase catches undertested code, missing edge cases, and test theatre. Currently only `implement-harness` and `review-pr` benefit from it.

**Cost:** ~2-3 minutes per vessel, ~$0.02 in tokens. **Catches:** weak test coverage, test theatre, missing assertions.

### 4.3 Strengthen noop detection

Several workflows use `XYLEM_NOOP` substring matching in phase output. This is generally robust, but there's an edge case in `merge-pr/check.md`: the prompt lists 6 conditions that must all pass, and any failure emits `XYLEM_NOOP`. However, the noop signal is a simple substring match — if an LLM produces `XYLEM_NOOP` in a reasoning block while actually intending to proceed, the phase would incorrectly noop.

**Mitigation:** The current prompts are explicit enough that this is low-risk. No change recommended, but monitor for false-noop incidents in phase outputs.

### 4.4 Add `go vet` to continuous-simplicity and continuous-improvement gates

Both workflows have `apply`/`implement` phases with gates that run `go build` and `go test` but not `go vet`. Since these workflows modify code, `go vet` should catch suspicious constructs.

**Change in `continuous-simplicity.yaml` apply gate and `continuous-improvement.yaml` implement gate:**
```yaml
command: "cd cli && go vet ./... && go build ./... && go test ./..."
```

---

## 5. Prompt Improvements

### 5.1 `diagnose-failures/diagnose.md` — scope reduction (highest leverage)

**File:** `.xylem/prompts/diagnose-failures/diagnose.md`

**Problem:** The prompt asks the agent to analyze ALL failed vessels in the last 24 hours. At current volume, this means reading 20+ vessel output directories, each with multiple phase files, plus git history cross-referencing. This exceeds the 20-turn budget every time.

**Before (conceptual):** "Read queue.jsonl, find all failed vessels in the last 24 hours, read all their phase outputs..."

**After:** Add a scoping section:
```markdown
## Scope constraints

- Analyze at most the **3 most recent** failed vessels
- **Exclude** vessels from the `diagnose-failures` and `backlog-refinement` workflows — these have known issues tracked separately
- For each vessel, read only the **last phase output** (the phase that failed), not all phases
- If no qualifying failures exist, output XYLEM_NOOP
```

**Impact:** Transforms a 20+ turn task into a 10-15 turn task. Enables the workflow to actually complete and produce value.

### 5.2 `backlog-refinement/analyze.md` — JSON output guardrails

**File:** `.xylem/prompts/backlog-refinement/analyze.md`

**Problem:** The LLM produces `actions.json` with comment bodies that contain raw control characters, breaking the downstream `jq` sanitizer.

**Add to the prompt:**
```markdown
## JSON output requirements

When writing `actions.json`, ensure all string values in the JSON are valid JSON strings:
- No raw control characters (U+0000 through U+001F) except `\n`, `\r`, `\t`
- All special characters must be properly escaped
- Use `\n` for newlines in comment bodies, never raw newlines
- Validate your JSON output with `python3 -c "import json; json.load(open('actions.json'))"` before finishing
```

**Impact:** Reduces the likelihood of jq failures at the source, complementing the sanitizer fix in Gap 2.

### 5.3 Gate retry feedback — verify it's actually injected

**File:** `cli/internal/runner/runner.go` (lines ~1836–2304)

The runner injects `gateResult` into template data on retry, but the prompt templates for `fix-bug/implement.md` and `implement-feature/implement.md` don't reference `{{.GateResult}}` or any similar variable. This means retry prompts re-run the same prompt blindly without telling the agent what went wrong.

**Verify:** Check whether `{{.PreviousOutputs}}` or `{{.GateResult}}` is available in the template context during retries. If `GateResult` is passed as template data but never consumed:

**Add to `fix-bug/implement.md`, `implement-feature/implement.md`, and other retryable phase prompts:**
```markdown
{{if .GateResult}}
## Previous attempt failed

The gate check failed with:
```
{{.GateResult}}
```

Fix the issues identified above before proceeding.
{{end}}
```

**Impact:** Gate retries become informed rather than blind, significantly improving retry success rate.

### 5.4 `continuous-improvement/analyze.md` — prevent scope creep

**Problem:** The continuous-improvement workflow has an 80-turn `implement` phase. Without tight scope constraints, the agent may attempt large refactors that fail gates and waste tokens.

**Add to `.xylem/prompts/continuous-improvement/analyze.md`:**
```markdown
## Scope constraint

Each improvement must be achievable in a single, focused PR that changes fewer than 10 files and fewer than 200 lines. If an improvement requires more, note it as "deferred — too large for single PR" and move to the next candidate.
```

### 5.5 Crosscheck verify prompts — consider deduplication

**Observation:** The crosscheck orchestrator prompt (~164 lines) is duplicated verbatim in `fix-bug/verify.md`, `implement-feature/verify.md`, and `implement-harness/verify.md`. This isn't a bug, but if the crosscheck prompt needs updating, three files must change. Consider extracting it to a shared partial if the template engine supports `{{template}}` or `{{.Include}}`.

**No immediate change** — this is a maintenance note, not a quality issue.

---

## 6. Quick Wins

These can be applied immediately with minimal risk:

### 6.1 Create `doc-garden.yaml` or remove the source

**File:** `.xylem.yml`, line referencing `doc-garden` source
**Action:** Remove the `doc-garden` source block from `.xylem.yml` until a workflow YAML is created. This eliminates 1 guaranteed failure per day.

### 6.2 Fix the jq sanitizer in `backlog-refinement.yaml`

**File:** `.xylem/workflows/backlog-refinement.yaml`, `apply_actions` phase command
**Action:** Replace the narrow Python sanitizer with a regex that strips all non-printable control characters except `\n`, `\r`, `\t`:
```python
import re; content = re.sub(r'[\x00-\x08\x0b\x0c\x0e-\x1f]', '', content)
```
Apply the same fix to `triage.yaml`'s `apply_actions` phase.

### 6.3 Increase `diagnose-failures` max_turns

**File:** `.xylem/workflows/diagnose-failures.yaml`, `diagnose` phase
**Action:** Change `max_turns: 20` → `max_turns: 40`

### 6.4 Reduce `diagnose-failures` schedule frequency

**File:** `.xylem.yml`, `diagnose-failures` source
**Action:** Change `schedule: "0 * * * *"` → `schedule: "0 */2 * * *"` (every 2h instead of every 1h)

### 6.5 Reduce `release-cadence` schedule frequency

**File:** `.xylem.yml`, `release-cadence` source
**Action:** Change from 2h to 4h schedule. Release-worthy PRs don't accumulate that fast; 5/5 recent runs completed as noops.

### 6.6 Add scope exclusion to diagnose-failures prompt

**File:** `.xylem/prompts/diagnose-failures/diagnose.md`
**Action:** Add the scoping constraints from Section 5.1 to prevent self-referential failure cascades.

---

## 7. Proposed New Workflows

### 7.1 `ci-watchdog`

**Trigger:** Scheduled, every 30 minutes
**Phases:**
1. `check` (command, noop): `gh run list --branch main --limit 1 --json conclusion -q '.[0].conclusion'` — if `success`, emit `XYLEM_NOOP`
2. `diagnose` (prompt, 20 turns): Read the failing CI run logs, identify the breaking commit, determine if it's a test flake or a real regression
3. `file_issue` (prompt, 15 turns, gate: `gh issue list -l bug -l ready-for-work --json title -q length` confirms issue was created): File a GitHub issue with label `bug` + `ready-for-work`, referencing the failing run and breaking commit

**Justification:** Closes Gap 6. Currently, a bad merge to `main` can break CI and block all downstream vessels until a human notices. This workflow detects CI failures within 30 minutes and feeds them into the existing `fix-bug` pipeline, completing the self-healing loop: merge → CI fail → issue → fix-bug → PR → merge.

### 7.2 `pr-self-review`

**Trigger:** GitHub PR event (on `labeled` with `ready-to-merge`)
**Phases:**
1. `review` (prompt, 30 turns): Review the PR diff for: scope creep beyond the linked issue, unnecessary formatting changes, missing test coverage, hardcoded values, debug leftovers. Output a structured review with pass/fail.
2. `apply_feedback` (prompt, 40 turns, gate: `go test ./...`, retries: 1, noop): If review found issues, fix them and push. If review passed, emit `XYLEM_NOOP`.
3. `push` (prompt, 20 turns): Push fixes if any were made.

**Justification:** Closes Gap 4 more completely than adding `test_critic` to individual workflows. This is a universal self-review that catches quality issues on any PR before it reaches external reviewers, reducing `respond-to-pr-review` cycles. Unlike per-workflow changes, this applies to all code-producing workflows uniformly.

**Trade-off vs Gap 4 fix:** This is an alternative to adding `test_critic` to `fix-bug`/`implement-feature`. The per-workflow approach is simpler and faster (no extra vessel). The `pr-self-review` approach is more thorough but adds a full vessel cycle of latency. Recommend starting with the per-workflow `test_critic` addition (Gap 4) and graduating to this if PR review round-trips remain high.

---

## Summary: Prioritized Action Plan

| Priority | Action | Expected impact | Section |
|---|---|---|---|
| P0 | Fix jq sanitizer in backlog-refinement + triage | Recovers 8 vessels/day | 6.2 |
| P0 | Scope + increase turns for diagnose-failures | Recovers 7 vessels/day, enables self-healing loop | 6.3 + 6.6 |
| P0 | Remove or create doc-garden workflow | Eliminates 1 guaranteed failure/day | 6.1 |
| P1 | Add goimports to fix-bug/implement-feature gates | Prevents formatting-only fix-pr-checks vessels | 4.1 |
| P1 | Add rebase-before-push to fix-bug/implement-feature | Prevents avoidable conflict-resolution cycles | Gap 5 |
| P1 | Add gate retry feedback to prompt templates | Improves retry success rate from ~60% to est. ~80% | 5.3 |
| P2 | Add test_critic to fix-bug/implement-feature | Catches weak tests before PR | Gap 4 |
| P2 | Reduce scheduled frequencies | Saves ~14 vessel-slots/day | 3.5 |
| P2 | Create ci-watchdog workflow | Closes post-merge monitoring gap | 7.1 |
| P3 | Scope continuous-improvement prompt | Prevents over-scoped refactors | 5.4 |
| P3 | Add go vet to continuous-simplicity/improvement gates | Catches suspicious constructs | 4.4 |

**Projected impact of P0 fixes alone:** Failure rate drops from 57.5% to ~20%, effective throughput doubles from ~1 to ~2 completed vessels/hour, and the diagnose→file→retry self-healing loop becomes operational for the first time.
