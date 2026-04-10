package policy

import (
	"math/rand"
	"testing"

	"pgregory.net/rapid"
)

var testClasses = []Class{Delivery, HarnessMaintenance, Ops}

var testOperations = []Operation{
	OpWriteControlPlane,
	OpCommitDefaultBranch,
	OpPushBranch,
	OpCreatePR,
	OpMergePR,
	OpReloadDaemon,
	OpReadSecrets,
}

func TestPropEvaluateStableUnderRuleReordering(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rules := append([]rule(nil), defaultRules...)
		seed := rapid.Int64().Draw(t, "seed")
		rng := rand.New(rand.NewSource(seed))
		rng.Shuffle(len(rules), func(i, j int) {
			rules[i], rules[j] = rules[j], rules[i]
		})

		for _, class := range testClasses {
			for _, op := range testOperations {
				want := Evaluate(class, op)
				got := evaluateWithRules(class, op, rules)
				if got != want {
					t.Fatalf("evaluateWithRules(%q, %q) = %+v, want %+v after shuffle seed=%d", class, op, got, want, seed)
				}
			}
		}
	})
}
