import os

import xylem_verify as xv


def test_vessel_outcome(work_dir, task_dir, verify):
    checks = []

    summary = verify.load_summary(work_dir)
    checks.append(("vessel_completed", summary["state"] == "completed"))

    completed = {p["name"] for p in summary["phases"] if p["status"] == "completed"}
    checks.append(("phases_completed", {"diagnose", "implement"}.issubset(completed)))

    gate_found = False
    for phase in summary["phases"]:
        if phase["name"] == "implement" and phase.get("gate_type") == "command":
            gate_found = True
            checks.append(("gate_passed", phase.get("gate_passed") is True))
            break
    if not gate_found:
        checks.append(("gate_passed", False))

    manifest = verify.load_evidence(work_dir)
    evidence_found = False
    evidence_level = ""
    if manifest:
        for claim in manifest["claims"]:
            if claim["phase"] == "implement" and claim["passed"]:
                evidence_found = True
                evidence_level = claim.get("level", "")
                checks.append(
                    (
                        "evidence_level",
                        xv.EVIDENCE_RANK.get(claim["level"], 0)
                        >= xv.EVIDENCE_RANK["behaviorally_checked"],
                    )
                )
                break
    if not evidence_found:
        checks.append(("evidence_level", False))

    checks.append(("budget_ok", not summary.get("budget_exceeded", False)))

    # Wire rubric scoring for the diagnose phase output
    rubric = verify.load_rubric("plan_quality")
    if rubric:
        diagnose_output = verify.load_phase_output(work_dir, "diagnose")
        if diagnose_output:
            rubric_score = verify.score_with_rubric(diagnose_output, rubric)
            checks.append(("plan_quality_rubric", rubric_score >= 0.5))

    weights = {
        "vessel_completed": 3.0,
        "phases_completed": 2.0,
        "gate_passed": 2.0,
        "evidence_level": 1.0,
        "budget_ok": 1.0,
        "plan_quality_rubric": 0.5,
    }
    score = verify.compute_reward(checks, weights)
    verify.write_reward(task_dir, score)

    if os.environ.get("XYLEM_EVAL_CAPTURE_BASELINE"):
        phases = summary.get("phases", [])
        latency = verify.load_phase_latency(summary, "diagnose")
        verify.write_baseline(
            task_dir, "fix-simple-null-pointer", checks, score, summary,
            latency or 0.0, evidence_level,
        )

    assert score >= 0.8, f"Reward {score:.2f} below threshold. Checks: {checks}"
