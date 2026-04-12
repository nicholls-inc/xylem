import os


def test_scan_enqueue_drain(work_dir, task_dir, verify):
    checks = []

    summary = verify.load_summary(work_dir)
    checks.append(("vessel_completed", summary["state"] == "completed"))

    phases = summary.get("phases", [])
    checks.append(("phases_ran", len(phases) > 0))

    checks.append(("budget_ok", not summary.get("budget_exceeded", False)))

    weights = {
        "vessel_completed": 3.0,
        "phases_ran": 2.0,
        "budget_ok": 1.0,
    }
    score = verify.compute_reward(checks, weights)
    verify.write_reward(task_dir, score)

    if os.environ.get("XYLEM_EVAL_CAPTURE_BASELINE"):
        latency = verify.load_phase_latency(summary, phases[0]["name"] if phases else "")
        evidence = verify.load_evidence(work_dir)
        evidence_level = ""
        if evidence:
            for claim in evidence.get("claims", []):
                if claim.get("passed"):
                    evidence_level = claim.get("level", "")
                    break
        verify.write_baseline(
            task_dir, "scan-enqueue-drain", checks, score, summary,
            latency or 0.0, evidence_level,
        )

    assert score >= 0.8, f"Reward {score:.2f} below threshold. Checks: {checks}"
