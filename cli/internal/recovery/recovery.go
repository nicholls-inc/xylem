package recovery

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

const (
	artifactFileName = "failure-review.json"
	schemaVersion    = "v1"

	DefaultRetryCap            = 2
	DefaultRetryCooldown       = 5 * time.Minute
	MetaClass                  = "recovery_class"
	MetaAction                 = "recovery_action"
	MetaRationale              = "recovery_rationale"
	MetaFollowUpRoute          = "recovery_followup_route"
	MetaRetrySuppressed        = "recovery_retry_suppressed"
	MetaRetryOutcome           = "recovery_retry_outcome"
	MetaRetryCount             = "recovery_retry_count"
	MetaRetryCap               = "recovery_retry_cap"
	MetaRetryAfter             = "recovery_retry_after"
	MetaFailureFingerprint     = "failure_fingerprint"
	MetaRemediationFingerprint = "remediation_fingerprint"
	MetaHarnessDigest          = "harness_digest"
	MetaWorkflowDigest         = "workflow_digest"
	MetaDecisionDigest         = "decision_digest"
	MetaRemediationEpoch       = "remediation_epoch"
	MetaUnlockedBy             = "recovery_unlocked_by"
	MetaUnlockDimension        = "recovery_unlock_dimension"
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
	ActionRetry     Action = "retry"
	ActionLessons   Action = "lessons"
	ActionRefine    Action = "refine"
	ActionSplitTask Action = "split_task"
	ActionDiagnose  Action = "diagnose"
)

type Artifact struct {
	SchemaVersion      string                          `json:"schema_version"`
	VesselID           string                          `json:"vessel_id"`
	Source             string                          `json:"source,omitempty"`
	Workflow           string                          `json:"workflow,omitempty"`
	Ref                string                          `json:"ref,omitempty"`
	State              string                          `json:"state"`
	FailedPhase        string                          `json:"failed_phase,omitempty"`
	Error              string                          `json:"error,omitempty"`
	GateOutput         string                          `json:"gate_output,omitempty"`
	FailureFingerprint string                          `json:"failure_fingerprint,omitempty"`
	SourceInputFP      string                          `json:"source_input_fingerprint,omitempty"`
	HarnessDigest      string                          `json:"harness_digest,omitempty"`
	WorkflowDigest     string                          `json:"workflow_digest,omitempty"`
	DecisionDigest     string                          `json:"decision_digest,omitempty"`
	RemediationEpoch   string                          `json:"remediation_epoch,omitempty"`
	RemediationFP      string                          `json:"remediation_fingerprint,omitempty"`
	RecoveryClass      Class                           `json:"recovery_class"`
	RecoveryAction     Action                          `json:"recovery_action"`
	Rationale          string                          `json:"rationale"`
	FollowUpRoute      string                          `json:"follow_up_route,omitempty"`
	RetrySuppressed    bool                            `json:"retry_suppressed"`
	RetryCount         int                             `json:"retry_count"`
	RetryCap           int                             `json:"retry_cap"`
	RetryAfter         *time.Time                      `json:"retry_after,omitempty"`
	RetryOutcome       string                          `json:"retry_outcome,omitempty"`
	UnlockDimension    string                          `json:"unlock_dimension,omitempty"`
	RetryOf            string                          `json:"retry_of,omitempty"`
	Trace              *observability.TraceContextData `json:"trace,omitempty"`
	CreatedAt          time.Time                       `json:"created_at"`
}

