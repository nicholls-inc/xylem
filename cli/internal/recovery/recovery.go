package recovery

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

const (
	artifactFileName = "failure-review.json"
	schemaVersion    = "v1"

	DecisionSourceDeterministic = "deterministic_classifier"
	DecisionSourceDiagnosis     = "diagnosis_workflow"

	DefaultRetryCap              = 2
	DefaultRetryCooldown         = 5 * time.Minute
	MetaClass                    = "recovery_class"
	MetaAction                   = "recovery_action"
	MetaRationale                = "recovery_rationale"
	MetaFollowUpRoute            = "recovery_followup_route"
	MetaRetrySuppressed          = "recovery_retry_suppressed"
	MetaRetryOutcome             = "recovery_retry_outcome"
	MetaRetryCount               = "recovery_retry_count"
	MetaRetryCap                 = "recovery_retry_cap"
	MetaRetryAfter               = "recovery_retry_after"
	MetaFailureFingerprint       = "failure_fingerprint"
	MetaRemediationFingerprint   = "remediation_fingerprint"
	MetaHarnessDigest            = "harness_digest"
	MetaWorkflowDigest           = "workflow_digest"
	MetaDecisionDigest           = "decision_digest"
	MetaRemediationEpoch         = "remediation_epoch"
	MetaUnlockedBy               = "recovery_unlocked_by"
	MetaUnlockDimension          = "recovery_unlock_dimension"
	MetaConfidence               = "recovery_confidence"
	MetaDecisionSource           = "recovery_decision_source"
	MetaRequiresDecisionRefresh  = "recovery_requires_decision_refresh"
	MetaRepeatedFailureCount     = "recovery_repeated_failure_count"
	diagnosisConfidenceThreshold = 0.5
	repeatedFailureThreshold     = 2
)

var safePathComponent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

type Class string

const (
	ClassTransient  Class = "transient"
	ClassHarnessGap Class = "harness_gap"
	ClassSpecGap    Class = "spec_gap"
	ClassScopeGap   Class = "scope_gap"
	ClassUnknown    Class = "unknown"
)

type Action string

const (
	ActionRetry           Action = "retry"
	ActionLessons         Action = "lessons"
	ActionRefine          Action = "refine"
	ActionSplitTask       Action = "split_task"
	ActionDiagnose        Action = "diagnose"
	ActionRequestInfo     Action = "request_info"
	ActionSplitIssue      Action = "split_issue"
	ActionHarnessPatch    Action = "harness_patch"
	ActionHumanEscalation Action = "human_escalation"
	ActionSuppress        Action = "suppress"
)

var validClasses = map[Class]struct{}{
	ClassTransient:  {},
	ClassHarnessGap: {},
	ClassSpecGap:    {},
	ClassScopeGap:   {},
	ClassUnknown:    {},
}

var validActions = map[Action]struct{}{
	ActionRetry:           {},
	ActionLessons:         {},
	ActionRefine:          {},
	ActionSplitTask:       {},
	ActionDiagnose:        {},
	ActionRequestInfo:     {},
	ActionSplitIssue:      {},
	ActionHarnessPatch:    {},
	ActionHumanEscalation: {},
	ActionSuppress:        {},
}

type Artifact struct {
	SchemaVersion           string                          `json:"schema_version"`
	VesselID                string                          `json:"vessel_id"`
	Source                  string                          `json:"source,omitempty"`
	Workflow                string                          `json:"workflow,omitempty"`
	Ref                     string                          `json:"ref,omitempty"`
	State                   string                          `json:"state"`
	FailedPhase             string                          `json:"failed_phase,omitempty"`
	Error                   string                          `json:"error,omitempty"`
	GateOutput              string                          `json:"gate_output,omitempty"`
	FailureFingerprint      string                          `json:"failure_fingerprint,omitempty"`
	SourceInputFP           string                          `json:"source_input_fingerprint,omitempty"`
	HarnessDigest           string                          `json:"harness_digest,omitempty"`
	WorkflowDigest          string                          `json:"workflow_digest,omitempty"`
	DecisionDigest          string                          `json:"decision_digest,omitempty"`
	RemediationEpoch        string                          `json:"remediation_epoch,omitempty"`
	RemediationFP           string                          `json:"remediation_fingerprint,omitempty"`
	RecoveryClass           Class                           `json:"recovery_class"`
	Confidence              float64                         `json:"confidence,omitempty"`
	RecoveryAction          Action                          `json:"recovery_action"`
	DecisionSource          string                          `json:"decision_source,omitempty"`
	Rationale               string                          `json:"rationale"`
	EvidencePaths           []string                        `json:"evidence_paths,omitempty"`
	RetryPreconditions      []string                        `json:"retry_preconditions,omitempty"`
	FollowUpRoute           string                          `json:"follow_up_route,omitempty"`
	RetrySuppressed         bool                            `json:"retry_suppressed"`
	RetryCount              int                             `json:"retry_count"`
	RetryCap                int                             `json:"retry_cap"`
	RetryAfter              *time.Time                      `json:"retry_after,omitempty"`
	RetryOutcome            string                          `json:"retry_outcome,omitempty"`
	UnlockDimension         string                          `json:"unlock_dimension,omitempty"`
	RequiresDecisionRefresh bool                            `json:"requires_decision_refresh,omitempty"`
	RepeatedFailureCount    int                             `json:"repeated_failure_count,omitempty"`
	RetryOf                 string                          `json:"retry_of,omitempty"`
	Trace                   *observability.TraceContextData `json:"trace,omitempty"`
	CreatedAt               time.Time                       `json:"created_at"`
}

type Input struct {
	VesselID             string
	Source               string
	Workflow             string
	Ref                  string
	State                queue.VesselState
	FailedPhase          string
	Error                string
	GateOutput           string
	RetryOf              string
	SourceInputFP        string
	HarnessDigest        string
	WorkflowDigest       string
	DecisionDigest       string
	RemediationEpoch     string
	RemediationFP        string
	Meta                 map[string]string
	Trace                *observability.TraceContextData
	CreatedAt            time.Time
	EvidencePaths        []string
	RepeatedFailureCount int
}

