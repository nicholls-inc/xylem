# Foxguard Baseline Justifications

Every entry in [`.foxguard/baseline.json`](./baseline.json) MUST have a corresponding entry in this file. The check at `scripts/check_foxguard_justifications.py` enforces 1:1 coverage and runs both in pre-commit and CI.

Adding a finding to the baseline without justification **will fail the build**. See `CLAUDE.md` § Foxguard protocol for the verify-and-justify workflow.

## Required format

Each justification is a level-2 heading followed by a code-fenced `Fingerprint:` line, a `**Rationale:**` block, and a `**Verified by:**` line.

```
## <path>:<line> — <rule_id>

Fingerprint: `<64-hex-fingerprint-from-baseline.json>`

**Rationale:** Why this finding is a false positive or acceptable risk in this context.
**Verified by:** <who>, <YYYY-MM-DD>
```

The heading text is for humans (file/line/rule may drift); the `Fingerprint:` line is the machine-parseable anchor.

---

## `cli/cmd/xylem/automerge.go:375` — `go/no-command-injection`

Fingerprint: `2f5d51eacd6165c5037406d0accb1cd803638b5c9181b7df33cb6d95ae6bb398`

**Rationale:** `exec.CommandContext(ghCtx, "gh", args...)` in `listOpenPRs`. The command name is the hardcoded string `"gh"`; Go's `exec.Command` passes `args` as an argv array directly to `execve(2)` — no shell interpretation, so shell-metachar injection is not possible. `args` are built from fixed flag strings, `strconv.Itoa`, and the repo slug sourced from trusted `.xylem.yml` config. Even if a malicious slug were configured, `gh --repo <slug>` would reject it rather than execute arbitrary code.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/automerge.go:397` — `go/no-command-injection`

Fingerprint: `32b99df4945d0eadba48bf5adb73283a3b2a4449464932b75e5a87ae9ded708f`

**Rationale:** Same pattern as `automerge.go:375` — `exec.CommandContext(ghCtx, "gh", args...)` in `getPRSummary`. Args are fixed flags plus `strconv.Itoa(number)` and the config-sourced repo slug. Argv invocation, no shell.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/automerge.go:432` — `go/no-command-injection`

Fingerprint: `3eaf7289d031aacf22e97b39e4c7e6f7e99926cd1a64ae6df96523e2198106fc`

**Rationale:** Same pattern as `automerge.go:375` — `exec.CommandContext(ghCtx, "gh", args...)` in `addPRLabels`. Args include `fmt.Sprintf("repos/%s/issues/%d/labels", slug, number)` passed to `gh api`; the slug is from trusted config and the API path is consumed as a single argv element by `gh`. JSON label body flows via stdin, never via args.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/automerge.go:464` — `go/no-command-injection`

Fingerprint: `bba6fc70260608f8739ddb0f2b77eca478a09ee44723fd047346a60ce816222b`

**Rationale:** Same pattern as `automerge.go:432` — `exec.CommandContext(ghCtx, "gh", args...)` in `requestCopilotReview`. Config-sourced repo + reviewer name flow into a `gh api` REST invocation via argv. JSON body via stdin. No shell.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/automerge.go:481` — `go/no-command-injection`

Fingerprint: `00d02abcd1bbe569d3d948198dd018753ff2a82d72f38548aa4205a3a17a4c98`

**Rationale:** Same pattern as `automerge.go:375` — `exec.CommandContext(ghCtx, "gh", args...)` in `adminMergePR`. Fixed flags (`pr merge --admin --squash --delete-branch`) plus optional `--repo <slug>` and `strconv.Itoa(number)`. Argv invocation, no shell.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/daemon_supervisor.go:308` — `go/no-command-injection`

Fingerprint: `51096230d3f67bb9b54e1136b13a4f0f7b3ab44a02f18341ff0ac47cf3518222`

**Rationale:** `exec.Command(launch.ExecutablePath, launch.Args...)` in `startDaemonSupervisorProcess`. Both `ExecutablePath` and `Args` are fields on an internal `daemonSupervisorLaunch` struct constructed inside the binary (the daemon re-spawning itself with `--config <path> daemon`). No user input reaches this call. Argv invocation, no shell.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/dtu.go:402` — `go/no-sql-injection`

Fingerprint: `b6e90751565375c6e535df224abff6e5f035eb1d76e34a30b5b69493aed61377`

**Rationale:** Miscategorized. This line generates a POSIX shell script for a shim wrapper (`#!/bin/sh\nexec <binary> shim-dispatch <shim> "$@"\n`) — there is no SQL involved anywhere in the DTU package. The real threat category would be shell injection, and both interpolated values are passed through `shellEscape()` at `dtu.go:509`, which does the canonical POSIX single-quote wrap-and-escape (`'` → `'\''`). Values come from internally-resolved binary and shim paths, not user input.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/eval.go:173` — `go/no-command-injection`

