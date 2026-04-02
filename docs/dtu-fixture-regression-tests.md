# DTU Guide 4A: Fixture-Backed Regression Tests

Use this method when you want a **checked-in, deterministic regression test** that runs in `go test` and exercises real xylem control-plane logic against twinned `gh`, `git`, `claude`, and `copilot` boundaries.

## When to use this method

Choose fixture-backed regression tests when you want to:

- reproduce a bug offline and keep it covered permanently
- verify scanner, queue, runner, or worktree behavior without live GitHub or provider calls
- model boundary faults such as malformed `gh` JSON, git fetch retries, or provider exit codes
- assert exact DTU evidence such as shim invocations, shim results, and state transitions

## What runs for real vs what is twinned

| Surface | Real or twinned? | Notes |
| --- | --- | --- |
| `scanner`, `source`, `queue`, `runner`, `worktree` | **Real** | The scenario harness calls the real xylem packages. |
| DTU store, manifest loading, scheduler, event log | **Real** | State persistence and scheduled mutations are part of the tested system. |
| `gh`, `git`, `claude`, `copilot` boundaries | **Twinned** | Behavior comes from the DTU manifest and materialized shim wrappers, not live tools or APIs. |
| GitHub network/auth/rate limits | **Twinned** | Only the modeled behavior is exercised. |
| Provider streaming/timing semantics | **Twinned** | Output is modeled as deterministic final stdout/stderr/exit behavior. |
| Shell command gates (`sh -c ...`) | **Covered when kept repo-local and deterministic** | Scenario tests can execute real local shell gates while DTU still twins `gh`, `git`, `claude`, and `copilot`. |
| Deterministic daemon timeout/restart time travel | **Covered where authored** | Control-plane waits and timeout checks now run through the DTU runtime clock, so fixtures can advance waiting-label timeouts and stale-running reconciliation without real sleeps. |

## Trust boundary

The trust boundary for this method is the **boundary transcript encoded in the fixture**.

If the test passes, you can trust that:

- xylem behaves correctly **given the modeled boundary behavior**
- the fixture and scenario assertions match the current DTU implementation
- queue transitions, retry handling, worktree lifecycle, prompt rendering, and event recording work for that modeled case

If the test passes, you **cannot** conclude that:

- live `gh`, `git`, or provider CLIs behave exactly the same way
- auth, git transport latency, network jitter, or rate limiting match production behavior
- command gates or other shell-driven workflow behavior are covered
- live `gh`/`git`/provider timing will match the authored DTU clock behavior

In short: **trust fixture-backed tests for xylem logic downstream of a modeled boundary transcript, not for proving live `gh`/`git`/provider fidelity.**

## How to create a fixture-backed regression test

### 1. Add a DTU fixture

Create a YAML fixture under `cli/internal/dtu/testdata/`.

Start from one of the existing examples:

- `issue-label-gate.yaml`
- `issue-label-gate-timeout.yaml`
- `issue-daemon-recovery.yaml` — use this for stale-running daemon recovery; the corresponding Guide 4B manual smoke path is currently unreliable and can degenerate into a normal timeout
- `issue-provider-failure.yaml`
- `issue-git-fetch-retry.yaml`
- `issue-gh-malformed.yaml`
- `github-pr-copilot.yaml`

A minimal provider-failure fixture looks like this:

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

Use these manifest sections as needed:

- `repositories`: seed issues, PRs, labels, branches, worktrees, reviews, checks
- `providers.scripts`: deterministic `claude` or `copilot` phase outputs
- `shim_faults`: deterministic `gh` or `git` failures, malformed output, delays, hangs, exit codes
- `scheduled_mutations`: delayed state changes triggered after matching observations

### 2. Add a scenario test

Add a test in `cli/internal/dtu/scenario_test.go` or another `*_test.go` file in `package dtu_test`.

The standard pattern is:

