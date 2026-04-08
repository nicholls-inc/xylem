def test_label_gate_resume(work_dir, task_dir, verify):
    checks = []

    summary = verify.load_summary(work_dir)
    checks.append(("vessel_completed", summary["state"] == "completed"))

    completed = {p["name"] for p in summary["phases"] if p["status"] == "completed"}
    checks.append(("workflow_completed", {"analyze", "plan", "implement", "pr"}.issubset(completed)))

    label_gate_passed = False
    for phase in summary["phases"]:
        if phase["name"] == "plan" and phase.get("gate_type") == "label":
            label_gate_passed = phase.get("gate_passed") is True
            break
    checks.append(("label_gate_passed", label_gate_passed))

    checks.append(("pr_output_present", bool(verify.load_phase_output(work_dir, "pr"))))

    weights = {
        "vessel_completed": 3.0,
        "workflow_completed": 2.0,
        "label_gate_passed": 3.0,
        "pr_output_present": 1.0,
    }
    score = verify.compute_reward(checks, weights)
    audit = verify.load_audit_log(work_dir)
    verify.write_result(
        task_dir,
        verify.build_result(
            "label-gate-resume",
            summary,
            verify.load_evidence(work_dir),
            audit,
            checks,
            score,
        ),
    )

    assert score >= 0.8, f"Reward {score:.2f} below threshold. Checks: {checks}"
