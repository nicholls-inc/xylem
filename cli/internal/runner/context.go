package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/ctxmgr"
	"github.com/nicholls-inc/xylem/cli/internal/memory"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

const (
	structuredProgressPrefix   = "progress_"
	structuredProgressSuffix   = ".json"
	latestHandoffSessionID     = "latest"
	compiledContextTokenBudget = 4096
)

type resumeArtifacts struct {
	Progress        *structuredProgressFile
	Handoff         *memory.HandoffArtifact
	PreviousOutputs map[string]string
}

type structuredState struct {
	cfg      *config.Config
	vessel   queue.Vessel
	workflow *workflow.Workflow

	phasesDir        string
	progressPath     string
	latestHandoffRel string

	resume   resumeArtifacts
	progress structuredProgressFile
}

type structuredProgressFile struct {
	VesselID       string                    `json:"vessel_id"`
	Workflow       string                    `json:"workflow,omitempty"`
	CurrentPhase   int                       `json:"current_phase"`
	Plan           string                    `json:"plan,omitempty"`
	Unresolved     []string                  `json:"unresolved,omitempty"`
	Checkpoints    []string                  `json:"checkpoints,omitempty"`
	Verification   []string                  `json:"verification,omitempty"`
	Approvals      []memory.OperatorApproval `json:"approvals,omitempty"`
	LastGateResult string                    `json:"last_gate_result,omitempty"`
	PhaseOutputs   map[string]string         `json:"phase_outputs,omitempty"`
	Phases         []structuredSnapshot      `json:"phases,omitempty"`
	UpdatedAt      time.Time                 `json:"updated_at"`
}

