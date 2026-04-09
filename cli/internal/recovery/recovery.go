package recovery

import (
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

	MetaClass           = "recovery_class"
	MetaAction          = "recovery_action"
	MetaRationale       = "recovery_rationale"
	MetaFollowUpRoute   = "recovery_followup_route"
	MetaRetrySuppressed = "recovery_retry_suppressed"
	MetaRetryOutcome    = "recovery_retry_outcome"
	MetaUnlockDimension = "recovery_unlock_dimension"
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
	SchemaVersion   string                          `json:"schema_version"`
	VesselID        string                          `json:"vessel_id"`
	Source          string                          `json:"source,omitempty"`
	Workflow        string                          `json:"workflow,omitempty"`
	Ref             string                          `json:"ref,omitempty"`
	State           string                          `json:"state"`
	FailedPhase     string                          `json:"failed_phase,omitempty"`
	Error           string                          `json:"error,omitempty"`
	GateOutput      string                          `json:"gate_output,omitempty"`
	RecoveryClass   Class                           `json:"recovery_class"`
	RecoveryAction  Action                          `json:"recovery_action"`
	Rationale       string                          `json:"rationale"`
	FollowUpRoute   string                          `json:"follow_up_route,omitempty"`
	RetrySuppressed bool                            `json:"retry_suppressed"`
	RetryOutcome    string                          `json:"retry_outcome,omitempty"`
	UnlockDimension string                          `json:"unlock_dimension,omitempty"`
	RetryOf         string                          `json:"retry_of,omitempty"`
	Trace           *observability.TraceContextData `json:"trace,omitempty"`
	CreatedAt       time.Time                       `json:"created_at"`
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
	retryOutcome := strings.TrimSpace(metaValue(input.Meta, MetaRetryOutcome))
	if retryOutcome == "" {
		if retrySuppressed {
			retryOutcome = "suppressed"
		} else {
			retryOutcome = "not_attempted"
		}
	}

	unlockDimension := strings.TrimSpace(metaValue(input.Meta, MetaUnlockDimension))
	trace := input.Trace
	if trace != nil && trace.TraceID == "" && trace.SpanID == "" {
		trace = nil
	}

	return &Artifact{
		SchemaVersion:   schemaVersion,
		VesselID:        input.VesselID,
		Source:          input.Source,
		Workflow:        input.Workflow,
		Ref:             input.Ref,
		State:           string(input.State),
		FailedPhase:     input.FailedPhase,
		Error:           input.Error,
		GateOutput:      input.GateOutput,
		RecoveryClass:   class,
		RecoveryAction:  action,
		Rationale:       rationale,
		FollowUpRoute:   followUpRoute,
		RetrySuppressed: retrySuppressed,
		RetryOutcome:    retryOutcome,
		UnlockDimension: unlockDimension,
		RetryOf:         input.RetryOf,
		Trace:           trace,
		CreatedAt:       createdAt,
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
	if artifact.RetryOutcome != "" {
		meta[MetaRetryOutcome] = artifact.RetryOutcome
	} else {
		delete(meta, MetaRetryOutcome)
	}
	if artifact.UnlockDimension != "" {
		meta[MetaUnlockDimension] = artifact.UnlockDimension
	} else {
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
