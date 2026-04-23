# Invariants: `cli/internal/intentcheck`

Status: **ratified v1** (2026-04-22). Ratified by: Harry Nicholls (PR #698, merged 2026-04-23).

This document is the load-bearing specification for the `intentcheck` package,
which contains the pure algorithmic core of the `xylem-intent-check` binary.
It is protected: changes require human review (see **Governance**). Agent-authored
PRs that relax an invariant without an accompanying `.claude/rules/protected-surfaces.md`
amendment must be rejected.

---

## Contract

The `intentcheck` package provides the **pure, I/O-free functions** that
constitute the security-critical core of the xylem intent-check pipeline.
The pipeline guards against intent drift: if an invariant spec (`docs/invariants/*.md`)
and its property test (`cli/internal/*/invariants_prop_test.go`) fall out of
agreement, the pipeline must detect it and record a `fail` verdict.

The functions in this package carry obligations that cannot be delegated to the
LLM or the filesystem layer:

- **Which files reach the pipeline** (`ResolveCounterparts`) — a miss here is a
  silent bypass: one side of a protected pair is checked without the other.
- **How the LLM verdict is interpreted** (`ParseDiffResult`) — a parsing failure
  that silently becomes a `pass` would defeat the entire check.
- **Tamper-evidence** (`ComputeContentHash`) — the content hash anchors the
  attestation to the exact file versions checked.

---

## Invariants

**I1. Completeness.**
Every file in the input set appears in the output of `ResolveCounterparts`.
Formal: `∀f ∈ input. f ∈ ResolveCounterparts(root, input)`.
- *Why:* `ResolveCounterparts` is called with the git-diff result. If it dropped
  an input file, the pipeline would silently omit a changed protected file from
  the intent-check run — a critical bypass.
- *Test:* rapid sequence of arbitrary protected file sets; after each call, assert
  every input file is present in the output.
  `// Invariant I1: Completeness` in `intentcheck_invariants_prop_test.go`.

**I2. Counterpart coverage.**
If the disk-resident counterpart of an input file exists (i.e. `FileExists(counterpart(f))`),
that counterpart appears in the output of `ResolveCounterparts`.
Formal: `∀f ∈ input. FileExists(counterpart(f)) ⟹ counterpart(f) ∈ ResolveCounterparts(root, input)`.
Counterpart mapping: `docs/invariants/<M>.md ↔ cli/internal/<M>/*_invariants_prop_test.go`.
- *Why:* the pipeline requires both sides of each pair regardless of which side
  triggered the run. Asymmetric changes (doc-only or test-only edits) must still
  deliver both sides to the LLM phases.
- *Test:* populate both sides of the pair on disk; pass only one side as input;
  assert both appear in output.
  `// Invariant I2: CounterpartCoverage` in `intentcheck_invariants_prop_test.go`.

**I3. Deduplication.**
The output of `ResolveCounterparts` contains no file path more than once.
Formal: `∀p. |{r ∈ ResolveCounterparts(root, input) : r = p}| ≤ 1`.
- *Why:* duplicate entries would cause the same file content to be hashed and
  sent to the LLM twice, producing a wrong content hash and a bloated prompt.
  Specifically, if both sides of a pair are already in the input, adding them
  again would produce duplicates.
- *Test:* pass both sides of a pair as input; assert output length matches
  distinct file count.
  `// Invariant I3: Deduplication` in `intentcheck_invariants_prop_test.go`.

**I4. Sorted output.**
The output of `ResolveCounterparts` is lexicographically sorted.
Formal: `∀i < j. ResolveCounterparts(root, input)[i] ≤ ResolveCounterparts(root, input)[j]`.
- *Why:* sorted order is required for deterministic content hash computation
  (`ComputeContentHash` iterates in the order it receives). If the sort order
  varied across runs, the same logical change would produce different hashes,
  breaking attestation comparability.
- *Test:* rapid input; assert `sort.StringsAreSorted(result)` after every call.
  `// Invariant I4: Sorted` in `intentcheck_invariants_prop_test.go`.

**I5. Idempotence.**
Applying `ResolveCounterparts` twice produces the same result as once.
Formal: `ResolveCounterparts(root, ResolveCounterparts(root, input)) = ResolveCounterparts(root, input)`.
- *Why:* the caller may invoke `ResolveCounterparts` on a list that was already
  resolved (e.g. in retry logic or test harnesses). Idempotence prevents
  unbounded list growth and ensures the function can be applied defensively.
- *Test:* rapid input; apply twice; assert both outputs are identical.
  `// Invariant I5: Idempotent` in `intentcheck_invariants_prop_test.go`.

**I6. Fail-closed parsing.**
`ParseDiffResult` with an input that cannot be parsed into a valid `DiffResult`
must return a non-nil error AND a `DiffResult` with `Match=false`.
The pipeline must never silently treat a parse failure as a passing verdict.
Formal: `ParseDiffResult(s) = (dr, err) ∧ err ≠ nil ⟹ dr.Match = false`.
- *Why:* the diff-checker LLM may return malformed output (refusal, truncation,
  provider error messages). A parse failure that silently becomes `Match=true`
  would allow every protected-surface change to be attested as `pass` whenever
  the LLM misbehaves — defeating the entire pipeline.
- *Test:* pass known-bad inputs (empty string, non-JSON, plain text); assert
  both that `err ≠ nil` and that `dr.Match == false`.
  `// Invariant I6: FailClosed` in `intentcheck_invariants_prop_test.go`.

---

## Not covered

- **LLM correctness.** Whether the back-translation accurately describes the
  test code, or whether the diff-checker's verdict is semantically correct. These
  are probabilistic and belong to prompt engineering and evaluation, not invariant
  specification.
- **Git discovery completeness.** Whether `discoverChangedFiles` finds all
  protected files that changed. The glob patterns are the contract; changes to
  the glob are governed by the calling binary's maintenance.
- **Attestation tamper-resistance.** The attestation file is written to disk; its
  integrity after writing is outside this package's scope.
- **API availability.** LLM provider availability, rate-limiting, and timeout
  behaviour are outside this package's scope.

---

## Gap analysis

Reviewed 2026-04-22 against `cli/internal/intentcheck/intentcheck.go` at initial
extraction from `cli/cmd/xylem-intent-check/main.go`.

| Invariant | Status | Site | Note |
|---|---|---|---|
| I1 | ✓ | `ResolveCounterparts` | Input files copied to result before counterpart augmentation. |
| I2 | ✓ | `ResolveCounterparts` | Counterpart added iff `os.Stat` succeeds and not already seen. |
| I3 | ✓ | `ResolveCounterparts` | `seen` map prevents duplicate additions; copy into result preserves uniqueness. |
| I4 | ✓ | `ResolveCounterparts` | `sort.Strings(result)` at return. |
| I5 | ✓ | `ResolveCounterparts` | I1+I3+I4 together imply idempotence: second call adds no new entries (counterparts already in list), produces same sorted set. Property test pins this. |
| I6 | ✓ | `ParseDiffResult` | Returns zero-value `DiffResult{}` (Match=false) on all error paths. |

**Summary:** all invariants currently hold. No known violations.

---

## Governance

1. **Spec location.** This file: `docs/invariants/intentcheck.md`. Changes require
   a human-signed commit. Agent-authored PRs that edit this file without explicit
   human direction must be rejected.
2. **Test location.** `cli/internal/intentcheck/intentcheck_invariants_prop_test.go`.
   Every property test must carry a `// Invariant IN: <Name>` comment linking it
   to the spec entry above.
3. **Protected surfaces.** `.claude/rules/protected-surfaces.md` already covers
   both `docs/invariants/*.md` and `cli/internal/*/*_invariants_prop_test.go`,
   which includes both files above. No extension required.
4. **CI enforcement.** The property tests run under the existing `go test ./...`
   path in CI. No known violations; all tests are expected to pass.
5. **Self-referential coverage.** `discoverChangedFiles` in the `xylem-intent-check`
   binary discovers changes to this doc and its property tests via the existing
   globs. Edits to either file will trigger an intent-check run, making this
   specification self-protecting.

**Amendment procedure:** an invariant may only be relaxed via a PR that (a) edits
this document, (b) is authored or signed by a human, and (c) includes rationale
tied to a real constraint (not "the test was failing"). Agents may propose
amendments but may not merge them.
