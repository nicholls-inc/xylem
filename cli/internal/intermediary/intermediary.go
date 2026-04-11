package intermediary

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/nicholls-inc/xylem/cli/internal/policy"
)

// Effect represents the outcome of a policy evaluation.
type Effect string

const (
	// Allow permits the intent to be executed.
	Allow Effect = "allow"
	// Deny blocks the intent from execution.
	Deny Effect = "deny"
	// RequireApproval pauses execution pending human review.
	RequireApproval Effect = "require_approval"
)

type PolicyMode string

const (
	PolicyModeWarn    PolicyMode = "warn"
	PolicyModeEnforce PolicyMode = "enforce"
)

// Intent represents a structured action request from an agent crossing the
// sandbox boundary. Agents declare what they want to do; the intermediary
// decides whether to permit it.
type Intent struct {
	Action        string            `json:"action"`
	Resource      string            `json:"resource"`
	AgentID       string            `json:"agent_id"`
	Justification string            `json:"justification"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// Rule defines a single policy rule that matches intents by glob patterns on
// action and resource, producing an effect.
type Rule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   Effect `json:"effect"`
}

// Policy is a named collection of rules evaluated in order.
type Policy struct {
	Name  string `json:"name"`
	Rules []Rule `json:"rules"`
}

type EvaluationContext struct {
	WorkflowClass policy.Class
	FilePath      string
	VesselID      string
}

// PolicyResult captures the outcome of evaluating an intent against policies.
type PolicyResult struct {
	Effect        Effect       `json:"effect"`
	MatchedRule   *Rule        `json:"matched_rule,omitempty"`
	Reason        string       `json:"reason"`
	WorkflowClass policy.Class `json:"-"`
	Operation     string       `json:"-"`
	RuleMatched   string       `json:"-"`
	FilePath      string       `json:"-"`
	VesselID      string       `json:"-"`
}

// AuditEntry records a single intermediary decision for the tamper-proof log.
type AuditEntry struct {
	Intent        Intent    `json:"intent"`
	Decision      Effect    `json:"decision"`
	Timestamp     time.Time `json:"timestamp"`
	ApprovedBy    string    `json:"approved_by,omitempty"`
	Error         string    `json:"error,omitempty"`
	WorkflowClass string    `json:"workflow_class,omitempty"`
	Operation     string    `json:"operation,omitempty"`
	RuleMatched   string    `json:"rule_matched,omitempty"`
	FilePath      string    `json:"file_path,omitempty"`
	VesselID      string    `json:"vessel_id,omitempty"`
}

// AuditLog provides an append-only JSONL-backed audit log with file locking.
type AuditLog struct {
	path     string
	lockPath string
}

// NewAuditLog creates a new JSONL-backed audit log at the given path.
func NewAuditLog(path string) *AuditLog {
	return &AuditLog{
		path:     path,
		lockPath: path + ".lock",
	}
}

// Append writes a single audit entry to the log file under an exclusive lock.
// INV: Every Append call adds exactly one JSONL line.
func (a *AuditLog) Append(entry AuditEntry) error {
	lock := flock.New(a.lockPath)
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("acquire audit log lock: %w", err)
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			log.Printf("warn: failed to unlock audit log: %v", err)
		}
	}()

	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer f.Close()

	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal audit entry: %w", err)
	}

	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}

// Entries reads all audit entries from the log file under a shared lock.
func (a *AuditLog) Entries() ([]AuditEntry, error) {
	lock := flock.New(a.lockPath)
	if err := lock.RLock(); err != nil {
		return nil, fmt.Errorf("acquire audit log read lock: %w", err)
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			log.Printf("warn: failed to unlock audit log: %v", err)
		}
	}()

	f, err := os.Open(a.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []AuditEntry{}, nil
		}
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	defer f.Close()

	var entries []AuditEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("parse audit entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	return entries, nil
}

// Executor performs the actual action described by an intent. Implementations
// carry out side effects (file writes, API calls, etc.) after the intermediary
// grants permission.
type Executor interface {
	Execute(ctx context.Context, intent Intent) error
}

// Intermediary is the core security component that validates all agent actions
// crossing the sandbox boundary. It evaluates intents against policies, executes
// allowed actions, and maintains a tamper-proof audit log.
type Intermediary struct {
	policies []Policy
	auditLog *AuditLog
	executor Executor
	mode     PolicyMode
}

// NewIntermediary creates an intermediary with the given policies, audit log,
// and executor.
func NewIntermediary(policies []Policy, auditLog *AuditLog, executor Executor) *Intermediary {
	return &Intermediary{
		policies: policies,
		auditLog: auditLog,
		executor: executor,
		mode:     PolicyModeEnforce,
	}
}

func ParsePolicyMode(s string) (PolicyMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(PolicyModeEnforce):
		return PolicyModeEnforce, nil
	case string(PolicyModeWarn):
		return PolicyModeWarn, nil
	default:
		return "", fmt.Errorf("invalid policy mode %q (must be %q or %q)", s, PolicyModeWarn, PolicyModeEnforce)
	}
}

func (i *Intermediary) SetMode(mode PolicyMode) {
	i.mode = normalizePolicyMode(mode)
}

func (i *Intermediary) Mode() PolicyMode {
	return normalizePolicyMode(i.mode)
}

func (i *Intermediary) ShouldBlock(effect Effect) bool {
	return i.Mode() == PolicyModeEnforce && effect != Allow
}

// Submit validates an intent, evaluates it against policies, executes it if
// allowed, and records an audit entry.
//
// INV: Denied intents never reach the executor in enforce mode.
// INV: Every Submit call produces exactly one audit entry.
// INV: RequireApproval intents are logged but not executed in enforce mode.
func (i *Intermediary) Submit(ctx context.Context, intent Intent) (Effect, error) {
	if err := ValidateIntent(intent); err != nil {
		entry := AuditEntry{
			Intent:        intent,
			Decision:      Deny,
			Timestamp:     time.Now().UTC(),
			Error:         err.Error(),
			WorkflowClass: string(policy.Delivery),
			VesselID:      intent.AgentID,
		}
		if appendErr := i.auditLog.Append(entry); appendErr != nil {
			return Deny, fmt.Errorf("audit validation failure: %w", appendErr)
		}
		return Deny, err
	}

	result := i.Evaluate(intent)
	entry := auditEntryForResult(intent, result)

	if !i.ShouldBlock(result.Effect) && i.executor != nil {
		if err := i.executor.Execute(ctx, intent); err != nil {
			entry.Error = err.Error()
			if appendErr := i.auditLog.Append(entry); appendErr != nil {
				return Allow, fmt.Errorf("audit execution failure: %w", appendErr)
			}
			return Allow, fmt.Errorf("execute intent: %w", err)
		}
	}

	if err := i.auditLog.Append(entry); err != nil {
		return result.Effect, fmt.Errorf("audit decision: %w", err)
	}
	return result.Effect, nil
}

func auditEntryForResult(intent Intent, result PolicyResult) AuditEntry {
	entry := AuditEntry{
		Intent:        intent,
		Decision:      result.Effect,
		Timestamp:     time.Now().UTC(),
		WorkflowClass: string(result.WorkflowClass),
		Operation:     result.Operation,
		RuleMatched:   result.RuleMatched,
		FilePath:      result.FilePath,
		VesselID:      result.VesselID,
	}
	if result.Effect != Allow && result.Reason != "" {
		entry.Error = result.Reason
	}
	return entry
}

func normalizePolicyMode(mode PolicyMode) PolicyMode {
	switch mode {
	case PolicyModeWarn:
		return PolicyModeWarn
	default:
		return PolicyModeEnforce
	}
}

func defaultWorkflowClass(class policy.Class) policy.Class {
	if class == "" {
		return policy.Delivery
	}
	return class
}

func policyEffect(decision policy.Decision) Effect {
	if decision.Allowed {
		return Allow
	}
	return Deny
}

var controlPlanePatterns = []string{
	".xylem/HARNESS.md",
	".xylem.yml",
	".xylem/workflows/*.yaml",
	".xylem/prompts/*/*.md",
}

func IsControlPlaneResource(resource string) bool {
	for _, pattern := range controlPlanePatterns {
		if MatchGlob(pattern, resource) {
			return true
		}
	}
	return false
}

func classifyOperation(intent Intent) (policy.Operation, bool) {
	switch intent.Action {
	case "file_write", "write":
		if IsControlPlaneResource(intent.Resource) {
			return policy.OpWriteControlPlane, true
		}
	case "git_push":
		return policy.OpPushBranch, true
	case "pr_create":
		return policy.OpCreatePR, true
	case "merge_pr":
		return policy.OpMergePR, true
	case "reload", "reload_daemon":
		return policy.OpReloadDaemon, true
	case "read_secret", "read_secrets":
		return policy.OpReadSecrets, true
	case "commit_default_branch":
		return policy.OpCommitDefaultBranch, true
	}
	return "", false
}

func policyRuleLabel(name string, index int) string {
	return fmt.Sprintf("policy.%s[%d]", strings.ReplaceAll(strings.TrimSpace(name), " ", "_"), index)
}

func (i *Intermediary) evaluatePolicies(intent Intent) PolicyResult {
	for _, policyDef := range i.policies {
		for ruleIndex, rule := range policyDef.Rules {
			if MatchGlob(rule.Action, intent.Action) && MatchGlob(rule.Resource, intent.Resource) {
				matchedRule := rule
				return PolicyResult{
					Effect:      rule.Effect,
					MatchedRule: &matchedRule,
					Reason:      fmt.Sprintf("matched rule in policy %q", policyDef.Name),
					RuleMatched: policyRuleLabel(policyDef.Name, ruleIndex),
				}
			}
		}
	}
	return PolicyResult{
		Effect: Deny,
		Reason: "no matching rule; default deny",
	}
}

// EvaluateWithContext checks an intent against the workflow-class matrix first,
// then applies any configured glob rules as an additional tightening layer.
func (i *Intermediary) EvaluateWithContext(intent Intent, evalCtx EvaluationContext) PolicyResult {
	result := PolicyResult{
		WorkflowClass: defaultWorkflowClass(evalCtx.WorkflowClass),
		VesselID:      strings.TrimSpace(evalCtx.VesselID),
	}
	if result.VesselID == "" {
		result.VesselID = intent.AgentID
	}

	if op, ok := classifyOperation(intent); ok {
		result.Operation = string(op)
		result.FilePath = strings.TrimSpace(evalCtx.FilePath)
		if result.FilePath == "" && op == policy.OpWriteControlPlane {
			result.FilePath = intent.Resource
		}

		classDecision := policy.Evaluate(result.WorkflowClass, op)
		result.Effect = policyEffect(classDecision)
		result.Reason = classDecision.Rule
		result.RuleMatched = classDecision.Rule
		if !classDecision.Allowed {
			return result
		}
		if len(i.policies) == 0 {
			return result
		}
	}

	globResult := i.evaluatePolicies(intent)
	globResult.WorkflowClass = result.WorkflowClass
	globResult.VesselID = result.VesselID
	if globResult.Operation == "" {
		globResult.Operation = result.Operation
	}
	if globResult.FilePath == "" {
		globResult.FilePath = result.FilePath
	}

	if result.Operation != "" {
		if len(i.policies) == 0 {
			return result
		}
		if globResult.Effect == Allow {
			return result
		}
	}
	return globResult
}

// Evaluate checks an intent against all policies using first-match semantics.
//
// INV: Policy evaluation is deterministic for the same input.
// INV: Default effect is Deny if no rule matches.
func (i *Intermediary) Evaluate(intent Intent) PolicyResult {
	return i.EvaluateWithContext(intent, EvaluationContext{})
}

// ErrEmptyAction is returned when an intent has an empty Action field.
var ErrEmptyAction = errors.New("intent action must not be empty")

// ErrEmptyResource is returned when an intent has an empty Resource field.
var ErrEmptyResource = errors.New("intent resource must not be empty")

// ErrEmptyAgentID is returned when an intent has an empty AgentID field.
var ErrEmptyAgentID = errors.New("intent agent_id must not be empty")

// ValidateIntent checks that an intent has all required fields populated.
func ValidateIntent(intent Intent) error {
	if intent.Action == "" {
		return ErrEmptyAction
	}
	if intent.Resource == "" {
		return ErrEmptyResource
	}
	if intent.AgentID == "" {
		return ErrEmptyAgentID
	}
	return nil
}

// MatchGlob matches a value against a glob pattern. A standalone "*" matches
// any value (including those containing path separators). Otherwise, it
// delegates to filepath.Match for standard glob semantics (*, ?, [...]).
func MatchGlob(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	matched, err := filepath.Match(pattern, value)
	if err != nil {
		// Malformed patterns never match.
		return false
	}
	return matched
}
