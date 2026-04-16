Write the new `formal-verification/CRITIQUE.md` based on the analysis.

## Analysis
{{.PreviousOutputs.analyze}}

You are the sole writer of `formal-verification/CRITIQUE.md`. Replace it wholesale; do not append.

## Doctrine (read this before writing)

This file is a teaching artefact as much as it is a status document. Readers arrive here wanting to know whether they can trust the proofs. Your job is to make them better at reading proofs critically, regardless of their prior formal-verification experience.

- "Proved" means: a Lean 4 theorem with no `sorry`, no unproved `axiom`, no `unsafe`, compiled by `lake build`. It is a claim about the *model*, not the *real source*. The quality of the proof is upper-bounded by the quality of the types and definitions it operates on.
- `decide` proofs are only as strong as the types they decide over. A `decide`-proved theorem about a wrapped `Int` says nothing about the production code's 64-bit overflow behaviour unless the types explicitly model it.
- Tested and proved are not the same. A test exhibits a behaviour on one input. A proof asserts it for every element of a type. Gaps in the type are gaps in the proof.
- `sorry` is a hole. An `axiom` is a larger hole with a name. Both are worth keeping in the tree — they are progress markers — but they are NOT guarantees.

## Required structure

Write the file with exactly these top-level sections, in this order:

```
# Lean Squad Critique

> 🔬 This file is produced by the `lean-squad-critique` workflow. It is intentionally sceptical. If something here reads as defensive, that is the point.

Last updated: <ISO-8601 UTC timestamp you generate>.

## How to read this file

<2-4 paragraphs teaching the reader what "proved" means in this codebase. Spell out the difference between proved and tested. Explain that `decide` and `axiom` weaken the claim. Name the model–reality gap explicitly. Keep it plain-English; no Lean syntax assumed.>

## What we have actually proved

<One subsection per genuinely-proved theorem, or a tight table. For each: theorem name, file path, the English statement of what it guards, and ONE honest caveat about what its types *do not* capture. If the list is empty, say so in one sentence and do not pad.>

## What we have NOT proved

<Two groups. First: theorems still carrying `sorry` (list them; say what is therefore unguaranteed). Second: declarations using `axiom` or `unsafe` (list them; state the assumed-but-unchecked property). Be explicit that a `sorry` is not a defect — it is a flag that proof work is still open.>

## Where our model diverges from reality

<One bullet per known divergence between the Lean model and the source. Typical categories: concurrency un-modelled; error paths abstracted away; external I/O turned into a pure function; bounded integer overflow ignored; panics/timeouts absent; partial functions modelled as total via `Option`; invariants that hold in tests but not in adversarial inputs. Cite the source file and the Lean file. If `formal-verification/CORRESPONDENCE.md` exists, treat its "caveats" column as a starting list and add anything the correspondence author missed.>

## Coverage gaps

<What source modules or public behaviours have no informal spec at all. What behaviours have informal specs but no formal translation. What formal specs still have zero proved theorems. The table from `TARGETS.md` is a good input but not a substitute for reading the source.>

## Recommended next targets

<3-5 ranked recommendations, each: `<target>` — `<why this is the highest-leverage gap>` — `<suggested lean-squad-* workflow>`. These become inputs for the tick's weighted scheduling next run. Rank by expected proof-value per unit effort; call out anything that is tractable today versus blocked on a dependency.>

## Known limitations of this critique

<Explicit list of what THIS critique cannot see. Examples: it did not run the build; it read files but did not trace call graphs; it trusts the timestamps on other artefacts; it cannot detect a wrong `axiom` that happens to be type-correct. Naming these keeps future readers calibrated.>
```

## Rules

1. Specific over general. `Theorem parse_roundtrip holds for well-formed input but says nothing about inputs rejected by the lexer` beats `parsing might have edge cases`.
2. Concrete file paths and theorem names everywhere. No hand-waving.
3. Honest stock-take, not progress report. If no theorem is genuinely proved yet, say so directly in the summary sentence.
4. Do not dress up gaps as progress. A `sorry` is not a "planned enhancement". An `axiom` is not a "conservative assumption". Call them what they are.
5. Never touch any file other than `formal-verification/CRITIQUE.md`.
6. Do not run `lake build`. Do not modify Lean sources. Read-only everywhere except the critique itself.

When you finish, emit `LEAN_SQUAD_RUN:` in the terminal output as a single line with this exact shape so the tick's retrospect phase can reconcile it:

```
LEAN_SQUAD_RUN: {"task":"lean-squad-critique","target":"CRITIQUE","status":"ok","artefact":"formal-verification/CRITIQUE.md"}
```

Use `"status":"partial"` if you had to skip a section for lack of data (say why in the file), and `"status":"blocked"` only if you could not write the file at all.