type DiagnosisInput struct {
	Artifact *Artifact
}

type RefreshOptions struct {
	ReviewedAt time.Time
	Rationale  string
}

type RemediationState struct {
	SourceInputFP    string
	HarnessDigest    string
	WorkflowDigest   string
	DecisionDigest   string
	RemediationEpoch string
	RemediationFP    string
}

func Build(input Input) *Artifact {
	createdAt := input.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	class, action, confidence, rationale := classify(input)
	decisionSource := strings.TrimSpace(metaValue(input.Meta, MetaDecisionSource))
	if decisionSource == "" {
		decisionSource = DecisionSourceDeterministic
	}
	followUpRoute := followUpRouteFor(action)
	retrySuppressed := action != ActionRetry
	retryCount := metaInt(input.Meta, MetaRetryCount)
	retryCap := metaInt(input.Meta, MetaRetryCap)
	if retryCap <= 0 && action == ActionRetry {
		retryCap = DefaultRetryCap
	}
	var retryAfter *time.Time
	if action == ActionRetry {
		next := retryAfterFor(createdAt, retryCount)
		retryAfter = &next
	}
	retryOutcome := strings.TrimSpace(metaValue(input.Meta, MetaRetryOutcome))
	if retryOutcome == "" {
		if retrySuppressed {
			retryOutcome = "suppressed"
		} else {
			retryOutcome = "not_attempted"
		}
	}

	repeatedFailureCount := input.RepeatedFailureCount
	if repeatedFailureCount == 0 {
		repeatedFailureCount = intValue(input.Meta, MetaRepeatedFailureCount)
	}
	evidencePaths := normalizeArtifactPaths(input.VesselID, input.EvidencePaths)
	unlockDimension := strings.TrimSpace(firstNonEmpty(
		metaValue(input.Meta, MetaUnlockedBy),
		metaValue(input.Meta, MetaUnlockDimension),
	))
	fingerprint := strings.TrimSpace(firstNonEmpty(
		metaValue(input.Meta, MetaFailureFingerprint),
		metaValue(input.Meta, "recovery_failure_fingerprint"),
	))
	if fingerprint == "" {
		fingerprint = failureFingerprint(input)
	}
	requiresDecisionRefresh := action == ActionDiagnose
	trace := input.Trace
	if trace != nil && trace.TraceID == "" && trace.SpanID == "" {
		trace = nil
	}
	remediation := remediationStateFromInput(input)
	remediation.DecisionDigest = firstNonEmpty(remediation.DecisionDigest, decisionDigestFor(
		class,
		action,
		rationale,
		followUpRoute,
		retrySuppressed,
		retryCap,
	))
	if remediation.RemediationEpoch == "" {
		remediation.RemediationEpoch = strconv.Itoa(max(retryCount, 0))
	}
	remediation.RemediationFP = ComputeRemediationFingerprint(remediation)

	artifact := &Artifact{
		SchemaVersion:           schemaVersion,
		VesselID:                input.VesselID,
		Source:                  input.Source,
		Workflow:                input.Workflow,
		Ref:                     input.Ref,
		State:                   string(input.State),
		FailedPhase:             input.FailedPhase,
		Error:                   input.Error,
		GateOutput:              input.GateOutput,
		FailureFingerprint:      fingerprint,
		SourceInputFP:           remediation.SourceInputFP,
		HarnessDigest:           remediation.HarnessDigest,
		WorkflowDigest:          remediation.WorkflowDigest,
		DecisionDigest:          remediation.DecisionDigest,
		RemediationEpoch:        remediation.RemediationEpoch,
		RemediationFP:           remediation.RemediationFP,
		RecoveryClass:           class,
		Confidence:              confidence,
		RecoveryAction:          action,
		DecisionSource:          decisionSource,
		Rationale:               rationale,
		EvidencePaths:           evidencePaths,
		RetryPreconditions:      defaultRetryPreconditions(action, repeatedFailureCount),
		FollowUpRoute:           followUpRoute,
		RetrySuppressed:         retrySuppressed,
		RetryCount:              retryCount,
		RetryCap:                retryCap,
		RetryAfter:              retryAfter,
		RetryOutcome:            retryOutcome,
		UnlockDimension:         unlockDimension,
		RequiresDecisionRefresh: requiresDecisionRefresh,
		RepeatedFailureCount:    repeatedFailureCount,
		RetryOf:                 input.RetryOf,
		Trace:                   trace,
		CreatedAt:               createdAt,
	}

	if artifact.DecisionSource == DecisionSourceDiagnosis {
		artifact.RetryPreconditions = normalizePreconditions(artifact.RetryPreconditions)
	}

	return artifact
}

func RunDiagnosisWorkflow(input DiagnosisInput) (*Artifact, bool, error) {
	if input.Artifact == nil {
		return nil, false, fmt.Errorf("run diagnosis workflow: artifact must not be nil")
	}
	if !ShouldDiagnose(input.Artifact) {
		return cloneArtifact(input.Artifact), false, nil
	}

	candidate := diagnosisCandidate(input.Artifact)
	raw, err := json.Marshal(candidate)
	if err != nil {
		return nil, true, fmt.Errorf("run diagnosis workflow: marshal candidate: %w", err)
	}
	updated, err := ApplyDiagnosisOutput(input.Artifact, raw)
	if err != nil {
		return nil, true, fmt.Errorf("run diagnosis workflow: apply output: %w", err)
	}
	return updated, true, nil
}

func ShouldDiagnose(artifact *Artifact) bool {
	if artifact == nil {
		return false
	}
	return artifact.RecoveryAction == ActionDiagnose ||
		artifact.Confidence < diagnosisConfidenceThreshold ||
		artifact.RepeatedFailureCount >= repeatedFailureThreshold
}

