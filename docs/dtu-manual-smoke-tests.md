➜ curl -X POST https://lib.harrynicholls.com/api/inbox -H "Authorization: Bearer yEPUqWjxJS5IUj0XXGvgx8kY+Zptt6EOGSCPvMJSm8s=" -H "CF-Access-Client-Id: 70cc19faebbd07891e1db8a7cd846477.access" -H "CF-Access-Client-Secret: 653107757010fc23513ca414bb0c77210c2c43a28130807d60f72bfb3f8008a1" -H "Content-Type: application/json" -d '{"title": "catch-29"}'
# DTU Guide 4B: Manual Smoke Tests Under DTU

Use this method when you want to **drive the real `xylem` CLI in a real repository** while replacing external `gh`, `git`, `claude`, and `copilot` boundaries with DTU-controlled behavior.

## When to use this method

Choose manual smoke tests when you want to:

- exercise your actual `.xylem.yml`, workflows, prompts, and harness
- reproduce a bug interactively in a real repository layout
- verify end-to-end `scan`, `drain`, `status`, and worktree behavior under controlled offline boundary conditions
- test command gates and local build/test commands together with twinned external boundaries

## What runs for real vs what is twinned

| Surface | Real or twinned? | Notes |
| --- | --- | --- |
| `xylem` CLI command parsing and config loading | **Real** | You are running the real CLI binary. |
| `.xylem.yml`, workflows, prompts, HARNESS.md | **Real** | Your repo's actual files are used. |
| Queue files, worktree directories, local filesystem | **Real** | The test mutates local state just like normal xylem execution. |
| Shell command gates and local tools such as `go test` | **Real** | DTU does not replace arbitrary shell commands. |
| `gh`, `git`, `claude`, `copilot` | **Twinned** | These are intercepted through the materialized DTU shim directory and env. |
| GitHub network/auth/provider APIs | **Twinned** | Only the behavior modeled in the manifest exists. |
| Deterministic daemon time travel | **Available inside a materialized DTU universe** | Control-plane waits and timeout checks now use the DTU runtime clock, so repeated `drain`, `daemon`, and `shim-dispatch` commands can advance deterministic state without real sleeping. |

## Trust boundary

The trust boundary for this method is:

- **inside** the real local repo, local filesystem, local workflows, and local shell commands
- **outside** the live networked boundaries that DTU replaces with fixture-defined behavior

If a manual DTU smoke test passes, you can trust that:

- the real xylem CLI can execute the scenario in your repo under the modeled boundary behavior
- your config, prompts, workflows, queue handling, worktree management, and local command gates are wired correctly for that case

If it passes, you **cannot** conclude that:

- live `gh`, `git`, or provider CLIs will behave identically
- your production auth, git transport latency, or rate-limit behavior is correct
- every real-world boundary variation is covered
- live `gh`/`git`/provider timing will match the authored DTU clock behavior

In short: **trust manual DTU smoke tests for real local xylem wiring plus modeled external boundaries, not for proving live boundary fidelity.**

## Before you start

Your workdir must already be a normal xylem repo, including:

- `.xylem.yml`
- `.xylem/workflows/`
- prompt files referenced by those workflows

If you do not already have that setup, initialize it first:

```bash
xylem init
```

## How to create a manual DTU smoke test

### 1. Create a manifest

Put the manifest anywhere convenient for your team. Common choices are:

- `testdata/dtu/<name>.yaml`
- `.xylem/dtu/<name>.yaml`
- a shared repo fixtures directory

Start from an existing example in this repository:

- `cli/internal/dtu/testdata/issue-label-gate.yaml`
- `cli/internal/dtu/testdata/issue-provider-failure.yaml`
- `cli/internal/dtu/testdata/issue-git-fetch-retry.yaml`
- `cli/internal/dtu/testdata/issue-gh-malformed.yaml`

Example: simulate a provider failure in the `implement` phase.

```yaml
metadata:
  name: issue-provider-failure
  scenario: provider failure during issue workflow
repositories:
  - owner: owner
    name: repo
    default_branch: main
    labels:
      - name: bug
      - name: failed
    issues:
      - number: 2
        title: provider crash
        body: the implement phase should fail
        labels: [bug]
providers:
  scripts:
    - name: plan
      provider: claude
      match:
        phase: plan
      stdout: plan ready
    - name: implement
      provider: claude
      match:
        phase: implement
      stderr: provider crashed
      exit_code: 2
```

Add these sections only when needed:

- `providers.scripts` for deterministic LLM phase behavior
- `shim_faults` for `gh` or `git` failures, malformed output, delays, or hangs
- `scheduled_mutations` for state changes that happen after N matching observations

### 2. Choose whether this is a one-shot or multi-step smoke test

Use **one-shot** when you only need to run a single xylem command, such as `scan --dry-run`.

Use **multi-step** when you need state to persist across multiple commands, such as:

- `scan` followed by `drain`
- repeated `drain` calls for wait/resume flows
- probing the shim directly before or after a run

## Method 4B.1: One-shot smoke run

For a single command, use `xylem dtu run`:

```bash
xylem dtu run --manifest /path/to/universe.yaml --workdir "$PWD" -- scan --dry-run
```

Examples:

```bash
xylem dtu run --manifest /path/to/universe.yaml --workdir "$PWD" -- scan
xylem dtu run --manifest /path/to/universe.yaml --workdir "$PWD" -- drain
```

### Important limitation of one-shot runs

`xylem dtu run` reloads DTU state from the manifest each time it starts. That makes it good for **single-command probes**, but not ideal for multi-step scenarios where you want accumulated state, retries, or scheduled mutations to carry across commands.

