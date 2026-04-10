Review the current xylem state and determine whether a weekly workflow-health issue should be opened or updated.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

Inspect recent queue state, failure-review artifacts, and summary files under `.xylem/state/` and `.xylem/phases/` if they exist.

If a fresh open weekly health issue already exists, output the exact standalone line `XYLEM_NOOP` and explain why no new report is needed.

Otherwise, produce a concise outline covering:

1. Failure hotspots by workflow
2. Retry and timeout patterns
3. Notable cost or throughput anomalies
4. Recommended operator follow-ups
