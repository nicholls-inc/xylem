package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/ctxmgr"
	"github.com/nicholls-inc/xylem/cli/internal/memory"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

const (
	defaultContextWindowTokens = 4000
	latestHandoffSessionID     = "latest"
	contextManifestSuffix      = ".context.json"
)

type resumeArtifacts struct {
	Handoff         *memory.HandoffArtifact
	Progress        *memory.ProgressFile
	PreviousOutputs map[string]string
	Resumed         bool
}

type structuredState struct {
	mu        sync.Mutex
	vesselID  string
	phasesDir string
	workflow  *workflow.Workflow
	progress  *memory.ProgressFile
	handoff   *memory.HandoffArtifact
}

type structuredSnapshot struct {
	Progress *memory.ProgressFile
	Handoff  *memory.HandoffArtifact
}

type PhaseContextManifest struct {
	Version                 string                  `json:"version"`
	VesselID                string                  `json:"vessel_id"`
	PhaseName               string                  `json:"phase_name"`
	PhaseIndex              int                     `json:"phase_index"`
	CreatedAt               time.Time               `json:"created_at"`
	Resumed                 bool                    `json:"resumed"`
	Strategy                string                  `json:"strategy"`
	Inputs                  ContextInputs           `json:"inputs"`
	Metrics                 ctxmgr.Metrics          `json:"metrics"`
	Compaction              ContextCompaction       `json:"compaction"`
	Window                  ctxmgr.Window           `json:"window"`
	SelectedPreviousOutputs map[string]string       `json:"selected_previous_outputs,omitempty"`
	Progress                *memory.ProgressFile    `json:"progress,omitempty"`
	Handoff                 *memory.HandoffArtifact `json:"handoff,omitempty"`
}

type ContextInputs struct {
	DependencyOutputs []string `json:"dependency_outputs,omitempty"`
	HasIssue          bool     `json:"has_issue"`
	HasGateResult     bool     `json:"has_gate_result"`
	HasProgress       bool     `json:"has_progress"`
	HasHandoff        bool     `json:"has_handoff"`
	HasApprovals      bool     `json:"has_approvals"`
}

type ContextCompaction struct {
	Applied           bool    `json:"applied"`
	Threshold         float64 `json:"threshold"`
	UtilizationBefore float64 `json:"utilization_before"`
	UtilizationAfter  float64 `json:"utilization_after"`
}

type compiledPhaseContext struct {
	TemplateData phase.TemplateData
	PromptPrefix string
}

func (r *Runner) phasesDir(vesselID string) (string, error) {
	if err := validateSummaryPathComponent(vesselID); err != nil {
		return "", fmt.Errorf("phases dir: invalid vessel ID: %w", err)
	}
	return filepath.Join(r.Config.StateDir, "phases", vesselID), nil
}

func (r *Runner) prepareStructuredState(vessel queue.Vessel, wf *workflow.Workflow) (*structuredState, resumeArtifacts, error) {
	phasesDir, err := r.phasesDir(vessel.ID)
	if err != nil {
		return nil, resumeArtifacts{}, err
	}
	if err := os.MkdirAll(phasesDir, 0o755); err != nil {
		return nil, resumeArtifacts{}, fmt.Errorf("create phases dir: %w", err)
	}

	if err := cleanupExpiredStructuredArtifacts(r.Config.StateDir, r.runtimeNow(), r.Config.CleanupAfterDuration()); err != nil {
		log.Printf("warn: cleanup structured artifacts: %v", err)
	}

	resume, err := r.loadResumeArtifacts(phasesDir, vessel, wf)
	if err != nil {
		return nil, resumeArtifacts{}, err
	}

	progress, err := ensureProgressFile(vessel.ID, wf, phasesDir, resume.Progress)
	if err != nil {
		return nil, resumeArtifacts{}, err
	}
	resume.Progress = progress

	handoff := resume.Handoff
	if handoff == nil {
		handoff = memory.NewHandoff(vessel.ID, latestHandoffSessionID)
	}
	handoff.MissionID = vessel.ID
	handoff.SessionID = latestHandoffSessionID
	handoff.Plan = workflowPhaseNames(wf)
	if handoff.PhaseOutputs == nil {
		handoff.PhaseOutputs = make(map[string]string)
	}
	if handoff.Verification == nil {
		handoff.Verification = make(map[string]string)
	}

	state := &structuredState{
		vesselID:  vessel.ID,
		phasesDir: phasesDir,
		workflow:  wf,
		progress:  progress,
		handoff:   handoff,
	}
	if err := state.persistHandoff("bootstrap"); err != nil {
		return nil, resumeArtifacts{}, err
	}
	return state, resume, nil
}

