# Diagnose

The nil-pointer panic only occurs when `item.Metadata` is nil and `processItem`
unconditionally dereferences it to read `Metadata.ID`.

## Proposed fix

1. Guard the dereference in `processItem` so missing metadata returns a typed
   validation error instead of panicking.
2. Add a regression test covering both `nil` metadata and the non-nil happy
   path.
3. Keep the change scoped to `main.go` and the existing test file; do not
   refactor unrelated parsing code.
