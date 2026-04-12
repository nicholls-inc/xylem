package policy

import (
	"math/rand"
	"testing"

	"pgregory.net/rapid"
)

// TestProp_PolicyStableUnderReorder is the canonical spec name (§15.4):
// policy matrix decisions are invariant under rule reordering for any
// (class, operation) pair — there is no rule ordering ambiguity.
func TestProp_PolicyStableUnderReorder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rules := append([]rule(nil), defaultRules...)
		seed := rapid.Int64().Draw(t, "seed")
		rng := rand.New(rand.NewSource(seed))
		rng.Shuffle(len(rules), func(i, j int) {
			rules[i], rules[j] = rules[j], rules[i]
		})

		for _, class := range allClasses {
			for _, op := range allOperations {
				want := Evaluate(class, op)
				got := evaluateWithRules(class, op, rules)
				if got != want {
					t.Fatalf("evaluateWithRules(%q, %q) = %+v, want %+v after shuffle seed=%d", class, op, got, want, seed)
				}
			}
		}
	})
}
