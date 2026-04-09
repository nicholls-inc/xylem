package policy

import "path/filepath"

// Class is the workflow policy class.
type Class string

const (
	Delivery           Class = "delivery"
	HarnessMaintenance Class = "harness-maintenance"
	Ops                Class = "ops"
)

// Mode controls whether policy violations are warnings or hard failures.
type Mode string

const (
	ModeWarn    Mode = "warn"
	ModeEnforce Mode = "enforce"
)

// Operation is a class-matrix operation.
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

// Decision is the result of evaluating a workflow class against an operation.
type Decision struct {
	Allowed bool
	Rule    string
	Audit   bool
}

// NormalizeClass returns a valid class, defaulting to delivery.
func NormalizeClass(value string) Class {
	switch Class(value) {
	case Delivery, HarnessMaintenance, Ops:
		return Class(value)
	default:
		return Delivery
	}
}

// Valid reports whether the class is recognized.
func (c Class) Valid() bool {
	switch c {
	case Delivery, HarnessMaintenance, Ops:
		return true
	default:
		return false
	}
}

// NormalizeMode returns a valid policy mode, defaulting to enforce.
func NormalizeMode(value string) Mode {
	switch Mode(value) {
	case ModeWarn, ModeEnforce:
		return Mode(value)
	default:
		return ModeEnforce
	}
}

// Valid reports whether the mode is recognized.
func (m Mode) Valid() bool {
	switch m {
	case ModeWarn, ModeEnforce:
		return true
	default:
		return false
	}
}

// Evaluate applies the authoritative workflow-class matrix.
func Evaluate(class Class, operation Operation) Decision {
	class = NormalizeClass(string(class))
	switch operation {
	case "":
		return Decision{Allowed: true}
	case OpWriteControlPlane:
		switch class {
		case HarnessMaintenance:
			return Decision{Allowed: true, Rule: "harness-maintenance.write_control_plane.allow", Audit: true}
		case Delivery:
			return Decision{Rule: "delivery.write_control_plane.deny"}
		case Ops:
			return Decision{Rule: "ops.write_control_plane.deny"}
		}
	case OpCommitDefaultBranch:
		return Decision{Rule: "policy.class.no-main-commits"}
	case OpPushBranch:
		return Decision{Allowed: true, Rule: string(class) + ".push_branch.allow"}
	case OpCreatePR:
		return Decision{Allowed: true, Rule: string(class) + ".create_pr.allow"}
	case OpMergePR:
		if class == Ops {
			return Decision{Allowed: true, Rule: "ops.merge_pr.allow"}
		}
		return Decision{Rule: string(class) + ".merge_pr.deny"}
	case OpReloadDaemon:
		if class == Ops {
			return Decision{Allowed: true, Rule: "ops.reload_daemon.allow"}
		}
		return Decision{Rule: string(class) + ".reload_daemon.deny"}
	case OpReadSecrets:
		return Decision{Rule: string(class) + ".read_secrets.deny"}
	}
	return Decision{Allowed: true}
}

// OperationFromActionResource maps an intermediary action/resource pair to a
// workflow-class matrix operation.
func OperationFromActionResource(action, resource string) Operation {
	switch action {
	case "file_write":
		if IsControlPlanePath(resource) {
			return OpWriteControlPlane
		}
	case "git_push":
		return OpPushBranch
	case "pr_create":
		return OpCreatePR
	case "merge_pr", "pr_merge":
		return OpMergePR
	case "daemon_reload":
		return OpReloadDaemon
	case "file_read":
		if IsSecretPath(resource) {
			return OpReadSecrets
		}
	}
	return ""
}

// IsControlPlanePath reports whether a path belongs to xylem's control plane.
func IsControlPlanePath(path string) bool {
	switch path {
	case ".xylem/HARNESS.md", ".xylem.yml":
		return true
	}
	if matched, err := filepath.Match(".xylem/workflows/*", path); err == nil && matched {
		return true
	}
	if matched, err := filepath.Match(".xylem/prompts/*/*.md", path); err == nil && matched {
		return true
	}
	return false
}

// IsSecretPath reports whether a path targets a secrets surface.
func IsSecretPath(path string) bool {
	switch {
	case path == ".env", filepath.Base(path) == ".env", filepath.Base(path) == ".env.local":
		return true
	case filepath.Base(path) == ".netrc":
		return true
	case path == "~/.aws", path == "~/.ssh", path == "~/.gnupg", path == "~/.docker/config.json":
		return true
	default:
		return false
	}
}