func ApplyDiagnosisOutput(base *Artifact, raw []byte) (*Artifact, error) {
	if base == nil {
		return nil, fmt.Errorf("apply diagnosis output: base artifact must not be nil")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, fmt.Errorf("apply diagnosis output: output must not be empty")
	}

	var candidate Artifact
	if err := json.Unmarshal(raw, &candidate); err != nil {
		return nil, fmt.Errorf("apply diagnosis output: unmarshal: %w", err)
	}

	merged, err := mergeDiagnosisCandidate(base, &candidate)
	if err != nil {
		return nil, fmt.Errorf("apply diagnosis output: %w", err)
	}
	if err := Validate(merged); err != nil {
		return nil, fmt.Errorf("apply diagnosis output: validate: %w", err)
	}
	return merged, nil
}

func Validate(artifact *Artifact) error {
	if artifact == nil {
		return fmt.Errorf("artifact must not be nil")
	}
	if err := validatePathComponent(artifact.VesselID); err != nil {
		return fmt.Errorf("invalid vessel ID: %w", err)
	}
	if _, ok := validClasses[artifact.RecoveryClass]; !ok {
		return fmt.Errorf("invalid recovery class %q", artifact.RecoveryClass)
	}
	if _, ok := validActions[artifact.RecoveryAction]; !ok {
		return fmt.Errorf("invalid recovery action %q", artifact.RecoveryAction)
	}
	if artifact.Confidence < 0 || artifact.Confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1")
	}
	if artifact.DecisionSource != "" &&
		artifact.DecisionSource != DecisionSourceDeterministic &&
		artifact.DecisionSource != DecisionSourceDiagnosis {
		return fmt.Errorf("invalid decision source %q", artifact.DecisionSource)
	}
	if artifact.DecisionSource == DecisionSourceDiagnosis {
		if artifact.RecoveryAction == ActionDiagnose {
			return fmt.Errorf("diagnosis workflow must emit a concrete action")
		}
		if len(artifact.EvidencePaths) == 0 {
			return fmt.Errorf("diagnosis workflow must cite evidence paths")
		}
	}
	if artifact.RecoveryAction == ActionRetry && len(artifact.RetryPreconditions) == 0 && artifact.DecisionSource != "" {
		return fmt.Errorf("retry decisions require explicit retry preconditions")
	}
	if artifact.RecoveryAction == ActionRetry && artifact.RequiresDecisionRefresh {
		return fmt.Errorf("retry decisions cannot require a decision refresh")
	}
	if artifact.RecoveryAction != ActionRetry && !artifact.RetrySuppressed {
		return fmt.Errorf("non-retry decisions must suppress retry")
	}
	for _, evidencePath := range artifact.EvidencePaths {
		if err := validateArtifactPath(artifact.VesselID, evidencePath); err != nil {
			return fmt.Errorf("invalid evidence path %q: %w", evidencePath, err)
		}
	}
	return nil
}

func ApplyToMeta(meta map[string]string, artifact *Artifact) map[string]string {
	if artifact == nil {
		return meta
	}
	meta = ApplyRemediationState(meta, RemediationState{
		SourceInputFP:    artifact.SourceInputFP,
		HarnessDigest:    artifact.HarnessDigest,
		WorkflowDigest:   artifact.WorkflowDigest,
		DecisionDigest:   artifact.DecisionDigest,
		RemediationEpoch: artifact.RemediationEpoch,
		RemediationFP:    artifact.RemediationFP,
	})
	meta[MetaClass] = string(artifact.RecoveryClass)
	meta[MetaAction] = string(artifact.RecoveryAction)
	meta[MetaRationale] = artifact.Rationale
	meta[MetaRetrySuppressed] = strconv.FormatBool(artifact.RetrySuppressed)
	meta[MetaRetryOutcome] = artifact.RetryOutcome
	meta[MetaConfidence] = strconv.FormatFloat(artifact.Confidence, 'f', -1, 64)
	meta[MetaFailureFingerprint] = artifact.FailureFingerprint
	meta[MetaRequiresDecisionRefresh] = strconv.FormatBool(artifact.RequiresDecisionRefresh)
	if artifact.RepeatedFailureCount > 0 {
		meta[MetaRepeatedFailureCount] = strconv.Itoa(artifact.RepeatedFailureCount)
	} else {
		delete(meta, MetaRepeatedFailureCount)
	}
	if artifact.DecisionSource != "" {
		meta[MetaDecisionSource] = artifact.DecisionSource
	} else {
		delete(meta, MetaDecisionSource)
	}
	if artifact.FollowUpRoute != "" {
		meta[MetaFollowUpRoute] = artifact.FollowUpRoute
	} else {
		delete(meta, MetaFollowUpRoute)
	}
	meta[MetaRetryCount] = strconv.Itoa(artifact.RetryCount)
	meta[MetaRetryCap] = strconv.Itoa(artifact.RetryCap)
	if artifact.RetryAfter != nil && !artifact.RetryAfter.IsZero() {
		meta[MetaRetryAfter] = artifact.RetryAfter.UTC().Format(time.RFC3339)
	} else {
		delete(meta, MetaRetryAfter)
	}
	if artifact.FailureFingerprint == "" {
		delete(meta, MetaFailureFingerprint)
	}
	if artifact.UnlockDimension != "" {
		meta[MetaUnlockedBy] = artifact.UnlockDimension
		meta[MetaUnlockDimension] = artifact.UnlockDimension
	} else {
		delete(meta, MetaUnlockedBy)
		delete(meta, MetaUnlockDimension)
	}
	return meta
}

func Path(stateDir, vesselID string) string {
	return config.RuntimePath(stateDir, "phases", vesselID, artifactFileName)
}

func RelativePath(vesselID string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, artifactFileName))
}

func Save(stateDir string, artifact *Artifact) error {
	if artifact == nil {
		return fmt.Errorf("save recovery artifact: artifact must not be nil")
	}
	if err := Validate(artifact); err != nil {
		return fmt.Errorf("save recovery artifact: %w", err)
	}
	path := Path(stateDir, artifact.VesselID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save recovery artifact: create dir: %w", err)
	}
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return fmt.Errorf("save recovery artifact: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("save recovery artifact: write: %w", err)
	}
	return nil
}

