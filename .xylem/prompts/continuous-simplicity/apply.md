Implement the selected continuous-simplicity changes.

Read:

1. `.xylem/state/continuous-simplicity/pr-plan.json`
2. `.xylem/state/continuous-simplicity/simplifications.json`
3. `.xylem/state/continuous-simplicity/duplications.json`

The planner has already filtered and capped the work. Only implement the entries in `selected`.

For each selected entry:

1. create or reset the branch named in `selected[].branch` from `main`
2. apply the change described by `selected[].implementation`
3. keep the change behavior-preserving
4. commit the result with the PR title as the commit subject
5. push the branch to `origin`

Constraints:

1. do not touch entries that were skipped
2. do not combine multiple selected entries onto one branch
3. do not edit tests unless needed to preserve existing behavior
4. do not modify `.xylem/workflows/` or `.xylem/prompts/`

After pushing every selected branch, return the worktree to its original branch and print a short summary listing each pushed branch and the files changed on it.
