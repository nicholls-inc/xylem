package recovery

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

const FailureReviewFileName = "failure-review.json"

type UnlockFingerprint struct {
	SourceInputFingerprint string `json:"source_input_fingerprint,omitempty"`
	HarnessDigest          string `json:"harness_digest,omitempty"`
	WorkflowDigest         string `json:"workflow_digest,omitempty"`
	DecisionDigest         string `json:"decision_digest,omitempty"`
}

type FailureReview struct {
	VesselID                string            `json:"vessel_id"`
	FailureFingerprint      string            `json:"failure_fingerprint,omitempty"`
	SourceRef               string            `json:"source_ref,omitempty"`
	Workflow                string            `json:"workflow,omitempty"`
	FailedPhase             string            `json:"failed_phase,omitempty"`
	Class                   string            `json:"class,omitempty"`
	Confidence              float64           `json:"confidence,omitempty"`
	RecommendedAction       string            `json:"recommended_action,omitempty"`
	RetryCount              int               `json:"retry_count,omitempty"`
	RetryCap                int               `json:"retry_cap,omitempty"`
	RetryAfter              *time.Time        `json:"retry_after,omitempty"`
	RequiresHarnessChange   bool              `json:"requires_harness_change,omitempty"`
	RequiresSourceChange    bool              `json:"requires_source_change,omitempty"`
	RequiresDecisionRefresh bool              `json:"requires_decision_refresh,omitempty"`
	EvidencePaths           []string          `json:"evidence_paths,omitempty"`
	Hypothesis              string            `json:"hypothesis,omitempty"`
	Unlock                  UnlockFingerprint `json:"unlock,omitempty"`
	RemediationEpoch        string            `json:"remediation_epoch,omitempty"`
	RemediationFingerprint  string            `json:"remediation_fingerprint,omitempty"`
}

type RetryGateResult struct {
	Allowed            bool
	CurrentFingerprint string
	UnlockedBy         string
	FailureFingerprint string
	RecoveryClass      string
	RecoveryAction     string
}

func FailureReviewPath(stateDir, vesselID string) string {
	return filepath.Join(stateDir, "phases", vesselID, FailureReviewFileName)
}

func FailureReviewRelativePath(vesselID string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, FailureReviewFileName))
}