func Load(path string) (*Artifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load recovery artifact: read: %w", err)
	}
	var artifact Artifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, fmt.Errorf("load recovery artifact: unmarshal: %w", err)
	}
	return &artifact, nil
}

type RetryDecision struct {
	Eligible        bool
	UnlockDimension string
}

func RetryReady(artifact *Artifact, now time.Time) RetryDecision {
	return RetryReadyWithRemediation(artifact, RemediationState{}, now)
}

func RetryReadyWithRemediation(artifact *Artifact, current RemediationState, now time.Time) RetryDecision {
	if artifact == nil || artifact.RecoveryAction != ActionRetry {
		return RetryDecision{}
	}
	if artifact.RetryCap > 0 && artifact.RetryCount >= artifact.RetryCap {
		return RetryDecision{}
	}
	if artifact.RetryAfter != nil && now.UTC().Before(artifact.RetryAfter.UTC()) {
		return RetryDecision{}
	}
	stored := normalizeRemediationState(RemediationState{
		SourceInputFP:    artifact.SourceInputFP,
		HarnessDigest:    artifact.HarnessDigest,
		WorkflowDigest:   artifact.WorkflowDigest,
		DecisionDigest:   firstNonEmpty(artifact.DecisionDigest, DecisionDigest(artifact)),
		RemediationEpoch: firstNonEmpty(artifact.RemediationEpoch, strconv.Itoa(max(artifact.RetryCount, 0))),
		RemediationFP:    artifact.RemediationFP,
	})
	current = normalizeRemediationState(current)
	if current.DecisionDigest == "" {
		current.DecisionDigest = DecisionDigest(artifact)
	}
	if current.RemediationEpoch == "" {
		current.RemediationEpoch = NextRemediationEpoch(artifact)
	}
	if current.RemediationFP == "" {
		current.RemediationFP = ComputeRemediationFingerprint(current)
	}
	if stored.RemediationFP == "" {
		if current.SourceInputFP == "" || stored.SourceInputFP == "" || current.SourceInputFP == stored.SourceInputFP {
			return RetryDecision{Eligible: true, UnlockDimension: "cooldown"}
		}
		return RetryDecision{Eligible: true, UnlockDimension: "source"}
	}
	if current.RemediationFP == stored.RemediationFP {
		return RetryDecision{}
	}
	return RetryDecision{
		Eligible:        true,
		UnlockDimension: remediationUnlockDimension(stored, current),
	}
}

func LoadForVessel(stateDir, vesselID string) (*Artifact, error) {
	if err := validatePathComponent(vesselID); err != nil {
		return nil, fmt.Errorf("load recovery artifact: invalid vessel ID: %w", err)
	}
	return Load(Path(stateDir, vesselID))
}

func RefreshRetryDecision(artifact *Artifact, opts RefreshOptions) (*Artifact, error) {
	if artifact == nil {
		return nil, fmt.Errorf("refresh retry decision: artifact must not be nil")
	}

	updated := cloneArtifact(artifact)
	reviewedAt := opts.ReviewedAt.UTC()
	if reviewedAt.IsZero() {
		reviewedAt = time.Now().UTC()
	}

	updated.SchemaVersion = schemaVersion
	updated.DecisionSource = DecisionSourceDiagnosis
	updated.RecoveryAction = ActionRetry
	updated.Confidence = max(updated.Confidence, 0.8)
	updated.Rationale = strings.TrimSpace(opts.Rationale)
	if updated.Rationale == "" {
		updated.Rationale = fmt.Sprintf(
			"A human refreshed the failure-review decision at %s after reviewing the cited artifacts and verifying that the relevant fix landed.",
			reviewedAt.Format(time.RFC3339),
		)
	}
	if len(updated.EvidencePaths) == 0 {
		updated.EvidencePaths = []string{filepath.ToSlash(filepath.Join("phases", updated.VesselID, "summary.json"))}
	}
	updated.RetryPreconditions = []string{
		"Review the cited artifacts and confirm the relevant fix landed before retrying.",
	}
	updated.FollowUpRoute = ""
	updated.RetrySuppressed = false
	updated.RetryOutcome = "not_attempted"
	updated.UnlockDimension = "decision"
	updated.RequiresDecisionRefresh = false
	updated.RetryAfter = nil
	switch {
	case updated.RetryCap <= 0:
		updated.RetryCap = max(DefaultRetryCap, updated.RetryCount+1)
	case updated.RetryCap <= updated.RetryCount:
		updated.RetryCap = updated.RetryCount + 1
	}

	updated.DecisionDigest = DecisionDigest(updated)
	if updated.RemediationEpoch == "" {
		updated.RemediationEpoch = strconv.Itoa(max(updated.RetryCount, 0))
	}
	updated.RemediationFP = ComputeRemediationFingerprint(RemediationState{
		SourceInputFP:    updated.SourceInputFP,
		HarnessDigest:    updated.HarnessDigest,
		WorkflowDigest:   updated.WorkflowDigest,
		DecisionDigest:   updated.DecisionDigest,
		RemediationEpoch: updated.RemediationEpoch,
	})
	if err := Validate(updated); err != nil {
		return nil, fmt.Errorf("refresh retry decision: %w", err)
	}
	return updated, nil
}

func RefreshRetryDecisionForVessel(stateDir, vesselID string, opts RefreshOptions) (*Artifact, error) {
	if err := validatePathComponent(vesselID); err != nil {
		return nil, fmt.Errorf("refresh retry decision: invalid vessel ID: %w", err)
	}
	artifact, err := LoadForVessel(stateDir, vesselID)
	if err != nil {
		return nil, fmt.Errorf("refresh retry decision: %w", err)
	}
	updated, err := RefreshRetryDecision(artifact, opts)
	if err != nil {
		return nil, err
	}
	if err := Save(stateDir, updated); err != nil {
		return nil, fmt.Errorf("refresh retry decision: %w", err)
	}
	return updated, nil
}