func (r *Runner) loadResumeArtifacts(phasesDir string, vessel queue.Vessel, wf *workflow.Workflow) (resumeArtifacts, error) {
	ctx, err := memory.StartSession(vessel.ID, latestHandoffSessionID, phasesDir)
	if err != nil {
		return resumeArtifacts{}, fmt.Errorf("load resume artifacts: %w", err)
	}

	resume := resumeArtifacts{
		Handoff:  ctx.Handoff,
		Progress: ctx.Progress,
	}
	if ctx.Handoff != nil && len(ctx.Handoff.PhaseOutputs) > 0 {
		resume.PreviousOutputs = cloneStringMap(ctx.Handoff.PhaseOutputs)
		resume.Resumed = true
		return resume, nil
	}

	resume.PreviousOutputs = r.rebuildPreviousOutputs(vessel.ID, wf)
	resume.Resumed = len(resume.PreviousOutputs) > 0 || ctx.Progress != nil
	return resume, nil
}

func ensureProgressFile(vesselID string, wf *workflow.Workflow, phasesDir string, existing *memory.ProgressFile) (*memory.ProgressFile, error) {
	if existing != nil {
		return existing, nil
	}

	progress, err := memory.LoadProgress(vesselID, phasesDir)
	if err == nil {
		return progress, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load progress: %w", err)
	}

	progress, err = memory.CreateProgress(vesselID, workflowPhaseNames(wf), phasesDir)
	if err != nil {
		return nil, fmt.Errorf("create progress: %w", err)
	}
	return progress, nil
}

func (s *structuredState) snapshot() structuredSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return structuredSnapshot{
		Progress: cloneProgressFile(s.progress),
		Handoff:  cloneHandoffArtifact(s.handoff),
	}
}

func (s *structuredState) markPhaseInProgress(phaseName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := memory.UpdateProgress(s.vesselID, phaseName, "in_progress", s.phasesDir); err != nil {
		return fmt.Errorf("mark phase in progress: %w", err)
	}
	progress, err := memory.LoadProgress(s.vesselID, s.phasesDir)
	if err != nil {
		return fmt.Errorf("reload progress: %w", err)
	}
	s.progress = progress
	s.refreshDerivedState()
	return s.persistHandoff(phaseName + "-start")
}

func (s *structuredState) recordApproval(phaseName, status, reason string, recordedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var approvals []memory.OperatorApproval
	for _, approval := range s.handoff.Approvals {
		if approval.Phase != phaseName {
			approvals = append(approvals, approval)
		}
	}
	approvals = append(approvals, memory.OperatorApproval{
		Phase:      phaseName,
		Status:     status,
		Reason:     reason,
		RecordedAt: recordedAt.UTC(),
	})
	sort.SliceStable(approvals, func(i, j int) bool {
		return approvals[i].Phase < approvals[j].Phase
	})
	s.handoff.Approvals = approvals
	return s.persistHandoff(phaseName + "-approval")
}

