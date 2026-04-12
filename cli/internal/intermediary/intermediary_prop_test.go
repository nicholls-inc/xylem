package intermediary

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// intentGen returns a rapid generator for valid intents.
func intentGen() *rapid.Generator[Intent] {
	return rapid.Custom(func(t *rapid.T) Intent {
		return Intent{
			Action:        rapid.StringMatching(`[a-z]+\.[a-z]+`).Draw(t, "action"),
			Resource:      rapid.StringMatching(`/[a-z]+(/[a-z]+)*`).Draw(t, "resource"),
			AgentID:       rapid.StringMatching(`agent-[0-9]+`).Draw(t, "agentID"),
			Justification: rapid.String().Draw(t, "justification"),
		}
	})
}

// ruleGen returns a rapid generator for policy rules.
func ruleGen() *rapid.Generator[Rule] {
	return rapid.Custom(func(t *rapid.T) Rule {
		effect := rapid.SampledFrom([]Effect{Allow, Deny, RequireApproval}).Draw(t, "effect")
		action := rapid.SampledFrom([]string{"*", "file.*", "net.*", "deploy.*"}).Draw(t, "ruleAction")
		resource := rapid.SampledFrom([]string{"*", "/tmp/*", "/etc/*"}).Draw(t, "ruleResource")
		return Rule{Action: action, Resource: resource, Effect: effect}
	})
}

// policyGen returns a rapid generator for policies.
func policyGen() *rapid.Generator[Policy] {
	return rapid.Custom(func(t *rapid.T) Policy {
		rules := rapid.SliceOfN(ruleGen(), 1, 5).Draw(t, "rules")
		name := rapid.StringMatching(`policy-[a-z]+`).Draw(t, "policyName")
		return Policy{Name: name, Rules: rules}
	})
}

// mustTempDir creates a temp directory. The caller must defer os.RemoveAll.
func mustTempDir(t *rapid.T) string {
	dir, err := os.MkdirTemp("", "intermediary-prop-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	return dir
}

func TestProp_EvaluateIsDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		policies := rapid.SliceOfN(policyGen(), 0, 3).Draw(t, "policies")
		intent := intentGen().Draw(t, "intent")

		dir := mustTempDir(t)
		defer os.RemoveAll(dir)
		al := NewAuditLog(filepath.Join(dir, "audit.jsonl"))
		inter := NewIntermediary(policies, al, &mockExecutor{})

		r1 := inter.Evaluate(intent)
		r2 := inter.Evaluate(intent)

		if r1.Effect != r2.Effect {
			t.Fatalf("non-deterministic: first=%q second=%q", r1.Effect, r2.Effect)
		}
		if r1.Reason != r2.Reason {
			t.Fatalf("non-deterministic reason: first=%q second=%q", r1.Reason, r2.Reason)
		}
	})
}

func TestProp_DeniedIntentsNeverExecute(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		intent := intentGen().Draw(t, "intent")
		exec := &mockExecutor{}

		// Policy that denies everything.
		policies := []Policy{{
			Name:  "deny-all",
			Rules: []Rule{{Action: "*", Resource: "*", Effect: Deny}},
		}}

		dir := mustTempDir(t)
		defer os.RemoveAll(dir)
		al := NewAuditLog(filepath.Join(dir, "audit.jsonl"))
		inter := NewIntermediary(policies, al, exec)

		effect, _ := inter.Submit(context.Background(), intent)
		if effect != Deny {
			t.Fatalf("expected Deny, got %q", effect)
		}
		if exec.calls() != 0 {
			t.Fatalf("executor called %d times for denied intent", exec.calls())
		}
	})
}

func TestProp_AuditLogLengthEqualsSubmitCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 20).Draw(t, "submitCount")
		exec := &mockExecutor{}

		policies := []Policy{{
			Name:  "allow-all",
			Rules: []Rule{{Action: "*", Resource: "*", Effect: Allow}},
		}}

		dir := mustTempDir(t)
		defer os.RemoveAll(dir)
		al := NewAuditLog(filepath.Join(dir, "audit.jsonl"))
		inter := NewIntermediary(policies, al, exec)

		for i := range n {
			intent := intentGen().Draw(t, "intent")
			_, err := inter.Submit(context.Background(), intent)
			if err != nil {
				t.Fatalf("submit %d: %v", i, err)
			}
		}

		entries, err := al.Entries()
		if err != nil {
			t.Fatalf("read audit: %v", err)
		}
		if len(entries) != n {
			t.Fatalf("audit entries: got %d, want %d", len(entries), n)
		}
		// Spot-check: all entries should have Allow decision (allow-all policy).
		for i, e := range entries {
			if e.Decision != Allow {
				t.Fatalf("entry[%d] decision: got %q, want %q", i, e.Decision, Allow)
			}
		}
	})
}

func TestProp_ValidateRejectsEmptyActionOrResource(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate intent with empty action.
		intent := Intent{
			Action:   "",
			Resource: rapid.String().Draw(t, "resource"),
			AgentID:  rapid.String().Draw(t, "agentID"),
		}
		if err := ValidateIntent(intent); err == nil {
			t.Fatal("expected error for empty action")
		}

		// Generate intent with empty resource.
		intent = Intent{
			Action:   rapid.StringMatching(`[a-z]+`).Draw(t, "action"),
			Resource: "",
			AgentID:  rapid.String().Draw(t, "agentID2"),
		}
		if err := ValidateIntent(intent); err == nil {
			t.Fatal("expected error for empty resource")
		}
	})
}

func TestProp_GlobStarMatchesEverything(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		value := rapid.StringMatching(`[a-z0-9.]{0,30}`).Draw(t, "value")
		if !MatchGlob("*", value) {
			t.Fatalf("* should match %q", value)
		}
	})
}

