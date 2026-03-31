# ADR-002: Git Worktree Isolation

**Status:** Accepted

## Context

Concurrent vessels must not interfere with each other on the filesystem. Each vessel works on a distinct task (a GitHub issue, a manual job) and needs its own branch and working directory. Options considered:

- **Containers / VMs** — full process and network isolation, but heavyweight and requires Docker or similar
- **Separate repository clones** — full filesystem isolation, but slow to create and wastes disk
- **Git worktrees** — lightweight, native git feature; each worktree shares the object store but has its own working tree and HEAD

## Decision

Each vessel is assigned a dedicated git worktree at `.claude/worktrees/<branchName>` relative to the repository root. The worktree is created by:

1. `git fetch origin <default-branch>` — ensures the branch point is current
2. `git worktree add .claude/worktrees/<branch> -B <branch> origin/<defaultBranch>` — creates the worktree on a new branch

Branch names follow the pattern `fix/issue-<N>-<slug>` or `feat/issue-<N>-<slug>` for GitHub sources, and `task/<id>-<slug>` for Manual sources.

At creation time, `copyClaudeConfig()` copies the following files from the parent `.claude/` directory into the worktree's `.claude/` directory:

- `settings.json`
- `settings.local.json`
- `rules/` (directory, copied allowlist-based)

The directories `worktrees/`, `conversations/`, and `projects/` are skipped to avoid recursive or session-specific state leaking into the worktree.

## Rationale

- **Native git** — no process or container overhead; `git worktree` is a stable, first-class git feature.
- **Branch-per-vessel** — each vessel produces a clean, reviewable branch with a meaningful name derived from the issue.
- **Config copy** — copying provider tool permission files (`settings.json`, `rules/`) at creation ensures the Claude session inside the worktree has the same allowed-tools configuration as the parent, without coupling the worktree to a mutable parent config.
- **Shared object store** — worktrees share the parent repository's object store, so fetch operations are fast and disk usage is minimal.

## Consequences

- **Positive:** Lightweight and fast to create compared to full clones.
- **Positive:** Each vessel works on its own branch; merging is a standard git pull request flow.
- **Positive:** Config copy at creation time gives each vessel a stable, independent snapshot of tool permissions.
- **Negative:** Isolation is **filesystem-only**. There is no process isolation, no network isolation, and no resource limits. Concurrent vessels share the same OS process environment, CPU, memory, and network.
- **Negative:** Worktree cleanup requires explicit action: `git worktree remove --force` followed by branch deletion. Stale worktrees accumulate if cleanup is skipped. Cleanup is governed by the `cleanup_after` setting (default: 7 days).
- **Negative:** The `.claude/worktrees/` path is baked into the worktree location convention; moving or renaming the parent repository requires recreating worktrees.
