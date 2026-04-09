Convert the SoTA gap-analysis inventory into the committed snapshot format.

Inputs to use:

1. `{{.PreviousOutputs.ingest}}`
2. `.xylem/state/sota-gap-snapshot.schema.json`
3. `docs/best-practices/harness-engineering.md`
4. `docs/design/sota-agent-harness-spec.md`
5. The live implementation under `cli/internal/` plus `cli/cmd/xylem/`, `cli/internal/runner/`, and `cli/internal/scanner/`

Write exactly one JSON file at:

`.xylem/state/sota-gap-snapshot.next.json`

Requirements:

1. The JSON MUST validate against `.xylem/state/sota-gap-snapshot.schema.json`.
2. Include all ten capability buckets.
3. For each capability, set `status` to exactly one of:
   - `wired`
   - `dormant`
   - `not-implemented`
4. Every capability must include:
   - a stable `key`
   - human-readable `name`
   - `layer`
   - `summary`
   - `recommendation`
   - integer `priority` (higher means more urgent/value-dense)
   - `spec_sections`
   - `code_evidence` with precise file paths and line numbers
5. Use `wired` only when the capability is actively exercised in xylem's primary execution path today.
6. Use `dormant` when code exists but is not materially wired into the primary path.
7. Use `not-implemented` when the capability is absent or only implied by docs/specs.

After writing the file, print a short summary listing each capability key and status. Do not print the full JSON inline.
