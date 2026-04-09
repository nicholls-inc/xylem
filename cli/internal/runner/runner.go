package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/gate"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/orchestrator"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/surface"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
	oteltrace "go.opentelemetry.io/otel/trace"
)

var errVesselCancelled = errors.New("vessel cancelled")

const vesselCancelPollInterval = 25 * time.Millisecond

// CommandRunner abstracts subprocess execution for testing.
type CommandRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
	RunProcess(ctx context.Context, dir string, name string, args ...string) error
	RunPhase(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error)
}

// WorktreeManager abstracts worktree lifecycle for testing.
type WorktreeManager interface {
	Create(ctx context.Context, branchName string) (string, error)
	Remove(ctx context.Context, worktreePath string) error
}

// DrainResult summarises a drain run.
type DrainResult struct {
	Launched  int
	Completed int
	Failed    int
	Skipped   int
	Waiting   int
}

// BuiltinWorkflowHandler runs a workflow without loading a YAML definition.
// It is used for internal workflows that execute Go code directly.
type BuiltinWorkflowHandler func(ctx context.Context, vessel queue.Vessel) error

// Runner launches Claude sessions for queued vessels with concurrency control.
type Runner struct {
	Config   *config.Config
	Queue    *queue.Queue
	Worktree WorktreeManager
	Runner   CommandRunner
	LiveGate gate.LiveGateRunner
	Sources  map[string]source.Source
	// BuiltinWorkflows handles workflow names that execute internal logic
	// instead of loading .xylem/workflows/<name>.yaml.
	BuiltinWorkflows map[string]BuiltinWorkflowHandler
	Reporter         *reporter.Reporter // may be nil for non-github vessels
	// Shared harness scaffolding for phase policy enforcement, audit logging,
	// protected-surface verification, and tracing.
	Intermediary *intermediary.Intermediary // nil = no policy enforcement
	AuditLog     *intermediary.AuditLog     // nil = no audit logging
	Tracer       *observability.Tracer      // nil = no tracing
	// DrainBudget bounds the wall time that Drain() spends dequeueing new
	// vessels. When the deadline elapses, Drain() stops dequeueing and
	// returns immediately while already-started goroutines continue in the
	// background. Call Wait or DrainAndWait if the caller needs terminal
	// outcomes for the in-flight vessels. Any pending vessels are picked up
	// by the next drain tick. Zero means unbounded (legacy behavior).
	//
	// Set this to drainInterval in the daemon so that Drain() returns
	// roughly once per tick even under sustained pool saturation. Without
	// this bound, a saturated tick holds the daemon's draining CAS guard
	// for the full vessel runtime, which prevents later ticks from using
	// newly available capacity and blocks idle-only work such as upgrades.
	DrainBudget time.Duration

	sem      chan struct{}
	wg       sync.WaitGroup
	traceWg  sync.WaitGroup
	inFlight atomic.Int32

	resultMu sync.Mutex
	result   DrainResult
}

// New creates a Runner.
func New(cfg *config.Config, q *queue.Queue, wt WorktreeManager, r CommandRunner) *Runner {
	concurrency := 1
	if cfg != nil && cfg.Concurrency > 0 {
		concurrency = cfg.Concurrency
	}
	return &Runner{
		Config:   cfg,
		Queue:    q,
		Worktree: wt,
		Runner:   r,
		LiveGate: gate.NewLiveVerifier(),
		sem:      make(chan struct{}, concurrency),
	}
}

// Drain dequeues pending vessels and launches sessions up to Config.Concurrency concurrently.
// On context cancellation, no new vessels are launched; running vessels complete normally.
// Drain returns once the current dequeue tick ends. Use DrainAndWait or Wait for
// synchronous callers that need terminal outcomes.
func (r *Runner) Drain(ctx context.Context) (DrainResult, error) {
	var drainSpan observability.SpanContext
	if r.Tracer != nil {
		drainSpan = r.Tracer.StartSpan(ctx, "drain_run", observability.DrainSpanAttributes(observability.DrainSpanData{
			Concurrency: r.Config.Concurrency,
			Timeout:     r.Config.Timeout,
		}))
		ctx = drainSpan.Context()
	}

	timeout, err := time.ParseDuration(r.Config.Timeout)
	if err != nil {
		if r.Tracer != nil {
			drainSpan.RecordError(err)
		}
		return DrainResult{}, fmt.Errorf("parse timeout: %w", err)
	}

	var result DrainResult
	healthCounts := FleetStatusReport{}
	patternCounts := map[string]int{}
	var drainStatsMu sync.Mutex
	var drainLaunchWg sync.WaitGroup

	// Drain budget: if set, Drain() stops dequeueing once the deadline
	// elapses. Already-started goroutines continue in the background.
	var drainDeadline time.Time
	if r.DrainBudget > 0 {
		drainDeadline = time.Now().Add(r.DrainBudget)
	}

drainLoop:
	for {
		select {
		case <-ctx.Done():
			break drainLoop
		default:
		}

		if !drainDeadline.IsZero() && time.Now().After(drainDeadline) {
			log.Printf("drain: budget %s elapsed, stopping dequeue (%d in-flight)", r.DrainBudget, r.InFlightCount())
			break drainLoop
		}

		select {
		case r.sem <- struct{}{}:
		default:
			if result.Launched == 0 && r.InFlightCount() > 0 {
				log.Printf("drain: concurrency saturated, ending tick with %d in-flight", r.InFlightCount())
			}
			break drainLoop
		}

		vessel, err := r.Queue.Dequeue()
		if err != nil || vessel == nil {
			<-r.sem
			break drainLoop
		}

		log.Printf("%sdequeued vessel workflow=%s", vesselLabel(*vessel), vessel.Workflow)

		result.Launched++
		r.recordLaunched()
		r.inFlight.Add(1)
		r.wg.Add(1)
		drainLaunchWg.Add(1)
		go func(j queue.Vessel) {
			defer r.wg.Done()
			defer drainLaunchWg.Done()
			defer func() {
				<-r.sem
				r.inFlight.Add(-1)
			}()

			vesselBaseCtx := context.Background()
			var vesselSpan observability.SpanContext
			if r.Tracer != nil {
				vesselSpan = r.Tracer.StartSpan(ctx, "vessel:"+j.ID, observability.VesselSpanAttributes(observability.VesselSpanData{
					ID:       j.ID,
					Source:   j.Source,
					Workflow: j.Workflow,
					Ref:      j.Ref,
				}))
				defer vesselSpan.End()
				vesselBaseCtx = oteltrace.ContextWithSpanContext(context.Background(), oteltrace.SpanContextFromContext(vesselSpan.Context()))
			}

			srcCfg := r.sourceConfigFromMeta(j)
			vesselTimeout, resolveErr := resolveTimeout(r.Config, srcCfg)
			if resolveErr != nil {
				log.Printf("warn: resolve timeout for vessel %s (config_source=%q): %v; using global timeout %s", j.ID, r.sourceConfigNameFromMeta(j), resolveErr, timeout)
				vesselTimeout = timeout // fallback to global
			}

			watchedCtx, stopWatching := r.watchVesselCancellation(vesselBaseCtx, j.ID)
			defer stopWatching(nil)
			vesselCtx, cancel := context.WithTimeout(watchedCtx, vesselTimeout)
			defer cancel()

			outcome := r.runVessel(vesselCtx, j)
			finalVessel := j
			if current, findErr := r.Queue.FindByID(j.ID); findErr != nil {
				log.Printf("warn: inspect vessel %s after run: %v", j.ID, findErr)
			} else if current != nil {
				finalVessel = *current
			}
			status := r.inspectVesselStatus(finalVessel)
			if r.Tracer != nil {
				vesselSpan.AddAttributes(observability.VesselHealthAttributes(observability.VesselHealthData{
					State:        string(finalVessel.State),
					Health:       string(status.Health),
					AnomalyCount: len(status.Anomalies),
					Anomalies:    AnomalyCodes(status.Anomalies),
				}))
			}

			drainStatsMu.Lock()
			switch status.Health {
			case VesselHealthHealthy:
				healthCounts.Healthy++
			case VesselHealthDegraded:
				healthCounts.Degraded++
			case VesselHealthUnhealthy:
				healthCounts.Unhealthy++
			}
			for _, anomaly := range status.Anomalies {
				patternCounts[anomaly.Code]++
			}
			drainStatsMu.Unlock()
			r.recordOutcome(outcome)
		}(*vessel)
	}

	if r.Tracer != nil {
		if result.Launched == 0 {
			drainSpan.End()
		} else {
			r.traceWg.Add(1)
			go func() {
				defer r.traceWg.Done()
				drainLaunchWg.Wait()
				drainStatsMu.Lock()
				patterns := make([]FleetPattern, 0, len(patternCounts))
				for code, count := range patternCounts {
					patterns = append(patterns, FleetPattern{Code: code, Count: count})
				}
				drainStatsMu.Unlock()
				sort.Slice(patterns, func(i, j int) bool {
					if patterns[i].Count == patterns[j].Count {
						return patterns[i].Code < patterns[j].Code
					}
					return patterns[i].Count > patterns[j].Count
				})
				drainSpan.AddAttributes(observability.DrainHealthAttributes(observability.DrainHealthData{
					Healthy:   healthCounts.Healthy,
					Degraded:  healthCounts.Degraded,
					Unhealthy: healthCounts.Unhealthy,
					Patterns:  FormatFleetPatterns(patterns),
				}))
				drainSpan.End()
			}()
		}
	}

	return result, nil
}

// DrainAndWait preserves the historical synchronous Drain behaviour for callers
// that need a terminal outcome summary rather than a per-tick launch count.
func (r *Runner) DrainAndWait(ctx context.Context) (DrainResult, error) {
	before := r.SnapshotResults()
	var launched int
	for {
		tickResult, err := r.Drain(ctx)
		if err != nil {
			return DrainResult{}, err
		}
		launched += tickResult.Launched
		r.Wait()
		if r.DrainBudget > 0 || tickResult.Launched == 0 || ctx.Err() != nil {
			break
		}
		pending, pendingErr := r.Queue.ListByState(queue.StatePending)
		if pendingErr != nil {
			return DrainResult{}, fmt.Errorf("list pending vessels: %w", pendingErr)
		}
		if len(pending) == 0 {
			break
		}
	}
	after := r.SnapshotResults()
	result := DrainResult{
		Launched:  launched,
		Completed: after.Completed - before.Completed,
		Failed:    after.Failed - before.Failed,
		Skipped:   after.Skipped - before.Skipped,
		Waiting:   after.Waiting - before.Waiting,
	}
	return result, nil
}

// Wait blocks until all in-flight vessels have finished and returns the
// cumulative outcome counts recorded by this Runner.
func (r *Runner) Wait() DrainResult {
	r.wg.Wait()
	r.traceWg.Wait()
	return r.SnapshotResults()
}

// InFlightCount reports the number of launched vessels that have not yet
// reached a terminal outcome.
func (r *Runner) InFlightCount() int {
	return int(r.inFlight.Load())
}

// SnapshotResults returns the cumulative outcome counts recorded by this Runner.
func (r *Runner) SnapshotResults() DrainResult {
	r.resultMu.Lock()
	defer r.resultMu.Unlock()
	return r.result
}

func (r *Runner) recordLaunched() {
	r.resultMu.Lock()
	defer r.resultMu.Unlock()
	r.result.Launched++
}

func (r *Runner) recordOutcome(outcome string) {
	r.resultMu.Lock()
	defer r.resultMu.Unlock()
	switch outcome {
	case "completed":
		r.result.Completed++
	case "failed":
		r.result.Failed++
	case "waiting":
		r.result.Waiting++
	case "cancelled":
		r.result.Skipped++
	default:
		r.result.Skipped++
	}
}

// CheckWaitingVessels checks all waiting vessels for label gate resolution.
func (r *Runner) CheckWaitingVessels(ctx context.Context) {
	waiting, err := r.Queue.ListByState(queue.StateWaiting)
	if err != nil {
		log.Printf("warn: list waiting vessels: %v", err)
		return
	}

	for _, vessel := range waiting {
		// Check timeout
		if vessel.WaitingSince != nil {
			timeoutDur := 24 * time.Hour // default
			if vessel.Workflow != "" {
				if s, loadErr := r.loadWorkflow(vessel.Workflow); loadErr == nil {
					if int(vessel.CurrentPhase) > 0 && int(vessel.CurrentPhase) <= len(s.Phases) {
						prevPhase := s.Phases[vessel.CurrentPhase-1]
						if prevPhase.Gate != nil && prevPhase.Gate.Timeout != "" {
							if parsed, pErr := time.ParseDuration(prevPhase.Gate.Timeout); pErr == nil {
								timeoutDur = parsed
							}
						}
					}
				}
			}
			waited := r.runtimeSince(*vessel.WaitingSince)
			if waited > timeoutDur {
				timeoutSpan := r.startWaitTransitionSpan(ctx, vessel, "timed_out", waited)
				if updateErr := r.Queue.Update(vessel.ID, queue.StateTimedOut, "label gate timed out"); updateErr != nil {
					r.finishWaitTransitionSpan(timeoutSpan, updateErr)
					log.Printf("warn: failed to update vessel %s to timed_out: %v", vessel.ID, updateErr)
					continue
				}
				src := r.resolveSourceForVessel(vessel)
				if err := src.OnTimedOut(ctx, vessel); err != nil {
					log.Printf("warn: OnTimedOut hook for vessel %s: %v", vessel.ID, err)
				}
				vrs := newVesselRunState(r.Config, vessel, r.runtimeNow())
				vrs.setTraceContext(observability.TraceContextFromContext(timeoutSpan.Context()))
				r.persistRunArtifacts(vessel, string(queue.StateTimedOut), vrs, nil, r.runtimeNow())
				r.finishWaitTransitionSpan(timeoutSpan, nil)
				// Clean up worktree (best-effort)
				r.removeWorktree(ctx, vessel.WorktreePath, vessel.ID)
				// Post timeout comment
				issueNum := r.parseIssueNum(vessel)
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post label-timeout comment", vessel.ID,
						r.Reporter.LabelTimeout(ctx, issueNum, vessel.WaitingFor, vessel.FailedPhase, waited))
				}
				continue
			}
		}

		// Check label
		issueNum := r.parseIssueNum(vessel)
		repo := r.resolveRepo(vessel)
		src := r.resolveSourceForVessel(vessel)
		if issueNum == 0 || repo == "" {
			continue
		}

		found, err := gate.CheckLabel(ctx, r.Runner, repo, issueNum, vessel.WaitingFor)
		if err != nil {
			log.Printf("warn: check label for vessel %s: %v", vessel.ID, err)
			continue
		}
		if found {
			resumeSpan := r.startWaitTransitionSpan(ctx, vessel, "resumed", waitedDuration(vessel.WaitingSince, r.runtimeNow()))
			// Advance past the gated phase — CurrentPhase was already incremented
			// when the vessel entered waiting state. Resume via pending so Drain can
			// pick the vessel back up through the normal dequeue flow.
			if err := r.Queue.Update(vessel.ID, queue.StatePending, ""); err != nil {
				r.finishWaitTransitionSpan(resumeSpan, err)
				log.Printf("warn: failed to resume vessel %s: %v", vessel.ID, err)
				continue
			}
			if err := src.OnResume(ctx, vessel); err != nil {
				log.Printf("warn: OnResume hook for vessel %s: %v", vessel.ID, err)
			}
			r.finishWaitTransitionSpan(resumeSpan, nil)
		}
	}
}

