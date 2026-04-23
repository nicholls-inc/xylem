# 07: Workflow YAML Amendments (PR 2 — Human-Authored)

This file contains the exact diffs to apply to the three protected workflow YAMLs for roadmap item #07.

**Why human-authored:** `.xylem/workflows/*.yaml` are protected control surfaces per `.claude/rules/protected-surfaces.md`. Amendments require a human-authored commit with a governance note.

**Governance note for the commit message:**
```
feat(assurance): wire intent-check phase into core delivery workflows (#07)

Governance amendment: adding intent_check command phase to fix-bug,
implement-feature, and implement-harness workflows per roadmap item #07
(docs/assurance/next/07-intent-check-phase.md). Phase runs xylem-intent-check
after the verify phase and before pr_draft. Fails closed if protected-surface
files changed but xylem-intent-check binary is unavailable or returns fail.
```

---

## Changes to apply

### `.xylem/workflows/fix-bug.yaml`

Insert after the `verify` phase (after line 53), before `pr_draft`:

```yaml
  # intent-check: roadmap #07 — governance amendment <date>
  - name: intent_check
    type: command
    run: |
      set -euo pipefail
      if ! command -v xylem-intent-check > /dev/null 2>&1; then
        echo "intent-check: xylem-intent-check not in PATH — install: cd cli && go install ./cmd/xylem-intent-check" >&2
        exit 1
      fi
      xylem-intent-check
```

### `.xylem/workflows/implement-feature.yaml`

Same insertion point (after `verify`, before `pr_draft`):

```yaml
  # intent-check: roadmap #07 — governance amendment <date>
  - name: intent_check
    type: command
    run: |
      set -euo pipefail
      if ! command -v xylem-intent-check > /dev/null 2>&1; then
        echo "intent-check: xylem-intent-check not in PATH — install: cd cli && go install ./cmd/xylem-intent-check" >&2
        exit 1
      fi
      xylem-intent-check
```

### `.xylem/workflows/implement-harness.yaml`

Insert after the `smoke` phase (after line 50), before `pr_draft`:

```yaml
  # intent-check: roadmap #07 — governance amendment <date>
  - name: intent_check
    type: command
    run: |
      set -euo pipefail
      if ! command -v xylem-intent-check > /dev/null 2>&1; then
        echo "intent-check: xylem-intent-check not in PATH — install: cd cli && go install ./cmd/xylem-intent-check" >&2
        exit 1
      fi
      xylem-intent-check
```

---

## Prerequisite

`xylem-intent-check` must be installed before the workflow phases run. In CI worktrees, the binary needs to be built and placed in PATH. Add to `.github/workflows/ci.yml` paths trigger and document in CI setup if running these workflows in CI.

The binary is already at `cli/cmd/xylem-intent-check/main.go` and can be installed with:
```bash
cd cli && go install ./cmd/xylem-intent-check
```

---

## How to land PR 2

1. Apply the three diffs above to the workflow YAMLs.
2. Commit with the governance note above as the commit message body.
3. Open the PR with label `governance-amendment` and reference this doc + PR 1.
4. After merge, enable the kill criterion tracker: create `docs/assurance/next/07-fp-tracker.csv` with headers `date,invariant_touched,phase_verdict,human_verdict`.
