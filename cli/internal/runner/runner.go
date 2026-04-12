package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/catalog"
	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/gate"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/memory"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/orchestrator"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/policy"
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

const workflowSnapshotDirName = "workflow"

// CommandRunner abstracts subprocess execution for testing.
type CommandRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
	RunProcess(ctx context.Context, dir string, name string, args ...string) error
	RunPhase(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error)
	RunPhaseWithEnv(ctx context.Context, dir string, extraEnv []string, stdin io.Reader, name string, args ...string) ([]byte, error)
}

type PhaseProcessObserver interface {
	ProcessStarted(pid int)
	ProcessExited(pid int)
}

type PhaseProcessRunner interface {
	RunPhaseObserved(ctx context.Context, dir string, stdin io.Reader, observer PhaseProcessObserver, name string, args ...string) ([]byte, error)
}

type PhaseProcessEnvRunner interface {
	RunPhaseObservedWithEnv(ctx context.Context, dir string, extraEnv []string, stdin io.Reader, observer PhaseProcessObserver, name string, args ...string) ([]byte, error)
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
	Catalog  *catalog.Catalog
	// BuiltinWorkflows handles workflow names that execute internal logic
	// instead of loading .xylem/workflows/<name>.yaml.
	BuiltinWorkflows map[string]BuiltinWorkflowHandler
	Reporter         *reporter.Reporter // may be nil for non-github vessels
	// Shared harness scaffolding for phase policy enforcement, audit logging,
	// protected-surface verification, and tracing.
	Intermediary *intermediary.Intermediary // nil = no policy enforcement
	AuditLog     *intermediary.AuditLog     // nil = no audit logging
	Tracer       *observability.Tracer      // nil = no tracing
	// EpisodicStore persists one entry per completed phase for cross-phase
	// and cross-vessel recall. nil disables episodic persistence.
	EpisodicStore *memory.EpisodicStore
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

	sem        chan struct{}
	wg         sync.WaitGroup
	traceWg    sync.WaitGroup
	inFlight   atomic.Int32
	scheduleMu sync.Mutex

	resultMu sync.Mutex
	result   DrainResult

	processMu       sync.Mutex
	processes       map[string]trackedProcess
	classMu         sync.Mutex
	inFlightByClass map[string]int
}

type trackedProcess struct {
	PID       int
	PhaseName string
	Exited    bool
}

type StallFinding struct {
	Code     string
	Level    string
	VesselID string
	Phase    string
	Message  string
}

// New creates a Runner.
func New(cfg *config.Config, q *queue.Queue, wt WorktreeManager, r CommandRunner) *Runner {
	concurrency := 1
	if cfg != nil && cfg.Concurrency > 0 {
		concurrency = cfg.Concurrency
	}
	return &Runner{
		Config:          cfg,
		Queue:           q,
		Worktree:        wt,
		Runner:          r,
		LiveGate:        gate.NewLiveVerifier(),
		sem:             make(chan struct{}, concurrency),
		processes:       make(map[string]trackedProcess),
		inFlightByClass: make(map[string]int),
	}
}

