# DTU manual smoke fixture repos

These directories are checked-in repo seeds for Guide 4B manual smoke tests.
Each fixture is meant to be passed as `--workdir` to `xylem dtu env` so the real
CLI runs against a repo layout that already contains `.xylem.yml`,
`.xylem/workflows/`, prompts, harness text, and WS5 eval assets where needed.

The fixtures keep mutable runtime state under `.xylem-state/` so the checked-in
`.xylem/` scaffolding stays reusable across runs.

WS1 uses the dedicated shared repo at `../ws1-smoke-fixture/`, which carries
scenario-specific config files inside one seeded repo. The per-manifest repos in
this directory cover WS3-WS6.

## Usage

```bash
cd /Users/harry.nicholls/repos/xylem/cli
go build ./cmd/xylem
XYLEM_BIN="$PWD/xylem"

eval "$("$XYLEM_BIN" dtu env \
  --manifest ./internal/dtu/testdata/ws3-observability-cost.yaml \
  --workdir ./internal/dtu/testdata/manual-smoke/ws3-observability-cost)"

(
  cd "$XYLEM_DTU_WORKDIR" || exit 1
  "$XYLEM_BIN" --config .xylem.yml scan
  "$XYLEM_BIN" --config .xylem.yml drain
  "$XYLEM_BIN" --config .xylem.yml status
)
```

## Fixture map

| Manifest | Seeded workdir |
| --- | --- |
| `ws3-observability-cost.yaml` | `manual-smoke/ws3-observability-cost/` |
| `ws3-summary-artifacts.yaml` | `manual-smoke/ws3-summary-artifacts/` |
| `ws4-evidence-model.yaml` | `manual-smoke/ws4-evidence-model/` |
| `ws5-eval-suite.yaml` | `manual-smoke/ws5-eval-suite/` |
| `ws6-cross-cutting.yaml` | `manual-smoke/ws6-cross-cutting/` |
