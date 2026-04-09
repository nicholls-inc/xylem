Turn the current SoTA snapshot and deterministic delta into a human-readable markdown report.

Read these files:

1. `.xylem/state/sota-gap-snapshot.json`
2. `.xylem/state/sota-gap-delta.json`

Write the report to:

`.xylem/state/sota-gap-report.md`

The report must contain:

1. A short executive summary with current counts for `wired`, `dormant`, and `not-implemented`
2. A section for improvements since the previous snapshot
3. A section for regressions / newly surfaced gaps
4. A ranked list of the highest-value gaps to close next, ordered by priority
5. Per-layer observations that help an operator see whether the primary CLI path is converging on the reference docs

Be concrete. Cite capability keys and file paths from the snapshot/delta rather than rewriting generic advice.

After writing the report, print a brief confirmation with the top three ranked gaps.
