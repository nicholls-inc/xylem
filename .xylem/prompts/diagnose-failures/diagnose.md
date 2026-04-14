Diagnose xylem vessel failures in this repository.

## Your Task

## Scope Constraints

To keep this analysis focused and completable within the turn budget:

- Analyze at most the **3 most recent** failed vessels within the cutoff window
- **Exclude** vessels from the `diagnose-failures` and `backlog-refinement` workflows — these have known recurring issues tracked separately and analyzing them creates a self-referential loop
- For each qualifying vessel, read only the **last phase output** (the phase where execution stopped), not all phase outputs. The last phase output contains the failure information.
- If no qualifying failures exist after applying these filters, output `XYLEM_NOOP`

<!-- CONFIGURE: Change the number below to control how far back to look for failures -->
Only consider vessels that failed within the last **24 hours**. Check each vessel's `created_at`
field in `.xylem/queue.jsonl` against today's date and skip vessels older than the cutoff.

Read `.xylem/queue.jsonl` to find all vessels within the cutoff window that are in `failed`,
`timed_out`, or `cancelled` states.

For each qualifying vessel:

1. Read its phase outputs under `.xylem/phases/<vessel-id>/` to understand what went wrong.
2. Note the vessel's `created_at` timestamp.
3. Run `git log --oneline --after="<created_at>" -- cli/` to check if the relevant code was
   changed after the vessel failed. If there are commits touching the affected package since
   the failure, mark the vessel as **potentially stale** — the problem may already be fixed.

## Output

Produce a structured failure report. Exclude potentially stale failures from the main report
and list them separately at the end.

For each **active** failure pattern:

- **Pattern name**: A short label for this failure class
- **Affected vessels**: Vessel IDs, workflow names, and failure timestamps
- **Phase where it failed**: Which phase and what the error was
- **Root cause**: What went wrong, with evidence from the phase output
- **Severity**: High (blocks this workflow class entirely) / Medium (intermittent) / Low (edge case)
- **Recommended fix**: Bug fix, config change, prompt improvement, or documentation update

Group vessels sharing the same root cause under one pattern. If a vessel has a unique failure,
treat it as its own pattern.

List **potentially stale** failures at the end under a "Stale (possibly already fixed)" section
with vessel IDs and which commits may have addressed them.

Do not modify any files. This is a read-only diagnostic phase.
