#!/usr/bin/env bash
set -euo pipefail

WORKDIR_ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$WORKDIR_ROOT"

python3 - <<'PY'
import ast
import os
import stat
import sys
import tempfile
import tomllib
from pathlib import Path

workdir = Path.cwd()
eval_root = workdir / ".xylem" / "eval"
sys.path.insert(0, str(eval_root / "helpers"))
import xylem_verify as xv


def parse_simple_yaml(path: Path) -> dict[str, str]:
    data: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        key, value = line.split(":", 1)
        data[key.strip()] = value.strip()
    return data


harbor = parse_simple_yaml(eval_root / "harbor.yaml")
assert harbor["agent"] == "claude-code"
assert harbor["path"] == "scenarios/"
assert int(harbor["n_attempts"]) > 0
assert int(harbor["n_concurrent"]) > 0

required_names = [
    "find_vessel_dir",
    "load_summary",
    "load_evidence",
    "load_phase_output",
    "load_audit_log",
    "assert_vessel_completed",
    "assert_vessel_failed",
    "assert_phases_completed",
    "assert_gates_passed",
    "assert_evidence_level",
    "assert_cost_within_budget",
    "compute_reward",
    "write_reward",
    "EVIDENCE_RANK",
]
missing = [name for name in required_names if not hasattr(xv, name)]
assert not missing, f"Missing helper exports: {missing}"

required_levels = {
    "proved",
    "mechanically_checked",
    "behaviorally_checked",
    "observed_in_situ",
    "",
}
assert required_levels <= set(xv.EVIDENCE_RANK.keys())
assert (
    xv.EVIDENCE_RANK["proved"]
    > xv.EVIDENCE_RANK["mechanically_checked"]
    > xv.EVIDENCE_RANK["behaviorally_checked"]
    > xv.EVIDENCE_RANK["observed_in_situ"]
    > xv.EVIDENCE_RANK[""]
)

assert xv.compute_reward([("a", True), ("b", True)]) == 1.0
assert xv.compute_reward([("a", True), ("b", False)]) == 0.5
weighted = xv.compute_reward(
    [("vessel_completed", True), ("gate_passed", False)],
    {"vessel_completed": 3.0, "gate_passed": 1.0},
)
assert weighted == 0.75, weighted

with tempfile.TemporaryDirectory() as tmp:
    xv.write_reward(tmp, 0.75)
    reward = Path(tmp, "reward.txt").read_text(encoding="utf-8").strip()
    assert reward == "0.7500"

conftest_tree = ast.parse((eval_root / "helpers" / "conftest.py").read_text(encoding="utf-8"))
fixture_funcs = {
    node.name for node in ast.walk(conftest_tree) if isinstance(node, ast.FunctionDef)
}
for name in ("work_dir", "task_dir", "verify"):
    assert name in fixture_funcs, f"fixture missing: {name}"

scenario = eval_root / "scenarios" / "widget-bug"
assert scenario.is_dir(), f"scenario missing: {scenario}"
for rel in ("instruction.md", "task.toml", "tests/test.sh", "tests/test_verification.py"):
    path = scenario / rel
    assert path.exists(), f"missing scenario artifact: {path}"

mode = os.stat(scenario / "tests" / "test.sh").st_mode
assert mode & stat.S_IXUSR, "tests/test.sh must be executable"

task = tomllib.loads((scenario / "task.toml").read_text(encoding="utf-8"))
assert task["task"]["id"] == "widget-bug"
assert task["task"]["environment"]["timeout_seconds"] > 0

instruction_text = (scenario / "instruction.md").read_text(encoding="utf-8").lower()
for word in ("harbor", "scoring", "verification"):
    assert word not in instruction_text, f"forbidden term in instruction: {word}"
assert "xylem" in instruction_text

with open(eval_root / "rubrics" / "plan_quality.toml", "rb") as handle:
    plan = tomllib.load(handle)
assert plan["rubric"]["name"] == "plan_quality"
assert abs(sum(c["weight"] for c in plan["rubric"]["criteria"]) - 1.0) < 0.001

with open(eval_root / "rubrics" / "evidence_quality.toml", "rb") as handle:
    evidence = tomllib.load(handle)
assert evidence["rubric"]["name"] == "evidence_quality"
assert abs(sum(c["weight"] for c in evidence["rubric"]["criteria"]) - 1.0) < 0.001

resolved = eval_root / harbor["path"]
assert resolved.is_dir(), f"scenario path missing: {resolved}"
assert not list(resolved.glob("**/Dockerfile")), "deferred Dockerfile unexpectedly present"
assert not (eval_root / "jobs").exists(), "deferred jobs dir unexpectedly present"
assert not (workdir / "jobs").exists(), "unexpected repo-root jobs dir present"

print("WS5 eval scaffold checks passed")
PY
