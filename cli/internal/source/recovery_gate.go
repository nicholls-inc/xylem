package source

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
)

type reenqueueDecision struct {
	Block  bool
	Parent *queue.Vessel
	NextID string
	Meta   map[string]string
}

func (g *GitHub) reenqueueDecision(prior *queue.Vessel, fingerprint string) (reenqueueDecision, error) {
	return evaluateReenqueue(g.Queue, g.StateDir, prior, fingerprint)
}

func (g *GitHubPR) reenqueueDecision(prior *queue.Vessel, fingerprint string) (reenqueueDecision, error) {
	return evaluateReenqueue(g.Queue, g.StateDir, prior, fingerprint)
}

func evaluateReenqueue(q *queue.Queue, stateDir string, prior *queue.Vessel, fingerprint string) (reenqueueDecision, error) {
	if prior == nil {
		return reenqueueDecision{}, nil
	}

	switch prior.State {
	case queue.StatePending, queue.StateRunning, queue.StateWaiting:
		return reenqueueDecision{Block: true}, nil
	case queue.StateFailed, queue.StateTimedOut:
	default:
		return reenqueueDecision{}, nil
	}

	review, err := recovery.LoadFailureReview(stateDir, prior.ID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return reenqueueDecision{}, fmt.Errorf("load failure review for %s: %w", prior.ID, err)
		}
		if prior.Meta["source_input_fingerprint"] == fingerprint {
			return reenqueueDecision{Block: true}, nil
		}
		return newRetryDecision(q, prior, map[string]string{
			"recovery_unlocked_by": "source",
		}), nil
	}

	gate := recovery.EvaluateRetry(
		review,
		fingerprint,
		recovery.CurrentHarnessDigest(),
		recovery.CurrentWorkflowDigest(prior.Workflow),
		sourceNow(),
	)
	if !gate.Allowed {
		return reenqueueDecision{Block: true}, nil
	}

	meta := map[string]string{
		"recovery_class":          gate.RecoveryClass,
		"recovery_action":         gate.RecoveryAction,
		"recovery_unlocked_by":    gate.UnlockedBy,
		"remediation_fingerprint": gate.CurrentFingerprint,
		"failure_fingerprint":     gate.FailureFingerprint,
	}
	return newRetryDecision(q, prior, meta), nil
}

func newRetryDecision(q *queue.Queue, prior *queue.Vessel, meta map[string]string) reenqueueDecision {
	rootID := recovery.RetryRootID(*prior)
	copied := make(map[string]string, len(meta))
	for key, value := range meta {
		if strings.TrimSpace(value) == "" {
			continue
		}
		copied[key] = value
	}
	copied["retry_of"] = rootID
	if _, ok := copied["retry_count"]; !ok {
		copied["retry_count"] = strconv.Itoa(recovery.RetryCountFromVessel(*prior) + 1)
	}
	return reenqueueDecision{
		Parent: prior,
		NextID: recovery.RetryID(rootID, q),
		Meta:   copied,
	}
}

func applyRecoveryMeta(meta map[string]string, decision reenqueueDecision) {
	if len(decision.Meta) == 0 {
		return
	}
	for key, value := range decision.Meta {
		if strings.TrimSpace(value) == "" {
			continue
		}
		meta[key] = value
	}
}