func (r *Runner) runVessel(ctx context.Context, vessel queue.Vessel) (outcome string) {
	// Look up source for this vessel
	src := r.resolveSourceForVessel(vessel)

	// Source-specific start hook (e.g., add in-progress label)
	startErr := src.OnStart(ctx, vessel)
	if startErr != nil {
		log.Printf("warn: source OnStart for %s: %v", vessel.ID, startErr)
	}
	// Unconditionally remove the running label when the vessel exits the
	// running state, regardless of outcome (completed, failed, waiting, etc.).
	// Uses context.Background because the vessel context may already be
	// cancelled by the time this fires.
	defer func() {
		if startErr != nil {
			return
		}
		if err := src.RemoveRunningLabel(context.Background(), vessel); err != nil {
			log.Printf("warn: RemoveRunningLabel for %s: %v", vessel.ID, err)
		}
	}()

	vrs := newVesselRunState(r.Config, vessel, r.runtimeNow())
	vrs.setTraceContext(observability.TraceContextFromContext(ctx))
	var claims []evidence.Claim
	defer func() {
		if outcome != "failed" {
			return
		}
		r.persistRunArtifacts(vessel, string(queue.StateFailed), vrs, claims, r.runtimeNow())
	}()

	// Prompt-only vessel (no workflow): single claude -p invocation
	if vessel.Workflow == "" && vessel.Prompt != "" {
		worktreePath, ok := r.ensureWorktree(ctx, &vessel, src)
		if !ok {
			return "failed"
		}
		return r.runPromptOnly(ctx, vessel, worktreePath, src, vrs)
	}

	// Load workflow definition
	if vessel.Workflow == "" {
		r.failVessel(vessel.ID, "vessel has neither workflow nor prompt")
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		return "failed"
	}

	if builtin, ok := r.BuiltinWorkflows[vessel.Workflow]; ok {
		return r.runBuiltinWorkflow(ctx, vessel, src, vrs, builtin)
	}

	worktreePath, ok := r.ensureWorktree(ctx, &vessel, src)
	if !ok {
		return "failed"
	}

	sk, err := r.loadWorkflow(vessel.Workflow)
	if err != nil {
		r.failVessel(vessel.ID, fmt.Sprintf("load workflow: %v", err))
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		return "failed"
	}

	// Fetch issue data (GitHub source only)
	issueData := r.fetchIssueData(ctx, &vessel)

	// Validate issue data when the workflow has command phases that reference
	// issue/PR template variables. Without valid issue data, templates like
	// {{.Issue.Number}} render to "0" causing confusing downstream failures.
	if err := validateIssueDataForWorkflow(vessel, issueData, sk); err != nil {
		r.failVessel(vessel.ID, err.Error())
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		return "failed"
	}

	// Read harness file
	harnessContent := r.readHarness()

	// Orchestrator-driven execution for workflows with explicit phase dependencies.
	// This enables parallel phase execution within waves and context firewalls.
	if sk.HasDependencies() {
		return r.runVesselOrchestrated(ctx, vessel, sk, issueData, harnessContent, worktreePath, src, vrs, &claims)
	}

	// Rebuild previousOutputs from .xylem/phases/<id>/*.output (for resume)
	previousOutputs := r.rebuildPreviousOutputs(vessel.ID, sk)
	srcCfg := r.sourceConfigFromMeta(vessel)

	// Execute phases sequentially (no explicit dependencies)
	var phaseResults []reporter.PhaseResult
	for i := vessel.CurrentPhase; i < len(sk.Phases); i++ {
		if r.vesselCancelled(ctx, vessel.ID) {
			return r.cancelVessel(vessel, worktreePath, vrs, claims)
		}
		p := sk.Phases[i]
		gateResult := ""

		// Initialize gate retries for this phase (once, before retry loop)
		if p.Gate != nil && (p.Gate.Type == "command" || p.Gate.Type == "live") && p.Gate.Retries > 0 && vessel.GateRetries == 0 {
			vessel.GateRetries = p.Gate.Retries
		}

		// Gate retry loop: may re-run the same phase with gate output appended
		for {
			if r.vesselCancelled(ctx, vessel.ID) {
				return r.cancelVessel(vessel, worktreePath, vrs, claims)
			}
			log.Printf("%sphase %q starting (%d/%d)", vesselLabel(vessel), p.Name, i+1, len(sk.Phases))
			phaseStart := r.runtimeNow()

			// Build template data
			td := phase.TemplateData{
				Issue: issueData,
				Phase: phase.PhaseData{
					Name:  p.Name,
					Index: i,
				},
				PreviousOutputs: previousOutputs,
				GateResult:      gateResult,
				Vessel: phase.VesselData{
					ID:     vessel.ID,
					Source: vessel.Source,
				},
			}

			var output []byte
			var runErr error
			var beforeSnapshot surface.Snapshot
			var checkProtectedSurfaces bool
			var promptForCost string
			provider := resolveProvider(r.Config, srcCfg, sk, &p)
			model := resolveModel(r.Config, srcCfg, sk, &p, provider)
			retryAttempt := 0
			if p.Gate != nil && (p.Gate.Type == "command" || p.Gate.Type == "live") {
				retryAttempt = providerAttempt(&p, vessel.GateRetries)
			}
			phaseSpan := startPhaseSpan(r.Tracer, ctx, r.Config, srcCfg, sk, p, i, retryAttempt)
			phaseSpanEnded := false
			var phaseDuration time.Duration
			phaseSpanStatus := "running"
			phaseOutputArtifactPath := ""
			finishCurrentPhaseSpan := func(err error) {
				if phaseSpanEnded {
					return
				}
				if err != nil && phaseSpanStatus == "running" {
					phaseSpanStatus = "failed"
				}
				if phaseDuration == 0 {
					phaseDuration = r.runtimeSince(phaseStart)
				}
				finishPhaseSpan(r.Tracer, phaseSpan, buildPhaseResultData(r.Config, srcCfg, sk, p, promptForCost, string(output), phaseDuration, phaseSpanStatus, phaseOutputArtifactPath), err)
				phaseSpanEnded = true
			}

			// Create phases dir early
			phasesDir := filepath.Join(r.Config.StateDir, "phases", vessel.ID)
			if err := os.MkdirAll(phasesDir, 0o755); err != nil {
				finishCurrentPhaseSpan(err)
				r.failVessel(vessel.ID, fmt.Sprintf("create phases dir: %v", err))
				if failErr := src.OnFail(ctx, vessel); failErr != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
				}
				return "failed"
			}

			if p.Type == "command" {
				// Command phase: render and execute shell command
				rendered, err := renderCommandTemplate(p.Name, "command", p.Run, td)
				if err != nil {
					finishCurrentPhaseSpan(err)
					r.failVessel(vessel.ID, err.Error())
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					return "failed"
				}
				if wErr := os.WriteFile(filepath.Join(phasesDir, p.Name+".command"), []byte(rendered), 0o644); wErr != nil {
					log.Printf("warn: write command file: %v", wErr)
				}
				if policyErr := r.enforcePhasePolicy(vessel, p, rendered, ""); policyErr != nil {
					finishCurrentPhaseSpan(policyErr)
					log.Printf("%sphase %q blocked: %v", vesselLabel(vessel), p.Name, policyErr)
					vessel.FailedPhase = p.Name
					r.failUpdatedVessel(&vessel, policyErr.Error())
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					issueNum := r.parseIssueNum(vessel)
					if issueNum > 0 && r.Reporter != nil {
						r.logReporterError("post vessel-failed comment", vessel.ID,
							r.Reporter.VesselFailed(ctx, issueNum, p.Name, policyErr.Error(), ""))
					}
					return "failed"
				}
				beforeSnapshot, checkProtectedSurfaces, err = r.takeProtectedSurfaceSnapshot(ctx, worktreePath)
				if err != nil {
					snapErr := fmt.Errorf("protected surface snapshot failed: %w", err)
					finishCurrentPhaseSpan(snapErr)
					r.failVessel(vessel.ID, fmt.Sprintf("phase %s: %v", p.Name, snapErr))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					issueNum := r.parseIssueNum(vessel)
					if issueNum > 0 && r.Reporter != nil {
						r.logReporterError("post vessel-failed comment", vessel.ID,
							r.Reporter.VesselFailed(ctx, issueNum, p.Name, snapErr.Error(), ""))
					}
					return "failed"
				}
				cmdOut, cmdErr := gate.RunCommand(ctx, r.Runner, worktreePath, rendered)
				output = []byte(cmdOut)
				runErr = cmdErr
			} else {
				// LLM phase: existing code
				promptContent, err := os.ReadFile(p.PromptFile)
				if err != nil {
					finishCurrentPhaseSpan(err)
					r.failVessel(vessel.ID, fmt.Sprintf("read prompt file %s: %v", p.PromptFile, err))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					return "failed"
				}
				promptTemplate := string(promptContent)
				rendered, err := phase.RenderPrompt(promptTemplate, td)
				if err != nil {
					finishCurrentPhaseSpan(err)
					r.failVessel(vessel.ID, fmt.Sprintf("render prompt for phase %s: %v", p.Name, err))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					return "failed"
				}
				promptForCost = rendered
				if harnessContent != "" {
					promptForCost = harnessContent + "\n\n" + rendered
				}
				promptPath := filepath.Join(phasesDir, p.Name+".prompt")
				if wErr := os.WriteFile(promptPath, []byte(rendered), 0o644); wErr != nil {
					log.Printf("warn: write prompt file %s: %v", promptPath, wErr)
				}
				if policyErr := r.enforcePhasePolicy(vessel, p, "", promptTemplate); policyErr != nil {
					finishCurrentPhaseSpan(policyErr)
					log.Printf("%sphase %q blocked: %v", vesselLabel(vessel), p.Name, policyErr)
					vessel.FailedPhase = p.Name
					r.failUpdatedVessel(&vessel, policyErr.Error())
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					issueNum := r.parseIssueNum(vessel)
					if issueNum > 0 && r.Reporter != nil {
						r.logReporterError("post vessel-failed comment", vessel.ID,
							r.Reporter.VesselFailed(ctx, issueNum, p.Name, policyErr.Error(), ""))
					}
					return "failed"
				}
				beforeSnapshot, checkProtectedSurfaces, err = r.takeProtectedSurfaceSnapshot(ctx, worktreePath)
				if err != nil {
					snapErr := fmt.Errorf("protected surface snapshot failed: %w", err)
					finishCurrentPhaseSpan(snapErr)
					r.failVessel(vessel.ID, fmt.Sprintf("phase %s: %v", p.Name, snapErr))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					issueNum := r.parseIssueNum(vessel)
					if issueNum > 0 && r.Reporter != nil {
						r.logReporterError("post vessel-failed comment", vessel.ID,
							r.Reporter.VesselFailed(ctx, issueNum, p.Name, snapErr.Error(), ""))
					}
					return "failed"
				}
				attempt := providerAttempt(&p, vessel.GateRetries)
				cmd, args, phaseStdin := buildProviderPhaseArgs(r.Config, srcCfg, sk, &p, harnessContent, provider, rendered, attempt)
				var stdinContent string
				if phaseStdin != nil {
					stdinContent = rendered
				}
				output, runErr = r.runPhaseWithRateLimitRetry(ctx, worktreePath, stdinContent, cmd, args)
			}

			if r.vesselCancelled(ctx, vessel.ID) {
				finishCurrentPhaseSpan(context.Canceled)
				return r.cancelVessel(vessel, worktreePath, vrs, claims)
			}

			// Shared: Write phase output
			outputPath := filepath.Join(phasesDir, p.Name+".output")
			phaseOutputArtifactPath = phaseArtifactRelativePath(vessel.ID, p.Name)
			if wErr := os.WriteFile(outputPath, output, 0o644); wErr != nil {
				log.Printf("warn: write output file %s: %v", outputPath, wErr)
			}
			fmt.Printf("Phase %s complete: %s\n", p.Name, outputPath)
			phaseDuration = r.runtimeSince(phaseStart)

			if runErr != nil {
				phaseSpanStatus = "failed"
				finishCurrentPhaseSpan(runErr)
				log.Printf("%sphase %q failed: %v", vesselLabel(vessel), p.Name, runErr)
				vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, 0, 0, 0.0, phaseDuration, "failed", nil, runErr.Error()))
				vessel.FailedPhase = p.Name
				r.failUpdatedVessel(&vessel, fmt.Sprintf("phase %s: %v", p.Name, runErr))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				issueNum := r.parseIssueNum(vessel)
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post vessel-failed comment", vessel.ID,
						r.Reporter.VesselFailed(ctx, issueNum, p.Name, runErr.Error(), ""))
				}
				return "failed"
			}

			if checkProtectedSurfaces {
				if err := r.verifyProtectedSurfaces(vessel, p, worktreePath, beforeSnapshot); err != nil {
					phaseSpanStatus = "failed"
					finishCurrentPhaseSpan(err)
					log.Printf("%sphase %q violated protected surfaces: %v", vesselLabel(vessel), p.Name, err)
					vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, 0, 0, 0.0, phaseDuration, "failed", nil, err.Error()))
					vessel.FailedPhase = p.Name
					r.failUpdatedVessel(&vessel, err.Error())
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					issueNum := r.parseIssueNum(vessel)
					if issueNum > 0 && r.Reporter != nil {
						r.logReporterError("post vessel-failed comment", vessel.ID,
							r.Reporter.VesselFailed(ctx, issueNum, p.Name, err.Error(), ""))
					}
					return "failed"
				}
			}

			recordedAt := r.runtimeNow()
			inputTokensEst, outputTokensEst, costUSDEst := vrs.recordPhaseTokens(p, model, promptForCost, string(output), recordedAt)
			if vrs.costTracker != nil && vrs.costTracker.BudgetExceeded() {
				errMsg := fmt.Sprintf("budget exceeded after phase %q: estimated cost $%.4f, estimated tokens %d",
					p.Name, vrs.costTracker.TotalCost(), vrs.costTracker.TotalTokens())
				vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", nil, errMsg))
				vessel.FailedPhase = p.Name
				r.failUpdatedVessel(&vessel, errMsg)
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				issueNum := r.parseIssueNum(vessel)
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post vessel-failed comment", vessel.ID,
						r.Reporter.VesselFailed(ctx, issueNum, p.Name, errMsg, ""))
				}
				phaseSpanStatus = "failed"
				finishCurrentPhaseSpan(fmt.Errorf("%s", errMsg))
				return "failed"
			}

			// Store output for subsequent phases
			previousOutputs[p.Name] = string(output)

			// Update current phase, persist
			vessel.CurrentPhase = i + 1
			if vessel.PhaseOutputs == nil {
				vessel.PhaseOutputs = make(map[string]string)
			}
			vessel.PhaseOutputs[p.Name] = outputPath
			if updateErr := r.Queue.UpdateVessel(vessel); updateErr != nil {
				if r.cancelledTransition(vessel.ID, updateErr) {
					finishCurrentPhaseSpan(context.Canceled)
					return r.cancelVessel(vessel, worktreePath, vrs, claims)
				}
				log.Printf("warn: persist phase progress for %s: %v", vessel.ID, updateErr)
			}

			log.Printf("%sphase %q completed (%s)", vesselLabel(vessel), p.Name, phaseDuration.Truncate(time.Second))

			issueNum := r.parseIssueNum(vessel)

			phaseStatus := "completed"
			if phaseMatchedNoOp(&p, string(output)) {
				phaseStatus = "no-op"
			}
			phaseSpanStatus = phaseStatus

			// Report phase completion (non-fatal)
			phaseResults = append(phaseResults, reporter.PhaseResult{Name: p.Name, Duration: phaseDuration, Status: phaseStatus})
			if issueNum > 0 && r.Reporter != nil {
				r.logReporterError("post phase-complete comment", vessel.ID,
					r.Reporter.PhaseComplete(ctx, issueNum, p.Name, phaseDuration, string(output)))
			}

			if phaseStatus == "no-op" {
				vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, phaseStatus, nil, ""))
				log.Printf("%sphase %q triggered no-op; completing workflow early", vesselLabel(vessel), p.Name)
				finishCurrentPhaseSpan(nil)
				if r.vesselCancelled(ctx, vessel.ID) {
					return r.cancelVessel(vessel, worktreePath, vrs, claims)
				}
				return r.completeVessel(ctx, vessel, worktreePath, phaseResults, vrs, claims)
			}

			// Handle gate
			if p.Gate == nil {
				vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, phaseStatus, nil, ""))
				finishCurrentPhaseSpan(nil)
				break // no gate, proceed to next phase
			}

			switch p.Gate.Type {
			case "command", "live":
				gateResultExec := r.executeVerificationGate(ctx, phaseSpan, vessel, p, td, worktreePath, retryAttempt)
				if r.vesselCancelled(ctx, vessel.ID) {
					finishCurrentPhaseSpan(context.Canceled)
					return r.cancelVessel(vessel, worktreePath, vrs, claims)
				}
				gateOut, passed, gateErr := gateResultExec.output, gateResultExec.passed, gateResultExec.err
				if gateErr != nil {
					phaseSpanStatus = "failed"
					vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), gateErr.Error()))
					finishCurrentPhaseSpan(nil)
					r.failVessel(vessel.ID, fmt.Sprintf("phase %s gate error: %v", p.Name, gateErr))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					return "failed"
				}
				if passed {
					log.Printf("%sgate passed for phase %q", vesselLabel(vessel), p.Name)
					phaseSpanStatus = phaseStatus
					vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, phaseStatus, gatePassedPointer(true), ""))
					if gateResultExec.evidenceClaim != nil {
						claims = append(claims, *gateResultExec.evidenceClaim)
					}
					finishCurrentPhaseSpan(nil)
					break // gate passed, proceed to next phase
				}

				// Gate failed
				retryDelay := 10 * time.Second
				if p.Gate.RetryDelay != "" {
					if parsed, pErr := time.ParseDuration(p.Gate.RetryDelay); pErr == nil {
						retryDelay = parsed
					}
				}

				if vessel.GateRetries <= 0 {
					log.Printf("%sgate failed for phase %q, retries exhausted", vesselLabel(vessel), p.Name)
					phaseSpanStatus = "failed"
					vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), "gate failed, retries exhausted"))
					finishCurrentPhaseSpan(nil)
					vessel.FailedPhase = p.Name
					vessel.GateOutput = gateOut
					r.failUpdatedVessel(&vessel, fmt.Sprintf("phase %s: gate failed, retries exhausted", p.Name))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					if issueNum > 0 && r.Reporter != nil {
						r.logReporterError("post vessel-failed comment", vessel.ID,
							r.Reporter.VesselFailed(ctx, issueNum, p.Name, "gate failed, retries exhausted", gateOut))
					}
					return "failed"
				}

				vessel.GateRetries--
				log.Printf("%sgate failed for phase %q, retries remaining=%d", vesselLabel(vessel), p.Name, vessel.GateRetries)
				if updateErr := r.Queue.UpdateVessel(vessel); updateErr != nil {
					if r.cancelledTransition(vessel.ID, updateErr) {
						finishCurrentPhaseSpan(context.Canceled)
						return r.cancelVessel(vessel, worktreePath, vrs, claims)
					}
					log.Printf("warn: persist gate retries for %s: %v", vessel.ID, updateErr)
				}

				// Re-render prompt with gate output context
				gateResult = fmt.Sprintf("The following gate check failed after the previous phase. Fix the issues and try again:\n\n%s", gateOut)

				if err := r.runtimeSleep(ctx, retryDelay); err != nil {
					if r.vesselCancelled(ctx, vessel.ID) {
						finishCurrentPhaseSpan(context.Canceled)
						return r.cancelVessel(vessel, worktreePath, vrs, claims)
					}
					vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), err.Error()))
					phaseSpanStatus = "failed"
					finishCurrentPhaseSpan(err)
					r.failVessel(vessel.ID, fmt.Sprintf("phase %s gate retry interrupted: %v", p.Name, err))
					if failErr := src.OnFail(ctx, vessel); failErr != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
					}
					return "failed"
				}
				phaseSpanStatus = "retrying"
				finishCurrentPhaseSpan(nil)
				continue // re-run same phase

			case "label":
				gateSpan := startGateSpan(r.Tracer, phaseSpan, ctx, p.Gate.Type)
				finishGateSpan(r.Tracer, gateSpan, observability.GateSpanData{
					Type:         p.Gate.Type,
					Passed:       false,
					RetryAttempt: retryAttempt,
				}, nil)
				log.Printf("%swaiting for label %q after phase %q", vesselLabel(vessel), p.Gate.WaitFor, p.Name)
				// Set vessel to waiting state
				vessel.FailedPhase = p.Name
				vessel.WaitingFor = p.Gate.WaitFor
				now := r.runtimeNow()
				vessel.WaitingSince = &now
				vessel.CurrentPhase = i + 1
				vessel.State = queue.StateWaiting
				if updateErr := r.Queue.UpdateVessel(vessel); updateErr != nil {
					if r.cancelledTransition(vessel.ID, updateErr) {
						finishCurrentPhaseSpan(context.Canceled)
						return r.cancelVessel(vessel, worktreePath, vrs, claims)
					}
					log.Printf("warn: persist waiting state for %s: %v", vessel.ID, updateErr)
					finishCurrentPhaseSpan(updateErr)
					return "failed"
				}
				if err := src.OnWait(ctx, vessel); err != nil {
					log.Printf("warn: OnWait hook for vessel %s: %v", vessel.ID, err)
				}
				waitSpan := r.startWaitTransitionSpan(ctx, vessel, "waiting", 0)
				r.finishWaitTransitionSpan(waitSpan, nil)
				phaseSpanStatus = "waiting"
				finishCurrentPhaseSpan(nil)
				return "waiting"
			}

			finishCurrentPhaseSpan(nil)
			break // gate passed or unknown gate type
		}

		// Reset gate retries so the next phase's initialization guard fires correctly.
		// This is only reached after the inner loop exits via break (gate passed or no gate),
		// never after a retry continue or early return.
		//
		// Also persist the reset when needed: the UpdateVessel inside the inner loop may have
		// written a non-zero GateRetries from this phase (e.g. retries:2 that passed on the
		// first attempt). Without this write, a daemon restart during the next phase's
		// RunPhase execution would load the stale non-zero count and give that phase
		// phantom retries. If GateRetries is already zero, skip the write to avoid an
		// unnecessary full queue rewrite under lock.
		prevRetries := vessel.GateRetries
		vessel.GateRetries = 0
		if prevRetries != 0 {
			if updateErr := r.Queue.UpdateVessel(vessel); updateErr != nil {
				if r.cancelledTransition(vessel.ID, updateErr) {
					return r.cancelVessel(vessel, worktreePath, vrs, claims)
				}
				log.Printf("warn: persist gate retry reset for %s: %v", vessel.ID, updateErr)
			}
		}
	}

	// All phases complete
	log.Printf("%scompleted all phases", vesselLabel(vessel))
	if r.vesselCancelled(ctx, vessel.ID) {
		return r.cancelVessel(vessel, worktreePath, vrs, claims)
	}
	if err := src.OnComplete(ctx, vessel); err != nil {
		log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
	}
	return r.completeVessel(ctx, vessel, worktreePath, phaseResults, vrs, claims)
}

