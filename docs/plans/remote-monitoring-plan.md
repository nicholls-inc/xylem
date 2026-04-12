# Remote Monitoring, Status Reporting & Escalation

## Problem

An operator running the xylem daemon on a remote machine (home server, CI runner, cloud VM) has no visibility into daemon status without SSH access to the host. The supervisor loop in Claude Code provided this during development, but it is not a sustainable operational model. Operators need:

1. **Periodic status snapshots** — vessel metrics, GitHub activity, daemon health — posted to a durable, append-only log they can check from any device.
2. **Escalation alerts** — immediate notification when the daemon hits a state requiring human intervention (auth failure, disk full, persistent vessel failures).

## Design

### Architecture Overview

```
                                    ┌─────────────────────┐
                                    │   GitHub Discussion  │
                                    │  (hourly comments)   │
                    ┌──────────────►│                      │
                    │               └─────────────────────┘
┌──────────────┐    │
│ daemon loop  │────┤  reportTick (1h)
│              │    │  escalationCheck (each tick)
│   tickHook   │────┤
└──────────────┘    │               ┌─────────────────────┐
                    │               │   Telegram Bot       │
                    └──────────────►│  (escalation alerts) │
                                    └─────────────────────┘

┌──────────────┐
│ xylem report │─── ad-hoc posting to Discussion / stdout
└──────────────┘
```

### Two notification channels

| Channel | Purpose | Frequency | Direction |
|---------|---------|-----------|-----------|
| GitHub Discussion | Append-only status log | Hourly | Daemon → GitHub |
| Telegram | Escalation alerts | On-event | Daemon → Operator |

### New packages

#### `cli/internal/notify/` — notification dispatch

Three files:

1. **`notify.go`** — `Notifier` interface + composite dispatcher
   ```go
   type Notifier interface {
       PostStatus(ctx context.Context, report StatusReport) error
       SendAlert(ctx context.Context, alert Alert) error
       Name() string
   }
   ```

2. **`telegram.go`** — Telegram Bot API client (zero external deps, `net/http` only)
   - `SendAlert` sends HTML-formatted message via `POST /bot<token>/sendMessage`
   - `PostStatus` is a no-op (Telegram is for alerts only)
   - Reads token from env var (`XYLEM_TELEGRAM_TOKEN`)
   - Reads chat ID from env var (`XYLEM_TELEGRAM_CHAT_ID`)
   - Rate limiting: deduplicates repeat alerts within a cooldown window (default 30m)

3. **`discussion.go`** — GitHub Discussion poster via `gh api graphql`
   - `PostStatus` appends a comment to a pinned Discussion
   - `SendAlert` is a no-op (Discussion is for status only)
   - Uses `CmdRunner` interface (same pattern as `source/github.go`) to invoke `gh`
   - Creates the Discussion on first run if it doesn't exist, persists the Discussion node ID to `state/discussion-id.txt`
   - GraphQL mutations: `createDiscussion`, `addDiscussionComment`

#### `cli/internal/reporter/` — metric collection and formatting

Two files:

1. **`reporter.go`** — `StatusReporter` that collects a snapshot:
   ```go
   type StatusReport struct {
       Timestamp     time.Time
       Daemon        DaemonStatus       // PID, uptime, binary, last upgrade
       Vessels       VesselMetrics       // counts by state
       Fleet         FleetStatusReport   // health analysis from runner.AnalyzeFleetStatus
       GitHubActivity GitHubActivity     // issues/PRs opened/closed/merged in window
       ActiveVessels []ActiveVessel      // running vessels with phase + duration
       Warnings      []string           // human-readable warning lines
   }
   ```
   - `Collect(ctx)` reads daemon-health.json, queue.jsonl, vessel summaries, and GitHub state via `gh`
   - Pure data collection — no side effects

2. **`format.go`** — renders `StatusReport` into Markdown (for Discussion) or plain text (for stdout/logs)
   - `FormatMarkdown(report) string`
   - `FormatPlainText(report) string`

#### Alert types and escalation triggers

```go
type AlertSeverity string
const (
    SeverityCritical AlertSeverity = "critical" // Requires human action
    SeverityWarning  AlertSeverity = "warning"  // Degraded, may self-heal
)

type Alert struct {
    Severity    AlertSeverity
    Code        string    // machine-readable (e.g. "auth_failure", "disk_full")
    Title       string    // short heading
    Detail      string    // longer description
    Timestamp   time.Time
    VesselIDs   []string  // affected vessels, if applicable
}
```

**Escalation triggers** (checked each daemon tick):

| Code | Severity | Condition |
|------|----------|-----------|
| `auth_failure` | critical | >=3 vessels failed in <5min with auth-related error |
| `high_failure_rate` | critical | >50% of vessels in last hour are failed/timed_out |
| `stall_detected` | warning | Stall monitor found phase stall or orphaned subprocess |
| `queue_backlog` | warning | >10 pending vessels for >30min |
| `upgrade_stuck` | warning | Auto-upgrade overdue for >3x upgrade_interval |
| `all_vessels_failing` | critical | Last N consecutive vessels all failed (N=5) |

Alert dedup: each `Code` has a cooldown (30min default for warning, 2h for critical) to prevent spam. Cooldown state lives in memory (resets on daemon restart, which is fine — a restart clears the condition).

### Config additions

```yaml
notifications:
  github_discussion:
    enabled: true
    repo: "owner/name"          # defaults to first source repo
    category: "Reports"         # Discussion category name
    interval: "1h"              # how often to post status
    title: "Xylem Daemon Status Log"  # Discussion title
  telegram:
    enabled: true
    token_env: "XYLEM_TELEGRAM_TOKEN"      # env var holding bot token
    chat_id_env: "XYLEM_TELEGRAM_CHAT_ID"  # env var holding chat ID
    levels:                                 # which severity levels to send
      - critical
      - warning
```

