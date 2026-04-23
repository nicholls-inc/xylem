#!/usr/bin/env bash
# check-intent-attestation.sh — pre-commit hook that enforces xylem-intent-check
# was run before committing changes to protected surfaces.
#
# Protected surfaces (from .claude/rules/protected-surfaces.md):
#   docs/invariants/*.md
#   cli/internal/*/invariants_prop_test.go
#   cli/internal/*/*_invariants_prop_test.go
#
# The hook reads .xylem/intent-check-attestation.json and verifies:
#   1. The attestation file exists.
#   2. The verdict is "pass".
#   3. The content hash matches the current contents of the attested files.
#   4. All staged protected files are covered by the attestation.
#
# Exit codes:
#   0  — no protected files staged, or all checks pass
#   1  — attestation missing, stale, verdict not pass, or files not covered
#
# NOTE: chmod +x scripts/check-intent-attestation.sh if running directly.
# The .pre-commit-config.yaml entry uses "bash scripts/check-intent-attestation.sh"
# so the executable bit is not required for pre-commit operation.
set -euo pipefail

ATTESTATION=".xylem/intent-check-attestation.json"

# ── 1. Collect staged files ──────────────────────────────────────────────────

staged=$(git diff --cached --name-only 2>/dev/null || true)

if [ -z "$staged" ]; then
  exit 0
fi

# ── 2. Filter against protected-surface patterns ─────────────────────────────
#
# Patterns:
#   docs/invariants/*.md
#   cli/internal/*/invariants_prop_test.go
#   cli/internal/*/*_invariants_prop_test.go

protected_staged=""
while IFS= read -r f; do
  case "$f" in
    docs/invariants/*.md) protected_staged="${protected_staged}${f}"$'\n' ;;
    cli/internal/*/invariants_prop_test.go) protected_staged="${protected_staged}${f}"$'\n' ;;
    cli/internal/*/*_invariants_prop_test.go) protected_staged="${protected_staged}${f}"$'\n' ;;
  esac
done <<< "$staged"

# Strip trailing newline and check if anything matched.
protected_staged="${protected_staged%$'\n'}"

if [ -z "$protected_staged" ]; then
  exit 0
fi

# ── 3. Check jq availability ─────────────────────────────────────────────────

if ! command -v jq > /dev/null 2>&1; then
  echo "jq required for intent-check attestation check — install with: brew install jq" >&2
  exit 1
fi

# ── 4. Check attestation file exists ─────────────────────────────────────────

if [ ! -f "$ATTESTATION" ]; then
  echo "intent-check attestation missing or stale. Run: xylem-intent-check — then re-stage .xylem/intent-check-attestation.json and retry the commit." >&2
  exit 1
fi

# ── 5. Check verdict == "pass" ───────────────────────────────────────────────

verdict=$(jq -r '.verdict // empty' "$ATTESTATION" 2>/dev/null || true)

if [ "$verdict" != "pass" ]; then
  echo "intent-check attestation verdict is not 'pass'. Run: xylem-intent-check — then re-stage .xylem/intent-check-attestation.json and retry the commit." >&2
  exit 1
fi

# ── 6. Recompute and verify content hash ─────────────────────────────────────

computed_hash=$(python3 -c "
import sys, json, hashlib
try:
    attestation = json.load(open('$ATTESTATION'))
    files = sorted(attestation.get('protected_files', []))
    h = hashlib.sha256()
    for f in files:
        with open(f, 'rb') as fh:
            h.update(fh.read())
    print(h.hexdigest())
except Exception as e:
    print('ERROR: ' + str(e), file=sys.stderr)
    sys.exit(1)
" 2>&1) || {
  echo "intent-check attestation is stale (content hash mismatch). Run: xylem-intent-check — then re-stage .xylem/intent-check-attestation.json and retry the commit." >&2
  exit 1
}

stored_hash=$(jq -r '.content_hash // empty' "$ATTESTATION" 2>/dev/null || true)

if [ "$computed_hash" != "$stored_hash" ]; then
  echo "intent-check attestation is stale (content hash mismatch). Run: xylem-intent-check — then re-stage .xylem/intent-check-attestation.json and retry the commit." >&2
  exit 1
fi

# ── 7. Check all staged protected files are covered ──────────────────────────

attested_files=$(jq -r '.protected_files[]? // empty' "$ATTESTATION" 2>/dev/null || true)

while IFS= read -r staged_file; do
  [ -z "$staged_file" ] && continue
  if ! echo "$attested_files" | grep -qxF "$staged_file"; then
    echo "intent-check attestation does not cover all staged protected files. Run: xylem-intent-check — then re-stage .xylem/intent-check-attestation.json and retry the commit." >&2
    exit 1
  fi
done <<< "$protected_staged"

exit 0
