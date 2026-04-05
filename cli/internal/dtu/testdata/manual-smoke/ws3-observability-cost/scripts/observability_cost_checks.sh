#!/usr/bin/env bash
set -euo pipefail

WORKDIR_ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
CLI_ROOT="$(CDPATH= cd -- "$WORKDIR_ROOT/../../../../.." && pwd)"

cd "$CLI_ROOT"

echo "[ws3] running observability helper smoke tests"
go test -count=1 ./internal/observability -run '^(TestSmoke_S(1_|2_|3_|4_|5_).+|TestNewTracer(WithEndpoint|WithEndpointShutdown|Default|Shutdown))$'

echo "[ws3] running cost helper smoke tests"
go test -count=1 ./internal/cost -run '^(TestSmoke_S(17_|18_|19_|20_|21_|22_|23_|24_).+|Test(BudgetExceededAlert|TokenBudgetExceeded))$'
