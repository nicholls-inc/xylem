Write `.github/workflows/lean-ci.yml`. You are the sole writer of this file;
no other workflow produces or mutates it.

Vessel: {{.Vessel.ID}}
Ref: {{.Vessel.Ref}}

## Analysis
{{.PreviousOutputs.analyze}}

{{if .GateResult}}
## Previous Gate Failure
The verify gate rejected the last attempt. Fix the issues below and re-emit
the file:

{{.GateResult}}
{{end}}

## What to produce

A single file at `.github/workflows/lean-ci.yml` with these exact properties.

1. **Header comment.** First lines of the file, as YAML comments:

   ```yaml
   # 🔬 Generated and maintained by the xylem lean-squad-ci workflow.
   # Source of truth: https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md
   # Mathlib is the Lean 4 math library; `lake exe cache get` pulls prebuilt
   # .olean files from the Mathlib community cache, keeping this job to ~2 min
   # instead of the ~30 min a from-scratch Mathlib build would take.
   ```

2. **Triggers.** Path-scoped so non-Lean PRs are completely unaffected:

   ```yaml
   on:
     pull_request:
       paths:
         - "formal-verification/lean/**"
         - "formal-verification/lakefile.*"
         - "formal-verification/lean-toolchain"
     workflow_dispatch: {}
   ```

3. **Permissions.** Minimum viable:

   ```yaml
   permissions:
     contents: read
   ```

4. **Concurrency.** Cancel superseded runs on the same PR. Use a
   `concurrency:` block whose `group` is the literal string
   `lean-ci-` followed by the GitHub Actions expression for `github.ref`
   (i.e. `${` + `{ github.ref }` + `}` written as one contiguous expression
   in the final YAML), and set `cancel-in-progress: true`. The `${...}`
   expression is GitHub Actions templating and must appear verbatim in the
   generated file.

5. **Jobs.** One job `build` on `ubuntu-latest`:

   - `actions/checkout@v4`.
   - `leanprover/lean-action@v1` with inputs `lake-package-directory: formal-verification`
     and `use-mathlib-cache: true`. That action reads `formal-verification/lean-toolchain`
     via `elan`, runs `lake exe cache get`, and then runs `lake build` — you do
     not need to repeat those steps manually.
   - If, and only if, your analysis indicates `leanprover/lean-action@v1` is
     not suitable for this repo's layout, fall back to explicit steps:
     install elan (`curl -sSf https://raw.githubusercontent.com/leanprover/elan/master/elan-init.sh | sh -s -- -y --default-toolchain none`),
     `echo "$HOME/.elan/bin" >> $GITHUB_PATH`, `cd formal-verification && lake exe cache get && lake build`.

6. **No secrets, no `pull_request_target`, no write permissions.** This job
   must stay safe for forks.

7. **YAML must parse.** The verify gate runs `python3 -c "import yaml;
   yaml.safe_load(open('.github/workflows/lean-ci.yml'))"` and must succeed.

## Constraints

- Do NOT create or modify any files other than `.github/workflows/lean-ci.yml`.
- Do NOT delete an existing `.github/workflows/lean-ci.yml`; overwrite it
  in place if the analysis phase said it needs fixing.
- Do NOT add `pull_request_target` or elevated permissions.
- Do NOT introduce a matrix strategy unless the analysis phase explicitly
  called for one — the Mathlib cache is keyed per-toolchain and matrix runs
  multiply cache misses.

Emit a short summary of the final file after writing it, so the verify gate
has something to echo if shellcheck/actionlint are not installed locally.
