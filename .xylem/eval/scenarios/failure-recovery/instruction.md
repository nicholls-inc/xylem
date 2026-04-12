# Task: Trigger a gate failure and recover via retry

## Issue

Verify that when a workflow gate command fails, the vessel enters `failed`
state, and that `xylem retry` successfully re-enqueues and eventually completes
the vessel.

## Context

You are working in a repository that uses xylem for autonomous task execution.
The repository has a `.xylem.yml` configuration and a `fix-bug` workflow. A
gate command has been configured to fail on first attempt (it exits 1 until a
marker file `.xylem/gate-ok` is created).

## What to do

1. Run `xylem enqueue --source manual --prompt 'Fix the broken test' --workflow fix-bug`
2. Run `xylem drain` — the gate should fail and the vessel should enter `failed` state
3. Inspect `xylem status` to confirm the vessel state is `failed`
4. Create the gate-ok marker: `touch .xylem/gate-ok`
5. Run `xylem retry <vessel-id>` to re-enqueue the vessel
6. Run `xylem drain` to complete the retry attempt
7. Inspect `xylem status` to confirm the vessel reached `completed`

## Constraints

- Do not modify `.xylem.yml` or any workflow YAML under `.xylem/workflows/`.
- Work only within the repository root.
