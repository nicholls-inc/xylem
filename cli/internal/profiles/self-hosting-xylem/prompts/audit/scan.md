Scan the xylem queue for failed vessels and identify systemic failure patterns.

## Your Task

Read `.xylem/queue.jsonl` to find all vessels in `failed`, `timed_out`, or `cancelled` states
within the last **48 hours**. Check each vessel's `created_at` field and skip vessels older
than the cutoff.

For each qualifying vessel:

1. Read its phase outputs under `.xylem/phases/<vessel-id>/` to understand what went wrong.
2. Identify the failing phase name and the error message or symptom.
3. Note the workflow name and vessel ID.

## Output

Group vessels by failure pattern (same root cause = same pattern). Write a structured scan
report to `.xylem/state/audit/scan.md`.

Create `.xylem/state/audit/` if it does not exist.

For each **failure pattern**:

- **Pattern key**: short snake_case identifier (e.g. `gate_timeout`, `prompt_file_missing`)
- **Affected vessels**: vessel IDs, workflow names, failure timestamps
- **Failing phase**: which phase name failed
- **Symptom**: the error text or observable behavior
- **Frequency**: how many vessels share this pattern
- **Severity**: high (blocks all vessels in this class) / medium (intermittent) / low (edge case)

If no qualifying failures are found, write a brief "No active failures in the last 48 hours" report
and exit — do not proceed to the next phase by writing an empty patterns list.

After writing, print: `Scan complete: .xylem/state/audit/scan.md (N patterns, M vessels)`.
