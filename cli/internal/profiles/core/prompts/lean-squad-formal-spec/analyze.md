You are running the `lean-squad-formal-spec` workflow (Task 3 of the Lean Squad system — see https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md).

This workflow produces a single Lean 4 file that makes a previously-written informal spec mechanically checkable: type definitions, function signatures, and theorem declarations — all with `sorry` bodies. Proofs come later (Task 5).

- Vessel ID: {{.Vessel.ID}}
- Workflow ref: {{.Vessel.Ref}}

## Zero-FV-expertise reminder

You do not need to be a Lean expert to run this phase. `sorry` is a valid Lean term that typechecks as any type — it means "this will be filled in later" and the file still compiles. That is the explicit contract of this phase.

## Parse the target

The vessel ref has the form `lean-squad://<target>`. Extract `<target>` — it is a slug like `parse_int` or `balanced_tree`.

The deliverables of this workflow target:
- Input spec: `formal-verification/specs/<target>_informal.md` (produced by `lean-squad-informal-spec`).
- Output Lean file: `formal-verification/lean/FVSquad/<Target>.lean` where `<Target>` is the PascalCase form of the slug (e.g. `parse_int` → `ParseInt`).

## No-op check

If `formal-verification/lean/FVSquad/<Target>.lean` already exists **and** contains at least one non-trivial declaration (a `def`, `structure`, `inductive`, `theorem`, or `abbrev` — not just an `import` or comment), emit the exact line

```
XYLEM_NOOP
```

in your final output and stop. This workflow is idempotent; re-running it on a completed target is a no-op.

Also emit `XYLEM_NOOP` if:
- `formal-verification/specs/<target>_informal.md` does not exist (Task 2 has not run for this target yet).
- `formal-verification/` does not exist at all (Task 1 bootstrap has not run).

## Otherwise

Read `formal-verification/specs/<target>_informal.md` in full. Then write a short analysis (no code yet) covering:

1. The target name (slug and PascalCase form).
2. The key types the spec implies — what Lean 4 type(s) model the input, output, and any intermediate state?
3. The function signatures implied by the spec — one per operation the spec describes.
4. The theorem statements implied by the spec's preconditions, postconditions, and invariants — one Lean theorem per spec bullet that makes a universal claim.
5. Any mathlib imports you expect to need (`import Mathlib.Data.List.Basic`, etc.). If none, say so.
6. Anything in the spec that is genuinely ambiguous or impossible to formalise without more source reading — note it, but do **not** refuse the task. In the `implement` phase you will leave such items as a TODO-comment next to a `sorry` theorem.

Keep the analysis concise. No Lean code in this phase.