Fingerprint: `1d1b8b71da95fd22cc8aed60b1bab7df8d82b42ca5da9ea9dfbfac1524978f83`

**Rationale:** `exec.Command(pytest, cmdArgs...)` in `cmdEvalRun`. `pytest` is resolved by `pytestPath()` (searches `PATH` for the pytest binary). `cmdArgs` is `[testsDir, "-v", "--tb=short"]` where `testsDir` is a filepath.Join of scenario directories resolved from the operator-controlled eval dir. Argv invocation, no shell. Not reachable by untrusted input.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/eval.go:264` — `go/no-command-injection`

Fingerprint: `b2f66cef158a64fa67c64b244630048eb39bbc4fbba7ee75828ab0d06082037b`

**Rationale:** Same pattern as `eval.go:173` — `exec.Command(pytest, cmdArgs...)` in `cmdEvalCompare`. Identical argument construction.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/exec.go:191` — `go/no-command-injection`

Fingerprint: `3b8508218773ef7f044f5cdd2beb92adc6d0962e308f09d1509d8460b2411df9`

**Rationale:** `realCmdRunner.Run` — the generic subprocess runner used by the xylem runner to execute workflow phases. `name` and `args` come from workflow YAML (a protected control surface per `.claude/rules/protected-surfaces.md`) and internal runner logic. Argv invocation, no shell. Threat model assumes workflow YAML is trusted.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/exec.go:197` — `go/no-command-injection`

Fingerprint: `0ba1820fffa148fed8fba4ce84032cc4f0b947201f03b36215f33b24f3e2e910`

**Rationale:** Same pattern as `exec.go:191` — `realCmdRunner.RunOutput`. Argv invocation, no shell, trusted workflow-sourced args.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/exec.go:203` — `go/no-command-injection`

Fingerprint: `824df01c9038b74443f9900bd27dfb0e1421962d45721e6e36a2da0ed8364970`

**Rationale:** Same pattern as `exec.go:191` — `realCmdRunner.RunProcess`. Argv invocation, no shell, trusted workflow-sourced args.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/exec.go:212` — `go/no-command-injection`

Fingerprint: `5ff66bc875d6265f668a883ed5ffc8f0548e4ffdcaefb57bc076b7bea9333072`

**Rationale:** Same pattern as `exec.go:191` — `realCmdRunner.RunProcessWithEnv`. Argv invocation, no shell, trusted workflow-sourced args.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/exec.go:252` — `go/no-command-injection`

Fingerprint: `b150ebac38c3eded8b796277265000676bc0696933a83d3d7c0cd2ae8b7853f2`

**Rationale:** Same pattern as `exec.go:191` — `realCmdRunner.runPhaseInternal`, the central phase executor. Argv invocation, no shell, trusted workflow-sourced args.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/internal/dtu/scenario_test.go:186` — `go/no-command-injection`

Fingerprint: `bcaa0f9b18ebcd2f2748abd5cc31c398027f29011364473f79d2551e88ca95aa`

**Rationale:** Test-only code. `exec.CommandContext(ctx, name, args...)` in the DTU scenario test harness — `name` is gated to `sh` scripts produced by test fixtures under `testdata/`. Never reachable in production.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/internal/dtu/testdata/manual-smoke/ws5-eval-suite/.xylem/eval/helpers/xylem_verify.py:36` — `py/no-path-traversal`

Fingerprint: `c17b1eea8e663a095359feb5fa555fff5357d1e9b7f3041deb3aca6d42a97737`

**Rationale:** Test fixture under `cli/internal/dtu/testdata/` — not shipped, not executed outside the eval harness. Path is `os.path.join(vessel_dir, "summary.json")` where `vessel_dir` is resolved from the test-harness `WORK_DIR`. No untrusted input flows in.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/internal/dtu/testdata/manual-smoke/ws5-eval-suite/.xylem/eval/helpers/xylem_verify.py:45` — `py/no-path-traversal`

Fingerprint: `0bb11ff7e627f38ba98a435e43c50ae5d76e71823ce06194efdcc06755a90ee8`

**Rationale:** Same as `xylem_verify.py:36` — test fixture reading `evidence-manifest.json` from the vessel directory. No traversal vector.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/internal/dtu/testdata/manual-smoke/ws5-eval-suite/.xylem/eval/helpers/xylem_verify.py:54` — `py/no-path-traversal`

Fingerprint: `12ba92e92181fe4a36e3c85f3e617834702a8c4a1d465fe819f5b4a75cea3d27`

