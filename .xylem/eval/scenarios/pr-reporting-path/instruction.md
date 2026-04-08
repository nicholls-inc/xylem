# Task: Complete the PR and reporting path

## Issue

The repository expects xylem to finish the bug-fix workflow all the way through
the PR/reporting phase and leave behind the usual execution artifacts.

## Context

You are working in a repository that uses xylem for autonomous task execution.
The repository has a workflow with a final `pr` phase that prepares pull-request
material and reporting output.

## What to do

1. Queue the task with xylem.
2. Run xylem until the workflow completes.
3. Inspect the generated phase outputs and final summary artifacts for the `pr`
   phase.

## Constraints

- Do not modify `.xylem.yml` or any files under `.xylem/`.
- Work only within the repository root.
