# Configuration Reference

This document covers every field in `.xylem.yml`, with defaults, validation rules, and annotated examples.

## File location

xylem looks for `.xylem.yml` in the root of your repository. Run `xylem init` to generate a starter config that auto-detects your GitHub remote and pre-fills the `repo` field.

```
your-repo/
  .xylem.yml          <-- configuration file
  .xylem/              <-- state directory (queue, workflows, prompts, phase outputs)
    queue.jsonl
    HARNESS.md
    workflows/
    prompts/
    phases/
```

## Minimal config

The smallest valid config for scanning GitHub issues:

```yaml
sources:
  bugs:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
```

Everything else falls back to defaults. You can also run xylem with no sources at all (for enqueue-only usage) -- an empty file with no `sources` key is valid.

## Full annotated config

```yaml
# ---------------------------------------------------------------------------
# Sources: where xylem finds work
# ---------------------------------------------------------------------------
sources:
  bugs:                                   # arbitrary name, used in logs
    type: github                          # source type (currently only "github")
    repo: owner/name                      # GitHub repo in owner/name format
    exclude: [wontfix, duplicate, no-bot] # issues with these labels are skipped
    tasks:
      fix-bugs:                           # arbitrary task name
        labels: [bug, ready-for-work]     # all listed labels must be present
        workflow: fix-bug                 # workflow definition to invoke

  features:
    type: github
    repo: owner/name
    exclude: [wontfix, duplicate, no-bot]
    tasks:
      implement-features:
        labels: [enhancement, low-effort, ready-for-work]
        workflow: implement-feature

# ---------------------------------------------------------------------------
# Execution limits
# ---------------------------------------------------------------------------
concurrency: 2          # max simultaneous Claude sessions
max_turns: 50           # max turns per Claude session
timeout: "30m"          # per-session timeout (Go duration string)

# ---------------------------------------------------------------------------
# State and branch management
# ---------------------------------------------------------------------------
state_dir: ".xylem"     # directory for queue, workflows, prompts, phase outputs
default_branch: "main"  # branch to create worktrees from (auto-detected if omitted)
cleanup_after: "168h"   # remove worktrees older than this (default: 7 days)

# ---------------------------------------------------------------------------
# Claude session settings
# ---------------------------------------------------------------------------
claude:
  command: "claude"                           # Claude CLI binary name or path
  flags: "--bare --dangerously-skip-permissions"  # flags passed to every session
  env:                                        # environment variables for sessions
    ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"  # supports shell variable substitution

# ---------------------------------------------------------------------------
# Daemon mode intervals
# ---------------------------------------------------------------------------
daemon:
  scan_interval: "60s"   # how often the daemon scans for new work
  drain_interval: "30s"  # how often the daemon drains pending vessels
```

## Field reference

### Top-level fields

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `sources` | map | -- | No | Map of source names to source configurations. Not required if you only use `xylem enqueue`. |
| `concurrency` | integer | `2` | No | Maximum number of simultaneous Claude sessions. Must be greater than 0. |
| `max_turns` | integer | `50` | No | Maximum turns per Claude session. Must be greater than 0. |
| `timeout` | string | `"30m"` | No | Per-session timeout. Must be a valid Go duration string and at least `30s`. |
| `state_dir` | string | `".xylem"` | No | Directory for queue file, workflows, prompts, and phase outputs. |
| `default_branch` | string | auto-detected | No | Git branch to create worktrees from. If omitted, xylem detects it from the repository. |
| `cleanup_after` | string | `"168h"` | No | Age threshold for worktree cleanup. Must be a valid Go duration string. |
| `claude` | object | see below | No | Claude CLI session settings. |
| `daemon` | object | see below | No | Daemon mode polling intervals. |

### Sources

Each key under `sources` is an arbitrary name (used in logs and vessel metadata). The value is a source configuration object.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `type` | string | -- | Yes | Source type. Currently only `"github"` is supported. |
| `repo` | string | -- | Yes (github) | GitHub repository in `owner/name` format. Validated strictly -- both owner and name must be non-empty. |
| `exclude` | list of strings | `[]` | No | Labels that prevent an issue from being queued. If an issue has any of these labels, it is skipped. |
| `tasks` | map | -- | Yes | Map of task names to task configurations. At least one task is required per source. |