func TestProp_GlobExactMatchesSelf(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate strings that are valid glob literals (no special chars).
		value := rapid.StringMatching(`[a-z0-9.]{1,20}`).Draw(t, "value")
		if !MatchGlob(value, value) {
			t.Fatalf("%q should match itself", value)
		}
	})
}

func TestProp_DenyOnlyPolicyDeniesEverything(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Build a policy with only deny rules.
		nRules := rapid.IntRange(1, 5).Draw(t, "nRules")
		rules := make([]Rule, nRules)
		for i := range nRules {
			rules[i] = Rule{Action: "*", Resource: "*", Effect: Deny}
		}
		policies := []Policy{{Name: "deny-only", Rules: rules}}

		dir := mustTempDir(t)
		defer os.RemoveAll(dir)
		al := NewAuditLog(filepath.Join(dir, "audit.jsonl"))
		inter := NewIntermediary(policies, al, &mockExecutor{})

		intent := intentGen().Draw(t, "intent")
		result := inter.Evaluate(intent)
		if result.Effect != Deny {
			t.Fatalf("deny-only policy returned %q for %+v", result.Effect, intent)
		}
	})
}

func TestProp_AuditEntryRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		entry := AuditEntry{
			Intent:        intentGen().Draw(t, "intent"),
			Decision:      rapid.SampledFrom([]Effect{Allow, Deny, RequireApproval}).Draw(t, "decision"),
			Timestamp:     time.Unix(rapid.Int64Range(0, 253402300799).Draw(t, "seconds"), 0).UTC(),
			ApprovedBy:    rapid.StringMatching(`[A-Za-z0-9._/-]{0,24}`).Draw(t, "approvedBy"),
			Error:         rapid.StringMatching(`[A-Za-z0-9._/ -]{0,40}`).Draw(t, "error"),
			WorkflowClass: rapid.SampledFrom([]string{"", "delivery", "harness-maintenance", "ops"}).Draw(t, "workflowClass"),
			Operation:     rapid.SampledFrom([]string{"", "write_control_plane", "push_branch", "commit_default_branch"}).Draw(t, "operation"),
			RuleMatched:   rapid.StringMatching(`[A-Za-z0-9._/-]{0,40}`).Draw(t, "ruleMatched"),
			FilePath:      rapid.StringMatching(`[A-Za-z0-9._/-]{0,60}`).Draw(t, "filePath"),
			VesselID:      rapid.StringMatching(`[A-Za-z0-9._/-]{0,24}`).Draw(t, "vesselID"),
		}

		raw, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}

		var got AuditEntry
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}

		if got.Intent.Action != entry.Intent.Action {
			t.Fatalf("Intent.Action = %q, want %q", got.Intent.Action, entry.Intent.Action)
		}
		if got.Intent.Resource != entry.Intent.Resource {
			t.Fatalf("Intent.Resource = %q, want %q", got.Intent.Resource, entry.Intent.Resource)
		}
		if got.Intent.AgentID != entry.Intent.AgentID {
			t.Fatalf("Intent.AgentID = %q, want %q", got.Intent.AgentID, entry.Intent.AgentID)
		}
		if got.Intent.Justification != entry.Intent.Justification {
			t.Fatalf("Intent.Justification = %q, want %q", got.Intent.Justification, entry.Intent.Justification)
		}
		if got.Intent.Metadata != nil {
			t.Fatalf("Intent.Metadata = %+v, want nil", got.Intent.Metadata)
		}
		if got.Decision != entry.Decision {
			t.Fatalf("Decision = %q, want %q", got.Decision, entry.Decision)
		}
		if !got.Timestamp.Equal(entry.Timestamp) {
			t.Fatalf("Timestamp = %s, want %s", got.Timestamp.Format(time.RFC3339), entry.Timestamp.Format(time.RFC3339))
		}
		if got.ApprovedBy != entry.ApprovedBy {
			t.Fatalf("ApprovedBy = %q, want %q", got.ApprovedBy, entry.ApprovedBy)
		}
		if got.Error != entry.Error {
			t.Fatalf("Error = %q, want %q", got.Error, entry.Error)
		}
		if got.WorkflowClass != entry.WorkflowClass {
			t.Fatalf("WorkflowClass = %q, want %q", got.WorkflowClass, entry.WorkflowClass)
		}
		if got.Operation != entry.Operation {
			t.Fatalf("Operation = %q, want %q", got.Operation, entry.Operation)
		}
		if got.RuleMatched != entry.RuleMatched {
			t.Fatalf("RuleMatched = %q, want %q", got.RuleMatched, entry.RuleMatched)
		}
		if got.FilePath != entry.FilePath {
			t.Fatalf("FilePath = %q, want %q", got.FilePath, entry.FilePath)
		}
		if got.VesselID != entry.VesselID {
			t.Fatalf("VesselID = %q, want %q", got.VesselID, entry.VesselID)
		}

		var rawFields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rawFields); err != nil {
			t.Fatalf("json.Unmarshal(raw fields) error = %v", err)
		}

		for key, value := range map[string]string{
			"approved_by":    entry.ApprovedBy,
			"error":          entry.Error,
			"workflow_class": entry.WorkflowClass,
			"operation":      entry.Operation,
			"rule_matched":   entry.RuleMatched,
			"file_path":      entry.FilePath,
			"vessel_id":      entry.VesselID,
		} {
			_, present := rawFields[key]
			if value == "" && present {
				t.Fatalf("%s unexpectedly serialized in %s", key, raw)
			}
			if value != "" && !present {
				t.Fatalf("%s missing from %s", key, raw)
			}
		}
	})
}
