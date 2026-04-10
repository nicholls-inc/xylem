package main

import (
	"testing"

	"pgregory.net/rapid"
)

func TestPropParseDaemonEnvLineRoundTripsBareAssignments(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		key := rapid.StringMatching(`[A-Za-z_][A-Za-z0-9_]{0,15}`).Draw(rt, "key")
		value := rapid.StringMatching(`[A-Za-z0-9_./:-]{0,24}`).Draw(rt, "value")

		gotKey, gotValue, ok, err := parseDaemonEnvLine("  " + key + " = " + value + "  ")
		if err != nil {
			rt.Fatalf("parseDaemonEnvLine() error = %v", err)
		}
		if !ok {
			rt.Fatal("parseDaemonEnvLine() reported ok=false for bare assignment")
		}
		if gotKey != key {
			rt.Fatalf("got key %q, want %q", gotKey, key)
		}
		if gotValue != value {
			rt.Fatalf("got value %q, want %q", gotValue, value)
		}
	})
}
