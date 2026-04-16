You are running the `analyze` phase of `lean-squad-aeneas` (Task 8 of the Lean Squad system).

See `https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md` for the upstream doctrine. The Lean Squad applies Lean 4 formal verification progressively and optimistically. You do **not** need prior FV expertise — Aeneas translates Rust to Lean for you.

## About Aeneas (read this before anything else)

**Aeneas** (`https://github.com/AeneasVerif/aeneas`) is a translator from Rust → Lean 4. It works by ingesting Rust's MIR (the compiler's mid-level IR) through a tool called **Charon**, and emitting faithful Lean 4 definitions. Aeneas handles ownership and lifetimes by threading a **state monad** through the generated code: mutable borrows become explicit state reads and writes at the Lean level. The output is **faithful but verbose** — you prove theorems *about* the generated code, not *on* it directly. That's a feature: the generated module is the single source of truth that future Lean Squad tasks can reason against without re-parsing Rust.

Because Aeneas only operates on Rust code, this workflow is a **no-op on non-Rust repos**.

## Vessel context

- Vessel ID: {{.Vessel.ID}}

This workflow is orphaned — the tick enqueues it without a target argument, so there is no `Vessel.Ref` to parse.

## Your job in this phase

Decide whether this vessel has work to do, and if so, identify which Rust crates to translate. Nothing else — do not run Charon or Aeneas yet.

### Step 1 — Is this a Rust repo?

Check whether a `Cargo.toml` exists at the repo root. If **no** `Cargo.toml` exists, emit the exact standalone line `XYLEM_NOOP` followed by a one-line explanation ("no Cargo.toml at repo root — Aeneas only operates on Rust code"). Stop here.

Workspace layouts are valid too: a root `Cargo.toml` with `[workspace]` is still a Rust repo.

### Step 2 — Is the Aeneas output already up to date?

Inspect `formal-verification/lean/FVSquad/Aeneas/Generated/`.

- If that directory does not exist, or is empty, the translation has never run. Proceed to Step 3.
- If it contains `.lean` files, compare their content timestamps (via `git log -1 --format=%cI -- <file>`) against the most recent commit touching any `src/**/*.rs` or `Cargo.toml` file. If every generated file is newer than the newest Rust source commit, emit `XYLEM_NOOP` with a one-line explanation ("Aeneas-generated Lean is already up to date"). The tick will pick another target next run.
- Otherwise, proceed to Step 3.

Do **not** modify any file in this phase.

### Step 3 — Identify crates to translate

List each crate that should be translated. For a single-crate repo this is just the root. For a workspace, enumerate member crates from `[workspace] members = [...]`.

Exclude crates that are obviously not candidates for translation:
- `*-macros`, `*-derive`, or anything that is purely a proc-macro (Aeneas cannot translate proc-macros)
- `build.rs`-only helper crates
- Test-only fixture crates (ones marked `publish = false` **and** living under `tests/` or `examples/`)

If after exclusions no crates remain, emit `XYLEM_NOOP` ("no translation-eligible crates after filtering").

### Step 4 — Emit a structured summary

If Steps 1–3 succeeded without emitting `XYLEM_NOOP`, produce a plain-text summary that later phases can read verbatim:

```
REPO_SHAPE: <single-crate | workspace>
CRATES_TO_TRANSLATE:
- name: <crate-name>
  path: <relative-path-from-repo-root>
  rust_edition: <2021 | 2024 | ...>
EXCLUDED_CRATES:
- name: <crate-name>
  reason: <short reason>
NOTES:
- <short note on anything unusual — nightly-only features, feature flags that gate large modules, etc.>
```

For `path`, use `.` for a single-crate root. Do not fabricate paths — if a member entry uses a glob (`crates/*`), expand it with `ls` rather than guessing.

Do **not** start writing Lean or invoking Aeneas in this phase. That is `implement`'s job.