func (s *structuredState) recordPhaseOutcome(phaseName string, phaseIdx int, output, status, verification string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	progressStatus := "completed"
	if status == "failed" {
		progressStatus = "failed"
	}
	if err := memory.UpdateProgress(s.vesselID, phaseName, progressStatus, s.phasesDir); err != nil {
		return fmt.Errorf("record phase outcome: %w", err)
	}
	progress, err := memory.LoadProgress(s.vesselID, s.phasesDir)
	if err != nil {
		return fmt.Errorf("reload progress: %w", err)
	}
	s.progress = progress

	if output != "" {
		if s.handoff.PhaseOutputs == nil {
			s.handoff.PhaseOutputs = make(map[string]string)
		}
		s.handoff.PhaseOutputs[phaseName] = output
	}
	if verification != "" {
		if s.handoff.Verification == nil {
			s.handoff.Verification = make(map[string]string)
		}
		s.handoff.Verification[phaseName] = verification
	}

	s.handoff.CurrentPhase = phaseIdx + 1
	s.refreshDerivedState()
	return s.persistHandoff(phaseName + "-" + status)
}

func (s *structuredState) refreshDerivedState() {
	s.handoff.Plan = workflowPhaseNames(s.workflow)
	s.handoff.Checkpoints = progressPhaseNames(s.progress, "completed")
	s.handoff.Completed = progressPhaseNames(s.progress, "completed")
	s.handoff.Failed = progressPhaseNames(s.progress, "failed")
	s.handoff.NextSteps = append(progressPhaseNames(s.progress, "in_progress"), progressPhaseNames(s.progress, "pending")...)
	s.handoff.Unresolved = unresolvedFromState(s.progress, s.handoff.Verification)
}

func (s *structuredState) persistHandoff(sessionID string) error {
	latest := cloneHandoffArtifact(s.handoff)
	latest.SessionID = latestHandoffSessionID
	if err := latest.Save(s.phasesDir); err != nil {
		return fmt.Errorf("save latest handoff: %w", err)
	}

	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	snapshot := cloneHandoffArtifact(s.handoff)
	snapshot.SessionID = sanitizeSessionID(sessionID)
	if err := snapshot.Save(s.phasesDir); err != nil {
		return fmt.Errorf("save handoff snapshot: %w", err)
	}
	return nil
}

