#!/usr/bin/env bash
# verify-kernels.sh — runs dafny_verify on every .dfy file changed on this branch.
# Called by the verify_kernel workflow phase (roadmap #08).
#
# Exit codes:
#   0  — no .dfy changes, or all changed specs verify, or Docker/image absent (soft fallback)
#   1  — one or more specs failed verification
set -euo pipefail

DOCKER_IMAGE="${DAFNY_DOCKER_IMAGE:-crosscheck-dafny:latest}"

# Make origin/main available for the 3-dot diff.
git fetch origin main 2>/dev/null || true

# 3-dot diff: only changes introduced on this branch, not upstream commits.
changed=$(git diff --name-only origin/main...HEAD 2>/dev/null | grep '\.dfy$' || true)

if [ -z "$changed" ]; then
  echo "verify-kernel: no .dfy files changed — skipping"
  exit 0
fi

echo "verify-kernel: changed .dfy files:"
echo "$changed" | sed 's/^/  /'

# Soft fallback when Docker is absent (CI environments without Docker).
if ! command -v docker > /dev/null 2>&1; then
  echo "verify-kernel: WARNING: Docker not available — skipping (pre-commit is only enforcement)"
  exit 0
fi

# `timeout` ships with GNU coreutils; absent on stock macOS (requires brew install coreutils).
# Fall back to running without a process-level timeout — Dafny's own 60s limit still applies.
if command -v timeout > /dev/null 2>&1; then
  TIMEOUT_PREFIX="timeout 130"
else
  echo "verify-kernel: WARNING: timeout(1) not available — no process-level timeout enforced"
  TIMEOUT_PREFIX=""
fi

# Soft fallback when the Dafny image hasn't been built yet.
if ! docker image inspect "$DOCKER_IMAGE" > /dev/null 2>&1; then
  echo "verify-kernel: WARNING: image $DOCKER_IMAGE not found — skipping"
  echo "verify-kernel: build it with: scripts/build-docker.sh in the crosscheck plugin directory"
  exit 0
fi

failed=0
while IFS= read -r dfy_file; do
  if [ ! -f "$dfy_file" ]; then
    echo "verify-kernel: $dfy_file deleted on branch, skipping"
    continue
  fi
  echo "=== Verifying: $dfy_file ==="
  abs_file=$(realpath "$dfy_file")
  dir=$(dirname "$abs_file")
  filename=$(basename "$abs_file")
  # shellcheck disable=SC2086
  if ! $TIMEOUT_PREFIX docker run --rm \
      --network=none --memory=512m --cpus=1 \
      -v "$dir:/work" "$DOCKER_IMAGE" \
      verify "/work/$filename"; then
    echo "FAILED: $dfy_file"
    failed=1
  else
    echo "OK: $dfy_file"
  fi
done <<< "$changed"

if [ "$failed" -ne 0 ]; then
  echo "verify-kernel: one or more specs failed verification"
  exit 1
fi
echo "verify-kernel: all changed specs verified"
