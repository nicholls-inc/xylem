import os

import xylem_verify as xv


def test_pr_reporting(work_dir, task_dir, verify):
    checks = []

    summary = verify.load_summary(work_dir)
    vessel_id = summary.get("id", "")
    checks.append(("vessel_completed", summary["state"] == "completed"))

    # Check that the report phase output contains the vessel ID
    report_output = verify.load_phase_output(work_dir, "report")
    report_contains_id = bool(
        report_output and vessel_id and vessel_id in report_output
    )
    checks.append(("report_contains_id", report_contains_id))

    # Check evidence level is at least observed_in_situ
    manifest = verify.load_evidence(work_dir)
    evidence_found = False
    if manifest:
        for claim in manifest.get("claims", []):
            if claim.get("passed"):
                evidence_found = (
                    xv.EVIDENCE_RANK.get(claim["level"], 0)
                    >= xv.EVIDENCE_RANK["observed_in_situ"]
                )
                break
    checks.append(("evidence_level_ok", evidence_found))

    checks.append(("budget_ok", not summary.get("budget_exceeded", False)))

    weights = {
        "vessel_completed": 3.0,
        "report_contains_id": 2.0,
        "evidence_level_ok": 1.0,
        "budget_ok": 1.0,
    }
    score = verify.compute_reward(checks, weights)
    verify.write_reward(task_dir, score)

    if os.environ.get("XYLEM_EVAL_CAPTURE_BASELINE"):
        phases = summary.get("phases", [])
        latency = verify.load_phase_latency(summary, phases[0]["name"] if phases else "")
        evidence_level = ""
        if manifest:
            for claim in manifest.get("claims", []):
                if claim.get("passed"):
                    evidence_level = claim.get("level", "")
                    break
        verify.write_baseline(
            task_dir, "pr-reporting", checks, score, summary,
            latency or 0.0, evidence_level,
        )

    assert score >= 0.8, f"Reward {score:.2f} below threshold. Checks: {checks}"
