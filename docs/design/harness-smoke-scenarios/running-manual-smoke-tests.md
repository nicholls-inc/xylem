# Running Manual Smoke Tests for Harness Scenarios

This guide covers how to manually test harness scenarios using DTU (Deterministic Test Universe) environments. Use this for interactive verification of control-plane behavior against twinned external boundaries.

**See also:**
- [WS1 Scenario Manual Tests](ws1-config-surface-policy.md#manual-smoke-tests) — Scenario-specific instructions and manifests
- [DTU Guide 4A: Fixture-Backed Regression Tests](../../dtu-fixture-regression-tests.md) — Checked-in `go test` scenarios
- [DTU Guide 4B: Manual Smoke Tests](../../dtu-manual-smoke-tests.md) — Comprehensive DTU reference

---

## Quick Start

```bash
cd /Users/harry.nicholls/repos/xylem/cli

# Build the CLI
go build ./cmd/xylem
XYLEM_BIN="$PWD/xylem"

# Pick a scenario manifest plus its matching seeded workdir and set up the DTU environment
eval "$("$XYLEM_BIN" dtu env \
  --manifest ./internal/dtu/testdata/ws1-policy-allow-happy-path.yaml \
  --workdir ./internal/dtu/testdata/ws1-smoke-fixture)"

# Run the scan-drain pipeline from the seeded repo
(
  cd "$XYLEM_DTU_WORKDIR" || exit 1
  "$XYLEM_BIN" --config .xylem.yml scan
  "$XYLEM_BIN" --config .xylem.yml drain
  "$XYLEM_BIN" --config .xylem.yml status
)

# Inspect results
cat "$XYLEM_DTU_STATE_PATH" | jq '.repositories'
cat "$XYLEM_DTU_EVENT_LOG_PATH" | jq -c 'select(.kind=="shim_result")'
```

---

## Current checked-in manifest status

Latest finalized DTU smoke summary (2026-04-02): **22 PASS, 4 FAIL, 1 ERROR** across the checked-in manifests.

That tally predates the new WS3-WS6 seeded workdirs. Treat those newer manifests as fixture-sanity / manual-triage probes until they are rebaselined.

**Current known failures:**
- `issue-gh-auth-scan-failure` fails because the `gh` shim returns `gh: authentication required`, but `xylem scan --dry-run` only surfaces exit status `1`.
- `ws1-surface-violation` fails because the documented protected-surface failure does not trigger.

Targeted rerun update (2026-04-03): `ws1-policy-deny-blocks-phase` and `ws1-policy-require-approval` now PASS after the WS1 shared smoke fixture added explicit `harness.policy.rules` for those configs.

**Current manual-smoke limitation:**
- `issue-daemon-recovery` is not a reliable Guide 4B manual smoke path. In the latest run it degenerated into a normal timeout, so treat that scenario as [Guide 4A fixture-backed regression test](../../dtu-fixture-regression-tests.md) territory instead.

For the WS1 failures above, see the scenario notes in [WS1 Scenario Manual Tests](ws1-config-surface-policy.md#manual-smoke-tests). For the broader DTU method limitation, see [DTU Guide 4B: Manual Smoke Tests](../../dtu-manual-smoke-tests.md#current-dtu-testing-limitation-to-remember).

---

## When to use manual smoke tests

Choose manual smoke tests when you want to:

- **Exercise the real `xylem` CLI** in a real repository layout
- **Verify end-to-end behavior** with twinned external boundaries (gh, git, claude, copilot)
- **Reproduce a bug interactively** with full state persistence across commands
- **Debug control-plane logic** by inspecting queue, worktree, and event logs in real time

---

## What runs for real vs what is twinned

| Surface | Real or Twinned? | Notes |
|---------|---|---|
| xylem CLI, config loading, workflows, HARNESS.md | **Real** | The selected seeded repo (or your own chosen workdir) is used as-is. |
| Queue files, worktree directories, local filesystem | **Real** | State persists across scan/drain/status commands. |
| Shell command gates (e.g., `go test`, `sh -c ...`) | **Real** | Arbitrary local commands execute live (not mocked). |
| `gh`, `git`, `claude`, `copilot` boundaries | **Twinned** | Intercepted via DTU shim directory; behavior from manifest. |
| GitHub APIs, provider streaming, network behavior | **Twinned** | Only modeled behavior in the manifest exists. |

---

## Setting up a DTU environment

### 1. Build the xylem CLI

```bash
cd /Users/harry.nicholls/repos/xylem/cli
go build ./cmd/xylem
```

Verify the binary exists:
```bash
ls -lh xylem
```

### 2. Materialize the DTU universe

Choose a manifest plus its matching seeded workdir (see the workstream docs for the exact pairings), then export the DTU environment:

```bash
XYLEM_BIN="$PWD/xylem"
eval "$("$XYLEM_BIN" dtu env \
  --manifest ./internal/dtu/testdata/ws1-policy-allow-happy-path.yaml \
  --workdir ./internal/dtu/testdata/ws1-smoke-fixture)"
```

This sets five operator-facing environment variables:
- `XYLEM_DTU_UNIVERSE_ID` — unique identifier for this DTU universe
- `XYLEM_DTU_STATE_PATH` — path to the state file (JSON)
- `XYLEM_DTU_EVENT_LOG_PATH` — path to the event log (JSONL)
- `XYLEM_DTU_WORKDIR` — path to the seeded repo used for the smoke
- `XYLEM_DTU_SHIM_DIR` — path to the gh/git/claude/copilot shims

### 3. Verify the shim directory

```bash
ls "$XYLEM_DTU_SHIM_DIR"
```

You should see: `claude`, `copilot`, `gh`, `git`

---

## Running a multi-step smoke test

After materializing the DTU environment, run the real xylem commands from the selected seeded repo:

```bash
(
  cd "$XYLEM_DTU_WORKDIR" || exit 1

  # Scan for issues to queue
  "$XYLEM_BIN" --config .xylem.yml scan

  # Dequeue and execute
  "$XYLEM_BIN" --config .xylem.yml drain

  # Check results
  "$XYLEM_BIN" --config .xylem.yml status
)
```

Each command preserves DTU state across invocations:
- Event log appends to the same JSONL
- State file updates in place
- Observation counters increment (affects scheduled mutations)

---

## Probing boundaries directly

When a smoke test fails, probe the boundary before assuming xylem is broken:

```bash
# Check what issues the gh shim sees
./xylem shim-dispatch gh --help

# Check gh issue list (if the manifest has issues)
echo "Testing gh boundary..."

# Check if claude shim responds
echo "test prompt" | ./xylem shim-dispatch claude -p "respond"

# Check git operations
./xylem shim-dispatch git --help
```

**Rule:** If direct shim probes are wrong, the problem is in the DTU fixture or DTU matching, not xylem. Fix the manifest before adjusting xylem code.

---

## Inspecting evidence

After running a smoke test, examine the DTU state and event log:

### View the state file

```bash
cat "$XYLEM_DTU_STATE_PATH" | jq '.'
```

Look for:
- Repositories, issues, labels
- Provider scripts and their matches
- Scheduled mutations and their triggers
- Current clock state

### View the event log

```bash
cat "$XYLEM_DTU_EVENT_LOG_PATH" | jq -c '.'
```

Look for:
- `shim_invocation` events (what command was called)
- `shim_result` events (what the shim returned)
- Observation counters (triggers for mutations)
- Prompt hashes (what text was sent to claude)

### Common event kinds

```bash
# All shim invocations
jq -c 'select(.kind=="shim_invocation")' "$XYLEM_DTU_EVENT_LOG_PATH"

# All shim results
jq -c 'select(.kind=="shim_result")' "$XYLEM_DTU_EVENT_LOG_PATH"

# Mutation triggers
jq -c 'select(.kind=="observation_matched")' "$XYLEM_DTU_EVENT_LOG_PATH"

# Provider script matches
jq -c 'select(.kind=="script_matched")' "$XYLEM_DTU_EVENT_LOG_PATH"
```

---

## Interpreting results

### If the smoke test passes

**You can trust that:**
- The real xylem CLI executes the scenario correctly in your repo
- Your config, workflows, prompts, and HARNESS.md are wired correctly
- The modeled boundary behavior is handled as expected

**You cannot conclude that:**
- Live GitHub or live providers behave identically
- Your production auth, rate limits, or latency are correct
- Every boundary edge case is covered

### If a boundary probe fails

**Example:** `./xylem shim-dispatch gh issue list --repo acme/widget --json number` returns an error or malformed JSON.

**Root cause:** The DTU manifest or DTU matching is incorrect, not xylem.

**Next step:** Check the manifest file and the DTU event log to see what shim invocation was made and why it failed.

### If the boundary probe succeeds but xylem fails

**Root cause:** Likely a bug in xylem's handling of the boundary response.

**Next step:**
1. Check the queue state: `(cd "$XYLEM_DTU_WORKDIR" && "$XYLEM_BIN" --config .xylem.yml status)`
2. Check the worktree: `ls -la "$XYLEM_DTU_WORKDIR/.xylem/worktrees/"`
3. Check the phase output: `cat "$XYLEM_DTU_WORKDIR"/.xylem/phases/*/phase-output`
4. Compare to the event log to see what shim output xylem received

---

## Running the same test under different conditions

Keep the matching fixture repo setup fixed and swap only the manifest/config pair described in the workstream doc:

```bash
eval "$("$XYLEM_BIN" dtu env \
  --manifest ./internal/dtu/testdata/ws1-policy-allow-happy-path.yaml \
  --workdir ./internal/dtu/testdata/ws1-smoke-fixture)"
(
  cd "$XYLEM_DTU_WORKDIR" || exit 1
  "$XYLEM_BIN" --config .xylem.yml scan
  "$XYLEM_BIN" --config .xylem.yml drain
)

# Now try a different scenario
eval "$("$XYLEM_BIN" dtu env \
  --manifest ./internal/dtu/testdata/ws1-surface-violation.yaml \
  --workdir ./internal/dtu/testdata/ws1-smoke-fixture)"
(
  cd "$XYLEM_DTU_WORKDIR" || exit 1
  "$XYLEM_BIN" --config .xylem.surface-violation.yml scan
  "$XYLEM_BIN" --config .xylem.surface-violation.yml drain
)
```

This lets you verify that the same xylem CLI and repo setup work (or fail predictably) across different boundary conditions.

---

## Scenario-specific instructions

Each workstream's smoke scenarios have manifest-specific run instructions. See:

- [WS1 Scenario Manual Tests](ws1-config-surface-policy.md#manual-smoke-tests) — Config, surface, policy scenarios (S1–S32)
- [WS3 Observability and Cost](ws3-observability-cost.md#manual-smoke-tests) — Span and budget smoke seeds
- [WS3 Summary Artifacts](ws3-summary-artifacts.md#manual-smoke-tests) — Per-vessel summary smoke seed
- [WS4 Evidence Model](ws4-evidence-model.md#manual-smoke-tests) — Gate-evidence smoke seed
- [WS5 Eval Suite](ws5-eval-suite.md#manual-smoke-tests) — Harbor-native scaffold smoke seed
- [WS6 Cross-Cutting](ws6-cross-cutting.md#manual-smoke-tests) — Cross-cutting smoke seed

---

## Cleanup

DTU environments are ephemeral and isolated. To start fresh:

```bash
# The shim directory is shared; the state is per-universe
rm -rf "/Users/harry.nicholls/repos/xylem/cli/.xylem/dtu/ws1-policy-allow-happy-path/"
rm -rf "$XYLEM_DTU_WORKDIR/.xylem/phases" "$XYLEM_DTU_WORKDIR/.xylem/worktrees"
rm -f "$XYLEM_DTU_WORKDIR/.xylem/queue.jsonl" "$XYLEM_DTU_WORKDIR/.xylem/queue.jsonl.lock"

# Re-materialize the environment
eval "$("$XYLEM_BIN" dtu env \
  --manifest ./internal/dtu/testdata/ws1-policy-allow-happy-path.yaml \
  --workdir ./internal/dtu/testdata/ws1-smoke-fixture)"
```

---

## Trust boundary

A passing manual DTU smoke test is meaningful evidence that:
- xylem's control-plane logic (queue, runner, worktree, gates) works for that scenario
- your config, workflows, and harness are wired correctly
- your local shell command gates behave as intended

It is **not** a replacement for:
- live differential checks against real GitHub or real providers
- manual smoke tests in a real repository with real boundaries
- daemon timing/restart behavior verification
