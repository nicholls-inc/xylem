import glob
import json
import os

try:
    import tomllib
except ImportError:
    try:
        import tomli as tomllib  # type: ignore[no-redef]
    except ImportError:
        tomllib = None  # type: ignore[assignment]


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


def load_phase_latency(summary: dict, phase_name: str) -> float | None:
    """Extract duration_seconds from a named phase in the summary, or None."""
    for phase in summary.get("phases", []):
        if phase.get("name") == phase_name:
            return phase.get("duration_seconds") or phase.get("latency_seconds")
    return None


def load_rubric(rubric_name: str) -> dict:
    """Load .xylem/eval/rubrics/<rubric_name>.toml relative to repo root.

    Falls back to searching upward from this file's location so tests can
    find the rubrics directory regardless of working directory.
    """
    if tomllib is None:
        return {}

    # Try to find the rubrics directory relative to this helper file
    helpers_dir = os.path.dirname(os.path.abspath(__file__))
    rubrics_dir = os.path.join(helpers_dir, "..", "rubrics")
    rubric_path = os.path.join(rubrics_dir, f"{rubric_name}.toml")

    if not os.path.exists(rubric_path):
        return {}

    with open(rubric_path, "rb") as f:
        return tomllib.load(f)


def load_baseline(scenario_id: str, baselines_dir: str) -> dict | None:
    """Load baselines/<scenario_id>.json, or None if absent."""
    path = os.path.join(baselines_dir, f"{scenario_id}.json")
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def write_baseline(
    task_dir: str,
    scenario_id: str,
    checks: list[tuple[str, bool]],
    reward: float,
    summary: dict,
    latency_seconds: float,
    evidence_level: str,
) -> None:
    """Write a baseline JSON to .xylem/eval/baselines/<scenario_id>.json.

    The baselines directory is resolved from task_dir by walking upward to
    find the eval directory, then writing to baselines/ within it.
    """
    from datetime import datetime, timezone

    # Resolve baselines_dir relative to task_dir (task_dir is the scenario dir)
    helpers_dir = os.path.dirname(os.path.abspath(__file__))
    baselines_dir = os.path.join(helpers_dir, "..", "baselines")
    os.makedirs(baselines_dir, exist_ok=True)

    # Extract task version from task.toml if present
    version = "1"
    task_toml = os.path.join(task_dir, "task.toml")
    if tomllib is not None and os.path.exists(task_toml):
        with open(task_toml, "rb") as f:
            task_data = tomllib.load(f)
        version = str(task_data.get("task", {}).get("version", "1"))

    baseline = {
        "scenario_id": scenario_id,
        "version": version,
        "captured_at": datetime.now(timezone.utc).isoformat(),
        "reward": reward,
        "checks": [{"name": name, "passed": passed} for name, passed in checks],
        "summary_state": summary.get("state", ""),
        "latency_seconds": latency_seconds,
        "budget_exceeded": summary.get("budget_exceeded", False),
        "evidence_level": evidence_level,
    }

    path = os.path.join(baselines_dir, f"{scenario_id}.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(baseline, f, indent=2)
        f.write("\n")


def compare_to_baseline(
    current_checks: list[tuple[str, bool]],
    current_reward: float,
    baseline: dict,
    regression_threshold: float = 0.05,
) -> dict:
    """Diff current run against a stored baseline.

    Returns a dict with:
      regressions  — checks that passed in baseline but fail now, plus a
                     "reward_drop" entry if the reward fell by more than
                     regression_threshold
      improvements — checks that failed in baseline but pass now
      delta        — current_reward - baseline["reward"]
    """
    baseline_check_map = {
        c["name"]: c["passed"] for c in baseline.get("checks", [])
    }
    current_check_map = dict(current_checks)

    regressions = []
    improvements = []

    all_names = set(baseline_check_map) | set(current_check_map)
    for name in sorted(all_names):
        was_passing = baseline_check_map.get(name, False)
        now_passing = current_check_map.get(name, False)
        if was_passing and not now_passing:
            regressions.append(name)
        elif not was_passing and now_passing:
            improvements.append(name)

    delta = current_reward - baseline.get("reward", 0.0)
    if delta < -regression_threshold:
        regressions.append("reward_drop")

    return {
        "regressions": regressions,
        "improvements": improvements,
        "delta": delta,
    }


def score_with_rubric(phase_output: str, rubric: dict) -> float:
    """Score phase output text against a rubric using keyword heuristics.

    Each criterion is evaluated by checking for indicator keywords in the
    output. Returns a weighted score in [0.0, 1.0].
    """
    if not rubric or not phase_output:
        return 0.0

    criteria = rubric.get("rubric", {}).get("criteria", [])
    if not criteria:
        return 0.0

    # Keyword sets per criterion name (heuristic, not ML)
    _CRITERION_KEYWORDS: dict[str, list[str]] = {
        "root_cause_identification": [
            "root cause", "because", "caused by", "reason:", "the issue is",
            "nil pointer", "null", "dereference", "panic",
        ],
        "reasoning_chain": [
            "therefore", "thus", "since", "as a result", "which means",
            "this leads to", "consequently", "follows that",
        ],
        "scope_accuracy": [
            "only", "minimal", "limited to", "scope", "without changing",
            "no other", "focused", "targeted",
        ],
        "trust_boundary_clarity": [
            "verified", "not verified", "boundary", "trust", "assumption",
            "confirmed", "unconfirmed",
        ],
        "evidence_completeness": [
            "evidence", "claim", "all phases", "complete", "covered",
            "missing", "gap",
        ],
    }

    output_lower = phase_output.lower()
    weighted_sum = 0.0
    total_weight = 0.0

    for criterion in criteria:
        name = criterion.get("name", "")
        weight = float(criterion.get("weight", 1.0))
        keywords = _CRITERION_KEYWORDS.get(name, [])

        if keywords:
            matched = sum(1 for kw in keywords if kw.lower() in output_lower)
            # Score is proportion of keywords matched, capped at 1.0
            criterion_score = min(1.0, matched / max(1, len(keywords) // 2))
        else:
            # No keywords defined: neutral 0.5 score
            criterion_score = 0.5

        weighted_sum += weight * criterion_score
        total_weight += weight

    return weighted_sum / total_weight if total_weight > 0 else 0.0


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