## Method 4B.2: Multi-step smoke run

For multi-step smoke tests, export the DTU environment once and then run plain `xylem` commands:

```bash
eval "$(xylem dtu env --manifest /path/to/universe.yaml)"
```

Then run the real CLI:

```bash
xylem --config .xylem.yml scan --dry-run
xylem --config .xylem.yml scan
xylem --config .xylem.yml drain
xylem --config .xylem.yml status
```

This preserves one DTU universe across the whole session, so event logs, scheduled mutations, and observation counters continue to accumulate.

You can also run the executable live-boundary suite from the same setup:

```bash
xylem dtu verify --verify-workdir "$PWD"
```

That executes enabled live differentials and canaries, applies the configured normalizers, and reports divergence-registry / attribution-policy context for mismatches.

As checked in today, the suite defines three `gh` differentials, one `git ls-remote` differential, two provider-process-shape differentials (`claude`, `copilot`), and four canaries (`gh`, `git`, `claude`, `copilot`). `XYLEM_DTU_LIVE_CANARY=1` enables the canaries. `XYLEM_DTU_LIVE_GH_DIFFERENTIAL=1`, `XYLEM_DTU_LIVE_GIT_DIFFERENTIAL=1`, and `XYLEM_DTU_LIVE_PROVIDER_DIFFERENTIAL=1` enable the current differential groups. In this repository, `.github/workflows/dtu-canary.yml` leaves the weekly run in canary-only mode, while a manual `full` dispatch enables all three differential groups.

## How to probe a boundary directly

When a smoke test fails, probe the boundary before blaming xylem:

```bash
eval "$(xylem dtu env --manifest /path/to/universe.yaml)"

xylem shim-dispatch gh issue view 1 --repo owner/repo --json labels
xylem shim-dispatch git fetch origin main
xylem shim-dispatch claude -p "smoke test" --max-turns 1
xylem shim-dispatch copilot -p "smoke test" -s
```

If the direct shim output is already wrong, the issue is usually in the DTU fixture, DTU matching, or DTU fidelity rather than in xylem control-plane logic.

## How to inspect evidence

After exporting the DTU environment, these paths are available:

- `XYLEM_DTU_STATE_PATH`
- `XYLEM_DTU_EVENT_LOG_PATH`

Inspect them with your preferred tools:

```bash
cat "$XYLEM_DTU_STATE_PATH"
cat "$XYLEM_DTU_EVENT_LOG_PATH"
```

With `jq`:

```bash
jq . "$XYLEM_DTU_STATE_PATH"
jq -c 'select(.kind=="shim_result")' "$XYLEM_DTU_EVENT_LOG_PATH"
```

Look for:

- the exact `shim_invocation` argv
- `shim_result` exit codes and error text
- prompt hashes for provider phases
- state transitions such as `pending -> waiting -> pending -> completed`
- scheduled mutations reflected in the saved DTU state

## How to run the same smoke test under different DTU conditions

The simplest pattern is to keep the xylem repo setup fixed and swap only the manifest:

```bash
eval "$(xylem dtu env --manifest ./testdata/dtu/issue-gh-malformed.yaml)"
xylem --config .xylem.yml scan --dry-run

eval "$(xylem dtu env --manifest ./testdata/dtu/issue-git-fetch-retry.yaml)"
xylem --config .xylem.yml scan
xylem --config .xylem.yml drain
```

That lets you run the same workflows against:

- happy-path issue scans
- malformed `gh` output
- git retry behavior
- provider failures
- delayed label appearance via scheduled mutations
- waiting-label timeout paths
- daemon stale-running recovery

## How to interpret results

Use this rule:

1. **If direct shim probing is wrong, do not trust the smoke result as evidence about xylem.** Fix the DTU fixture or DTU behavior first.
2. **If direct shim probing is right but xylem fails, the failure is much more likely to be in xylem.**
3. **If a local command gate fails, that may be a real local build/test issue rather than a DTU issue.** In manual smoke tests, arbitrary shell commands are live.
4. **If live behavior differs from DTU behavior, check the divergence registry.** Intentional differences are tracked in `cli/internal/dtu/testdata/divergence-registry.yaml`.
   That registry currently records `git fetch origin <default-branch>` latency as an intentional divergence because the DTU git layer is a deterministic shim replacement, not a live transport simulator.

## What assurances this method provides

A passing manual DTU smoke test provides meaningful evidence that:

- the real xylem CLI can execute the scenario in your repo
- your config, workflows, prompts, harness, queue, and worktree setup are wired correctly for that case
- local command gates and local tool invocation behave as expected in that repo state
- xylem responds correctly to the modeled boundary behavior

## What assurances this method does not provide

A passing manual DTU smoke test does **not** prove that:

- live `gh`, `git`, or provider CLIs will respond exactly the same way
- your production auth or network environment is healthy
- every boundary shape has been modeled
- there are no gaps in DTU fidelity
- every daemon timing path is covered unless you exercised it in your manifest

## Recommended trust level

Trust manual DTU smoke tests **highly for real local xylem execution under modeled external conditions**.

Do **not** treat them as a replacement for:

- live canaries or differential checks
- explicit command-by-command contract comparisons to live tools
- deterministic daemon restart/time-travel tests

## Current DTU testing limitation to remember

The repository already contains:

- a divergence registry
- an attribution policy
- env-gated live differential/canary case definitions

Those live cases are now executable through `xylem dtu verify`. Manual DTU smoke tests remain most useful as:

1. reproducible offline smoke tests in a real repo, and
2. a triage tool for separating xylem bugs from DTU or fixture bugs before running live differentials.
