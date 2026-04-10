package source

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
)

func applyCurrentRemediationMeta(meta map[string]string, artifact *recovery.Artifact, harnessDigest, workflowDigest string) map[string]string {
	state := recovery.RemediationState{
		SourceInputFP:  strings.TrimSpace(meta["source_input_fingerprint"]),
		HarnessDigest:  strings.TrimSpace(harnessDigest),
		WorkflowDigest: strings.TrimSpace(workflowDigest),
		DecisionDigest: recovery.DecisionDigest(artifact),
	}
	if artifact != nil {
		state.RemediationEpoch = recovery.NextRemediationEpoch(artifact)
	} else {
		state.RemediationEpoch = "0"
	}
	return recovery.ApplyRemediationState(meta, state)
}

func defaultHarnessDigest() string {
	return recovery.DigestFile(filepath.Join(".xylem", "HARNESS.md"), "har")
}

func defaultWorkflowDigest(workflow string) string {
	return recovery.DigestFile(filepath.Join(".xylem", "workflows", workflow+".yaml"), "wf")
}

func retryDecision(artifact *recovery.Artifact, previousMeta, currentMeta map[string]string, now time.Time) recovery.RetryDecision {
	if artifact == nil {
		return recovery.RetryDecision{}
	}
	comparison := *artifact
	stored := recovery.RemediationStateFromMeta(previousMeta)
	if stored.SourceInputFP == "" {
		stored.SourceInputFP = strings.TrimSpace(artifact.SourceInputFP)
	}
	if stored.HarnessDigest == "" {
		stored.HarnessDigest = strings.TrimSpace(artifact.HarnessDigest)
	}
	if stored.WorkflowDigest == "" {
		stored.WorkflowDigest = strings.TrimSpace(artifact.WorkflowDigest)
	}
	if stored.DecisionDigest == "" {
		stored.DecisionDigest = strings.TrimSpace(artifact.DecisionDigest)
	}
	if stored.RemediationEpoch == "" {
		stored.RemediationEpoch = strings.TrimSpace(artifact.RemediationEpoch)
	}
	if stored.RemediationFP == "" {
		stored.RemediationFP = strings.TrimSpace(artifact.RemediationFP)
	}
	comparison.SourceInputFP = stored.SourceInputFP
	comparison.HarnessDigest = stored.HarnessDigest
	comparison.WorkflowDigest = stored.WorkflowDigest
	comparison.DecisionDigest = stored.DecisionDigest
	comparison.RemediationEpoch = stored.RemediationEpoch
	comparison.RemediationFP = stored.RemediationFP
	return recovery.RetryReadyWithRemediation(&comparison, recovery.RemediationStateFromMeta(currentMeta), now)
}

func retryCandidateWithoutArtifact(base, latest queue.Vessel, q *queue.Queue, now time.Time) (*queue.Vessel, bool) {
	if strings.TrimSpace(latest.Meta["source_input_fingerprint"]) != strings.TrimSpace(base.Meta["source_input_fingerprint"]) {
		return nil, false
	}
	if !legacyRetryCooldownElapsed(latest, now) {
		return nil, true
	}
	retry := recovery.NextRetryVessel(base, latest, nil, q, now, "cooldown")
	return &retry, false
}

func legacyRetryCooldownElapsed(vessel queue.Vessel, now time.Time) bool {
	anchor := legacyRetryCooldownAnchor(vessel)
	if anchor.IsZero() {
		return true
	}
	return !now.UTC().Before(anchor.Add(recovery.DefaultRetryCooldown))
}

func legacyRetryCooldownAnchor(vessel queue.Vessel) time.Time {
	if vessel.EndedAt != nil && !vessel.EndedAt.IsZero() {
		return vessel.EndedAt.UTC()
	}
	if vessel.StartedAt != nil && !vessel.StartedAt.IsZero() {
		return vessel.StartedAt.UTC()
	}
	if !vessel.CreatedAt.IsZero() {
		return vessel.CreatedAt.UTC()
	}
	return time.Time{}
}
