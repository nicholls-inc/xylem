# Multi-repo daemon operation

xylem supports running independent daemon instances across multiple repositories simultaneously. Each daemon instance is fully isolated — there is no shared state, no global coordination, and no cross-repo communication.

## Setup

Each repository needs three things:

1. Its own `.xylem.yml` configuration file at the repo root.
2. A dedicated daemon worktree created from the main branch:
   ```bash
   git worktree add .daemon-root main
   ```
3. A `xylem daemon` invocation started from inside that worktree:
   ```bash
   cd .daemon-root
   xylem daemon
   ```

Repeat this for each repository. Each daemon runs independently in its own terminal session or process supervisor.

## Isolation model

All xylem state is relative to the daemon's working directory (`StateDir`, which defaults to `.xylem`). No paths are shared across repositories.

| Resource | Location | Isolation |
|---|---|---|
| Work queue | `<state_dir>/state/queue.jsonl` | Per-repo |
| Daemon lock / PID file | `<state_dir>/state/daemon.pid` | Per-repo — serves as both the exclusive flock and PID storage, preventing duplicate daemons in the same repo |
| Pause marker | `<state_dir>/state/paused` | Per-repo — `xylem pause` affects only the local daemon |
| Audit log | `<state_dir>/state/audit.jsonl` | Per-repo |
| Phase outputs | `<state_dir>/state/phases/<vessel-id>/` | Per-repo |
| Schedule state | `<state_dir>/state/schedule.json` | Per-repo |
| Git worktrees | created under the repo root | Per-repo |

There is no global semaphore, shared queue, or cross-repo IPC of any kind.

## Resource awareness

`concurrency` limits apply per-daemon. If you run three daemons each with `concurrency: 2`, up to six concurrent Claude sessions may be active at the same time. xylem has no awareness of other daemons running on the same machine.

Plan your total concurrency as the sum across all active daemons:

```yaml
# repo-a/.xylem.yml
concurrency: 2   # up to 2 sessions from this repo

# repo-b/.xylem.yml
concurrency: 1   # up to 1 session from this repo

# combined: up to 3 concurrent sessions on this machine
```

If your machine or API key has a rate limit that covers the total, set per-repo concurrency to stay within that budget. xylem does not coordinate this for you.

## Cost budgets

`cost.daily_budget_usd` is enforced per-daemon. Each daemon tracks only the spend it has incurred — there is no cross-repo budget aggregation.

If you want to cap total daily spend across multiple repos, divide the budget manually across each `.xylem.yml`:

```yaml
# repo-a/.xylem.yml — allocate $5 of a $10 total daily budget
cost:
  daily_budget_usd: 5.0

# repo-b/.xylem.yml — allocate the other $5
cost:
  daily_budget_usd: 5.0
```

There is no mechanism to enforce a shared ceiling across running daemons. Monitor individual daemon logs or your API provider's usage dashboard to track combined spend.

## Observability

All daemons export traces under the service name `"xylem"` with no per-repo identifier attribute. If you point multiple daemons at the same OTLP collector (e.g., a shared Jaeger instance), their traces will be indistinguishable by service name.

To separate traces in the collector, either:

- Run a separate collector or Jaeger instance per repo and point each `observability.endpoint` at the appropriate one.
- Add per-repo `resource.attributes` at the collector level if your OTLP pipeline supports attribute injection.

There is currently no `observability.service_name` config field in `.xylem.yml`. If cross-repo trace separation is important to your workflow, track [nicholls-inc/xylem#446](https://github.com/nicholls-inc/xylem/issues/446) for future config support, or run separate collector instances as a workaround.

## Known limitation: PID-reuse on stop

`xylem daemon stop` works by reading the PID from `<state_dir>/state/daemon.pid` and sending it a `SIGTERM`. It does not verify that the target process is actually a xylem daemon before signaling.

In the unlikely sequence:

1. Daemon A crashes without cleaning up its PID file.
2. The operating system reuses that PID for an unrelated process (or for daemon B in another repo).
3. `xylem daemon stop` (run in repo A's directory) sends `SIGTERM` to the reused PID.

Under this scenario, xylem could inadvertently signal the wrong process. The probability is low in practice — OS PID space is large and reuse takes time — but it is a known design limitation.

**Workaround**: before running `xylem daemon stop`, verify that the daemon is healthy with `xylem status`. If the daemon has crashed and left a stale PID file, remove it manually:

```bash
rm .xylem/state/daemon.pid
```

Then start the daemon fresh.

## Example: two repos on one machine

```bash
# Terminal 1 — repo-a
cd ~/projects/repo-a/.daemon-root
xylem daemon

# Terminal 2 — repo-b
cd ~/projects/repo-b/.daemon-root
xylem daemon
```

Each daemon scans its own sources, maintains its own queue, and spawns Claude sessions in its own isolated worktrees. They do not interfere with each other.
