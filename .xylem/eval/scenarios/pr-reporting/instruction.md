# Task: Run workflow and verify phase report output

## Issue

Verify that after a full workflow execution, `xylem report` produces output
that includes the vessel ID and references the completed phases.

## Context

You are working in a repository that uses xylem for autonomous task execution.
The repository has a `.xylem.yml` configuration and a `fix-bug` workflow that
produces phase outputs.

## What to do

1. Run `xylem enqueue --source manual --prompt 'Document the main package' --workflow fix-bug`
2. Run `xylem drain` to execute all phases
3. After drain completes, run `xylem report --json` and capture the output
4. Verify the output contains the vessel ID and at least one completed phase name
5. Inspect `xylem status` to confirm the vessel reached `completed`

## Constraints

- Do not modify `.xylem.yml` or any files under `.xylem/`.
- Work only within the repository root.
- Do not add features beyond what is requested.
