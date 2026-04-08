# Task: Recover from a command-gate failure

## Issue

The fix-bug workflow is expected to fail its command gate on the first attempt,
repair the issue, and then pass on a retry without human intervention.

## Context

You are working in a repository that uses xylem for autonomous task execution.
The repository has a `fix-bug` workflow with a command gate after the implement
phase.

## What to do

1. Queue the bug-fix task with xylem.
2. Run xylem until the implement phase hits a gate failure.
3. Let xylem retry the phase, then confirm the gate passes and the vessel
   completes successfully.
4. Inspect the final summary and evidence outputs.

## Constraints

- Do not modify `.xylem.yml` or any files under `.xylem/`.
- Work only within the repository root.
