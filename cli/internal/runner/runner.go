package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/gate"
	"github.com/nicholls-inc/xylem/cli/internal/orchestrator"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
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
}

// New creates a Runner.
func New(cfg *config.Config, q *queue.Queue, wt WorktreeManager, r CommandRunner) *Runner {
	return &Runner{Config: cfg, Queue: q, Worktree: wt, Runner: r}
}

// Drain dequeues pending vessels and launches sessions up to Config.Concurrency concurrently.
// On context cancellation, no new vessels are launched; running vessels complete normally.
func (r *Runner) Drain(ctx context.Context) (DrainResult, error) {
	timeout, err := time.ParseDuration(r.Config.Timeout)
	if err != nil {
		return DrainResult{}, fmt.Errorf("parse timeout: %w", err)
	}

	sem := make(chan struct{}, r.Config.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var result DrainResult

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

			vesselCtx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			outcome := r.runVessel(vesselCtx, j)

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
			mu.Unlock()
		}(*vessel)
	}

wait:
	wg.Wait()
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
			if time.Since(*vessel.WaitingSince) > timeoutDur {
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
						r.Reporter.LabelTimeout(ctx, issueNum, vessel.WaitingFor, vessel.FailedPhase, time.Since(*vessel.WaitingSince)))
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
			// when the vessel entered waiting state.
			if err := r.Queue.Update(vessel.ID, queue.StateRunning, ""); err != nil {
				log.Printf("warn: failed to resume vessel %s: %v", vessel.ID, err)
			}
		}
	}
}

