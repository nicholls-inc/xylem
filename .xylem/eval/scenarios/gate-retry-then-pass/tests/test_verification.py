import xylem_verify as xv


def test_gate_retry_then_pass(work_dir, task_dir, verify):
    checks = []

    summary = verify.load_summary(work_dir)
    checks.append(("vessel_completed", summary["state"] == "completed"))

    implement_gate_passed = False
    for phase in summary["phases"]:
        if phase["name"] == "implement" and phase.get("gate_type") == "command":
            implement_gate_passed = phase.get("gate_passed") is True
            break
    checks.append(("implement_gate_passed", implement_gate_passed))
    checks.append(("phase_retried", verify.count_phase_retries(summary) >= 1))

    manifest = verify.load_evidence(work_dir)
    checks.append(
        (
            "evidence_level",
            xv.EVIDENCE_RANK.get(verify.max_evidence_level(manifest), 0)
            >= xv.EVIDENCE_RANK["behaviorally_checked"],
        )
    )

    weights = {
        "vessel_completed": 3.0,
        "implement_gate_passed": 3.0,
        "phase_retried": 2.0,
        "evidence_level": 1.0,
    }
    score = verify.compute_reward(checks, weights)
    audit = verify.load_audit_log(work_dir)
    verify.write_result(
        task_dir,
        verify.build_result(
            "gate-retry-then-pass",
            summary,
            manifest,
            audit,
            checks,
            score,
        ),
    )

    assert score >= 0.8, f"Reward {score:.2f} below threshold. Checks: {checks}"