func NextRetryVessel(base, parent queue.Vessel, artifact *Artifact, q *queue.Queue, createdAt time.Time, unlockDimension string) queue.Vessel {
	meta := copyMeta(parent.Meta)
	for key, value := range base.Meta {
		meta[key] = value
	}
	meta["retry_of"] = parent.ID
	if parent.Error != "" {
		meta["retry_error"] = parent.Error
	}
	if parent.FailedPhase != "" {
		meta["failed_phase"] = parent.FailedPhase
	}
	if parent.GateOutput != "" {
		meta["gate_output"] = parent.GateOutput
	}

	retryCount := 1
	if artifact != nil {
		retryCount = artifact.RetryCount + 1
		meta = ApplyToMeta(meta, artifact)
		meta = ApplyRemediationState(meta, RemediationState{
			SourceInputFP:    base.Meta["source_input_fingerprint"],
			HarnessDigest:    base.Meta[MetaHarnessDigest],
			WorkflowDigest:   base.Meta[MetaWorkflowDigest],
			DecisionDigest:   base.Meta[MetaDecisionDigest],
			RemediationEpoch: base.Meta[MetaRemediationEpoch],
			RemediationFP:    base.Meta[MetaRemediationFingerprint],
		})
		meta[MetaRetryOutcome] = "enqueued"
		if artifact.FollowUpRoute == "" {
			delete(meta, MetaFollowUpRoute)
		}
		if artifact.Rationale == "" {
			delete(meta, MetaRationale)
		}
	}
	meta[MetaRetryCount] = strconv.Itoa(retryCount)
	if unlockDimension != "" {
		meta[MetaUnlockedBy] = unlockDimension
		meta[MetaUnlockDimension] = unlockDimension
	}
	failureFingerprint := firstNonEmpty(meta[MetaFailureFingerprint], computeFailureFingerprint(Input{
		State:       parent.State,
		FailedPhase: parent.FailedPhase,
		Error:       parent.Error,
		GateOutput:  parent.GateOutput,
	}))
	if failureFingerprint != "" {
		meta[MetaFailureFingerprint] = failureFingerprint
	}
	if meta[MetaRemediationEpoch] == "" {
		meta[MetaRemediationEpoch] = strconv.Itoa(retryCount)
	}
	meta = ApplyRemediationState(meta, RemediationStateFromMeta(meta))

	retry := queue.Vessel{
		ID:             RetryID(parent.ID, q),
		Source:         firstNonEmpty(base.Source, parent.Source),
		Ref:            firstNonEmpty(base.Ref, parent.Ref),
		Workflow:       firstNonEmpty(base.Workflow, parent.Workflow),
		WorkflowDigest: "",
		Prompt:         firstNonEmpty(base.Prompt, parent.Prompt),
		Meta:           meta,
		State:          queue.StatePending,
		CreatedAt:      createdAt.UTC(),
		RetryOf:        parent.ID,
	}
	retry.FailedPhase = parent.FailedPhase
	retry.GateOutput = parent.GateOutput
	return retry
}

func RetryID(originalID string, q *queue.Queue) string {
	if q == nil {
		return originalID + "-retry-1"
	}
	vessels, _ := q.List()
	maxRetry := 0
	prefix := originalID + "-retry-"
	for _, vessel := range vessels {
		if strings.HasPrefix(vessel.ID, prefix) {
			numStr := strings.TrimPrefix(vessel.ID, prefix)
			if n, err := strconv.Atoi(numStr); err == nil && n > maxRetry {
				maxRetry = n
			}
		}
	}
	return fmt.Sprintf("%s-retry-%d", originalID, maxRetry+1)
}
func UpdateRetryOutcome(stateDir, vesselID, outcome string) error {
	if strings.TrimSpace(outcome) == "" {
		return nil
	}
	if err := validatePathComponent(vesselID); err != nil {
		return fmt.Errorf("update retry outcome: invalid vessel ID: %w", err)
	}
	path := Path(stateDir, vesselID)
	artifact, err := Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	artifact.RetryOutcome = outcome
	return Save(stateDir, artifact)
}

func RetryAuthorized(stateDir, vesselID string) error {
	if err := validatePathComponent(vesselID); err != nil {
		return fmt.Errorf("retry authorization: invalid vessel ID: %w", err)
	}
	artifact, err := Load(Path(stateDir, vesselID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("retry authorization: %w", err)
	}
	return ValidateRetryAuthorization(artifact)
}

func ValidateRetryAuthorization(artifact *Artifact) error {
	if artifact == nil {
		return nil
	}
	if artifact.RecoveryAction != ActionRetry {
		if artifact.RequiresDecisionRefresh {
			return fmt.Errorf("retry blocked until the failure-review decision changes: %s", strings.Join(artifact.RetryPreconditions, "; "))
		}
		return fmt.Errorf("retry blocked by failure-review action %q", artifact.RecoveryAction)
	}
	if len(artifact.RetryPreconditions) == 0 {
		if artifact.DecisionSource == "" {
			return nil
		}
		return fmt.Errorf("retry blocked because the failure review does not define explicit retry preconditions")
	}
	return nil
}

func CountMatchingFailures(vessels []queue.Vessel, current queue.Vessel, fingerprint string) int {
	if fingerprint == "" {
		return 0
	}
	count := 0
	currentRoot := retryRoot(current)
	for _, vessel := range vessels {
		if vessel.State != queue.StateFailed && vessel.State != queue.StateTimedOut {
			continue
		}
		if fingerprintForVessel(vessel) != fingerprint {
			continue
		}
		switch {
		case current.Ref != "" && vessel.Ref == current.Ref && vessel.Workflow == current.Workflow:
			count++
		case current.Ref == "" && retryRoot(vessel) == currentRoot && vessel.Workflow == current.Workflow:
			count++
		}
	}
	return count
}

func classify(input Input) (Class, Action, float64, string) {
	combined := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		input.Error,
		input.GateOutput,
	}, "\n")))

	switch {
	case input.State == queue.StateTimedOut ||
		containsAny(combined, "timeout", "timed out", "deadline exceeded", "connection reset", "connection refused",
			"temporarily unavailable", "temporary failure", "rate limit", "429", "502", "503", "504"):
		return ClassTransient, ActionRetry, 0.92, "The failure looks transient, so retry remains the preferred recovery path."
	case containsAny(combined, "harness", "agents.md", "protected surface", "policy blocked", "instruction", "prompt template"):
		return ClassHarnessGap, ActionLessons, 0.9, "The failure points at missing or incorrect harness guidance and should feed institutional memory instead of repeating the same run."
	case containsAny(combined, "acceptance criteria", "unclear requirement", "missing requirement", "spec gap", "requirements gap",
		"underspecified", "need clarification", "needs clarification", "ambiguous requirement", "ambiguous spec"):
		return ClassSpecGap, ActionRefine, 0.88, "The task is underspecified, so it should route back through refinement instead of retrying unchanged."
	case containsAny(combined, "too broad", "split task", "split into", "scope gap", "out of scope", "multiple tasks", "too much work",
		"larger than one change", "separate issue"):
		return ClassScopeGap, ActionSplitTask, 0.86, "The task exceeds the current execution scope and should be refined or split before another attempt."
	default:
		return ClassUnknown, ActionDiagnose, 0.25, "The failure needs diagnosis before xylem can safely decide whether to retry or route follow-up work."
	}
}

