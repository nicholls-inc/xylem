import os


def test_failure_recovery(work_dir, task_dir, verify):
    checks = []

    summary = verify.load_summary(work_dir)
    checks.append(("vessel_completed", summary["state"] == "completed"))

    # Check that a retry event was recorded in the audit log
    audit = verify.load_audit_log(work_dir)
    retry_observed = any(
        entry.get("event") == "retry"
        or entry.get("action") == "retry"
        or entry.get("new_state") == "pending"  # re-enqueue transition
        for entry in audit
    )
    # Also check if any phase has a retry_count > 0
    if not retry_observed:
        retry_observed = any(
            p.get("retry_count", 0) > 0 for p in summary.get("phases", [])
        )
    checks.append(("retry_observed", retry_observed))

    checks.append(("budget_ok", not summary.get("budget_exceeded", False)))

    weights = {
        "vessel_completed": 3.0,
        "retry_observed": 3.0,
        "budget_ok": 1.0,
    }
    score = verify.compute_reward(checks, weights)
    verify.write_reward(task_dir, score)

    if os.environ.get("XYLEM_EVAL_CAPTURE_BASELINE"):
        phases = summary.get("phases", [])
        latency = verify.load_phase_latency(summary, phases[0]["name"] if phases else "")
        evidence = verify.load_evidence(work_dir)
        evidence_level = ""
        if evidence:
            for claim in evidence.get("claims", []):
                if claim.get("passed"):
                    evidence_level = claim.get("level", "")
                    break
        verify.write_baseline(
            task_dir, "failure-recovery", checks, score, summary,
            latency or 0.0, evidence_level,
        )

    assert score >= 0.75, f"Reward {score:.2f} below threshold. Checks: {checks}"
