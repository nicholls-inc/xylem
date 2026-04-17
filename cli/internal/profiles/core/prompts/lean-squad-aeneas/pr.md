You are running the `pr` phase of `lean-squad-aeneas` (Task 8 of the Lean Squad).

## Vessel context

- Vessel ID: {{.Vessel.ID}}

## Prior-phase output

```
{{.PreviousOutputs.analyze}}
```

```
{{.PreviousOutputs.implement}}
```

## Your job in this phase

Commit the Aeneas-generated Lean, push to a dedicated branch, and open a pull request. The `verify` phase has already confirmed `lake build` succeeds, so you are staging known-good output.

### Branch name

Create the branch exactly:

```
lean-squad-aeneas/{{.Vessel.ID}}
```

### Commit

Stage only files under `formal-verification/lean/FVSquad/Aeneas/Generated/` plus, if `implement` created them on a cold start, `formal-verification/lakefile.lean` and `formal-verification/lean-toolchain`. Do **not** stage anything else — the `lean-squad-aeneas` workflow is the sole writer of `Generated/`.

Use a Conventional Commits message, for example:

```
feat(lean-squad): Aeneas-generated Lean from Rust
```

### PR

Open the PR with `gh pr create`. Title must be exactly:

```
[Lean Squad] Aeneas-generated Lean from Rust
```

Body: a short markdown block containing

1. one sentence naming which crates were translated (from `CRATES_TRANSLATED` in `implement`'s output),
2. a link to the upstream doctrine at `https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md`,
3. a link to Aeneas at `https://github.com/AeneasVerif/aeneas`,
4. a one-line summary of what future Lean Squad tasks can now do with this module (prove theorems *about* the generated code),
5. the structured run marker (one line, verbatim, so the next tick can reconcile state):

```
LEAN_SQUAD_RUN: {"task":"aeneas","status":"ok","artefact":"formal-verification/lean/FVSquad/Aeneas/Generated/"}
```

6. the 🔬 disclosure: "Produced by xylem's `lean-squad-aeneas` workflow; faithful but verbose Aeneas output, `lake build` green, not yet reasoned about."

Example:

```
gh pr create \
  --title "[Lean Squad] Aeneas-generated Lean from Rust" \
  --body "<body as above>" \
  --label "lean-squad"
```

### Idempotency

If `implement` emitted `INSTALL_FAILED` or every crate appears in `CRATES_FAILED`, do not open an empty PR. Print a short explanation and exit — `git status --porcelain formal-verification/lean/FVSquad/Aeneas/Generated/` will be empty in that case and the failure is already captured in the phase output.
