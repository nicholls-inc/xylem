# Task: Resume a label-gated implement-feature workflow

## Issue

An enhancement issue is ready for xylem, but the workflow requires human plan
approval before implementation can continue.

## Context

You are working in a repository that uses xylem for autonomous task execution.
The repository has an `implement-feature` workflow with a `plan-approved` label
gate between the plan and implement phases.

## What to do

1. Queue the task with xylem using the `implement-feature` workflow.
2. Run xylem until the vessel pauses for the `plan-approved` label.
3. Apply the required label, resume execution, and let the workflow finish.
4. Inspect the final status and phase outputs.

## Constraints

- Do not modify `.xylem.yml` or any files under `.xylem/`.
- Work only within the repository root.