// Drain dequeues pending vessels and launches sessions up to the configured global
// concurrency limit.
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

		r.scheduleMu.Lock()
		vessel, err := r.Queue.DequeueMatching(func(v queue.Vessel) bool {
			return r.classSlotAvailable(v.ConcurrencyClass())
		})
		if err != nil || vessel == nil {
			r.scheduleMu.Unlock()
			<-r.sem
			break drainLoop
		}
		class := vessel.ConcurrencyClass()
		if !r.reserveClassSlot(class) {
			r.scheduleMu.Unlock()
			<-r.sem
			continue
		}
		r.scheduleMu.Unlock()

		log.Printf("%sdequeued vessel workflow=%s workflow_class=%s", vesselLabel(*vessel), vessel.Workflow, class)

		result.Launched++
		r.recordLaunched()
		r.inFlight.Add(1)
		r.wg.Add(1)
		drainLaunchWg.Add(1)
		go func(j queue.Vessel, workflowClass string) {
			defer r.wg.Done()
			defer drainLaunchWg.Done()
			defer func() {
				r.releaseClassSlot(workflowClass)
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
				if r.Config != nil && r.Config.StateDir != "" {
					if summary, loadErr := LoadVesselSummary(r.Config.StateDir, j.ID); loadErr == nil && summary != nil {
						vesselSpan.AddAttributes(observability.VesselCostAttributes(observability.VesselCostData{
							TotalTokens:            summary.TotalTokensEst,
							TotalCostUSDEst:        summary.TotalCostUSDEst,
							UsageSource:            string(summary.UsageSource),
							UsageUnavailableReason: summary.UsageUnavailableReason,
							BudgetExceeded:         summary.BudgetExceeded,
							BudgetWarning:          summary.BudgetWarning,
						}))
					}
				}
				vesselSpan.AddAttributes(observability.RecoveryAttributes(recoveryAttributesFromMeta(finalVessel.Meta)))
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
		}(*vessel, class)
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

func (r *Runner) classSlotAvailable(class string) bool {
	if r.Config == nil {
		return true
	}
	limit, ok := r.Config.ConcurrencyLimit(class)
	if !ok {
		return true
	}
	r.classMu.Lock()
	defer r.classMu.Unlock()
	return r.inFlightByClass[class] < limit
}

func (r *Runner) reserveClassSlot(class string) bool {
	if r.Config == nil {
		return true
	}
	limit, ok := r.Config.ConcurrencyLimit(class)
	if !ok {
		return true
	}
	r.classMu.Lock()
	defer r.classMu.Unlock()
	if r.inFlightByClass == nil {
		r.inFlightByClass = make(map[string]int)
	}
	if r.inFlightByClass[class] >= limit {
		return false
	}
	r.inFlightByClass[class]++
	return true
}

func (r *Runner) releaseClassSlot(class string) {
	if r.Config == nil {
		return
	}
	if _, ok := r.Config.ConcurrencyLimit(class); !ok {
		return
	}
	r.classMu.Lock()
	defer r.classMu.Unlock()
	if r.inFlightByClass[class] <= 1 {
		delete(r.inFlightByClass, class)
		return
	}
	r.inFlightByClass[class]--
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
				if s, _, loadErr := r.loadWorkflow(vessel.Workflow); loadErr == nil {
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
				vrs := newVesselRunState(r.Config, vessel, r.runtimeNow())
				vrs.setTraceContext(observability.TraceContextFromContext(timeoutSpan.Context()))
				r.annotateRecoveryMetadata(vessel.ID, queue.StateTimedOut, "label gate timed out", traceContextPointer(vrs.trace))
				src := r.resolveSourceForVessel(vessel)
				if err := src.OnTimedOut(ctx, vessel); err != nil {
					log.Printf("warn: OnTimedOut hook for vessel %s: %v", vessel.ID, err)
				}
				r.persistRunArtifacts(vessel, string(queue.StateTimedOut), vrs, nil, r.runtimeNow())
				if r.Tracer != nil {
					if current, findErr := r.Queue.FindByID(vessel.ID); findErr == nil && current != nil {
						timeoutSpan.AddAttributes(observability.RecoveryAttributes(recoveryAttributesFromMeta(current.Meta)))
					}
				}
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
			r.warnOnWorkflowDrift(vessel)
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
	defer r.clearTrackedProcess(vessel.ID)

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
	worktreePath := vessel.WorktreePath
	defer func() {
		if outcome != "failed" {
			return
		}
		r.persistRunArtifacts(vessel, string(queue.StateFailed), vrs, claims, r.runtimeNow())
	}()
	defer func() {
		if worktreePath == "" || !r.isVesselTimedOut(vessel.ID) {
			return
		}
		r.removeWorktree(context.Background(), worktreePath, vessel.ID)
	}()

	// Prompt-only vessel (no workflow): single claude -p invocation
	if vessel.Workflow == "" && vessel.Prompt != "" {
		var ok bool
		worktreePath, ok = r.ensureWorktree(ctx, &vessel, src)
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

	sk, _, err := r.loadVesselWorkflow(&vessel)
	if err != nil {
		r.failVessel(vessel.ID, fmt.Sprintf("load workflow: %v", err))
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		return "failed"
	}

	var ok bool
	worktreePath, ok = r.ensureWorktree(ctx, &vessel, src)
	if !ok {
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
	// Execute phases sequentially (no explicit dependencies)
	var phaseResults []reporter.PhaseResult
	for i := vessel.CurrentPhase; i < len(sk.Phases); i++ {
		if r.vesselCancelled(ctx, vessel.ID) {
			return r.cancelVessel(vessel, worktreePath, vrs, claims)
		}
		p := sk.Phases[i]
		res := r.runSinglePhase(ctx, vessel, sk, i, previousOutputs, issueData, harnessContent, worktreePath, src, vrs, true)
		if res.phaseSummary.Name != "" {
			vrs.addPhase(res.phaseSummary)
		}
		if res.evaluationReport != nil {
			vrs.addEvaluationReport(*res.evaluationReport)
		}
		if len(res.evidenceClaims) > 0 {
			claims = append(claims, res.evidenceClaims...)
		}

		switch res.status {
		case "failed":
			return "failed"
		case "waiting":
			return "waiting"
		case "cancelled":
			return r.cancelVessel(vessel, worktreePath, vrs, claims)
		case "timed_out":
			return "timed_out"
		case "completed", "no-op":
			if r.EpisodicStore != nil {
				summary := res.output
				const maxSummaryLen = 512
				if len(summary) > maxSummaryLen {
					summary = summary[:maxSummaryLen]
				}
				entry := memory.EpisodicEntry{
					VesselID:   vessel.ID,
					PhaseName:  p.Name,
					RecordedAt: r.runtimeNow().UTC(),
					Outcome:    res.status,
					Summary:    summary,
				}
				if err := r.EpisodicStore.Append(entry); err != nil {
					log.Printf("warn: episodic store append for vessel %s phase %s: %v", vessel.ID, p.Name, err)
				}
			}
			previousOutputs[p.Name] = res.output
			vessel.CurrentPhase = i + 1
			if vessel.PhaseOutputs == nil {
				vessel.PhaseOutputs = make(map[string]string)
			}
			vessel.PhaseOutputs[p.Name] = config.RuntimePath(r.Config.StateDir, "phases", vessel.ID, p.Name+".output")
			if updateErr := r.Queue.UpdateVessel(vessel); updateErr != nil {
				if r.cancelledTransition(vessel.ID, updateErr) {
					return r.cancelVessel(vessel, worktreePath, vrs, claims)
				}
				if r.timedOutTransition(vessel.ID, updateErr) {
					return "timed_out"
				}
				log.Printf("warn: persist phase progress for %s: %v", vessel.ID, updateErr)
			}
			if res.phaseReport.Name != "" {
				phaseResults = append(phaseResults, res.phaseReport)
			}
			if res.status == "no-op" {
				log.Printf("%sphase %q triggered no-op; completing workflow early", vesselLabel(vessel), p.Name)
				if r.vesselCancelled(ctx, vessel.ID) {
					return r.cancelVessel(vessel, worktreePath, vrs, claims)
				}
				return r.completeVessel(ctx, vessel, worktreePath, phaseResults, vrs, claims)
			}
		}
	}

	// All phases complete
	log.Printf("%scompleted all phases", vesselLabel(vessel))
	if r.vesselCancelled(ctx, vessel.ID) {
		return r.cancelVessel(vessel, worktreePath, vrs, claims)
	}
	if r.isVesselTimedOut(vessel.ID) {
		return "timed_out"
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

	tier, providerChain := resolvePhaseProviderChain(r.Config, nil, vessel, nil, nil)
	if len(providerChain) == 0 {
		r.failVessel(vessel.ID, "no providers configured")
		return "failed"
	}
	provider := providerChain[0]
	model := resolvePhaseModel(r.Config, nil, nil, nil, provider, tier)
	phaseDef := workflow.Phase{Name: "prompt"}
	phaseStartedAt := r.runtimeNow()
	beforeSnapshot, checkProtectedSurfaces, err := r.takeProtectedSurfaceSnapshot(ctx, worktreePath)
	if err != nil {
		snapErr := fmt.Errorf("protected surface snapshot failed: %w", err)
		if vrs != nil {
			vrs.addPhase(vrs.phaseSummaryWithLLM(r.Config, nil, nil, phaseDef, "", 0, 0, 0.0, r.runtimeSince(phaseStartedAt), "failed", nil, snapErr.Error(), provider, model))
		}
		r.failVessel(vessel.ID, snapErr.Error())
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		issueNum := r.parseIssueNum(vessel)
		if issueNum > 0 && r.Reporter != nil {
			r.logReporterError("post vessel-failed comment", vessel.ID,
				r.Reporter.VesselFailed(ctx, issueNum, phaseDef.Name, snapErr.Error(), ""))
		}
		return "failed"
	}

	outputPath := config.RuntimePath(r.Config.StateDir, "phases", vessel.ID, "prompt.output")
	if touchErr := r.touchPhaseActivity(outputPath); touchErr != nil {
		log.Printf("warn: touch phase activity %s: %v", outputPath, touchErr)
	}
	output, provider, model, runErr := r.runPhaseWithProviderFallback(ctx, vessel.ID, "prompt", worktreePath, providerChain, func(provider string) (providerInvocation, error) {
		cmd, args := buildPromptOnlyCmdArgs(r.Config, provider, tier, prompt)
		return providerInvocation{
			Provider:     provider,
			Model:        modelForProvider(r.Config, provider, tier),
			Env:          providerEnvForName(r.Config, provider),
			Command:      cmd,
			Args:         args,
			StdinContent: prompt,
		}, nil
	})
	phaseDuration := r.runtimeSince(phaseStartedAt)
	if r.vesselCancelled(ctx, vessel.ID) {
		return r.cancelVessel(vessel, worktreePath, vrs, nil)
	}
	if r.isVesselTimedOut(vessel.ID) {
		return "timed_out"
	}
	if runErr != nil {
		if vrs != nil {
			vrs.addPhase(vrs.phaseSummaryWithLLM(r.Config, nil, nil, phaseDef, "", 0, 0, 0.0, phaseDuration, "failed", nil, runErr.Error(), provider, model))
		}
		r.failVessel(vessel.ID, runErr.Error())
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		issueNum := r.parseIssueNum(vessel)
		if issueNum > 0 && r.Reporter != nil {
			r.logReporterError("post vessel-failed comment", vessel.ID,
				r.Reporter.VesselFailed(ctx, issueNum, phaseDef.Name, runErr.Error(), ""))
		}
		return "failed"
	}
	if checkProtectedSurfaces {
		if err := r.verifyProtectedSurfaces(vessel, workflow.Phase{Name: "prompt-only"}, worktreePath, beforeSnapshot); err != nil {
			if vrs != nil {
				vrs.addPhase(vrs.phaseSummaryWithLLM(r.Config, nil, nil, phaseDef, "", 0, 0, 0.0, phaseDuration, "failed", nil, err.Error(), provider, model))
			}
			r.failVessel(vessel.ID, err.Error())
			if err := src.OnFail(ctx, vessel); err != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
			}
			issueNum := r.parseIssueNum(vessel)
			if issueNum > 0 && r.Reporter != nil {
				r.logReporterError("post vessel-failed comment", vessel.ID,
					r.Reporter.VesselFailed(ctx, issueNum, phaseDef.Name, err.Error(), ""))
			}
			return "failed"
		}
	}
	recordedAt := r.runtimeNow()
	var phaseResults []reporter.PhaseResult
	if vrs != nil {
		inputTokensEst, outputTokensEst, costUSDEst := vrs.recordLLMUsage(model, prompt, string(output), recordedAt)
		phaseSummary := vrs.phaseSummaryWithLLM(r.Config, nil, nil, phaseDef, "", inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "completed", nil, "", provider, model)
		phaseReport := reporter.PhaseResult{
			Name:                   phaseDef.Name,
			Duration:               phaseDuration,
			Status:                 "completed",
			Provider:               provider,
			Model:                  model,
			InputTokensEst:         inputTokensEst,
			OutputTokensEst:        outputTokensEst,
			CostUSDEst:             costUSDEst,
			UsageSource:            cost.UsageSourceEstimated,
			UsageUnavailableReason: "",
		}
		if vrs.costTracker != nil && vrs.costTracker.BudgetExceeded() {
			errMsg := fmt.Sprintf("budget exceeded: estimated cost $%.4f, estimated tokens %d",
				vrs.costTracker.TotalCost(), vrs.costTracker.TotalTokens())
			phaseSummary.Status = "failed"
			phaseSummary.Error = errMsg
			vrs.addPhase(phaseSummary)
			r.failVessel(vessel.ID, errMsg)
			if err := src.OnFail(ctx, vessel); err != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
			}
			issueNum := r.parseIssueNum(vessel)
			if issueNum > 0 && r.Reporter != nil {
				r.logReporterError("post vessel-failed comment", vessel.ID,
					r.Reporter.VesselFailed(ctx, issueNum, phaseDef.Name, errMsg, ""))
			}
			return "failed"
		}
		vrs.addPhase(phaseSummary)
		phaseResults = append(phaseResults, phaseReport)
	}
	issueNum := r.parseIssueNum(vessel)
	if issueNum > 0 && r.Reporter != nil && len(phaseResults) > 0 {
		r.reportPhaseComplete(ctx, vessel, phaseResults[0], string(output))
	}

	if r.vesselCancelled(ctx, vessel.ID) {
		return r.cancelVessel(vessel, worktreePath, vrs, nil)
	}
	if r.isVesselTimedOut(vessel.ID) {
		return "timed_out"
	}
	if err := src.OnComplete(ctx, vessel); err != nil {
		log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
	}

	return r.completeVessel(ctx, vessel, worktreePath, phaseResults, vrs, nil)
}

func (r *Runner) runBuiltinWorkflow(ctx context.Context, vessel queue.Vessel, src source.Source, vrs *vesselRunState, handler BuiltinWorkflowHandler) string {
	startedAt := r.runtimeNow()
	if err := handler(ctx, vessel); err != nil {
		if r.vesselCancelled(ctx, vessel.ID) {
			return r.cancelVessel(vessel, "", vrs, nil)
		}
		if r.isVesselTimedOut(vessel.ID) {
			return "timed_out"
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
	if r.isVesselTimedOut(vessel.ID) {
		return "timed_out"
	}
	if vrs != nil {
		vrs.addPhase(PhaseSummary{
			Name:                   vessel.Workflow,
			Type:                   "builtin",
			DurationMS:             r.runtimeSince(startedAt).Milliseconds(),
			Status:                 "completed",
			UsageSource:            cost.UsageSourceNotApplicable,
			UsageUnavailableReason: "builtin workflow did not execute an llm phase",
		})
	}
	if r.vesselCancelled(ctx, vessel.ID) {
		return r.cancelVessel(vessel, "", vrs, nil)
	}
	if r.isVesselTimedOut(vessel.ID) {
		return "timed_out"
	}
	if err := src.OnComplete(ctx, vessel); err != nil {
		log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
	}
	return r.completeVessel(ctx, vessel, "", nil, vrs, nil)
}

func (r *Runner) phaseToolCatalog() (*catalog.Catalog, error) {
	if r.Catalog != nil {
		return r.Catalog, nil
	}
	if r.Config == nil {
		return catalog.NewDefaultPhaseCatalog()
	}
	return r.Config.BuildPhaseToolCatalog()
}

func (r *Runner) resolvePhaseAllowedTools(wf *workflow.Workflow, p *workflow.Phase, provider config.ProviderConfig) (string, error) {
	if p == nil {
		return "", nil
	}
	toolCatalog, err := r.phaseToolCatalog()
	if err != nil {
		return "", fmt.Errorf("resolve phase allowed tools: %w", err)
	}

	requested := mergeAllowedToolLists(parseAllowedToolsString(pointerStringValue(p.AllowedTools)), provider.AllowedTools)
	role := catalog.RoleDiagnostic
	if r.Config != nil {
		class := policy.Class("")
		if wf != nil {
			class = wf.Class
		}
		role = r.Config.PhaseToolRole(class, p.Name, p.Type)
	}
	resolved, err := toolCatalog.ResolveRoleTools(role, requested)
	if err != nil {
		return "", fmt.Errorf("resolve phase %q allowed tools for role %q: %w", p.Name, role, err)
	}
	if len(resolved) == 0 {
		return "", nil
	}
	return strings.Join(resolved, ","), nil
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
	tier := resolveTier(cfg, *vessel, nil, nil)
	_, providerChain := resolvePhaseProviderChain(cfg, nil, *vessel, nil, nil)
	if len(providerChain) == 0 {
		return "", nil, fmt.Errorf("no providers configured")
	}
	provider := providerChain[0]
	if vessel.Prompt != "" {
		prompt := vessel.Prompt
		if vessel.Ref != "" {
			prompt = fmt.Sprintf("Ref: %s\n\n%s", vessel.Ref, vessel.Prompt)
		}
		cmd, args := buildPromptOnlyCmdArgs(cfg, provider, tier, prompt)
		return cmd, args, nil
	}

	// Workflow-based mode: build command from flags (v2 phase-based execution will replace this)
	wfPrompt := fmt.Sprintf("/%s %s", vessel.Workflow, vessel.Ref)
	cmd, args := buildPromptOnlyCmdArgs(cfg, provider, tier, wfPrompt)
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

func (r *Runner) warnOnWorkflowDrift(vessel queue.Vessel) {
	storedDigest, currentDigest := workflowDigestsForResume(vessel)
	if !workflowDigestDrifted(storedDigest, currentDigest) {
		return
	}
	log.Printf(
		"warn: waiting vessel %s workflow %q drifted while waiting: stored=%s current=%s",
		vessel.ID,
		vessel.Workflow,
		storedDigest,
		currentDigest,
	)
}

func workflowDigestsForResume(vessel queue.Vessel) (string, string) {
	storedDigest := strings.TrimSpace(vessel.Meta[recovery.MetaWorkflowDigest])
	currentDigest := currentWorkflowDigest(vessel.Workflow)
	return storedDigest, currentDigest
}

func workflowDigestDrifted(storedDigest, currentDigest string) bool {
	storedDigest = strings.TrimSpace(storedDigest)
	currentDigest = strings.TrimSpace(currentDigest)
	if storedDigest == "" || currentDigest == "" {
		return false
	}
	return storedDigest != currentDigest
}

func currentWorkflowDigest(workflowName string) string {
	workflowName = strings.TrimSpace(workflowName)
	if workflowName == "" {
		return ""
	}
	return recovery.DigestFile(filepath.Join(".xylem", "workflows", workflowName+".yaml"), "wf")
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
	state, ok := r.terminalTransitionState(vesselID, err)
	return ok && state == queue.StateCancelled
}

func (r *Runner) timedOutTransition(vesselID string, err error) bool {
	state, ok := r.terminalTransitionState(vesselID, err)
	return ok && state == queue.StateTimedOut
}

func (r *Runner) terminalTransitionState(vesselID string, err error) (queue.VesselState, bool) {
	if err == nil || !errors.Is(err, queue.ErrInvalidTransition) {
		return "", false
	}
	current, findErr := r.Queue.FindByID(vesselID)
	if findErr != nil {
		log.Printf("warn: inspect cancel state for %s: %v", vesselID, findErr)
		return "", false
	}
	if current == nil || !current.State.IsTerminal() {
		return "", false
	}
	return current.State, true
}

func (r *Runner) isVesselTimedOut(vesselID string) bool {
	vessel, err := r.Queue.FindByID(vesselID)
	if err != nil {
		log.Printf("warn: inspect timeout state for %s: %v", vesselID, err)
		return false
	}
	return vessel != nil && vessel.State == queue.StateTimedOut
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
		if r.timedOutTransition(id, updateErr) {
			return
		}
		log.Printf("warn: failed to update vessel %s state: %v", id, updateErr)
		return
	}
	r.annotateRecoveryMetadata(id, queue.StateFailed, errMsg, nil)
}

func (r *Runner) failUpdatedVessel(vessel *queue.Vessel, errMsg string) {
	if vessel == nil {
		return
	}
	now := r.runtimeNow()
	vessel.State = queue.StateFailed
	vessel.Error = errMsg
	vessel.EndedAt = &now
	vessel.Meta = recovery.ApplyToMeta(vessel.Meta, recovery.Build(recovery.Input{
		VesselID:       vessel.ID,
		Source:         vessel.Source,
		Workflow:       vessel.Workflow,
		Ref:            vessel.Ref,
		State:          queue.StateFailed,
		FailedPhase:    vessel.FailedPhase,
		Error:          errMsg,
		GateOutput:     vessel.GateOutput,
		RetryOf:        vessel.RetryOf,
		WorkflowDigest: vessel.WorkflowDigest,
		Meta:           vessel.Meta,
		CreatedAt:      now,
	}))
	if updateErr := r.Queue.UpdateVessel(*vessel); updateErr != nil {
		if r.cancelledTransition(vessel.ID, updateErr) {
			return
		}
		if r.timedOutTransition(vessel.ID, updateErr) {
			return
		}
		log.Printf("warn: failed to persist vessel %s state: %v", vessel.ID, updateErr)
		r.failVessel(vessel.ID, errMsg)
	}
}

func (r *Runner) annotateRecoveryMetadata(id string, state queue.VesselState, errMsg string, trace *observability.TraceContextData) {
	if r.Queue == nil {
		return
	}
	current, err := r.Queue.FindByID(id)
	if err != nil || current == nil {
		return
	}
	current.Meta = recovery.ApplyToMeta(current.Meta, recovery.Build(recovery.Input{
		VesselID:       current.ID,
		Source:         current.Source,
		Workflow:       current.Workflow,
		Ref:            current.Ref,
		State:          state,
		FailedPhase:    current.FailedPhase,
		Error:          errMsg,
		GateOutput:     current.GateOutput,
		RetryOf:        current.RetryOf,
		WorkflowDigest: current.WorkflowDigest,
		Meta:           current.Meta,
		Trace:          trace,
		CreatedAt:      r.runtimeNow(),
	}))
	if updateErr := r.Queue.UpdateVessel(*current); updateErr != nil {
		log.Printf("warn: annotate recovery metadata for vessel %s: %v", id, updateErr)
	}
}

func (r *Runner) persistRecoveryMetadata(id string, artifact *recovery.Artifact) {
	if r.Queue == nil || artifact == nil {
		return
	}
	current, err := r.Queue.FindByID(id)
	if err != nil || current == nil {
		return
	}
	current.Meta = recovery.ApplyToMeta(current.Meta, artifact)
	if updateErr := r.Queue.UpdateVessel(*current); updateErr != nil {
		log.Printf("warn: persist recovery metadata for vessel %s: %v", id, updateErr)
	}
}

func (r *Runner) completeVessel(ctx context.Context, vessel queue.Vessel, worktreePath string, phaseResults []reporter.PhaseResult, vrs *vesselRunState, claims []evidence.Claim) string {
	if updateErr := r.Queue.Update(vessel.ID, queue.StateCompleted, ""); updateErr != nil {
		if r.cancelledTransition(vessel.ID, updateErr) {
			return r.cancelVessel(vessel, worktreePath, vrs, claims)
		}
		if r.timedOutTransition(vessel.ID, updateErr) {
			return "timed_out"
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
	artifactVessel := vessel
	if latest, err := r.Queue.FindByID(vessel.ID); err == nil && latest != nil {
		artifactVessel = *latest
	}
	manifest := &evidence.Manifest{
		VesselID:  vessel.ID,
		Workflow:  vessel.Workflow,
		Claims:    append([]evidence.Claim(nil), claims...),
		CreatedAt: now.UTC(),
	}
	if err := evidence.SaveManifest(r.Config.StateDir, vessel.ID, manifest); err != nil {
		log.Printf("warn: save evidence manifest: %v", err)
		manifest = nil
	} else {
		summary.EvidenceManifestPath = evidenceManifestRelativePath(vessel.ID)
		reviewArtifacts.EvidenceManifest = summary.EvidenceManifestPath
	}

	if vrs.costTracker != nil {
		reportPath := config.RuntimePath(r.Config.StateDir, "phases", vessel.ID, costReportFileName)
		report := vrs.buildCostReport(summary)
		if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
			log.Printf("warn: save cost report: %v", fmt.Errorf("create dir: %w", err))
		} else if err := cost.SaveReport(reportPath, report); err != nil {
			log.Printf("warn: save cost report: %v", err)
		} else {
			summary.CostReportPath = costReportRelativePath(vessel.ID)
			reviewArtifacts.CostReport = summary.CostReportPath
		}

		alertsPath := config.RuntimePath(r.Config.StateDir, "phases", vessel.ID, budgetAlertsFileName)
		if err := saveJSONArtifact(alertsPath, vrs.costTracker.Alerts()); err != nil {
			log.Printf("warn: save budget alerts: %v", err)
		} else {
			summary.BudgetAlertsPath = budgetAlertsRelativePath(vessel.ID)
			reviewArtifacts.BudgetAlerts = summary.BudgetAlertsPath
		}
	}

	evalPath := config.RuntimePath(r.Config.StateDir, "phases", vessel.ID, evalReportFileName)
	if artifact := vrs.evaluationArtifact(); artifact != nil {
		if err := saveJSONArtifact(evalPath, artifact); err != nil {
			log.Printf("warn: save eval report: %v", err)
		} else {
			summary.EvalReportPath = evalReportRelativePath(vessel.ID)
			reviewArtifacts.EvalReport = summary.EvalReportPath
		}
	} else if info, err := os.Stat(evalPath); err == nil && !info.IsDir() {
		summary.EvalReportPath = evalReportRelativePath(vessel.ID)
		reviewArtifacts.EvalReport = summary.EvalReportPath
	}

	if state == string(queue.StateFailed) || state == string(queue.StateTimedOut) {
		seedArtifact := recovery.Build(recovery.Input{
			VesselID:       artifactVessel.ID,
			Source:         artifactVessel.Source,
			Workflow:       artifactVessel.Workflow,
			Ref:            artifactVessel.Ref,
			State:          artifactVessel.State,
			FailedPhase:    artifactVessel.FailedPhase,
			Error:          artifactVessel.Error,
			GateOutput:     artifactVessel.GateOutput,
			RetryOf:        artifactVessel.RetryOf,
			WorkflowDigest: artifactVessel.WorkflowDigest,
			Meta:           artifactVessel.Meta,
			Trace:          traceContextPointer(vrs.trace),
			CreatedAt:      now,
		})
		reviewArtifact := recovery.Build(recovery.Input{
			VesselID:             artifactVessel.ID,
			Source:               artifactVessel.Source,
			Workflow:             artifactVessel.Workflow,
			Ref:                  artifactVessel.Ref,
			State:                artifactVessel.State,
			FailedPhase:          artifactVessel.FailedPhase,
			Error:                artifactVessel.Error,
			GateOutput:           artifactVessel.GateOutput,
			RetryOf:              artifactVessel.RetryOf,
			WorkflowDigest:       artifactVessel.WorkflowDigest,
			Meta:                 artifactVessel.Meta,
			Trace:                traceContextPointer(vrs.trace),
			CreatedAt:            now,
			RepeatedFailureCount: r.matchingFailureCount(artifactVessel, seedArtifact.FailureFingerprint),
			EvidencePaths:        r.failureEvidencePaths(vessel.ID, summary, reviewArtifacts),
		})
		if diagnosedArtifact, invoked, err := recovery.RunDiagnosisWorkflow(recovery.DiagnosisInput{Artifact: reviewArtifact}); err != nil {
			log.Printf("warn: run recovery diagnosis workflow: %v", err)
		} else if invoked {
			reviewArtifact = diagnosedArtifact
		}
		if err := recovery.Save(r.Config.StateDir, reviewArtifact); err != nil {
			log.Printf("warn: save recovery artifact: %v", err)
		} else {
			summary.FailureReviewPath = failureReviewRelativePath(vessel.ID)
			reviewArtifacts.FailureReview = summary.FailureReviewPath
			summary.Recovery = recoverySummaryFromArtifact(reviewArtifact)
			r.persistRecoveryMetadata(vessel.ID, reviewArtifact)
		}
	}

	if reviewArtifacts.EvidenceManifest != "" || reviewArtifacts.CostReport != "" ||
		reviewArtifacts.BudgetAlerts != "" || reviewArtifacts.EvalReport != "" || reviewArtifacts.FailureReview != "" {
		summary.ReviewArtifacts = reviewArtifacts
	}

	if err := SaveVesselSummary(r.Config.StateDir, summary); err != nil {
		log.Printf("warn: save vessel summary: %v", err)
	}

	return manifest
}

func (r *Runner) failureEvidencePaths(vesselID string, summary *VesselSummary, artifacts *ReviewArtifacts) []string {
	paths := []string{filepath.ToSlash(filepath.Join("phases", vesselID, summaryFileName))}
	if summary == nil {
		return paths
	}
	for _, path := range []string{
		summary.EvidenceManifestPath,
		summary.CostReportPath,
		summary.BudgetAlertsPath,
		summary.EvalReportPath,
	} {
		if strings.TrimSpace(path) != "" {
			paths = append(paths, path)
		}
	}
	if artifacts != nil {
		for _, path := range []string{
			artifacts.EvidenceManifest,
			artifacts.CostReport,
			artifacts.BudgetAlerts,
			artifacts.EvalReport,
		} {
			if strings.TrimSpace(path) != "" {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func (r *Runner) matchingFailureCount(vessel queue.Vessel, fingerprint string) int {
	if r.Queue == nil || fingerprint == "" {
		return 0
	}
	vessels, err := r.Queue.List()
	if err != nil {
		log.Printf("warn: list queue for repeated failure count: %v", err)
		return 0
	}
	return recovery.CountMatchingFailures(vessels, vessel, fingerprint)
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

func recoveryAttributesFromMeta(meta map[string]string) observability.RecoveryData {
	if meta == nil {
		return observability.RecoveryData{}
	}
	return observability.RecoveryData{
		Class:           meta[recovery.MetaClass],
		Action:          meta[recovery.MetaAction],
		RetrySuppressed: meta[recovery.MetaRetrySuppressed],
		RetryOutcome:    meta[recovery.MetaRetryOutcome],
		UnlockDimension: firstNonEmptyMeta(meta, recovery.MetaUnlockedBy, recovery.MetaUnlockDimension),
	}
}

func firstNonEmptyMeta(meta map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(meta[key]); value != "" {
			return value
		}
	}
	return ""
}

func traceContextPointer(data *TraceArtifacts) *observability.TraceContextData {
	if data == nil {
		return nil
	}
	return &observability.TraceContextData{
		TraceID: data.TraceID,
		SpanID:  data.SpanID,
	}
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
		waveTimedOut := false
		for _, res := range results {
			p := wf.Phases[res.phaseIdx]
			result := res.result

			if result.phaseSummary.Name != "" {
				vrs.addPhase(result.phaseSummary)
			}
			if result.evaluationReport != nil {
				vrs.addEvaluationReport(*result.evaluationReport)
			}
			if len(result.evidenceClaims) > 0 && claims != nil {
				*claims = append(*claims, result.evidenceClaims...)
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
			case "timed_out":
				waveTimedOut = true
			}

			if result.status == "completed" || result.status == "no-op" {
				if result.phaseReport.Name != "" {
					allPhaseResults = append(allPhaseResults, result.phaseReport)
				} else {
					allPhaseResults = append(allPhaseResults, reporter.PhaseResult{
						Name:     p.Name,
						Duration: result.duration,
						Status:   result.status,
					})
				}
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
		if waveTimedOut {
			return "timed_out"
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
	if r.isVesselTimedOut(vessel.ID) {
		return "timed_out"
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
	output           string
	status           string // "completed", "no-op", "failed", "waiting", "cancelled", "timed_out"
	duration         time.Duration
	gateOut          string
	phaseSummary     PhaseSummary
	phaseReport      reporter.PhaseResult
	evaluationReport *PhaseEvaluationReport
	evidenceClaims   []evidence.Claim
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
	if vessel.Meta != nil {
		vessel.Meta = maps.Clone(vessel.Meta)
	}
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
		log.Printf("%sphase %q starting", vesselLabel(vessel), p.Name)
		phaseStart := r.runtimeNow()

		td := r.buildTemplateData(vessel, wf, issueData, p.Name, phaseIdx, previousOutputs, gateResult, phase.EvaluationData{})

		var output []byte
		var runErr error
		var beforeSnapshot surface.Snapshot
		var checkProtectedSurfaces bool
		var promptForCost string
		var evaluationReport *PhaseEvaluationReport
		var evidenceClaims []evidence.Claim
		tier, providerChain := resolvePhaseProviderChain(r.Config, srcCfg, vessel, wf, &p)
		provider := ""
		model := ""
		if len(providerChain) > 0 {
			provider = providerChain[0]
			model = resolvePhaseModel(r.Config, srcCfg, wf, &p, provider, tier)
		}
		retryAttempt := 0
		if p.Gate != nil && (p.Gate.Type == "command" || p.Gate.Type == "live") {
			retryAttempt = providerAttempt(&p, gateRetries)
		}
		phaseSpan := startPhaseSpan(r.Tracer, ctx, r.Config, wf, p, phaseIdx, retryAttempt, "", "", tier)
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
			finishPhaseSpan(r.Tracer, phaseSpan, buildPhaseResultData(r.Config, p, promptForCost, string(output), phaseDuration, phaseSpanStatus, phaseOutputArtifactPath, provider, model, tier), err)
			phaseSpanEnded = true
		}

		phasesDir := config.RuntimePath(r.Config.StateDir, "phases", vessel.ID)
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
			if policyErr := r.enforcePhasePolicy(ctx, vessel, wf, p, worktreePath, rendered, ""); policyErr != nil {
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
			outputPath := filepath.Join(phasesDir, p.Name+".output")
			if touchErr := r.touchPhaseActivity(outputPath); touchErr != nil {
				log.Printf("warn: touch phase activity %s: %v", outputPath, touchErr)
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
			if policyErr := r.enforcePhasePolicy(ctx, vessel, wf, p, worktreePath, "", promptTemplate); policyErr != nil {
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
			outputPath := filepath.Join(phasesDir, p.Name+".output")
			if touchErr := r.touchPhaseActivity(outputPath); touchErr != nil {
				log.Printf("warn: touch phase activity %s: %v", outputPath, touchErr)
			}
			if p.Evaluator != nil {
				exec, err := r.runPhaseEvaluationLoop(ctx, vessel, wf, phaseIdx, previousOutputs, issueData, gateResult, harnessContent, worktreePath, phasesDir, promptTemplate, vrs, attempt)
				if exec != nil {
					output = exec.output
					promptForCost = exec.promptForCost
					provider = exec.provider
					model = exec.model
					evaluationReport = exec.evaluationReport
					if evaluationReport != nil {
						evidenceClaims = append(evidenceClaims, buildEvaluationClaim(vessel.ID, p, *evaluationReport, r.runtimeNow()))
						if r.Tracer != nil {
							phaseSpan.AddAttributes(phaseEvaluationSpanAttributes(*evaluationReport))
						}
					}
				}
				if err != nil {
					runErr = err
				}
			} else {
				rendered, err := phase.RenderPromptWithOptions(promptTemplate, td, phase.RenderOptions{
					ContextBudget: phaseContextBudget(r.Config),
					Preamble:      harnessContent,
				})
				if err != nil {
					finishCurrentPhaseSpan(err)
					r.failVessel(vessel.ID, fmt.Sprintf("render prompt for phase %s: %v", p.Name, err))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
				}
				promptPath := filepath.Join(phasesDir, p.Name+".prompt")
				if wErr := os.WriteFile(promptPath, []byte(rendered), 0o644); wErr != nil {
					log.Printf("warn: write prompt file %s: %v", promptPath, wErr)
				}
				output, promptForCost, provider, model, runErr = r.runPromptInvocation(ctx, vessel, worktreePath, srcCfg, wf, &p, harnessContent, rendered, attempt)
			}
		}

		if r.vesselCancelled(ctx, vessel.ID) {
			finishCurrentPhaseSpan(context.Canceled)
			return singlePhaseResult{status: "cancelled", duration: r.runtimeSince(phaseStart)}
		}
		if r.isVesselTimedOut(vessel.ID) {
			finishCurrentPhaseSpan(context.DeadlineExceeded)
			return singlePhaseResult{status: "timed_out", duration: r.runtimeSince(phaseStart)}
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
				status:           "failed",
				duration:         phaseDuration,
				phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, 0, 0, 0.0, phaseDuration, "failed", nil, runErr.Error(), provider, model), evaluationReport),
				evaluationReport: evaluationReport,
				evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
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
					status:           "failed",
					duration:         phaseDuration,
					phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, 0, 0, 0.0, phaseDuration, "failed", nil, err.Error(), provider, model), evaluationReport),
					evaluationReport: evaluationReport,
					evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
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
				status:           "failed",
				duration:         phaseDuration,
				phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", nil, errMsg, provider, model), evaluationReport),
				evaluationReport: evaluationReport,
				evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
			}
		}

		if err := r.publishPhaseOutput(ctx, vessel, p, td, string(output)); err != nil {
			phaseSpanStatus = "failed"
			finishCurrentPhaseSpan(err)
			log.Printf("%sphase %q failed while publishing output: %v", vesselLabel(vessel), p.Name, err)
			vessel.FailedPhase = p.Name
			r.failUpdatedVessel(&vessel, fmt.Sprintf("phase %s: %v", p.Name, err))
			if err := src.OnFail(ctx, vessel); err != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
			}
			issueNum := r.parseIssueNum(vessel)
			if issueNum > 0 && r.Reporter != nil {
				r.logReporterError("post vessel-failed comment", vessel.ID,
					r.Reporter.VesselFailed(ctx, issueNum, p.Name, err.Error(), ""))
			}
			return singlePhaseResult{
				status:           "failed",
				duration:         phaseDuration,
				phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", nil, err.Error(), provider, model), evaluationReport),
				evaluationReport: evaluationReport,
				evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
			}
		}

		// Report phase completion.
		issueNum := r.parseIssueNum(vessel)
		phaseReport := reporter.PhaseResult{
			Name:                   p.Name,
			Duration:               phaseDuration,
			Provider:               provider,
			Model:                  model,
			InputTokensEst:         inputTokensEst,
			OutputTokensEst:        outputTokensEst,
			CostUSDEst:             costUSDEst,
			UsageSource:            cost.UsageSourceEstimated,
			UsageUnavailableReason: "",
		}
		if p.Type == "command" {
			phaseReport.UsageSource = cost.UsageSourceNotApplicable
			phaseReport.UsageUnavailableReason = "non-llm phase"
		}
		if phaseMatchedNoOp(&p, string(output)) {
			phaseReport.Status = "no-op"
			r.reportPhaseComplete(ctx, vessel, phaseReport, string(output))
			phaseSpanStatus = "no-op"
			finishCurrentPhaseSpan(nil)
			return singlePhaseResult{
				output:           string(output),
				status:           "no-op",
				duration:         phaseDuration,
				phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "no-op", nil, "", provider, model), evaluationReport),
				phaseReport:      phaseReport,
				evaluationReport: evaluationReport,
				evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
			}
		}

		phaseReport.Status = "completed"

		// Handle gate.
		if p.Gate == nil {
			r.reportPhaseComplete(ctx, vessel, phaseReport, string(output))
			phaseSpanStatus = "completed"
			finishCurrentPhaseSpan(nil)
			return singlePhaseResult{
				output:           string(output),
				status:           "completed",
				duration:         phaseDuration,
				phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "completed", nil, "", provider, model), evaluationReport),
				phaseReport:      phaseReport,
				evaluationReport: evaluationReport,
				evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
			}
		}

		switch p.Gate.Type {
		case "command", "live":
			gateResultExec := r.executeVerificationGate(ctx, phaseSpan, vessel, p, td, worktreePath, retryAttempt)
			if r.vesselCancelled(ctx, vessel.ID) {
				finishCurrentPhaseSpan(context.Canceled)
				return singlePhaseResult{status: "cancelled", duration: phaseDuration}
			}
			if r.isVesselTimedOut(vessel.ID) {
				finishCurrentPhaseSpan(context.DeadlineExceeded)
				return singlePhaseResult{status: "timed_out", duration: phaseDuration}
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
					status:           "failed",
					duration:         phaseDuration,
					gateOut:          gateOut,
					phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), gateErr.Error(), provider, model), evaluationReport),
					evaluationReport: evaluationReport,
					evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
				}
			}
			if passed {
				log.Printf("%sgate passed for phase %q", vesselLabel(vessel), p.Name)
				r.reportPhaseComplete(ctx, vessel, phaseReport, string(output))
				phaseSpanStatus = "completed"
				finishCurrentPhaseSpan(nil)
				return singlePhaseResult{
					output:           string(output),
					status:           "completed",
					duration:         phaseDuration,
					phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "completed", gatePassedPointer(true), "", provider, model), evaluationReport),
					phaseReport:      phaseReport,
					evaluationReport: evaluationReport,
					evidenceClaims:   appendEvidenceClaim(evidenceClaims, gateResultExec.evidenceClaim),
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
					status:           "failed",
					duration:         phaseDuration,
					gateOut:          gateOut,
					phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), "gate failed, retries exhausted", provider, model), evaluationReport),
					evaluationReport: evaluationReport,
					evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
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
				if r.isVesselTimedOut(vessel.ID) {
					finishCurrentPhaseSpan(context.DeadlineExceeded)
					return singlePhaseResult{status: "timed_out", duration: phaseDuration}
				}
				r.failVessel(vessel.ID, fmt.Sprintf("phase %s gate retry interrupted: %v", p.Name, err))
				if failErr := src.OnFail(ctx, vessel); failErr != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
				}
				phaseSpanStatus = "failed"
				finishCurrentPhaseSpan(err)
				return singlePhaseResult{
					status:           "failed",
					duration:         phaseDuration,
					gateOut:          gateOut,
					phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), err.Error(), provider, model), evaluationReport),
					evaluationReport: evaluationReport,
					evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
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
			r.reportPhaseComplete(ctx, vessel, phaseReport, string(output))
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
				if r.timedOutTransition(vessel.ID, updateErr) {
					finishCurrentPhaseSpan(context.DeadlineExceeded)
					return singlePhaseResult{status: "timed_out", duration: r.runtimeSince(phaseStart)}
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
			return singlePhaseResult{
				output:           string(output),
				status:           "waiting",
				duration:         r.runtimeSince(phaseStart),
				evaluationReport: evaluationReport,
				evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
			}
		}

		// Unknown gate type: treat as passed.
		r.reportPhaseComplete(ctx, vessel, phaseReport, string(output))
		phaseSpanStatus = "completed"
		finishCurrentPhaseSpan(nil)
		return singlePhaseResult{
			output:           string(output),
			status:           "completed",
			duration:         phaseDuration,
			phaseSummary:     applyPhaseEvaluationSummary(vrs.phaseSummaryWithLLM(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "completed", nil, "", provider, model), evaluationReport),
			phaseReport:      phaseReport,
			evaluationReport: evaluationReport,
			evidenceClaims:   append([]evidence.Claim(nil), evidenceClaims...),
		}
	}
}

