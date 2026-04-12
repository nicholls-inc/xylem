import os


def test_label_gate_waiting_resume(work_dir, task_dir, verify):
    checks = []

    summary = verify.load_summary(work_dir)
    checks.append(("vessel_completed", summary["state"] == "completed"))

    # Check that a waiting state was observed at some point via audit log
    audit = verify.load_audit_log(work_dir)
    waiting_observed = any(
        entry.get("state") == "waiting" or entry.get("new_state") == "waiting"
        for entry in audit
    )
    # Also check phase-level waiting status as a fallback
    if not waiting_observed:
        waiting_observed = any(
            p.get("status") == "waiting" for p in summary.get("phases", [])
        )
    checks.append(("waiting_state_observed", waiting_observed))

    # Check that a label gate was present in the workflow
    gate_found = any(
        p.get("gate_type") == "label" for p in summary.get("phases", [])
    )
    checks.append(("gate_type_label", gate_found))

    checks.append(("budget_ok", not summary.get("budget_exceeded", False)))

    weights = {
        "vessel_completed": 3.0,
        "waiting_state_observed": 2.0,
        "gate_type_label": 2.0,
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
            task_dir, "label-gate-waiting-resume", checks, score, summary,
            latency or 0.0, evidence_level,
        )

    assert score >= 0.75, f"Reward {score:.2f} below threshold. Checks: {checks}"
