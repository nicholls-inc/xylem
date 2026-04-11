Review the xylem GitHub backlog and prepare a conservative issue-hygiene action plan.

## Inputs

Read these files first:

1. `.xylem/state/backlog-refinement/open-issues.json`
2. `.xylem/state/backlog-refinement/merged-prs.json`
3. `.xylem/state/backlog-refinement/labels.json`

The open-issues snapshot is the primary backlog input. The merged-PR snapshot is there so you
can detect open issues that appear to have been resolved by a merged pull request. The labels
snapshot tells you which labels already exist in the repository.

## Goal

Produce a **conservative** refinement pass that improves backlog hygiene without closing issues or
removing labels.

You may use `gh issue view <number> --repo nicholls-inc/xylem --json ...` for targeted follow-up
checks when the snapshots are insufficient, especially to:

- confirm whether dependency issues are still open
- inspect issue comments before posting a new cross-reference
- verify that a merged PR truly references an issue with a `closes` / `fixes` style keyword

Keep follow-up API calls focused. Prefer the snapshots over re-querying the full backlog.

## Allowed actions

You may plan only these mutations:

1. **Add labels** that already exist in the repository
2. **Post comments** on issues

Do **not** plan any of the following:

- issue closure
- label removal
- body rewrites
- milestone/assignee changes
- creation of new repository labels

## Heuristics

Build a lightweight dependency view from issue bodies. Treat `Depends On`, `Blocked By`, and direct
issue references as dependency signals when the meaning is clear.

Plan actions only when confidence is high:

1. **Ready for work**
   - If an open issue is missing `ready-for-work`, and every dependency appears resolved, and the
     issue description is concrete enough to act on, add `ready-for-work`.
   - If the issue is still labeled `blocked`, do not remove that label. Add a comment explaining
     that dependencies appear resolved and that a human should confirm the issue is ready.

2. **Blocked labeling**
   - If an issue clearly depends on an open issue and is missing `blocked`, add `blocked`.

3. **Priority labels**
   - If the repository already contains `priority-high`, `priority-medium`, or `priority-low`,
     you may add **at most one** of them.
   - Prefer `priority-high` only when the issue blocks multiple open issues or is clearly on the
     critical path for active harness work.
   - Prefer `priority-medium` for single-blocker issues with active downstream work.
   - Prefer `priority-low` only when the signal is strong and additive value is clear.
   - If those labels do not exist, record the priority insight in the summary instead of inventing
     labels or creating substitute labels.

4. **Merged-PR closure suggestions**
   - If a merged PR clearly references an open issue with `closes #N`, `fixes #N`, or similar,
     do not close the issue yourself.
   - Post a comment on the issue that links the merged PR and recommends human review for closure.

5. **Duplicate linking**
   - Only act on duplicates when confidence is high.
   - Add comments on both issues with cross-references and a short explanation of the overlap.
   - Before planning the comment, inspect current issue comments and skip the action if the same
     cross-link already exists.

## Action budget

Keep the run small and reviewable:

- no more than **12** issue actions total
- no more than **1** planned comment per issue unless duplicate cross-linking requires one on each side

If there are more candidates, choose the highest-signal ones and list the remainder in the summary.

## Output files

Write both of these files:

1. `.xylem/state/backlog-refinement/actions.json`
2. `.xylem/state/backlog-refinement/summary.md`

### `actions.json` contract

Write valid JSON with this exact shape:

```json
{
  "version": 1,
  "generated_at": "RFC3339",
  "actions": [
    {
      "issue_number": 123,
      "kind": "ready-for-work|blocked|priority|closure-review|duplicate-link",
      "reason": "one sentence",
      "confidence": 0.0,
      "add_labels": ["ready-for-work"],
      "comment": "Markdown comment or empty string"
    }
  ]
}
```

Rules:

- `add_labels` must contain only labels that already exist and are not already on the issue
- `comment` may be empty, but never null
- `confidence` must be between `0` and `1`
- if there are no safe actions, still write a valid file with `"actions": []`

### `summary.md` contract

Write concise markdown with these sections:

```markdown
# Backlog Refinement Summary

## Snapshot
- Open issues scanned: N
- Merged PRs scanned: N
- Planned actions: N

## Planned actions
- `#123` — add `ready-for-work` because ...

## Priority insights
- ...

## Follow-up recommended
- ...

## Skipped for caution
- ...
```

The summary should explain what was acted on, what was intentionally left alone, and any backlog
items that look important but were too ambiguous for automation.

After writing both files, print one line:

`Backlog refinement plan written: .xylem/state/backlog-refinement/actions.json`
