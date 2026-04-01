import glob
import json
import os


def find_vessel_dir(work_dir: str) -> str:
    """Locate the single vessel directory under .xylem/phases/."""
    pattern = os.path.join(work_dir, ".xylem", "phases", "*", "summary.json")
    matches = glob.glob(pattern)
    assert len(matches) == 1, f"Expected 1 vessel dir, found {len(matches)}: {matches}"
    return os.path.dirname(matches[0])


def load_summary(work_dir: str) -> dict:
    """Load and parse .xylem/phases/<id>/summary.json."""
    vessel_dir = find_vessel_dir(work_dir)
    with open(os.path.join(vessel_dir, "summary.json"), encoding="utf-8") as f:
        return json.load(f)


def load_evidence(work_dir: str) -> dict | None:
    """Load evidence-manifest.json, or None if absent."""
    vessel_dir = find_vessel_dir(work_dir)
    path = os.path.join(vessel_dir, "evidence-manifest.json")
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def load_phase_output(work_dir: str, phase_name: str) -> str | None:
    """Load a phase's .output file, or None if absent."""
    vessel_dir = find_vessel_dir(work_dir)
    path = os.path.join(vessel_dir, f"{phase_name}.output")
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as f:
        return f.read()


def load_audit_log(work_dir: str) -> list[dict]:
    """Load .xylem/audit.jsonl as a list of entries."""
    path = os.path.join(work_dir, ".xylem", "audit.jsonl")
    if not os.path.exists(path):
        return []
    entries = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                entries.append(json.loads(line))
    return entries


EVIDENCE_RANK = {
    "proved": 4,
    "mechanically_checked": 3,
    "behaviorally_checked": 2,
    "observed_in_situ": 1,
    "": 0,
}


def assert_vessel_completed(work_dir: str):
    summary = load_summary(work_dir)
    assert summary["state"] == "completed", f"Vessel state: {summary['state']}"


def assert_vessel_failed(work_dir: str):
    summary = load_summary(work_dir)
    assert summary["state"] == "failed", f"Vessel state: {summary['state']}"


def assert_phases_completed(summary: dict, phase_names: list[str]):
    completed = [p["name"] for p in summary["phases"] if p["status"] == "completed"]
    for name in phase_names:
        assert name in completed, f"Phase {name} not completed. Completed: {completed}"


def assert_gates_passed(summary: dict, phase_names: list[str]):
    for phase in summary["phases"]:
        if phase["name"] in phase_names and phase.get("gate_type"):
            assert phase.get("gate_passed") is True, (
                f"Gate for phase {phase['name']} did not pass"
            )


def assert_evidence_level(manifest: dict, phase_name: str, min_level: str):
    for claim in manifest["claims"]:
        if claim["phase"] == phase_name and claim["passed"]:
            actual_rank = EVIDENCE_RANK.get(claim["level"], 0)
            min_rank = EVIDENCE_RANK.get(min_level, 0)
            assert actual_rank >= min_rank, (
                f"Phase {phase_name}: evidence {claim['level']} < {min_level}"
            )
            return
    assert False, f"No passing evidence claim found for phase {phase_name}"


def assert_cost_within_budget(summary: dict):
    assert not summary.get("budget_exceeded", False), "Budget exceeded"


def compute_reward(
    checks: list[tuple[str, bool]], weights: dict[str, float] | None = None
) -> float:
    """Compute 0.0-1.0 reward from named pass/fail checks with optional weights."""
    if not checks:
        return 0.0
    if weights is None:
        weights = {name: 1.0 for name, _ in checks}
    total_weight = sum(weights.get(name, 1.0) for name, _ in checks)
    earned = sum(weights.get(name, 1.0) for name, passed in checks if passed)
    return earned / total_weight if total_weight > 0 else 0.0


def write_reward(task_dir: str, score: float):
    """Write reward.txt for Harbor."""
    with open(os.path.join(task_dir, "reward.txt"), "w", encoding="utf-8") as f:
        f.write(f"{score:.4f}\n")
