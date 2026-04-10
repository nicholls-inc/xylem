# Field Report Aggregation

You are analyzing anonymized field reports from productized xylem installations.
Each report is a JSON object containing aggregate vessel statistics from a single
deployment over a weekly window.

## Input

Read the parsed field reports from `.xylem/state/field-reports/parsed/`. Each line
is a JSON object with `issue_number` and `report` fields. The `report` contains:

- `version`: schema version (currently 1)
- `total_vessels`: number of vessels in the reporting window
- `fleet`: healthy/degraded/unhealthy counts
- `workflows[]`: per-workflow success/failure/cost/duration stats
- `cost_digest`: aggregate token and cost statistics
- `recovery_digest[]`: recovery classification distribution
- `failure_patterns[]`: (extended only) recurring anomaly codes

## Task

1. Read all parsed reports from the latest `.jsonl` file
2. Produce a cross-deployment aggregate that identifies:
   - **Workflow success rates**: which workflows have the highest failure rates across installations?
   - **Cost outliers**: which workflows are disproportionately expensive?
   - **Common failure patterns**: are the same anomaly codes appearing across multiple installations?
   - **Fleet health trends**: what proportion of installations are healthy vs degraded/unhealthy?
   - **Profile version adoption**: are users on older profile versions?

3. If any pattern warrants a xylem improvement, produce an `issues-to-file.json` at
   `.xylem/state/field-reports/issues-to-file.json` with the following format:

```json
[
  {
    "title": "[field] <concise description of the issue>",
    "body": "## Field Report Signal\n\n<evidence from aggregated reports>\n\n## Proposed Fix\n\n<what should change in xylem>",
    "labels": ["field-report", "enhancement", "ready-for-work"]
  }
]
```

4. Write the aggregate summary to `.xylem/state/field-reports/aggregated/<date>.json`

## Filing Criteria

Only file issues when:
- A workflow fails at >30% rate across 3+ installations
- A specific anomaly code appears in 50%+ of reports
- Cost per vessel exceeds $2 median across 3+ installations
- A pattern suggests a systemic xylem bug (not a user configuration issue)

Do NOT file issues for:
- Low sample sizes (fewer than 3 reports)
- Expected variation in success rates
- User-specific configuration problems
- Issues that already exist (check with `gh issue list --search`)

If no issues meet the filing criteria, output `XYLEM_NOOP`.

## Output Contract

Print one of:
- `XYLEM_NOOP` — no actionable patterns found
- `ISSUES_FILED: <count>` — number of issues written to issues-to-file.json