func (r *Runner) compilePhaseContext(vessel queue.Vessel, p workflow.Phase, phaseIdx int, issueData phase.IssueData, previousOutputs map[string]string, gateResult string, snapshot structuredSnapshot, resumed bool) (*compiledPhaseContext, error) {
	phasesDir, err := r.phasesDir(vessel.ID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(phasesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create phases dir: %w", err)
	}

	inputs := ContextInputs{
		DependencyOutputs: sortedKeys(previousOutputs),
		HasIssue:          issueData.Number != 0 || issueData.Title != "" || issueData.Body != "",
		HasGateResult:     gateResult != "",
		HasProgress:       snapshot.Progress != nil,
		HasHandoff:        snapshot.Handoff != nil,
	}
	if snapshot.Handoff != nil && len(snapshot.Handoff.Approvals) > 0 {
		inputs.HasApprovals = true
	}

	processors := []ctxmgr.Processor{
		{
			Name:     "resume_handoff",
			Priority: 10,
			Fn: func(step ctxmgr.StepContext) []ctxmgr.Segment {
				if snapshot.Handoff == nil {
					return nil
				}
				content := formatHandoffSegment(snapshot.Handoff)
				if strings.TrimSpace(content) == "" {
					return nil
				}
				return []ctxmgr.Segment{{
					Name:     "resume_handoff",
					Content:  content,
					Tokens:   ctxmgr.EstimateTokens(content),
					Durable:  true,
					Source:   "handoff",
					Priority: 10,
				}}
			},
		},
		{
			Name:     "progress",
			Priority: 20,
			Fn: func(step ctxmgr.StepContext) []ctxmgr.Segment {
				if snapshot.Progress == nil {
					return nil
				}
				content := formatProgressSegment(snapshot.Progress)
				if strings.TrimSpace(content) == "" {
					return nil
				}
				return []ctxmgr.Segment{{
					Name:     "progress",
					Content:  content,
					Tokens:   ctxmgr.EstimateTokens(content),
					Durable:  true,
					Source:   "progress",
					Priority: 20,
				}}
			},
		},
		{
			Name:     "issue",
			Priority: 30,
			Fn: func(step ctxmgr.StepContext) []ctxmgr.Segment {
				content := formatIssueSegment(issueData)
				if strings.TrimSpace(content) == "" {
					return nil
				}
				return []ctxmgr.Segment{{
					Name:     "issue",
					Content:  content,
					Tokens:   ctxmgr.EstimateTokens(content),
					Durable:  true,
					Source:   "issue",
					Priority: 30,
				}}
			},
		},
		{
			Name:     "dependencies",
			Priority: 40,
			Fn: func(step ctxmgr.StepContext) []ctxmgr.Segment {
				keys := sortedKeys(previousOutputs)
				segments := make([]ctxmgr.Segment, 0, len(keys))
				for _, key := range keys {
					content := previousOutputs[key]
					segments = append(segments, ctxmgr.Segment{
						Name:     "previous_output:" + key,
						Content:  content,
						Tokens:   ctxmgr.EstimateTokens(content),
						Durable:  false,
						Source:   "previous_output:" + key,
						Priority: 40,
					})
				}
				return segments
			},
		},
		{
			Name:     "gate_result",
			Priority: 50,
			Fn: func(step ctxmgr.StepContext) []ctxmgr.Segment {
				if strings.TrimSpace(gateResult) == "" {
					return nil
				}
				return []ctxmgr.Segment{{
					Name:     "gate_result",
					Content:  gateResult,
					Tokens:   ctxmgr.EstimateTokens(gateResult),
					Durable:  false,
					Source:   "gate_result",
					Priority: 50,
				}}
			},
		},
	}

	pipeline := ctxmgr.NewPipeline(processors...)
	window := pipeline.Assemble(ctxmgr.StepContext{
		StepName:        p.Name,
		Phase:           p.Name,
		AvailableTokens: defaultContextWindowTokens,
		Metadata: map[string]string{
			"vessel_id": vessel.ID,
			"phase":     p.Name,
		},
	}, defaultContextWindowTokens)

	utilBefore := window.Utilization()
	strategy := ctxmgr.SelectStrategy(utilBefore, window.DurableTokens() > 0, phaseComplexity(p, len(previousOutputs)))
	compaction := ContextCompaction{
		Threshold:         ctxmgr.DefaultCompactionThreshold,
		UtilizationBefore: utilBefore,
		UtilizationAfter:  utilBefore,
	}
	if strategy == ctxmgr.StrategyCompress {
		window = ctxmgr.Compact(window, ctxmgr.CompactionConfig{
			Threshold:       ctxmgr.DefaultCompactionThreshold,
			PreserveDurable: true,
		})
		compaction.Applied = true
		compaction.UtilizationAfter = window.Utilization()
	}

	selectedOutputs := selectedPreviousOutputs(window)
	manifestPath := filepath.Join(phasesDir, p.Name+contextManifestSuffix)
	manifest := PhaseContextManifest{
		Version:                 "v1",
		VesselID:                vessel.ID,
		PhaseName:               p.Name,
		PhaseIndex:              phaseIdx,
		CreatedAt:               r.runtimeNow(),
		Resumed:                 resumed,
		Strategy:                string(strategy),
		Inputs:                  inputs,
		Metrics:                 pipeline.Metrics(),
		Compaction:              compaction,
		Window:                  *window,
		SelectedPreviousOutputs: cloneStringMap(selectedOutputs),
		Progress:                cloneProgressFile(snapshot.Progress),
		Handoff:                 cloneHandoffArtifact(snapshot.Handoff),
	}
	if err := saveJSONFile(manifestPath, manifest); err != nil {
		return nil, fmt.Errorf("save context manifest: %w", err)
	}

	return &compiledPhaseContext{
		TemplateData: phase.TemplateData{
			Issue: issueData,
			Phase: phase.PhaseData{
				Name:  p.Name,
				Index: phaseIdx,
			},
			PreviousOutputs: selectedOutputs,
			GateResult:      gateResult,
			Context: phase.ContextData{
				ManifestPath: filepath.ToSlash(filepath.Join("phases", vessel.ID, filepath.Base(manifestPath))),
				Strategy:     string(strategy),
				Compiled:     renderCompiledContext(window),
				Resumed:      resumed,
			},
			Vessel: phase.VesselData{
				ID:     vessel.ID,
				Source: vessel.Source,
			},
		},
		PromptPrefix: renderCompiledContext(window),
	}, nil
}

func workflowPhaseNames(wf *workflow.Workflow) []string {
	if wf == nil {
		return nil
	}
	out := make([]string, 0, len(wf.Phases))
	for _, p := range wf.Phases {
		out = append(out, p.Name)
	}
	return out
}

func progressPhaseNames(progress *memory.ProgressFile, status string) []string {
	if progress == nil {
		return nil
	}
	var out []string
	for _, item := range progress.Items {
		if item.Status == status {
			out = append(out, item.Task)
		}
	}
	return out
}

func unresolvedFromState(progress *memory.ProgressFile, verification map[string]string) []string {
	var unresolved []string
	for _, task := range progressPhaseNames(progress, "failed") {
		unresolved = append(unresolved, fmt.Sprintf("phase %s failed", task))
	}
	keys := sortedKeys(verification)
	for _, key := range keys {
		status := verification[key]
		if strings.Contains(status, "failed") || strings.Contains(status, "waiting") || strings.Contains(status, "required") {
			unresolved = append(unresolved, fmt.Sprintf("%s: %s", key, status))
		}
	}
	return unresolved
}

func formatHandoffSegment(h *memory.HandoffArtifact) string {
	if h == nil {
		return ""
	}
	var b strings.Builder
	if len(h.Plan) > 0 {
		fmt.Fprintf(&b, "Plan: %s\n", strings.Join(h.Plan, ", "))
	}
	if len(h.Checkpoints) > 0 {
		fmt.Fprintf(&b, "Checkpoints complete: %s\n", strings.Join(h.Checkpoints, ", "))
	}
	if len(h.Unresolved) > 0 {
		fmt.Fprintf(&b, "Unresolved: %s\n", strings.Join(h.Unresolved, " | "))
	}
	if len(h.NextSteps) > 0 {
		fmt.Fprintf(&b, "Next steps: %s\n", strings.Join(h.NextSteps, ", "))
	}
	if len(h.Verification) > 0 {
		fmt.Fprintf(&b, "Verification: %s\n", formatStringMap(h.Verification))
	}
	if len(h.Approvals) > 0 {
		parts := make([]string, 0, len(h.Approvals))
		for _, approval := range h.Approvals {
			if approval.Reason != "" {
				parts = append(parts, fmt.Sprintf("%s=%s (%s)", approval.Phase, approval.Status, approval.Reason))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s=%s", approval.Phase, approval.Status))
		}
		fmt.Fprintf(&b, "Approvals: %s\n", strings.Join(parts, ", "))
	}
	return strings.TrimSpace(b.String())
}

func formatProgressSegment(progress *memory.ProgressFile) string {
	if progress == nil {
		return ""
	}
	parts := make([]string, 0, len(progress.Items))
	for _, item := range progress.Items {
		parts = append(parts, fmt.Sprintf("%s=%s", item.Task, item.Status))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Progress: " + strings.Join(parts, ", ")
}

func formatIssueSegment(issueData phase.IssueData) string {
	if issueData.Title == "" && issueData.Body == "" && issueData.URL == "" {
		return ""
	}
	var parts []string
	if issueData.Number != 0 {
		parts = append(parts, fmt.Sprintf("Issue #%d", issueData.Number))
	}
	if issueData.Title != "" {
		parts = append(parts, issueData.Title)
	}
	if issueData.URL != "" {
		parts = append(parts, issueData.URL)
	}
	if len(issueData.Labels) > 0 {
		parts = append(parts, "labels="+strings.Join(issueData.Labels, ","))
	}
	return strings.Join(parts, " | ")
}

func renderCompiledContext(window *ctxmgr.Window) string {
	if window == nil || len(window.Segments) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Structured context\n")
	for _, segment := range window.Segments {
		fmt.Fprintf(&b, "\n[%s]\n%s\n", segment.Name, segment.Content)
	}
	return strings.TrimSpace(b.String())
}

func selectedPreviousOutputs(window *ctxmgr.Window) map[string]string {
	selected := make(map[string]string)
	if window == nil {
		return selected
	}
	for _, segment := range window.Segments {
		if !strings.HasPrefix(segment.Source, "previous_output:") {
			continue
		}
		phaseName := strings.TrimPrefix(segment.Source, "previous_output:")
		selected[phaseName] = segment.Content
	}
	return selected
}

func withCompiledContext(prefix, rendered string) string {
	if strings.TrimSpace(prefix) == "" {
		return rendered
	}
	if strings.TrimSpace(rendered) == "" {
		return prefix
	}
	return prefix + "\n\nPhase instructions\n" + rendered
}

func phaseComplexity(p workflow.Phase, dependencyCount int) string {
	if p.MaxTurns >= 20 || dependencyCount >= 3 {
		return "high"
	}
	return "medium"
}

func saveJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func cleanupExpiredStructuredArtifacts(stateDir string, now time.Time, retainFor time.Duration) error {
	phasesRoot := filepath.Join(stateDir, "phases")
	entries, err := os.ReadDir(phasesRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read phases root: %w", err)
	}

	cutoff := now.Add(-retainFor)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirPath := filepath.Join(phasesRoot, entry.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			return fmt.Errorf("read vessel phase dir %s: %w", entry.Name(), err)
		}
		for _, file := range files {
			if file.IsDir() || !isExpirableStructuredArtifact(file.Name()) {
				continue
			}
			info, err := file.Info()
			if err != nil {
				return fmt.Errorf("stat structured artifact %s: %w", file.Name(), err)
			}
			if info.ModTime().After(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(dirPath, file.Name())); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove structured artifact %s: %w", file.Name(), err)
			}
		}
	}
	return nil
}

func isExpirableStructuredArtifact(name string) bool {
	if strings.HasSuffix(name, contextManifestSuffix) {
		return true
	}
	if strings.HasPrefix(name, "handoff_") && !strings.HasSuffix(name, "_latest.json") {
		return true
	}
	return false
}

func sanitizeSessionID(sessionID string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-")
	cleaned := replacer.Replace(strings.TrimSpace(sessionID))
	if cleaned == "" {
		return "session"
	}
	return cleaned
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneProgressFile(progress *memory.ProgressFile) *memory.ProgressFile {
	if progress == nil {
		return nil
	}
	cloned := *progress
	cloned.Items = append([]memory.ProgressItem(nil), progress.Items...)
	return &cloned
}

func cloneHandoffArtifact(h *memory.HandoffArtifact) *memory.HandoffArtifact {
	if h == nil {
		return nil
	}
	cloned := *h
	cloned.Plan = append([]string(nil), h.Plan...)
	cloned.Checkpoints = append([]string(nil), h.Checkpoints...)
	cloned.Completed = append([]string(nil), h.Completed...)
	cloned.Failed = append([]string(nil), h.Failed...)
	cloned.Unresolved = append([]string(nil), h.Unresolved...)
	cloned.NextSteps = append([]string(nil), h.NextSteps...)
	cloned.PhaseOutputs = cloneStringMap(h.PhaseOutputs)
	cloned.Verification = cloneStringMap(h.Verification)
	cloned.Approvals = append([]memory.OperatorApproval(nil), h.Approvals...)
	return &cloned
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func formatStringMap(m map[string]string) string {
	keys := sortedKeys(m)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, m[key]))
	}
	return strings.Join(parts, ", ")
}
