# CLI Reference

Complete reference for every `xylem` command. Each section documents the command's purpose, usage syntax, flags, behavior, and practical examples.

## Global Flags

These flags are available on every command (except `init`, which skips config loading).

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config` | `string` | `.xylem.yml` | Path to the xylem configuration file. Also configurable via the `XYLEM_CONFIG` environment variable. |

Before any command runs (except `init`), xylem performs the following checks:

1. Verifies that `git` is on PATH.
2. Loads and validates the config file at the `--config` path.
3. If any configured source has `type: github`, verifies that `gh` is on PATH.
4. Initializes the JSONL queue at `<state_dir>/queue.jsonl` and the worktree manager.

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success. |
| `1` | General error, or `drain` completed with at least one failed vessel. |
| `2` | Infrastructure error during `drain` (queue read failure, fatal drain error). |

When a command fails, the error message is printed to stderr.

---

## xylem init

Bootstrap the configuration file, state directory, workflow definitions, and prompt templates.

### Usage

```
xylem init [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--force` | `bool` | `false` | Overwrite existing files. Without this flag, existing files are skipped. |

### Behavior

1. Writes a scaffold `.xylem.yml` (or the path specified by `--config`). Auto-detects the GitHub remote from `git remote get-url origin` and pre-fills the `repo` field.
2. Creates the `.xylem/` state directory if it does not exist.
3. Writes `.xylem/.gitignore` (always overwritten) with contents that ignore everything except the `.gitignore` itself.
4. Creates `.xylem/HARNESS.md` -- a template for project-specific instructions appended to the session runner's system prompt.
5. Creates workflow definitions: `.xylem/workflows/fix-bug.yaml` and `.xylem/workflows/implement-feature.yaml`.
6. Creates prompt templates for each workflow phase (`analyze`, `plan`, `implement`, `pr`) under `.xylem/prompts/<workflow>/<phase>.md`.

When `--force` is not set, each file is skipped if it already exists, with a message indicating the skip.

After running, `init` prints next steps guiding you to edit the config, customize the harness, and run your first scan.

### Examples

```bash
# First-time setup in a new repo
xylem init

# Re-scaffold everything, overwriting existing files
xylem init --force

# Use a custom config path
xylem init --config my-config.yml
```

---

## xylem dtu

Manage Digital Twin Universe manifests, materialized state, and runtime wiring for offline scenario execution.

The `dtu` family focuses on the shared CLI glue around the twin runtime:

- `load` validates a manifest and writes normalized DTU state to `<state_dir>/dtu/<universe>/state.json`
- `materialize` creates the universe runtime directories (`runtime`, `workdir`, shim dir) and installs `gh`, `git`, `claude`, and `copilot` wrappers into the shim dir
- `env` prints the environment variables needed by xylem and DTU shims
- `run` re-executes `xylem` with those DTU environment variables applied
- `verify` executes env-gated live differentials and canaries, comparing live output to DTU shim output where applicable

The `dtu` subcommands themselves do not require a normal `.xylem.yml`. The command re-executed by `xylem dtu run` still does, so point `--workdir` at a repo that already has `.xylem.yml` and `.xylem/workflows/`, or populate the materialized workdir first.

Example manifests used by this repository's DTU tests live under `cli/internal/dtu/testdata/*.yaml`. Outside this repo, pass the path to your own DTU manifest.

For testing workflows and trust boundaries, see:

- [DTU Guide 4A: Fixture-Backed Regression Tests](dtu-fixture-regression-tests.md)
- [DTU Guide 4B: Manual Smoke Tests Under DTU](dtu-manual-smoke-tests.md)

### Usage

```bash
xylem dtu <subcommand> [flags]
```

### Shared flags

| Flag | Type | Description |
|------|------|-------------|
| `--manifest` | `string` | Path to the DTU manifest YAML file. Required for all current subcommands. |
| `--universe` | `string` | Override the universe ID. Defaults to a sanitized form of `metadata.name`. |
| `--state-dir` | `string` | Override the state directory. Defaults to `.xylem` or the configured `state_dir`. |
| `--shim-dir` | `string` | Override the shim directory prepended to `PATH`. Defaults to `<state_dir>/dtu/shims`. |
| `--workdir` | `string` | Override the materialized working directory. Defaults to `<state_dir>/dtu/<universe>/workdir`. |

### `xylem dtu verify`

```bash
xylem dtu verify
xylem dtu verify --verify-workdir "$PWD"
xylem dtu verify --suite cli/internal/dtu/testdata/live-verification.yaml
```

Runs the env-gated executable DTU verification suite. For each enabled differential case, xylem:

1. runs the declared live command directly,
2. materializes the matching DTU fixture,
3. runs the equivalent twin command through `xylem shim-dispatch`,
4. applies the declared normalizer from `internal/dtu/normalizers.go`, and
5. reports any normalized mismatch with divergence-registry and attribution-policy context.

Enabled canaries run live only. They act as drift alarms and do not perform twin-vs-live comparison.

By default, `verify` loads:

- `cli/internal/dtu/testdata/live-verification.yaml`
- `cli/internal/dtu/testdata/divergence-registry.yaml`
- `cli/internal/dtu/testdata/attribution-policy.yaml`

The command prints a summary plus per-case details. It exits non-zero if any mismatch, drift alarm, or execution/normalization error occurs.

The checked-in suite currently contains six differential cases (three `gh`, one `git ls-remote`, and two provider-process-shape checks for `claude`/`copilot`) plus four canaries (`gh`, `git`, `claude`, `copilot`). `XYLEM_DTU_LIVE_CANARY=1` enables the canaries. `XYLEM_DTU_LIVE_GH_DIFFERENTIAL=1`, `XYLEM_DTU_LIVE_GIT_DIFFERENTIAL=1`, and `XYLEM_DTU_LIVE_PROVIDER_DIFFERENTIAL=1` enable the current differential groups.

### `xylem dtu load`

```bash
xylem dtu load --manifest /path/to/universe.yaml
xylem dtu load --manifest cli/internal/dtu/testdata/issue-label-gate.yaml
```

Loads the manifest, validates it through `internal/dtu`, and writes normalized DTU state. This is useful when you want to inspect or pre-seed state without running anything yet.

### `xylem dtu materialize`

```bash
xylem dtu materialize --manifest /path/to/universe.yaml
```

Does everything `load` does, then ensures the runtime directories exist and installs runnable `gh`, `git`, `claude`, and `copilot` shim wrappers into the shim directory. Those wrappers dispatch back through the current `xylem` binary so the DTU runtime stays portable without checking in prebuilt helper binaries.

### `xylem dtu env`

```bash
xylem dtu env --manifest /path/to/universe.yaml
xylem dtu env --manifest /path/to/universe.yaml --shell=false
```

Prints:

- `XYLEM_DTU_UNIVERSE_ID`
- `XYLEM_DTU_STATE_PATH`
- `XYLEM_DTU_STATE_DIR`
- `XYLEM_DTU_MANIFEST`
- `XYLEM_DTU_EVENT_LOG_PATH`
- `XYLEM_DTU_WORKDIR`
- `XYLEM_DTU_SHIM_DIR`
- `PATH` with the shim dir prepended

By default, `env` prints `export ...` lines to stdout for shell evaluation. Use `--shell=false` to print raw `KEY=VALUE` lines instead.

### `xylem dtu run`

```bash
xylem dtu run --manifest /path/to/universe.yaml --workdir "$PWD" -- scan
xylem dtu run --manifest /path/to/universe.yaml --workdir "$PWD" -- drain
xylem dtu run --manifest /path/to/universe.yaml --workdir "$PWD" -- scan --dry-run
```

Materializes DTU state and then re-runs the current `xylem` binary with the provided subcommand arguments under the DTU environment. The inner command runs from `--workdir` (default: `<state_dir>/dtu/<universe>/workdir`) and still performs normal xylem config/workflow loading, so use a workdir that already contains `.xylem.yml` and `.xylem/workflows/` when you want to run `scan`, `drain`, or `daemon`.

---

## xylem scan

Query all configured sources for actionable tasks and enqueue new vessels.

### Usage

```
xylem scan [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--dry-run` | `bool` | `false` | Preview candidates without writing to the queue. |

### Behavior

- Iterates over every source defined in `.xylem.yml` and calls its `Scan()` method.
- Supported source types are `github`, `github-pr`, `github-pr-events`, `github-merge`, `schedule`, and `scheduled`.
- `github` scans open issues matching task labels.
- `github-pr` scans open pull requests matching task labels.
- `github-pr-events` scans open pull requests for configured `on` triggers such as labels, submitted reviews, failed checks, and comments.
- `github-merge` scans merged pull requests and dedupes by merge commit SHA.
- `schedule` emits a synthetic vessel when its configured cadence elapses and persists the last-fired timestamp under `<state_dir>/state/schedule.json`.
- `scheduled` enqueues one vessel per task per schedule window (`@weekly`, `24h`, etc.) and persists per-task buckets under `<state_dir>/schedules/` so repeated scans do not duplicate work.
- If scanning is paused (via `xylem pause`), prints a message and exits without scanning.
- Deduplication is handled automatically. Depending on the source, xylem skips refs that are already present in the queue, already present in any vessel state, or already have xylem-owned branches/open PRs.

In `--dry-run` mode, scan writes candidates to a temporary queue, prints a table of what would be enqueued (ID, Source, Workflow, Ref), and then discards the temporary queue. No changes are made to the real queue.

### Output

Normal mode:

```
Added 3 vessels, skipped 2
```

Dry-run mode:

```
ID              Source          Workflow              Ref
----            ------          -----                 ---
issue-42        github-issue    fix-bug               https://github.com/owner/repo/issues/42
issue-55        github-issue    implement-feature     https://github.com/owner/repo/issues/55

2 candidate(s) would be queued (dry-run -- no changes made)
```

### Examples

```bash
# Preview what would be queued
xylem scan --dry-run

# Scan and enqueue
xylem scan

# Scan with a non-default config
xylem scan --config production.yml
```

---

## xylem drain

Dequeue pending vessels and launch sessions in isolated git worktrees.

### Usage

```
xylem drain [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--dry-run` | `bool` | `false` | Preview pending vessels and the commands that would run, without launching sessions. |

### Behavior

1. Before draining pending vessels, checks any vessels in the `waiting` state (e.g., waiting for a label gate) and resumes them if their condition is now met.
2. Dequeues pending vessels in FIFO order, up to the `concurrency` limit.
3. For each vessel:
   - Calls `source.OnStart()` to perform side effects (for example, applying configured status labels).
   - Creates an isolated git worktree via `worktree.Create`.
   - Executes workflow phases sequentially in the worktree. Prompt phases use the resolved provider (`claude` or `copilot`), and command phases run shell commands directly.
   - Runs quality gates between phases (command gates with retries, label gates with polling).
4. Marks vessels as `completed`, `failed`, `waiting`, or `timed_out` based on outcome.
5. Executes any built-in scheduled vessels already sitting in the queue (for example, `context-weight-audit` and `harness-gap-analysis`) without creating a worktree or launching an LLM session.
6. If `harness.review` is configured for automatic cadence, regenerates the latest harness review as a best-effort post-drain step.
7. Prints a summary line and exits.

**Graceful shutdown**: `drain` handles `SIGINT` and `SIGTERM`. When a signal is received, running sessions are allowed to finish, but no new pending vessels are started.

**Exit codes**: Returns exit code `1` if any vessels failed during the drain. Returns exit code `2` for infrastructure errors (e.g., queue read failure).

In `--dry-run` mode, lists pending vessels with a command preview, without launching any sessions.

### Output

Normal mode:

```
Completed 2, failed 0, skipped 1, waiting 0
```

Dry-run mode:

```
ID              Source          Workflow              Command
----            ------          -----                 -------
issue-42        github-issue    fix-bug               claude -p "/fix-bug https://github.com/..." --max-turns 50
task-1710504000 manual          (prompt)              claude -p "Refactor the auth middlewar..." --max-turns 50

2 vessel(s) would be drained (dry-run -- no sessions launched)
```

### Examples

```bash
# Process all pending vessels
xylem drain

# Preview what would run
xylem drain --dry-run

# Typical scan-then-drain workflow
xylem scan && xylem drain
```

---

## xylem review

Aggregate persisted run artifacts into a recurring harness review report.

### Usage

```
xylem review
```

### Flags

None.

### Behavior

1. Scans historical vessel summaries under `<state_dir>/phases/`.
2. Loads any linked evidence manifests, cost reports, budget alerts, eval reports, and failure-review artifacts when present.
3. Rolls those artifacts up by source, workflow, and phase into deterministic recommendations: `keep`, `investigate`, `prune-candidate`, or `insufficient-data`.
4. Writes:
   - `<state_dir>/<harness.review.output_dir>/harness-review.json`
   - `<state_dir>/<harness.review.output_dir>/harness-review.md`
5. Prints the latest markdown review to stdout.

Missing historical artifacts from older runs are tolerated; the review degrades gracefully instead of failing.

### Examples

```bash
# Generate the latest review report on demand
xylem review
```

---

## xylem lessons

Synthesize recurring failed-run patterns into institutional-memory proposals for `.xylem/HARNESS.md`.

### Usage

```
xylem lessons
```

### Flags

None.

### Behavior

1. Scans historical vessel summaries under `<state_dir>/phases/`.
2. Filters to failed and timed-out runs inside the last 30 days by default.
3. Clusters recurring failures using structured artifacts first: evidence manifests, phase failures, evaluator reports, and persisted recovery decisions.
4. Skips lessons already present in `.xylem/HARNESS.md` or already represented by an equivalent open PR.
5. Writes:
   - `<state_dir>/reviews/lessons.json`
   - `<state_dir>/reviews/lessons.md`
6. For each remaining proposal slice, creates a branch, commits the generated `.xylem/HARNESS.md` patch, pushes it, and opens a reviewable PR.
7. Persists proposal records containing:
    - a branch name
    - a PR title/body
    - the exact HARNESS.md markdown block to add
    - evidence references back to failed runs
    - PR creation status plus `pr_number` / `pr_url` when a PR was opened

### Artifact contract

`<state_dir>/reviews/lessons.json` contains:

- `lessons[]`: evidence-backed negative constraints with `fingerprint`, `negative_constraint`, `rationale`, `example`, `evidence[]`, and any recovery decision context (`recovery_class`, `recovery_action`, `follow_up_route`) that shaped the cluster
- `proposals[]`: narrow PR slices with `branch`, `title`, `body`, `harness_path`, `harness_patch`, `lesson_fingerprints`, `status`, and optional `pr_number` / `pr_url`
- `skipped[]`: deduplicated lessons omitted because the harness or an open PR already covers them

Future agents can consume this artifact directly to inspect what was synthesized, what was skipped, and which PRs were opened.

### Examples

```bash
# Generate the current institutional-memory proposal set
xylem lessons
```

---

## xylem gap-report

Deterministic helpers used by recurring SoTA gap-analysis workflows.

### Usage

```
xylem gap-report <diff|file-issues|post-summary|guard> [flags]
```

### Behavior

1. `diff` compares a previous and current snapshot, writes a delta JSON artifact, and can overwrite the committed canonical snapshot with the current snapshot.
2. `file-issues` creates up to the top three `[sota-gap]` issues from the deterministic delta, deduping against already-open issues by title.
3. `post-summary` ensures a tracking issue exists and posts a human-readable weekly summary comment.
4. `guard` exits non-zero when the run produced zero new issues and zero snapshot improvements.

These commands are intended for workflow command phases rather than day-to-day manual use.

---

## xylem daemon

Run a continuous scan-drain loop as a long-lived process.

### Usage

```
xylem daemon
```

### Flags

None. Intervals are configured in `.xylem.yml` under the `daemon` key.

### Configuration

| Config field | Default | Description |
|--------------|---------|-------------|
| `daemon.scan_interval` | `"60s"` | How often the daemon scans for new work. |
| `daemon.drain_interval` | `"30s"` | How often the daemon drains pending vessels. |
| `daemon.auto_upgrade` | `false` | Periodically `git fetch`/`reset` the daemon's current worktree to `origin/main`, rebuild the binary, and `exec()` into it when the binary changes. |
| `daemon.upgrade_interval` | `"5m"` | How often the daemon re-checks `auto_upgrade` while it is running. |

The daemon uses the shorter of the two intervals as its tick interval and checks whether enough time has elapsed for each operation on every tick.

Run the daemon from the **root of a dedicated git worktree on branch `main`**. Auto-upgrade syncs the daemon's current working directory before rebuilding, so workflow YAML and prompt changes must be present in that worktree to take effect.

### Behavior

1. Parses scan and drain intervals from config (falling back to defaults).
2. Enters a loop that alternates between scanning and draining based on elapsed time since each operation last ran.
3. After each tick, logs a summary of the queue state (pending, running, completed, failed counts).
4. Daemon drains also execute built-in scheduled vessels such as `context-weight-audit` and `harness-gap-analysis` before launching normal workflow runs.
5. Automatic harness review generation follows the same best-effort cadence as `xylem drain` because daemon draining uses the same post-drain hook.

**Graceful shutdown**: Handles `SIGINT` and `SIGTERM`. On signal, the daemon logs a shutdown message and exits cleanly. Running sessions finish before the process terminates.

### Output

```
daemon started: scan_interval=60s drain_interval=30s
daemon: scan complete -- added=1 skipped=0
daemon: drain complete -- completed=1 failed=0 skipped=0
daemon: tick summary -- pending=0 running=0 completed=1 failed=0
```

### Examples

```bash
# Run in the foreground
xylem daemon

# Run as a background process
xylem daemon &

# Stop a foreground or supervised daemon cleanly
xylem daemon stop

# With systemd, launchd, or similar process managers
# See "Common Patterns" below for a systemd unit example
```

---

## xylem daemon-supervisor

Restart `xylem daemon` after unexpected exits.

### Usage

```
xylem daemon-supervisor
```

### Behavior

1. Acquires a singleton supervisor PID lock at `<state_dir>/daemon-supervisor.pid`.
2. Loads environment overrides from `.daemon-root/.env` before each daemon start.
3. Starts `xylem daemon` with the active `--config` path.
4. If the daemon exits without a matching `xylem daemon stop` request, logs the restart count, waits 10 seconds, and starts it again.
5. `xylem daemon stop` writes `<state_dir>/daemon-supervisor.stop`, signals the daemon, and prevents the next restart.

### Examples

```bash
# Keep the daemon alive and reload .daemon-root/.env on every restart
xylem daemon-supervisor

# Stop a supervised daemon without triggering the restart loop
xylem daemon stop
```

---

## xylem enqueue

Manually enqueue a task without scanning any source.

### Usage

```
xylem enqueue [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--workflow` | `string` | `""` | Workflow to invoke (e.g., `fix-bug`, `implement-feature`). |
| `--ref` | `string` | `""` | Task reference (URL, ticket ID, or description). |
| `--prompt` | `string` | `""` | Direct prompt to pass to the resolved provider. Bypasses workflow phases entirely. |
| `--prompt-file` | `string` | `""` | Read prompt from a file. Mutually exclusive with `--prompt`. |
| `--source` | `string` | `"manual"` | Source identifier tag for the vessel. |
| `--id` | `string` | auto-generated | Custom vessel ID. If empty, generates `task-<unix-millis>`. |

### Validation Rules

- At least one of `--workflow` or `--prompt`/`--prompt-file` is required.
- `--prompt` and `--prompt-file` are mutually exclusive. Providing both is an error.

### Behavior

Creates a new vessel in `pending` state and appends it to the queue. The vessel will be picked up on the next `drain`.

When `--prompt` (or `--prompt-file`) is used without `--workflow`, the prompt is passed directly to the resolved provider, bypassing the phase-based workflow system entirely.

When `--workflow` is specified, the vessel follows the named workflow's phases during drain.

### Examples

```bash
# Enqueue a bug fix for a specific GitHub issue
xylem enqueue --workflow fix-bug --ref "https://github.com/owner/repo/issues/99"

# Enqueue a direct prompt (no workflow phases)
xylem enqueue --prompt "Refactor the auth middleware to use JWT"

# Enqueue from a prompt file
xylem enqueue --prompt-file task.md --workflow implement-feature

# Custom vessel ID and source tag
xylem enqueue --workflow fix-bug --ref "#42" --id "hotfix-42" --source "jira"

# Enqueue and immediately drain
xylem enqueue --workflow fix-bug --ref "#42" && xylem drain
```

---

## xylem retry

Retry a failed vessel, carrying forward failure context so the next session can learn from it.

### Usage

```
xylem retry <vessel-id> [flags]
```

### Arguments

| Argument | Required | Description |
|----------|----------|-------------|
| `vessel-id` | Yes | The ID of the failed vessel to retry. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--from-scratch` | `bool` | `false` | Re-execute the workflow from the beginning instead of resuming from the failed phase. |

### Behavior

1. Looks up the vessel by ID. Returns an error if not found.
2. Validates the vessel is in the `failed` or `timed_out` state. Returns an error if the vessel is in any other state.
3. Creates a new vessel with ID `<original-id>-retry-<N>`, where `N` is auto-incremented based on existing retries in the queue.
4. Copies all configuration from the original vessel (source, ref, workflow, prompt, metadata).
5. Adds failure context to the new vessel's metadata:
   - `retry_of`: original vessel ID
   - `retry_error`: the error message from the failed run
   - `failed_phase`: the phase that failed
   - `gate_output`: output from the gate that caused the failure
6. Sets the new vessel to `pending` state.
7. By default, if the failed vessel has a saved worktree path, retry resumes from the failed phase and copies phase outputs into the new retry vessel's phase directory.
8. If `--from-scratch` is set, xylem does not reuse the saved worktree path or copied phase outputs.
9. If `<state_dir>/phases/<vessel-id>/failure-review.json` exists, xylem records the prior run's `retry_outcome` as `enqueued` so operators can inspect that the failed run was explicitly retried.

This failure context is available to prompt templates so the retried session can avoid repeating the same mistakes.

### Examples

```bash
# Retry a failed vessel
xylem retry issue-42
# Created retry vessel issue-42-retry-1 (retrying issue-42)

# Retry from the beginning instead of resuming
xylem retry issue-42 --from-scratch

# Retry again after another failure
xylem retry issue-42
# Created retry vessel issue-42-retry-2 (retrying issue-42)
```

---

## xylem status

Show the current queue state and a summary of all vessels.

### Usage

```
xylem status [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--json` | `bool` | `false` | Output as a JSON array. |
| `--state` | `string` | `""` | Filter vessels by state. Valid values: `pending`, `running`, `completed`, `failed`, `cancelled`, `waiting`, `timed_out`. |

### Behavior

Lists all vessels in the queue (or only those matching `--state`), displaying a table with columns:

| Column | Description |
|--------|-------------|
| ID | Vessel identifier. |
| Source | Source that created the vessel (e.g., `github-issue`, `manual`). |
| Workflow | Workflow name, or `(prompt)` for direct-prompt vessels. |
| State | Current vessel state. |
| Info | Additional context: for `waiting` vessels, shows what label is being waited for and elapsed time. |
| Started | UTC time the vessel started running, or `--` if not yet started. |
| Duration | Elapsed time since start (or total duration if completed/failed). |

After the table, prints a summary line with counts by state.

In `--json` mode, outputs the full vessel data as an indented JSON array to stdout. When the queue is empty, outputs `[]`.

### Examples

```bash
# Show all vessels
xylem status

# Show only pending vessels
xylem status --state pending

# Show failed vessels
xylem status --state failed

# Machine-readable output for scripting
xylem status --json

# Filter and pipe to jq
xylem status --json | jq '.[] | select(.state == "failed") | .id'
```

---

## xylem pause

Pause scan and drain operations.

### Usage

```
xylem pause
```

### Flags

None.

### Behavior

Creates a pause marker file at `<state_dir>/paused`. While this marker exists:

- `xylem scan` prints "Scanning is paused" and exits without scanning.
- The daemon respects the pause state during its scan cycles.

If already paused, prints "Already paused." and takes no action.

**Important**: Pausing does not affect currently running sessions. Sessions that are already in progress will run to completion.

### Examples

```bash
# Pause before a deployment
xylem pause
# Scanning paused. Run `xylem resume` to resume.

# Verify pause is active
xylem scan
# Scanning is paused. Run `xylem resume` to resume.
```

---

## xylem resume

Resume paused operations.

### Usage

```
xylem resume
```

### Flags

None.

### Behavior

Removes the pause marker file at `<state_dir>/paused`. If not currently paused, prints "Not paused." and takes no action.

### Examples

```bash
# Resume after a deployment
xylem resume
# Scanning resumed.
```

---

## xylem cancel

Cancel a queued or running vessel by ID.

### Usage

```
xylem cancel <vessel-id>
```

### Arguments

| Argument | Required | Description |
|----------|----------|-------------|
| `vessel-id` | Yes | The ID of the vessel to cancel. |

### Behavior

Transitions the vessel to the `cancelled` state in the queue.

**Important**: Cancel does not kill running sessions. If the vessel is currently being executed, the session will run to completion, but the vessel's state in the queue will be marked as `cancelled`.

### Examples

```bash
# Cancel a pending vessel
xylem cancel issue-42
# Cancelled vessel issue-42

# Verify the cancellation
xylem status --state cancelled
```

---

## xylem cleanup

Remove stale git worktrees and old phase outputs created by xylem, then compact stale queue records.

### Usage

```
xylem cleanup [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--dry-run` | `bool` | `false` | Preview what would be removed without removing anything. |

### Behavior

Performs three cleanup operations:

**1. Worktree cleanup**: Lists all git worktrees created by xylem and removes them. Uses the worktree manager's `ListXylem` method to identify xylem-owned worktrees.

**2. Phase output cleanup**: Scans the `<state_dir>/phases/` directory for phase output directories belonging to terminal vessels (completed, failed, cancelled, or timed out) that are older than the `cleanup_after` threshold (default: 7 days / 168 hours). Removes those directories.

**3. Queue compaction**: Compacts the queue file by removing stale queue records. In `--dry-run` mode, it reports how many stale records would be removed.

In `--dry-run` mode, prints what would be removed without deleting anything.

### Examples

```bash
# Preview what would be cleaned up
xylem cleanup --dry-run

# Remove stale worktrees and old phase outputs
xylem cleanup

# Typical maintenance pattern: run weekly
0 3 * * 0 cd /path/to/repo && xylem cleanup >> /tmp/xylem-cleanup.log 2>&1
```

---

## Common Patterns

### Scan and drain as a one-liner

The most common ad-hoc usage. Scan for new work, then immediately process it:

```bash
xylem scan && xylem drain
```

### Daemon mode (recommended for production)

Run xylem as a long-lived background process that continuously scans and drains:

```bash
xylem daemon
```

Configure intervals in `.xylem.yml`:

```yaml
daemon:
  scan_interval: "2m"
  drain_interval: "30s"
```

### Cron setup

If you prefer cron over daemon mode, schedule scan and drain separately or together:

```cron
# Combined: scan and drain every hour
0 * * * * cd /path/to/repo && xylem scan && xylem drain >> /tmp/xylem.log 2>&1

# Separate: scan every 15 minutes, drain every 30 minutes
*/15 * * * * cd /path/to/repo && xylem scan >> /tmp/xylem-scan.log 2>&1
0,30 * * * * cd /path/to/repo && xylem drain >> /tmp/xylem-drain.log 2>&1

# Weekly cleanup
0 3 * * 0 cd /path/to/repo && xylem cleanup >> /tmp/xylem-cleanup.log 2>&1
```

### Manual task workflow

Enqueue a one-off task, drain it, and check the result:

```bash
xylem enqueue --workflow fix-bug --ref "https://github.com/owner/repo/issues/99"
xylem drain
xylem status --state completed
```

### Direct prompt (no workflow)

Skip the multi-phase workflow system and pass a prompt directly to the resolved provider:

```bash
xylem enqueue --prompt "Add rate limiting to the /api/orders endpoint"
xylem drain
```

### Retry loop for flaky failures

Retry a failed vessel and drain again:

```bash
xylem retry issue-42
xylem drain
```

### Pause during deployments

Temporarily stop xylem from scanning or starting new work:

```bash
xylem pause
# ... deploy ...
xylem resume
```

### Monitor queue state in a script

Use JSON output for programmatic access:

```bash
# Count failed vessels
xylem status --json | jq '[.[] | select(.state == "failed")] | length'

# Get IDs of all pending vessels
xylem status --json | jq -r '.[] | select(.state == "pending") | .id'
```

### Systemd unit (Linux)

```ini
[Unit]
Description=xylem daemon
After=network.target

[Service]
Type=simple
WorkingDirectory=/path/to/repo
ExecStart=/usr/local/bin/xylem daemon-supervisor
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
```

### launchd plist (macOS)

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.xylem.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/xylem</string>
    <string>daemon-supervisor</string>
  </array>
  <key>WorkingDirectory</key>
  <string>/path/to/repo</string>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/tmp/xylem-daemon.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/xylem-daemon.log</string>
</dict>
</plist>
```

Place API keys in `/path/to/repo/.daemon-root/.env` so the supervisor reloads them before every daemon restart. Use `xylem daemon stop` for a clean shutdown that does not bounce back via KeepAlive.

## Vessel States

Every vessel in the queue has one of the following states:

| State | Description |
|-------|-------------|
| `pending` | Queued and waiting to be picked up by the next drain. |
| `running` | Currently being executed in a session. |
| `completed` | All workflow phases finished successfully. |
| `failed` | A phase or gate failed. Eligible for `xylem retry`. |
| `waiting` | Blocked on a label gate (e.g., waiting for human approval). Checked on each drain. |
| `timed_out` | The session exceeded the configured `timeout` duration. |
| `cancelled` | Cancelled via `xylem cancel`. |

State transitions follow a defined state machine. For example, `pending` can transition to `running` or `cancelled`, but `completed` is terminal.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `XYLEM_CONFIG` | Alternative to `--config`. Sets the config file path. The flag takes precedence if both are set. |

`.xylem.yml` also includes `claude.env` and `copilot.env` maps. Today, the documented runtime validation uses `claude.env` to require `ANTHROPIC_API_KEY` when `claude.flags` includes `--bare`.