func diagnosisCandidate(base *Artifact) *Artifact {
	artifact := cloneArtifact(base)
	artifact.DecisionSource = DecisionSourceDiagnosis
	artifact.EvidencePaths = normalizeArtifactPaths(base.VesselID, base.EvidencePaths)
	combined := strings.ToLower(strings.Join([]string{base.Error, base.GateOutput, base.Rationale}, "\n"))

	switch {
	case containsAny(combined, "acceptance criteria", "missing requirement", "ambiguous requirement", "ambiguous spec", "need clarification"):
		artifact.RecoveryClass = ClassSpecGap
		artifact.RecoveryAction = ActionRequestInfo
		artifact.Confidence = 0.83
		artifact.FollowUpRoute = followUpRouteFor(artifact.RecoveryAction)
		artifact.Rationale = fmt.Sprintf("Diagnosis reviewed %s and found missing requirements that block a safe retry.", strings.Join(artifact.EvidencePaths, ", "))
		artifact.RetrySuppressed = true
		artifact.RetryOutcome = "suppressed"
		artifact.RequiresDecisionRefresh = true
		artifact.RetryPreconditions = []string{"Update the source requirements and rerun diagnosis before retrying."}
	case containsAny(combined, "too broad", "split into", "separate issue", "multiple tasks", "too much work"):
		artifact.RecoveryClass = ClassScopeGap
		artifact.RecoveryAction = ActionSplitIssue
		artifact.Confidence = 0.82
		artifact.FollowUpRoute = followUpRouteFor(artifact.RecoveryAction)
		artifact.Rationale = fmt.Sprintf("Diagnosis reviewed %s and found that the failure is caused by scope that exceeds one vessel.", strings.Join(artifact.EvidencePaths, ", "))
		artifact.RetrySuppressed = true
		artifact.RetryOutcome = "suppressed"
		artifact.RequiresDecisionRefresh = true
		artifact.RetryPreconditions = []string{"Split or refine the work item and rerun diagnosis before retrying."}
	case containsAny(combined, "harness", "agents.md", "policy blocked", "instruction", "prompt template", "protected surface"):
		artifact.RecoveryClass = ClassHarnessGap
		artifact.RecoveryAction = ActionHarnessPatch
		artifact.Confidence = 0.84
		artifact.FollowUpRoute = ""
		artifact.Rationale = fmt.Sprintf("Diagnosis reviewed %s and found a harness or workflow gap rather than a source issue.", strings.Join(artifact.EvidencePaths, ", "))
		artifact.RetrySuppressed = true
		artifact.RetryOutcome = "suppressed"
		artifact.RequiresDecisionRefresh = true
		artifact.RetryPreconditions = []string{"Merge the relevant harness or workflow fix and rerun diagnosis before retrying."}
	case containsAny(combined, "timeout", "timed out", "deadline exceeded", "temporary failure", "429", "502", "503", "504") && base.RepeatedFailureCount < 3:
		artifact.RecoveryClass = ClassTransient
		artifact.RecoveryAction = ActionRetry
		artifact.Confidence = 0.72
		artifact.FollowUpRoute = ""
		artifact.Rationale = fmt.Sprintf("Diagnosis reviewed %s and still found transient evidence strong enough to allow a bounded retry.", strings.Join(artifact.EvidencePaths, ", "))
		artifact.RetrySuppressed = false
		artifact.RetryOutcome = "not_attempted"
		artifact.RequiresDecisionRefresh = false
		artifact.RetryPreconditions = []string{"Review the cited artifacts and confirm the retry budget before retrying."}
	default:
		artifact.RecoveryClass = ClassUnknown
		artifact.RecoveryAction = ActionHumanEscalation
		artifact.Confidence = 0.79
		artifact.FollowUpRoute = ""
		artifact.Rationale = fmt.Sprintf("Diagnosis reviewed %s but still found ambiguous or repeated evidence that requires human escalation.", strings.Join(artifact.EvidencePaths, ", "))
		artifact.RetrySuppressed = true
		artifact.RetryOutcome = "suppressed"
		artifact.RequiresDecisionRefresh = true
		artifact.RetryPreconditions = []string{"Refresh the recovery decision after a human reviews the cited artifacts."}
	}

	return artifact
}

func mergeDiagnosisCandidate(base, candidate *Artifact) (*Artifact, error) {
	if base == nil || candidate == nil {
		return nil, fmt.Errorf("base and candidate artifacts must not be nil")
	}
	if candidate.VesselID != "" && candidate.VesselID != base.VesselID {
		return nil, fmt.Errorf("candidate vessel ID %q does not match %q", candidate.VesselID, base.VesselID)
	}

	merged := cloneArtifact(base)
	merged.SchemaVersion = schemaVersion
	merged.DecisionSource = DecisionSourceDiagnosis
	merged.RecoveryClass = candidate.RecoveryClass
	merged.Confidence = candidate.Confidence
	merged.RecoveryAction = candidate.RecoveryAction
	merged.Rationale = candidate.Rationale
	merged.EvidencePaths = normalizeArtifactPaths(base.VesselID, candidate.EvidencePaths)
	merged.RetryPreconditions = normalizePreconditions(candidate.RetryPreconditions)
	merged.FollowUpRoute = candidate.FollowUpRoute
	merged.RetrySuppressed = candidate.RetrySuppressed
	merged.RetryOutcome = candidate.RetryOutcome
	merged.UnlockDimension = candidate.UnlockDimension
	merged.RequiresDecisionRefresh = candidate.RequiresDecisionRefresh
	if candidate.RepeatedFailureCount > 0 {
		merged.RepeatedFailureCount = candidate.RepeatedFailureCount
	}
	return merged, nil
}

