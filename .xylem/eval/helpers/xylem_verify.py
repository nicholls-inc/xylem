import glob
import json
import os


STATE_DIR_CANDIDATES = [".xylem", ".xylem-state"]
EVIDENCE_RANK = {
    "proved": 4,
    "mechanically_checked": 3,
    "behaviorally_checked": 2,
    "observed_in_situ": 1,
    "": 0,
}


def state_dir(work_dir: str) -> str:
    for candidate in STATE_DIR_CANDIDATES:
        path = os.path.join(work_dir, candidate)
        if os.path.isdir(path):
            return path
    return os.path.join(work_dir, ".xylem")


def reward_dir(task_dir: str | None = None) -> str:
    candidates = [
        os.environ.get("HARBOR_VERIFIER_DIR"),
        os.environ.get("VERIFIER_DIR"),
        "/logs/verifier",
        task_dir,
    ]
    for candidate in candidates:
        if candidate and os.path.isdir(candidate):
            return candidate
    return task_dir or os.getcwd()


def find_vessel_dir(work_dir: str) -> str:
    """Locate the single vessel directory under the xylem state dir."""
    pattern = os.path.join(state_dir(work_dir), "phases", "*", "summary.json")
    matches = glob.glob(pattern)
    assert len(matches) == 1, f"Expected 1 vessel dir, found {len(matches)}: {matches}"
    return os.path.dirname(matches[0])


def load_summary(work_dir: str) -> dict:
    vessel_dir = find_vessel_dir(work_dir)
    with open(os.path.join(vessel_dir, "summary.json"), encoding="utf-8") as f:
        return json.load(f)


def load_evidence(work_dir: str) -> dict | None:
    vessel_dir = find_vessel_dir(work_dir)
    path = os.path.join(vessel_dir, "evidence-manifest.json")
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def load_phase_output(work_dir: str, phase_name: str) -> str | None:
    vessel_dir = find_vessel_dir(work_dir)
    path = os.path.join(vessel_dir, f"{phase_name}.output")
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as f:
        return f.read()


def load_audit_log(work_dir: str) -> list[dict]:
    path = os.path.join(state_dir(work_dir), "audit.jsonl")
    if not os.path.exists(path):
        return []
    entries = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                entries.append(json.loads(line))
    return entries


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
    if not checks:
        return 0.0
    if weights is None:
        weights = {name: 1.0 for name, _ in checks}
    total_weight = sum(weights.get(name, 1.0) for name, _ in checks)
    earned = sum(weights.get(name, 1.0) for name, passed in checks if passed)
    return earned / total_weight if total_weight > 0 else 0.0


def max_evidence_level(manifest: dict | None) -> str:
    if not manifest:
        return ""
    best = ""
    for claim in manifest.get("claims", []):
        if claim.get("passed") and EVIDENCE_RANK.get(claim.get("level", ""), 0) > EVIDENCE_RANK.get(best, 0):
            best = claim.get("level", "")
    return best


def evidence_score(level: str) -> float:
    return EVIDENCE_RANK.get(level, 0) / 4.0


def count_phase_retries(summary: dict) -> int:
    seen = {}
    retries = 0
    for phase in summary.get("phases", []):
        name = phase.get("name", "")
        seen[name] = seen.get(name, 0) + 1
        if seen[name] > 1:
            retries += 1
    return retries


def count_tool_failures(summary: dict) -> int:
    failures = 0
    for phase in summary.get("phases", []):
        if phase.get("status") != "completed" or phase.get("error"):
            failures += 1
    return failures


def count_policy_violations(audit: list[dict]) -> int:
    return sum(1 for entry in audit if entry.get("decision") == "deny")


def build_result(
    task_id: str,
    summary: dict,
    manifest: dict | None,
    audit: list[dict],
    checks: list[tuple[str, bool]],
    score: float,
) -> dict:
    level = max_evidence_level(manifest)
    return {
        "schema_version": "1",
        "task_id": task_id,
        "reward": score,
        "success": summary.get("state") == "completed",
        "latency_seconds": round(summary.get("duration_ms", 0) / 1000.0, 4),
        "cost_usd_est": summary.get("total_cost_usd_est", 0.0),
        "retry_count": count_phase_retries(summary),
        "tool_failure_count": count_tool_failures(summary),
        "policy_violation_count": count_policy_violations(audit),
        "evidence_score": evidence_score(level),
        "evidence_level": level,
        "budget_exceeded": bool(summary.get("budget_exceeded", False)),
        "checks": [{"name": name, "passed": passed} for name, passed in checks],
    }


def write_reward(task_dir: str, score: float):
    with open(os.path.join(reward_dir(task_dir), "reward.txt"), "w", encoding="utf-8") as f:
        f.write(f"{score:.4f}\n")


def write_result(task_dir: str, result: dict):
    output_dir = reward_dir(task_dir)
    os.makedirs(output_dir, exist_ok=True)
    write_reward(task_dir, float(result.get("reward", 0.0)))
    with open(os.path.join(output_dir, "reward.json"), "w", encoding="utf-8") as f:
        json.dump(result, f, indent=2, sort_keys=True)
        f.write("\n")