// runPromptOnly handles vessels with a prompt but no workflow.
func (r *Runner) runPromptOnly(ctx context.Context, vessel queue.Vessel, worktreePath string, src source.Source, vrs *vesselRunState) string {
	prompt := vessel.Prompt
	if vessel.Ref != "" {
		prompt = fmt.Sprintf("Ref: %s\n\n%s", vessel.Ref, vessel.Prompt)
	}
	if r.vesselCancelled(ctx, vessel.ID) {
		return r.cancelVessel(vessel, worktreePath, vrs, nil)
	}

	cmd, args := buildPromptOnlyCmdArgs(r.Config, prompt)
	provider := resolveProvider(r.Config, nil, nil, nil)
	model := resolveModel(r.Config, nil, nil, nil, provider)
	beforeSnapshot, checkProtectedSurfaces, err := r.takeProtectedSurfaceSnapshot(ctx, worktreePath)
	if err != nil {
		snapErr := fmt.Errorf("protected surface snapshot failed: %w", err)
		r.failVessel(vessel.ID, snapErr.Error())
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		return "failed"
	}

	output, runErr := r.runPhaseWithRateLimitRetry(ctx, worktreePath, prompt, cmd, args)
	if r.vesselCancelled(ctx, vessel.ID) {
		return r.cancelVessel(vessel, worktreePath, vrs, nil)
	}
	if runErr != nil {
		r.failVessel(vessel.ID, runErr.Error())
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		return "failed"
	}
	if checkProtectedSurfaces {
		if err := r.verifyProtectedSurfaces(vessel, workflow.Phase{Name: "prompt-only"}, worktreePath, beforeSnapshot); err != nil {
			r.failVessel(vessel.ID, err.Error())
			if err := src.OnFail(ctx, vessel); err != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
			}
			return "failed"
		}
	}
	recordedAt := r.runtimeNow()
	if vrs != nil {
		vrs.recordPromptOnlyUsage(model, prompt, string(output), recordedAt)
		if vrs.costTracker != nil && vrs.costTracker.BudgetExceeded() {
			errMsg := fmt.Sprintf("budget exceeded: estimated cost $%.4f, estimated tokens %d",
				vrs.costTracker.TotalCost(), vrs.costTracker.TotalTokens())
			r.failVessel(vessel.ID, errMsg)
			if err := src.OnFail(ctx, vessel); err != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
			}
			return "failed"
		}
	}

	if r.vesselCancelled(ctx, vessel.ID) {
		return r.cancelVessel(vessel, worktreePath, vrs, nil)
	}
	if err := src.OnComplete(ctx, vessel); err != nil {
		log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
	}

	return r.completeVessel(ctx, vessel, worktreePath, nil, vrs, nil)
}

func (r *Runner) runBuiltinWorkflow(ctx context.Context, vessel queue.Vessel, src source.Source, vrs *vesselRunState, handler BuiltinWorkflowHandler) string {
	startedAt := r.runtimeNow()
	if err := handler(ctx, vessel); err != nil {
		if r.vesselCancelled(ctx, vessel.ID) {
			return r.cancelVessel(vessel, "", vrs, nil)
		}
		duration := r.runtimeSince(startedAt)
		if vrs != nil {
			vrs.addPhase(PhaseSummary{
				Name:       vessel.Workflow,
				Type:       "builtin",
				DurationMS: duration.Milliseconds(),
				Status:     "failed",
				Error:      err.Error(),
			})
		}
		r.failVessel(vessel.ID, err.Error())
		if failErr := src.OnFail(ctx, vessel); failErr != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
		}
		return "failed"
	}

	if r.vesselCancelled(ctx, vessel.ID) {
		return r.cancelVessel(vessel, "", vrs, nil)
	}
	if vrs != nil {
		vrs.addPhase(PhaseSummary{
			Name:       vessel.Workflow,
			Type:       "builtin",
			DurationMS: r.runtimeSince(startedAt).Milliseconds(),
			Status:     "completed",
		})
	}
	if r.vesselCancelled(ctx, vessel.ID) {
		return r.cancelVessel(vessel, "", vrs, nil)
	}
	if err := src.OnComplete(ctx, vessel); err != nil {
		log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
	}
	return r.completeVessel(ctx, vessel, "", nil, vrs, nil)
}

func (r *Runner) ensureWorktree(ctx context.Context, vessel *queue.Vessel, src source.Source) (string, bool) {
	// Worktree: reuse if set (resuming from waiting), otherwise create.
	// If set but missing on disk (e.g., retry after cleanup), recreate it.
	worktreePath := vessel.WorktreePath
	if worktreePath != "" {
		if _, statErr := os.Stat(worktreePath); os.IsNotExist(statErr) {
			log.Printf("vessel %s: worktree %s missing on disk, recreating", vessel.ID, worktreePath)
			branchName := src.BranchName(*vessel)
			recreated, recreateErr := r.Worktree.Create(ctx, branchName)
			if recreateErr != nil {
				r.failVessel(vessel.ID, fmt.Sprintf("recreate missing worktree: %v", recreateErr))
				if err := src.OnFail(ctx, *vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				return "", false
			}
			worktreePath = recreated
			vessel.WorktreePath = worktreePath
			if updateErr := r.Queue.UpdateVessel(*vessel); updateErr != nil {
				if r.cancelledTransition(vessel.ID, updateErr) {
					return "", false
				}
				log.Printf("warn: failed to persist worktree path for %s: %v", vessel.ID, updateErr)
			}
		}
		return worktreePath, true
	}

	branchName := src.BranchName(*vessel)
	created, err := r.Worktree.Create(ctx, branchName)
	if err != nil {
		r.failVessel(vessel.ID, err.Error())
		if failErr := src.OnFail(ctx, *vessel); failErr != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
		}
		return "", false
	}
	vessel.WorktreePath = created
	if updateErr := r.Queue.UpdateVessel(*vessel); updateErr != nil {
		if r.cancelledTransition(vessel.ID, updateErr) {
			return "", false
		}
		log.Printf("warn: failed to persist worktree path for %s: %v", vessel.ID, updateErr)
	}
	return created, true
}

func (r *Runner) resolveSourceForVessel(vessel queue.Vessel) source.Source {
	if r.Sources != nil {
		if configName := r.sourceConfigNameFromMeta(vessel); configName != "" {
			if src, ok := r.Sources[configName]; ok {
				return src
			}
		}
		if src, ok := r.Sources[vessel.Source]; ok {
			return src
		}
	}
	return &source.Manual{}
}

// buildCommand constructs the LLM command and args from config and vessel.
func buildCommand(cfg *config.Config, vessel *queue.Vessel) (string, []string, error) {
	// Direct prompt mode
	if vessel.Prompt != "" {
		prompt := vessel.Prompt
		if vessel.Ref != "" {
			prompt = fmt.Sprintf("Ref: %s\n\n%s", vessel.Ref, vessel.Prompt)
		}
		cmd, args := buildPromptOnlyCmdArgs(cfg, prompt)
		return cmd, args, nil
	}

	// Workflow-based mode: build command from flags (v2 phase-based execution will replace this)
	wfPrompt := fmt.Sprintf("/%s %s", vessel.Workflow, vessel.Ref)
	cmd, args := buildPromptOnlyCmdArgs(cfg, wfPrompt)
	return cmd, args, nil
}

func (r *Runner) removeWorktree(ctx context.Context, worktreePath, vesselID string) {
	if worktreePath == "" {
		return
	}
	if removeErr := r.Worktree.Remove(ctx, worktreePath); removeErr != nil {
		log.Printf("warn: failed to remove worktree for %s: %v", vesselID, removeErr)
	}
}

func (r *Runner) startWaitTransitionSpan(ctx context.Context, vessel queue.Vessel, transition string, waited time.Duration) observability.SpanContext {
	if r.Tracer == nil {
		return observability.SpanContext{}
	}
	attrs := append(
		observability.VesselSpanAttributes(observability.VesselSpanData{
			ID:       vessel.ID,
			Source:   vessel.Source,
			Workflow: vessel.Workflow,
			Ref:      vessel.Ref,
		}),
		observability.WaitSpanAttributes(observability.WaitSpanData{
			Transition: transition,
			PhaseName:  vessel.FailedPhase,
			Label:      vessel.WaitingFor,
			WaitedMS:   waited.Milliseconds(),
		})...,
	)
	return r.Tracer.StartSpan(ctx, "wait_transition:"+transition, attrs)
}

func (r *Runner) finishWaitTransitionSpan(span observability.SpanContext, err error) {
	if r.Tracer == nil {
		return
	}
	if err != nil {
		span.RecordError(err)
	}
	span.End()
}

func waitedDuration(since *time.Time, now time.Time) time.Duration {
	if since == nil {
		return 0
	}
	return now.Sub(*since)
}

