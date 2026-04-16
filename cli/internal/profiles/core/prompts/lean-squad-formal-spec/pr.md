Open a pull request for the formal spec you just wrote.

- Vessel ID: {{.Vessel.ID}}
- Workflow ref: {{.Vessel.Ref}}

## Analysis

{{.PreviousOutputs.analyze}}

## Implementation summary

{{.PreviousOutputs.implement}}

## Steps

1. Extract the target slug (`<target>`) from `{{.Vessel.Ref}}` and the PascalCase form (`<Target>`) you used in the Lean file name.
2. Create a branch named `lean-squad-formal-spec-<target>/{{.Vessel.ID}}`.
3. Commit the new `formal-verification/lean/FVSquad/<Target>.lean` with a Conventional Commits message:
   ```
   feat(lean-squad): add formal spec for <target>
   ```
4. Push the branch.
5. Open the PR with `gh pr create`:
   - Title: `[Lean Squad] Formal spec for <target>`
   - Body (use a heredoc):
     ```
     🔬 Lean Squad — Task 3 (formal-spec)

     Translated `formal-verification/specs/<target>_informal.md` into
     `formal-verification/lean/FVSquad/<Target>.lean`: Lean 4 type defs,
     function signatures, and theorem declarations. All bodies are `sorry`
     pending:

     - Task 4 (`lean-squad-extract-impl`) — fills function bodies from source.
     - Task 5 (`lean-squad-prove`) — discharges the theorems.

     See https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md
     for the full workflow doctrine.

     LEAN_SQUAD_RUN: {"task":"formal-spec","target":"<target>","status":"ok","artefact":"formal-verification/lean/FVSquad/<Target>.lean"}
     ```
   - Labels: `ready-to-merge`, `lean-squad`.

## Notes

- Do not mark the PR as `[WIP]`. `sorry`-bodied declarations are the *correct* output of this phase, not a work-in-progress signal.
- If `gh pr create` fails because the branch already has an open PR (re-run of a previous vessel), update the existing PR with `gh pr edit` instead.