### Tasks

Each key under `tasks` is an arbitrary name. The value defines which issues match and which workflow runs.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `labels` | list of strings | -- | Yes | Labels that trigger this task. An issue must have all listed labels to match. At least one label is required. |
| `workflow` | string | -- | Yes | Name of the workflow to invoke (e.g., `fix-bug`, `implement-feature`). Must not be empty or whitespace-only. Corresponds to a YAML file in `<state_dir>/workflows/`. |

### Claude session settings

The `claude` section controls how xylem invokes the Claude CLI for each session.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `claude.command` | string | `"claude"` | No | Claude CLI binary name or absolute path. |
| `claude.flags` | string | `""` | No | Additional CLI flags passed to every session. Passed as a single string (e.g., `"--bare --dangerously-skip-permissions"`). |
| `claude.env` | map of string to string | `{}` | No | Environment variables injected into every Claude session. Supports `${VAR}` syntax for shell variable substitution. |

**Validation rules:**

- If `claude.flags` contains `--bare`, then `claude.env` must include a non-empty `ANTHROPIC_API_KEY`. The `--bare` flag disables Claude's built-in authentication, so you must provide your own API key.
- `claude.template` is no longer supported and produces a hard error if present. Migrate to phase-based workflows in `<state_dir>/workflows/`.
- `claude.allowed_tools` is no longer supported and produces a hard error if present. Define allowed tools in workflow phase definitions instead.

### Daemon settings

The `daemon` section controls polling intervals when running `xylem daemon`.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `daemon.scan_interval` | string | `"60s"` | No | How often the daemon scans sources for new work. Must be a valid Go duration string. |
| `daemon.drain_interval` | string | `"30s"` | No | How often the daemon drains pending vessels from the queue. Must be a valid Go duration string. |

## Duration strings

Several fields accept Go duration strings. These are parsed by Go's `time.ParseDuration` and support the following units:

| Unit | Suffix | Example |
|------|--------|---------|
| Nanoseconds | `ns` | `500ns` |
| Microseconds | `us` or `\u00b5s` | `100us` |
| Milliseconds | `ms` | `500ms` |
| Seconds | `s` | `30s` |
| Minutes | `m` | `15m` |
| Hours | `h` | `168h` |

You can combine units: `1h30m`, `2m30s`. There is no `d` (day) suffix -- use `24h` for one day, `168h` for one week.

## Environment variable substitution

The `claude.env` map supports `${VAR}` syntax to reference shell environment variables at runtime:

```yaml
claude:
  env:
    ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"
    GITHUB_TOKEN: "${GITHUB_TOKEN}"
    CUSTOM_FLAG: "static-value"
```

This lets you keep secrets out of your config file. The substitution happens when the environment is passed to the Claude subprocess -- the literal `${VAR}` string is stored in the YAML and resolved at execution time.

## Default branch detection

If `default_branch` is omitted, xylem auto-detects the default branch from the repository's git configuration. You only need to set this explicitly if:

- Your repository uses a non-standard default branch name that git cannot detect
- You want worktrees to branch from a specific branch other than the default

```yaml
# Only needed if auto-detection does not work for your setup
default_branch: "develop"
```

## Cleanup settings

The `cleanup_after` field controls how long worktrees persist before `xylem cleanup` removes them.

```yaml
cleanup_after: "168h"   # 7 days (default)
cleanup_after: "24h"    # 1 day
cleanup_after: "720h"   # 30 days
```

If `cleanup_after` is empty or omitted, it defaults to `168h` (7 days). If the value is present but not a valid duration, validation fails with an error.

Run `xylem cleanup` to remove worktrees that exceed this age. Use `xylem cleanup --dry-run` to preview what would be removed.

## Exclude labels

The `exclude` list prevents issues from being queued, regardless of which task labels they match. This is useful for issues that are tagged for work but temporarily blocked.

Default exclude labels (applied when using the legacy format or when not explicitly overridden):

```yaml
exclude: [wontfix, duplicate, in-progress, no-bot]
```

In the multi-source format, each source has its own `exclude` list. There is no global exclude -- you set it per source.