type structuredSnapshot struct {
	Name       string    `json:"name"`
	Index      int       `json:"index"`
	Status     string    `json:"status"`
	OutputPath string    `json:"output_path,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type PhaseContextManifest struct {
	VesselID     string            `json:"vessel_id"`
	Workflow     string            `json:"workflow,omitempty"`
	Phase        string            `json:"phase"`
	ManifestPath string            `json:"manifest_path"`
	Strategy     ctxmgr.Strategy   `json:"strategy"`
	Resumed      bool              `json:"resumed"`
	Inputs       ContextInputs     `json:"inputs"`
	Compaction   ContextCompaction `json:"compaction"`
	Window       *ctxmgr.Window    `json:"window"`
	Compiled     string            `json:"compiled"`
	CreatedAt    time.Time         `json:"created_at"`
}

type ContextInputs struct {
	IssuePresent     bool     `json:"issue_present"`
	GateResult       bool     `json:"gate_result"`
	PreviousOutputs  []string `json:"previous_outputs,omitempty"`
	DurableSections  []string `json:"durable_sections,omitempty"`
	DependencyScoped bool     `json:"dependency_scoped"`
}

type ContextCompaction struct {
	Applied       bool    `json:"applied"`
	Threshold     float64 `json:"threshold"`
	BeforeTokens  int     `json:"before_tokens"`
	AfterTokens   int     `json:"after_tokens"`
	DurableTokens int     `json:"durable_tokens"`
}

func prepareStructuredState(cfg *config.Config, vessel queue.Vessel, wf *workflow.Workflow) (*structuredState, error) {
	if cfg == nil {
		return nil, fmt.Errorf("prepare structured state: config must not be nil")
	}
	if err := validateSummaryPathComponent(vessel.ID); err != nil {
		return nil, fmt.Errorf("prepare structured state: invalid vessel ID: %w", err)
	}

	phasesDir := filepath.Join(cfg.StateDir, "phases", vessel.ID)
	if err := os.MkdirAll(phasesDir, 0o755); err != nil {
		return nil, fmt.Errorf("prepare structured state: create phases dir: %w", err)
	}

	state := &structuredState{
		cfg:              cfg,
		vessel:           vessel,
		workflow:         wf,
		phasesDir:        phasesDir,
		progressPath:     filepath.Join(phasesDir, progressFileName(vessel.ID)),
		latestHandoffRel: latestHandoffRelativePath(vessel.ID),
	}

	resume, err := loadResumeArtifacts(phasesDir, vessel.ID)
	if err != nil {
		return nil, fmt.Errorf("prepare structured state: %w", err)
	}
	state.resume = resume

	if err := state.ensureProgressFile(); err != nil {
		return nil, fmt.Errorf("prepare structured state: ensure progress file: %w", err)
	}

	if err := cleanupExpiredStructuredArtifacts(cfg, vessel.ID, time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("prepare structured state: cleanup expired structured artifacts: %w", err)
	}

	return state, nil
}

func loadResumeArtifacts(phasesDir, vesselID string) (resumeArtifacts, error) {
	var resume resumeArtifacts

	progress, err := readStructuredProgress(filepath.Join(phasesDir, progressFileName(vesselID)))
	if err != nil {
		if !os.IsNotExist(err) {
			return resume, fmt.Errorf("load resume artifacts: read progress: %w", err)
		}
	} else {
		resume.Progress = progress
	}

	handoff, err := memory.LoadHandoff(vesselID, latestHandoffSessionID, phasesDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return resume, fmt.Errorf("load resume artifacts: read handoff: %w", err)
		}
	} else {
		resume.Handoff = handoff
		resume.PreviousOutputs = cloneStringMap(handoff.PhaseOutputs)
	}
	if len(resume.PreviousOutputs) == 0 && resume.Progress != nil {
		resume.PreviousOutputs = readPhaseOutputContents(phasesDir, resume.Progress.PhaseOutputs)
	}

	return resume, nil
}

func readStructuredProgress(path string) (*structuredProgressFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var progress structuredProgressFile
	if err := json.Unmarshal(data, &progress); err != nil {
		return nil, fmt.Errorf("unmarshal progress %s: %w", path, err)
	}
	return &progress, nil
}

func progressFileName(vesselID string) string {
	return structuredProgressPrefix + vesselID + structuredProgressSuffix
}

func latestHandoffRelativePath(vesselID string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, "handoff_"+vesselID+"_"+latestHandoffSessionID+".json"))
}

func (s *structuredState) ensureProgressFile() error {
	if s.resume.Progress != nil {
		s.progress = cloneStructuredProgress(*s.resume.Progress)
	} else {
		s.progress = structuredProgressFile{
			VesselID:     s.vessel.ID,
			Workflow:     s.vessel.Workflow,
			CurrentPhase: s.vessel.CurrentPhase,
			PhaseOutputs: make(map[string]string),
		}
	}

	if s.progress.PhaseOutputs == nil {
		s.progress.PhaseOutputs = make(map[string]string)
	}

	if s.resume.Handoff != nil {
		s.mergeHandoff(*s.resume.Handoff)
	}

	s.progress.VesselID = s.vessel.ID
	if s.progress.Workflow == "" {
		s.progress.Workflow = s.vessel.Workflow
	}
	if s.progress.CurrentPhase < s.vessel.CurrentPhase {
		s.progress.CurrentPhase = s.vessel.CurrentPhase
	}

	s.progress.Phases = ensurePhaseSnapshots(s.progress.Phases, s.workflow)
	s.progress.UpdatedAt = time.Now().UTC()

	return s.persist(false)
}

func (s *structuredState) mergeHandoff(h memory.HandoffArtifact) {
	if s.progress.Plan == "" {
		s.progress.Plan = h.Plan
	}
	if s.progress.CurrentPhase == 0 && h.CurrentPhase != "" && s.workflow != nil {
		for idx, p := range s.workflow.Phases {
			if p.Name == h.CurrentPhase {
				s.progress.CurrentPhase = idx
				break
			}
		}
	}
	s.progress.Unresolved = mergeUniqueStrings(s.progress.Unresolved, h.Unresolved)
	s.progress.Checkpoints = mergeUniqueStrings(s.progress.Checkpoints, h.Checkpoints)
	s.progress.Verification = mergeUniqueStrings(s.progress.Verification, h.Verification)
	s.progress.Approvals = mergeApprovals(s.progress.Approvals, h.Approvals)
}

func ensurePhaseSnapshots(existing []structuredSnapshot, wf *workflow.Workflow) []structuredSnapshot {
	indexed := make(map[string]structuredSnapshot, len(existing))
	for _, item := range existing {
		indexed[item.Name] = item
	}

	snapshots := make([]structuredSnapshot, 0)
	if wf == nil {
		snapshots = append(snapshots, existing...)
		sort.SliceStable(snapshots, func(i, j int) bool { return snapshots[i].Index < snapshots[j].Index })
		return snapshots
	}

	for idx, p := range wf.Phases {
		item, ok := indexed[p.Name]
		if !ok {
			item = structuredSnapshot{Name: p.Name, Index: idx, Status: "pending"}
		}
		item.Name = p.Name
		item.Index = idx
		if item.Status == "" {
			item.Status = "pending"
		}
		snapshots = append(snapshots, item)
	}
	return snapshots
}

func (s *structuredState) recordPhaseOutcome(phaseName string, phaseIdx int, output, status, detail string) error {
	now := time.Now().UTC()
	item := structuredSnapshot{
		Name:       phaseName,
		Index:      phaseIdx,
		Status:     status,
		OutputPath: phaseArtifactRelativePath(s.vessel.ID, phaseName),
		Detail:     detail,
		UpdatedAt:  now,
	}

	replaced := false
	for i := range s.progress.Phases {
		if s.progress.Phases[i].Name == phaseName {
			s.progress.Phases[i] = item
			replaced = true
			break
		}
	}
	if !replaced {
		s.progress.Phases = append(s.progress.Phases, item)
		sort.SliceStable(s.progress.Phases, func(i, j int) bool { return s.progress.Phases[i].Index < s.progress.Phases[j].Index })
	}

	s.progress.CurrentPhase = maxInt(s.progress.CurrentPhase, phaseIdx+1)
	s.progress.PhaseOutputs[phaseName] = item.OutputPath
	if output != "" && (phaseName == "plan" || strings.Contains(phaseName, "plan")) {
		s.progress.Plan = phase.TruncateOutput(output, phase.MaxCompiledContextLen)
	}
	if status == "completed" || status == "no-op" {
		s.progress.Checkpoints = appendUniqueString(s.progress.Checkpoints, phaseName)
	}
	if status == "failed" && detail != "" {
		s.progress.Unresolved = appendUniqueString(s.progress.Unresolved, phaseName+": "+detail)
	}
	if detail != "" && (strings.Contains(detail, "command:") || strings.Contains(detail, "label:") || strings.Contains(detail, "gate")) {
		s.progress.Verification = appendUniqueString(s.progress.Verification, phaseName+": "+detail)
	}
	if detail != "" && (strings.Contains(detail, "command:") || strings.Contains(detail, "label:")) {
		s.progress.LastGateResult = detail
	}
	s.progress.UpdatedAt = now

	return s.persist(true)
}

func (s *structuredState) recordApproval(approval memory.OperatorApproval) error {
	s.progress.Approvals = mergeApprovals(s.progress.Approvals, []memory.OperatorApproval{approval})
	s.progress.UpdatedAt = time.Now().UTC()
	return s.persist(true)
}

func (s *structuredState) persist(writeSnapshot bool) error {
	if err := s.writeProgress(); err != nil {
		return err
	}
	if err := s.writeHandoff(latestHandoffSessionID); err != nil {
		return err
	}
	if !writeSnapshot {
		return nil
	}
	return s.writeHandoff(time.Now().UTC().Format("20060102T150405Z"))
}

func (s *structuredState) writeProgress() error {
	data, err := json.MarshalIndent(s.progress, "", "  ")
	if err != nil {
		return fmt.Errorf("write progress: marshal: %w", err)
	}
	if err := os.WriteFile(s.progressPath, data, 0o644); err != nil {
		return fmt.Errorf("write progress: write: %w", err)
	}
	return nil
}

func (s *structuredState) writeHandoff(sessionID string) error {
	handoff := memory.NewHandoff(s.vessel.ID, sessionID)
	if s.workflow != nil {
		if s.progress.CurrentPhase >= 0 && s.progress.CurrentPhase < len(s.workflow.Phases) {
			handoff.CurrentPhase = s.workflow.Phases[s.progress.CurrentPhase].Name
		} else if len(s.workflow.Phases) > 0 {
			handoff.CurrentPhase = s.workflow.Phases[len(s.workflow.Phases)-1].Name
		}
	}
	handoff.Plan = s.progress.Plan
	handoff.Unresolved = cloneStrings(s.progress.Unresolved)
	handoff.Checkpoints = cloneStrings(s.progress.Checkpoints)
	handoff.Verification = cloneStrings(s.progress.Verification)
	handoff.Approvals = cloneApprovals(s.progress.Approvals)
	handoff.PhaseOutputs = readPhaseOutputContents(s.phasesDir, s.progress.PhaseOutputs)
	for _, item := range s.progress.Phases {
		switch item.Status {
		case "completed", "no-op":
			handoff.Completed = append(handoff.Completed, item.Name)
		case "failed":
			handoff.Failed = append(handoff.Failed, item.Name)
		}
	}
	for _, item := range s.progress.Phases {
		if item.Status == "pending" || item.Status == "in_progress" {
			handoff.NextSteps = append(handoff.NextSteps, item.Name)
		}
	}
	if err := handoff.Save(s.phasesDir); err != nil {
		return fmt.Errorf("write handoff: %w", err)
	}
	return nil
}

func (s *structuredState) compilePhaseContext(p workflow.Phase, phaseIdx int, issueData phase.IssueData, previousOutputs map[string]string, gateResult string, dependencyScoped bool) (*PhaseContextManifest, error) {
	processors := make([]ctxmgr.Processor, 0)
	durableSections := make([]string, 0)

	if section := strings.TrimSpace(renderIssueContext(issueData)); section != "" {
		processors = append(processors, staticContextProcessor("issue", 10, section, true, "issue"))
		durableSections = append(durableSections, "issue")
	}
	if s.progress.Plan != "" {
		processors = append(processors, staticContextProcessor("plan", 20, s.progress.Plan, true, "progress.plan"))
		durableSections = append(durableSections, "plan")
	}
	if len(s.progress.Checkpoints) > 0 {
		processors = append(processors, staticContextProcessor("checkpoints", 30, strings.Join(s.progress.Checkpoints, "\n"), true, "progress.checkpoints"))
		durableSections = append(durableSections, "checkpoints")
	}
	if len(s.progress.Unresolved) > 0 {
		processors = append(processors, staticContextProcessor("unresolved", 40, strings.Join(s.progress.Unresolved, "\n"), true, "progress.unresolved"))
		durableSections = append(durableSections, "unresolved")
	}
	if len(s.progress.Verification) > 0 {
		processors = append(processors, staticContextProcessor("verification", 50, strings.Join(s.progress.Verification, "\n"), true, "progress.verification"))
		durableSections = append(durableSections, "verification")
	}
	if approvals := renderApprovals(s.progress.Approvals); approvals != "" {
		processors = append(processors, staticContextProcessor("approvals", 60, approvals, true, "progress.approvals"))
		durableSections = append(durableSections, "approvals")
	}
	if gateResult != "" {
		processors = append(processors, staticContextProcessor("gate_result", 70, gateResult, false, "gate"))
	}
	for priority, name := range sortedMapKeys(previousOutputs) {
		processors = append(processors, staticContextProcessor("output:"+name, 100+priority, previousOutputs[name], false, "phase."+name))
	}

	pipeline := ctxmgr.NewPipeline(processors...)
	window := pipeline.Assemble(ctxmgr.StepContext{
		StepName:        p.Name,
		Phase:           p.Name,
		AvailableTokens: compiledContextTokenBudget,
		Metadata: map[string]string{
			"workflow":    s.vessel.Workflow,
			"vessel_id":   s.vessel.ID,
			"phase_index": fmt.Sprintf("%d", phaseIdx),
		},
	}, compiledContextTokenBudget)

	hasDurableState := len(durableSections) > 0
	complexity := "normal"
	strategy := ctxmgr.SelectStrategy(window.Utilization(), hasDurableState, complexity)
	selectedWindow := window
	compaction := ContextCompaction{
		Threshold:     ctxmgr.DefaultCompactionThreshold,
		BeforeTokens:  window.UsedTokens(),
		DurableTokens: window.DurableTokens(),
	}
	if strategy == ctxmgr.StrategyCompress {
		selectedWindow = ctxmgr.Compact(window, ctxmgr.CompactionConfig{
			Threshold:       ctxmgr.DefaultCompactionThreshold,
			PreserveDurable: true,
		})
		compaction.Applied = true
	}
	compaction.AfterTokens = selectedWindow.UsedTokens()

	manifest := &PhaseContextManifest{
		VesselID:     s.vessel.ID,
		Workflow:     s.vessel.Workflow,
		Phase:        p.Name,
		ManifestPath: filepath.ToSlash(filepath.Join("phases", s.vessel.ID, p.Name+".context.json")),
		Strategy:     strategy,
		Resumed:      s.resume.Progress != nil || s.resume.Handoff != nil,
		Inputs: ContextInputs{
			IssuePresent:     issueData.Number != 0 || issueData.Title != "" || issueData.Body != "",
			GateResult:       gateResult != "",
			PreviousOutputs:  sortedMapKeys(previousOutputs),
			DurableSections:  durableSections,
			DependencyScoped: dependencyScoped,
		},
		Compaction: compaction,
		Window: &ctxmgr.Window{
			Segments:  cloneSegments(selectedWindow.Segments),
			MaxTokens: selectedWindow.MaxTokens,
		},
		Compiled:  renderCompiledContext(selectedWindow),
		CreatedAt: time.Now().UTC(),
	}

	if err := s.writeContextManifest(p.Name, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func (s *structuredState) writeContextManifest(phaseName string, manifest *PhaseContextManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("write context manifest %s: marshal: %w", phaseName, err)
	}
	path := filepath.Join(s.phasesDir, phaseName+".context.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write context manifest %s: write: %w", phaseName, err)
	}
	return nil
}

func renderCompiledContext(window *ctxmgr.Window) string {
	if window == nil || len(window.Segments) == 0 {
		return ""
	}

	var parts []string
	for _, segment := range window.Segments {
		parts = append(parts, fmt.Sprintf("[%s]\n%s", segment.Name, segment.Content))
	}
	return strings.Join(parts, "\n\n")
}

func withCompiledContext(rendered, compiled string) string {
	if strings.TrimSpace(compiled) == "" {
		return rendered
	}
	return compiled + "\n\n---\n\n" + rendered
}

func renderIssueContext(issueData phase.IssueData) string {
	var parts []string
	if issueData.Number != 0 {
		parts = append(parts, fmt.Sprintf("Number: %d", issueData.Number))
	}
	if issueData.Title != "" {
		parts = append(parts, "Title: "+issueData.Title)
	}
	if issueData.URL != "" {
		parts = append(parts, "URL: "+issueData.URL)
	}
	if len(issueData.Labels) > 0 {
		parts = append(parts, "Labels: "+strings.Join(issueData.Labels, ", "))
	}
	if issueData.Body != "" {
		parts = append(parts, "Body:\n"+phase.TruncateOutput(issueData.Body, phase.MaxIssueBodyLen))
	}
	return strings.Join(parts, "\n")
}

func renderApprovals(approvals []memory.OperatorApproval) string {
	if len(approvals) == 0 {
		return ""
	}

	lines := make([]string, 0, len(approvals))
	for _, approval := range approvals {
		line := approval.ApprovedBy
		if approval.Reason != "" {
			line += ": " + approval.Reason
		}
		if !approval.ApprovedAt.IsZero() {
			line += " (" + approval.ApprovedAt.UTC().Format(time.RFC3339) + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func staticContextProcessor(name string, priority int, content string, durable bool, source string) ctxmgr.Processor {
	return ctxmgr.Processor{
		Name:     name,
		Priority: priority,
		Fn: func(_ ctxmgr.StepContext) []ctxmgr.Segment {
			if strings.TrimSpace(content) == "" {
				return nil
			}
			return []ctxmgr.Segment{{
				Name:     name,
				Content:  content,
				Tokens:   ctxmgr.EstimateTokens(content),
				Durable:  durable,
				Source:   source,
				Priority: priority,
			}}
		},
	}
}

func cleanupExpiredStructuredArtifacts(cfg *config.Config, vesselID string, now time.Time) error {
	if cfg == nil || cfg.CleanupAfter == "" {
		return nil
	}
	cleanupAfter, err := time.ParseDuration(cfg.CleanupAfter)
	if err != nil {
		return fmt.Errorf("parse cleanup_after: %w", err)
	}

	phasesDir := filepath.Join(cfg.StateDir, "phases", vesselID)
	entries, err := os.ReadDir(phasesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read phases dir: %w", err)
	}

	cutoff := now.Add(-cleanupAfter)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		expirableContext := strings.HasSuffix(name, ".context.json")
		expirableHandoff := strings.HasPrefix(name, "handoff_"+vesselID+"_") && !strings.HasSuffix(name, "_"+latestHandoffSessionID+".json")
		if !expirableContext && !expirableHandoff {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", name, err)
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(phasesDir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove expired artifact %s: %w", name, err)
		}
	}
	return nil
}

func (s *structuredState) applySummary(summary *VesselSummary) {
	if summary == nil {
		return
	}
	summary.ProgressPath = filepath.ToSlash(filepath.Join("phases", s.vessel.ID, progressFileName(s.vessel.ID)))
	summary.HandoffPath = s.latestHandoffRel
	summary.Retention = &ArtifactRetention{
		CleanupAfter: s.cfg.CleanupAfter,
		Durable: []string{
			filepath.ToSlash(filepath.Join("phases", s.vessel.ID, summaryFileName)),
			summary.ProgressPath,
			summary.HandoffPath,
			filepath.ToSlash(filepath.Join("phases", s.vessel.ID, "*.prompt")),
			filepath.ToSlash(filepath.Join("phases", s.vessel.ID, "*.output")),
			filepath.ToSlash(filepath.Join("phases", s.vessel.ID, "*.command")),
		},
		Expirable: []string{
			filepath.ToSlash(filepath.Join("phases", s.vessel.ID, "*.context.json")),
			filepath.ToSlash(filepath.Join("phases", s.vessel.ID, "handoff_"+s.vessel.ID+"_*.json (except latest)")),
		},
	}
}

func cloneStructuredProgress(progress structuredProgressFile) structuredProgressFile {
	return structuredProgressFile{
		VesselID:       progress.VesselID,
		Workflow:       progress.Workflow,
		CurrentPhase:   progress.CurrentPhase,
		Plan:           progress.Plan,
		Unresolved:     cloneStrings(progress.Unresolved),
		Checkpoints:    cloneStrings(progress.Checkpoints),
		Verification:   cloneStrings(progress.Verification),
		Approvals:      cloneApprovals(progress.Approvals),
		LastGateResult: progress.LastGateResult,
		PhaseOutputs:   cloneStringMap(progress.PhaseOutputs),
		Phases:         cloneSnapshots(progress.Phases),
		UpdatedAt:      progress.UpdatedAt,
	}
}

func readPhaseOutputContents(phasesDir string, outputs map[string]string) map[string]string {
	if len(outputs) == 0 {
		return nil
	}

	contents := make(map[string]string, len(outputs))
	for phaseName, relPath := range outputs {
		if strings.TrimSpace(relPath) == "" {
			continue
		}

		path := relPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(slashBase(phasesDir), filepath.FromSlash(strings.TrimPrefix(relPath, "phases/")))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		contents[phaseName] = string(data)
	}
	if len(contents) == 0 {
		return nil
	}
	return contents
}

func slashBase(phasesDir string) string {
	return filepath.Dir(filepath.Dir(filepath.Clean(phasesDir)))
}

func cloneSnapshots(in []structuredSnapshot) []structuredSnapshot {
	if len(in) == 0 {
		return nil
	}
	out := make([]structuredSnapshot, len(in))
	copy(out, in)
	return out
}

func cloneSegments(in []ctxmgr.Segment) []ctxmgr.Segment {
	if len(in) == 0 {
		return nil
	}
	out := make([]ctxmgr.Segment, len(in))
	copy(out, in)
	return out
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneApprovals(in []memory.OperatorApproval) []memory.OperatorApproval {
	if len(in) == 0 {
		return nil
	}
	out := make([]memory.OperatorApproval, len(in))
	copy(out, in)
	return out
}

func mergeUniqueStrings(base, add []string) []string {
	for _, item := range add {
		base = appendUniqueString(base, item)
	}
	return base
}

func appendUniqueString(items []string, value string) []string {
	for _, existing := range items {
		if existing == value {
			return items
		}
	}
	return append(items, value)
}

func mergeApprovals(base, add []memory.OperatorApproval) []memory.OperatorApproval {
	for _, approval := range add {
		found := false
		for _, existing := range base {
			if existing.ApprovedBy == approval.ApprovedBy && existing.Reason == approval.Reason && existing.ApprovedAt.Equal(approval.ApprovedAt) {
				found = true
				break
			}
		}
		if !found {
			base = append(base, approval)
		}
	}
	return base
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
