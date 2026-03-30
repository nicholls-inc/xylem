---
applyTo: "**/*.go"
---
# Go Code Review Standards

## Testing Patterns

- Tests must use interfaces and stubs for external dependencies (subprocess execution, git operations, GitHub API) — never shell out to real processes in unit tests
- Prefer `t.Helper()` in test helpers so failure messages point to the caller
- Use table-driven tests with `t.Run` subtests for cases that vary only by input/expected output
- Property-based tests use `pgregory.net/rapid` and must be named `TestProp*` in `*_prop_test.go` files
- Test queue and worktree operations use `t.TempDir()` for isolation — never write to fixed paths
- Prefer `t.Fatalf` for setup failures, `t.Errorf` for assertion failures that should not stop the test

## Naming and Style

- Prefer short, clear variable names: `cfg` not `configuration`, `q` not `queue`, `wt` not `worktreeManager`
- Interface names should describe the capability: `CommandRunner`, `WorktreeManager` — not the implementation
- Exported types that are only used as function parameters or return values in one package probably should not be exported
- Receiver names are single-letter or short abbreviations: `(q *Queue)`, `(r *Runner)`, `(s *Scanner)`

## Struct and Interface Design

- Interfaces are defined where they are consumed, not where they are implemented (e.g., `runner.CommandRunner` lives in `runner`, not in a shared package)
- Prefer accepting interfaces and returning concrete types
- Avoid `interface{}` / `any` when a concrete type or small interface suffices

## Error Patterns

```go
// Preferred: wrapped with context
return fmt.Errorf("dequeue vessel: %w", err)

// Avoid: bare error propagation
return err
```

## State Machine Discipline

- All vessel state changes must go through `queue.Update` or `queue.UpdateVessel` — never mutate `Vessel.State` directly and rewrite the file
- New states require updating `validTransitions`, `IsTerminal()`, and the `Update` switch
- Gate results (pass/fail) map to state transitions — verify the mapping is correct

## Go Template Usage

- Workflow prompts are rendered via `text/template` — verify that template variables match the `PhaseContext` struct fields
- Missing template variables silently render as empty strings — flag any `{{.FieldName}}` where `FieldName` does not exist on the context struct

## Dependencies

- This project has minimal dependencies by design — flag any new third-party imports and verify they are justified
- Prefer stdlib over external packages for common tasks (e.g., `encoding/json`, `os/exec`, `text/template`)
