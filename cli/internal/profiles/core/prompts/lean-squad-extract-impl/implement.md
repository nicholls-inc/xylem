You are running the `implement` phase of `lean-squad-extract-impl` (Task 4 of the Lean Squad).

See `https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md` for the upstream doctrine. Your job is narrow: replace `sorry` **function bodies** in one existing Lean file with faithful translations of the source code. Theorems are not your concern — leave their `sorry` alone.

## Vessel context

- Vessel ID: {{.Vessel.ID}}
- Vessel ref: `{{.Vessel.Ref}}`

## Prior-phase output (authoritative)

The `analyze` phase parsed the slug, located the source, confirmed the Lean file exists, and confirmed at least one `def` has a `sorry` body. Use its output below verbatim; do not re-derive these paths.

```
{{.PreviousOutputs.analyze}}
```

From that output you have:
- `TARGET_SLUG` — the kebab-case identifier for this target
- `SOURCE_PATH` — the implementation file in the original language
- `LEAN_PATH` — the Lean file you will edit
- `TEST_PATHS` — zero or more test files (useful for sanity checks)
- `COMMENT_SOURCES` — docs or comment-bearing files
- `FUNCTION_SORRIES` — which `def`s still need bodies

## Your job in this phase

Edit **only** `LEAN_PATH`. Replace the `sorry` body of each listed `def` / `partial def` with a faithful translation of the corresponding construct in `SOURCE_PATH`. Preserve the existing type signatures, module header, imports, and all `theorem` / `lemma` declarations verbatim — those belong to other workflows.

### What "faithful translation" means

1. **Behaviour-preserving under the existing type signature.** The type in the Lean file was chosen by `lean-squad-formal-spec`. You must translate so that the function computes the same values the source would for inputs admitted by that type.

2. **Idiomatic Lean 4.** Prefer pattern matching, `match`, `if ... then ... else`, and structural recursion. Avoid `do`-notation unless the source is genuinely imperative and the Lean type involves a monad already. Do not invent new types, new imports, or new auxiliary top-level declarations unless strictly necessary for the body — and if you do, place them immediately above the `def` they support.

3. **Use Mathlib / core only.** Do not add third-party dependencies. If a construct in the source has no clean Lean equivalent (e.g. mutation of an in-place buffer), translate to a pure-functional equivalent and document the divergence.

4. **Abstractions may be lossy — that's fine, but disclose them.** When a translation loses information relative to the source (for example, modelling `Go int` with Lean's arbitrary-precision `Int` instead of a fixed-width 64-bit integer), add a one-line comment at the body:

   ```
   -- NOTE: source uses Go's int (64-bit, two's complement); translated using Int.
   -- Overflow / wrap-around behaviour is not modelled here.
   ```

   Use the `-- NOTE:` prefix so later phases and reviewers can grep for divergences.

5. **If a source construct cannot be translated safely, leave `sorry` in place** and add a `-- NOTE: untranslated — <reason>` comment. An honest `sorry` with a reason is better than a guess.

### What you must not do

- Do **not** modify any `theorem` or `lemma` body. If one is `sorry`, leave it `sorry`. That belongs to `lean-squad-prove` (Unit 6).
- Do **not** rename functions, change signatures, or restructure the module. If the signature seems wrong, leave the body `sorry` with a `-- NOTE:` explaining what you observed — `lean-squad-formal-spec` will redo the signature.
- Do **not** write or modify any file other than `LEAN_PATH`. This workflow is the sole writer of function bodies in `formal-verification/lean/FVSquad/<CamelCaseTarget>.lean`.
- Do **not** run `lake build` or otherwise attempt the proof. The `verify` phase runs the build.
- Do **not** commit, push, or open a PR. The `pr` phase handles that.

### Reading the source

1. Read `SOURCE_PATH` end to end.
2. Read `LEAN_PATH` end to end — note the existing imports, namespaces, and the exact signatures you must preserve.
3. Skim `TEST_PATHS` for concrete input/output pairs that help you sanity-check your translation by hand.

### Editing workflow

For each `def` listed in `FUNCTION_SORRIES`:
1. Identify the corresponding function in the source.
2. Translate the body, keeping the signature unchanged.
3. If the translation needs an auxiliary helper (e.g. a local `let rec`), prefer local definitions inside the `def` over new top-level declarations.
4. Add `-- NOTE:` comments for any lossy modelling, as described above.

### End-of-phase summary

After editing, emit a short plain-text summary that `pr` will use:

```
LEAN_FILE: <LEAN_PATH>
FUNCTIONS_FILLED:
- <def name>
- <def name>
FUNCTIONS_LEFT_SORRY:
- <def name> — <reason>
DIVERGENCES:
- <short one-liner for each -- NOTE: added>
```

If no `def` was actually filled (every one had to be left `sorry` with a NOTE, or you found none to fill after all), say so explicitly. The `pr` phase will decline to open an empty PR in that case.
