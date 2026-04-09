package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

const (
	artifactFileName = "failure-review.json"
	schemaVersion    = "v1"

	MetaClass                  = "recovery_class"
	MetaAction                 = "recovery_action"
	MetaRationale              = "recovery_rationale"
	MetaFollowUpRoute          = "recovery_followup_route"
	MetaRetrySuppressed        = "recovery_retry_suppressed"
	MetaRetryOutcome           = "recovery_retry_outcome"
	MetaUnlockDimension        = "recovery_unlock_dimension"
	MetaUnlockedBy             = "recovery_unlocked_by"
	MetaRetryCount             = "recovery_retry_count"
	MetaRetryCap               = "recovery_retry_cap"
	MetaRetryAfter             = "recovery_retry_after"
	MetaFailureFingerprint     = "failure_fingerprint"
	MetaRemediationFingerprint = "remediation_fingerprint"
)

var safePathComponent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

const (
	defaultStateDir      = ".xylem"
	defaultRetryCap      = 2
	defaultRetryCooldown = 15 * time.Minute
)

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
	RecoveryClass      Class                           `json:"recovery_class"`
	RecoveryAction     Action                          `json:"recovery_action"`
	Rationale          string                          `json:"rationale"`
	FollowUpRoute      string                          `json:"follow_up_route,omitempty"`
	FailureFingerprint string                          `json:"failure_fingerprint,omitempty"`
	RetrySuppressed    bool                            `json:"retry_suppressed"`
	RetryOutcome       string                          `json:"retry_outcome,omitempty"`
	RetryCount         int                             `json:"retry_count,omitempty"`
	RetryCap           int                             `json:"retry_cap,omitempty"`
	RetryAfter         *time.Time                      `json:"retry_after,omitempty"`
	UnlockDimension    string                          `json:"unlock_dimension,omitempty"`
	RetryOf            string                          `json:"retry_of,omitempty"`
	Unlock             *Unlock                         `json:"unlock,omitempty"`
	Trace              *observability.TraceContextData `json:"trace,omitempty"`
	CreatedAt          time.Time                       `json:"created_at"`
}