## Multiple sources

You can configure multiple sources that scan different repositories or different label sets within the same repository:

```yaml
sources:
  frontend-bugs:
    type: github
    repo: myorg/frontend
    exclude: [wontfix, no-bot]
    tasks:
      fix-bugs:
        labels: [bug, triaged]
        workflow: fix-bug

  backend-bugs:
    type: github
    repo: myorg/backend
    exclude: [wontfix, no-bot]
    tasks:
      fix-bugs:
        labels: [bug, triaged]
        workflow: fix-bug

  backend-features:
    type: github
    repo: myorg/backend
    exclude: [wontfix, no-bot, needs-design]
    tasks:
      implement-features:
        labels: [enhancement, approved]
        workflow: implement-feature
```

Each source is scanned independently. Deduplication prevents the same issue from being queued twice.

## Legacy config format

xylem still supports an older flat format with top-level `repo`, `tasks`, and `exclude` fields. On load, this format is automatically normalized into a single `github` source named `"github"`.

### Legacy format

```yaml
repo: owner/name

tasks:
  fix-bugs:
    labels: [bug, ready-for-work]
    workflow: fix-bug

concurrency: 2
max_turns: 50
timeout: "30m"
state_dir: ".xylem"
exclude: [wontfix, duplicate, in-progress, no-bot]
```

### What happens at load time

The `normalize()` function detects the legacy format (top-level `repo` is set, `sources` is empty, and `tasks` is non-empty) and rewrites it internally to:

```yaml
sources:
  github:
    type: github
    repo: owner/name
    exclude: [wontfix, duplicate, in-progress, no-bot]
    tasks:
      fix-bugs:
        labels: [bug, ready-for-work]
        workflow: fix-bug
```

The migration is transparent -- you do not need to change your config file. Both formats are validated the same way after normalization.

### When to migrate

The legacy format only supports a single GitHub source. If you need multiple sources, different exclude lists per source, or sources scanning different repositories, migrate to the multi-source format.

## Removed fields

The following fields are no longer supported. If present in your config, xylem exits with an error and a migration message.

| Removed field | Error message | Migration path |
|---------------|---------------|----------------|
| `claude.template` | `claude.template is no longer supported; migrate to phase-based workflows in .xylem/workflows/` | Define prompt templates per phase in your workflow YAML files under `<state_dir>/workflows/`. See the [README](../README.md) for workflow definition format. |
| `claude.allowed_tools` | `claude.allowed_tools is no longer supported; use allowed_tools in workflow phase definitions` | Move tool allowlists into individual workflow phase definitions. |

## Validation summary

The following rules are enforced when loading `.xylem.yml`. If any rule fails, `Load()` returns an error and xylem does not start.

| Rule | Error |
|------|-------|
| `concurrency` must be > 0 | `concurrency must be greater than 0` |
| `max_turns` must be > 0 | `max_turns must be greater than 0` |
| `timeout` must be a valid Go duration | `timeout must be a valid duration: ...` |
| `timeout` must be >= 30s | `timeout must be at least 30s` |
| `cleanup_after`, if set, must be a valid Go duration | `cleanup_after must be a valid duration: ...` |
| `claude.template` must not be present | `claude.template is no longer supported...` |
| `claude.allowed_tools` must not be present | `claude.allowed_tools is no longer supported...` |
| `--bare` in flags requires `ANTHROPIC_API_KEY` in env | `--bare requires ANTHROPIC_API_KEY in claude.env` |
| `daemon.scan_interval`, if set, must be a valid Go duration | `daemon.scan_interval must be a valid duration: ...` |
| `daemon.drain_interval`, if set, must be a valid Go duration | `daemon.drain_interval must be a valid duration: ...` |
| Each source must have a `type` | `source "<name>" must specify a type` |
| GitHub sources must have a `repo` in `owner/name` format | `source "<name>" (github): repo must be in owner/name format` |
| GitHub sources must have at least one task | `source "<name>" (github): at least one task is required` |
| Each task must have at least one label | `source "<name>" task "<task>": must include at least one labels entry` |
| Each task must have a non-empty workflow | `source "<name>" task "<task>": must include a workflow` |