func (r *Runner) watchVesselCancellation(parent context.Context, vesselID string) (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	go func() {
		ticker := time.NewTicker(vesselCancelPollInterval)
		defer ticker.Stop()
		for {
			cancelled, err := r.isVesselCancelled(vesselID)
			if err != nil {
				log.Printf("warn: inspect cancel state for %s: %v", vesselID, err)
			} else if cancelled {
				cancel(errVesselCancelled)
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return ctx, cancel
}

func (r *Runner) isVesselCancelled(vesselID string) (bool, error) {
	vessel, err := r.Queue.FindByID(vesselID)
	if err != nil {
		return false, fmt.Errorf("find vessel %s: %w", vesselID, err)
	}
	return vessel != nil && vessel.State == queue.StateCancelled, nil
}

func (r *Runner) vesselCancelled(ctx context.Context, vesselID string) bool {
	if errors.Is(context.Cause(ctx), errVesselCancelled) {
		return true
	}
	cancelled, err := r.isVesselCancelled(vesselID)
	if err != nil {
		log.Printf("warn: inspect cancel state for %s: %v", vesselID, err)
		return false
	}
	return cancelled
}

func (r *Runner) cancelledTransition(vesselID string, err error) bool {
	if err == nil || !errors.Is(err, queue.ErrInvalidTransition) {
		return false
	}
	cancelled, findErr := r.isVesselCancelled(vesselID)
	if findErr != nil {
		log.Printf("warn: inspect cancel state for %s: %v", vesselID, findErr)
		return false
	}
	return cancelled
}

func (r *Runner) cancelVessel(vessel queue.Vessel, worktreePath string, vrs *vesselRunState, claims []evidence.Claim) string {
	current := vessel
	if latest, err := r.Queue.FindByID(vessel.ID); err != nil {
		log.Printf("warn: inspect cancelled vessel %s: %v", vessel.ID, err)
	} else if latest != nil {
		current = *latest
	}
	log.Printf("%svessel cancelled; stopping execution", vesselLabel(current))
	r.persistRunArtifacts(current, string(queue.StateCancelled), vrs, claims, r.runtimeNow())
	r.removeWorktree(context.Background(), worktreePath, vessel.ID)
	return "cancelled"
}

func (r *Runner) failVessel(id string, errMsg string) {
	if updateErr := r.Queue.Update(id, queue.StateFailed, errMsg); updateErr != nil {
		if r.cancelledTransition(id, updateErr) {
			return
		}
		log.Printf("warn: failed to update vessel %s state: %v", id, updateErr)
	}
}

func (r *Runner) failUpdatedVessel(vessel *queue.Vessel, errMsg string) {
	if vessel == nil {
		return
	}
	now := r.runtimeNow()
	vessel.State = queue.StateFailed
	vessel.Error = errMsg
	vessel.EndedAt = &now
	if updateErr := r.Queue.UpdateVessel(*vessel); updateErr != nil {
		if r.cancelledTransition(vessel.ID, updateErr) {
			return
		}
		log.Printf("warn: failed to persist vessel %s state: %v", vessel.ID, updateErr)
		r.failVessel(vessel.ID, errMsg)
	}
}

func (r *Runner) completeVessel(ctx context.Context, vessel queue.Vessel, worktreePath string, phaseResults []reporter.PhaseResult, vrs *vesselRunState, claims []evidence.Claim) string {
	if updateErr := r.Queue.Update(vessel.ID, queue.StateCompleted, ""); updateErr != nil {
		if r.cancelledTransition(vessel.ID, updateErr) {
			return r.cancelVessel(vessel, worktreePath, vrs, claims)
		}
		log.Printf("warn: failed to update vessel %s state: %v", vessel.ID, updateErr)
	}

	manifest := r.persistRunArtifacts(vessel, string(queue.StateCompleted), vrs, claims, r.runtimeNow())

	// Clean up worktree (best-effort)
	r.removeWorktree(ctx, worktreePath, vessel.ID)

	// Report completion
	issueNum := r.parseIssueNum(vessel)
	if issueNum > 0 && r.Reporter != nil {
		r.logReporterError("post vessel-completed comment", vessel.ID,
			r.Reporter.VesselCompleted(ctx, issueNum, phaseResults, manifest))
	}

	return "completed"
}

func (r *Runner) persistRunArtifacts(vessel queue.Vessel, state string, vrs *vesselRunState, claims []evidence.Claim, now time.Time) *evidence.Manifest {
	if vrs == nil {
		vrs = newVesselRunState(r.Config, vessel, now)
	}

	summary := vrs.buildSummary(state, now)
	reviewArtifacts := &ReviewArtifacts{}
	var manifest *evidence.Manifest
	if len(claims) > 0 {
		manifest = &evidence.Manifest{
			VesselID:  vessel.ID,
			Workflow:  vessel.Workflow,
			Claims:    append([]evidence.Claim(nil), claims...),
			CreatedAt: now.UTC(),
		}
		if err := evidence.SaveManifest(r.Config.StateDir, vessel.ID, manifest); err != nil {
			log.Printf("warn: save evidence manifest: %v", err)
		} else {
			summary.EvidenceManifestPath = evidenceManifestRelativePath(vessel.ID)
			reviewArtifacts.EvidenceManifest = summary.EvidenceManifestPath
		}
	}

	if vrs.costTracker != nil {
		reportPath := filepath.Join(r.Config.StateDir, "phases", vessel.ID, costReportFileName)
		if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
			log.Printf("warn: save cost report: %v", fmt.Errorf("create dir: %w", err))
		} else if err := cost.SaveReport(reportPath, vrs.costTracker.Report(vessel.ID)); err != nil {
			log.Printf("warn: save cost report: %v", err)
		} else {
			summary.CostReportPath = costReportRelativePath(vessel.ID)
			reviewArtifacts.CostReport = summary.CostReportPath
		}

		alertsPath := filepath.Join(r.Config.StateDir, "phases", vessel.ID, budgetAlertsFileName)
		if err := saveJSONArtifact(alertsPath, vrs.costTracker.Alerts()); err != nil {
			log.Printf("warn: save budget alerts: %v", err)
		} else {
			summary.BudgetAlertsPath = budgetAlertsRelativePath(vessel.ID)
			reviewArtifacts.BudgetAlerts = summary.BudgetAlertsPath
		}
	}

	evalPath := filepath.Join(r.Config.StateDir, "phases", vessel.ID, evalReportFileName)
	if info, err := os.Stat(evalPath); err == nil && !info.IsDir() {
		summary.EvalReportPath = evalReportRelativePath(vessel.ID)
		reviewArtifacts.EvalReport = summary.EvalReportPath
	}

	if state == string(queue.StateFailed) || state == string(queue.StateTimedOut) {
		failureReview := r.buildFailureReview(vessel, summary, now)
		if err := recovery.SaveFailureReview(r.Config.StateDir, failureReview); err != nil {
			log.Printf("warn: save failure review: %v", err)
		} else {
			summary.FailureReviewPath = recovery.FailureReviewRelativePath(vessel.ID)
			reviewArtifacts.FailureReview = summary.FailureReviewPath
		}
	}

	if reviewArtifacts.EvidenceManifest != "" || reviewArtifacts.CostReport != "" ||
		reviewArtifacts.BudgetAlerts != "" || reviewArtifacts.EvalReport != "" ||
		reviewArtifacts.FailureReview != "" {
		summary.ReviewArtifacts = reviewArtifacts
	}

	if err := SaveVesselSummary(r.Config.StateDir, summary); err != nil {
		log.Printf("warn: save vessel summary: %v", err)
	}

	return manifest
}

func (r *Runner) buildFailureReview(vessel queue.Vessel, summary *VesselSummary, now time.Time) *recovery.FailureReview {
	evidencePaths := []string{filepath.ToSlash(filepath.Join("phases", vessel.ID, summaryFileName))}
	for _, path := range []string{
		summary.EvidenceManifestPath,
		summary.CostReportPath,
		summary.BudgetAlertsPath,
		summary.EvalReportPath,
	} {
		if strings.TrimSpace(path) == "" {
			continue
		}
		evidencePaths = append(evidencePaths, path)
	}

	hypothesis := strings.TrimSpace(vessel.Error)
	if hypothesis == "" {
		hypothesis = strings.TrimSpace(vessel.GateOutput)
	}

	return &recovery.FailureReview{
		VesselID:           vessel.ID,
		FailureFingerprint: recovery.FailureFingerprint(vessel),
		SourceRef:          vessel.Ref,
		Workflow:           vessel.Workflow,
		FailedPhase:        vessel.FailedPhase,
		Class:              "unknown",
		RecommendedAction:  "retry",
		RetryCount:         recovery.RetryCountFromVessel(vessel),
		RetryCap:           2,
		EvidencePaths:      evidencePaths,
		Hypothesis:         hypothesis,
		Unlock: recovery.UnlockFingerprint{
			SourceInputFingerprint: vessel.Meta["source_input_fingerprint"],
			HarnessDigest:          recovery.CurrentHarnessDigest(),
			WorkflowDigest:         recovery.CurrentWorkflowDigest(vessel.Workflow),
		},
	}
}

func saveJSONArtifact(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// runVesselOrchestrated executes a workflow with explicit phase dependencies
// using the orchestrator for tracking and wave-based parallel execution.
// Phases within the same wave (no dependencies between them) run concurrently.
// Context firewalls ensure each phase only sees outputs from its declared dependencies.
func (r *Runner) runVesselOrchestrated(ctx context.Context, vessel queue.Vessel, wf *workflow.Workflow, issueData phase.IssueData, harnessContent, worktreePath string, src source.Source, vrs *vesselRunState, claims *[]evidence.Claim) string {
	graph, err := buildPhaseGraph(wf)
	if err != nil {
		r.failVessel(vessel.ID, fmt.Sprintf("build phase graph: %v", err))
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		return "failed"
	}

	// Rebuild previous outputs for resume.
	allOutputs := r.rebuildPreviousOutputs(vessel.ID, wf)

	// Mark already-completed phases in orchestrator.
	for phaseName, output := range allOutputs {
		_ = graph.orch.UpdateAgent(phaseName, orchestrator.StatusCompleted, 0, 0, "")
		_ = graph.orch.SetResult(orchestrator.SubAgentResult{
			AgentID: phaseName,
			Summary: output,
			Success: true,
		})
	}

	var allPhaseResults []reporter.PhaseResult

	for waveIdx, wave := range graph.waves {
		// Filter out already-completed phases.
		var pending []int
		for _, idx := range wave {
			if _, done := allOutputs[wf.Phases[idx].Name]; !done {
				pending = append(pending, idx)
			}
		}
		if len(pending) == 0 {
			continue
		}

		// Execute wave: single phase runs inline, multiple phases run concurrently.
		type waveResult struct {
			phaseIdx int
			result   singlePhaseResult
		}

		results := make([]waveResult, len(pending))

		if len(pending) == 1 {
			// Single phase: run inline (no goroutine overhead).
			idx := pending[0]
			depOutputs := graph.dependencyOutputs(idx, allOutputs)
			res := r.runSinglePhase(ctx, vessel, wf, idx, depOutputs, issueData, harnessContent, worktreePath, src, vrs, true)
			results[0] = waveResult{phaseIdx: idx, result: res}
		} else {
			// Multiple phases: run concurrently.
			var wg sync.WaitGroup
			var mu sync.Mutex
			for ri, idx := range pending {
				wg.Add(1)
				go func(ri, idx int) {
					defer wg.Done()
					mu.Lock()
					depOutputs := graph.dependencyOutputs(idx, allOutputs)
					mu.Unlock()

					// Mark running in orchestrator.
					mu.Lock()
					_ = graph.orch.UpdateAgent(wf.Phases[idx].Name, orchestrator.StatusRunning, 0, 0, "")
					mu.Unlock()

					res := r.runSinglePhase(ctx, vessel, wf, idx, depOutputs, issueData, harnessContent, worktreePath, src, vrs, false)

					mu.Lock()
					results[ri] = waveResult{phaseIdx: idx, result: res}
					mu.Unlock()
				}(ri, idx)
			}
			wg.Wait()
		}

		// Process wave results: update orchestrator, collect outputs.
		waveFailed := false
		waveWaiting := false
		waveNoOp := false
		waveCancelled := false
		for _, res := range results {
			p := wf.Phases[res.phaseIdx]
			result := res.result

			if result.phaseSummary.Name != "" {
				vrs.addPhase(result.phaseSummary)
			}
			if result.evidenceClaim != nil && claims != nil {
				*claims = append(*claims, *result.evidenceClaim)
			}

			switch result.status {
			case "completed", "no-op":
				_ = graph.orch.UpdateAgent(p.Name, orchestrator.StatusCompleted, 0, result.duration, "")
				_ = graph.orch.SetResult(orchestrator.SubAgentResult{
					AgentID: p.Name,
					Summary: result.output,
					Success: true,
				})
				allOutputs[p.Name] = result.output
			case "failed":
				_ = graph.orch.UpdateAgent(p.Name, orchestrator.StatusFailed, 0, result.duration, result.gateOut)
				waveFailed = true
			case "waiting":
				waveWaiting = true
			case "cancelled":
				waveCancelled = true
			}

			if result.status == "completed" || result.status == "no-op" {
				allPhaseResults = append(allPhaseResults, reporter.PhaseResult{
					Name:     p.Name,
					Duration: result.duration,
					Status:   result.status,
				})
			}

			if result.status == "no-op" {
				waveNoOp = true
			}
		}

		if waveFailed {
			return "failed"
		}
		if waveCancelled {
			var completedClaims []evidence.Claim
			if claims != nil {
				completedClaims = *claims
			}
			return r.cancelVessel(vessel, worktreePath, vrs, completedClaims)
		}
		if waveWaiting {
			return "waiting"
		}
		if waveNoOp {
			log.Printf("%sphase triggered no-op; completing workflow early", vesselLabel(vessel))
			if r.vesselCancelled(ctx, vessel.ID) {
				var completedClaims []evidence.Claim
				if claims != nil {
					completedClaims = *claims
				}
				return r.cancelVessel(vessel, worktreePath, vrs, completedClaims)
			}
			if err := src.OnComplete(ctx, vessel); err != nil {
				log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
			}
			var completedClaims []evidence.Claim
			if claims != nil {
				completedClaims = *claims
			}
			return r.completeVessel(ctx, vessel, worktreePath, allPhaseResults, vrs, completedClaims)
		}

		if waveIdx < len(graph.waves)-1 && vrs.costTracker != nil && vrs.costTracker.BudgetExceeded() {
			errMsg := fmt.Sprintf("budget exceeded after concurrent wave: estimated cost $%.4f, estimated tokens %d",
				vrs.costTracker.TotalCost(), vrs.costTracker.TotalTokens())
			r.failVessel(vessel.ID, errMsg)
			if err := src.OnFail(ctx, vessel); err != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
			}
			issueNum := r.parseIssueNum(vessel)
			if issueNum > 0 && r.Reporter != nil {
				r.logReporterError("post vessel-failed comment", vessel.ID,
					r.Reporter.VesselFailed(ctx, issueNum, "", errMsg, ""))
			}
			return "failed"
		}
	}

	// All waves complete.
	log.Printf("%scompleted all phases (orchestrated, %d waves)", vesselLabel(vessel), len(graph.waves))
	if r.vesselCancelled(ctx, vessel.ID) {
		var completedClaims []evidence.Claim
		if claims != nil {
			completedClaims = *claims
		}
		return r.cancelVessel(vessel, worktreePath, vrs, completedClaims)
	}
	if err := src.OnComplete(ctx, vessel); err != nil {
		log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
	}
	var completedClaims []evidence.Claim
	if claims != nil {
		completedClaims = *claims
	}
	return r.completeVessel(ctx, vessel, worktreePath, allPhaseResults, vrs, completedClaims)
}

// singlePhaseResult holds the outcome of executing one phase including its gate.
type singlePhaseResult struct {
	output        string
	status        string // "completed", "no-op", "failed", "waiting", "cancelled"
	duration      time.Duration
	gateOut       string
	phaseSummary  PhaseSummary
	evidenceClaim *evidence.Claim
}

type gateExecutionResult struct {
	output        string
	passed        bool
	err           error
	evidenceClaim *evidence.Claim
}

// runSinglePhase executes a single workflow phase (prompt or command), including
// gate evaluation and retries. It returns the outcome without mutating the
// vessel's queue state directly (the caller handles that).
func (r *Runner) runSinglePhase(ctx context.Context, vessel queue.Vessel, wf *workflow.Workflow, phaseIdx int, previousOutputs map[string]string, issueData phase.IssueData, harnessContent, worktreePath string, src source.Source, vrs *vesselRunState, enforceBudget bool) singlePhaseResult {
	p := wf.Phases[phaseIdx]
	gateResult := ""
	gateRetries := 0
	if p.Gate != nil && (p.Gate.Type == "command" || p.Gate.Type == "live") && p.Gate.Retries > 0 {
		gateRetries = p.Gate.Retries
	}

	srcCfg := r.sourceConfigFromMeta(vessel)

	for {
		if r.vesselCancelled(ctx, vessel.ID) {
			return singlePhaseResult{status: "cancelled"}
		}
		log.Printf("%sphase %q starting (orchestrated)", vesselLabel(vessel), p.Name)
		phaseStart := r.runtimeNow()

		td := phase.TemplateData{
			Issue: issueData,
			Phase: phase.PhaseData{
				Name:  p.Name,
				Index: phaseIdx,
			},
			PreviousOutputs: previousOutputs,
			GateResult:      gateResult,
			Vessel: phase.VesselData{
				ID:     vessel.ID,
				Source: vessel.Source,
			},
		}

		var output []byte
		var runErr error
		var beforeSnapshot surface.Snapshot
		var checkProtectedSurfaces bool
		var promptForCost string
		provider := resolveProvider(r.Config, srcCfg, wf, &p)
		model := resolveModel(r.Config, srcCfg, wf, &p, provider)
		retryAttempt := 0
		if p.Gate != nil && (p.Gate.Type == "command" || p.Gate.Type == "live") {
			retryAttempt = providerAttempt(&p, gateRetries)
		}
		phaseSpan := startPhaseSpan(r.Tracer, ctx, r.Config, srcCfg, wf, p, phaseIdx, retryAttempt)
		phaseSpanEnded := false
		var phaseDuration time.Duration
		phaseSpanStatus := "running"
		phaseOutputArtifactPath := ""
		finishCurrentPhaseSpan := func(err error) {
			if phaseSpanEnded {
				return
			}
			if err != nil && phaseSpanStatus == "running" {
				phaseSpanStatus = "failed"
			}
			if phaseDuration == 0 {
				phaseDuration = r.runtimeSince(phaseStart)
			}
			finishPhaseSpan(r.Tracer, phaseSpan, buildPhaseResultData(r.Config, srcCfg, wf, p, promptForCost, string(output), phaseDuration, phaseSpanStatus, phaseOutputArtifactPath), err)
			phaseSpanEnded = true
		}

		phasesDir := filepath.Join(r.Config.StateDir, "phases", vessel.ID)
		if err := os.MkdirAll(phasesDir, 0o755); err != nil {
			finishCurrentPhaseSpan(err)
			r.failVessel(vessel.ID, fmt.Sprintf("create phases dir: %v", err))
			if failErr := src.OnFail(ctx, vessel); failErr != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
			}
			return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
		}

		if p.Type == "command" {
			rendered, err := renderCommandTemplate(p.Name, "command", p.Run, td)
			if err != nil {
				finishCurrentPhaseSpan(err)
				r.failVessel(vessel.ID, err.Error())
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
			}
			if wErr := os.WriteFile(filepath.Join(phasesDir, p.Name+".command"), []byte(rendered), 0o644); wErr != nil {
				log.Printf("warn: write command file: %v", wErr)
			}
			if policyErr := r.enforcePhasePolicy(vessel, p, rendered, ""); policyErr != nil {
				finishCurrentPhaseSpan(policyErr)
				log.Printf("%sphase %q blocked: %v", vesselLabel(vessel), p.Name, policyErr)
				vessel.FailedPhase = p.Name
				r.failUpdatedVessel(&vessel, policyErr.Error())
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				issueNum := r.parseIssueNum(vessel)
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post vessel-failed comment", vessel.ID,
						r.Reporter.VesselFailed(ctx, issueNum, p.Name, policyErr.Error(), ""))
				}
				return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
			}
			beforeSnapshot, checkProtectedSurfaces, err = r.takeProtectedSurfaceSnapshot(ctx, worktreePath)
			if err != nil {
				snapErr := fmt.Errorf("protected surface snapshot failed: %w", err)
				finishCurrentPhaseSpan(snapErr)
				r.failVessel(vessel.ID, fmt.Sprintf("phase %s: %v", p.Name, snapErr))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				issueNum := r.parseIssueNum(vessel)
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post vessel-failed comment", vessel.ID,
						r.Reporter.VesselFailed(ctx, issueNum, p.Name, snapErr.Error(), ""))
				}
				return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
			}
			cmdOut, cmdErr := gate.RunCommand(ctx, r.Runner, worktreePath, rendered)
			output = []byte(cmdOut)
			runErr = cmdErr
		} else {
			promptContent, err := os.ReadFile(p.PromptFile)
			if err != nil {
				finishCurrentPhaseSpan(err)
				r.failVessel(vessel.ID, fmt.Sprintf("read prompt file %s: %v", p.PromptFile, err))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
			}
			promptTemplate := string(promptContent)
			rendered, err := phase.RenderPrompt(promptTemplate, td)
			if err != nil {
				finishCurrentPhaseSpan(err)
				r.failVessel(vessel.ID, fmt.Sprintf("render prompt for phase %s: %v", p.Name, err))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
			}
			promptForCost = rendered
			if harnessContent != "" {
				promptForCost = harnessContent + "\n\n" + rendered
			}
			promptPath := filepath.Join(phasesDir, p.Name+".prompt")
			if wErr := os.WriteFile(promptPath, []byte(rendered), 0o644); wErr != nil {
				log.Printf("warn: write prompt file %s: %v", promptPath, wErr)
			}
			if policyErr := r.enforcePhasePolicy(vessel, p, "", promptTemplate); policyErr != nil {
				finishCurrentPhaseSpan(policyErr)
				log.Printf("%sphase %q blocked: %v", vesselLabel(vessel), p.Name, policyErr)
				vessel.FailedPhase = p.Name
				r.failUpdatedVessel(&vessel, policyErr.Error())
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				issueNum := r.parseIssueNum(vessel)
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post vessel-failed comment", vessel.ID,
						r.Reporter.VesselFailed(ctx, issueNum, p.Name, policyErr.Error(), ""))
				}
				return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
			}
			beforeSnapshot, checkProtectedSurfaces, err = r.takeProtectedSurfaceSnapshot(ctx, worktreePath)
			if err != nil {
				snapErr := fmt.Errorf("protected surface snapshot failed: %w", err)
				finishCurrentPhaseSpan(snapErr)
				r.failVessel(vessel.ID, fmt.Sprintf("phase %s: %v", p.Name, snapErr))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				issueNum := r.parseIssueNum(vessel)
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post vessel-failed comment", vessel.ID,
						r.Reporter.VesselFailed(ctx, issueNum, p.Name, snapErr.Error(), ""))
				}
				return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
			}
			attempt := providerAttempt(&p, gateRetries)
			cmd, args, phaseStdin := buildProviderPhaseArgs(r.Config, srcCfg, wf, &p, harnessContent, provider, rendered, attempt)
			var stdinContent string
			if phaseStdin != nil {
				stdinContent = rendered
			}
			output, runErr = r.runPhaseWithRateLimitRetry(ctx, worktreePath, stdinContent, cmd, args)
		}

		if r.vesselCancelled(ctx, vessel.ID) {
			finishCurrentPhaseSpan(context.Canceled)
			return singlePhaseResult{status: "cancelled", duration: r.runtimeSince(phaseStart)}
		}

		// Write output file.
		outputPath := filepath.Join(phasesDir, p.Name+".output")
		phaseOutputArtifactPath = phaseArtifactRelativePath(vessel.ID, p.Name)
		if wErr := os.WriteFile(outputPath, output, 0o644); wErr != nil {
			log.Printf("warn: write output file %s: %v", outputPath, wErr)
		}
		fmt.Printf("Phase %s complete: %s\n", p.Name, outputPath)
		phaseDuration = r.runtimeSince(phaseStart)

		if runErr != nil {
			phaseSpanStatus = "failed"
			finishCurrentPhaseSpan(runErr)
			log.Printf("%sphase %q failed: %v", vesselLabel(vessel), p.Name, runErr)
			vessel.FailedPhase = p.Name
			r.failUpdatedVessel(&vessel, fmt.Sprintf("phase %s: %v", p.Name, runErr))
			if err := src.OnFail(ctx, vessel); err != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
			}
			issueNum := r.parseIssueNum(vessel)
			if issueNum > 0 && r.Reporter != nil {
				r.logReporterError("post vessel-failed comment", vessel.ID,
					r.Reporter.VesselFailed(ctx, issueNum, p.Name, runErr.Error(), ""))
			}
			return singlePhaseResult{
				status:       "failed",
				duration:     phaseDuration,
				phaseSummary: vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, 0, 0, 0.0, phaseDuration, "failed", nil, runErr.Error()),
			}
		}

		if checkProtectedSurfaces {
			if err := r.verifyProtectedSurfaces(vessel, p, worktreePath, beforeSnapshot); err != nil {
				phaseSpanStatus = "failed"
				finishCurrentPhaseSpan(err)
				log.Printf("%sphase %q violated protected surfaces: %v", vesselLabel(vessel), p.Name, err)
				vessel.FailedPhase = p.Name
				r.failUpdatedVessel(&vessel, err.Error())
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				issueNum := r.parseIssueNum(vessel)
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post vessel-failed comment", vessel.ID,
						r.Reporter.VesselFailed(ctx, issueNum, p.Name, err.Error(), ""))
				}
				return singlePhaseResult{
					status:       "failed",
					duration:     phaseDuration,
					phaseSummary: vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, 0, 0, 0.0, phaseDuration, "failed", nil, err.Error()),
				}
			}
		}

		recordedAt := r.runtimeNow()
		inputTokensEst, outputTokensEst, costUSDEst := vrs.recordPhaseTokens(p, model, promptForCost, string(output), recordedAt)
		if enforceBudget && vrs.costTracker != nil && vrs.costTracker.BudgetExceeded() {
			errMsg := fmt.Sprintf("budget exceeded after phase %q: estimated cost $%.4f, estimated tokens %d",
				p.Name, vrs.costTracker.TotalCost(), vrs.costTracker.TotalTokens())
			vessel.FailedPhase = p.Name
			r.failUpdatedVessel(&vessel, errMsg)
			if err := src.OnFail(ctx, vessel); err != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
			}
			issueNum := r.parseIssueNum(vessel)
			if issueNum > 0 && r.Reporter != nil {
				r.logReporterError("post vessel-failed comment", vessel.ID,
					r.Reporter.VesselFailed(ctx, issueNum, p.Name, errMsg, ""))
			}
			phaseSpanStatus = "failed"
			finishCurrentPhaseSpan(fmt.Errorf("%s", errMsg))
			return singlePhaseResult{
				status:       "failed",
				duration:     phaseDuration,
				phaseSummary: vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", nil, errMsg),
			}
		}

		// Report phase completion.
		issueNum := r.parseIssueNum(vessel)
		if phaseMatchedNoOp(&p, string(output)) {
			if issueNum > 0 && r.Reporter != nil {
				r.logReporterError("post phase-complete comment", vessel.ID,
					r.Reporter.PhaseComplete(ctx, issueNum, p.Name, phaseDuration, string(output)))
			}
			phaseSpanStatus = "no-op"
			finishCurrentPhaseSpan(nil)
			return singlePhaseResult{
				output:        string(output),
				status:        "no-op",
				duration:      phaseDuration,
				phaseSummary:  vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "no-op", nil, ""),
				evidenceClaim: nil,
			}
		}

		if issueNum > 0 && r.Reporter != nil {
			r.logReporterError("post phase-complete comment", vessel.ID,
				r.Reporter.PhaseComplete(ctx, issueNum, p.Name, phaseDuration, string(output)))
		}

		// Handle gate.
		if p.Gate == nil {
			phaseSpanStatus = "completed"
			finishCurrentPhaseSpan(nil)
			return singlePhaseResult{
				output:        string(output),
				status:        "completed",
				duration:      phaseDuration,
				phaseSummary:  vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "completed", nil, ""),
				evidenceClaim: nil,
			}
		}

		switch p.Gate.Type {
		case "command", "live":
			gateResultExec := r.executeVerificationGate(ctx, phaseSpan, vessel, p, td, worktreePath, retryAttempt)
			if r.vesselCancelled(ctx, vessel.ID) {
				finishCurrentPhaseSpan(context.Canceled)
				return singlePhaseResult{status: "cancelled", duration: phaseDuration}
			}
			gateOut, passed, gateErr := gateResultExec.output, gateResultExec.passed, gateResultExec.err
			if gateErr != nil {
				r.failVessel(vessel.ID, fmt.Sprintf("phase %s gate error: %v", p.Name, gateErr))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				phaseSpanStatus = "failed"
				finishCurrentPhaseSpan(nil)
				return singlePhaseResult{
					status:       "failed",
					duration:     phaseDuration,
					gateOut:      gateOut,
					phaseSummary: vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), gateErr.Error()),
				}
			}
			if passed {
				log.Printf("%sgate passed for phase %q", vesselLabel(vessel), p.Name)
				phaseSpanStatus = "completed"
				finishCurrentPhaseSpan(nil)
				return singlePhaseResult{
					output:        string(output),
					status:        "completed",
					duration:      phaseDuration,
					phaseSummary:  vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "completed", gatePassedPointer(true), ""),
					evidenceClaim: gateResultExec.evidenceClaim,
				}
			}

			// Gate failed — retry or fail.
			retryDelay := 10 * time.Second
			if p.Gate.RetryDelay != "" {
				if parsed, pErr := time.ParseDuration(p.Gate.RetryDelay); pErr == nil {
					retryDelay = parsed
				}
			}
			if gateRetries <= 0 {
				log.Printf("%sgate failed for phase %q, retries exhausted", vesselLabel(vessel), p.Name)
				vessel.FailedPhase = p.Name
				vessel.GateOutput = gateOut
				r.failUpdatedVessel(&vessel, fmt.Sprintf("phase %s: gate failed, retries exhausted", p.Name))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post vessel-failed comment", vessel.ID,
						r.Reporter.VesselFailed(ctx, issueNum, p.Name, "gate failed, retries exhausted", gateOut))
				}
				phaseSpanStatus = "failed"
				finishCurrentPhaseSpan(nil)
				return singlePhaseResult{
					status:       "failed",
					duration:     phaseDuration,
					gateOut:      gateOut,
					phaseSummary: vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), "gate failed, retries exhausted"),
				}
			}
			gateRetries--
			log.Printf("%sgate failed for phase %q, retries remaining=%d", vesselLabel(vessel), p.Name, gateRetries)
			gateResult = fmt.Sprintf("The following gate check failed after the previous phase. Fix the issues and try again:\n\n%s", gateOut)
			if err := r.runtimeSleep(ctx, retryDelay); err != nil {
				if r.vesselCancelled(ctx, vessel.ID) {
					finishCurrentPhaseSpan(context.Canceled)
					return singlePhaseResult{status: "cancelled", duration: phaseDuration}
				}
				r.failVessel(vessel.ID, fmt.Sprintf("phase %s gate retry interrupted: %v", p.Name, err))
				if failErr := src.OnFail(ctx, vessel); failErr != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
				}
				phaseSpanStatus = "failed"
				finishCurrentPhaseSpan(err)
				return singlePhaseResult{
					status:       "failed",
					duration:     phaseDuration,
					gateOut:      gateOut,
					phaseSummary: vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), err.Error()),
				}
			}
			phaseSpanStatus = "retrying"
			finishCurrentPhaseSpan(nil)
			continue // re-run phase

		case "label":
			gateSpan := startGateSpan(r.Tracer, phaseSpan, ctx, p.Gate.Type)
			finishGateSpan(r.Tracer, gateSpan, observability.GateSpanData{
				Type:         p.Gate.Type,
				Passed:       false,
				RetryAttempt: retryAttempt,
			}, nil)
			log.Printf("%swaiting for label %q after phase %q", vesselLabel(vessel), p.Gate.WaitFor, p.Name)
			vessel.FailedPhase = p.Name
			vessel.WaitingFor = p.Gate.WaitFor
			now := r.runtimeNow()
			vessel.WaitingSince = &now
			vessel.State = queue.StateWaiting
			vessel.CurrentPhase = phaseIdx + 1
			if updateErr := r.Queue.UpdateVessel(vessel); updateErr != nil {
				if r.cancelledTransition(vessel.ID, updateErr) {
					finishCurrentPhaseSpan(context.Canceled)
					return singlePhaseResult{status: "cancelled", duration: r.runtimeSince(phaseStart)}
				}
				log.Printf("warn: persist waiting state for %s: %v", vessel.ID, updateErr)
				finishCurrentPhaseSpan(updateErr)
				return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
			}
			if err := src.OnWait(ctx, vessel); err != nil {
				log.Printf("warn: OnWait hook for vessel %s: %v", vessel.ID, err)
			}
			waitSpan := r.startWaitTransitionSpan(ctx, vessel, "waiting", 0)
			r.finishWaitTransitionSpan(waitSpan, nil)
			phaseSpanStatus = "waiting"
			finishCurrentPhaseSpan(nil)
			return singlePhaseResult{output: string(output), status: "waiting", duration: r.runtimeSince(phaseStart)}
		}

		// Unknown gate type: treat as passed.
		phaseSpanStatus = "completed"
		finishCurrentPhaseSpan(nil)
		return singlePhaseResult{
			output:        string(output),
			status:        "completed",
			duration:      phaseDuration,
			phaseSummary:  vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "completed", nil, ""),
			evidenceClaim: nil,
		}
	}
}

