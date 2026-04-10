package policy

import (
	"fmt"
	"strings"
)

type Class string

const (
	Delivery           Class = "delivery"
	HarnessMaintenance Class = "harness-maintenance"
	Ops                Class = "ops"
)

type Operation string

const (
	OpWriteControlPlane   Operation = "write_control_plane"
	OpCommitDefaultBranch Operation = "commit_default_branch"
	OpPushBranch          Operation = "push_branch"
	OpCreatePR            Operation = "create_pr"
	OpMergePR             Operation = "merge_pr"
	OpReloadDaemon        Operation = "reload_daemon"
	OpReadSecrets         Operation = "read_secrets"
)

type Decision struct {
	Allowed bool
	Rule    string
	Audit   bool
}

type rule struct {
	class     Class
	operation Operation
	decision  Decision
}

var defaultRules = []rule{
	{class: Delivery, operation: OpWriteControlPlane, decision: Decision{Rule: "delivery.no_control_plane_writes"}},
	{class: HarnessMaintenance, operation: OpWriteControlPlane, decision: Decision{Allowed: true, Rule: "harness_maintenance.worktree_writes_allowed", Audit: true}},
	{class: Ops, operation: OpWriteControlPlane, decision: Decision{Rule: "ops.no_control_plane_writes"}},

	{class: Delivery, operation: OpCommitDefaultBranch, decision: Decision{Rule: "delivery.default_branch_commits_denied"}},
	{class: HarnessMaintenance, operation: OpCommitDefaultBranch, decision: Decision{Rule: "harness_maintenance.default_branch_commits_denied"}},
	{class: Ops, operation: OpCommitDefaultBranch, decision: Decision{Rule: "ops.default_branch_commits_denied"}},

	{class: Delivery, operation: OpPushBranch, decision: Decision{Allowed: true, Rule: "delivery.feature_branch_push_allowed"}},
	{class: HarnessMaintenance, operation: OpPushBranch, decision: Decision{Allowed: true, Rule: "harness_maintenance.feature_branch_push_allowed"}},
	{class: Ops, operation: OpPushBranch, decision: Decision{Allowed: true, Rule: "ops.feature_branch_push_allowed"}},

	{class: Delivery, operation: OpCreatePR, decision: Decision{Allowed: true, Rule: "delivery.pr_creation_allowed"}},
	{class: HarnessMaintenance, operation: OpCreatePR, decision: Decision{Allowed: true, Rule: "harness_maintenance.pr_creation_allowed"}},
	{class: Ops, operation: OpCreatePR, decision: Decision{Allowed: true, Rule: "ops.pr_creation_allowed"}},

	{class: Delivery, operation: OpMergePR, decision: Decision{Rule: "delivery.pr_merge_denied"}},
	{class: HarnessMaintenance, operation: OpMergePR, decision: Decision{Rule: "harness_maintenance.pr_merge_denied"}},
	{class: Ops, operation: OpMergePR, decision: Decision{Allowed: true, Rule: "ops.pr_merge_label_gated", Audit: true}},

	{class: Delivery, operation: OpReloadDaemon, decision: Decision{Rule: "delivery.reload_denied"}},
	{class: HarnessMaintenance, operation: OpReloadDaemon, decision: Decision{Rule: "harness_maintenance.reload_denied"}},
	{class: Ops, operation: OpReloadDaemon, decision: Decision{Allowed: true, Rule: "ops.reload_on_merge", Audit: true}},

	{class: Delivery, operation: OpReadSecrets, decision: Decision{Rule: "delivery.read_secrets_denied"}},
	{class: HarnessMaintenance, operation: OpReadSecrets, decision: Decision{Rule: "harness_maintenance.read_secrets_denied"}},
	{class: Ops, operation: OpReadSecrets, decision: Decision{Rule: "ops.read_secrets_denied"}},
}

var allClasses = []Class{Delivery, HarnessMaintenance, Ops}

var allOperations = []Operation{
	OpWriteControlPlane,
	OpCommitDefaultBranch,
	OpPushBranch,
	OpCreatePR,
	OpMergePR,
	OpReloadDaemon,
	OpReadSecrets,
}

func Evaluate(class Class, op Operation) Decision {
	return evaluateWithRules(class, op, defaultRules)
}

func ParseClass(s string) (Class, error) {
	switch strings.TrimSpace(s) {
	case "":
		return Delivery, nil
	case string(Delivery):
		return Delivery, nil
	case string(HarnessMaintenance):
		return HarnessMaintenance, nil
	case string(Ops):
		return Ops, nil
	default:
		return "", fmt.Errorf("unknown workflow class %q (must be %q, %q, or %q)", s, Delivery, HarnessMaintenance, Ops)
	}
}

func evaluateWithRules(class Class, op Operation, rules []rule) Decision {
	for _, rule := range rules {
		if rule.class == class && rule.operation == op {
			return rule.decision
		}
	}
	return Decision{Rule: "policy.default_deny"}
}
