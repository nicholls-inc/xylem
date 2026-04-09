package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmoke_S1_DeliveryControlPlaneWritesAreDenied(t *testing.T) {
	got := Evaluate(Delivery, OpWriteControlPlane)

	require.False(t, got.Allowed)
	assert.Equal(t, "delivery.no_control_plane_writes", got.Rule)
	assert.False(t, got.Audit)
}

func TestSmoke_S2_HarnessMaintenanceDefaultBranchCommitsAreDenied(t *testing.T) {
	got := Evaluate(HarnessMaintenance, OpCommitDefaultBranch)

	require.False(t, got.Allowed)
	assert.Equal(t, "harness_maintenance.default_branch_commits_denied", got.Rule)
	assert.False(t, got.Audit)
}

func TestSmoke_S3_OpsPullRequestMergesAreAllowed(t *testing.T) {
	got := Evaluate(Ops, OpMergePR)

	require.True(t, got.Allowed)
	assert.Equal(t, "ops.pr_merge_label_gated", got.Rule)
	assert.True(t, got.Audit)
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name  string
		class Class
		op    Operation
		want  Decision
	}{
		{
			name:  "delivery cannot write control plane",
			class: Delivery,
			op:    OpWriteControlPlane,
			want:  Decision{Rule: "delivery.no_control_plane_writes"},
		},
		{
			name:  "harness maintenance can write control plane with audit",
			class: HarnessMaintenance,
			op:    OpWriteControlPlane,
			want:  Decision{Allowed: true, Rule: "harness_maintenance.worktree_writes_allowed", Audit: true},
		},
		{
			name:  "harness maintenance cannot commit default branch",
			class: HarnessMaintenance,
			op:    OpCommitDefaultBranch,
			want:  Decision{Rule: "harness_maintenance.default_branch_commits_denied"},
		},
		{
			name:  "ops can merge pr",
			class: Ops,
			op:    OpMergePR,
			want:  Decision{Allowed: true, Rule: "ops.pr_merge_label_gated", Audit: true},
		},
		{
			name:  "ops can reload daemon with audit",
			class: Ops,
			op:    OpReloadDaemon,
			want:  Decision{Allowed: true, Rule: "ops.reload_on_merge", Audit: true},
		},
		{
			name:  "delivery can create pr",
			class: Delivery,
			op:    OpCreatePR,
			want:  Decision{Allowed: true, Rule: "delivery.pr_creation_allowed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Evaluate(tt.class, tt.op); got != tt.want {
				t.Fatalf("Evaluate(%q, %q) = %+v, want %+v", tt.class, tt.op, got, tt.want)
			}
		})
	}
}

func TestParseClass(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Class
		wantErr string
	}{
		{name: "empty defaults to delivery", input: "", want: Delivery},
		{name: "delivery", input: "delivery", want: Delivery},
		{name: "harness maintenance", input: "harness-maintenance", want: HarnessMaintenance},
		{name: "ops", input: "ops", want: Ops},
		{name: "invalid", input: "runtime", wantErr: `unknown workflow class "runtime"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseClass(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseClass(%q) error = nil, want %q", tt.input, tt.wantErr)
				}
				if err.Error() != tt.wantErr+` (must be "delivery", "harness-maintenance", or "ops")` {
					t.Fatalf("ParseClass(%q) error = %q", tt.input, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseClass(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ParseClass(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
