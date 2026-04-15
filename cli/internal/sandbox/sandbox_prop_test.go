package sandbox

import (
	"os"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// genEnvEntry generates a valid KEY=VALUE environment entry.
// Key is a non-empty ASCII identifier; value may be anything.
func genEnvEntry(t *rapid.T) string {
	// Generate a key that looks like an env var (letters/digits/underscore,
	// not starting with a digit).
	key := rapid.StringMatching(`[A-Z][A-Z0-9_]{0,15}`).Draw(t, "key")
	val := rapid.String().Draw(t, "val")
	return key + "=" + val
}

// genEnvList generates a slice of env entries with distinct keys.
func genEnvList(t *rapid.T, name string) []string {
	n := rapid.IntRange(0, 20).Draw(t, name+"_n")
	keys := make(map[string]struct{}, n)
	out := make([]string, 0, n)
	for range n {
		entry := genEnvEntry(t)
		key, _, _ := strings.Cut(entry, "=")
		if _, dup := keys[key]; dup {
			continue
		}
		keys[key] = struct{}{}
		out = append(out, entry)
	}
	return out
}

// TestPropEnvScopingPolicy_NeverLeaksBlockedVars verifies that for any set of
// provider env entries, no entry whose key is absent from the passlist appears
// in the output when the ambient env is empty (we can't control os.Environ in
// property tests, so we only examine the providerEnv pass-through guarantee).
//
// Specifically: a key that is NOT in the passlist and NOT in providerEnv must
// not appear in the output. We verify this by checking that every entry in the
// output either has its key in the passlist or in the providerEnv key set.
func TestPropEnvScopingPolicy_NeverLeaksBlockedVars(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		extraPasslist := rapid.SliceOf(rapid.StringMatching(`[A-Z][A-Z0-9_]{0,10}`)).Draw(t, "extraPasslist")
		pol := newEnvScopingPolicy(extraPasslist)

		providerEnv := genEnvList(t, "provider")
		got := pol.PhaseEnv(providerEnv)

		// Build the set of allowed keys (passlist union providerEnv keys).
		allowed := make(map[string]struct{}, len(pol.passlist)+len(providerEnv))
		for k := range pol.passlist {
			allowed[k] = struct{}{}
		}
		for _, e := range providerEnv {
			k, _, _ := strings.Cut(e, "=")
			allowed[k] = struct{}{}
		}

		for _, entry := range got {
			key, _, found := strings.Cut(entry, "=")
			if !found {
				continue
			}
			if _, ok := allowed[key]; !ok {
				t.Fatalf("key %q in output env is not in passlist or providerEnv", key)
			}
		}
	})
}

// TestPropEnvScopingPolicy_AlwaysIncludesProviderEnv verifies that every entry
// in providerEnv appears in the output of PhaseEnv.
func TestPropEnvScopingPolicy_AlwaysIncludesProviderEnv(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pol := newEnvScopingPolicy(nil)
		providerEnv := genEnvList(t, "provider")
		got := pol.PhaseEnv(providerEnv)

		gotSet := make(map[string]struct{}, len(got))
		for _, e := range got {
			gotSet[e] = struct{}{}
		}
		for _, e := range providerEnv {
			if _, ok := gotSet[e]; !ok {
				t.Fatalf("providerEnv entry %q missing from PhaseEnv output", e)
			}
		}
	})
}

// TestPropNoopPolicy_SupersetOfAmbient verifies that NoopPolicy.PhaseEnv
// always returns a superset of os.Environ() (all ambient vars are preserved)
// and that providerEnv entries are additive.
func TestPropNoopPolicy_SupersetOfAmbient(t *testing.T) {
	// Snapshot ambient env once; os.Environ() is stable within a test process.
	ambient := os.Environ()

	rapid.Check(t, func(t *rapid.T) {
		pol := NoopPolicy{}
		providerEnv := genEnvList(t, "provider")
		got := pol.PhaseEnv(providerEnv)

		gotSet := make(map[string]struct{}, len(got))
		for _, e := range got {
			gotSet[e] = struct{}{}
		}

		// Every ambient entry must be present (NoopPolicy must not strip anything).
		for _, e := range ambient {
			if _, ok := gotSet[e]; !ok {
				t.Fatalf("NoopPolicy: ambient env entry %q missing from output", e)
			}
		}

		// Every providerEnv entry must also be present.
		for _, e := range providerEnv {
			if _, ok := gotSet[e]; !ok {
				t.Fatalf("NoopPolicy: providerEnv entry %q missing from output", e)
			}
		}
	})
}

// TestPropEnvScopingPolicy_DeterministicForSameInput verifies that calling
// PhaseEnv twice with the same providerEnv produces the same output set
// (order may differ due to os.Environ, but the set of entries must match).
func TestPropEnvScopingPolicy_DeterministicForSameInput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pol := newEnvScopingPolicy(nil)
		providerEnv := genEnvList(t, "provider")

		got1 := pol.PhaseEnv(providerEnv)
		got2 := pol.PhaseEnv(providerEnv)

		set1 := toSet(got1)
		set2 := toSet(got2)

		for e := range set1 {
			if _, ok := set2[e]; !ok {
				t.Fatalf("non-deterministic output: %q in first call but not second", e)
			}
		}
		for e := range set2 {
			if _, ok := set1[e]; !ok {
				t.Fatalf("non-deterministic output: %q in second call but not first", e)
			}
		}
	})
}

func toSet(entries []string) map[string]struct{} {
	s := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		s[e] = struct{}{}
	}
	return s
}