func SaveFailureReview(stateDir string, review *FailureReview) error {
	if review == nil {
		return fmt.Errorf("save failure review: review must not be nil")
	}
	if err := validatePathComponent(review.VesselID); err != nil {
		return fmt.Errorf("save failure review: invalid vessel ID: %w", err)
	}

	normalized := *review
	normalized.EvidencePaths = normalizePaths(review.EvidencePaths)
	decisionDigest := DecisionDigest(&normalized)
	if normalized.Unlock.DecisionDigest == "" {
		normalized.Unlock.DecisionDigest = decisionDigest
	}
	if normalized.RemediationFingerprint == "" {
		normalized.RemediationFingerprint = RemediationFingerprint(
			normalized.Unlock.SourceInputFingerprint,
			normalized.Unlock.HarnessDigest,
			normalized.Unlock.WorkflowDigest,
			normalized.Unlock.DecisionDigest,
			normalized.RemediationEpoch,
		)
	}

	path := FailureReviewPath(stateDir, normalized.VesselID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save failure review: create dir: %w", err)
	}
	data, err := json.MarshalIndent(&normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("save failure review: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("save failure review: write: %w", err)
	}
	return nil
}

func LoadFailureReview(stateDir, vesselID string) (*FailureReview, error) {
	data, err := os.ReadFile(FailureReviewPath(stateDir, vesselID))
	if err != nil {
		return nil, fmt.Errorf("load failure review: read: %w", err)
	}
	var review FailureReview
	if err := json.Unmarshal(data, &review); err != nil {
		return nil, fmt.Errorf("load failure review: unmarshal: %w", err)
	}
	return &review, nil
}

func DecisionDigest(review *FailureReview) string {
	if review == nil {
		return ""
	}
	evidencePaths := normalizePaths(review.EvidencePaths)
	payload := struct {
		Class                   string   `json:"class,omitempty"`
		Confidence              float64  `json:"confidence,omitempty"`
		RecommendedAction       string   `json:"recommended_action,omitempty"`
		RetryCount              int      `json:"retry_count,omitempty"`
		RetryCap                int      `json:"retry_cap,omitempty"`
		RetryAfter              string   `json:"retry_after,omitempty"`
		RequiresHarnessChange   bool     `json:"requires_harness_change,omitempty"`
		RequiresSourceChange    bool     `json:"requires_source_change,omitempty"`
		RequiresDecisionRefresh bool     `json:"requires_decision_refresh,omitempty"`
		FailedPhase             string   `json:"failed_phase,omitempty"`
		EvidencePaths           []string `json:"evidence_paths,omitempty"`
		Hypothesis              string   `json:"hypothesis,omitempty"`
	}{
		Class:                   strings.TrimSpace(review.Class),
		Confidence:              review.Confidence,
		RecommendedAction:       strings.TrimSpace(review.RecommendedAction),
		RetryCount:              review.RetryCount,
		RetryCap:                review.RetryCap,
		RequiresHarnessChange:   review.RequiresHarnessChange,
		RequiresSourceChange:    review.RequiresSourceChange,
		RequiresDecisionRefresh: review.RequiresDecisionRefresh,
		FailedPhase:             strings.TrimSpace(review.FailedPhase),
		EvidencePaths:           evidencePaths,
		Hypothesis:              strings.TrimSpace(review.Hypothesis),
	}
	if review.RetryAfter != nil && !review.RetryAfter.IsZero() {
		payload.RetryAfter = review.RetryAfter.UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return hashBytes(data)
}

func RemediationFingerprint(sourceFingerprint, harnessDigest, workflowDigest, decisionDigest, remediationEpoch string) string {
	return hashStrings(
		strings.TrimSpace(sourceFingerprint),
		strings.TrimSpace(harnessDigest),
		strings.TrimSpace(workflowDigest),
		strings.TrimSpace(decisionDigest),
		strings.TrimSpace(remediationEpoch),
	)
}

func EvaluateRetry(review *FailureReview, currentSourceFingerprint, currentHarnessDigest, currentWorkflowDigest string, now time.Time) RetryGateResult {
	if review == nil {
		return RetryGateResult{}
	}

	currentDecisionDigest := DecisionDigest(review)
	previousDecisionDigest := strings.TrimSpace(review.Unlock.DecisionDigest)
	if previousDecisionDigest == "" {
		previousDecisionDigest = currentDecisionDigest
	}

	currentEpoch := strings.TrimSpace(review.RemediationEpoch)
	if isCooldownSatisfied(review, now) {
		currentEpoch = cooldownEpoch(review.RetryAfter.UTC())
	}

	result := RetryGateResult{
		CurrentFingerprint: RemediationFingerprint(
			currentSourceFingerprint,
			currentHarnessDigest,
			currentWorkflowDigest,
			currentDecisionDigest,
			currentEpoch,
		),
		FailureFingerprint: strings.TrimSpace(review.FailureFingerprint),
		RecoveryClass:      strings.TrimSpace(review.Class),
		RecoveryAction:     strings.TrimSpace(review.RecommendedAction),
	}

	if !strings.EqualFold(strings.TrimSpace(review.RecommendedAction), "retry") {
		return result
	}
	if review.RetryCap > 0 && review.RetryCount >= review.RetryCap {
		return result
	}
	if review.RetryAfter != nil && !review.RetryAfter.IsZero() && now.Before(review.RetryAfter.UTC()) {
		return result
	}
	if review.RequiresSourceChange && currentSourceFingerprint == strings.TrimSpace(review.Unlock.SourceInputFingerprint) {
		return result
	}
	if review.RequiresHarnessChange && currentHarnessDigest == strings.TrimSpace(review.Unlock.HarnessDigest) {
		return result
	}
	if review.RequiresDecisionRefresh && currentDecisionDigest == previousDecisionDigest {
		return result
	}

	previousFingerprint := strings.TrimSpace(review.RemediationFingerprint)
	if previousFingerprint == "" {
		previousFingerprint = RemediationFingerprint(
			review.Unlock.SourceInputFingerprint,
			review.Unlock.HarnessDigest,
			review.Unlock.WorkflowDigest,
			previousDecisionDigest,
			review.RemediationEpoch,
		)
	}
	if result.CurrentFingerprint == previousFingerprint {
		return result
	}

	result.UnlockedBy = firstUnlockDimension(
		currentSourceFingerprint != strings.TrimSpace(review.Unlock.SourceInputFingerprint),
		currentHarnessDigest != strings.TrimSpace(review.Unlock.HarnessDigest),
		currentWorkflowDigest != strings.TrimSpace(review.Unlock.WorkflowDigest),
		currentDecisionDigest != previousDecisionDigest,
		currentEpoch != strings.TrimSpace(review.RemediationEpoch),
	)
	result.Allowed = result.UnlockedBy != ""
	return result
}

func CurrentHarnessDigest() string {
	return FileDigest(filepath.Join(".xylem", "HARNESS.md"))
}

func CurrentWorkflowDigest(workflowName string) string {
	if strings.TrimSpace(workflowName) == "" {
		return ""
	}
	return FileDigest(filepath.Join(".xylem", "workflows", workflowName+".yaml"))
}

func FileDigest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hashBytes(data)
}

func FailureFingerprint(vessel queue.Vessel) string {
	return hashStrings(
		string(vessel.State),
		strings.TrimSpace(vessel.FailedPhase),
		strings.TrimSpace(vessel.Error),
		strings.TrimSpace(vessel.GateOutput),
	)
}

func RetryID(originalID string, q *queue.Queue) string {
	vessels, err := q.List()
	if err != nil {
		return originalID + "-retry-1"
	}
	maxRetry := 0
	prefix := originalID + "-retry-"
	for _, vessel := range vessels {
		if !strings.HasPrefix(vessel.ID, prefix) {
			continue
		}
		numStr := strings.TrimPrefix(vessel.ID, prefix)
		n, err := strconv.Atoi(numStr)
		if err == nil && n > maxRetry {
			maxRetry = n
		}
	}
	return fmt.Sprintf("%s-retry-%d", originalID, maxRetry+1)
}

func RetryRootID(vessel queue.Vessel) string {
	if vessel.Meta != nil {
		if root := strings.TrimSpace(vessel.Meta["retry_of"]); root != "" {
			return root
		}
	}
	if root := strings.TrimSpace(vessel.RetryOf); root != "" {
		return root
	}
	return strings.TrimSpace(baseRetryID(vessel.ID))
}

func RetryCountFromVessel(vessel queue.Vessel) int {
	if vessel.Meta != nil {
		if raw := strings.TrimSpace(vessel.Meta["retry_count"]); raw != "" {
			if count, err := strconv.Atoi(raw); err == nil && count >= 0 {
				return count
			}
		}
	}
	return retrySuffixCount(vessel.ID)
}

func retrySuffixCount(id string) int {
	base := baseRetryID(id)
	if base == id {
		return 0
	}
	count, err := strconv.Atoi(id[len(base)+len("-retry-"):])
	if err != nil || count < 0 {
		return 0
	}
	return count
}

func baseRetryID(id string) string {
	idx := strings.LastIndex(id, "-retry-")
	if idx < 0 {
		return id
	}
	base := id[:idx]
	if _, err := strconv.Atoi(id[idx+len("-retry-"):]); err != nil {
		return id
	}
	return base
}

func normalizePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		normalized = append(normalized, filepath.ToSlash(path))
	}
	sort.Strings(normalized)
	return normalized
}

func hashStrings(parts ...string) string {
	return hashBytes([]byte(strings.Join(parts, "\n")))
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

func isCooldownSatisfied(review *FailureReview, now time.Time) bool {
	if review == nil || review.RetryAfter == nil || review.RetryAfter.IsZero() {
		return false
	}
	return !now.Before(review.RetryAfter.UTC())
}

func cooldownEpoch(retryAfter time.Time) string {
	return "cooldown:" + retryAfter.UTC().Format(time.RFC3339Nano)
}

func firstUnlockDimension(sourceChanged, harnessChanged, workflowChanged, decisionChanged, cooldownChanged bool) string {
	switch {
	case sourceChanged:
		return "source"
	case harnessChanged:
		return "harness"
	case workflowChanged:
		return "workflow"
	case decisionChanged:
		return "decision"
	case cooldownChanged:
		return "cooldown"
	default:
		return ""
	}
}

func validatePathComponent(component string) error {
	if component == "" {
		return fmt.Errorf("path component must not be empty")
	}
	if strings.Contains(component, "..") {
		return fmt.Errorf("path component must not contain %q", "..")
	}
	if strings.ContainsAny(component, `/\`) {
		return fmt.Errorf("path component %q contains invalid separators", component)
	}
	return nil
}
