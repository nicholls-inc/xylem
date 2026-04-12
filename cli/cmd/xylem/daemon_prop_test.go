package main

import (
	"context"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/profiles"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// validProfileCombinations is the set of profile combinations that Compose
// accepts. Drawing from this small set keeps property runs fast.
var validProfileCombinations = [][]string{
	{"core"},
	{"core", "self-hosting-xylem"},
}

// TestProp_DaemonStartupConvergesProfileDigest asserts that for any valid
// profile combination, one call to daemonStartup causes the runtime digest
// to equal the embedded digest.
func TestProp_DaemonStartupConvergesProfileDigest(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		profileNames := rapid.SampledFrom(validProfileCombinations).Draw(rt, "profiles")

		dir := t.TempDir()
		stateDir := filepath.Join(dir, ".xylem")

		cfg := &config.Config{
			Profiles: profileNames,
			StateDir: stateDir,
		}
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		err := daemonStartup(context.Background(), cfg, q, nil, &seedRunnerStub{}, false)
		if err != nil {
			rt.Fatalf("daemonStartup failed: %v", err)
		}

		composed, err := profiles.Compose(profileNames...)
		if err != nil {
			rt.Fatalf("Compose(%v) failed: %v", profileNames, err)
		}
		embedded := profiles.ComputeEmbeddedDigest(composed)
		runtime := profiles.ComputeRuntimeDigest(stateDir)

		if embedded != runtime {
			rt.Fatalf("digest mismatch after daemonStartup:\n  embedded=%s\n  runtime=%s",
				embedded, runtime)
		}
	})
}

// TestProp_DaemonStartupIdempotentOnSecondCall asserts that calling
// daemonStartup twice with the same config leaves the runtime digest equal to
// the embedded digest — i.e. the second call does not corrupt or regress the
// state written by the first.
func TestProp_DaemonStartupIdempotentOnSecondCall(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		profileNames := rapid.SampledFrom(validProfileCombinations).Draw(rt, "profiles")

		dir := t.TempDir()
		stateDir := filepath.Join(dir, ".xylem")

		cfg := &config.Config{
			Profiles: profileNames,
			StateDir: stateDir,
		}
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		// First call — materialises the assets.
		if err := daemonStartup(context.Background(), cfg, q, nil, &seedRunnerStub{}, false); err != nil {
			rt.Fatalf("first daemonStartup failed: %v", err)
		}

		// Second call — must leave digests unchanged.
		if err := daemonStartup(context.Background(), cfg, q, nil, &seedRunnerStub{}, false); err != nil {
			rt.Fatalf("second daemonStartup failed: %v", err)
		}

		// Digests must agree after both calls. This directly proves idempotence:
		// if the second call re-synced unnecessarily it would still produce a
		// matching digest, but if it overwrote with wrong content it would not.
		composed, err := profiles.Compose(profileNames...)
		if err != nil {
			rt.Fatalf("Compose(%v) failed: %v", profileNames, err)
		}
		embedded := profiles.ComputeEmbeddedDigest(composed)
		runtime := profiles.ComputeRuntimeDigest(stateDir)
		if embedded != runtime {
			rt.Fatalf("digest mismatch after second daemonStartup:\n  embedded=%s\n  runtime=%s",
				embedded, runtime)
		}
	})
}
