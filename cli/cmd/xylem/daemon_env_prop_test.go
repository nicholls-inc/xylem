package main

import (
	"testing"

	"pgregory.net/rapid"
)

func TestPropApplyDaemonEnvEntriesLastWriteWins(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		key := rapid.StringMatching(`[A-Za-z_][A-Za-z0-9_]{0,15}`).Draw(rt, "key")
		values := rapid.SliceOfN(rapid.StringMatching(`[A-Za-z0-9_./:= -]{0,24}`), 1, 8).Draw(rt, "values")

		got := map[string]string{}
		env := make([]string, 0, len(values))
		for _, value := range values {
			env = append(env, key+"="+value)
		}

		err := applyDaemonEnvEntries(env, func(envKey, envValue string) error {
			got[envKey] = envValue
			return nil
		})
		if err != nil {
			rt.Fatalf("applyDaemonEnvEntries() error = %v", err)
		}
		if got[key] != values[len(values)-1] {
			rt.Fatalf("value for %q = %q, want %q", key, got[key], values[len(values)-1])
		}
	})
}
