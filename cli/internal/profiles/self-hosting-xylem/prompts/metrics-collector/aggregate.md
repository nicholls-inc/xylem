Aggregate today's raw vessel metrics into a structured daily report.

## Context

The `collect` phase has written today's raw vessel snapshot to:

```
.xylem/state/metrics/{{ now | date "2006-01-02" }}-raw.jsonl
```

Each line is a JSONL record representing one vessel from `.xylem/queue.jsonl`.

## Your Task

1. Read the raw snapshot file (use `date -u +%Y-%m-%d` to determine today's date if needed).
2. Compute the following metrics:
   - **Total vessels** in the snapshot
   - **Per-state counts**: `pending`, `running`, `completed`, `failed`, `timed_out`, `waiting`, `cancelled`
   - **Completion rate**: completed / (completed + failed + timed_out + cancelled) × 100%
   - **Failure rate**: (failed + timed_out) / total × 100%
   - **Average run duration** (seconds) for completed vessels, using `created_at` and the last phase timestamp if available
   - **Workflow breakdown**: count of vessels per workflow name
   - **Phase-level failure distribution**: which phases produced the most failures (read `.xylem/phases/<vessel-id>/` for failed vessels to identify the failing phase name)
3. Write a markdown report to:

```
.xylem/state/metrics/YYYY-MM-DD-report.md
```

where `YYYY-MM-DD` is today's UTC date.

## Report Format

```markdown
# Metrics Report: YYYY-MM-DD

## Summary
- Total vessels: N
- Completed: N (X%)
- Failed: N (X%)
- Timed out: N (X%)
- Cancelled: N (X%)
- Pending: N
- Running: N

## Completion Rate
X%

## Failure Rate
X%

## Average Run Duration (completed vessels)
Xs

## Workflow Breakdown
| Workflow | Count |
|---|---|
| workflow-name | N |

## Phase Failure Distribution
| Phase | Failures |
|---|---|
| phase-name | N |

## Notes
Any notable patterns or anomalies.
```

After writing the report, print a one-line summary: `Report written: .xylem/state/metrics/YYYY-MM-DD-report.md (N vessels)`.
