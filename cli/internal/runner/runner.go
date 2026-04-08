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
	"sort"
	"strconv"
	"strings"
	"sync"
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
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/surface"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
	oteltrace "go.opentelemetry.io/otel/trace"
)

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
	Completed int
	Failed    int
	Skipped   int
	Waiting   int
}

// Runner launches Claude sessions for queued vessels with concurrency control.
type Runner struct {
	Config   *config.Config
	Queue    *queue.Queue
	Worktree WorktreeManager
	Runner   CommandRunner
	Sources  map[string]source.Source
	Reporter *reporter.Reporter // may be nil for non-github vessels
	// Shared harness scaffolding for phase policy enforcement, audit logging,
	// protected-surface verification, and tracing.
	Intermediary *intermediary.Intermediary // nil = no policy enforcement
	AuditLog     *intermediary.AuditLog     // nil = no audit logging
	Tracer       *observability.Tracer      // nil = no tracing
}

// New creates a Runner.
func New(cfg *config.Config, q *queue.Queue, wt WorktreeManager, r CommandRunner) *Runner {
	return &Runner{Config: cfg, Queue: q, Worktree: wt, Runner: r}
}

// Drain dequeues pending vessels and launches sessions up to Config.Concurrency concurrently.
// On context cancellation, no new vessels are launched; running vessels complete normally.
func (r *Runner) Drain(ctx context.Context) (DrainResult, error) {
	var drainSpan observability.SpanContext
	if r.Tracer != nil {
		drainSpan = r.Tracer.StartSpan(ctx, "drain_run", observability.DrainSpanAttributes(observability.DrainSpanData{
			Concurrency: r.Config.Concurrency,
			Timeout:     r.Config.Timeout,
		}))
		ctx = drainSpan.Context()
		defer drainSpan.End()
	}

	timeout, err := time.ParseDuration(r.Config.Timeout)
	if err != nil {
		if r.Tracer != nil {
			drainSpan.RecordError(err)
		}
		return DrainResult{}, fmt.Errorf("parse timeout: %w", err)
	}

	sem := make(chan struct{}, r.Config.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var result DrainResult
	healthCounts := FleetStatusReport{}
	patternCounts := map[string]int{}

	for {
		select {
		case <-ctx.Done():
			goto wait
		default:
		}

		vessel, err := r.Queue.Dequeue()
		if err != nil || vessel == nil {
			break
		}

		log.Printf("%sdequeued vessel workflow=%s", vesselLabel(*vessel), vessel.Workflow)

		sem <- struct{}{}
		wg.Add(1)
		go func(j queue.Vessel) {
			defer wg.Done()
			defer func() { <-sem }()

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

			vesselCtx, cancel := context.WithTimeout(vesselBaseCtx, vesselTimeout)
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

			mu.Lock()
			switch outcome {
			case "completed":
				result.Completed++
			case "failed":
				result.Failed++
			case "waiting":
				result.Waiting++
			default:
				result.Skipped++
			}
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
			mu.Unlock()
		}(*vessel)
	}

wait:
	wg.Wait()
	if r.Tracer != nil {
		patterns := make([]FleetPattern, 0, len(patternCounts))
		for code, count := range patternCounts {
			patterns = append(patterns, FleetPattern{Code: code, Count: count})
		}
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
	}
	return result, nil
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
				if updateErr := r.Queue.Update(vessel.ID, queue.StateTimedOut, "label gate timed out"); updateErr != nil {
					log.Printf("warn: failed to update vessel %s to timed_out: %v", vessel.ID, updateErr)
				}
				src := r.resolveSource(vessel.Source)
				if err := src.OnTimedOut(ctx, vessel); err != nil {
					log.Printf("warn: OnTimedOut hook for vessel %s: %v", vessel.ID, err)
				}
				// Clean up worktree (best-effort)
				r.removeWorktree(vessel.WorktreePath, vessel.ID)
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
		if issueNum == 0 || repo == "" {
			continue
		}

		found, err := gate.CheckLabel(ctx, r.Runner, repo, issueNum, vessel.WaitingFor)
		if err != nil {
			log.Printf("warn: check label for vessel %s: %v", vessel.ID, err)
			continue
		}
		if found {
			// Advance past the gated phase — CurrentPhase was already incremented
			// when the vessel entered waiting state. Resume via pending so Drain can
			// pick the vessel back up through the normal dequeue flow.
			if err := r.Queue.Update(vessel.ID, queue.StatePending, ""); err != nil {
				log.Printf("warn: failed to resume vessel %s: %v", vessel.ID, err)
			}
		}
	}
}

