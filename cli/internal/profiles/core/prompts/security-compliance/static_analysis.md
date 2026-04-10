You are running the scheduled `security-compliance` workflow for this repository.

Review the repository for static-analysis signals that matter to repository security posture.

## Inputs

- Vessel ID: {{.Vessel.ID}}
- Secret scan report:
{{.PreviousOutputs.scan_secrets}}

## What to check

1. Prefer security-relevant analyzers that are actually available in the repo/toolchain, such as `actionlint`, `go vet`, `staticcheck`, `semgrep`, `zizmor`, `poutine`, or language-native linters with security coverage.
2. Detect the repo shape first so you only run relevant analyzers.
3. Capture both true findings and meaningful tooling gaps.
4. Keep the repo read-only.
5. Do not open GitHub issues in this phase.

## Deliverable

End with this exact heading structure:

RESULT: CLEAN | FINDINGS | TOOLING-GAP
SEVERITY: NONE | LOW | MEDIUM | HIGH | CRITICAL
TOOLS_RUN:
- tool name — command and outcome, or `unavailable`
SUMMARY:
- concise overall conclusion
FINDINGS:
- one bullet per actionable static-analysis finding or important tooling gap
FOLLOW_UP:
- one bullet per recommended next step

If everything is clean, keep `FINDINGS:` and `FOLLOW_UP:` present and state `- none`.
