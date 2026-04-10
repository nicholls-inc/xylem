You are running the scheduled `security-compliance` workflow for this repository.

Audit the dependency surface for known vulnerabilities and stale remediation work.

## Inputs

- Vessel ID: {{.Vessel.ID}}
- Secret scan report:
{{.PreviousOutputs.scan_secrets}}

- Static analysis report:
{{.PreviousOutputs.static_analysis}}

## What to check

1. Detect which ecosystems are present before choosing tools.
2. Prefer ecosystem-native or security-specific tooling when available, such as `govulncheck`, `npm audit`, `pnpm audit`, `pip-audit`, `poetry audit`, `cargo audit`, `bundler-audit`, or `osv-scanner`.
3. Record missing tooling when the repo has dependencies but no suitable auditor is installed.
4. If GitHub access is available, inspect open `security`-labeled issues to spot overdue remediation work and mention it in the output.
5. Keep the repo read-only in this phase.

## Deliverable

End with this exact heading structure:

RESULT: CLEAN | FINDINGS | TOOLING-GAP
SEVERITY: NONE | LOW | MEDIUM | HIGH | CRITICAL
TOOLS_RUN:
- tool name — command and outcome, or `unavailable`
SUMMARY:
- concise overall conclusion
FINDINGS:
- one bullet per actionable vulnerability, stale remediation item, or meaningful tooling gap
FOLLOW_UP:
- one bullet per recommended remediation, upgrade, or tracking action

If no issues are found, keep `FINDINGS:` and `FOLLOW_UP:` present and state `- none`.
