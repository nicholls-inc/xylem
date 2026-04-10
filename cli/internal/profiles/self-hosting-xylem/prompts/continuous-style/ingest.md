Prepare the scheduled continuous-style run for xylem.

Target the CLI's user-facing terminal output surfaces:

1. `cli/cmd/xylem/`
2. `cli/internal/reporter/`
3. `cli/internal/review/`
4. `cli/internal/lessons/`
5. Any directly related helpers those packages rely on for user-facing output

Your goal in this phase is to produce a factual inventory that the next phase can convert into a stable findings file.

The inventory must identify:

1. the concrete files and commands that emit user-facing stdout or stderr
2. which surfaces are primarily tabular, status-oriented, markdown/report-oriented, or warning/error-oriented
3. where the current output already looks consistent and operator-friendly
4. where the output feels inconsistent, noisy, poorly routed, or hard to scan
5. the exact file paths and line ranges that support each observation

Stay grounded in this repository's conventions. Do not assume xylem wants a full terminal UI framework; just inventory the current output patterns and the most meaningful rough edges.