func defaultRetryPreconditions(action Action, repeatedFailureCount int) []string {
	switch action {
	case ActionRetry:
		if repeatedFailureCount >= repeatedFailureThreshold {
			return []string{"Review the repeated failure evidence before retrying."}
		}
		return []string{"Operator reviewed the failure context before retrying."}
	case ActionDiagnose:
		return []string{"Run the diagnosis workflow and replace the current failure-review decision before retrying."}
	default:
		return nil
	}
}

func followUpRouteFor(action Action) string {
	switch action {
	case ActionRefine, ActionSplitTask, ActionRequestInfo, ActionSplitIssue:
		return "needs-refinement"
	default:
		return ""
	}
}

func failureFingerprint(input Input) string {
	normalized := normalizeFailure(strings.Join([]string{
		input.Workflow,
		input.FailedPhase,
		input.Error,
		input.GateOutput,
	}, "\n"))
	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%x", sum)
}

func fingerprintForVessel(vessel queue.Vessel) string {
	metaFingerprint := strings.TrimSpace(vessel.Meta[MetaFailureFingerprint])
	if metaFingerprint != "" {
		return metaFingerprint
	}
	return failureFingerprint(Input{
		Workflow:    vessel.Workflow,
		State:       vessel.State,
		FailedPhase: vessel.FailedPhase,
		Error:       vessel.Error,
		GateOutput:  vessel.GateOutput,
	})
}

func retryRoot(vessel queue.Vessel) string {
	if vessel.RetryOf != "" {
		return stripRetrySuffix(vessel.RetryOf)
	}
	return stripRetrySuffix(vessel.ID)
}

func stripRetrySuffix(id string) string {
	if idx := strings.Index(id, "-retry-"); idx >= 0 {
		return id[:idx]
	}
	return id
}

func normalizeFailure(text string) string {
	fields := strings.Fields(strings.ToLower(text))
	return strings.Join(fields, " ")
}

func normalizeArtifactPaths(vesselID string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" {
			continue
		}
		if err := validateArtifactPath(vesselID, path); err != nil {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	slices.Sort(out)
	return out
}

func normalizePreconditions(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validateArtifactPath(vesselID, path string) error {
	if path == "" {
		return fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("path must be relative")
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return fmt.Errorf("path must not traverse parent directories")
	}
	prefix := fmt.Sprintf("phases/%s/", vesselID)
	if !strings.HasPrefix(cleaned, prefix) {
		return fmt.Errorf("path must stay under %q", prefix)
	}
	return nil
}

func cloneArtifact(artifact *Artifact) *Artifact {
	if artifact == nil {
		return nil
	}
	cloned := *artifact
	if artifact.Trace != nil {
		trace := *artifact.Trace
		cloned.Trace = &trace
	}
	cloned.EvidencePaths = append([]string(nil), artifact.EvidencePaths...)
	cloned.RetryPreconditions = append([]string(nil), artifact.RetryPreconditions...)
	return &cloned
}

func intValue(meta map[string]string, key string) int {
	if meta == nil {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimSpace(meta[key]))
	if err != nil {
		return 0
	}
	return value
}

func metaValue(meta map[string]string, key string) string {
	if meta == nil {
		return ""
	}
	return meta[key]
}

func metaInt(meta map[string]string, key string) int {
	value := strings.TrimSpace(metaValue(meta, key))
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

func containsAny(text string, substrings ...string) bool {
	for _, substring := range substrings {
		if strings.Contains(text, substring) {
			return true
		}
	}
	return false
}

func retryAfterFor(createdAt time.Time, retryCount int) time.Time {
	backoffMultiplier := 1 << max(retryCount, 0)
	return createdAt.Add(time.Duration(backoffMultiplier) * DefaultRetryCooldown).UTC()
}

func computeFailureFingerprint(input Input) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(strings.Join([]string{
		string(input.State),
		input.FailedPhase,
		normalizeFailureText(input.Error),
		normalizeFailureText(input.GateOutput),
	}, "\n")))))
	return fmt.Sprintf("fail-%x", sum)
}

func normalizeFailureText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(strings.ToLower(text))), " ")
}

func DecisionDigest(artifact *Artifact) string {
	if artifact == nil {
		return ""
	}
	return decisionDigestFor(
		artifact.RecoveryClass,
		artifact.RecoveryAction,
		artifact.Rationale,
		artifact.FollowUpRoute,
		artifact.RetrySuppressed,
		artifact.RetryCap,
	)
}

func NextRemediationEpoch(artifact *Artifact) string {
	if artifact == nil {
		return "0"
	}
	return strconv.Itoa(max(artifact.RetryCount, 0) + 1)
}

func ComputeRemediationFingerprint(state RemediationState) string {
	state = normalizeRemediationState(state)
	sum := sha256.Sum256([]byte(strings.Join([]string{
		state.SourceInputFP,
		state.HarnessDigest,
		state.WorkflowDigest,
		state.DecisionDigest,
		state.RemediationEpoch,
	}, "\n")))
	return fmt.Sprintf("rem-%x", sum)
}

func remediationFingerprint(sourceFingerprint, unlockDimension string, retryCount int) string {
	return ComputeRemediationFingerprint(RemediationState{
		SourceInputFP:    sourceFingerprint,
		DecisionDigest:   unlockDimension,
		RemediationEpoch: strconv.Itoa(retryCount),
	})
}