func startPhaseSpan(tracer *observability.Tracer, ctx context.Context, cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p workflow.Phase, phaseIdx int, retryAttempt int) observability.SpanContext {
	if tracer == nil {
		return observability.SpanContext{}
	}

	provider := resolveProvider(cfg, srcCfg, wf, &p)
	model := resolveModel(cfg, srcCfg, wf, &p, provider)

	return tracer.StartSpan(ctx, "phase:"+p.Name, observability.PhaseSpanAttributes(observability.PhaseSpanData{
		Name:         p.Name,
		Index:        phaseIdx,
		Type:         phaseTypeLabel(p),
		Workflow:     workflowName(wf),
		Provider:     provider,
		Model:        model,
		RetryAttempt: retryAttempt,
		SandboxMode:  sandboxModeFromFlags(cfg),
	}))
}

func finishPhaseSpan(tracer *observability.Tracer, span observability.SpanContext, data observability.PhaseResultData, err error) {
	if tracer == nil {
		return
	}

	span.AddAttributes(observability.PhaseResultAttributes(data))
	if err != nil {
		span.RecordError(err)
	}
	span.End()
}

func startGateSpan(tracer *observability.Tracer, phaseSpan observability.SpanContext, ctx context.Context, gateType string) observability.SpanContext {
	if tracer == nil {
		return observability.SpanContext{}
	}

	gateCtx := ctx
	if phaseCtx := phaseSpan.Context(); phaseCtx != nil {
		gateCtx = phaseCtx
	}

	return tracer.StartSpan(gateCtx, "gate:"+gateType, nil)
}

func finishGateSpan(tracer *observability.Tracer, span observability.SpanContext, data observability.GateSpanData, err error) {
	if tracer == nil {
		return
	}

	span.AddAttributes(observability.GateSpanAttributes(data))
	if err != nil {
		span.RecordError(err)
	}
	span.End()
}

func startGateStepSpan(tracer *observability.Tracer, gateSpan observability.SpanContext, ctx context.Context, name string) observability.SpanContext {
	if tracer == nil {
		return observability.SpanContext{}
	}

	stepCtx := ctx
	if gateCtx := gateSpan.Context(); gateCtx != nil {
		stepCtx = gateCtx
	}

	return tracer.StartSpan(stepCtx, "gate_step:"+name, nil)
}

func finishGateStepSpan(tracer *observability.Tracer, span observability.SpanContext, data observability.GateStepSpanData, err error) {
	if tracer == nil {
		return
	}

	span.AddAttributes(observability.GateStepSpanAttributes(data))
	if err != nil {
		span.RecordError(err)
	}
	span.End()
}

func (r *Runner) executeVerificationGate(ctx context.Context, phaseSpan observability.SpanContext, vessel queue.Vessel, p workflow.Phase, td phase.TemplateData, worktreePath string, retryAttempt int) gateExecutionResult {
	if p.Gate == nil {
		return gateExecutionResult{passed: true}
	}

	switch p.Gate.Type {
	case "command":
		rendered, err := renderCommandTemplate(p.Name, "gate command", p.Gate.Run, td)
		if err != nil {
			return gateExecutionResult{err: err}
		}
		gateSpan := startGateSpan(r.Tracer, phaseSpan, ctx, p.Gate.Type)
		gateOut, passed, gateErr := gate.RunCommandGate(ctx, r.Runner, worktreePath, rendered)
		finishGateSpan(r.Tracer, gateSpan, observability.GateSpanData{
			Type:         p.Gate.Type,
			Passed:       passed,
			RetryAttempt: retryAttempt,
		}, gateErr)
		result := gateExecutionResult{
			output: gateOut,
			passed: passed,
			err:    gateErr,
		}
		if passed {
			gateRecordedAt := r.runtimeNow()
			claim := buildGateClaim(p, true, phaseArtifactRelativePath(vessel.ID, p.Name), gateRecordedAt)
			result.evidenceClaim = &claim
		}
		return result
	case "live":
		gateSpan := startGateSpan(r.Tracer, phaseSpan, ctx, p.Gate.Type)
		liveGate := r.LiveGate
		if liveGate == nil {
			liveGate = gate.NewLiveVerifier()
		}
		liveResult, gateErr := liveGate.Run(ctx, r.Runner, gate.LiveRequest{
			StateDir:    r.Config.StateDir,
			VesselID:    vessel.ID,
			PhaseName:   p.Name,
			WorktreeDir: worktreePath,
			Gate:        p.Gate,
		})
		if liveResult != nil {
			for _, step := range liveResult.Steps {
				stepSpan := startGateStepSpan(r.Tracer, gateSpan, ctx, step.Name)
				var stepErr error
				if !step.Passed && step.Message != "" {
					stepErr = fmt.Errorf("%s", step.Message)
				}
				finishGateStepSpan(r.Tracer, stepSpan, observability.GateStepSpanData{
					Name:   step.Name,
					Mode:   step.Mode,
					Passed: step.Passed,
				}, stepErr)
			}
		}
		passed := gateErr == nil && liveResult != nil && liveResult.Passed
		finishGateSpan(r.Tracer, gateSpan, observability.GateSpanData{
			Type:         p.Gate.Type,
			Passed:       passed,
			RetryAttempt: retryAttempt,
		}, gateErr)
		result := gateExecutionResult{
			passed: passed,
			err:    gateErr,
		}
		if liveResult != nil {
			result.output = liveResult.Output
			if liveResult.Passed {
				gateRecordedAt := r.runtimeNow()
				claim := buildGateClaim(p, true, liveResult.ReportPath, gateRecordedAt)
				result.evidenceClaim = &claim
			}
		}
		return result
	default:
		return gateExecutionResult{passed: true}
	}
}

func buildPhaseResultData(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p workflow.Phase, renderedPrompt, output string, duration time.Duration, status string, outputArtifactPath string) observability.PhaseResultData {
	data := observability.PhaseResultData{
		DurationMS:         duration.Milliseconds(),
		Status:             status,
		OutputArtifactPath: outputArtifactPath,
	}
	if phaseTypeLabel(p) != "prompt" {
		return data
	}

	provider := resolveProvider(cfg, srcCfg, wf, &p)
	model := resolveModel(cfg, srcCfg, wf, &p, provider)
	data.InputTokensEst = cost.EstimateTokens(renderedPrompt)
	data.OutputTokensEst = cost.EstimateTokens(output)
	data.CostUSDEst = cost.EstimateCost(data.InputTokensEst, data.OutputTokensEst, cost.LookupPricing(model))
	return data
}

func workflowName(wf *workflow.Workflow) string {
	if wf == nil {
		return ""
	}
	return wf.Name
}

