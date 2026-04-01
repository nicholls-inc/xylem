# Task: Fix null pointer dereference in processItem

## Issue

The `processItem` function in `main.go` panics with a nil pointer dereference
when called with an item that has no metadata field.

## Context

You are working in a repository that uses xylem for autonomous task execution.
The repository has a `.xylem.yml` configuration and a `fix-bug` workflow.

## What to do

1. Run `xylem enqueue --source manual --prompt 'Fix nil pointer in processItem when metadata is nil' --workflow fix-bug`
2. Run `xylem drain`
3. After drain completes, inspect the resulting status with `xylem status`

## Constraints

- Do not modify `.xylem.yml` or any files under `.xylem/`.
- Work only within the repository root.
