Perform a comprehensive autonomy and velocity review of xylem's self-building capabilities.

Repository: {{.Repo.Slug}}
Date: {{.Date}}

## Step 1 — Read configuration and workflow definitions

Read these files:

1. `.xylem.yml` (full config: sources, concurrency, timeouts, scheduling)
2. All workflow files: `ls .xylem/workflows/*.yaml` then read each one
3. `.xylem/HARNESS.md`

Understand the full set of configured sources, workflows, phases, gates, and scheduling intervals.

## Step 2 — Read operational data

1. **Queue history**: Read the last 50 entries from `.xylem/state/queue.jsonl` (use `tail -50`).
   Count total entries, then check: if fewer than 10 entries have timestamps after the last
   autonomy review (or in the past 7 days if no prior review exists), output `XYLEM_NOOP` and stop.

2. **Recent vessel outputs**: Find the 5 most recently modified directories under
   `.xylem/state/phases/` (use `ls -t | head -5`). For each, read all `.output` files to
   understand what work was attempted and how it concluded.

## Step 3 — Analyze and evaluate

### Workflow coverage

Map the full self-building lifecycle: issue discovery, triage, refinement, implementation,
testing, PR creation, review, merge, post-merge follow-up, failure diagnosis, and recovery.

- Identify dead ends where work stalls with no next step
- Identify missing transitions (e.g., a workflow produces output but nothing consumes it)
- Check the failure-to-recovery loop: do failed vessels get diagnosed and re-attempted?
- Check whether all issue label states have a corresponding source+workflow path

### Throughput

From the queue data, calculate:

- Completion rate (completed / total vessels in the window)
- Failure rate (failed + timed_out / total)
- Average duration for completed vessels
- Vessels per day

Identify bottlenecks:

- Is `concurrency.global` limiting throughput?
- Are `max_turns` values causing unnecessary timeouts?
- Are scheduling intervals too frequent or too sparse?
- Are gate retries causing cascading delays?

### Quality gates

- Are gates applied consistently across similar workflows?
- Are test/smoke phases present where code changes happen?
- Is rebase-before-push applied universally to PR-producing workflows?
- Are gates strict enough to prevent low-quality merges?

### Prompt quality

For each workflow's prompt files, check:

- Specificity: are instructions concrete enough for autonomous execution?
- Phase handoff: do later phases reference `.PreviousOutputs.<key>` to receive earlier results?
- Gate retry feedback: do prompts use `.GateResult` where gates exist?
- Scope creep protection: do prompts define clear boundaries on what to change?
- Output contracts: do prompts specify exact file paths and formats for outputs?

### Failure patterns

From the recent vessel outputs and queue data:

- Group failures by root cause category (scope too broad, missing context, gate failure, timeout, tool error)
- Identify self-referential loops (e.g., a fix workflow that keeps failing on the same issue)
- Find recurring patterns that should be encoded as workflow improvements or lessons

## Step 4 — Write findings

Create the state directory and write findings:

```bash
mkdir -p .xylem/state/autonomy-review
```

Write `.xylem/state/autonomy-review/findings.json` with this structure:

```json
{
  "version": 1,
  "generated_at": "RFC3339 timestamp",
  "repo": "{{.Repo.Slug}}",
  "window": {
    "start": "RFC3339",
    "end": "RFC3339",
    "vessels_total": 0,
    "vessels_completed": 0,
    "vessels_failed": 0,
    "vessels_timed_out": 0,
    "completion_rate": 0.0,
    "failure_rate": 0.0,
    "avg_duration_minutes": 0.0,
    "vessels_per_day": 0.0
  },
  "coverage_gaps": [
    {
      "gap": "description of the gap",
      "impact": "high|medium|low",
      "suggested_fix": "what to change"
    }
  ],
  "throughput_recommendations": [
    {
      "parameter": "concurrency.global|max_turns|timeout|schedule",
      "current_value": "...",
      "suggested_value": "...",
      "reason": "..."
    }
  ],
  "quality_gate_issues": [
    {
      "workflow": "workflow name",
      "issue": "description",
      "suggestion": "what to add or change"
    }
  ],
  "prompt_issues": [
    {
      "file": "path to prompt file",
      "issue": "description",
      "suggestion": "specific change"
    }
  ],
  "failure_patterns": [
    {
      "pattern": "description",
      "count": 0,
      "root_cause": "category",
      "mitigation": "suggested fix"
    }
  ]
}
```

Also print a human-readable summary of the key findings to stdout.
