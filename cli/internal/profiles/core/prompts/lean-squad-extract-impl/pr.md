You are running the `pr` phase of `lean-squad-extract-impl` (Task 4 of the Lean Squad).

## Vessel context

- Vessel ID: {{.Vessel.ID}}
- Vessel ref: `{{.Vessel.Ref}}`

## Prior-phase output

```
{{.PreviousOutputs.analyze}}
```

```
{{.PreviousOutputs.implement}}
```

## Your job in this phase

Commit the Lean-file changes, push to a dedicated branch, and open a pull request.

### Branch name

Derive `<target-slug>` from the `analyze` output's `TARGET_SLUG` line (or, failing that, from `{{.Vessel.Ref}}` by stripping the `lean-squad://` prefix). Create the branch:

```
lean-squad-extract-impl-<target-slug>/{{.Vessel.ID}}
```

### Commit

Stage only the Lean file named in `implement`'s `LEAN_FILE` line (expected to be `formal-verification/lean/FVSquad/<CamelCaseTarget>.lean`). Do not stage any other file. Use a commit message in Conventional Commits style, for example:

```
feat(lean-squad): extract implementation for <target-slug>
```

### PR

Open the PR with `gh pr create`. Title (exactly this shape):

```
[Lean Squad] Extract implementation for <target-slug>
```

Body: a short markdown block containing
1. one sentence naming the target and what was filled in (for example: "Translates the `parseUri` source into Lean 4; theorem bodies remain `sorry` for `lean-squad-prove`.").
2. a link to the upstream doctrine at `https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md`.
3. a pointer to `formal-verification/CORRESPONDENCE.md` noting that Unit 7 (`lean-squad-correspondence`) will record the source-to-Lean mapping for this target.
4. the structured run marker (one line, verbatim, so the next tick can reconcile state):

```
LEAN_SQUAD_RUN: {"task":"extract-impl","target":"<target-slug>","status":"ok","artefact":"<LEAN_FILE>"}
```

5. the 🔬 disclosure: "Produced by xylem's `lean-squad-extract-impl` workflow; function bodies are a best-effort translation, `lake build` green, theorems remain `sorry`."

Labels: apply `lean-squad`. Do not apply `ready-to-merge` — the output contains mechanical code translations that deserve a human sanity check before merge. Example:

```
gh pr create \
  --title "[Lean Squad] Extract implementation for <target-slug>" \
  --body "<body as above>" \
  --label "lean-squad"
```

### Idempotency

If `implement` did not actually modify the Lean file (every `def` left `sorry`, or no diff), do not open an empty PR. Instead, print a short explanation and exit — `git status --porcelain formal-verification/lean/` will be empty in that case.
