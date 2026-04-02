package dtu

import "testing"

func TestSelectProviderScriptPrefersExactAttempt(t *testing.T) {
	t.Parallel()

	state := &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample"},
		Providers: Providers{Scripts: []ProviderScript{
			{Name: "reply", Provider: ProviderClaude, Match: ProviderScriptMatch{Phase: "analyze"}, Stdout: "fallback"},
			{Name: "reply", Provider: ProviderClaude, Match: ProviderScriptMatch{Phase: "analyze", Attempt: 2}, Stdout: "retry"},
		}},
		Counters: Counters{NextCommentID: 1, NextReviewID: 1, NextCheckID: 1},
	}
	state.normalize()

	script, err := state.SelectProviderScript(ProviderInvocation{
		Provider: ProviderClaude,
		Phase:    "analyze",
		Attempt:  2,
	})
	if err != nil {
		t.Fatalf("SelectProviderScript() error = %v", err)
	}
	if script.Stdout != "retry" {
		t.Fatalf("Stdout = %q, want retry", script.Stdout)
	}
}

func TestSelectProviderScriptByNameAndAttempt(t *testing.T) {
	t.Parallel()

	state := &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample"},
		Providers: Providers{Scripts: []ProviderScript{
			{Name: "reply", Provider: ProviderClaude},
			{Name: "reply", Provider: ProviderClaude, Match: ProviderScriptMatch{Attempt: 3}, Stdout: "third"},
		}},
		Counters: Counters{NextCommentID: 1, NextReviewID: 1, NextCheckID: 1},
	}
	state.normalize()

	script, err := state.SelectProviderScript(ProviderInvocation{
		Provider:   ProviderClaude,
		ScriptName: "reply",
		Attempt:    3,
	})
	if err != nil {
		t.Fatalf("SelectProviderScript() error = %v", err)
	}
	if script.Match.Attempt != 3 {
		t.Fatalf("Match.Attempt = %d, want 3", script.Match.Attempt)
	}
}

func TestSelectProviderScriptFallsBackToPhaseWhenScriptNameUnknown(t *testing.T) {
	t.Parallel()

	state := &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample"},
		Providers: Providers{Scripts: []ProviderScript{
			{Name: "plan-response", Provider: ProviderClaude, Match: ProviderScriptMatch{Phase: "plan"}, Stdout: "plan"},
			{Name: "implement-response", Provider: ProviderClaude, Match: ProviderScriptMatch{Phase: "implement"}, Stdout: "implement"},
		}},
		Counters: Counters{NextCommentID: 1, NextReviewID: 1, NextCheckID: 1},
	}
	state.normalize()

	script, err := state.SelectProviderScript(ProviderInvocation{
		Provider:   ProviderClaude,
		ScriptName: "plan",
		Phase:      "plan",
	})
	if err != nil {
		t.Fatalf("SelectProviderScript() error = %v", err)
	}
	if script.Name != "plan-response" {
		t.Fatalf("Name = %q, want plan-response", script.Name)
	}
}

func TestSelectProviderScriptPreservesExplicitNameBehavior(t *testing.T) {
	t.Parallel()

	state := &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample"},
		Providers: Providers{Scripts: []ProviderScript{
			{Name: "plan", Provider: ProviderClaude, Match: ProviderScriptMatch{Attempt: 2}},
			{Name: "plan-response", Provider: ProviderClaude, Match: ProviderScriptMatch{Phase: "plan"}},
		}},
		Counters: Counters{NextCommentID: 1, NextReviewID: 1, NextCheckID: 1},
	}
	state.normalize()

	_, err := state.SelectProviderScript(ProviderInvocation{
		Provider:   ProviderClaude,
		ScriptName: "plan",
		Phase:      "plan",
		Attempt:    1,
	})
	if err == nil {
		t.Fatal("SelectProviderScript() error = nil, want explicit-name mismatch")
	}
	if got, want := err.Error(), `no matching provider script named "plan" for provider "claude"`; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestSelectProviderScriptPrefersMatchingScenario(t *testing.T) {
	t.Parallel()

	state := &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample", Scenario: "issue workflow"},
		Providers: Providers{Scripts: []ProviderScript{
			{Name: "reply", Provider: ProviderClaude, Stdout: "generic"},
			{Name: "reply", Provider: ProviderClaude, Match: ProviderScriptMatch{Scenario: "issue workflow"}, Stdout: "scenario"},
			{Name: "reply", Provider: ProviderClaude, Match: ProviderScriptMatch{Scenario: "merge workflow"}, Stdout: "other"},
		}},
		Counters: Counters{NextCommentID: 1, NextReviewID: 1, NextCheckID: 1},
	}
	state.normalize()

	script, err := state.SelectProviderScript(ProviderInvocation{
		Provider: ProviderClaude,
	})
	if err != nil {
		t.Fatalf("SelectProviderScript() error = %v", err)
	}
	if script.Stdout != "scenario" {
		t.Fatalf("Stdout = %q, want scenario", script.Stdout)
	}
}

func TestSelectShimFaultPrefersExactArgsAndAttempt(t *testing.T) {
	t.Parallel()

	state := &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample"},
		ShimFaults: []ShimFault{
			{Name: "gh-search", Command: ShimCommandGH, Match: ShimFaultMatch{ArgsPrefix: []string{"search"}}, ExitCode: 1},
			{Name: "gh-search", Command: ShimCommandGH, Match: ShimFaultMatch{ArgsExact: []string{"search", "issues"}, Attempt: 2}, ExitCode: 9},
		},
		Counters: Counters{NextCommentID: 1, NextReviewID: 1, NextCheckID: 1},
	}
	state.normalize()

	fault, err := state.SelectShimFault(ShimInvocation{
		Command: ShimCommandGH,
		Args:    []string{"search", "issues"},
		Attempt: 2,
	})
	if err != nil {
		t.Fatalf("SelectShimFault() error = %v", err)
	}
	if fault.ExitCode != 9 {
		t.Fatalf("ExitCode = %d, want 9", fault.ExitCode)
	}
}

func TestSelectShimFaultByName(t *testing.T) {
	t.Parallel()

	state := &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample"},
		ShimFaults: []ShimFault{
			{Name: "git-fetch", Command: ShimCommandGit, Match: ShimFaultMatch{ArgsPrefix: []string{"fetch"}}, ExitCode: 7},
		},
		Counters: Counters{NextCommentID: 1, NextReviewID: 1, NextCheckID: 1},
	}
	state.normalize()

	fault, err := state.SelectShimFault(ShimInvocation{
		Command:   ShimCommandGit,
		FaultName: "git-fetch",
		Args:      []string{"fetch", "origin"},
	})
	if err != nil {
		t.Fatalf("SelectShimFault() error = %v", err)
	}
	if fault.Name != "git-fetch" {
		t.Fatalf("Name = %q, want git-fetch", fault.Name)
	}
}