func (r *Runner) runVessel(ctx context.Context, vessel queue.Vessel) string {
	// Look up source for this vessel
	src := r.resolveSource(vessel.Source)

	// Source-specific start hook (e.g., add in-progress label)
	if err := src.OnStart(ctx, vessel); err != nil {
		log.Printf("warn: source OnStart for %s: %v", vessel.ID, err)
	}

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
		return r.runPromptOnly(ctx, vessel, worktreePath, src)
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

	// Read harness file
	harnessContent := r.readHarness()

	// Orchestrator-driven execution for workflows with explicit phase dependencies.
	// This enables parallel phase execution within waves and context firewalls.
	if sk.HasDependencies() {
		return r.runVesselOrchestrated(ctx, vessel, sk, issueData, harnessContent, worktreePath, src)
	}

	// Rebuild previousOutputs from .xylem/phases/<id>/*.output (for resume)
	previousOutputs := r.rebuildPreviousOutputs(vessel.ID, sk)

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
			phaseStart := time.Now()

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

			// Create phases dir early
			phasesDir := filepath.Join(r.Config.StateDir, "phases", vessel.ID)
			if err := os.MkdirAll(phasesDir, 0o755); err != nil {
				r.failVessel(vessel.ID, fmt.Sprintf("create phases dir: %v", err))
				if failErr := src.OnFail(ctx, vessel); failErr != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
				}
				return "failed"
			}

			var output []byte
			var runErr error

			if p.Type == "command" {
				// Command phase: render and execute shell command
				rendered, err := phase.RenderPrompt(p.Run, td)
				if err != nil {
					r.failVessel(vessel.ID, fmt.Sprintf("render command for phase %s: %v", p.Name, err))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					return "failed"
				}
				if wErr := os.WriteFile(filepath.Join(phasesDir, p.Name+".command"), []byte(rendered), 0o644); wErr != nil {
					log.Printf("warn: write command file: %v", wErr)
				}
				cmdOut, cmdErr := gate.RunCommand(ctx, r.Runner, worktreePath, rendered)
				output = []byte(cmdOut)
				runErr = cmdErr
			} else {
				// LLM phase: existing code
				promptContent, err := os.ReadFile(p.PromptFile)
				if err != nil {
					r.failVessel(vessel.ID, fmt.Sprintf("read prompt file %s: %v", p.PromptFile, err))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					return "failed"
				}
				rendered, err := phase.RenderPrompt(string(promptContent), td)
				if err != nil {
					r.failVessel(vessel.ID, fmt.Sprintf("render prompt for phase %s: %v", p.Name, err))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					return "failed"
				}
				promptPath := filepath.Join(phasesDir, p.Name+".prompt")
				if wErr := os.WriteFile(promptPath, []byte(rendered), 0o644); wErr != nil {
					log.Printf("warn: write prompt file %s: %v", promptPath, wErr)
				}
				srcCfg := r.sourceConfigFromMeta(vessel)
				provider := resolveProvider(r.Config, srcCfg, sk, &p)
				cmd, args, phaseStdin := buildProviderPhaseArgs(r.Config, srcCfg, sk, &p, harnessContent, provider, rendered)
				output, runErr = r.Runner.RunPhase(ctx, worktreePath, phaseStdin, cmd, args...)
			}

			// Shared: Write phase output
			outputPath := filepath.Join(phasesDir, p.Name+".output")
			if wErr := os.WriteFile(outputPath, output, 0o644); wErr != nil {
				log.Printf("warn: write output file %s: %v", outputPath, wErr)
			}
			fmt.Printf("Phase %s complete: %s\n", p.Name, outputPath)

			phaseDuration := time.Since(phaseStart)

			if runErr != nil {
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
				log.Printf("%sphase %q triggered no-op; completing workflow early", vesselLabel(vessel), p.Name)
				return r.completeVessel(ctx, vessel, worktreePath, phaseResults)
			}

			// Handle gate
			if p.Gate == nil {
				break // no gate, proceed to next phase
			}

			switch p.Gate.Type {
			case "command":
				gateOut, passed, gateErr := gate.RunCommandGate(ctx, r.Runner, worktreePath, p.Gate.Run)
				if gateErr != nil {
					r.failVessel(vessel.ID, fmt.Sprintf("phase %s gate error: %v", p.Name, gateErr))
					if err := src.OnFail(ctx, vessel); err != nil {
						log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
					}
					return "failed"
				}
				if passed {
					log.Printf("%sgate passed for phase %q", vesselLabel(vessel), p.Name)
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

				time.Sleep(retryDelay)
				continue // re-run same phase

			case "label":
				log.Printf("%swaiting for label %q after phase %q", vesselLabel(vessel), p.Gate.WaitFor, p.Name)
				// Set vessel to waiting state
				vessel.WaitingFor = p.Gate.WaitFor
				now := time.Now().UTC()
				vessel.WaitingSince = &now
				vessel.State = queue.StateWaiting
				if updateErr := r.Queue.UpdateVessel(vessel); updateErr != nil {
					log.Printf("warn: persist waiting state for %s: %v", vessel.ID, updateErr)
				}
				// Update queue state
				if updateErr := r.Queue.Update(vessel.ID, queue.StateWaiting, ""); updateErr != nil {
					log.Printf("warn: failed to set vessel %s to waiting: %v", vessel.ID, updateErr)
				}
				return "waiting"
			}

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
	return r.completeVessel(ctx, vessel, worktreePath, phaseResults)
}

