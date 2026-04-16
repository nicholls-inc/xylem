You are running the `pr` phase of `lean-squad-informal-spec` (Task 2 of the Lean Squad).

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

Commit the informal-spec file, push to a dedicated branch, and open a pull request.

### Branch name

Derive `<target-slug>` from the `analyze` output's `TARGET_SLUG` line (or, failing that, from `{{.Vessel.Ref}}` by stripping the `lean-squad://` prefix). Create the branch:

```
lean-squad-informal-spec-<target-slug>/{{.Vessel.ID}}
```

### Commit

Stage only `formal-verification/specs/<target-slug>_informal.md`. Do not stage any other file. Use a commit message in Conventional Commits style, for example:

```
feat(lean-squad): informal spec for <target-slug>
```

### PR

Open the PR with `gh pr create`. Title (exactly this shape):

```
[Lean Squad] Informal spec for <target-slug>
```

Body: a short markdown block containing
1. one sentence of what the spec covers,
2. a link to the upstream doctrine at `https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md`,
3. the structured run marker (one line, verbatim, so the next tick can reconcile state):

```
LEAN_SQUAD_RUN: {"task":"informal-spec","target":"<target-slug>","status":"ok","artefact":"formal-verification/specs/<target-slug>_informal.md"}
```

4. the 🔬 disclosure: "Produced by xylem's `lean-squad-informal-spec` workflow; plain-English, not mechanically checked."

Labels: apply `lean-squad` (and `ready-to-merge` if this repository conventionally auto-merges spec-only PRs; otherwise omit). Example:

```
gh pr create \
  --title "[Lean Squad] Informal spec for <target-slug>" \
  --body "<body as above>" \
  --label "lean-squad"
```

### Idempotency

If the spec file was not actually written in the `implement` phase (for example, `implement` emitted no changes), do not open an empty PR. Instead, print a short explanation and exit — `git status --porcelain formal-verification/specs/` will be empty in that case.