func ApplyRemediationState(meta map[string]string, state RemediationState) map[string]string {
	if meta == nil {
		meta = make(map[string]string)
	}
	state = normalizeRemediationState(state)
	if state.SourceInputFP != "" {
		meta["source_input_fingerprint"] = state.SourceInputFP
	}
	setOrDelete(meta, MetaHarnessDigest, state.HarnessDigest)
	setOrDelete(meta, MetaWorkflowDigest, state.WorkflowDigest)
	setOrDelete(meta, MetaDecisionDigest, state.DecisionDigest)
	setOrDelete(meta, MetaRemediationEpoch, state.RemediationEpoch)
	if state.RemediationFP == "" {
		state.RemediationFP = ComputeRemediationFingerprint(state)
	}
	setOrDelete(meta, MetaRemediationFingerprint, state.RemediationFP)
	return meta
}

func RemediationStateFromMeta(meta map[string]string) RemediationState {
	return normalizeRemediationState(RemediationState{
		SourceInputFP:    metaValue(meta, "source_input_fingerprint"),
		HarnessDigest:    metaValue(meta, MetaHarnessDigest),
		WorkflowDigest:   metaValue(meta, MetaWorkflowDigest),
		DecisionDigest:   metaValue(meta, MetaDecisionDigest),
		RemediationEpoch: metaValue(meta, MetaRemediationEpoch),
		RemediationFP:    metaValue(meta, MetaRemediationFingerprint),
	})
}

func DigestFile(path, prefix string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%s-%x", prefix, sum)
}

func HydrateArtifact(artifact *Artifact, meta map[string]string) *Artifact {
	if artifact == nil {
		return nil
	}
	hydrated := *artifact
	state := normalizeRemediationState(RemediationState{
		SourceInputFP:    firstNonEmpty(hydrated.SourceInputFP, metaValue(meta, "source_input_fingerprint")),
		HarnessDigest:    firstNonEmpty(hydrated.HarnessDigest, metaValue(meta, MetaHarnessDigest)),
		WorkflowDigest:   firstNonEmpty(hydrated.WorkflowDigest, metaValue(meta, MetaWorkflowDigest)),
		DecisionDigest:   firstNonEmpty(hydrated.DecisionDigest, metaValue(meta, MetaDecisionDigest), DecisionDigest(&hydrated)),
		RemediationEpoch: firstNonEmpty(hydrated.RemediationEpoch, metaValue(meta, MetaRemediationEpoch), strconv.Itoa(max(hydrated.RetryCount, 0))),
	})
	hydrated.SourceInputFP = state.SourceInputFP
	hydrated.HarnessDigest = state.HarnessDigest
	hydrated.WorkflowDigest = state.WorkflowDigest
	hydrated.DecisionDigest = state.DecisionDigest
	hydrated.RemediationEpoch = state.RemediationEpoch
	hydrated.RemediationFP = ComputeRemediationFingerprint(state)
	return &hydrated
}

func copyMeta(meta map[string]string) map[string]string {
	if len(meta) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(meta))
	for key, value := range meta {
		cloned[key] = value
	}
	return cloned
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func validatePathComponent(component string) error {
	if component == "" {
		return fmt.Errorf("path component must not be empty")
	}
	if strings.Contains(component, "..") {
		return fmt.Errorf("path component must not contain %q", "..")
	}
	if !safePathComponent.MatchString(component) {
		return fmt.Errorf("path component %q contains invalid characters (allowed: a-zA-Z0-9._-)", component)
	}
	return nil
}

func remediationStateFromInput(input Input) RemediationState {
	return normalizeRemediationState(RemediationState{
		SourceInputFP:    firstNonEmpty(input.SourceInputFP, metaValue(input.Meta, "source_input_fingerprint")),
		HarnessDigest:    firstNonEmpty(input.HarnessDigest, metaValue(input.Meta, MetaHarnessDigest)),
		WorkflowDigest:   firstNonEmpty(input.WorkflowDigest, metaValue(input.Meta, MetaWorkflowDigest)),
		DecisionDigest:   firstNonEmpty(input.DecisionDigest, metaValue(input.Meta, MetaDecisionDigest)),
		RemediationEpoch: firstNonEmpty(input.RemediationEpoch, metaValue(input.Meta, MetaRemediationEpoch)),
		RemediationFP:    firstNonEmpty(input.RemediationFP, metaValue(input.Meta, MetaRemediationFingerprint)),
	})
}

func normalizeRemediationState(state RemediationState) RemediationState {
	state.SourceInputFP = strings.TrimSpace(state.SourceInputFP)
	state.HarnessDigest = strings.TrimSpace(state.HarnessDigest)
	state.WorkflowDigest = strings.TrimSpace(state.WorkflowDigest)
	state.DecisionDigest = strings.TrimSpace(state.DecisionDigest)
	state.RemediationEpoch = strings.TrimSpace(state.RemediationEpoch)
	state.RemediationFP = strings.TrimSpace(state.RemediationFP)
	return state
}

func remediationUnlockDimension(stored, current RemediationState) string {
	switch {
	case current.SourceInputFP != stored.SourceInputFP:
		return "source"
	case current.HarnessDigest != stored.HarnessDigest:
		return "harness"
	case current.WorkflowDigest != stored.WorkflowDigest:
		return "workflow"
	case current.DecisionDigest != stored.DecisionDigest:
		return "decision"
	case current.RemediationEpoch != stored.RemediationEpoch:
		return "cooldown"
	default:
		return "cooldown"
	}
}

func decisionDigestFor(class Class, action Action, rationale, followUpRoute string, retrySuppressed bool, retryCap int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(class),
		string(action),
		normalizeFailureText(rationale),
		strings.TrimSpace(followUpRoute),
		strconv.FormatBool(retrySuppressed),
		strconv.Itoa(retryCap),
	}, "\n")))
	return fmt.Sprintf("dec-%x", sum)
}

func setOrDelete(meta map[string]string, key, value string) {
	if strings.TrimSpace(value) == "" {
		delete(meta, key)
		return
	}
	meta[key] = value
}