func startPhaseSpan(tracer *observability.Tracer, ctx context.Context, cfg *config.Config, wf *workflow.Workflow, p workflow.Phase, phaseIdx int, retryAttempt int, provider, model, tier string) observability.SpanContext {
	if tracer == nil {
		return observability.SpanContext{}
	}

	return tracer.StartSpan(ctx, "phase:"+p.Name, observability.PhaseSpanAttributes(observability.PhaseSpanData{
		Name:         p.Name,
		Index:        phaseIdx,
		Type:         phaseTypeLabel(p),
		Workflow:     workflowName(wf),
		Provider:     provider,
		Model:        model,
		Tier:         tier,
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

func phaseEvaluationSpanAttributes(report PhaseEvaluationReport) []observability.SpanAttribute {
	attrs := []observability.SpanAttribute{
		{Key: "xylem.eval.enabled", Value: "true"},
		{Key: "xylem.eval.iterations", Value: fmt.Sprintf("%d", report.Iterations)},
		{Key: "xylem.eval.converged", Value: fmt.Sprintf("%t", report.Converged)},
		{Key: "xylem.eval.intensity", Value: report.Intensity},
		{Key: "xylem.eval.criteria_count", Value: fmt.Sprintf("%d", len(report.Criteria))},
	}
	if report.FinalResult != nil {
		attrs = append(attrs,
			observability.SpanAttribute{Key: "xylem.eval.pass", Value: fmt.Sprintf("%t", report.FinalResult.Pass)},
			observability.SpanAttribute{Key: "xylem.eval.feedback_count", Value: fmt.Sprintf("%d", len(report.FinalResult.Feedback))},
		)
	}
	return append(attrs, observability.SignalSetSpanAttributes(report.Signals)...)
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

func buildPhaseResultData(cfg *config.Config, p workflow.Phase, renderedPrompt, output string, duration time.Duration, status string, outputArtifactPath, provider, model, tier string) observability.PhaseResultData {
	data := observability.PhaseResultData{
		DurationMS:         duration.Milliseconds(),
		Status:             status,
		OutputArtifactPath: outputArtifactPath,
		LLMProvider:        provider,
		LLMModel:           model,
		LLMTier:            tier,
	}
	if phaseTypeLabel(p) != "prompt" {
		data.UsageSource = string(cost.UsageSourceNotApplicable)
		data.UsageUnavailableReason = "non-llm phase"
		return data
	}

	data.InputTokensEst = cost.EstimateTokens(renderedPrompt)
	data.OutputTokensEst = cost.EstimateTokens(output)
	data.CostUSDEst = cost.EstimateCost(data.InputTokensEst, data.OutputTokensEst, cost.LookupPricing(model))
	data.UsageSource = string(cost.UsageSourceEstimated)
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
			claim.Level = evidence.BehaviorallyChecked
			claim.Checker = p.Gate.Run
			claim.TrustBoundary = "Command gate output"
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

func buildEvaluationClaim(vesselID string, p workflow.Phase, report PhaseEvaluationReport, recordedAt time.Time) evidence.Claim {
	passed := report.Converged && report.FinalResult != nil && report.FinalResult.Pass
	claimText := fmt.Sprintf("Evaluator review for phase %q did not meet configured quality thresholds", p.Name)
	if passed {
		claimText = fmt.Sprintf("Evaluator review for phase %q met configured quality thresholds", p.Name)
	}
	return evidence.Claim{
		Claim:         claimText,
		Level:         evidence.BehaviorallyChecked,
		Checker:       "generator-evaluator loop",
		TrustBoundary: "LLM evaluator review of generated output",
		ArtifactPath:  evalReportRelativePath(vesselID),
		Phase:         p.Name,
		Passed:        passed,
		Timestamp:     recordedAt.UTC(),
	}
}

func appendEvidenceClaim(claims []evidence.Claim, claim *evidence.Claim) []evidence.Claim {
	if claim == nil {
		return append([]evidence.Claim(nil), claims...)
	}
	out := append([]evidence.Claim(nil), claims...)
	return append(out, *claim)
}

func phaseActionType(p *workflow.Phase) string {
	if p != nil && p.Type == "command" {
		return "external_command"
	}
	return "phase_execute"
}

const harnessMaintenanceDefaultBranchRule = "policy.class.no-main-commits"
const deliveryDefaultBranchRule = "delivery.default_branch_commits_denied"

// defaultBranchPushRule returns the audit rule string for default-branch push
// enforcement for the given workflow class and whether enforcement is active.
// Only classes that explicitly deny OpCommitDefaultBranch are enforced here;
// the Ops class is omitted intentionally (no default-branch push path in ops workflows).
func defaultBranchPushRule(class policy.Class) (ruleMatched string, enforced bool) {
	switch class {
	case policy.HarnessMaintenance:
		return harnessMaintenanceDefaultBranchRule, true
	case policy.Delivery:
		return deliveryDefaultBranchRule, true
	default:
		return "", false
	}
}

var controlPlanePathRe = regexp.MustCompile(`(^|[^A-Za-z0-9._/-])(\.xylem/HARNESS\.md|\.xylem\.yml|\.xylem/workflows/[A-Za-z0-9._/-]+\.yaml|\.xylem/prompts/[A-Za-z0-9._/-]+\.md)([^A-Za-z0-9._/-]|$)`)

func (r *Runner) enforcePhasePolicy(ctx context.Context, vessel queue.Vessel, sk *workflow.Workflow, p workflow.Phase, worktreePath, renderedCommand, renderedPrompt string) error {
	workflowClass := policy.Delivery
	if sk != nil && sk.Class != "" {
		workflowClass = sk.Class
	}

	for _, intent := range r.phasePolicyIntents(vessel, p, renderedCommand, renderedPrompt) {
		if guardErr := r.enforceDefaultBranchPushPolicy(ctx, vessel, workflowClass, p.Name, worktreePath, intent); guardErr != nil {
			return guardErr
		}
		if r.Intermediary == nil {
			continue
		}

		result := r.Intermediary.EvaluateWithContext(intent, intermediary.EvaluationContext{
			WorkflowClass: workflowClass,
			FilePath:      auditFilePath(worktreePath, intent.Resource),
			VesselID:      vessel.ID,
		})
		entry := intermediary.AuditEntry{
			Intent:        intent,
			Decision:      result.Effect,
			Timestamp:     time.Now().UTC(),
			WorkflowClass: string(workflowClass),
			Operation:     result.Operation,
			RuleMatched:   result.RuleMatched,
			FilePath:      result.FilePath,
			VesselID:      vessel.ID,
		}
		if result.Effect != intermediary.Allow {
			entry.Error = result.Reason
		}
		if err := r.appendAuditEntry(entry); err != nil {
			return fmt.Errorf("record audit evidence: %w", err)
		}
		if r.Intermediary.ShouldBlock(result.Effect) {
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
	for _, resource := range extractControlPlaneWriteResources(classificationText) {
		appendIntent("file_write", resource)
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

func indicatesControlPlaneWrite(rendered string) bool {
	lower := strings.ToLower(rendered)
	markers := []string{
		"write ",
		"edit ",
		"update ",
		"modify ",
		"change ",
		"create ",
		"append ",
		"overwrite ",
		"replace ",
		"rewrite ",
		"touch ",
		"echo ",
		"printf ",
		"tee ",
		"sed ",
		"cp ",
		"mv ",
		">",
		">>",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func extractControlPlaneWriteResources(rendered string) []string {
	if !indicatesControlPlaneWrite(rendered) {
		return nil
	}
	matches := controlPlanePathRe.FindAllStringSubmatch(rendered, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	resources := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		resource := match[2]
		if !intermediary.IsControlPlaneResource(resource) {
			continue
		}
		if _, ok := seen[resource]; ok {
			continue
		}
		seen[resource] = struct{}{}
		resources = append(resources, resource)
	}
	return resources
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

func auditFilePath(worktreePath, resource string) string {
	resource = strings.TrimSpace(resource)
	if worktreePath == "" || resource == "" || !intermediary.IsControlPlaneResource(resource) {
		return ""
	}
	if filepath.IsAbs(resource) {
		return resource
	}
	return filepath.Join(worktreePath, filepath.FromSlash(resource))
}

func normalizeBranchRef(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.Trim(ref, "\"'")
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if parts := strings.SplitN(ref, ":", 2); len(parts) == 2 {
		ref = parts[1]
	}
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return ref
}

func pushTargetsDefaultBranch(resource, defaultBranch string) bool {
	target := normalizeBranchRef(resource)
	defaultBranch = normalizeBranchRef(defaultBranch)
	return target != "" && defaultBranch != "" && target == defaultBranch
}

func (r *Runner) policyMode() intermediary.PolicyMode {
	if r != nil && r.Config != nil {
		return r.Config.HarnessPolicyMode()
	}
	if r != nil && r.Intermediary != nil {
		return r.Intermediary.Mode()
	}
	return intermediary.PolicyModeEnforce
}

func (r *Runner) enforceDefaultBranchPushPolicy(ctx context.Context, vessel queue.Vessel, workflowClass policy.Class, phaseName, worktreePath string, intent intermediary.Intent) error {
	ruleMatched, enforced := defaultBranchPushRule(workflowClass)
	if !enforced || intent.Action != "git_push" || worktreePath == "" {
		return nil
	}

	defaultBranch, err := r.detectDefaultBranchAtPath(ctx, worktreePath)
	if err != nil || !pushTargetsDefaultBranch(intent.Resource, defaultBranch) {
		// Cannot confirm the push targets the default branch; skip enforcement and
		// let the intermediary policy layer handle the intent instead.
		return nil
	}

	if err := r.appendAuditEntry(intermediary.AuditEntry{
		Intent:        intent,
		Decision:      intermediary.Deny,
		Timestamp:     time.Now().UTC(),
		Error:         ruleMatched,
		WorkflowClass: string(workflowClass),
		Operation:     string(policy.OpCommitDefaultBranch),
		RuleMatched:   ruleMatched,
		VesselID:      vessel.ID,
	}); err != nil {
		return fmt.Errorf("record default branch push denial: %w", err)
	}

	if r.policyMode() == intermediary.PolicyModeWarn {
		return nil
	}

	return formatPolicyDecisionError(phaseName, intent, intermediary.PolicyResult{
		Effect:        intermediary.Deny,
		Reason:        ruleMatched,
		WorkflowClass: workflowClass,
		Operation:     string(policy.OpCommitDefaultBranch),
		RuleMatched:   ruleMatched,
		VesselID:      vessel.ID,
	})
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
	if err := r.recordProtectedSurfaceViolations(vessel, p, worktreePath, errMsg, violations); err != nil {
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

	sk, _, err := r.loadWorkflow(vessel.Workflow)
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

func (r *Runner) recordProtectedSurfaceViolations(vessel queue.Vessel, p workflow.Phase, worktreePath, errMsg string, violations []surface.Violation) error {
	workflowClass, workflowClassErr := r.workflowClassForAudit(vessel)
	ruleMatched := ""
	if workflowClassErr != nil {
		log.Printf("runner: determine workflow class for audit for vessel %s workflow %q: %v", vessel.ID, vessel.Workflow, workflowClassErr)
		workflowClass = ""
	} else if classDecision := policy.Evaluate(workflowClass, policy.OpWriteControlPlane); !classDecision.Allowed {
		ruleMatched = classDecision.Rule
	}
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
			Decision:      intermediary.Deny,
			Timestamp:     time.Now().UTC(),
			Error:         errMsg,
			WorkflowClass: string(workflowClass),
			Operation:     string(policy.OpWriteControlPlane),
			RuleMatched:   ruleMatched,
			FilePath:      auditFilePath(worktreePath, violation.Path),
			VesselID:      vessel.ID,
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
	if entry.VesselID == "" {
		entry.VesselID = entry.Intent.AgentID
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

func (r *Runner) reportPhaseComplete(ctx context.Context, vessel queue.Vessel, phaseResult reporter.PhaseResult, output string) {
	issueNum := r.parseIssueNum(vessel)
	if issueNum <= 0 || r.Reporter == nil {
		return
	}
	r.logReporterError("post phase-complete comment", vessel.ID,
		r.Reporter.PhaseComplete(ctx, issueNum, phaseResult, output))
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

func (r *Runner) snapshotWorkflowDigest(vessel *queue.Vessel, digest string) error {
	if vessel == nil {
		return nil
	}
	vessel.WorkflowDigest = strings.TrimSpace(digest)
	state := recovery.RemediationStateFromMeta(vessel.Meta)
	state.WorkflowDigest = vessel.WorkflowDigest
	vessel.Meta = recovery.ApplyRemediationState(vessel.Meta, state)
	if r.Queue == nil {
		return nil
	}
	if err := r.Queue.UpdateVessel(*vessel); err != nil {
		return fmt.Errorf("persist workflow digest snapshot: %w", err)
	}
	return nil
}

func (r *Runner) loadVesselWorkflow(vessel *queue.Vessel) (*workflow.Workflow, string, error) {
	if vessel == nil {
		return nil, "", fmt.Errorf("vessel must not be nil")
	}

	storedDigest := strings.TrimSpace(vessel.WorkflowDigest)
	snapshotPath := r.workflowSnapshotPath(vessel.ID, vessel.Workflow)
	if storedDigest != "" && snapshotPath != "" {
		snapshot, snapshotDigest, err := r.loadWorkflowFromSnapshot(snapshotPath)
		if err == nil {
			if snapshotDigest != storedDigest {
				return nil, "", fmt.Errorf("workflow snapshot digest mismatch for vessel %s: stored=%s snapshot=%s", vessel.ID, storedDigest, snapshotDigest)
			}
			return snapshot, snapshotDigest, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}

	livePath := filepath.Join(".xylem", "workflows", vessel.Workflow+".yaml")
	sk, liveDigest, err := workflow.LoadWithDigest(livePath)
	if err != nil {
		return nil, "", err
	}
	if storedDigest != "" && vessel.CurrentPhase > 0 && storedDigest != liveDigest {
		return nil, "", fmt.Errorf("workflow snapshot missing for vessel %s: stored digest %s no longer matches live workflow %s", vessel.ID, storedDigest, liveDigest)
	}
	if snapshotPath != "" {
		if err := r.writeWorkflowSnapshot(livePath, snapshotPath); err != nil {
			return nil, "", err
		}
	}
	if err := r.snapshotWorkflowDigest(vessel, liveDigest); err != nil {
		return nil, "", fmt.Errorf("persist workflow snapshot: %w", err)
	}
	return sk, liveDigest, nil
}

func (r *Runner) loadWorkflowFromSnapshot(path string) (*workflow.Workflow, string, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, "", err
	}
	sk, digest, err := workflow.LoadWithDigest(path)
	if err != nil {
		return nil, "", fmt.Errorf("load workflow snapshot %q: %w", path, err)
	}
	return sk, digest, nil
}

func (r *Runner) writeWorkflowSnapshot(srcPath, snapshotPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read workflow source %q: %w", srcPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
		return fmt.Errorf("create workflow snapshot dir %q: %w", filepath.Dir(snapshotPath), err)
	}
	if err := os.WriteFile(snapshotPath, data, 0o644); err != nil {
		return fmt.Errorf("write workflow snapshot %q: %w", snapshotPath, err)
	}
	return nil
}

func (r *Runner) loadWorkflow(name string) (*workflow.Workflow, string, error) {
	path := filepath.Join(".xylem", "workflows", name+".yaml")
	return workflow.LoadWithDigest(path)
}

func (r *Runner) workflowClassForAudit(vessel queue.Vessel) (policy.Class, error) {
	if strings.TrimSpace(vessel.Workflow) == "" {
		return policy.Delivery, nil
	}
	sk, _, err := r.loadWorkflow(vessel.Workflow)
	if err != nil {
		return policy.Delivery, fmt.Errorf("load workflow %q: %w", vessel.Workflow, err)
	}
	if sk == nil || sk.Class == "" {
		return policy.Delivery, nil
	}
	return sk.Class, nil
}

func (r *Runner) workflowSnapshotPath(vesselID, workflowName string) string {
	if r == nil || r.Config == nil || r.Config.StateDir == "" || strings.TrimSpace(vesselID) == "" || strings.TrimSpace(workflowName) == "" {
		return ""
	}
	return config.RuntimePath(r.Config.StateDir, "phases", vesselID, workflowSnapshotDirName, workflowName+".yaml")
}

func (r *Runner) readHarness() string {
	data, err := os.ReadFile(filepath.Join(".xylem", "HARNESS.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

// githubIssueURLRe matches GitHub issue URLs and captures owner/repo and number.
var githubIssueURLRe = regexp.MustCompile(`^https?://github\.com/([^/]+/[^/]+)/issues/(\d+)`)

// githubPRURLRe matches GitHub pull request URLs and captures owner/repo and number.
var githubPRURLRe = regexp.MustCompile(`^https?://github\.com/([^/]+/[^/]+)/pull/(\d+)`)

func (r *Runner) fetchIssueData(ctx context.Context, vessel *queue.Vessel) phase.IssueData {
	switch vessel.Source {
	case "github-issue":
		return r.fetchGitHubData(ctx, vessel, "issue", "issue")
	case "github-pr", "github-pr-events", "github-merge":
		return r.fetchGitHubData(ctx, vessel, "pr", "pr")
	case "manual":
		return r.fetchManualIssueData(ctx, vessel)
	default:
		return phase.IssueData{}
	}
}

// fetchManualIssueData hydrates issue data for manual vessels whose ref
// matches a GitHub issue or PR URL pattern.
func (r *Runner) fetchManualIssueData(ctx context.Context, vessel *queue.Vessel) phase.IssueData {
	if vessel.Ref == "" {
		return phase.IssueData{}
	}

	if m := githubIssueURLRe.FindStringSubmatch(vessel.Ref); m != nil {
		repo, numStr := m[1], m[2]
		r.hydrateManualMeta(vessel, "issue_num", numStr, repo)
		return r.fetchGitHubData(ctx, vessel, "issue", "issue")
	}

	if m := githubPRURLRe.FindStringSubmatch(vessel.Ref); m != nil {
		repo, numStr := m[1], m[2]
		r.hydrateManualMeta(vessel, "pr_num", numStr, repo)
		return r.fetchGitHubData(ctx, vessel, "pr", "pr")
	}

	return phase.IssueData{}
}

// hydrateManualMeta sets the issue/PR number and source_repo in the vessel's
// Meta map so that parseIssueNum and resolveRepo can find them.
func (r *Runner) hydrateManualMeta(vessel *queue.Vessel, numKey, numStr, repo string) {
	if vessel.Meta == nil {
		vessel.Meta = make(map[string]string)
	}
	if vessel.Meta[numKey] == "" {
		vessel.Meta[numKey] = numStr
	}
	if vessel.Meta["source_repo"] == "" {
		vessel.Meta["source_repo"] = repo
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
	phasesDir := config.RuntimePath(r.Config.StateDir, "phases", vesselID)
	for _, p := range sk.Phases {
		outputPath := filepath.Join(phasesDir, p.Name+".output")
		data, err := os.ReadFile(outputPath)
		if err == nil {
			outputs[p.Name] = string(data)
		}
	}
	return outputs
}

func (r *Runner) touchPhaseActivity(path string) error {
	now := r.runtimeNow()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create activity dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open activity file: %w", err)
	}
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("close activity file: %w", closeErr)
	}
	if err := os.Chtimes(path, now, now); err != nil {
		return fmt.Errorf("update activity mtime: %w", err)
	}
	return nil
}

func (r *Runner) latestPhaseActivity(vesselID string) (string, time.Time, error) {
	phasesDir := config.RuntimePath(r.Config.StateDir, "phases", vesselID)
	entries, err := os.ReadDir(phasesDir)
	if err != nil {
		return "", time.Time{}, err
	}
	var (
		latestPhase string
		latestTime  time.Time
	)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".output") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", time.Time{}, err
		}
		if latestPhase == "" || info.ModTime().After(latestTime) {
			latestPhase = strings.TrimSuffix(entry.Name(), ".output")
			latestTime = info.ModTime()
		}
	}
	if latestPhase == "" {
		return "", time.Time{}, os.ErrNotExist
	}
	return latestPhase, latestTime, nil
}

func (r *Runner) markProcessStarted(vesselID, phaseName string, pid int) {
	r.processMu.Lock()
	defer r.processMu.Unlock()
	r.processes[vesselID] = trackedProcess{PID: pid, PhaseName: phaseName}
}

func (r *Runner) markProcessExited(vesselID string, pid int) {
	r.processMu.Lock()
	defer r.processMu.Unlock()
	current, ok := r.processes[vesselID]
	if !ok || current.PID != pid {
		return
	}
	current.Exited = true
	r.processes[vesselID] = current
}

func (r *Runner) clearTrackedProcess(vesselID string) {
	r.processMu.Lock()
	defer r.processMu.Unlock()
	delete(r.processes, vesselID)
}

func (r *Runner) trackedProcess(vesselID string) (trackedProcess, bool) {
	r.processMu.Lock()
	defer r.processMu.Unlock()
	proc, ok := r.processes[vesselID]
	return proc, ok
}

func (r *Runner) liveTrackedProcess(vesselID string) (trackedProcess, bool) {
	proc, ok := r.trackedProcess(vesselID)
	if !ok || proc.Exited || !processAlive(proc.PID) {
		return trackedProcess{}, false
	}
	return proc, true
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

func stopProcess(pid int, sleep func(context.Context, time.Duration) error) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("sigterm process %d: %w", pid, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		if sleep != nil {
			if err := sleep(context.Background(), 250*time.Millisecond); err != nil {
				break
			}
		} else {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if !processAlive(pid) {
		return nil
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("sigkill process %d: %w", pid, err)
	}
	return nil
}

type vesselProcessObserver struct {
	r         *Runner
	vesselID  string
	phaseName string
}

func (o vesselProcessObserver) ProcessStarted(pid int) {
	o.r.markProcessStarted(o.vesselID, o.phaseName, pid)
}

func (o vesselProcessObserver) ProcessExited(pid int) {
	o.r.markProcessExited(o.vesselID, pid)
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
		if vessel.Meta != nil && vessel.Meta["source_repo"] != "" {
			return vessel.Meta["source_repo"]
		}
		return ""
	}
}

func (r *Runner) resolveDefaultBranch() string {
	if r.Config != nil && strings.TrimSpace(r.Config.DefaultBranch) != "" {
		return strings.TrimSpace(r.Config.DefaultBranch)
	}
	return "main"
}

func (r *Runner) buildTemplateData(vessel queue.Vessel, wf *workflow.Workflow, issueData phase.IssueData, phaseName string, phaseIndex int, previousOutputs map[string]string, gateResult string, evaluation phase.EvaluationData) phase.TemplateData {
	sourceName := vessel.Source
	if configSource := r.sourceConfigNameFromMeta(vessel); configSource != "" {
		sourceName = configSource
	}
	repoSlug := strings.TrimSpace(r.resolveRepo(vessel))
	validation := phase.ValidationData{}
	if r.Config != nil {
		validation = phase.ValidationData{
			Format: strings.TrimSpace(r.Config.Validation.Format),
			Lint:   strings.TrimSpace(r.Config.Validation.Lint),
			Build:  strings.TrimSpace(r.Config.Validation.Build),
			Test:   strings.TrimSpace(r.Config.Validation.Test),
		}
	}
	var episodicCtx []memory.EpisodicEntry
	if r.EpisodicStore != nil && phaseIndex > 0 {
		entries, err := r.EpisodicStore.RecentForVessel(vessel.ID, 10)
		if err != nil {
			log.Printf("warn: read episodic context for vessel %s: %v", vessel.ID, err)
		} else {
			// Reverse so most-recent is first.
			for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
				entries[i], entries[j] = entries[j], entries[i]
			}
			episodicCtx = entries
		}
	}
	return phase.TemplateData{
		Date:  r.runtimeNow().UTC().Format("2006-01-02"),
		Issue: issueData,
		Phase: phase.PhaseData{
			Name:  phaseName,
			Index: phaseIndex,
		},
		PreviousOutputs:     previousOutputs,
		PreviousOutputOrder: orderedPreviousOutputNames(wf, phaseIndex, previousOutputs),
		GateResult:          gateResult,
		Evaluation:          evaluation,
		Vessel: phase.VesselData{
			ID:     vessel.ID,
			Ref:    vessel.Ref,
			Source: vessel.Source,
			Meta:   maps.Clone(vessel.Meta),
		},
		Repo: phase.RepoData{
			Slug:          repoSlug,
			DefaultBranch: r.resolveDefaultBranch(),
		},
		Source: phase.SourceData{
			Name: sourceName,
			Repo: repoSlug,
		},
		Validation:      validation,
		EpisodicContext: episodicCtx,
	}
}

func orderedPreviousOutputNames(wf *workflow.Workflow, phaseIndex int, previousOutputs map[string]string) []string {
	if len(previousOutputs) == 0 {
		return nil
	}
	ordered := make([]string, 0, len(previousOutputs))
	seen := make(map[string]struct{}, len(previousOutputs))
	if wf != nil {
		limit := phaseIndex
		if limit > len(wf.Phases) {
			limit = len(wf.Phases)
		}
		for i := 0; i < limit; i++ {
			name := wf.Phases[i].Name
			if _, ok := previousOutputs[name]; !ok {
				continue
			}
			ordered = append(ordered, name)
			seen[name] = struct{}{}
		}
	}
	extras := make([]string, 0, len(previousOutputs)-len(ordered))
	for name := range previousOutputs {
		if _, ok := seen[name]; ok {
			continue
		}
		extras = append(extras, name)
	}
	sort.Strings(extras)
	return append(ordered, extras...)
}

func phaseContextBudget(cfg *config.Config) int {
	if cfg == nil || cfg.Phase.ContextBudget <= 0 {
		return config.DefaultPhaseContextBudget
	}
	return cfg.Phase.ContextBudget
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
		r.annotateRecoveryMetadata(vessel.ID, queue.StateTimedOut, errMsg, traceContextPointer(vrs.trace))
		src := r.resolveSourceForVessel(vessel)
		if err := src.OnTimedOut(ctx, vessel); err != nil {
			log.Printf("warn: OnTimedOut hook for vessel %s: %v", vessel.ID, err)
		}
		r.persistRunArtifacts(vessel, string(queue.StateTimedOut), vrs, nil, r.runtimeNow())
		if r.Tracer != nil {
			if current, findErr := r.Queue.FindByID(vessel.ID); findErr == nil && current != nil {
				timeoutSpan.AddAttributes(observability.RecoveryAttributes(recoveryAttributesFromMeta(current.Meta)))
			}
		}
		r.finishWaitTransitionSpan(timeoutSpan, nil)

	}
}

func (r *Runner) CheckStalledVessels(ctx context.Context) []StallFinding {
	threshold := r.Config.Daemon.StallMonitor.PhaseStallThreshold
	if threshold == "" {
		return nil
	}
	stallThreshold, err := time.ParseDuration(threshold)
	if err != nil {
		log.Printf("warn: parse phase stall threshold: %v", err)
		return nil
	}

	running, err := r.Queue.ListByState(queue.StateRunning)
	if err != nil {
		log.Printf("warn: list running vessels for stall check: %v", err)
		return nil
	}

	findings := make([]StallFinding, 0)
	for _, vessel := range running {
		if r.Config.Daemon.StallMonitor.OrphanCheckEnabled {
			proc, ok := r.trackedProcess(vessel.ID)
			if ok && (proc.Exited || !processAlive(proc.PID)) {
				// Grace period: don't orphan-kill if the last phase completed
				// recently. Between phases the subprocess is legitimately dead
				// while the runner goroutine sets up the next phase.
				if _, modifiedAt, phaseErr := r.latestPhaseActivity(vessel.ID); phaseErr == nil {
					if r.runtimeSince(modifiedAt) < stallThreshold {
						continue // recent phase activity, vessel is transitioning
					}
				}
				msg := "vessel orphaned (no live subprocess)"
				log.Printf("warn: %s for vessel %s", msg, vessel.ID)
				if r.timeoutRunningVessel(ctx, vessel, msg) {
					findings = append(findings, StallFinding{
						Code:     "orphaned_subprocess",
						Level:    "critical",
						VesselID: vessel.ID,
						Message:  msg,
					})
				}
				continue
			}
		}

		if _, ok := r.liveTrackedProcess(vessel.ID); ok {
			continue
		}

		phaseName, modifiedAt, err := r.latestPhaseActivity(vessel.ID)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				log.Printf("warn: inspect phase activity for vessel %s: %v", vessel.ID, err)
			}
			continue
		}
		staleFor := r.runtimeSince(modifiedAt)
		if staleFor <= stallThreshold {
			continue
		}

		if proc, ok := r.trackedProcess(vessel.ID); ok && !proc.Exited && processAlive(proc.PID) {
			if err := stopProcess(proc.PID, r.runtimeSleep); err != nil {
				log.Printf("warn: stop stalled process for vessel %s: %v", vessel.ID, err)
			}
		}
		msg := fmt.Sprintf("phase stalled: no output for %s", staleFor.Truncate(time.Second))
		log.Printf("warn: %s for vessel %s (phase=%s)", msg, vessel.ID, phaseName)
		if r.timeoutRunningVessel(ctx, vessel, msg) {
			findings = append(findings, StallFinding{
				Code:     "phase_stalled",
				Level:    "critical",
				VesselID: vessel.ID,
				Phase:    phaseName,
				Message:  fmt.Sprintf("Vessel %s phase-stalled (%s no output on %s)", vessel.ID, staleFor.Truncate(time.Second), phaseName),
			})
		}
	}
	return findings
}

func (r *Runner) timeoutRunningVessel(ctx context.Context, vessel queue.Vessel, errMsg string) bool {
	r.clearTrackedProcess(vessel.ID)
	if updateErr := r.Queue.Update(vessel.ID, queue.StateTimedOut, errMsg); updateErr != nil {
		log.Printf("warn: failed to update vessel %s to timed_out: %v", vessel.ID, updateErr)
		return false
	}

	timeoutSpan := r.startWaitTransitionSpan(ctx, vessel, "timed_out", waitedDuration(vessel.StartedAt, r.runtimeNow()))
	vrs := newVesselRunState(r.Config, vessel, r.runtimeNow())
	vrs.setTraceContext(observability.TraceContextFromContext(timeoutSpan.Context()))
	r.annotateRecoveryMetadata(vessel.ID, queue.StateTimedOut, errMsg, traceContextPointer(vrs.trace))
	src := r.resolveSourceForVessel(vessel)
	if err := src.OnTimedOut(ctx, vessel); err != nil {
		log.Printf("warn: OnTimedOut hook for vessel %s: %v", vessel.ID, err)
	}
	r.persistRunArtifacts(vessel, string(queue.StateTimedOut), vrs, nil, r.runtimeNow())
	if r.Tracer != nil {
		if current, findErr := r.Queue.FindByID(vessel.ID); findErr == nil && current != nil {
			timeoutSpan.AddAttributes(observability.RecoveryAttributes(recoveryAttributesFromMeta(current.Meta)))
		}
	}
	r.finishWaitTransitionSpan(timeoutSpan, nil)
	return true
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

type providerInvocation struct {
	Provider     string
	Model        string
	Env          []string
	Command      string
	Args         []string
	StdinContent string
}

type providerErrorDisposition uint8

const (
	providerErrorFail providerErrorDisposition = iota
	providerErrorRetrySameProvider
	providerErrorFallbackNextProvider
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

func classifyProviderError(err error) providerErrorDisposition {
	if err == nil {
		return providerErrorFail
	}
	if isRateLimitError(err) {
		return providerErrorRetrySameProvider
	}

	msg := strings.ToLower(err.Error())
	fallbackPatterns := []string{
		"authentication failed",
		"authentication required",
		"authentication_error",
		"invalid api key",
		"api key is invalid",
		"missing api key",
		"invalid x-api-key",
		"anthropic_api_key",
		"github token",
		"github_token",
		"bad credentials",
		"invalid token",
		"token is invalid",
		"oauth token",
		"not authorized to access this model",
		"permission denied for model",
		"model access denied",
		"service unavailable",
		"temporarily unavailable",
		"provider unavailable",
		"overloaded_error",
		"model is overloaded",
		"currently overloaded",
		"executable file not found",
		"command not found",
		"no quota", // copilot: "402 You have no quota (Request ID: ...)"
	}
	for _, pattern := range fallbackPatterns {
		if strings.Contains(msg, pattern) {
			return providerErrorFallbackNextProvider
		}
	}
	if strings.Contains(msg, "unauthorized") &&
		(strings.Contains(msg, "api key") || strings.Contains(msg, "token") || strings.Contains(msg, "authentication")) {
		return providerErrorFallbackNextProvider
	}
	if strings.Contains(msg, "forbidden") &&
		(strings.Contains(msg, "api key") || strings.Contains(msg, "token") || strings.Contains(msg, "model")) {
		return providerErrorFallbackNextProvider
	}
	return providerErrorFail
}

// runPhaseWithRateLimitRetry wraps RunPhase with retry logic for API rate limit
// errors (HTTP 429). It retries up to rateLimitMaxRetries times with exponential
// backoff (30s, 60s, 120s) before returning the final error.
// stdinContent is re-wrapped in a fresh strings.Reader for each attempt;
// pass "" for nil stdin.
func (r *Runner) runPhaseWithRateLimitRetry(
	ctx context.Context, vesselID, phaseName, dir, stdinContent string, extraEnv []string, cmd string, args []string,
) ([]byte, error) {
	for attempt := 0; attempt <= rateLimitMaxRetries; attempt++ {
		var stdin io.Reader
		if stdinContent != "" {
			stdin = strings.NewReader(stdinContent)
		}
		var (
			output []byte
			err    error
		)
		if observedRunner, ok := r.Runner.(PhaseProcessEnvRunner); ok {
			output, err = observedRunner.RunPhaseObservedWithEnv(ctx, dir, extraEnv, stdin, vesselProcessObserver{
				r:         r,
				vesselID:  vesselID,
				phaseName: phaseName,
			}, cmd, args...)
		} else {
			output, err = r.Runner.RunPhaseWithEnv(ctx, dir, extraEnv, stdin, cmd, args...)
		}
		if err == nil || classifyProviderError(err) != providerErrorRetrySameProvider {
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

func (r *Runner) runPhaseWithProviderFallback(
	ctx context.Context, vesselID, phaseName, dir string, providers []string, build func(provider string) (providerInvocation, error),
) ([]byte, string, string, error) {
	if len(providers) == 0 {
		return nil, "", "", fmt.Errorf("no providers configured for phase %s", phaseName)
	}
	for idx, provider := range providers {
		invocation, err := build(provider)
		if err != nil {
			return nil, "", "", err
		}
		output, err := r.runPhaseWithRateLimitRetry(
			ctx,
			vesselID,
			phaseName,
			dir,
			invocation.StdinContent,
			invocation.Env,
			invocation.Command,
			invocation.Args,
		)
		if err == nil {
			return output, invocation.Provider, invocation.Model, nil
		}

		disposition := classifyProviderError(err)
		if disposition == providerErrorFail || idx == len(providers)-1 {
			return output, invocation.Provider, invocation.Model, err
		}

		switch disposition {
		case providerErrorRetrySameProvider:
			log.Printf("provider %s exhausted rate-limit retries, falling back to %s", invocation.Provider, providers[idx+1])
		case providerErrorFallbackNextProvider:
			log.Printf("provider %s unavailable for phase %s, falling back to %s: %v", invocation.Provider, phaseName, providers[idx+1], err)
		}
	}
	return nil, "", "", nil
}

func defaultRoutingTier(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.LLMRouting.DefaultTier) != "" {
		return strings.TrimSpace(cfg.LLMRouting.DefaultTier)
	}
	return config.DefaultLLMRoutingTier
}

func providerConfigForName(cfg *config.Config, providerName string) (config.ProviderConfig, bool) {
	if cfg == nil {
		return config.ProviderConfig{}, false
	}
	if cfg.Providers != nil {
		if provider, ok := cfg.Providers[providerName]; ok {
			return provider, true
		}
	}
	defaultTier := defaultRoutingTier(cfg)
	switch providerName {
	case "claude":
		return config.ProviderConfig{
			Kind:         "claude",
			Command:      cfg.Claude.Command,
			Flags:        cfg.Claude.Flags,
			Tiers:        map[string]string{defaultTier: cfg.Claude.DefaultModel},
			Env:          maps.Clone(cfg.Claude.Env),
			AllowedTools: append([]string(nil), cfg.Claude.AllowedTools...),
		}, cfg.Claude.Command != "" || cfg.Claude.DefaultModel != "" || len(cfg.Claude.Env) > 0 || len(cfg.Claude.AllowedTools) > 0
	case "copilot":
		return config.ProviderConfig{
			Kind:    "copilot",
			Command: cfg.Copilot.Command,
			Flags:   cfg.Copilot.Flags,
			Tiers:   map[string]string{defaultTier: cfg.Copilot.DefaultModel},
			Env:     maps.Clone(cfg.Copilot.Env),
		}, cfg.Copilot.Command != "" || cfg.Copilot.DefaultModel != "" || len(cfg.Copilot.Env) > 0
	default:
		return config.ProviderConfig{}, false
	}
}

func expandedProviderEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		value := os.ExpandEnv(env[key])
		if value == "" {
			continue
		}
		out = append(out, key+"="+value)
	}
	return out
}

func providerEnvForName(cfg *config.Config, providerName string) []string {
	provider, ok := providerConfigForName(cfg, providerName)
	if !ok {
		return nil
	}
	return expandedProviderEnv(provider.Env)
}

func parseAllowedToolsString(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	return mergeAllowedToolLists(parts, nil)
}

func mergeAllowedToolLists(first, second []string) []string {
	total := len(first) + len(second)
	if total == 0 {
		return nil
	}
	out := make([]string, 0, total)
	seen := make(map[string]struct{}, total)
	for _, list := range [][]string{first, second} {
		for _, item := range list {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func pointerStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// buildPhaseArgs constructs the claude-style CLI arguments for a phase invocation.
func buildPhaseArgs(cfg *config.Config, providerName string, provider config.ProviderConfig, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, tier, harnessContent string) []string {
	args := []string{"-p"}
	args = append(args, "--max-turns", fmt.Sprintf("%d", p.MaxTurns))

	model := resolvePhaseModel(cfg, srcCfg, wf, p, providerName, tier)

	// Add flags, stripping --model if we resolved one from the hierarchy
	if provider.Flags != "" {
		fields := strings.Fields(provider.Flags)
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

	for _, tool := range provider.AllowedTools {
		args = append(args, "--allowedTools", tool)
	}

	// Claude CLI uses --append-system-prompt, unlike Copilot's --system-prompt
	if harnessContent != "" {
		args = append(args, "--append-system-prompt", harnessContent)
	}

	return args
}

// buildCopilotPhaseArgs constructs the GitHub Copilot CLI arguments for a phase invocation.
func buildCopilotPhaseArgs(cfg *config.Config, providerName string, provider config.ProviderConfig, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, tier, harnessContent, renderedPrompt string) []string {
	// Combine harness + prompt into a single prompt text for -p.
	// Copilot has no --system-prompt or --append-system-prompt flag.
	promptText := renderedPrompt
	if harnessContent != "" {
		promptText = harnessContent + "\n\n" + renderedPrompt
	}

	args := []string{"-p", promptText, "-s"}

	model := resolvePhaseModel(cfg, srcCfg, wf, p, providerName, tier)

	// Add user flags, stripping flags we always prepend to avoid duplication.
	// -p/--prompt is value-aware (strips flag + its value); -s/--headless are boolean.
	if provider.Flags != "" {
		fields := strings.Fields(provider.Flags)
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
func buildProviderPhaseArgs(cfg *config.Config, providerCfg config.ProviderConfig, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, harnessContent, provider, tier, renderedPrompt string, attempt int) (string, []string, io.Reader, string, error) {
	model := resolvePhaseModel(cfg, srcCfg, wf, p, provider, tier)
	switch providerCfg.Kind {
	case "copilot":
		return providerCfg.Command, appendDTUProviderArgs(buildCopilotPhaseArgs(cfg, provider, providerCfg, srcCfg, wf, p, tier, harnessContent, renderedPrompt), p, attempt), nil, model, nil
	default:
		var stdin io.Reader
		if renderedPrompt != "" {
			stdin = strings.NewReader(renderedPrompt)
		}
		return providerCfg.Command, appendDTUProviderArgs(buildPhaseArgs(cfg, provider, providerCfg, srcCfg, wf, p, tier, harnessContent), p, attempt), stdin, model, nil
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

func resolveLegacyProvider(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase) string {
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

func resolveLegacyModel(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, provider string) string {
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

func resolveTier(cfg *config.Config, vessel queue.Vessel, wf *workflow.Workflow, p *workflow.Phase) string {
	if p != nil && p.Tier != nil && strings.TrimSpace(*p.Tier) != "" {
		return strings.TrimSpace(*p.Tier)
	}
	if wf != nil && wf.Tier != nil && strings.TrimSpace(*wf.Tier) != "" {
		return strings.TrimSpace(*wf.Tier)
	}
	if strings.TrimSpace(vessel.Tier) != "" {
		return strings.TrimSpace(vessel.Tier)
	}
	return defaultRoutingTier(cfg)
}

func resolveProviderChain(cfg *config.Config, tier string) []string {
	if cfg == nil {
		return []string{"claude"}
	}
	if len(cfg.LLMRouting.Tiers) == 0 {
		return []string{resolveLegacyProvider(cfg, nil, nil, nil)}
	}
	if routing, ok := cfg.LLMRouting.Tiers[tier]; ok && len(routing.Providers) > 0 {
		return append([]string(nil), routing.Providers...)
	}
	defaultTier := defaultRoutingTier(cfg)
	if tier != defaultTier {
		log.Printf("warn: llm routing tier %q missing, falling back to default tier %q", tier, defaultTier)
	}
	if routing, ok := cfg.LLMRouting.Tiers[defaultTier]; ok && len(routing.Providers) > 0 {
		return append([]string(nil), routing.Providers...)
	}
	return []string{resolveLegacyProvider(cfg, nil, nil, nil)}
}

func modelForProvider(cfg *config.Config, providerName, tier string) string {
	provider, ok := providerConfigForName(cfg, providerName)
	if !ok {
		return ""
	}
	return provider.Tiers[tier]
}

func usesLegacyProviderOverride(srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase) bool {
	if p != nil && p.Tier != nil {
		return false
	}
	if wf != nil && wf.Tier != nil {
		return false
	}
	if srcCfg != nil {
		if strings.TrimSpace(srcCfg.LLM) != "" || strings.TrimSpace(srcCfg.Model) != "" {
			return true
		}
	}
	if p != nil {
		if p.LLM != nil && strings.TrimSpace(*p.LLM) != "" {
			return true
		}
		if p.Model != nil && strings.TrimSpace(*p.Model) != "" {
			return true
		}
	}
	if wf != nil {
		if wf.LLM != nil && strings.TrimSpace(*wf.LLM) != "" {
			return true
		}
		if wf.Model != nil && strings.TrimSpace(*wf.Model) != "" {
			return true
		}
	}
	return false
}

func resolvePhaseProviderChain(cfg *config.Config, srcCfg *config.SourceConfig, vessel queue.Vessel, wf *workflow.Workflow, p *workflow.Phase) (string, []string) {
	tier := resolveTier(cfg, vessel, wf, p)
	if usesLegacyProviderOverride(srcCfg, wf, p) {
		return tier, []string{resolveLegacyProvider(cfg, srcCfg, wf, p)}
	}
	return tier, resolveProviderChain(cfg, tier)
}

func resolvePhaseModel(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, provider, tier string) string {
	if usesLegacyProviderOverride(srcCfg, wf, p) && provider == resolveLegacyProvider(cfg, srcCfg, wf, p) {
		return resolveLegacyModel(cfg, srcCfg, wf, p, provider)
	}
	return modelForProvider(cfg, provider, tier)
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
func buildPromptOnlyCmdArgs(cfg *config.Config, provider, tier, prompt string) (string, []string) {
	providerCfg, ok := providerConfigForName(cfg, provider)
	if !ok {
		return "", nil
	}
	model := modelForProvider(cfg, provider, tier)
	switch providerCfg.Kind {
	case "copilot":
		cmd := providerCfg.Command
		var args []string
		if prompt != "" {
			args = append(args, "-p", prompt, "-s")
		}
		if providerCfg.Flags != "" {
			fields := strings.Fields(providerCfg.Flags)
			fields = stripPromptFlag(fields)
			fields = stripBoolFlag(fields, "-s")
			fields = stripBoolFlag(fields, "--silent")
			fields = stripBoolFlag(fields, "--headless") // legacy: strip if present
			if model != "" {
				fields = stripModelFlag(fields)
			}
			args = append(args, fields...)
		}
		if model != "" {
			args = append(args, "--model", model)
		}
		return cmd, args
	default:
		cmd := providerCfg.Command
		args := []string{"-p"}
		if prompt != "" {
			args = append(args, prompt)
		}
		args = append(args, "--max-turns", fmt.Sprintf("%d", cfg.MaxTurns))
		if providerCfg.Flags != "" {
			fields := strings.Fields(providerCfg.Flags)
			if model != "" {
				fields = stripModelFlag(fields)
			}
			args = append(args, fields...)
		}
		if model != "" {
			args = append(args, "--model", model)
		}
		for _, tool := range providerCfg.AllowedTools {
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