// runPromptOnly handles vessels with a prompt but no workflow.
func (r *Runner) runPromptOnly(ctx context.Context, vessel queue.Vessel, worktreePath string, src source.Source) string {
	prompt := vessel.Prompt
	if vessel.Ref != "" {
		prompt = fmt.Sprintf("Ref: %s\n\n%s", vessel.Ref, vessel.Prompt)
	}

	cmd, args := buildPromptOnlyCmdArgs(r.Config, prompt)

	_, runErr := r.Runner.RunPhase(ctx, worktreePath, strings.NewReader(prompt), cmd, args...)

	if runErr != nil {
		r.failVessel(vessel.ID, runErr.Error())
		if err := src.OnFail(ctx, vessel); err != nil {
			log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
		}
		return "failed"
	}

	if updateErr := r.Queue.Update(vessel.ID, queue.StateCompleted, ""); updateErr != nil {
		log.Printf("warn: failed to update vessel %s state: %v", vessel.ID, updateErr)
	}
	if err := src.OnComplete(ctx, vessel); err != nil {
		log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
	}

	// Clean up worktree (best-effort)
	r.removeWorktree(worktreePath, vessel.ID)

	return "completed"
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

func (r *Runner) completeVessel(ctx context.Context, vessel queue.Vessel, worktreePath string, phaseResults []reporter.PhaseResult) string {
	if updateErr := r.Queue.Update(vessel.ID, queue.StateCompleted, ""); updateErr != nil {
		log.Printf("warn: failed to update vessel %s state: %v", vessel.ID, updateErr)
	}

	// Clean up worktree (best-effort)
	r.removeWorktree(worktreePath, vessel.ID)

	// Report completion
	issueNum := r.parseIssueNum(vessel)
	if issueNum > 0 && r.Reporter != nil {
		r.logReporterError("post vessel-completed comment", vessel.ID,
			r.Reporter.VesselCompleted(ctx, issueNum, phaseResults))
	}

	return "completed"
}

// runVesselOrchestrated executes a workflow with explicit phase dependencies
// using the orchestrator for tracking and wave-based parallel execution.
// Phases within the same wave (no dependencies between them) run concurrently.
// Context firewalls ensure each phase only sees outputs from its declared dependencies.
func (r *Runner) runVesselOrchestrated(ctx context.Context, vessel queue.Vessel, wf *workflow.Workflow, issueData phase.IssueData, harnessContent, worktreePath string, src source.Source) string {
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

	for _, wave := range graph.waves {
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
			output   string
			status   string // "completed", "no-op", "failed", "waiting"
			duration time.Duration
			gateOut  string
		}

		results := make([]waveResult, len(pending))

		if len(pending) == 1 {
			// Single phase: run inline (no goroutine overhead).
			idx := pending[0]
			depOutputs := graph.dependencyOutputs(idx, allOutputs)
			res := r.runSinglePhase(ctx, vessel, wf, idx, depOutputs, issueData, harnessContent, worktreePath, src)
			results[0] = waveResult{phaseIdx: idx, output: res.output, status: res.status, duration: res.duration, gateOut: res.gateOut}
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

					res := r.runSinglePhase(ctx, vessel, wf, idx, depOutputs, issueData, harnessContent, worktreePath, src)

					mu.Lock()
					results[ri] = waveResult{phaseIdx: idx, output: res.output, status: res.status, duration: res.duration, gateOut: res.gateOut}
					mu.Unlock()
				}(ri, idx)
			}
			wg.Wait()
		}

		// Process wave results: update orchestrator, collect outputs.
		for _, res := range results {
			p := wf.Phases[res.phaseIdx]

			switch res.status {
			case "completed", "no-op":
				_ = graph.orch.UpdateAgent(p.Name, orchestrator.StatusCompleted, 0, res.duration, "")
				_ = graph.orch.SetResult(orchestrator.SubAgentResult{
					AgentID: p.Name,
					Summary: res.output,
					Success: true,
				})
				allOutputs[p.Name] = res.output
			case "failed":
				_ = graph.orch.UpdateAgent(p.Name, orchestrator.StatusFailed, 0, res.duration, res.gateOut)
				// Fail-fast: stop processing.
				return "failed"
			case "waiting":
				return "waiting"
			}

			allPhaseResults = append(allPhaseResults, reporter.PhaseResult{
				Name:     p.Name,
				Duration: res.duration,
				Status:   res.status,
			})

			if res.status == "no-op" {
				log.Printf("%sphase %q triggered no-op; completing workflow early", vesselLabel(vessel), p.Name)
				if err := src.OnComplete(ctx, vessel); err != nil {
					log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
				}
				return r.completeVessel(ctx, vessel, worktreePath, allPhaseResults)
			}
		}
	}

	// All waves complete.
	log.Printf("%scompleted all phases (orchestrated, %d waves)", vesselLabel(vessel), len(graph.waves))
	if err := src.OnComplete(ctx, vessel); err != nil {
		log.Printf("warn: OnComplete hook for vessel %s: %v", vessel.ID, err)
	}
	return r.completeVessel(ctx, vessel, worktreePath, allPhaseResults)
}

// singlePhaseResult holds the outcome of executing one phase including its gate.
type singlePhaseResult struct {
	output   string
	status   string // "completed", "no-op", "failed", "waiting"
	duration time.Duration
	gateOut  string
}

