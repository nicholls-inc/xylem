# Task: Exercise label-gate waiting and resume flow

## Issue

Verify that a workflow with a `label` gate correctly suspends the vessel into
`waiting` state and that `xylem resume` restores it to `running` after the
label is applied.

## Context

You are working in a repository that uses xylem for autonomous task execution.
The repository has a `.xylem.yml` configuration and a workflow that includes a
`label` gate between phases. The gate checks for a GitHub label before the
second phase is allowed to run.

## What to do

1. Run `xylem enqueue --source manual --prompt 'Implement feature X' --workflow implement-feature`
2. Run `xylem drain` — the vessel should reach `waiting` state when the label gate fires
3. Inspect `xylem status` to confirm the vessel state is `waiting`
4. Simulate label application by running `xylem resume <vessel-id>`
5. Run `xylem drain` again to complete the remaining phases
6. Inspect `xylem status` to confirm the vessel reached `completed`

## Constraints

- Do not modify `.xylem.yml` or any files under `.xylem/`.
- Work only within the repository root.
