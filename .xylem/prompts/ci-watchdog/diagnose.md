Diagnose a CI failure on the main branch.

## CI Run Information
{{.PreviousOutputs.check}}

## Instructions

1. Parse the run ID and URL from the CI run information above.

2. Fetch the failing logs:
   ```
   gh run view <run-id> --log-failed 2>&1 | head -200
   ```

3. Identify the breaking commit:
   ```
   gh run view <run-id> --json headSha -q '.headSha'
   git log --oneline -5 main
   ```

4. Determine the failure class:
   - **Flake**: The failure is non-deterministic (test timeout, network issue, race condition). Evidence: the same test passed in a recent prior run, or the error message indicates transient infrastructure.
   - **Regression**: The failure is deterministic and caused by a specific code change. Evidence: a new test failure that corresponds to recently changed code.
   - **Infrastructure**: CI infrastructure issue (runner failure, Docker pull timeout, etc.). Evidence: failure before any test execution.

5. Output a structured diagnosis:

```
## CI Failure Diagnosis

**Run:** <url>
**Commit:** <sha> — <commit message>
**Failing step:** <step name>
**Failure class:** flake | regression | infrastructure
**Confidence:** high | medium | low

### Error Summary
<concise description of what failed and why>

### Root Cause
<for regressions: which commit/change caused it>
<for flakes: what condition triggers the non-determinism>
<for infrastructure: what CI component failed>

### Recommended Action
<specific fix description>
```

Do not modify any files. This is a read-only diagnostic phase.
