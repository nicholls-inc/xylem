#!/usr/bin/env python3
"""Enforce invariant-to-test coverage mapping between specs and property tests.

For every module with an invariant spec at ``docs/invariants/<module>.md``, this
script verifies that every invariant ID declared in the doc appears in at least
one ``// Invariant <ID>: ...`` comment inside the module's property-test file
(``cli/internal/<module>/*invariants_prop_test.go``). Conversely, every such
comment must refer to an invariant declared in that module's doc.

Invariant headers in the doc look like:

    **I1. At-most-one active per ref.**
    **I1a. Enqueue of an active ref is a no-op.**
    **S3. Pause marker aborts the tick before any side effect.**

Test-side comments look like:

    // Invariant I1: At-most-one active per ref.
    // Invariant S3: Pause marker aborts the tick before any side effect.

Headers carrying the literal HTML comment ``<!-- aspirational -->`` on the same
line are exempt from the missing-coverage check. Orphan-comment checks still
apply: a test comment whose ID is not declared in the module doc is always an
error.

Coverage scoping is **per module**, not global. Queue's ``I1`` and runner's
``I1`` refer to different invariants; each is expected to be covered in its own
package's property-test file.

Exit codes:
  0 - every required invariant is covered; no orphan comments
  1 - one or more coverage violations
  2 - input files missing or malformed
"""

from __future__ import annotations

import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
DOCS_DIR = REPO_ROOT / "docs" / "invariants"
INTERNAL_DIR = REPO_ROOT / "cli" / "internal"

# Invariant header: **<prefix><digits>[suffix]. ...** at line start.
# Group 1 is the identifier (e.g. I1, I1a, S8, I14).
HEADER_RE = re.compile(r"^\*\*([A-Z]+\d+[a-z]?)\.\s", re.MULTILINE)

# Aspirational opt-out marker on the same line as the header.
ASPIRATIONAL_RE = re.compile(
    r"^\*\*([A-Z]+\d+[a-z]?)\..*<!--\s*aspirational\s*-->",
    re.MULTILINE,
)

# Test-side comment: // Invariant <ID>: ... (literal "Invariant", ID, colon).
# Matches only at the start of a stripped line so in-prose refs do not match.
TEST_COMMENT_RE = re.compile(r"^//\s*Invariant\s+([A-Z]+\d+[a-z]?):\s")


@dataclass
class ModuleCoverage:
    module: str
    doc_path: Path
    test_paths: list[Path]
    declared: set[str] = field(default_factory=set)
    aspirational: set[str] = field(default_factory=set)
    covered: set[str] = field(default_factory=set)
    comment_sites: list[tuple[str, Path, int]] = field(default_factory=list)


def discover_modules() -> list[ModuleCoverage]:
    """Locate (module, doc, test_paths) triples for every invariant doc."""
    if not DOCS_DIR.is_dir():
        print(f"ERROR: {DOCS_DIR} is missing", file=sys.stderr)
        sys.exit(2)

    modules: list[ModuleCoverage] = []
    for doc in sorted(DOCS_DIR.glob("*.md")):
        stem = doc.stem
        if stem.lower() == "readme":
            continue
        module_dir = INTERNAL_DIR / stem
        test_paths = sorted(module_dir.glob("*invariants_prop_test.go")) if module_dir.is_dir() else []
        modules.append(
            ModuleCoverage(module=stem, doc_path=doc, test_paths=test_paths)
        )
    return modules


def load_doc(cov: ModuleCoverage) -> None:
    text = cov.doc_path.read_text()
    for m in HEADER_RE.finditer(text):
        cov.declared.add(m.group(1))
    for m in ASPIRATIONAL_RE.finditer(text):
        cov.aspirational.add(m.group(1))


def load_tests(cov: ModuleCoverage) -> None:
    for path in cov.test_paths:
        for lineno, line in enumerate(path.read_text().splitlines(), start=1):
            m = TEST_COMMENT_RE.match(line.lstrip())
            if m:
                inv_id = m.group(1)
                cov.covered.add(inv_id)
                cov.comment_sites.append((inv_id, path, lineno))


def rel(path: Path) -> str:
    try:
        return str(path.relative_to(REPO_ROOT))
    except ValueError:
        return str(path)


def main() -> int:
    modules = discover_modules()
    if not modules:
        print(f"ERROR: no invariant docs found under {DOCS_DIR}", file=sys.stderr)
        return 2

    failures: list[str] = []

    for cov in modules:
        load_doc(cov)

        if not cov.declared:
            failures.append(
                f"{rel(cov.doc_path)}: no invariant headers found "
                f"(expected lines like `**I1. ...**`)"
            )
            continue

        if not cov.test_paths:
            expected_dir = INTERNAL_DIR / cov.module
            failures.append(
                f"{rel(cov.doc_path)}: no property test file found at "
                f"{rel(expected_dir)}/*invariants_prop_test.go"
            )
            continue

        load_tests(cov)

        # Missing coverage: declared in doc and not aspirational, yet no comment.
        required = cov.declared - cov.aspirational
        for inv_id in sorted(required - cov.covered):
            failures.append(
                f"{rel(cov.doc_path)}: invariant {inv_id} is declared but has no "
                f"`// Invariant {inv_id}: ...` comment in "
                f"{rel(INTERNAL_DIR / cov.module)}/*invariants_prop_test.go"
            )

        # Orphan comments: test refers to an ID not in the module doc.
        for inv_id in sorted(cov.covered - cov.declared):
            sites = ", ".join(
                f"{rel(p)}:{ln}"
                for _id, p, ln in cov.comment_sites
                if _id == inv_id
            )
            failures.append(
                f"{rel(cov.doc_path)}: `// Invariant {inv_id}:` refers to an invariant "
                f"not declared in this module doc (sites: {sites})"
            )

    if failures:
        print("invariant coverage check FAILED:", file=sys.stderr)
        for msg in failures:
            print(f"  {msg}", file=sys.stderr)
        print(
            "\nFix by either:\n"
            "  - adding a `// Invariant <ID>: <Name>` comment above the test that "
            "enforces the invariant,\n"
            "  - updating the spec so the ID is declared, or\n"
            "  - marking the header with `<!-- aspirational -->` if it is "
            "intentionally not yet covered.\n"
            "See docs/invariants/README.md for the full workflow.",
            file=sys.stderr,
        )
        return 1

    total_declared = sum(len(m.declared) for m in modules)
    total_aspirational = sum(len(m.aspirational) for m in modules)
    total_required = total_declared - total_aspirational
    total_covered = sum(len((m.declared - m.aspirational) & m.covered) for m in modules)
    print(
        f"invariant coverage OK: {total_covered}/{total_required} required "
        f"invariants covered across {len(modules)} module(s) "
        f"({total_aspirational} aspirational)."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
