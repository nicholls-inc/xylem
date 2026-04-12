# Task: Enqueue via manual source, scan, and drain

## Issue

Verify that the scan → enqueue → drain pipeline correctly progresses a vessel
from `pending` through `running` to `completed`.

## Context

You are working in a repository that uses xylem for autonomous task execution.
The repository has a `.xylem.yml` configuration with a `manual` source and a
`fix-bug` workflow.

## What to do

1. Run `xylem enqueue --source manual --prompt 'Add a comment to the main function' --workflow fix-bug`
2. Run `xylem scan` to verify the vessel appears in the queue
3. Run `xylem drain` to execute the vessel
4. After drain completes, inspect the result with `xylem status`

## Constraints

- Do not modify `.xylem.yml` or any files under `.xylem/`.
- Work only within the repository root.
- Do not add features beyond what is requested.