type Unlock struct {
	SourceInputFingerprint string `json:"source_input_fingerprint,omitempty"`
	HarnessDigest          string `json:"harness_digest,omitempty"`
	WorkflowDigest         string `json:"workflow_digest,omitempty"`
	DecisionDigest         string `json:"decision_digest,omitempty"`
	RemediationEpoch       string `json:"remediation_epoch,omitempty"`
	RemediationFingerprint string `json:"remediation_fingerprint,omitempty"`
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
	retryCount := parseMetaInt(input.Meta, MetaRetryCount)
	retryCap := parseMetaInt(input.Meta, MetaRetryCap)
	if action == ActionRetry && retryCap <= 0 {
		retryCap = defaultRetryCap
	}
	retryAfter := parseMetaTime(input.Meta, MetaRetryAfter)
	if action == ActionRetry && retryAfter == nil {
		next := createdAt.Add(defaultRetryBackoff(retryCount))
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

	unlockDimension := strings.TrimSpace(metaValue(input.Meta, MetaUnlockDimension))
	if unlockDimension == "" {
		unlockDimension = strings.TrimSpace(metaValue(input.Meta, MetaUnlockedBy))
	}
	trace := input.Trace
	if trace != nil && trace.TraceID == "" && trace.SpanID == "" {
		trace = nil
	}

	artifact := &Artifact{
		SchemaVersion:      schemaVersion,
		VesselID:           input.VesselID,
		Source:             input.Source,
		Workflow:           input.Workflow,
		Ref:                input.Ref,
		State:              string(input.State),
		FailedPhase:        input.FailedPhase,
		Error:              input.Error,
		GateOutput:         input.GateOutput,
		RecoveryClass:      class,
		RecoveryAction:     action,
		Rationale:          rationale,
		FollowUpRoute:      followUpRoute,
		FailureFingerprint: failureFingerprint(input.State, input.FailedPhase, input.Error, input.GateOutput),
		RetrySuppressed:    retrySuppressed,
		RetryOutcome:       retryOutcome,
		RetryCount:         retryCount,
		RetryCap:           retryCap,
		RetryAfter:         retryAfter,
		UnlockDimension:    unlockDimension,
		RetryOf:            input.RetryOf,
		Trace:              trace,
		CreatedAt:          createdAt,
	}
	return artifact
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
	if artifact.RetryOutcome != "" {
		meta[MetaRetryOutcome] = artifact.RetryOutcome
	} else {
		delete(meta, MetaRetryOutcome)
	}
	if artifact.RetryCount > 0 {
		meta[MetaRetryCount] = strconv.Itoa(artifact.RetryCount)
	} else {
		delete(meta, MetaRetryCount)
	}
	if artifact.RetryCap > 0 {
		meta[MetaRetryCap] = strconv.Itoa(artifact.RetryCap)
	} else {
		delete(meta, MetaRetryCap)
	}
	if artifact.RetryAfter != nil {
		meta[MetaRetryAfter] = artifact.RetryAfter.UTC().Format(time.RFC3339Nano)
	} else {
		delete(meta, MetaRetryAfter)
	}
	if artifact.UnlockDimension != "" {
		meta[MetaUnlockDimension] = artifact.UnlockDimension
		meta[MetaUnlockedBy] = artifact.UnlockDimension
	} else {
		delete(meta, MetaUnlockDimension)
		delete(meta, MetaUnlockedBy)
	}
	if artifact.FailureFingerprint != "" {
		meta[MetaFailureFingerprint] = artifact.FailureFingerprint
	} else {
		delete(meta, MetaFailureFingerprint)
	}
	if artifact.Unlock != nil && artifact.Unlock.RemediationFingerprint != "" {
		meta[MetaRemediationFingerprint] = artifact.Unlock.RemediationFingerprint
	} else {
		delete(meta, MetaRemediationFingerprint)
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

type RetryGateDecision struct {
	Blocked                bool
	UnlockDimension        string
	FailureFingerprint     string
	RemediationFingerprint string
	Artifact               *Artifact
}

func PopulateUnlock(stateDir string, artifact *Artifact, sourceFingerprint string, now time.Time) error {
	if artifact == nil {
		return nil
	}
	unlock, err := computeUnlock(normalizeStateDir(stateDir), artifact.Workflow, sourceFingerprint, artifact, now)
	if err != nil {
		return err
	}
	artifact.Unlock = unlock
	return nil
}

func EvaluateRetryGate(stateDir string, latest *queue.Vessel, currentWorkflow, currentSourceFingerprint string, now time.Time) (RetryGateDecision, error) {
	if latest == nil {
		return RetryGateDecision{}, nil
	}
	switch latest.State {
	case queue.StatePending, queue.StateRunning, queue.StateWaiting:
		return RetryGateDecision{Blocked: true}, nil
	case queue.StateFailed, queue.StateTimedOut:
	default:
		return RetryGateDecision{}, nil
	}

	artifact, err := Load(Path(normalizeStateDir(stateDir), latest.ID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RetryGateDecision{Blocked: latest.Meta["source_input_fingerprint"] == currentSourceFingerprint}, nil
		}
		return RetryGateDecision{}, err
	}
	if artifact.Workflow == "" {
		artifact.Workflow = currentWorkflow
	}

	stored := *artifact
	storedSourceFingerprint := latest.Meta["source_input_fingerprint"]
	if stored.Unlock != nil && stored.Unlock.SourceInputFingerprint != "" {
		storedSourceFingerprint = stored.Unlock.SourceInputFingerprint
	}
	if stored.Unlock == nil || stored.Unlock.RemediationFingerprint == "" {
		if err := PopulateUnlock(stateDir, &stored, storedSourceFingerprint, stored.CreatedAt); err != nil {
			return RetryGateDecision{}, err
		}
	}

	current := *artifact
	if err := PopulateUnlock(stateDir, &current, currentSourceFingerprint, now.UTC()); err != nil {
		return RetryGateDecision{}, err
	}
	if current.RecoveryAction != ActionRetry {
		return RetryGateDecision{Blocked: true, Artifact: &current}, nil
	}
	if current.RetryCap > 0 && current.RetryCount >= current.RetryCap {
		return RetryGateDecision{Blocked: true, Artifact: &current}, nil
	}
	if current.RetryAfter != nil && now.UTC().Before(current.RetryAfter.UTC()) {
		return RetryGateDecision{Blocked: true, Artifact: &current}, nil
	}
	if stored.Unlock == nil || current.Unlock == nil || stored.Unlock.RemediationFingerprint == current.Unlock.RemediationFingerprint {
		return RetryGateDecision{Blocked: true, Artifact: &current}, nil
	}

	return RetryGateDecision{
		Blocked:                false,
		UnlockDimension:        unlockDimension(*stored.Unlock, *current.Unlock),
		FailureFingerprint:     current.FailureFingerprint,
		RemediationFingerprint: current.Unlock.RemediationFingerprint,
		Artifact:               &current,
	}, nil
}

func PrepareRetryMeta(base map[string]string, artifact *Artifact, unlockDimension, remediationFingerprint string) map[string]string {
	meta := make(map[string]string, len(base)+8)
	for k, v := range base {
		meta[k] = v
	}
	delete(meta, MetaRetryAfter)
	delete(meta, MetaRetryOutcome)
	if artifact == nil {
		return meta
	}
	meta[MetaClass] = string(artifact.RecoveryClass)
	meta[MetaAction] = string(artifact.RecoveryAction)
	meta[MetaFailureFingerprint] = artifact.FailureFingerprint
	if artifact.RetryCap > 0 {
		meta[MetaRetryCap] = strconv.Itoa(artifact.RetryCap)
	}
	meta[MetaRetryCount] = strconv.Itoa(artifact.RetryCount + 1)
	if unlockDimension != "" {
		meta[MetaUnlockDimension] = unlockDimension
		meta[MetaUnlockedBy] = unlockDimension
	}
	if remediationFingerprint != "" {
		meta[MetaRemediationFingerprint] = remediationFingerprint
	}
	return meta
}

func RetryRootID(id string) string {
	root := id
	for {
		next, ok := trimRetrySuffix(root)
		if !ok {
			return root
		}
		root = next
	}
}

func NextRetryID(originalID string, vessels []queue.Vessel) string {
	root := RetryRootID(originalID)
	maxRetry := 0
	prefix := root + "-retry-"
	for _, v := range vessels {
		if !strings.HasPrefix(v.ID, prefix) {
			continue
		}
		numStr := strings.TrimPrefix(v.ID, prefix)
		if n, err := strconv.Atoi(numStr); err == nil && n > maxRetry {
			maxRetry = n
		}
	}
	return fmt.Sprintf("%s-retry-%d", root, maxRetry+1)
}

func metaValue(meta map[string]string, key string) string {
	if meta == nil {
		return ""
	}
	return meta[key]
}

func parseMetaInt(meta map[string]string, key string) int {
	value := strings.TrimSpace(metaValue(meta, key))
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func parseMetaTime(meta map[string]string, key string) *time.Time {
	value := strings.TrimSpace(metaValue(meta, key))
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func defaultRetryBackoff(retryCount int) time.Duration {
	if retryCount < 0 {
		retryCount = 0
	}
	return defaultRetryCooldown << retryCount
}

func failureFingerprint(state queue.VesselState, failedPhase, errMsg, gateOutput string) string {
	return hashStrings(
		string(state),
		strings.TrimSpace(failedPhase),
		strings.TrimSpace(errMsg),
		strings.TrimSpace(gateOutput),
	)
}

func computeUnlock(stateDir, workflowName, sourceFingerprint string, artifact *Artifact, now time.Time) (*Unlock, error) {
	harnessDigest, err := harnessDigest(stateDir)
	if err != nil {
		return nil, err
	}
	workflowDigest, err := workflowDigest(stateDir, workflowName)
	if err != nil {
		return nil, err
	}
	unlock := &Unlock{
		SourceInputFingerprint: strings.TrimSpace(sourceFingerprint),
		HarnessDigest:          harnessDigest,
		WorkflowDigest:         workflowDigest,
		DecisionDigest:         decisionDigest(artifact),
		RemediationEpoch:       remediationEpoch(artifact, now.UTC()),
	}
	unlock.RemediationFingerprint = remediationFingerprint(unlock)
	return unlock, nil
}

func harnessDigest(stateDir string) (string, error) {
	return digestFile(filepath.Join(stateDir, "HARNESS.md"))
}

func workflowDigest(stateDir, workflowName string) (string, error) {
	if strings.TrimSpace(workflowName) == "" {
		return "", nil
	}
	workflowPath := filepath.Join(stateDir, "workflows", workflowName+".yaml")
	workflowBytes, err := os.ReadFile(workflowPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read workflow %q: %w", workflowPath, err)
	}

	files := []string{filepath.Clean(workflowPath)}
	if wf, loadErr := workflow.Load(workflowPath); loadErr == nil {
		seen := map[string]struct{}{filepath.Clean(workflowPath): {}}
		for _, phase := range wf.Phases {
			if phase.Type == "command" || strings.TrimSpace(phase.PromptFile) == "" {
				continue
			}
			path := filepath.Clean(phase.PromptFile)
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			files = append(files, path)
		}
	}

	sort.Strings(files)
	payload := make([]string, 0, len(files)*2+1)
	payload = append(payload, string(workflowBytes))
	for _, path := range files[1:] {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("read workflow dependency %q: %w", path, readErr)
		}
		payload = append(payload, path, string(data))
	}
	return hashStrings(payload...), nil
}

func decisionDigest(artifact *Artifact) string {
	if artifact == nil {
		return ""
	}
	retryAfter := ""
	if artifact.RetryAfter != nil {
		retryAfter = artifact.RetryAfter.UTC().Format(time.RFC3339Nano)
	}
	return hashStrings(
		string(artifact.RecoveryClass),
		string(artifact.RecoveryAction),
		artifact.Rationale,
		artifact.FollowUpRoute,
		strconv.Itoa(artifact.RetryCount),
		strconv.Itoa(artifact.RetryCap),
		retryAfter,
	)
}

func remediationEpoch(artifact *Artifact, now time.Time) string {
	if artifact == nil || artifact.RetryAfter == nil {
		return ""
	}
	retryAfter := artifact.RetryAfter.UTC().Format(time.RFC3339Nano)
	if now.Before(artifact.RetryAfter.UTC()) {
		return "cooldown:pending:" + retryAfter
	}
	return "cooldown:eligible:" + retryAfter
}

func remediationFingerprint(unlock *Unlock) string {
	if unlock == nil {
		return ""
	}
	return hashStrings(
		unlock.SourceInputFingerprint,
		unlock.HarnessDigest,
		unlock.WorkflowDigest,
		unlock.DecisionDigest,
		unlock.RemediationEpoch,
	)
}

func digestFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return hashStrings(path, string(data)), nil
}

func hashStrings(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func unlockDimension(stored, current Unlock) string {
	switch {
	case stored.SourceInputFingerprint != current.SourceInputFingerprint:
		return "source"
	case stored.HarnessDigest != current.HarnessDigest:
		return "harness"
	case stored.WorkflowDigest != current.WorkflowDigest:
		return "workflow"
	case stored.DecisionDigest != current.DecisionDigest:
		return "decision"
	case stored.RemediationEpoch != current.RemediationEpoch:
		return "cooldown"
	default:
		return ""
	}
}

func normalizeStateDir(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return defaultStateDir
	}
	return stateDir
}

func trimRetrySuffix(id string) (string, bool) {
	idx := strings.LastIndex(id, "-retry-")
	if idx < 0 {
		return "", false
	}
	if _, err := strconv.Atoi(id[idx+len("-retry-"):]); err != nil {
		return "", false
	}
	return id[:idx], true
}

func containsAny(text string, substrings ...string) bool {
	for _, substring := range substrings {
		if strings.Contains(text, substring) {
			return true
		}
	}
	return false
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
