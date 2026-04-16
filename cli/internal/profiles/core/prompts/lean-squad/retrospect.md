You are running the `retrospect` phase of the `lean-squad` tick coordinator.

🔬 **Lean Squad tick retrospective.** This is the last phase of an 8-hour tick. You record what
this tick did so future ticks can make better decisions.

- Vessel ID: {{.Vessel.ID}}
- Tick ref: {{.Vessel.Ref}}
- Fired at: {{index .Vessel.Meta "schedule.fired_at"}}
- Dispatch log: {{.PreviousOutputs.dispatch}}
- Assess-state output: {{.PreviousOutputs.assess_state}}
- Repository: {{.Repo}}

## What you do, concretely

1. **Ensure `formal-verification/repo-memory.json` exists.** If the file is missing (this should
   only happen when bootstrap is pending), create it with a seed:
   ```json
   {"phase": "orient", "targets": [], "runs": []}
   ```
2. **Append a new entry to `runs[]`.** Parse the `PLAN:` lines from the `assess_state` output
   above and the `DISPATCHED:` lines from the `dispatch` output above. Build one entry:
   ```json
   {
     "timestamp": "<iso-8601 UTC from schedule.fired_at>",
     "vessel_id": "<this vessel id>",
     "gate_signal": "<short description of which opt-in triggered>",
     "bootstrap_pending": <true|false, derived from previous outputs>,
     "plan": [
       {"task": "<task-slug>", "target": "<target-slug>"},
       {"task": "<task-slug>", "target": "<target-slug>"}
     ],
     "dispatched": [
       {"workflow": "lean-squad-<task>", "target": "<slug>", "vessel_id": "<id>"},
       ...
     ],
     "notes": "<optional 1-sentence summary>"
   }
   ```
   Include the always-on `report` and `status` dispatches in `dispatched[]`.
3. **Update `runs[]` in-place.** Read the existing JSON, append this new entry to the end of
   `runs[]`, write the file back with `jq` or a short Python snippet. Keep the rest of the file
   untouched. Do not edit `targets[]` or `phase` — other workflows own those fields.
4. **Open a PR.** Create a branch, commit the memory update, push, and open a PR:
   - Branch name: `lean-squad/tick-<bucket>` where `<bucket>` is the UTC hour bucket used by
     the dispatch phase (format `YYYYMMDD-HH`, e.g. `20260416-18`). Derive it from the fired_at
     time or run `date -u +%Y%m%d-%H` if that matches closely enough.
   - Commit message: `chore(lean-squad): tick retrospective <bucket>`
   - PR title: `[Lean Squad] Tick <bucket>`
   - PR body: list the two selected tasks, any bootstrap state, and the full dispatch log
     quoted in a collapsible `<details>` block. Tag with 🔬 at the top.
   - PR labels: `lean-squad`
   - If the branch already exists from an earlier tick in the same hour bucket (very unlikely
     at 8h cadence, but possible if a retry fires), append a `-retry<N>` suffix.

## Safety rules

- Do **NOT** edit `formal-verification/TARGETS.md`, `RESEARCH.md`, `CORRESPONDENCE.md`,
  `CRITIQUE.md`, `REPORT.md`, or any file under `formal-verification/lean/` or
  `formal-verification/specs/`. Other workflows are the sole writers of those files.
- Do **NOT** merge PRs in this phase. Merges happen in `merge_open_prs` earlier in the tick.
- If JSON parsing of `repo-memory.json` fails, DO NOT overwrite the file. Log the error with a
  clear message and exit the phase gracefully — a subsequent tick will retry.

## Deliverable

Output the git operations you ran, the resulting PR URL, and end with a single line:

```
RESULT: <pr-url>
```

If you can't open a PR (e.g. no changes to commit because memory update was a no-op), end with:

```
RESULT: none — <reason>
```
