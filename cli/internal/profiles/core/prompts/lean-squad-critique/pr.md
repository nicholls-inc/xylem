Open a pull request with the refreshed `formal-verification/CRITIQUE.md`.

## Analysis
{{.PreviousOutputs.analyze}}

## Implementation
{{.PreviousOutputs.implement}}

## Steps

1. Stage and commit only `formal-verification/CRITIQUE.md`:
   ```
   git add formal-verification/CRITIQUE.md
   git commit -m "[Lean Squad] Update critique"
   ```
   If `git diff --cached --quiet` shows there is nothing to commit, stop here and report `no changes — critique already up to date`.

2. Push the branch `lean-squad-critique/{{.Vessel.ID}}` and open the PR:
   ```
   git push -u origin lean-squad-critique/{{.Vessel.ID}}
   gh pr create \
     --title "[Lean Squad] Update critique" \
     --body "<body, template below>" \
     --label "lean-squad" \
     --label "ready-to-merge"
   ```

## PR body template

```
## Summary

Refresh of `formal-verification/CRITIQUE.md` — the Lean Squad's sceptical stock-take of what the current proofs actually guarantee (and what they do not).

This is Task 7 of the [agentics Lean Squad](https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md) ported into xylem's core profile. The critique is intentionally honest about gaps: `sorry`-holes, unchecked `axiom`s, model–reality divergences, and coverage blind spots are all called out explicitly.

## What changed

<one-sentence summary of the highest-signal change in the critique versus the previous version: e.g. "newly-proved X called out; concurrency divergence on Y flagged; recommended next target shifted from A to B">

## How to read this

`formal-verification/CRITIQUE.md` opens with a "How to read this file" section aimed at readers without prior Lean expertise. It explains the difference between proved and tested, why `decide` and `axiom` weaken claims, and where the model diverges from the real source.

## Trust calibration

- Proved theorems are claims about the model, not the source. The critique says so out loud.
- `sorry`-holes are progress markers, not defects. They are listed, not hidden.
- Recommended next targets at the bottom feed into the tick's weighted scheduling.

🔬 Produced by the `lean-squad-critique` workflow. Vessel ref: {{.Vessel.Ref}}.

LEAN_SQUAD_RUN: {"task":"lean-squad-critique","target":"CRITIQUE","status":"ok","artefact":"formal-verification/CRITIQUE.md"}
```

Finish by printing the PR URL.