**Rationale:** Same as `xylem_verify.py:36` — test fixture reading `audit.jsonl` from the state directory. No traversal vector.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/internal/dtu/verification_runner.go:411` — `go/no-command-injection`

Fingerprint: `37d7960342a6d2c60f6f6fc3c3fdd88ee77f11cbed7b3b45d4dda74916832d8a`

**Rationale:** `exec.CommandContext(ctx, invocation.Command, invocation.Args...)` in the DTU verification runner. `invocation` comes from the DTU verification manifest (trusted dev-authored YAML). Argv invocation, no shell.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/internal/gate/live.go:175` — `go/no-ssrf`

Fingerprint: `b7618a5ac7373493f6e1a8b28707c2b05c4131afb412334a944e3fc0231b57c2`

**Rationale:** `LiveHTTPGate` is a deliberate feature: workflow authors define HTTP gates with explicit `base_url` and `url` fields to probe live endpoints as a quality gate (analogous to `curl` in a CI step). URLs originate from workflow YAML — a protected control surface. Calling this SSRF misidentifies the trust model. **Follow-up:** if workflow YAML ever becomes contributable by untrusted parties (e.g., auto-fetched from PR branches before review), re-evaluate and add explicit host allowlisting.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/internal/notify/telegram.go:222` — `go/no-ssrf`

Fingerprint: `fd94c064e4b7c35844bbd0ea83d1a87f14742d80d5e19323894934d23a6330af`

**Rationale:** URL is `telegramAPIBase + t.token + "/getUpdates?..."` where `telegramAPIBase` is the hardcoded const `"https://api.telegram.org/bot"` (`telegram.go:21`). The only dynamic portion is the bot token, which is a path segment on a fixed host. Path-position characters (`@`, `/`, `?`, `#`) cannot escape the already-established authority; any malformed token just produces a bad request on `api.telegram.org`, not a different host. Token is sourced from operator config.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/internal/notify/telegram.go:279` — `go/no-ssrf`

Fingerprint: `c59d99d94caf065a29fecfe3dec79b986e0d1b1e695257b02ea124ec448af0f3`

**Rationale:** Same pattern as `telegram.go:222` — `telegramAPIBase + t.token + "/sendMessage"`. Hardcoded scheme+host prefix; token is a path segment on the fixed `api.telegram.org` authority.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/internal/policy/class.go:25` — `go/no-hardcoded-secret`

Fingerprint: `b793025106a3bd9856ef0b72329bf6ead85d6494836eaf636b3ec4b377b3d651`

**Rationale:** `OpReadSecrets Operation = "read_secrets"` — a policy-operation enum label identifying "the act of reading secrets" as a permission class in the intermediary policy engine. The value is an identifier string, not a credential. The scanner matched on the substring `secret` in the constant name.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-16

## `cli/cmd/xylem/daemon_reload.go:491` — `go/taint-path-traversal`

Fingerprint: `90c54d149dfb33fa5165024c65b84a875b8add5427a1a7e8042e74d3a53ecb37`

**Rationale:** `writeDaemonReloadRequest` calls `os.WriteFile(daemonReloadRequestPath(cfg), …)`. `daemonReloadRequestPath` is `config.RuntimePath(cfg.StateDir, "daemon-reload-request.json")` — a join of the trusted config-provided `StateDir` with a fixed literal filename. No HTTP input or untrusted source reaches this sink; v0.6.3's cross-file taint tracer appears to have conflated an unrelated `net/http` import in this package with the write path.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-17

## `cli/internal/cost/aggregate_test.go:17` — `go/taint-path-traversal`

Fingerprint: `d0a022c55f26afb477365e7cafee7827d9dccc52c5fab350e0cfa61467b5e76c`

**Rationale:** Test helper `writeReport` calls `SaveReport(filepath.Join(vesselDir, "cost-report.json"), r)`. `vesselDir` is `filepath.Join(t.TempDir()-derived dir, "phases", vesselID)` where `vesselID` is a test-provided string literal. Not reachable from production code paths; no untrusted input.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-17

## `cli/internal/cost/budgetgate_test.go:23` — `go/taint-path-traversal`

Fingerprint: `5bfa9ace51f62fed06cf680080d01434ac6c61086fdce3c4d1902d48b8b262b1`

**Rationale:** Identical pattern to `aggregate_test.go:17` — test helper using `t.TempDir()`-rooted paths with literal filenames. Not reachable from production code paths.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-17

## `cli/internal/dtu/runtime_clock.go:127` — `go/taint-path-traversal`

Fingerprint: `7e7f68bef6fead654ca711bd4714a574f2073d411a5c52ad40125f880d1dfdcc`

**Rationale:** `runtimeStore` reads `os.Getenv(EnvStatePath)` and passes it to `os.Stat`. Environment variables are part of the operator-controlled process environment, not untrusted external input — any attacker able to set the env var already has code execution in the daemon process. Treating env vars as taint sources is categorically a false positive in this threat model. The DTU runtime store path is a deliberately operator-configured hook for the deterministic-time universe.
**Verified by:** harry.nicholls + Claude (Opus 4.7), 2026-04-17