func (r *Runner) runVessel(ctx context.Context, vessel queue.Vessel) (outcome string) {
	// Look up source for this vessel
	src := r.resolveSource(vessel.Source)

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
	var claims []evidence.Claim
	defer func() {
		if outcome != "failed" {
			return
		}
		r.persistRunArtifacts(vessel, string(queue.StateFailed), vrs, claims, r.runtimeNow())
	}()

	// Worktree: reuse if set (resuming from waiting), otherwise create
	worktreePath := vessel.WorktreePath
	if worktreePath == "" {
		branchName := src.BranchName(vessel)
		var err error
		worktreePath, err = r.Worktree.Create(ctx, branchName)
		if err != nil {
			r.failVessel(vessel.ID, err.Error())
			if err := src.OnFail(ctx, vessel); err != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
			}
			return "failed"
		}
		vessel.WorktreePath = worktreePath
		if updateErr := r.Queue.UpdateVessel(vessel); updateErr != nil {
			log.Printf("warn: failed to persist worktree path for %s: %v", vessel.ID, updateErr)
		}
	}

	// Prompt-only vessel (no workflow): single claude -p invocation
	if vessel.Workflow == "" && vessel.Prompt != "" {
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
		p := sk.Phases[i]
		gateResult := ""

		// Initialize gate retries for this phase (once, before retry loop)
		if p.Gate != nil && p.Gate.Type == "command" && p.Gate.Retries > 0 && vessel.GateRetries == 0 {
			vessel.GateRetries = p.Gate.Retries
		}

		// Gate retry loop: may re-run the same phase with gate output appended
		for {
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
			if p.Gate != nil && p.Gate.Type == "command" {
				retryAttempt = providerAttempt(&p, vessel.GateRetries)
			}
			phaseSpan := startPhaseSpan(r.Tracer, ctx, r.Config, srcCfg, sk, p, i)
			phaseSpanEnded := false
			var phaseDuration time.Duration
			finishCurrentPhaseSpan := func(err error) {
				if phaseSpanEnded {
					return
				}
				if phaseDuration == 0 {
					phaseDuration = r.runtimeSince(phaseStart)
				}
				finishPhaseSpan(r.Tracer, phaseSpan, buildPhaseResultData(r.Config, srcCfg, sk, p, promptForCost, string(output), phaseDuration), err)
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
				rendered, err := phase.RenderPrompt(p.Run, td)
				if err != nil {
					finishCurrentPhaseSpan(err)
					r.failVessel(vessel.ID, fmt.Sprintf("render command for phase %s: %v", p.Name, err))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					return "failed"
				}
				if err := validateCommandRender(p.Name, rendered); err != nil {
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
				if policyErr := r.enforcePhasePolicy(vessel, p); policyErr != nil {
					finishCurrentPhaseSpan(policyErr)
					log.Printf("%sphase %q blocked: %v", vesselLabel(vessel), p.Name, policyErr)
					vessel.FailedPhase = p.Name
					r.failVessel(vessel.ID, policyErr.Error())
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
				beforeSnapshot, checkProtectedSurfaces, err = r.takeProtectedSurfaceSnapshot(worktreePath)
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
				rendered, err := phase.RenderPrompt(string(promptContent), td)
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
				if policyErr := r.enforcePhasePolicy(vessel, p); policyErr != nil {
					finishCurrentPhaseSpan(policyErr)
					log.Printf("%sphase %q blocked: %v", vesselLabel(vessel), p.Name, policyErr)
					vessel.FailedPhase = p.Name
					r.failVessel(vessel.ID, policyErr.Error())
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
				beforeSnapshot, checkProtectedSurfaces, err = r.takeProtectedSurfaceSnapshot(worktreePath)
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

			// Shared: Write phase output
			outputPath := filepath.Join(phasesDir, p.Name+".output")
			if wErr := os.WriteFile(outputPath, output, 0o644); wErr != nil {
				log.Printf("warn: write output file %s: %v", outputPath, wErr)
			}
			fmt.Printf("Phase %s complete: %s\n", p.Name, outputPath)
			phaseDuration = r.runtimeSince(phaseStart)

			if runErr != nil {
				finishCurrentPhaseSpan(runErr)
				log.Printf("%sphase %q failed: %v", vesselLabel(vessel), p.Name, runErr)
				vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, 0, 0, 0.0, phaseDuration, "failed", nil, runErr.Error()))
				vessel.FailedPhase = p.Name
				r.failVessel(vessel.ID, fmt.Sprintf("phase %s: %v", p.Name, runErr))
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
					finishCurrentPhaseSpan(err)
					log.Printf("%sphase %q violated protected surfaces: %v", vesselLabel(vessel), p.Name, err)
					vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, 0, 0, 0.0, phaseDuration, "failed", nil, err.Error()))
					vessel.FailedPhase = p.Name
					r.failVessel(vessel.ID, err.Error())
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
				r.failVessel(vessel.ID, errMsg)
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				issueNum := r.parseIssueNum(vessel)
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post vessel-failed comment", vessel.ID,
						r.Reporter.VesselFailed(ctx, issueNum, p.Name, errMsg, ""))
				}
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
				log.Printf("warn: persist phase progress for %s: %v", vessel.ID, updateErr)
			}

			log.Printf("%sphase %q completed (%s)", vesselLabel(vessel), p.Name, phaseDuration.Truncate(time.Second))

			issueNum := r.parseIssueNum(vessel)

			phaseStatus := "completed"
			if phaseMatchedNoOp(&p, string(output)) {
				phaseStatus = "no-op"
			}

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
				return r.completeVessel(ctx, vessel, worktreePath, phaseResults, vrs, claims)
			}

			// Handle gate
			if p.Gate == nil {
				vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, phaseStatus, nil, ""))
				finishCurrentPhaseSpan(nil)
				break // no gate, proceed to next phase
			}

			switch p.Gate.Type {
			case "command":
				gateSpan := startGateSpan(r.Tracer, phaseSpan, ctx, p.Gate.Type)
				gateOut, passed, gateErr := gate.RunCommandGate(ctx, r.Runner, worktreePath, p.Gate.Run)
				finishGateSpan(r.Tracer, gateSpan, observability.GateSpanData{
					Type:         p.Gate.Type,
					Passed:       passed,
					RetryAttempt: retryAttempt,
				}, gateErr)
				if gateErr != nil {
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
					vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, phaseStatus, gatePassedPointer(true), ""))
					gateRecordedAt := r.runtimeNow()
					claims = append(claims, buildGateClaim(p, true, phaseArtifactRelativePath(vessel.ID, p.Name), gateRecordedAt))
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
					vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), "gate failed, retries exhausted"))
					finishCurrentPhaseSpan(nil)
					vessel.FailedPhase = p.Name
					vessel.GateOutput = gateOut
					r.failVessel(vessel.ID, fmt.Sprintf("phase %s: gate failed, retries exhausted", p.Name))
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
					log.Printf("warn: persist gate retries for %s: %v", vessel.ID, updateErr)
				}

				// Re-render prompt with gate output context
				gateResult = fmt.Sprintf("The following gate check failed after the previous phase. Fix the issues and try again:\n\n%s", gateOut)

				if err := r.runtimeSleep(ctx, retryDelay); err != nil {
					vrs.addPhase(vrs.phaseSummary(r.Config, srcCfg, sk, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), err.Error()))
					finishCurrentPhaseSpan(err)
					r.failVessel(vessel.ID, fmt.Sprintf("phase %s gate retry interrupted: %v", p.Name, err))
					if failErr := src.OnFail(ctx, vessel); failErr != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
					}
					return "failed"
				}
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
					log.Printf("warn: persist waiting state for %s: %v", vessel.ID, updateErr)
					finishCurrentPhaseSpan(updateErr)
					return "failed"
				}
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
				log.Printf("warn: persist gate retry reset for %s: %v", vessel.ID, updateErr)
			}
		}
	}

	// All phases complete
	log.Printf("%scompleted all phases", vesselLabel(vessel))
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

	cmd, args := buildPromptOnlyCmdArgs(r.Config, prompt)
	provider := resolveProvider(r.Config, nil, nil, nil)
	model := resolveModel(r.Config, nil, nil, nil, provider)
	beforeSnapshot, checkProtectedSurfaces, err := r.takeProtectedSurfaceSnapshot(worktreePath)
	if err != nil {
		snapErr := fmt.Errorf("protected surface snapshot failed: %w", err)
		r.failVessel(vessel.ID, snapErr.Error())
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		return "failed"
	}

	output, runErr := r.runPhaseWithRateLimitRetry(ctx, worktreePath, prompt, cmd, args)
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

	if err := src.OnComplete(ctx, vessel); err != nil {
		log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
	}

	return r.completeVessel(ctx, vessel, worktreePath, nil, vrs, nil)
}

