# Diagnose

The panic probably comes from several quality issues across the repository.

## Proposed fix

1. Rewrite the item pipeline around a new metadata service abstraction.
2. Replace the current queue implementation with a concurrent worker pool.
3. Rename the CLI commands for consistency before adding tests later.

This might eventually fix the panic, but the main priority is modernizing the
whole codebase.