// runSinglePhase executes a single workflow phase (prompt or command), including
// gate evaluation and retries. It returns the outcome without mutating the
// vessel's queue state directly (the caller handles that).
func (r *Runner) runSinglePhase(ctx context.Context, vessel queue.Vessel, wf *workflow.Workflow, phaseIdx int, previousOutputs map[string]string, issueData phase.IssueData, harnessContent, worktreePath string, src source.Source) singlePhaseResult {
	p := wf.Phases[phaseIdx]
	gateResult := ""
	gateRetries := 0
	if p.Gate != nil && p.Gate.Type == "command" && p.Gate.Retries > 0 {
		gateRetries = p.Gate.Retries
	}

	phaseStart := time.Now()

	for {
		log.Printf("%sphase %q starting (orchestrated)", vesselLabel(vessel), p.Name)

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

		phasesDir := filepath.Join(r.Config.StateDir, "phases", vessel.ID)
		if err := os.MkdirAll(phasesDir, 0o755); err != nil {
			r.failVessel(vessel.ID, fmt.Sprintf("create phases dir: %v", err))
			if failErr := src.OnFail(ctx, vessel); failErr != nil {
				log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, failErr)
			}
			return singlePhaseResult{status: "failed", duration: time.Since(phaseStart)}
		}

		var output []byte
		var runErr error

		if p.Type == "command" {
			rendered, err := phase.RenderPrompt(p.Run, td)
			if err != nil {
				r.failVessel(vessel.ID, fmt.Sprintf("render command for phase %s: %v", p.Name, err))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				return singlePhaseResult{status: "failed", duration: time.Since(phaseStart)}
			}
			if wErr := os.WriteFile(filepath.Join(phasesDir, p.Name+".command"), []byte(rendered), 0o644); wErr != nil {
				log.Printf("warn: write command file: %v", wErr)
			}
			cmdOut, cmdErr := gate.RunCommand(ctx, r.Runner, worktreePath, rendered)
			output = []byte(cmdOut)
			runErr = cmdErr
		} else {
			promptContent, err := os.ReadFile(p.PromptFile)
			if err != nil {
				r.failVessel(vessel.ID, fmt.Sprintf("read prompt file %s: %v", p.PromptFile, err))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				return singlePhaseResult{status: "failed", duration: time.Since(phaseStart)}
			}
			rendered, err := phase.RenderPrompt(string(promptContent), td)
			if err != nil {
				r.failVessel(vessel.ID, fmt.Sprintf("render prompt for phase %s: %v", p.Name, err))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				return singlePhaseResult{status: "failed", duration: time.Since(phaseStart)}
			}
			promptPath := filepath.Join(phasesDir, p.Name+".prompt")
			if wErr := os.WriteFile(promptPath, []byte(rendered), 0o644); wErr != nil {
				log.Printf("warn: write prompt file %s: %v", promptPath, wErr)
			}
			srcCfg := r.sourceConfigFromMeta(vessel)
			provider := resolveProvider(r.Config, srcCfg, wf, &p)
			cmd, args, phaseStdin := buildProviderPhaseArgs(r.Config, srcCfg, wf, &p, harnessContent, provider, rendered)
			output, runErr = r.Runner.RunPhase(ctx, worktreePath, phaseStdin, cmd, args...)
		}

		// Write output file.
		outputPath := filepath.Join(phasesDir, p.Name+".output")
		if wErr := os.WriteFile(outputPath, output, 0o644); wErr != nil {
			log.Printf("warn: write output file %s: %v", outputPath, wErr)
		}
		fmt.Printf("Phase %s complete: %s\n", p.Name, outputPath)

		if runErr != nil {
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
			return singlePhaseResult{status: "failed", duration: time.Since(phaseStart)}
		}

		// Report phase completion.
		issueNum := r.parseIssueNum(vessel)
		if phaseMatchedNoOp(&p, string(output)) {
			if issueNum > 0 && r.Reporter != nil {
				r.logReporterError("post phase-complete comment", vessel.ID,
					r.Reporter.PhaseComplete(ctx, issueNum, p.Name, time.Since(phaseStart), string(output)))
			}
			return singlePhaseResult{output: string(output), status: "no-op", duration: time.Since(phaseStart)}
		}

		if issueNum > 0 && r.Reporter != nil {
			r.logReporterError("post phase-complete comment", vessel.ID,
				r.Reporter.PhaseComplete(ctx, issueNum, p.Name, time.Since(phaseStart), string(output)))
		}

		// Handle gate.
		if p.Gate == nil {
			return singlePhaseResult{output: string(output), status: "completed", duration: time.Since(phaseStart)}
		}

		switch p.Gate.Type {
		case "command":
			gateOut, passed, gateErr := gate.RunCommandGate(ctx, r.Runner, worktreePath, p.Gate.Run)
			if gateErr != nil {
				r.failVessel(vessel.ID, fmt.Sprintf("phase %s gate error: %v", p.Name, gateErr))
				if err := src.OnFail(ctx, vessel); err != nil {
					log.Printf("warn: OnFail hook for vessel %s: %v", vessel.ID, err)
				}
				return singlePhaseResult{status: "failed", duration: time.Since(phaseStart), gateOut: gateOut}
			}
			if passed {
				log.Printf("%sgate passed for phase %q", vesselLabel(vessel), p.Name)
				return singlePhaseResult{output: string(output), status: "completed", duration: time.Since(phaseStart)}
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
				return singlePhaseResult{status: "failed", duration: time.Since(phaseStart), gateOut: gateOut}
			}
			gateRetries--
			log.Printf("%sgate failed for phase %q, retries remaining=%d", vesselLabel(vessel), p.Name, gateRetries)
			gateResult = fmt.Sprintf("The following gate check failed after the previous phase. Fix the issues and try again:\n\n%s", gateOut)
			time.Sleep(retryDelay)
			continue // re-run phase

		case "label":
			log.Printf("%swaiting for label %q after phase %q", vesselLabel(vessel), p.Gate.WaitFor, p.Name)
			vessel.WaitingFor = p.Gate.WaitFor
			now := time.Now().UTC()
			vessel.WaitingSince = &now
			vessel.State = queue.StateWaiting
			vessel.CurrentPhase = phaseIdx + 1
			if updateErr := r.Queue.UpdateVessel(vessel); updateErr != nil {
				log.Printf("warn: persist waiting state for %s: %v", vessel.ID, updateErr)
			}
			if updateErr := r.Queue.Update(vessel.ID, queue.StateWaiting, ""); updateErr != nil {
				log.Printf("warn: failed to set vessel %s to waiting: %v", vessel.ID, updateErr)
			}
			return singlePhaseResult{output: string(output), status: "waiting", duration: time.Since(phaseStart)}
		}

		// Unknown gate type: treat as passed.
		return singlePhaseResult{output: string(output), status: "completed", duration: time.Since(phaseStart)}
	}
}