1. create the DTU environment with `newScenarioEnv`
2. write a workflow into the temp repo with `writeScenarioWorkflow`
3. configure a real xylem source via `config.Config`
4. run `scanner.New(...).Scan(...)`
5. run `newDrainRunner(...).Drain(...)`
6. assert queue state, DTU state, and DTU events

Skeleton:

```go
func TestScenarioIssueProviderFailureMarksFailed(t *testing.T) {
	env := newScenarioEnv(t, "issue-provider-failure.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "plan", prompt: "Plan issue {{.Issue.Number}}"},
		{name: "implement", prompt: "Implement issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	if _, err := scan.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(cfg, env.queue, env.cmdRunner, env.repoDir, src)
	result, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", result.Failed)
	}

	events := readEvents(t, env.store)
	if len(filterShimEvents(events, dtu.EventKindShimResult, "claude", nil)) == 0 {
		t.Fatal("expected claude shim results")
	}
}
```

### 3. Assert the right layer

Prefer assertions at three layers:

| Layer | Examples |
| --- | --- |
| Queue / vessel state | `pending`, `waiting`, `completed`, `failed`, `CurrentPhase`, persisted worktree path |
| DTU universe state | labels added, PR checks present, worktrees created or cleaned up |
| DTU event log | `shim_invocation`, `shim_result`, prompt hash, exit codes, retries, exact argv |

That combination keeps the test from becoming "test theatre" where only a final state is asserted.

## How to run these tests

Run the whole DTU scenario suite:

```bash
cd cli
go test ./internal/dtu -run TestScenario
```

Run one scenario:

```bash
cd cli
go test ./internal/dtu -run TestScenarioIssueProviderFailureMarksFailed
```

Run the broader DTU-related integration surface:

```bash
cd cli
go test ./internal/dtu ./internal/dtushim ./internal/source ./internal/scanner ./internal/runner ./internal/worktree
```

## How to interpret failures

Use this sequence:

1. **Check the shim events first.** If the logged `shim_result` is not what the fixture intended, the problem is likely in DTU matching, DTU state, or the fixture itself.
2. **If the shim events are right, check xylem state next.** If the boundary transcript is correct but queue state or runner behavior is wrong, the problem is likely in xylem.
3. **Check the divergence registry before treating a mismatch as a bug.** Some differences are intentional and documented in `cli/internal/dtu/testdata/divergence-registry.yaml`.
   Today that registry includes an intentional `git fetch origin <default-branch>` latency divergence: DTU fully twins `git` through the shim runtime, but it does not try to reproduce live fetch transport jitter or packfile timing.

The repository's attribution policy in `cli/internal/dtu/testdata/attribution-policy.yaml` is the intended triage rule:

- twin-only failure -> likely `twin_bug`
- twin/live disagreement at the boundary -> likely `fidelity_bug`
- same failure in twin and live -> likely `xylem_bug`
- live-only failure without coverage -> likely `missing_fidelity`

## What assurances this method provides

A passing fixture-backed regression test provides strong evidence that:

- xylem handles the modeled boundary transcript correctly
- a previously reproduced regression stays fixed
- state transitions and cleanup behavior are stable for that scenario
- retry and wait/resume behavior work as asserted
- DTU event logging captured the expected boundary activity

## What assurances this method does not provide

A passing fixture-backed regression test does **not** prove that:

- live `gh`, `git`, `claude`, or `copilot` behave the same way
- your auth setup, rate limits, or network conditions are healthy
- arbitrary live command gates beyond the authored local shell behavior work
- every daemon timing path is covered unless you authored a fixture for it
- no other live boundary shapes exist beyond the authored fixture

## Recommended trust level

Trust fixture-backed regression tests **highly for xylem control-plane behavior under known, authored boundary conditions**.

Do **not** trust them as a substitute for:

- live differential checks
- manual smoke tests in a real repo
- live command-gate coverage against non-fixture external tools
- operational confidence in unmodeled daemon timing behavior