Added to `Config` struct as `Notifications NotificationsConfig`.

### Daemon integration

The daemon loop's `tickHook` is the natural integration point. Currently it writes `daemon-health.json`. We add two additional concerns, both gated on config:

1. **Report tick** — if `notifications.github_discussion.enabled` and interval has elapsed since last post, collect status and post.
2. **Escalation check** — if `notifications.telegram.enabled`, evaluate escalation conditions against current queue/fleet state and send alerts.

Both run inside `tickHook` (not in a separate goroutine) to avoid concurrent queue reads. The tick already fires every ~30s, so the escalation check is near-real-time. The report interval (1h) is tracked by a simple `lastReportAt` timestamp.

The `tickHook` closure in `cmdDaemon` already has access to `q`, `cfg`, `startedAt`, `lastUpgradeAt` — all the inputs the reporter needs.

### CLI command: `xylem report`

```
xylem report [--post] [--json] [--root DIR]
```

- Default: collect and print status to stdout
- `--post`: also post to configured GitHub Discussion
- `--json`: output as JSON
- `--root`: inspect state from a different root (like doctor)

Reuses `reporter.Collect()` and `notify.Discussion.PostStatus()`.

### Status report format (GitHub Discussion comment)

```markdown
## Xylem Status -- 2026-04-12 14:00 UTC

### Daemon
| | |
|---|---|
| PID | 12345 |
| Uptime | 4h 32m |
| Binary | `a70a1a2c` |
| Last upgrade | 12:28 UTC |

### Vessels
| State | Count |
|-------|-------|
| Pending | 5 |
| Running | 3 |
| Completed | 42 |
| Failed | 7 |
| Timed Out | 2 |
| Waiting | 1 |
| Cancelled | 0 |

### Active Vessels
| Vessel | Phase | Duration |
|--------|-------|----------|
| issue-210 | implement | 12m |
| issue-211 | verify | 3m |

### GitHub Activity (last hour)
| Metric | Count | Details |
|--------|-------|---------|
| Issues opened | 3 | #210, #211, #212 |
| Issues closed | 2 | #205, #208 |
| PRs opened | 4 | |
| PRs merged | 3 | |

### Fleet Health
- 85% healthy (51/60 vessels)
- Patterns: `gate_failed=4`, `timed_out=3`

### Warnings
- 7 failed vessels in last 24h
- Pending backlog: 5 vessels queued >30min
```

### Telegram alert format

```
CRITICAL: Auth Failure

3 vessels failed in 4 minutes with authentication errors.
All retry paths exhausted.

Affected: issue-405, issue-210, issue-211

Action required: renew credentials and strip xylem-failed labels.
```

HTML formatted for Telegram's parse_mode.

## What else an operator would want

Beyond the minimum (Discussion + Telegram), the status report includes:

1. **Daemon health** — PID, uptime, binary commit, last upgrade timestamp. Answers "is it even running?" and "is it on the latest code?"
2. **Active vessel detail** — which vessels are running, what phase, how long. Answers "what's it doing right now?"
3. **Fleet health analysis** — healthy/degraded/unhealthy percentages and dominant failure patterns. Answers "is the fleet generally healthy?"
4. **GitHub activity window** — issues opened/closed, PRs opened/merged/failed in the reporting window. Answers "is it making progress?"
5. **Throughput** — completed vessels in the window. Answers "how fast is it working?"
6. **Warnings** — aggregated from stall monitor, backlog, fleet health. Answers "what should I worry about?"
7. **Auto-upgrade status** — whether upgrade is current or overdue. Answers "is it running stale code?"

These map directly to the questions a remote operator asks when checking in:
- Is it alive? (daemon health)
- Is it productive? (throughput + GitHub activity)
- Is anything stuck? (active vessels + stall warnings)
- Does it need me? (escalation alerts via Telegram)

## Implementation order

1. `cli/internal/config/` — add `NotificationsConfig` to config struct + defaults + validation
2. `cli/internal/notify/telegram.go` — Telegram client (net/http, zero deps)
3. `cli/internal/notify/discussion.go` — Discussion poster (gh api graphql via CmdRunner)
4. `cli/internal/notify/notify.go` — composite notifier
5. `cli/internal/reporter/reporter.go` — StatusReport collection
6. `cli/internal/reporter/format.go` — Markdown + plain text formatters
7. `cli/cmd/xylem/daemon.go` — wire reportTick + escalation into tickHook
8. `cli/cmd/xylem/report.go` — xylem report CLI command
9. Tests for all new packages

## Testing strategy

- **notify/telegram_test.go** — httptest.Server mocking Telegram API; verify request body, auth header, HTML formatting, cooldown dedup
- **notify/discussion_test.go** — mock CmdRunner; verify GraphQL mutation shape, Discussion creation vs comment append, node ID persistence
- **reporter/reporter_test.go** — temp dirs with synthetic queue.jsonl + daemon-health.json; verify metric collection
- **reporter/format_test.go** — golden-file tests for Markdown and plain text output
- **cmd/xylem/daemon_test.go** — verify report tick fires at correct interval in existing daemon loop test harness
- **cmd/xylem/report_test.go** — verify CLI flag parsing and output modes

No real GitHub or Telegram API calls in tests. All external interactions go through interfaces.

## Non-goals (v1)

- Telegram inline buttons / callback handling (requires long-polling or webhook server)
- Slack integration (can be added as another Notifier impl later)
- Grafana/Prometheus metrics export (OTel already covers this path)
- Historical trend analysis (the Discussion comment log IS the history)
- Two-way commands via Telegram (e.g., "retry vessel X" — too much attack surface for v1)
