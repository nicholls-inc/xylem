Analyze the last 7 daily metrics reports to identify week-over-week trends.

## Context

The `collect` phase has written a list of available daily report files to:

```
.xylem/state/metrics/portfolio-inputs.txt
```

Each line is a path to a daily `YYYY-MM-DD-report.md` file.

## Your Task

1. Read `.xylem/state/metrics/portfolio-inputs.txt` to get the list of report files.
2. Read each report file listed.
3. Extract the following time-series data across reports (oldest to newest):
   - Completion rate (%)
   - Failure rate (%)
   - Total vessels processed
   - Average run duration (seconds, if available)
   - Top failing phases

4. Identify trends and patterns:
   - Is the failure rate increasing or decreasing?
   - Are any workflows consistently failing more than others?
   - Are specific phases responsible for most failures?
   - Is throughput (total vessels/day) trending up or down?
   - Are there any days with unusually high failure spikes?

5. Write your analysis to `.xylem/state/metrics/portfolio-analysis.md` with:
   - A summary of the trend direction for each key metric
   - The top 3 concerns (if any) ranked by severity
   - Any patterns that warrant a GitHub issue

If there are fewer than 3 daily reports available, note the limited data window and proceed with whatever is available.

After writing, print: `Analysis written: .xylem/state/metrics/portfolio-analysis.md`.