func sandboxModeFromFlags(cfg *config.Config) string {
	if cfg == nil {
		return "default"
	}
	fields := strings.Fields(cfg.Claude.Flags)
	for i, field := range fields {
		switch {
		case field == "--dangerously-skip-permissions":
			return "dangerously-skip-permissions"
		case field == "--permission-mode" || field == "--sandbox":
			if i+1 < len(fields) && strings.TrimSpace(fields[i+1]) != "" {
				return fields[i+1]
			}
		case strings.HasPrefix(field, "--permission-mode="):
			return strings.TrimPrefix(field, "--permission-mode=")
		case strings.HasPrefix(field, "--sandbox="):
			return strings.TrimPrefix(field, "--sandbox=")
		}
	}
	return "default"
}

func phaseMatchedNoOp(p *workflow.Phase, output string) bool {
	return p != nil && p.NoOp != nil && strings.Contains(output, p.NoOp.Match)
}

func buildGateClaim(p workflow.Phase, passed bool, artifactPath string, recordedAt time.Time) evidence.Claim {
	claim := evidence.Claim{
		Claim:         fmt.Sprintf("Gate passed for phase %q", p.Name),
		Level:         evidence.Untyped,
		Checker:       "",
		TrustBoundary: "No trust boundary declared",
		ArtifactPath:  artifactPath,
		Phase:         p.Name,
		Passed:        passed,
		Timestamp:     recordedAt.UTC(),
	}

	if p.Gate != nil {
		switch p.Gate.Type {
		case "live":
			claim.Level = evidence.ObservedInSitu
			if p.Gate.Live != nil {
				claim.Checker = "live/" + p.Gate.Live.Mode
			} else {
				claim.Checker = "live"
			}
			claim.TrustBoundary = "Running system observation"
		default:
			claim.Checker = p.Gate.Run
		}
	}
	if p.Gate == nil || p.Gate.Evidence == nil {
		return claim
	}

	if p.Gate.Evidence.Claim != "" {
		claim.Claim = p.Gate.Evidence.Claim
	}
	if p.Gate.Evidence.Level != "" {
		claim.Level = evidence.Level(p.Gate.Evidence.Level)
	}
	if p.Gate.Evidence.Checker != "" {
		claim.Checker = p.Gate.Evidence.Checker
	}
	if p.Gate.Evidence.TrustBoundary != "" {
		claim.TrustBoundary = p.Gate.Evidence.TrustBoundary
	}

	return claim
}

func phaseActionType(p *workflow.Phase) string {
	if p != nil && p.Type == "command" {
		return "external_command"
	}
	return "phase_execute"
}

func (r *Runner) enforcePhasePolicy(vessel queue.Vessel, p workflow.Phase, renderedCommand, renderedPrompt string) error {
	if r.Intermediary == nil {
		return nil
	}

	for _, intent := range r.phasePolicyIntents(vessel, p, renderedCommand, renderedPrompt) {
		result := r.Intermediary.Evaluate(intent)
		entry := intermediary.AuditEntry{
			Intent:    intent,
			Decision:  result.Effect,
			Timestamp: time.Now().UTC(),
		}
		if result.Effect != intermediary.Allow {
			entry.Error = result.Reason
		}
		if err := r.appendAuditEntry(entry); err != nil {
			return fmt.Errorf("record audit evidence: %w", err)
		}
		if result.Effect != intermediary.Allow {
			return formatPolicyDecisionError(p.Name, intent, result)
		}
	}
	return nil
}

func (r *Runner) phasePolicyIntents(vessel queue.Vessel, p workflow.Phase, renderedCommand, renderedPrompt string) []intermediary.Intent {
	baseAction := phaseActionType(&p)
	intents := []intermediary.Intent{{
		Action:        baseAction,
		Resource:      p.Name,
		AgentID:       vessel.ID,
		Justification: phaseIntentJustification(vessel, p.Name),
		Metadata:      phaseIntentMetadata(vessel, p, ""),
	}}

	classificationText := renderedPrompt
	classifiedFrom := "prompt"
	if p.Type == "command" {
		classificationText = renderedCommand
		classifiedFrom = "command"
	}
	if classificationText == "" {
		return intents
	}

	sourceRepo := strings.TrimSpace(r.resolveRepo(vessel))
	if sourceCfg := r.sourceConfigFromMeta(vessel); sourceCfg != nil && strings.TrimSpace(sourceCfg.Repo) != "" {
		sourceRepo = strings.TrimSpace(sourceCfg.Repo)
	}
	seen := map[string]struct{}{
		baseAction + "\x00" + p.Name: {},
	}

	appendIntent := func(action, resource string) {
		key := action + "\x00" + resource
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		intents = append(intents, intermediary.Intent{
			Action:        action,
			Resource:      resource,
			AgentID:       vessel.ID,
			Justification: fmt.Sprintf("phase %q may perform %s", p.Name, action),
			Metadata:      phaseIntentMetadata(vessel, p, classifiedFrom),
		})
	}

	if indicatesGitCommit(classificationText) {
		appendIntent("git_commit", "*")
	}
	if indicatesGitPush(classificationText) {
		appendIntent("git_push", extractGitPushResource(classificationText))
	}
	if indicatesPRCreate(classificationText) {
		appendIntent("pr_create", extractRepoFlag(classificationText, sourceRepo))
	}

	return intents
}

func phaseIntentJustification(vessel queue.Vessel, phaseName string) string {
	if vessel.Workflow != "" {
		return fmt.Sprintf("execute phase %q of workflow %q", phaseName, vessel.Workflow)
	}
	return fmt.Sprintf("execute workflow phase %q", phaseName)
}

func phaseIntentMetadata(vessel queue.Vessel, p workflow.Phase, classifiedFrom string) map[string]string {
	metadata := map[string]string{
		"phase":      p.Name,
		"source":     vessel.Source,
		"workflow":   vessel.Workflow,
		"phase_type": p.Type,
	}
	if metadata["phase_type"] == "" {
		metadata["phase_type"] = "prompt"
	}
	if classifiedFrom != "" {
		metadata["classified_from"] = classifiedFrom
	}
	return metadata
}

func formatPolicyDecisionError(phaseName string, intent intermediary.Intent, result intermediary.PolicyResult) error {
	qualifier := ""
	if intent.Action != "" && intent.Action != "phase_execute" && intent.Action != "external_command" {
		qualifier = " for " + intent.Action
		if intent.Resource != "" && intent.Resource != "*" {
			qualifier += " on " + intent.Resource
		}
	}

	switch result.Effect {
	case intermediary.RequireApproval:
		if result.Reason != "" {
			return fmt.Errorf("phase %q requires approval%s (automatic approval not yet supported): %s", phaseName, qualifier, result.Reason)
		}
		return fmt.Errorf("phase %q requires approval%s (automatic approval not yet supported)", phaseName, qualifier)
	case intermediary.Deny:
		if result.Reason != "" {
			return fmt.Errorf("phase %q denied by policy%s: %s", phaseName, qualifier, result.Reason)
		}
		return fmt.Errorf("phase %q denied by policy%s", phaseName, qualifier)
	default:
		if result.Reason != "" {
			return fmt.Errorf("phase %q denied by policy%s: %s", phaseName, qualifier, result.Reason)
		}
		return fmt.Errorf("phase %q denied by policy%s", phaseName, qualifier)
	}
}

func indicatesGitCommit(rendered string) bool {
	lower := strings.ToLower(rendered)
	return strings.Contains(lower, "git commit") ||
		strings.Contains(lower, "commit all changes") ||
		strings.Contains(lower, "commit the changes")
}

func indicatesGitPush(rendered string) bool {
	lower := strings.ToLower(rendered)
	return strings.Contains(lower, "git push") ||
		strings.Contains(lower, "push the branch") ||
		strings.Contains(lower, "push branch")
}

func indicatesPRCreate(rendered string) bool {
	lower := strings.ToLower(rendered)
	return strings.Contains(lower, "gh pr create") ||
		strings.Contains(lower, "create a pull request") ||
		strings.Contains(lower, "create the pull request") ||
		strings.Contains(lower, "create a pr")
}

func extractGitPushResource(rendered string) string {
	fields := strings.Fields(rendered)
	for i := 0; i < len(fields)-1; i++ {
		if !strings.EqualFold(fields[i], "git") || !strings.EqualFold(fields[i+1], "push") {
			continue
		}

		positional := make([]string, 0, 2)
		for _, raw := range fields[i+2:] {
			token := strings.Trim(strings.TrimSpace(raw), "\"'")
			switch token {
			case "", "&&", "||", ";", "|":
				return "*"
			}
			if strings.HasPrefix(token, "-") {
				continue
			}
			positional = append(positional, token)
			if len(positional) == 2 {
				return positional[1]
			}
		}
		return "*"
	}
	return "*"
}

func extractRepoFlag(rendered, fallback string) string {
	fields := strings.Fields(rendered)
	for i := 0; i < len(fields); i++ {
		token := strings.Trim(strings.TrimSpace(fields[i]), "\"'")
		if strings.HasPrefix(token, "--repo=") {
			if repo := strings.TrimPrefix(token, "--repo="); repo != "" {
				return repo
			}
		}
		if token == "--repo" && i+1 < len(fields) {
			if repo := strings.Trim(strings.TrimSpace(fields[i+1]), "\"'"); repo != "" {
				return repo
			}
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return "*"
}

func (r *Runner) takeProtectedSurfaceSnapshot(ctx context.Context, worktreePath string) (surface.Snapshot, bool, error) {
	patterns := r.Config.EffectiveProtectedSurfaces()
	if len(patterns) == 0 {
		return surface.Snapshot{}, false, nil
	}

	sourceRoot, err := r.protectedSurfaceSourceRoot(ctx, worktreePath)
	if err == nil {
		if _, restoreErr := restoreMissingProtectedSurfacesFromRoot(worktreePath, sourceRoot, patterns); restoreErr != nil {
			return surface.Snapshot{}, false, fmt.Errorf("restore missing protected surfaces: %w", restoreErr)
		}
	}

	snapshot, err := surface.TakeSnapshot(worktreePath, patterns)
	if err != nil {
		return surface.Snapshot{}, false, fmt.Errorf("take protected surface snapshot: %w", err)
	}
	return snapshot, true, nil
}

func (r *Runner) verifyProtectedSurfaces(vessel queue.Vessel, p workflow.Phase, worktreePath string, before surface.Snapshot) error {
	patterns := r.Config.EffectiveProtectedSurfaces()
	if len(patterns) == 0 {
		return nil
	}

	// Race guard: CheckHungVessels may remove the worktree synchronously
	// while this goroutine is still draining a stuck cmd.Wait() from a
	// killed subprocess whose grandchildren still hold the stdout/stderr
	// pipes open. If the worktree no longer exists, filepath.Glob inside
	// surface.TakeSnapshot returns no matches with no error, producing an
	// empty after-snapshot and a spurious "every protected file deleted"
	// violation. Skip the check in that case — there is nothing observable
	// left to verify. A legitimate in-worktree deletion of protected files
	// (worktree dir still present, .xylem/ subtree gone) is still caught.
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		log.Printf("%sphase %q surface check skipped: worktree %s no longer exists (cleanup race)",
			vesselLabel(vessel), p.Name, worktreePath)
		return nil
	}

	// Pre-verify restore: if the phase temporarily removed protected files
	// (e.g., resolve-conflicts workflow runs `gh pr checkout` on a PR branch
	// that predates the .xylem/ tracking commit (#157), then git switches
	// branches and drops tracked files that the target branch doesn't have),
	// restore them from the canonical source root before comparing the
	// after-snapshot. Only MISSING files are restored (those with
	// violation.After == "deleted" against the source root); modifications
	// are untouched and will still be caught by the Compare below.
	//
	// This closes the loop on issue #174: Fix B's post-phase self-heal was
	// correctly running but only AFTER the violation was recorded, so
	// vessels still failed. Restoring before the snapshot eliminates the
	// spurious "deleted" category while preserving all other enforcement.
	//
	// Uses context.Background() because the vessel's ctx may already be
	// cancelling (e.g., phase timeout) — cleanup work should survive.
	if sourceRoot, srcErr := r.protectedSurfaceSourceRoot(context.Background(), worktreePath); srcErr == nil {
		restored, restoreErr := restoreMissingProtectedSurfacesFromRoot(worktreePath, sourceRoot, patterns)
		if restoreErr != nil {
			log.Printf("%sphase %q pre-verify restore failed: %v",
				vesselLabel(vessel), p.Name, restoreErr)
		} else if restored > 0 {
			log.Printf("%sphase %q pre-verify restored %d protected surface file(s) from source root",
				vesselLabel(vessel), p.Name, restored)
		}
	}

	after, err := surface.TakeSnapshot(worktreePath, patterns)
	if err != nil {
		log.Printf("%sphase %q protected surface verification skipped: %v",
			vesselLabel(vessel), p.Name, err)
		return nil
	}

	violations := surface.Compare(before, after)
	policy, err := r.workflowProtectedWritePolicy(vessel, violations)
	if err != nil {
		return fmt.Errorf("resolve protected surface policy: %w", err)
	}
	violations = filterAdditiveProtectedSurfaceViolations(violations, policy.allowAdditive)
	violations = filterCanonicalProtectedSurfaceViolations(vessel, p, violations, policy.allowCanonical)

	// Source-root alignment filter: drop modification violations where the
	// after-hash matches the canonical source root's current hash for that
	// path. This handles the resolve-conflicts cascade where the phase runs
	// `git merge origin/main --no-commit` on a PR branch that predates the
	// .xylem/ control-plane tracking commit (#157). The merge brings the PR
	// branch's view of .xylem/ files INTO ALIGNMENT with main's canonical
	// state — exactly the outcome the control plane wants. Flagging this as
	// a violation blocks PR #143, PR #164, and any other PR cut from a
	// pre-#157 commit that needs a conflict resolution merge.
	//
	// Rationale: the protected-surface policy exists to prevent vessels
	// from DIVERGING the control plane from its canonical state. A
	// modification that CONVERGES to the canonical state is the opposite —
	// it's the intended normalization. We suppress it and log for audit.
	//
	// This does NOT mask legitimate violations where the agent modifies a
	// file to some other content (neither the branch's original hash nor
	// the canonical source root's hash): those still raise violations.
	//
	// GUARD: we only apply the filter when the source root is DIFFERENT
	// from the worktree path. If they're the same (as in tests where
	// `git rev-parse --git-common-dir` fails and the helper falls back to
	// returning worktreePath), the source snapshot would be the same as
	// the after snapshot — every modification would "align" and every
	// violation would be suppressed. The existing SuppressesTransient /
	// StillCatchesMutation tests would break. Requiring a distinct source
	// root ensures we only suppress real merge-alignment cases.
	//
	// Source snapshot uses context.Background() because the vessel's ctx
	// may be cancelling; the snapshot is a fast read-only filesystem walk.
	if sourceRoot, srcErr := r.protectedSurfaceSourceRoot(context.Background(), worktreePath); srcErr == nil && sourceRoot != worktreePath {
		if sourceSnapshot, sErr := surface.TakeSnapshot(sourceRoot, patterns); sErr == nil {
			violations = filterViolationsAlignedWithSourceRoot(vessel, p, violations, sourceSnapshot)
		}
	}

	if len(violations) == 0 {
		return nil
	}

	errMsg := fmt.Sprintf("phase %q violated protected surfaces: %s", p.Name, formatViolations(violations))
	if err := r.recordProtectedSurfaceViolations(vessel, p, errMsg, violations); err != nil {
		return fmt.Errorf("%s (record audit evidence: %w)", errMsg, err)
	}
	if err := r.restoreDeletedProtectedSurfaces(context.Background(), worktreePath, violations); err != nil {
		log.Printf("%sphase %q protected surface self-heal failed: %v", vesselLabel(vessel), p.Name, err)
	}
	return fmt.Errorf("%s", errMsg)
}

type protectedSurfaceWorkflowPolicy struct {
	allowAdditive  bool
	allowCanonical bool
}

func (r *Runner) workflowProtectedWritePolicy(vessel queue.Vessel, violations []surface.Violation) (protectedSurfaceWorkflowPolicy, error) {
	needsAdditivePolicy := containsAdditiveProtectedSurfaceViolation(violations)
	needsCanonicalPolicy := containsCanonicalProtectedSurfaceModification(violations) &&
		strings.TrimSpace(vessel.Meta["issue_body"]) != ""
	if !needsAdditivePolicy && !needsCanonicalPolicy {
		return protectedSurfaceWorkflowPolicy{}, nil
	}
	if strings.TrimSpace(vessel.Workflow) == "" {
		return protectedSurfaceWorkflowPolicy{}, nil
	}

	sk, err := r.loadWorkflow(vessel.Workflow)
	if err != nil {
		return protectedSurfaceWorkflowPolicy{}, fmt.Errorf("load workflow %q: %w", vessel.Workflow, err)
	}
	return protectedSurfaceWorkflowPolicy{
		allowAdditive:  sk.AllowAdditiveProtectedWrites,
		allowCanonical: sk.AllowCanonicalProtectedWrites,
	}, nil
}

func containsAdditiveProtectedSurfaceViolation(violations []surface.Violation) bool {
	for _, violation := range violations {
		if violation.Before == "absent" {
			return true
		}
	}
	return false
}

func containsCanonicalProtectedSurfaceModification(violations []surface.Violation) bool {
	for _, violation := range violations {
		if isProtectedSurfaceModification(violation) {
			return true
		}
	}
	return false
}

func filterAdditiveProtectedSurfaceViolations(violations []surface.Violation, allowAdditive bool) []surface.Violation {
	if !allowAdditive || len(violations) == 0 {
		return violations
	}

	filtered := make([]surface.Violation, 0, len(violations))
	for _, violation := range violations {
		if violation.Before == "absent" {
			continue
		}
		filtered = append(filtered, violation)
	}
	return filtered
}

func filterCanonicalProtectedSurfaceViolations(vessel queue.Vessel, p workflow.Phase, violations []surface.Violation, allowCanonical bool) []surface.Violation {
	if !allowCanonical || len(violations) == 0 {
		return violations
	}

	issueBody := strings.TrimSpace(vessel.Meta["issue_body"])
	if issueBody == "" {
		return violations
	}

	filtered := make([]surface.Violation, 0, len(violations))
	for _, violation := range violations {
		if !isProtectedSurfaceModification(violation) || !issueBodyMentionsProtectedPath(issueBody, violation.Path) {
			filtered = append(filtered, violation)
			continue
		}
		log.Printf("%sphase %q: suppressing canonical protected-surface violation on %s (workflow opt-in + issue body reference)",
			vesselLabel(vessel), p.Name, violation.Path)
	}
	return filtered
}

func isProtectedSurfaceModification(violation surface.Violation) bool {
	return violation.Before != "absent" && violation.After != "deleted"
}

func issueBodyMentionsProtectedPath(issueBody, path string) bool {
	normalizedPath := filepath.ToSlash(strings.TrimSpace(path))
	if normalizedPath == "" {
		return false
	}
	re := regexp.MustCompile(`(^|[^A-Za-z0-9._/-])` + regexp.QuoteMeta(normalizedPath) + `([^A-Za-z0-9._/-]|$)`)
	return re.FindStringIndex(issueBody) != nil
}

// filterViolationsAlignedWithSourceRoot drops modification violations whose
// after-hash exactly matches the canonical source root's current hash for
// that path. See the extended comment at the call site in verifyProtectedSurfaces
// for rationale.
//
// A violation is suppressed if ALL of:
//   - it has a real before-hash (not "absent" — those are additions, handled
//     separately by filterAdditiveProtectedSurfaceViolations)
//   - it has a real after-hash (not "deleted" — those are handled by the
//     post-phase self-heal and pre-verify restore code paths)
//   - the path exists in the source snapshot
//   - the after-hash exactly equals the source snapshot's hash for that path
//
// All other violations pass through unchanged, preserving detection of
// rogue modifications and any deletion/addition patterns that weren't
// already covered upstream.
func filterViolationsAlignedWithSourceRoot(vessel queue.Vessel, p workflow.Phase, violations []surface.Violation, sourceSnapshot surface.Snapshot) []surface.Violation {
	if len(violations) == 0 {
		return violations
	}
	sourceHashByPath := make(map[string]string, len(sourceSnapshot.Files))
	for _, f := range sourceSnapshot.Files {
		sourceHashByPath[f.Path] = f.Hash
	}
	filtered := make([]surface.Violation, 0, len(violations))
	for _, v := range violations {
		if v.Before == "absent" || v.After == "deleted" {
			// Not a modification — pass through (additions and deletions
			// are handled by other filters / self-heal paths).
			filtered = append(filtered, v)
			continue
		}
		sourceHash, ok := sourceHashByPath[v.Path]
		if !ok {
			// Path not tracked in source root — can't determine alignment,
			// preserve as violation to be safe.
			filtered = append(filtered, v)
			continue
		}
		if v.After == sourceHash {
			// Modification brings file INTO ALIGNMENT with canonical source.
			// Suppress and log for audit visibility.
			log.Printf("%sphase %q: suppressing alignment violation on %s (after-hash matches source root)",
				vesselLabel(vessel), p.Name, v.Path)
			continue
		}
		filtered = append(filtered, v)
	}
	return filtered
}

func (r *Runner) protectedSurfaceSourceRoot(ctx context.Context, worktreePath string) (string, error) {
	out, err := r.Runner.RunOutput(ctx, "git", "-C", worktreePath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return worktreePath, nil
	}

	commonDir := strings.TrimSpace(string(out))
	if commonDir == "" {
		return worktreePath, nil
	}
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir), nil
	}
	return worktreePath, nil
}

