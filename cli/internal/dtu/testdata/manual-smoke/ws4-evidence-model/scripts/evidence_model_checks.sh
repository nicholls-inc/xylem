#!/usr/bin/env bash
set -euo pipefail

WORKDIR_ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
CLI_ROOT="$(CDPATH= cd -- "$WORKDIR_ROOT/../../../../.." && pwd)"

cd "$CLI_ROOT"

echo "[ws4] running evidence package smoke tests"
go test -count=1 ./internal/evidence -run '^TestSmoke_S(1_|2_|3_|4_|5_|6_|7_|8_|9_|10_).+$'
