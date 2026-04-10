Turn the continuous-style findings into a human-readable markdown report.

Read:

1. `.xylem/state/continuous-style/findings.json`

Write:

`.xylem/state/continuous-style/report.md`

The report must contain:

1. a short executive summary of the current terminal-output health
2. a section on what already looks consistent or operator-friendly
3. a ranked section of the findings to address next
4. file-path-level evidence and concrete follow-up directions for each finding
5. a clear note when the run found no worthwhile issues

Keep the report concrete and grounded in xylem's current CLI surfaces. Avoid generic terminal-UI advice that is not tied to this repository.

After writing the report, print a brief confirmation with the top findings or `no findings`.
