def test_pr_reporting_path(work_dir, task_dir, verify):
    checks = []

    summary = verify.load_summary(work_dir)
    checks.append(("vessel_completed", summary["state"] == "completed"))

    pr_completed = False
    for phase in summary["phases"]:
        if phase["name"] == "pr" and phase["status"] == "completed":
            pr_completed = True
            break
    checks.append(("pr_phase_completed", pr_completed))

    pr_output = verify.load_phase_output(work_dir, "pr")
    checks.append(("pr_output_present", bool(pr_output and pr_output.strip())))
    checks.append(("summary_tracks_cost", summary.get("total_cost_usd_est", 0.0) >= 0.0))

    weights = {
        "vessel_completed": 3.0,
        "pr_phase_completed": 3.0,
        "pr_output_present": 2.0,
        "summary_tracks_cost": 1.0,
    }
    score = verify.compute_reward(checks, weights)
    audit = verify.load_audit_log(work_dir)
    verify.write_result(
        task_dir,
        verify.build_result(
            "pr-reporting-path",
            summary,
            verify.load_evidence(work_dir),
            audit,
            checks,
            score,
        ),
    )

    assert score >= 0.8, f"Reward {score:.2f} below threshold. Checks: {checks}"
