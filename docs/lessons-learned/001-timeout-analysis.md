# Lesson 001: Vessel Timeout Limits — Helping or Hindering?

**Date:** 2026-04-16
**Data basis:** 851 vessels across full queue history, `runner.go` stall-monitor code, per-vessel `summary.json` and phase output files
**Conclusion:** Timeouts are correctly catching stuck vessels. They are not cutting off productive work.

---

## Background

The stall monitor (`runner.go:CheckStalledVessels`) kills any vessel whose phase output file has not been updated for longer than `daemon.stall_monitor.phase_stall_threshold` (default: 30m). The question was whether this threshold is too aggressive — whether vessels are being killed in the middle of genuine productive work.

---

## What timed-out vessels actually look like

36 vessels carry `state: timed_out` across the full queue history. They fall into two structural failure patterns:

### Pattern A — Chdir cascade (28 vessels, ~10m stalls)

`analyze` and `plan` complete normally. `implement` then fails in **2–10 ms** with:
```
chdir .claude/worktrees/feat/issue-NNN-NNN: no such file or directory
```
The worktree was pruned between phases. The subprocess dies immediately, leaving `implement.output` at 0 bytes. The stall monitor fires ~10 minutes later.

Root cause: prune-races-drain race (fixed in PR #548). More time on the original attempt would not have helped.

### Pattern B — Worker-stall (8 vessels, 8h+ / 30–90m stalls)

No phase directory is ever created. The vessel was dequeued but the worker goroutine never registered a subprocess. The stall monitor is the only thing that eventually releases these slots.

Root cause: worker-stall bug (fixed in PR #543). More time would not have helped — there was no subprocess.

---

## Are productive vessels ever cut off?

**No evidence of this.** Across 278 completed vessels:

- 71% complete in under 10 minutes
- 8% (23 vessels) take over 30 minutes total
- The longest individual *phase* duration observed: **30.9 minutes** (`analyze` for `issue-225-retry-1-retry-1-retry-1`, using copilot gpt-5.4)

The stall monitor fires on **file staleness**, not wall-clock vessel duration. Copilot gpt-5.4 streams output continuously, so the phase output file is updated throughout — the staleness timer resets on every write. The vessel at 30.9m completed successfully.

Zero vessels failed due to `max_turns` limits being reached. The 20/60 turn limits in workflow phases are not a binding constraint.

---

## Recovery pattern after timeouts

Of 36 timed-out vessels, 12 eventually completed — all via **retry under clean conditions**, not by being given more time. None completed on the same attempt that stalled. This confirms the root cause is infrastructure state (missing worktree, dead subprocess), not insufficient runtime.

---

## The one boundary risk

The 30.9m analyze phase is close enough to the 30m threshold to warrant attention. A genuinely complex feature requiring 35–40m of analysis could be killed by the stall monitor during productive streaming output.

There is a compounding factor: `processAlive` uses `kill -0` (`runner.go:4045`), which the sandbox blocks. This means `liveTrackedProcess` always returns `false` in sandboxed environments, so the stall monitor falls back to pure file-staleness checks even for live processes. With a streaming copilot process this is fine (file updates reset the timer); for a hypothetically block-buffered process it would be a false positive.

A threshold of 45m would provide margin without meaningful downside.

---

## Key code locations

| Concern | Location |
|---|---|
| Stall monitor entry point | `cli/internal/runner/runner.go:4357` (`CheckStalledVessels`) |
| Live-process skip (prevents false positives) | `runner.go:4401` |
| File-staleness check | `runner.go:4431` |
| `processAlive` (broken by sandbox `kill -0`) | `runner.go:4037` |
| Stall threshold default (30m) | `cli/internal/config/config.go:423` |
| Threshold config key | `daemon.stall_monitor.phase_stall_threshold` in `.xylem.yml` |

---

## Guidance for future changes

- **Do not raise the threshold above 45m** without re-examining whether copilot block-buffering has returned. A threshold much above 45m means stuck vessels block concurrency slots for an unacceptable duration.
- **Do not lower the threshold below 20m.** Several completed vessels had individual phases in the 15–20m range. A 10m threshold (used historically, before PR #364) caused false positives for these.
- **If you see a `timed_out` vessel**, check `summary.json` first. If `implement` (or any phase) shows `duration_ms < 1000` with a chdir error, this is Pattern A — a worktree/prune issue, not a timeout problem. If there is no `phases/` directory at all, this is Pattern B — a worker-stall issue.
- **`max_turns` is not the bottleneck.** If a vessel is failing at a phase, check gate failures, process crashes, and infrastructure state — not turn limits.
