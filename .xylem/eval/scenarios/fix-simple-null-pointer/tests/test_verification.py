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
    if manifest:
        for claim in manifest["claims"]:
            if claim["phase"] == "implement" and claim["passed"]:
                evidence_found = True
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

    weights = {
        "vessel_completed": 3.0,
        "phases_completed": 2.0,
        "gate_passed": 2.0,
        "evidence_level": 1.0,
        "budget_ok": 1.0,
    }
    score = verify.compute_reward(checks, weights)
    verify.write_reward(task_dir, score)

    assert score >= 0.8, f"Reward {score:.2f} below threshold. Checks: {checks}"
