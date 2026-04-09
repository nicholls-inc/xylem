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
	VesselID    string
	Source      string
	Workflow    string
	Ref         string
	State       queue.VesselState
	FailedPhase string
	Error       string
	GateOutput  string
	RetryOf     string
	Meta        map[string]string
	Trace       *observability.TraceContextData
	CreatedAt   time.Time
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
	if meta == nil {
		meta = make(map[string]string)
	}
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
	if artifact == nil || artifact.RecoveryAction != ActionRetry {
		return RetryDecision{}
	}
	if artifact.RetryCap > 0 && artifact.RetryCount >= artifact.RetryCap {
		return RetryDecision{}
	}
	if artifact.RetryAfter != nil && now.UTC().Before(artifact.RetryAfter.UTC()) {
		return RetryDecision{}
	}
	return RetryDecision{
		Eligible:        true,
		UnlockDimension: "cooldown",
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
	if sourceFingerprint := strings.TrimSpace(meta["source_input_fingerprint"]); sourceFingerprint != "" {
		meta[MetaRemediationFingerprint] = remediationFingerprint(sourceFingerprint, unlockDimension, retryCount)
	}

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

func remediationFingerprint(sourceFingerprint, unlockDimension string, retryCount int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		sourceFingerprint,
		unlockDimension,
		strconv.Itoa(retryCount),
	}, "\n")))
	return fmt.Sprintf("rem-%x", sum)
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