type Input struct {
	VesselID         string
	Source           string
	Workflow         string
	Ref              string
	State            queue.VesselState
	FailedPhase      string
	Error            string
	GateOutput       string
	RetryOf          string
	SourceInputFP    string
	HarnessDigest    string
	WorkflowDigest   string
	DecisionDigest   string
	RemediationEpoch string
	RemediationFP    string
	Meta             map[string]string
	Trace            *observability.TraceContextData
	CreatedAt        time.Time
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

	class, action, rationale := classify(input)
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

	unlockDimension := strings.TrimSpace(firstNonEmpty(
		metaValue(input.Meta, MetaUnlockedBy),
		metaValue(input.Meta, MetaUnlockDimension),
	))
	failureFingerprint := strings.TrimSpace(metaValue(input.Meta, MetaFailureFingerprint))
	if failureFingerprint == "" {
		failureFingerprint = computeFailureFingerprint(input)
	}
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

	return &Artifact{
		SchemaVersion:      schemaVersion,
		VesselID:           input.VesselID,
		Source:             input.Source,
		Workflow:           input.Workflow,
		Ref:                input.Ref,
		State:              string(input.State),
		FailedPhase:        input.FailedPhase,
		Error:              input.Error,
		GateOutput:         input.GateOutput,
		FailureFingerprint: failureFingerprint,
		SourceInputFP:      remediation.SourceInputFP,
		HarnessDigest:      remediation.HarnessDigest,
		WorkflowDigest:     remediation.WorkflowDigest,
		DecisionDigest:     remediation.DecisionDigest,
		RemediationEpoch:   remediation.RemediationEpoch,
		RemediationFP:      remediation.RemediationFP,
		RecoveryClass:      class,
		RecoveryAction:     action,
		Rationale:          rationale,
		FollowUpRoute:      followUpRoute,
		RetrySuppressed:    retrySuppressed,
		RetryCount:         retryCount,
		RetryCap:           retryCap,
		RetryAfter:         retryAfter,
		RetryOutcome:       retryOutcome,
		UnlockDimension:    unlockDimension,
		RetryOf:            input.RetryOf,
		Trace:              trace,
		CreatedAt:          createdAt,
	}
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
	if artifact.FollowUpRoute != "" {
		meta[MetaFollowUpRoute] = artifact.FollowUpRoute
	} else {
		delete(meta, MetaFollowUpRoute)
	}
	meta[MetaRetrySuppressed] = strconv.FormatBool(artifact.RetrySuppressed)
	meta[MetaRetryCount] = strconv.Itoa(artifact.RetryCount)
	meta[MetaRetryCap] = strconv.Itoa(artifact.RetryCap)
	meta[MetaFailureFingerprint] = artifact.FailureFingerprint
	if artifact.RetryOutcome != "" {
		meta[MetaRetryOutcome] = artifact.RetryOutcome
	} else {
		delete(meta, MetaRetryOutcome)
	}
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
	return filepath.Join(stateDir, "phases", vesselID, artifactFileName)
}

func RelativePath(vesselID string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, artifactFileName))
}

func Save(stateDir string, artifact *Artifact) error {
	if artifact == nil {
		return fmt.Errorf("save recovery artifact: artifact must not be nil")
	}
	if err := validatePathComponent(artifact.VesselID); err != nil {
		return fmt.Errorf("save recovery artifact: invalid vessel ID: %w", err)
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
		ID:        RetryID(parent.ID, q),
		Source:    firstNonEmpty(base.Source, parent.Source),
		Ref:       firstNonEmpty(base.Ref, parent.Ref),
		Workflow:  firstNonEmpty(base.Workflow, parent.Workflow),
		Prompt:    firstNonEmpty(base.Prompt, parent.Prompt),
		Meta:      meta,
		State:     queue.StatePending,
		CreatedAt: createdAt.UTC(),
		RetryOf:   parent.ID,
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

func classify(input Input) (Class, Action, string) {
	combined := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		input.Error,
		input.GateOutput,
	}, "\n")))

	switch {
	case input.State == queue.StateTimedOut ||
		containsAny(combined, "timeout", "timed out", "deadline exceeded", "connection reset", "connection refused",
			"temporarily unavailable", "temporary failure", "rate limit", "429", "502", "503", "504"):
		return ClassTransient, ActionRetry, "The failure looks transient, so retry remains the preferred recovery path."
	case containsAny(combined, "harness", "agents.md", "protected surface", "policy blocked", "instruction", "prompt template"):
		return ClassHarnessGap, ActionLessons, "The failure points at missing or incorrect harness guidance and should feed institutional memory instead of repeating the same run."
	case containsAny(combined, "acceptance criteria", "unclear requirement", "missing requirement", "spec gap", "requirements gap",
		"underspecified", "need clarification", "needs clarification", "ambiguous requirement", "ambiguous spec"):
		return ClassSpecGap, ActionRefine, "The task is underspecified, so it should route back through refinement instead of retrying unchanged."
	case containsAny(combined, "too broad", "split task", "split into", "scope gap", "out of scope", "multiple tasks", "too much work",
		"larger than one change", "separate issue"):
		return ClassScopeGap, ActionSplitTask, "The task exceeds the current execution scope and should be refined or split before another attempt."
	default:
		return ClassUnknown, ActionDiagnose, "The failure needs diagnosis before xylem can safely decide whether to retry or route follow-up work."
	}
}

func followUpRouteFor(action Action) string {
	switch action {
	case ActionRefine, ActionSplitTask:
		return "needs-refinement"
	default:
		return ""
	}
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