func restoreMissingProtectedSurfacesFromRoot(worktreePath, sourceRoot string, patterns []string) (int, error) {
	if len(patterns) == 0 {
		return 0, nil
	}

	sourceSnapshot, err := surface.TakeSnapshot(sourceRoot, patterns)
	if err != nil {
		return 0, fmt.Errorf("take source protected surface snapshot: %w", err)
	}
	worktreeSnapshot, err := surface.TakeSnapshot(worktreePath, patterns)
	if err != nil {
		return 0, fmt.Errorf("take worktree protected surface snapshot: %w", err)
	}

	restored := 0
	for _, violation := range surface.Compare(sourceSnapshot, worktreeSnapshot) {
		if violation.After != "deleted" {
			continue
		}
		if err := copyProtectedSurfaceFile(sourceRoot, worktreePath, violation.Path); err != nil {
			return restored, fmt.Errorf("restore %s from source root: %w", violation.Path, err)
		}
		restored++
	}

	return restored, nil
}

func copyProtectedSurfaceFile(sourceRoot, worktreePath, relPath string) error {
	srcPath := filepath.Join(sourceRoot, filepath.FromSlash(relPath))
	dstPath := filepath.Join(worktreePath, filepath.FromSlash(relPath))

	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat source file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent dir: %w", err)
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source file: %w", err)
	}
	// Idempotency: a prior call may have left the destination chmod'd
	// 0o444 (read-only). Make it writable before os.WriteFile so repeated
	// restores across multiple pre-verify cycles succeed.
	if dstInfo, statErr := os.Stat(dstPath); statErr == nil && dstInfo.Mode().Perm()&0o200 == 0 {
		if chmodErr := os.Chmod(dstPath, 0o644); chmodErr != nil {
			return fmt.Errorf("chmod writable for rewrite: %w", chmodErr)
		}
	}
	if err := os.WriteFile(dstPath, data, info.Mode()); err != nil {
		return fmt.Errorf("write restored file: %w", err)
	}
	if err := os.Chmod(dstPath, 0o444); err != nil {
		return fmt.Errorf("mark restored file read-only: %w", err)
	}
	// Add the restored path to the worktree's .git/info/exclude so that
	// subsequent `git add -A` (e.g., in resolve-conflicts's push phase) does
	// NOT stage the restored file into the PR commit. This only affects
	// untracked files; a file that's already tracked on the PR branch is
	// unaffected by exclude entries.
	//
	// Fail-soft: exclude is a best-effort pollution guard, not a safety
	// invariant. If it fails (e.g., .git dir missing in a test or some
	// corner case), the file restore still succeeds — worst case a later
	// `git add -A` stages the restored file into a PR commit, which is no
	// worse than the pre-fix behavior of failing the vessel outright.
	if err := addWorktreeExcludeEntry(worktreePath, relPath); err != nil {
		log.Printf("warn: copyProtectedSurfaceFile: add exclude entry for %s: %v", relPath, err)
	}
	return nil
}

// addWorktreeExcludeEntry appends a path to the worktree's .git/info/exclude
// file if not already present. For linked worktrees (the xylem vessel case),
// $GIT_DIR points to .git/worktrees/<name>, and info/exclude there is
// per-worktree — it does NOT affect sibling worktrees.
//
// Idempotent: a second call with the same relPath no-ops because the exact
// line is already present.
func addWorktreeExcludeEntry(worktreePath, relPath string) error {
	gitdir, err := resolveWorktreeGitdir(worktreePath)
	if err != nil {
		return fmt.Errorf("resolve gitdir: %w", err)
	}
	excludePath := filepath.Join(gitdir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("mkdir info: %w", err)
	}

	// Use a leading slash so the pattern anchors at the worktree root,
	// matching exactly this file (not any subdirectory with the same name).
	line := "/" + relPath

	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read exclude: %w", err)
	}
	for _, e := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(e) == line {
			return nil
		}
	}

	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open exclude: %w", err)
	}
	defer f.Close()

	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	if _, err := f.WriteString(prefix + line + "\n"); err != nil {
		return fmt.Errorf("write exclude: %w", err)
	}
	return nil
}

// resolveWorktreeGitdir returns the $GIT_DIR for a worktree. For the main
// repo, .git is a directory and that IS the gitdir. For linked worktrees,
// .git is a file containing "gitdir: <absolute-or-relative-path>".
func resolveWorktreeGitdir(worktreePath string) (string, error) {
	gitPath := filepath.Join(worktreePath, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return gitPath, nil
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("unexpected .git file content: %q", line)
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(worktreePath, gitdir)
	}
	return gitdir, nil
}

func (r *Runner) restoreDeletedProtectedSurfaces(ctx context.Context, worktreePath string, violations []surface.Violation) error {
	var errs []error
	defaultBranch := ""

	for _, violation := range violations {
		if violation.After != "deleted" {
			continue
		}
		if err := r.restoreProtectedSurfacePath(ctx, worktreePath, violation.Path, &defaultBranch); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", violation.Path, err))
		}
	}

	return errors.Join(errs...)
}

func (r *Runner) restoreProtectedSurfacePath(ctx context.Context, worktreePath, relPath string, defaultBranch *string) error {
	if _, err := r.Runner.RunOutput(ctx, "git", "-C", worktreePath, "checkout", "--", relPath); err == nil {
		return markProtectedSurfaceReadOnly(worktreePath, relPath)
	}

	if defaultBranch != nil && *defaultBranch == "" {
		branch, err := r.detectDefaultBranchAtPath(ctx, worktreePath)
		if err != nil {
			return fmt.Errorf("detect default branch: %w", err)
		}
		*defaultBranch = branch
	}

	if defaultBranch == nil || *defaultBranch == "" {
		return fmt.Errorf("default branch unavailable for restore")
	}

	if _, err := r.Runner.RunOutput(ctx, "git", "-C", worktreePath, "checkout", "origin/"+*defaultBranch, "--", relPath); err != nil {
		return fmt.Errorf("checkout origin/%s -- %s: %w", *defaultBranch, relPath, err)
	}
	return markProtectedSurfaceReadOnly(worktreePath, relPath)
}

func markProtectedSurfaceReadOnly(worktreePath, relPath string) error {
	if err := os.Chmod(filepath.Join(worktreePath, filepath.FromSlash(relPath)), 0o444); err != nil {
		return fmt.Errorf("chmod restored file: %w", err)
	}
	return nil
}

func (r *Runner) detectDefaultBranchAtPath(ctx context.Context, worktreePath string) (string, error) {
	out, err := r.Runner.RunOutput(ctx, "git", "-C", worktreePath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(string(out))
		if branch := strings.TrimPrefix(ref, "refs/remotes/origin/"); branch != ref && branch != "" {
			return branch, nil
		}
	}

	out, err = r.Runner.RunOutput(ctx, "git", "-C", worktreePath, "remote", "show", "origin")
	if err != nil {
		return "", fmt.Errorf("git remote show origin: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "HEAD branch:") {
			branch := strings.TrimSpace(strings.TrimPrefix(line, "HEAD branch:"))
			if branch != "" {
				return branch, nil
			}
		}
	}
	return "", fmt.Errorf("could not detect default branch from origin")
}

func (r *Runner) recordProtectedSurfaceViolations(vessel queue.Vessel, p workflow.Phase, errMsg string, violations []surface.Violation) error {
	for _, violation := range violations {
		if err := r.appendAuditEntry(intermediary.AuditEntry{
			Intent: intermediary.Intent{
				Action:        "file_write",
				Resource:      violation.Path,
				AgentID:       vessel.ID,
				Justification: fmt.Sprintf("verify protected surfaces after phase %q", p.Name),
				Metadata: map[string]string{
					"phase":    p.Name,
					"before":   violation.Before,
					"after":    violation.After,
					"source":   vessel.Source,
					"workflow": vessel.Workflow,
				},
			},
			Decision:  intermediary.Deny,
			Timestamp: time.Now().UTC(),
			Error:     errMsg,
		}); err != nil {
			return fmt.Errorf("append surface violation audit for %s: %w", violation.Path, err)
		}
	}
	return nil
}

func (r *Runner) appendAuditEntry(entry intermediary.AuditEntry) error {
	if r.AuditLog == nil {
		return nil
	}
	if err := r.AuditLog.Append(entry); err != nil {
		return fmt.Errorf("append audit entry: %w", err)
	}
	return nil
}

func formatViolations(violations []surface.Violation) string {
	parts := make([]string, 0, len(violations))
	for _, violation := range violations {
		parts = append(parts, fmt.Sprintf("%s (before: %s, after: %s)", violation.Path, violation.Before, violation.After))
	}
	return strings.Join(parts, "; ")
}

func (r *Runner) logReporterError(action string, vesselID string, err error) {
	if err != nil {
		log.Printf("warn: %s for vessel %s: %v", action, vesselID, err)
	}
}

// sourceConfigFromMeta returns the SourceConfig for a vessel by looking up
// the config source name stored in vessel Meta at scan time.
func (r *Runner) sourceConfigFromMeta(v queue.Vessel) *config.SourceConfig {
	name := r.sourceConfigNameFromMeta(v)
	if r.Config == nil || name == "" {
		return nil
	}
	if sc, ok := r.Config.Sources[name]; ok {
		return &sc
	}
	return nil
}

func (r *Runner) sourceConfigNameFromMeta(v queue.Vessel) string {
	if v.Meta == nil {
		return ""
	}
	return v.Meta["config_source"]
}

func (r *Runner) loadWorkflow(name string) (*workflow.Workflow, error) {
	path := filepath.Join(".xylem", "workflows", name+".yaml")
	return workflow.Load(path)
}

