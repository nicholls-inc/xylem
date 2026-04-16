The `lake build` gate will run after this phase. Prepare for it.

- Vessel ID: {{.Vessel.ID}}
- Workflow ref: {{.Vessel.Ref}}

## Implementation output

{{.PreviousOutputs.implement}}

{{if .GateResult}}
## Prior gate output

{{.GateResult}}
{{end}}

## What to do

1. Re-read the Lean file you just wrote. Scan for obvious compile-breakers:
   - Every `import` statement references a module that exists (check Mathlib namespacing carefully).
   - Every type constructor is spelled correctly (`List α` not `list α`; `Nat` not `nat`).
   - Every `theorem`/`def` has a body — even if that body is just `sorry` or `by sorry`.
   - The `namespace` block is closed with `end` at the bottom.
2. If you see a likely error, fix it in place.
3. If `formal-verification/lakefile.toml` is missing required entries for your new file (usually not, since Lake picks up `FVSquad/*.lean` via the default globbing), leave it alone — do **not** edit the lakefile in this workflow. That is Unit 1 (`lean-squad-bootstrap`)'s responsibility.

## Acceptable gate outcomes

- ✅ `lake build` succeeds with warnings about `sorry` — this is the expected, correct outcome.
- ❌ `lake build` reports `error:` lines — the gate will retry once. The `implement` phase will re-run with the gate output in context.

Do not attempt to prove any theorem or fill any function body in this phase. Compilation is the only success criterion.

End with a one-line summary of what you changed (if anything) in this phase.