func (r *Runner) resolveSource(name string) source.Source {
	if r.Sources != nil {
		if src, ok := r.Sources[name]; ok {
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

func (r *Runner) removeWorktree(worktreePath, vesselID string) {
	if worktreePath == "" {
		return
	}
	if removeErr := r.Worktree.Remove(context.Background(), worktreePath); removeErr != nil {
		log.Printf("warn: failed to remove worktree for %s: %v", vesselID, removeErr)
	}
}

func (r *Runner) failVessel(id string, errMsg string) {
	if updateErr := r.Queue.Update(id, queue.StateFailed, errMsg); updateErr != nil {
		log.Printf("warn: failed to update vessel %s state: %v", id, updateErr)
	}
}

func (r *Runner) completeVessel(ctx context.Context, vessel queue.Vessel, worktreePath string, phaseResults []reporter.PhaseResult, vrs *vesselRunState, claims []evidence.Claim) string {
	if updateErr := r.Queue.Update(vessel.ID, queue.StateCompleted, ""); updateErr != nil {
		log.Printf("warn: failed to update vessel %s state: %v", vessel.ID, updateErr)
	}

	manifest := r.persistRunArtifacts(vessel, string(queue.StateCompleted), vrs, claims, r.runtimeNow())

	// Clean up worktree (best-effort)
	r.removeWorktree(worktreePath, vessel.ID)

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
		}
	}

	if err := SaveVesselSummary(r.Config.StateDir, summary); err != nil {
		log.Printf("warn: save vessel summary: %v", err)
	}

	return manifest
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
		if waveWaiting {
			return "waiting"
		}
		if waveNoOp {
			log.Printf("%sphase triggered no-op; completing workflow early", vesselLabel(vessel))
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
	status        string // "completed", "no-op", "failed", "waiting"
	duration      time.Duration
	gateOut       string
	phaseSummary  PhaseSummary
	evidenceClaim *evidence.Claim
}

// runSinglePhase executes a single workflow phase (prompt or command), including
// gate evaluation and retries. It returns the outcome without mutating the
// vessel's queue state directly (the caller handles that).
func (r *Runner) runSinglePhase(ctx context.Context, vessel queue.Vessel, wf *workflow.Workflow, phaseIdx int, previousOutputs map[string]string, issueData phase.IssueData, harnessContent, worktreePath string, src source.Source, vrs *vesselRunState, enforceBudget bool) singlePhaseResult {
	p := wf.Phases[phaseIdx]
	gateResult := ""
	gateRetries := 0
	if p.Gate != nil && p.Gate.Type == "command" && p.Gate.Retries > 0 {
		gateRetries = p.Gate.Retries
	}

	srcCfg := r.sourceConfigFromMeta(vessel)

	for {
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
		if p.Gate != nil && p.Gate.Type == "command" {
			retryAttempt = providerAttempt(&p, gateRetries)
		}
		phaseSpan := startPhaseSpan(r.Tracer, ctx, r.Config, srcCfg, wf, p, phaseIdx)
		phaseSpanEnded := false
		var phaseDuration time.Duration
		finishCurrentPhaseSpan := func(err error) {
			if phaseSpanEnded {
				return
			}
			if phaseDuration == 0 {
				phaseDuration = r.runtimeSince(phaseStart)
			}
			finishPhaseSpan(r.Tracer, phaseSpan, buildPhaseResultData(r.Config, srcCfg, wf, p, promptForCost, string(output), phaseDuration), err)
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
			rendered, err := phase.RenderPrompt(p.Run, td)
			if err != nil {
				finishCurrentPhaseSpan(err)
				r.failVessel(vessel.ID, fmt.Sprintf("render command for phase %s: %v", p.Name, err))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
			}
			if err := validateCommandRender(p.Name, rendered); err != nil {
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
			if policyErr := r.enforcePhasePolicy(vessel, p); policyErr != nil {
				finishCurrentPhaseSpan(policyErr)
				log.Printf("%sphase %q blocked: %v", vesselLabel(vessel), p.Name, policyErr)
				vessel.FailedPhase = p.Name
				r.failVessel(vessel.ID, policyErr.Error())
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
			beforeSnapshot, checkProtectedSurfaces, err = r.takeProtectedSurfaceSnapshot(worktreePath)
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
			rendered, err := phase.RenderPrompt(string(promptContent), td)
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
			if policyErr := r.enforcePhasePolicy(vessel, p); policyErr != nil {
				finishCurrentPhaseSpan(policyErr)
				log.Printf("%sphase %q blocked: %v", vesselLabel(vessel), p.Name, policyErr)
				vessel.FailedPhase = p.Name
				r.failVessel(vessel.ID, policyErr.Error())
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
			beforeSnapshot, checkProtectedSurfaces, err = r.takeProtectedSurfaceSnapshot(worktreePath)
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

		// Write output file.
		outputPath := filepath.Join(phasesDir, p.Name+".output")
		if wErr := os.WriteFile(outputPath, output, 0o644); wErr != nil {
			log.Printf("warn: write output file %s: %v", outputPath, wErr)
		}
		fmt.Printf("Phase %s complete: %s\n", p.Name, outputPath)
		phaseDuration = r.runtimeSince(phaseStart)

		if runErr != nil {
			finishCurrentPhaseSpan(runErr)
			log.Printf("%sphase %q failed: %v", vesselLabel(vessel), p.Name, runErr)
			vessel.FailedPhase = p.Name
			r.failVessel(vessel.ID, fmt.Sprintf("phase %s: %v", p.Name, runErr))
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
				finishCurrentPhaseSpan(err)
				log.Printf("%sphase %q violated protected surfaces: %v", vesselLabel(vessel), p.Name, err)
				vessel.FailedPhase = p.Name
				r.failVessel(vessel.ID, err.Error())
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
			r.failVessel(vessel.ID, errMsg)
			if err := src.OnFail(ctx, vessel); err != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
			}
			issueNum := r.parseIssueNum(vessel)
			if issueNum > 0 && r.Reporter != nil {
				r.logReporterError("post vessel-failed comment", vessel.ID,
					r.Reporter.VesselFailed(ctx, issueNum, p.Name, errMsg, ""))
			}
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
		case "command":
			gateSpan := startGateSpan(r.Tracer, phaseSpan, ctx, p.Gate.Type)
			gateOut, passed, gateErr := gate.RunCommandGate(ctx, r.Runner, worktreePath, p.Gate.Run)
			finishGateSpan(r.Tracer, gateSpan, observability.GateSpanData{
				Type:         p.Gate.Type,
				Passed:       passed,
				RetryAttempt: retryAttempt,
			}, gateErr)
			if gateErr != nil {
				r.failVessel(vessel.ID, fmt.Sprintf("phase %s gate error: %v", p.Name, gateErr))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
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
				gateRecordedAt := r.runtimeNow()
				claim := buildGateClaim(p, true, phaseArtifactRelativePath(vessel.ID, p.Name), gateRecordedAt)
				finishCurrentPhaseSpan(nil)
				return singlePhaseResult{
					output:        string(output),
					status:        "completed",
					duration:      phaseDuration,
					phaseSummary:  vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "completed", gatePassedPointer(true), ""),
					evidenceClaim: &claim,
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
				r.failVessel(vessel.ID, fmt.Sprintf("phase %s: gate failed, retries exhausted", p.Name))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				if issueNum > 0 && r.Reporter != nil {
					r.logReporterError("post vessel-failed comment", vessel.ID,
						r.Reporter.VesselFailed(ctx, issueNum, p.Name, "gate failed, retries exhausted", gateOut))
				}
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
				r.failVessel(vessel.ID, fmt.Sprintf("phase %s gate retry interrupted: %v", p.Name, err))
				if failErr := src.OnFail(ctx, vessel); failErr != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
				}
				finishCurrentPhaseSpan(err)
				return singlePhaseResult{
					status:       "failed",
					duration:     phaseDuration,
					gateOut:      gateOut,
					phaseSummary: vrs.phaseSummary(r.Config, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, phaseDuration, "failed", gatePassedPointer(false), err.Error()),
				}
			}
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
				log.Printf("warn: persist waiting state for %s: %v", vessel.ID, updateErr)
				finishCurrentPhaseSpan(updateErr)
				return singlePhaseResult{status: "failed", duration: r.runtimeSince(phaseStart)}
			}
			finishCurrentPhaseSpan(nil)
			return singlePhaseResult{output: string(output), status: "waiting", duration: r.runtimeSince(phaseStart)}
		}

		// Unknown gate type: treat as passed.
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

func startPhaseSpan(tracer *observability.Tracer, ctx context.Context, cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p workflow.Phase, phaseIdx int) observability.SpanContext {
	if tracer == nil {
		return observability.SpanContext{}
	}

	provider := resolveProvider(cfg, srcCfg, wf, &p)
	model := resolveModel(cfg, srcCfg, wf, &p, provider)

	return tracer.StartSpan(ctx, "phase:"+p.Name, observability.PhaseSpanAttributes(observability.PhaseSpanData{
		Name:     p.Name,
		Index:    phaseIdx,
		Type:     phaseTypeLabel(p),
		Provider: provider,
		Model:    model,
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

func buildPhaseResultData(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p workflow.Phase, renderedPrompt, output string, duration time.Duration) observability.PhaseResultData {
	data := observability.PhaseResultData{
		DurationMS: duration.Milliseconds(),
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
		claim.Checker = p.Gate.Run
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

func (r *Runner) enforcePhasePolicy(vessel queue.Vessel, p workflow.Phase) error {
	if r.Intermediary == nil {
		return nil
	}

	justification := fmt.Sprintf("execute workflow phase %q", p.Name)
	if vessel.Workflow != "" {
		justification = fmt.Sprintf("execute phase %q of workflow %q", p.Name, vessel.Workflow)
	}

	intent := intermediary.Intent{
		Action:        phaseActionType(&p),
		Resource:      p.Name,
		AgentID:       vessel.ID,
		Justification: justification,
		Metadata: map[string]string{
			"phase":      p.Name,
			"source":     vessel.Source,
			"workflow":   vessel.Workflow,
			"phase_type": p.Type,
		},
	}
	if intent.Metadata["phase_type"] == "" {
		intent.Metadata["phase_type"] = "prompt"
	}

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

	switch result.Effect {
	case intermediary.Allow:
		return nil
	case intermediary.RequireApproval:
		if result.Reason != "" {
			return fmt.Errorf("phase %q requires approval (automatic approval not yet supported): %s", p.Name, result.Reason)
		}
		return fmt.Errorf("phase %q requires approval (automatic approval not yet supported)", p.Name)
	case intermediary.Deny:
		if result.Reason != "" {
			return fmt.Errorf("phase %q denied by policy: %s", p.Name, result.Reason)
		}
		return fmt.Errorf("phase %q denied by policy", p.Name)
	default:
		if result.Reason != "" {
			return fmt.Errorf("phase %q denied by policy: %s", p.Name, result.Reason)
		}
		return fmt.Errorf("phase %q denied by policy", p.Name)
	}
}

func (r *Runner) takeProtectedSurfaceSnapshot(worktreePath string) (surface.Snapshot, bool, error) {
	patterns := r.Config.EffectiveProtectedSurfaces()
	if len(patterns) == 0 {
		return surface.Snapshot{}, false, nil
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

	after, err := surface.TakeSnapshot(worktreePath, patterns)
	if err != nil {
		log.Printf("%sphase %q protected surface verification skipped: %v",
			vesselLabel(vessel), p.Name, err)
		return nil
	}

	violations := surface.Compare(before, after)
	if len(violations) == 0 {
		return nil
	}

	errMsg := fmt.Sprintf("phase %q violated protected surfaces: %s", p.Name, formatViolations(violations))
	if err := r.recordProtectedSurfaceViolations(vessel, p, errMsg, violations); err != nil {
		return fmt.Errorf("%s (record audit evidence: %w)", errMsg, err)
	}
	return fmt.Errorf("%s", errMsg)
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
	if name == "" {
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
	if r.Sources == nil {
		return ""
	}
	src, ok := r.Sources[vessel.Source]
	if !ok {
		return ""
	}
	switch s := src.(type) {
	case *source.GitHub:
		return s.Repo
	case *source.GitHubPR:
		return s.Repo
	case *source.GitHubPREvents:
		return s.Repo
	case *source.GitHubMerge:
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

// validateCommandRender checks the rendered command string for unresolved
// template variables (leftover "{{" sequences). This catches cases where
// template data has zero values that cause confusing downstream failures.
func validateCommandRender(phaseName, rendered string) error {
	if strings.Contains(rendered, "{{") {
		return fmt.Errorf("command phase %s: unresolved template variable in: %s", phaseName, rendered)
	}
	return nil
}

// validateIssueDataForWorkflow returns an error when the workflow contains
// command phases that reference .Issue template variables but the issue data
// has a zero Number. This prevents commands like `gh pr merge 0` from
// executing with confusing results.
func validateIssueDataForWorkflow(vessel queue.Vessel, data phase.IssueData, wf *workflow.Workflow) error {
	if wf == nil {
		return nil
	}
	for _, p := range wf.Phases {
		if p.Type != "command" {
			continue
		}
		if strings.Contains(p.Run, ".Issue.") && data.Number == 0 {
			return fmt.Errorf("command phase %s references .Issue but issue data is unavailable for vessel %s (Number is 0)", p.Name, vessel.ID)
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

		// Clean up worktree (best-effort)
		r.removeWorktree(vessel.WorktreePath, vessel.ID)
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

// isRateLimitError reports whether the error indicates an API rate limit (HTTP 429).
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "rate_limit_error")
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