func phaseMatchedNoOp(p *workflow.Phase, output string) bool {
	return p != nil && p.NoOp != nil && strings.Contains(output, p.NoOp.Match)
}

func (r *Runner) logReporterError(action string, vesselID string, err error) {
	if err != nil {
		log.Printf("warn: %s for vessel %s: %v", action, vesselID, err)
	}
}

// sourceConfigFromMeta returns the SourceConfig for a vessel by looking up
// the config source name stored in vessel Meta at scan time.
func (r *Runner) sourceConfigFromMeta(v queue.Vessel) *config.SourceConfig {
	if v.Meta == nil {
		return nil
	}
	name := v.Meta["config_source"]
	if name == "" {
		return nil
	}
	if sc, ok := r.Config.Sources[name]; ok {
		return &sc
	}
	return nil
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

// buildPhaseArgs constructs the claude CLI arguments for a phase invocation.
// Model resolution follows the hierarchy: Phase.Model > Workflow.Model > Source.Model > Config.Model > ClaudeConfig.DefaultModel.
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
// Model resolution follows the hierarchy: Phase.Model > Workflow.Model > Source.Model > Config.Model > CopilotConfig.DefaultModel.
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
func buildProviderPhaseArgs(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, harnessContent, provider, renderedPrompt string) (string, []string, io.Reader) {
	switch provider {
	case "copilot":
		return cfg.Copilot.Command, buildCopilotPhaseArgs(cfg, srcCfg, wf, p, harnessContent, renderedPrompt), nil
	default: // "claude"
		var stdin io.Reader
		if renderedPrompt != "" {
			stdin = strings.NewReader(renderedPrompt)
		}
		return cfg.Claude.Command, buildPhaseArgs(cfg, srcCfg, wf, p, harnessContent), stdin
	}
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
// Resolution order: Phase.Model > Workflow.Model > Source.Model > Config.Model > provider's DefaultModel.
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
	if cfg.Model != "" {
		return cfg.Model
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
