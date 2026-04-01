def test_surface_violation(work_dir, task_dir, verify):
    checks = []

    summary = verify.load_summary(work_dir)
    checks.append(("vessel_failed", summary["state"] == "failed"))

    audit = verify.load_audit_log(work_dir)
    has_violation = any(
        entry.get("decision") == "deny"
        and "file_write" in entry.get("intent", {}).get("action", "")
        for entry in audit
    )
    checks.append(("violation_logged", has_violation))

    score = verify.compute_reward(checks)
    verify.write_reward(task_dir, score)

    assert score >= 0.9, f"Reward {score:.2f}. Checks: {checks}"
