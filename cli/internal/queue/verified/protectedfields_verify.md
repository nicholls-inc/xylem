# Lightweight Verification: `protectedFieldsEqual`

**Function:** `protectedFieldsEqual` at `cli/internal/queue/queue.go:98`
**Helpers:** `timePtrEqual` (line 124), `stringMapEqual` (line 131)
**Invariant:** I2 — Terminal records are immutable in place
**Method:** Semi-formal contract analysis + property-based tests
**Companion tests:** `cli/internal/queue/protectedfields_verify_test.go`
**Full formal proof target:** Roadmap #10 (Gobra — handles Go-native `*time.Time` and `map[string]string`)

---

## Why not Dafny extraction?

`protectedFieldsEqual` operates on the 19-field `Vessel` struct, which contains `*time.Time`
and `map[string]string` fields. Modelling these in Dafny requires either abstract ghost types
(no extractable Go) or a full `Vessel` datatype whose extracted Go cannot interoperate with
the real `queue.Vessel` without a conversion shim — which defeats the purpose. Roadmap #10
(Gobra) is the correct vehicle: a Go-native verifier can reason over the real types directly.

---

## Contract Table

| # | Contract | Type | Status |
|---|---|---|---|
| C1 | Returns `true` iff all 19 I2-protected fields are equal between `a` and `b` | Postcondition | Verified by field-mutation tests |
| C2 | Reflexive: `protectedFieldsEqual(v, v) == true` for any `v` | Postcondition | Property test |
| C3 | Symmetric: `protectedFieldsEqual(a, b) == protectedFieldsEqual(b, a)` | Postcondition | Property test |
| C4 | Pure: no side effects; both `Vessel` args passed by value | Invariant | Structural (value semantics) |
| C5 | `timePtrEqual(nil, nil) == true` | Postcondition | Unit test |
| C6 | `timePtrEqual(non-nil, nil) == false` and vice versa | Postcondition | Unit test |
| C7 | `timePtrEqual` uses `time.Time.Equal` — instant-only, ignores monotonic clock and timezone | Postcondition | Unit test |
| C8 | `stringMapEqual(nil, nil) == true` | Postcondition | Unit test |
| C9 | `stringMapEqual(nil, {}) == true` — nil and empty `PhaseOutputs` are semantically equivalent | Postcondition | Unit test |
| C10 | `Meta` exclusion is sound: runner may legitimately mutate `Meta` on terminal vessels | Design | Exclusion test |
| C11 | `ID`, `Prompt`, `CreatedAt` exclusion is sound: these are I3 identity fields, not I2-protected | Design | Exclusion test |

---

## Field Coverage Analysis

The 19 fields listed in I2 (`docs/invariants/queue.md` §I2 lines 48–50) and the fields
compared by `protectedFieldsEqual`:

| Field | Type | Compared via | In I2 spec |
|---|---|---|---|
| `State` | `VesselState` | `!=` | ✓ |
| `Ref` | `string` | `!=` | ✓ |
| `Source` | `string` | `!=` | ✓ |
| `Workflow` | `string` | `!=` | ✓ |
| `WorkflowDigest` | `string` | `!=` | ✓ |
| `WorkflowClass` | `string` | `!=` | ✓ |
| `Tier` | `string` | `!=` | ✓ |
| `RetryOf` | `string` | `!=` | ✓ |
| `Error` | `string` | `!=` | ✓ |
| `CurrentPhase` | `int` | `!=` | ✓ |
| `GateRetries` | `int` | `!=` | ✓ |
| `WaitingFor` | `string` | `!=` | ✓ |
| `WorktreePath` | `string` | `!=` | ✓ |
| `FailedPhase` | `string` | `!=` | ✓ |
| `GateOutput` | `string` | `!=` | ✓ |
| `StartedAt` | `*time.Time` | `timePtrEqual` | ✓ |
| `EndedAt` | `*time.Time` | `timePtrEqual` | ✓ |
| `WaitingSince` | `*time.Time` | `timePtrEqual` | ✓ |
| `PhaseOutputs` | `map[string]string` | `stringMapEqual` | ✓ |

**Intentionally excluded (not I2-protected per spec):**

| Field | Exclusion rationale |
|---|---|
| `ID` | Immutable identity key set at creation; never mutated by any queue operation |
| `Prompt` | I3 identity field (carried across retry); not listed in I2 |
| `Meta` | Explicitly excluded: runner recovery paths legitimately write `Meta` on terminal vessels |
| `CreatedAt` | I3 identity field (value type, set once at enqueue); not listed in I2 |

---

## Helper Analysis

### `timePtrEqual(a, b *time.Time) bool`

```
if a == nil || b == nil { return a == b }
return a.Equal(*b)
```

- **nil/nil → true**: `nil == nil` evaluates true. ✓
- **non-nil/nil → false**: `a == nil || b == nil` fires on `b == nil`; `non-nil-ptr == nil` is false. ✓
- **nil/non-nil → false**: symmetric. ✓
- **both non-nil**: `a.Equal(*b)` compares the instant in time, ignoring monotonic clock reading and location. This is correct for stored timestamps (JSON round-trip strips monotonic). ✓
- **no panic path**: the pointer dereference `*b` is only reached when neither pointer is nil. ✓

### `stringMapEqual(a, b map[string]string) bool`

```
if len(a) != len(b) { return false }
for k, v := range a { if bv, ok := b[k]; !ok || bv != v { return false } }
return true
```

- **nil/nil → true**: `len(nil) == len(nil)` = 0 == 0; range over nil is a no-op. ✓
- **nil/empty → true**: `len(nil) == len({})` = 0 == 0; range over nil is a no-op. This is correct for `PhaseOutputs` where nil and empty are semantically equivalent (no outputs recorded). ✓
- **length guard prevents extra-key bypass**: if `b` has more keys than `a`, `len(a) != len(b)` catches it. ✓
- **correctness**: with equal lengths, iterating `a` and checking each key in `b` is a complete bijection check. ✓
- **termination**: Go map range always terminates for finite maps. ✓

---

## Verification Gaps

These properties are stated informally here but would require Gobra (#10) for machine-checked proof:

1. **Universal completeness**: the per-field mutation tests check each field independently using a fixed base vessel; rapid property tests use randomly generated pending vessels. Neither proves correctness for *all* possible field value combinations.
2. **Helper correctness under all `time.Time` values**: `timePtrEqual` is tested for nil combinations and the zone-agnostic instant case; exotic values (monotonic clock, sub-nanosecond, zero time) are not exhaustively covered.
3. **No panic for all inputs**: the `timePtrEqual` nil guard is manually verified correct; Gobra would prove this statically.

---

## Upgrade Path to #10 (Gobra)

The contracts above translate directly to Gobra annotations:

```go
// @ requires true  // no preconditions — pure function on value types
// @ ensures  result == (allI2FieldsEqual(a, b))
func protectedFieldsEqual(a, b Vessel) (result bool) { ... }
```

Gobra can reason over `*time.Time` via its permission model and over `map[string]string`
natively. The per-field table above is the exact checklist for writing the Gobra spec.
