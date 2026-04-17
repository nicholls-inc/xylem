#!/usr/bin/env python3
"""Enforce 1:1 coverage between `.foxguard/baseline.json` and `.foxguard/justifications.md`.

Every fingerprint suppressed in the baseline must have a corresponding entry in the
justifications file with a non-empty rationale and verifier. This prevents the baseline
from becoming a silent dumping ground for unreviewed findings.

Exit codes:
  0  - every baseline fingerprint has a complete justification
  1  - one or more baseline entries are missing justifications (or are incomplete)
  2  - input files are malformed or missing
"""

from __future__ import annotations

import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
BASELINE_PATH = REPO_ROOT / ".foxguard" / "baseline.json"
JUSTIFICATIONS_PATH = REPO_ROOT / ".foxguard" / "justifications.md"

FINGERPRINT_RE = re.compile(r"^Fingerprint:\s*`([0-9a-f]{64})`\s*$", re.MULTILINE)
HEADING_RE = re.compile(r"^##\s+(?!Required format\b)(.+?)\s*$", re.MULTILINE)
RATIONALE_RE = re.compile(r"\*\*Rationale:\*\*\s*(\S.*?)(?=\n{2,}|\n\*\*Verified by:|$)", re.DOTALL)
VERIFIED_RE = re.compile(r"\*\*Verified by:\*\*\s*(\S.*?)$", re.MULTILINE)


@dataclass
class Justification:
    fingerprint: str
    heading: str
    rationale: str
    verified_by: str


def load_baseline() -> list[dict]:
    if not BASELINE_PATH.exists():
        return []
    try:
        data = json.loads(BASELINE_PATH.read_text())
    except json.JSONDecodeError as exc:
        print(f"ERROR: {BASELINE_PATH} is not valid JSON: {exc}", file=sys.stderr)
        sys.exit(2)
    entries = data.get("entries", [])
    if not isinstance(entries, list):
        print(f"ERROR: {BASELINE_PATH}: 'entries' must be a list", file=sys.stderr)
        sys.exit(2)
    return entries


def parse_justifications() -> tuple[dict[str, Justification], list[str]]:
    """Return (fingerprint→Justification, list of parse errors)."""
    if not JUSTIFICATIONS_PATH.exists():
        print(f"ERROR: {JUSTIFICATIONS_PATH} is missing", file=sys.stderr)
        sys.exit(2)

    text = JUSTIFICATIONS_PATH.read_text()
    # Split into sections by level-2 heading. The first chunk (before any heading) is
    # the preamble and is ignored. Everything after the "Required format" heading up
    # to the horizontal rule is also a documentation section with no fingerprint.
    parts = re.split(r"(?m)^##\s+", text)
    justifications: dict[str, Justification] = {}
    errors: list[str] = []

    for chunk in parts[1:]:
        # chunk starts with the heading text on the first line
        newline = chunk.find("\n")
        heading = chunk[:newline].strip() if newline >= 0 else chunk.strip()
        body = chunk[newline + 1 :] if newline >= 0 else ""

        fp_match = FINGERPRINT_RE.search(body)
        if not fp_match:
            # Not a justification section (e.g. "Required format") — skip.
            continue
        fingerprint = fp_match.group(1)

        rationale_match = RATIONALE_RE.search(body)
        verified_match = VERIFIED_RE.search(body)

        rationale = rationale_match.group(1).strip() if rationale_match else ""
        verified_by = verified_match.group(1).strip() if verified_match else ""

        if fingerprint in justifications:
            errors.append(f"duplicate justification for fingerprint {fingerprint[:12]}… (heading: {heading!r})")
            continue

        if not rationale:
            errors.append(f"justification {fingerprint[:12]}… ({heading!r}) missing **Rationale:** body")
        if not verified_by:
            errors.append(f"justification {fingerprint[:12]}… ({heading!r}) missing **Verified by:** line")

        justifications[fingerprint] = Justification(
            fingerprint=fingerprint,
            heading=heading,
            rationale=rationale,
            verified_by=verified_by,
        )

    return justifications, errors


def main() -> int:
    baseline_entries = load_baseline()
    justifications, parse_errors = parse_justifications()

    baseline_fps = {e["fingerprint"]: e for e in baseline_entries if "fingerprint" in e}
    justification_fps = set(justifications.keys())

    missing = [fp for fp in baseline_fps if fp not in justification_fps]
    orphans = [fp for fp in justification_fps if fp not in baseline_fps]

    errors: list[str] = list(parse_errors)

    if missing:
        errors.append(
            f"{len(missing)} baseline entry/entries lack a justification in "
            f"{JUSTIFICATIONS_PATH.relative_to(REPO_ROOT)}:"
        )
        for fp in missing:
            entry = baseline_fps[fp]
            errors.append(
                f"  - {entry.get('file', '?')}:{entry.get('line', '?')} "
                f"[{entry.get('rule_id', '?')}] fingerprint={fp}"
            )
        errors.append(
            "\nAdd a section following the format in "
            f"{JUSTIFICATIONS_PATH.relative_to(REPO_ROOT)}. "
            "See CLAUDE.md § Foxguard protocol for the full workflow."
        )

    if errors:
        print("foxguard justifications check FAILED:", file=sys.stderr)
        for err in errors:
            print(f"  {err}", file=sys.stderr)
        return 1

    if orphans:
        # Orphans don't fail the build — they just signal cleanup. Print as warning.
        print(
            f"foxguard justifications: {len(orphans)} orphan justification(s) "
            "(baseline no longer contains these fingerprints — consider removing):",
            file=sys.stderr,
        )
        for fp in orphans:
            print(f"  - {justifications[fp].heading} (fingerprint={fp[:12]}…)", file=sys.stderr)

    print(
        f"foxguard justifications OK: {len(baseline_fps)} baseline entry/entries, "
        f"{len(justification_fps)} justification(s)."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
