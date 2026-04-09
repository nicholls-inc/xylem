package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

const phaseStallTerminationGracePeriod = 30 * time.Second

type phaseExecutionContextKey struct{}

type PhaseExecutionMetadata struct {
	VesselID  string
	PhaseName string
}

type ProcessInfo struct {
	PID       int
	Phase     string
	StartedAt time.Time
	Live      bool
}

type ProcessTracker interface {
	ProcessInfo(vesselID string) (ProcessInfo, bool)
	TerminateProcess(vesselID string, gracePeriod time.Duration) error
}

type StallAlert struct {
	Code     string
	Severity string
	VesselID string
	Phase    string
	Message  string
}

func WithPhaseExecutionMetadata(ctx context.Context, meta PhaseExecutionMetadata) context.Context {
	return context.WithValue(ctx, phaseExecutionContextKey{}, meta)
}

func PhaseExecutionMetadataFromContext(ctx context.Context) (PhaseExecutionMetadata, bool) {
	meta, ok := ctx.Value(phaseExecutionContextKey{}).(PhaseExecutionMetadata)
	return meta, ok
}

func (r *Runner) CheckStalledVessels(ctx context.Context) []StallAlert {
	if r == nil || r.Config == nil || r.ProcessTracker == nil {
		return nil
	}

	threshold, err := time.ParseDuration(r.Config.Daemon.StallMonitor.PhaseStallThreshold)
	if err != nil {
		log.Printf("warn: parse phase stall threshold: %v", err)
		return nil
	}

	running, err := r.Queue.ListByState(queue.StateRunning)
	if err != nil {
		log.Printf("warn: list running vessels for stall check: %v", err)
		return nil
	}

	alerts := make([]StallAlert, 0)
	for _, vessel := range running {
		info, ok := r.ProcessTracker.ProcessInfo(vessel.ID)
		if !ok || !info.Live || info.PID <= 0 {
			if !r.Config.Daemon.StallMonitor.OrphanCheckEnabled {
				continue
			}
			errMsg := "vessel orphaned (no live subprocess)"
			log.Printf("warn: %s for vessel %s", errMsg, vessel.ID)
			if r.timeoutRunningVessel(ctx, vessel, errMsg) {
				alerts = append(alerts, StallAlert{
					Code:     "orphaned_subprocess",
					Severity: "critical",
					VesselID: vessel.ID,
					Message:  fmt.Sprintf("Vessel %s orphaned (no live subprocess)", vessel.ID),
				})
			}
			continue
		}

		activityAt, err := latestPhaseActivityAt(r.Config.StateDir, vessel.ID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				activityAt = info.StartedAt
			} else {
				log.Printf("warn: inspect phase activity for %s: %v", vessel.ID, err)
				continue
			}
		}
		if activityAt.IsZero() && vessel.StartedAt != nil {
			activityAt = *vessel.StartedAt
		}
		if activityAt.IsZero() {
			continue
		}

		staleFor := r.runtimeSince(activityAt)
		if staleFor <= threshold {
			continue
		}

		errMsg := fmt.Sprintf("phase stalled: no output for %s", staleFor.Round(time.Second))
		log.Printf("warn: %s for vessel %s phase=%q pid=%d", errMsg, vessel.ID, info.Phase, info.PID)
		if err := r.ProcessTracker.TerminateProcess(vessel.ID, phaseStallTerminationGracePeriod); err != nil {
			log.Printf("warn: terminate stalled vessel %s: %v", vessel.ID, err)
		}
		if r.timeoutRunningVessel(ctx, vessel, errMsg) {
			alerts = append(alerts, StallAlert{
				Code:     "phase_stalled",
				Severity: "critical",
				VesselID: vessel.ID,
				Phase:    info.Phase,
				Message:  fmt.Sprintf("Vessel %s phase-stalled (%s no output on %s)", vessel.ID, staleFor.Round(time.Second), info.Phase),
			})
		}
	}

	return alerts
}

func (r *Runner) timeoutRunningVessel(ctx context.Context, vessel queue.Vessel, errMsg string) bool {
	if updateErr := r.Queue.Update(vessel.ID, queue.StateTimedOut, errMsg); updateErr != nil {
		log.Printf("warn: failed to update vessel %s to timed_out: %v", vessel.ID, updateErr)
		return false
	}
	src := r.resolveSourceForVessel(vessel)
	if err := src.OnTimedOut(ctx, vessel); err != nil {
		log.Printf("warn: OnTimedOut hook for vessel %s: %v", vessel.ID, err)
	}
	r.removeWorktree(vessel.WorktreePath, vessel.ID)
	return true
}

func latestPhaseActivityAt(stateDir, vesselID string) (time.Time, error) {
	phaseDir := filepath.Join(stateDir, "phases", vesselID)
	var latest time.Time
	var found bool
	err := filepath.WalkDir(phaseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !found || info.ModTime().After(latest) {
			latest = info.ModTime()
			found = true
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	if !found {
		return time.Time{}, os.ErrNotExist
	}
	return latest, nil
}