func (r *Runner) readHarness() string {
	data, err := os.ReadFile(filepath.Join(".xylem", "HARNESS.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

func (r *Runner) fetchIssueData(ctx context.Context, vessel *queue.Vessel) phase.IssueData {
	switch vessel.Source {
	case "github-issue":
		return r.fetchGitHubData(ctx, vessel, "issue", "issue")
	case "github-pr", "github-pr-events", "github-merge":
		return r.fetchGitHubData(ctx, vessel, "pr", "pr")
	default:
		return phase.IssueData{}
	}
}

// fetchGitHubData fetches title/body/labels for an issue or PR.
// ghCmd is the gh subcommand ("issue" or "pr"), metaPrefix is the Meta key prefix ("issue" or "pr").
func (r *Runner) fetchGitHubData(ctx context.Context, vessel *queue.Vessel, ghCmd, metaPrefix string) phase.IssueData {
	data := phase.IssueData{}

	num := r.parseIssueNum(*vessel)
	if num == 0 {
		return data
	}

	titleKey := metaPrefix + "_title"
	bodyKey := metaPrefix + "_body"
	labelsKey := metaPrefix + "_labels"

	// Check if already cached in Meta
	if vessel.Meta != nil && vessel.Meta[titleKey] != "" {
		data.Number = num
		data.Title = vessel.Meta[titleKey]
		data.Body = vessel.Meta[bodyKey]
		data.URL = vessel.Ref
		if labelsStr, ok := vessel.Meta[labelsKey]; ok {
			data.Labels = strings.Split(labelsStr, ",")
		}
		return data
	}

	repo := r.resolveRepo(*vessel)
	if repo == "" {
		return data
	}

	out, err := r.Runner.RunOutput(ctx, "gh", ghCmd, "view",
		fmt.Sprintf("%d", num),
		"--repo", repo,
		"--json", "title,body,labels,url",
	)
	if err != nil {
		log.Printf("warn: fetch %s data for vessel %s: %v", ghCmd, vessel.ID, err)
		return data
	}

	var resp struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		URL    string `json:"url"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		log.Printf("warn: parse %s data for vessel %s: %v", ghCmd, vessel.ID, err)
		return data
	}

	data.Number = num
	data.Title = resp.Title
	data.Body = resp.Body
	data.URL = resp.URL
	for _, l := range resp.Labels {
		data.Labels = append(data.Labels, l.Name)
	}

	// Cache in Meta
	if vessel.Meta == nil {
		vessel.Meta = make(map[string]string)
	}
	vessel.Meta[titleKey] = resp.Title
	vessel.Meta[bodyKey] = resp.Body
	vessel.Meta[labelsKey] = strings.Join(data.Labels, ",")

	return data
}

func (r *Runner) rebuildPreviousOutputs(vesselID string, sk *workflow.Workflow) map[string]string {
	outputs := make(map[string]string)
	phasesDir := filepath.Join(r.Config.StateDir, "phases", vesselID)
	for _, p := range sk.Phases {
		outputPath := filepath.Join(phasesDir, p.Name+".output")
		data, err := os.ReadFile(outputPath)
		if err == nil {
			outputs[p.Name] = string(data)
		}
	}
	return outputs
}

func (r *Runner) parseIssueNum(vessel queue.Vessel) int {
	if vessel.Meta == nil {
		return 0
	}
	numStr, ok := vessel.Meta["issue_num"]
	if !ok {
		numStr, ok = vessel.Meta["pr_num"]
		if !ok {
			return 0
		}
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0
	}
	return n
}

func (r *Runner) resolveRepo(vessel queue.Vessel) string {
	if sourceCfg := r.sourceConfigFromMeta(vessel); sourceCfg != nil && strings.TrimSpace(sourceCfg.Repo) != "" {
		return strings.TrimSpace(sourceCfg.Repo)
	}
	src := r.resolveSourceForVessel(vessel)
	switch s := src.(type) {
	case *source.GitHub:
		return s.Repo
	case *source.GitHubPR:
		return s.Repo
	case *source.GitHubPREvents:
		return s.Repo
	case *source.GitHubMerge:
		return s.Repo
	case *source.Scheduled:
		return s.Repo
	default:
		return ""
	}
}

func vesselLabel(v queue.Vessel) string {
	if v.Meta != nil {
		if title := v.Meta["issue_title"]; title != "" {
			return fmt.Sprintf("[%s] ", title)
		}
		if title := v.Meta["pr_title"]; title != "" {
			return fmt.Sprintf("[%s] ", title)
		}
	}
	return fmt.Sprintf("[%s] ", v.ID)
}

func renderCommandTemplate(phaseName, kind, command string, td phase.TemplateData) (string, error) {
	rendered, err := phase.RenderPrompt(command, td)
	if err != nil {
		return "", fmt.Errorf("render %s for phase %s: %w", kind, phaseName, err)
	}
	if err := validateCommandRender(fmt.Sprintf("%s for phase %s", kind, phaseName), rendered); err != nil {
		return "", err
	}
	return rendered, nil
}

// validateCommandRender checks the rendered command string for unresolved
// template variables (leftover "{{" sequences). This catches cases where
// template data has zero values that cause confusing downstream failures.
func validateCommandRender(subject, rendered string) error {
	if strings.Contains(rendered, "{{") {
		return fmt.Errorf("%s: unresolved template variable in: %s", subject, rendered)
	}
	return nil
}

// validateIssueDataForWorkflow returns an error when the workflow contains
// command phases or command gates that reference .Issue template variables but
// the issue data has a zero Number. This prevents commands like
// `gh pr merge 0` from executing with confusing results.
func validateIssueDataForWorkflow(vessel queue.Vessel, data phase.IssueData, wf *workflow.Workflow) error {
	if wf == nil {
		return nil
	}
	for _, p := range wf.Phases {
		if p.Type == "command" && strings.Contains(p.Run, ".Issue.") && data.Number == 0 {
			return fmt.Errorf("command phase %s references .Issue but issue data is unavailable for vessel %s (Number is 0)", p.Name, vessel.ID)
		}
		if p.Gate != nil && p.Gate.Type == "command" && strings.Contains(p.Gate.Run, ".Issue.") && data.Number == 0 {
			return fmt.Errorf("command gate for phase %s references .Issue but issue data is unavailable for vessel %s (Number is 0)", p.Name, vessel.ID)
		}
	}
	return nil
}

// CheckHungVessels checks all running vessels for timeout. Vessels that have
// been running longer than the configured timeout are transitioned to timed_out
// and their worktrees are cleaned up.
func (r *Runner) CheckHungVessels(ctx context.Context) {
	timeout, err := time.ParseDuration(r.Config.Timeout)
	if err != nil {
		log.Printf("warn: parse timeout for hung vessel check: %v", err)
		return
	}

	running, err := r.Queue.ListByState(queue.StateRunning)
	if err != nil {
		log.Printf("warn: list running vessels: %v", err)
		return
	}

	for _, vessel := range running {
		if vessel.StartedAt == nil {
			continue
		}
		srcCfg := r.sourceConfigFromMeta(vessel)
		vesselTimeout, resolveErr := resolveTimeout(r.Config, srcCfg)
		if resolveErr != nil {
			log.Printf("warn: resolve timeout for vessel %s (config_source=%q): %v; using global timeout %s", vessel.ID, r.sourceConfigNameFromMeta(vessel), resolveErr, timeout)
			vesselTimeout = timeout
		}
		elapsed := r.runtimeSince(*vessel.StartedAt)
		if elapsed <= vesselTimeout {
			continue
		}

		errMsg := fmt.Sprintf("vessel timed out after %s", elapsed.Truncate(time.Second))
		log.Printf("warn: %s for vessel %s", errMsg, vessel.ID)

		if updateErr := r.Queue.Update(vessel.ID, queue.StateTimedOut, errMsg); updateErr != nil {
			log.Printf("warn: failed to update vessel %s to timed_out: %v", vessel.ID, updateErr)
			continue
		}

		timeoutSpan := r.startWaitTransitionSpan(ctx, vessel, "timed_out", elapsed)
		vrs := newVesselRunState(r.Config, vessel, r.runtimeNow())
		vrs.setTraceContext(observability.TraceContextFromContext(timeoutSpan.Context()))
		r.persistRunArtifacts(vessel, string(queue.StateTimedOut), vrs, nil, r.runtimeNow())
		r.finishWaitTransitionSpan(timeoutSpan, nil)

		// Clean up worktree (best-effort)
		r.removeWorktree(ctx, vessel.WorktreePath, vessel.ID)
	}
}

func (r *Runner) runtimeNow() time.Time {
	now, err := dtu.RuntimeNow()
	if err != nil {
		log.Printf("warn: runner: resolve runtime clock: %v", err)
		return time.Now().UTC()
	}
	return now.UTC()
}

func (r *Runner) inspectVesselStatus(vessel queue.Vessel) VesselStatusReport {
	summary, err := LoadVesselSummary(r.Config.StateDir, vessel.ID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("warn: load vessel summary for %s: %v", vessel.ID, err)
		}
		return AnalyzeVesselStatus(vessel, nil)
	}
	return AnalyzeVesselStatus(vessel, summary)
}

func (r *Runner) runtimeSince(start time.Time) time.Duration {
	elapsed, err := dtu.RuntimeSince(start)
	if err != nil {
		log.Printf("warn: runner: resolve runtime elapsed time: %v", err)
		return time.Since(start)
	}
	return elapsed
}

func (r *Runner) runtimeSleep(ctx context.Context, delay time.Duration) error {
	if err := dtu.RuntimeSleep(ctx, delay); err != nil {
		log.Printf("warn: runner: runtime sleep: %v", err)
		return err
	}
	return nil
}

const (
	rateLimitMaxRetries  = 3
	rateLimitBaseBackoff = 30 * time.Second
)

// isRateLimitError reports whether the error indicates a transient LLM
// provider error that should be retried with backoff. This includes HTTP 429
// rate limits (rate_limit_error), insufficient credit balance, and generic
// "too many requests" errors from both Anthropic and Copilot providers.
//
// The function name is retained for backwards compatibility; "transient" is
// the more accurate description of what it now detects.
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Anthropic: rate_limit_error, "Credit balance is too low"
	// Copilot/OpenAI: "rate limit", "insufficient_quota"
	// Generic: "too many requests", HTTP 429 status ("429 too many", "api error: 429")
	//
	// Deliberately NOT matching bare "429" — it appears in unrelated contexts
	// like "processed 429 items". The patterns below require context.
	transientPatterns := []string{
		"rate_limit_error",
		"rate limit",
		"rate-limit",
		"credit balance is too low",
		"insufficient_quota",
		"insufficient credit",
		"too many requests",
		"api error: 429",
		"status 429",
		"429 too many",
	}
	for _, p := range transientPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// runPhaseWithRateLimitRetry wraps RunPhase with retry logic for API rate limit
// errors (HTTP 429). It retries up to rateLimitMaxRetries times with exponential
// backoff (30s, 60s, 120s) before returning the final error.
// stdinContent is re-wrapped in a fresh strings.Reader for each attempt;
// pass "" for nil stdin.
func (r *Runner) runPhaseWithRateLimitRetry(
	ctx context.Context, dir, stdinContent, cmd string, args []string,
) ([]byte, error) {
	for attempt := 0; attempt <= rateLimitMaxRetries; attempt++ {
		var stdin io.Reader
		if stdinContent != "" {
			stdin = strings.NewReader(stdinContent)
		}
		output, err := r.Runner.RunPhase(ctx, dir, stdin, cmd, args...)
		if err == nil || !isRateLimitError(err) {
			return output, err
		}
		if attempt == rateLimitMaxRetries {
			return output, err
		}
		backoff := rateLimitBaseBackoff * time.Duration(1<<uint(attempt))
		log.Printf("rate limit error (attempt %d/%d), retrying after %v: %v",
			attempt+1, rateLimitMaxRetries+1, backoff, err)
		if sleepErr := r.runtimeSleep(ctx, backoff); sleepErr != nil {
			return nil, sleepErr
		}
	}
	// unreachable, but satisfies the compiler
	return nil, nil
}

// buildPhaseArgs constructs the claude CLI arguments for a phase invocation.
// Model resolution follows the hierarchy: Phase.Model > Workflow.Model > Source.Model > ClaudeConfig.DefaultModel.
// When a model is resolved from the hierarchy, any --model flag in Claude.Flags is stripped to avoid duplication.
func buildPhaseArgs(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, harnessContent string) []string {
	args := []string{"-p"}
	args = append(args, "--max-turns", fmt.Sprintf("%d", p.MaxTurns))

	model := resolveModel(cfg, srcCfg, wf, p, "claude")

	// Add flags, stripping --model if we resolved one from the hierarchy
	if cfg.Claude.Flags != "" {
		fields := strings.Fields(cfg.Claude.Flags)
		if model != "" {
			fields = stripModelFlag(fields)
		}
		args = append(args, fields...)
	}

	if model != "" {
		args = append(args, "--model", model)
	}

	// Claude CLI uses --allowedTools (camelCase), unlike Copilot's --allowed-tools (kebab-case)
	if p.AllowedTools != nil && *p.AllowedTools != "" {
		args = append(args, "--allowedTools", *p.AllowedTools)
	}

	for _, tool := range cfg.Claude.AllowedTools {
		args = append(args, "--allowedTools", tool)
	}

	// Claude CLI uses --append-system-prompt, unlike Copilot's --system-prompt
	if harnessContent != "" {
		args = append(args, "--append-system-prompt", harnessContent)
	}

	return args
}

// buildCopilotPhaseArgs constructs the GitHub Copilot CLI arguments for a phase invocation.
// Model resolution follows the hierarchy: Phase.Model > Workflow.Model > Source.Model > CopilotConfig.DefaultModel.
// The rendered prompt and harness content are combined into the -p flag value because
// copilot has no --system-prompt equivalent.
func buildCopilotPhaseArgs(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, harnessContent, renderedPrompt string) []string {
	// Combine harness + prompt into a single prompt text for -p.
	// Copilot has no --system-prompt or --append-system-prompt flag.
	promptText := renderedPrompt
	if harnessContent != "" {
		promptText = harnessContent + "\n\n" + renderedPrompt
	}

	args := []string{"-p", promptText, "-s"}

	model := resolveModel(cfg, srcCfg, wf, p, "copilot")

	// Add user flags, stripping flags we always prepend to avoid duplication.
	// -p/--prompt is value-aware (strips flag + its value); -s/--headless are boolean.
	if cfg.Copilot.Flags != "" {
		fields := strings.Fields(cfg.Copilot.Flags)
		fields = stripPromptFlag(fields)
		fields = stripBoolFlag(fields, "-s")
		fields = stripBoolFlag(fields, "--silent")
		fields = stripBoolFlag(fields, "--headless") // legacy: was never valid, strip if present
		if model != "" {
			fields = stripModelFlag(fields)
		}
		args = append(args, fields...)
	}

	if model != "" {
		args = append(args, "--model", model)
	}

	// Copilot uses --available-tools to restrict which tools are visible,
	// plus --allow-all-tools to auto-approve them for non-interactive mode.
	if p.AllowedTools != nil && *p.AllowedTools != "" {
		args = append(args, "--available-tools", *p.AllowedTools, "--allow-all-tools")
	}

	return args
}

// buildProviderPhaseArgs dispatches to the correct arg builder based on the resolved provider,
// and returns the command binary, argument slice, and stdin reader for the phase invocation.
// For providers that embed the prompt in CLI args (copilot -p <text>), stdin is nil.
// For providers that read the prompt from stdin (claude -p), stdin carries the prompt.
func buildProviderPhaseArgs(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, harnessContent, provider, renderedPrompt string, attempt int) (string, []string, io.Reader) {
	switch provider {
	case "copilot":
		return cfg.Copilot.Command, appendDTUProviderArgs(buildCopilotPhaseArgs(cfg, srcCfg, wf, p, harnessContent, renderedPrompt), p, attempt), nil
	default: // "claude"
		var stdin io.Reader
		if renderedPrompt != "" {
			stdin = strings.NewReader(renderedPrompt)
		}
		return cfg.Claude.Command, appendDTUProviderArgs(buildPhaseArgs(cfg, srcCfg, wf, p, harnessContent), p, attempt), stdin
	}
}

func appendDTUProviderArgs(args []string, p *workflow.Phase, attempt int) []string {
	if !isDTUProviderRun() {
		return args
	}
	phaseName := ""
	scriptName := ""
	if p != nil {
		phaseName = p.Name
		if strings.TrimSpace(p.PromptFile) != "" {
			scriptName = strings.TrimSuffix(filepath.Base(p.PromptFile), filepath.Ext(p.PromptFile))
		}
	}
	out := append([]string(nil), args...)
	if phaseName != "" {
		out = append(out, "--dtu-phase", phaseName)
	}
	if scriptName != "" {
		out = append(out, "--dtu-script", scriptName)
	}
	if attempt > 0 {
		out = append(out, "--dtu-attempt", strconv.Itoa(attempt))
	}
	return out
}

func providerAttempt(p *workflow.Phase, retriesRemaining int) int {
	if p == nil || p.Gate == nil || p.Gate.Type != "command" || p.Gate.Retries <= 0 {
		return 1
	}
	attempt := p.Gate.Retries - retriesRemaining + 1
	if attempt < 1 {
		return 1
	}
	return attempt
}

func isDTUProviderRun() bool {
	return strings.TrimSpace(os.Getenv(dtu.EnvStatePath)) != ""
}

// resolveTimeout returns the effective timeout for a vessel.
// Resolution order: Source.Timeout > Config.Timeout.
func resolveTimeout(cfg *config.Config, srcCfg *config.SourceConfig) (time.Duration, error) {
	raw := cfg.Timeout
	if srcCfg != nil && srcCfg.Timeout != "" {
		raw = srcCfg.Timeout
	}
	return time.ParseDuration(raw)
}

// resolveProvider determines which LLM provider to use for a phase invocation.
// Resolution order: Phase.LLM > Workflow.LLM > Source.LLM > Config.LLM, defaulting to "claude".
func resolveProvider(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase) string {
	if p != nil && p.LLM != nil && *p.LLM != "" {
		return *p.LLM
	}
	if wf != nil && wf.LLM != nil && *wf.LLM != "" {
		return *wf.LLM
	}
	if srcCfg != nil && srcCfg.LLM != "" {
		return srcCfg.LLM
	}
	if cfg != nil && cfg.LLM != "" {
		return cfg.LLM
	}
	return "claude"
}

// resolveModel determines the model string for a phase invocation.
// Resolution order: Phase.Model > Workflow.Model > Source.Model > provider's DefaultModel.
// The provider's DefaultModel ensures the fallback is always compatible with the
// resolved provider, avoiding cross-provider model leaks (e.g. gpt-* sent to claude).
func resolveModel(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, provider string) string {
	if p != nil && p.Model != nil && *p.Model != "" {
		return *p.Model
	}
	if wf != nil && wf.Model != nil && *wf.Model != "" {
		return *wf.Model
	}
	if srcCfg != nil && srcCfg.Model != "" {
		return srcCfg.Model
	}
	if cfg == nil {
		return ""
	}
	switch provider {
	case "copilot":
		return cfg.Copilot.DefaultModel
	default:
		return cfg.Claude.DefaultModel
	}
}

// stripBoolFlag removes all occurrences of a boolean flag (no value) from a slice of CLI tokens.
func stripBoolFlag(fields []string, flag string) []string {
	var out []string
	for _, f := range fields {
		if f != flag {
			out = append(out, f)
		}
	}
	return out
}

// buildPromptOnlyCmdArgs returns the command and args for a prompt-only invocation.
// For claude, the prompt is a positional argument after the -p boolean flag.
// For copilot, the prompt is the value of the -p flag (-p <text>).
func buildPromptOnlyCmdArgs(cfg *config.Config, prompt string) (string, []string) {
	provider := resolveProvider(cfg, nil, nil, nil)
	switch provider {
	case "copilot":
		cmd := cfg.Copilot.Command
		var args []string
		if prompt != "" {
			args = append(args, "-p", prompt, "-s")
		}
		if cfg.Copilot.Flags != "" {
			fields := strings.Fields(cfg.Copilot.Flags)
			fields = stripPromptFlag(fields)
			fields = stripBoolFlag(fields, "-s")
			fields = stripBoolFlag(fields, "--silent")
			fields = stripBoolFlag(fields, "--headless") // legacy: strip if present
			args = append(args, fields...)
		}
		return cmd, args
	default: // "claude"
		cmd := cfg.Claude.Command
		args := []string{"-p"}
		if prompt != "" {
			args = append(args, prompt)
		}
		args = append(args, "--max-turns", fmt.Sprintf("%d", cfg.MaxTurns))
		if cfg.Claude.Flags != "" {
			args = append(args, strings.Fields(cfg.Claude.Flags)...)
		}
		for _, tool := range cfg.Claude.AllowedTools {
			args = append(args, "--allowedTools", tool)
		}
		return cmd, args
	}
}

// stripModelFlag removes --model and its value from a slice of CLI flag tokens.
func stripModelFlag(fields []string) []string {
	var out []string
	for i := 0; i < len(fields); i++ {
		if fields[i] == "--model" && i+1 < len(fields) {
			i++ // skip the value too
			continue
		}
		out = append(out, fields[i])
	}
	return out
}

// stripPromptFlag removes -p/--prompt and its value from a slice of CLI flag tokens.
// Unlike stripBoolFlag, this handles -p <value> where the next token is the prompt text.
func stripPromptFlag(fields []string) []string {
	var out []string
	for i := 0; i < len(fields); i++ {
		if (fields[i] == "-p" || fields[i] == "--prompt") && i+1 < len(fields) {
			i++ // skip the value too
			continue
		}
		out = append(out, fields[i])
	}
	return out
}
