package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const runtimeArtifactSchemaVersion = "xylem.runtime.v1"

type RuntimeArtifacts struct {
	SchemaVersion string                `json:"schema_version"`
	VesselID      string                `json:"vessel_id"`
	Source        string                `json:"source"`
	Workflow      string                `json:"workflow,omitempty"`
	State         string                `json:"state"`
	GeneratedAt   time.Time             `json:"generated_at"`
	SummaryPath   string                `json:"summary_path"`
	Artifacts     RuntimeArtifactPaths  `json:"artifacts"`
	Trace         TraceArtifact         `json:"trace"`
	Budget        BudgetEventsArtifact  `json:"budget"`
	Audit         AuditEventsArtifact   `json:"audit"`
	Evidence      RuntimeEvidenceStatus `json:"evidence"`
	Phases        []PhaseArtifact       `json:"phases"`
}

type RuntimeArtifactPaths struct {
	CostReport       string `json:"cost_report"`
	BudgetEvents     string `json:"budget_events"`
	AuditEvents      string `json:"audit_events"`
	Trace            string `json:"trace"`
	EvidenceManifest string `json:"evidence_manifest,omitempty"`
}

type RuntimeEvidenceStatus struct {
	ManifestPath   string `json:"manifest_path,omitempty"`
	ClaimCount     int    `json:"claim_count"`
	StrongestLevel string `json:"strongest_level,omitempty"`
}

type PhaseArtifact struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	PromptPath  string `json:"prompt_path,omitempty"`
	CommandPath string `json:"command_path,omitempty"`
	OutputPath  string `json:"output_path,omitempty"`
}

type BudgetEventsArtifact struct {
	VesselID       string             `json:"vessel_id"`
	GeneratedAt    time.Time          `json:"generated_at"`
	Action         string             `json:"action"`
	ApprovalLabel  string             `json:"approval_label,omitempty"`
	BudgetExceeded bool               `json:"budget_exceeded"`
	MaxCostUSD     *float64           `json:"max_cost_usd,omitempty"`
	MaxTokens      *int               `json:"max_tokens,omitempty"`
	Alerts         []cost.BudgetAlert `json:"alerts"`
}

type AuditEventsArtifact struct {
	VesselID    string                    `json:"vessel_id"`
	GeneratedAt time.Time                 `json:"generated_at"`
	SourcePath  string                    `json:"source_path,omitempty"`
	EntryCount  int                       `json:"entry_count"`
	Entries     []intermediary.AuditEntry `json:"entries"`
}

type TraceArtifact struct {
	VesselID          string    `json:"vessel_id"`
	GeneratedAt       time.Time `json:"generated_at"`
	Enabled           bool      `json:"enabled"`
	CollectorEndpoint string    `json:"collector_endpoint,omitempty"`
	TraceID           string    `json:"trace_id,omitempty"`
	SpanID            string    `json:"span_id,omitempty"`
	Traceparent       string    `json:"traceparent,omitempty"`
}

func persistRuntimeArtifacts(ctx context.Context, cfg *config.Config, auditLog *intermediary.AuditLog, summary *VesselSummary, manifest *evidence.Manifest, vrs *vesselRunState, now time.Time) {
	if cfg == nil || summary == nil || vrs == nil {
		return
	}

	costReport := vrs.costTracker.Report(vrs.vesselID)
	costReportPath := filepath.Join(cfg.StateDir, "phases", vrs.vesselID, costReportFileName)
	if err := os.MkdirAll(filepath.Dir(costReportPath), 0o755); err != nil {
		log.Printf("warn: create cost report dir: %v", err)
	} else if err := cost.SaveReport(costReportPath, costReport); err != nil {
		log.Printf("warn: save cost report: %v", err)
	}
	summary.CostReportPath = costReportRelativePath(vrs.vesselID)

	budgetArtifact := buildBudgetEventsArtifact(summary, vrs, now)
	budgetPath := filepath.Join(cfg.StateDir, "phases", vrs.vesselID, budgetEventsFileName)
	if err := saveRuntimeArtifact(budgetPath, budgetArtifact); err != nil {
		log.Printf("warn: save budget events: %v", err)
	} else {
		summary.BudgetEventsPath = budgetEventsRelativePath(vrs.vesselID)
	}

	auditArtifact, err := buildAuditEventsArtifact(auditLog, vrs.vesselID, now)
	if err != nil {
		log.Printf("warn: build audit events: %v", err)
	} else {
		auditPath := filepath.Join(cfg.StateDir, "phases", vrs.vesselID, auditEventsFileName)
		if err := saveRuntimeArtifact(auditPath, auditArtifact); err != nil {
			log.Printf("warn: save audit events: %v", err)
		} else {
			summary.AuditEventsPath = auditEventsRelativePath(vrs.vesselID)
		}
	}

	traceArtifact := buildTraceArtifact(ctx, cfg, vrs.vesselID, now)
	tracePath := filepath.Join(cfg.StateDir, "phases", vrs.vesselID, traceFileName)
	if err := saveRuntimeArtifact(tracePath, traceArtifact); err != nil {
		log.Printf("warn: save trace artifact: %v", err)
	} else {
		summary.TracePath = traceArtifactRelativePath(vrs.vesselID)
	}

	runtimeArtifact := RuntimeArtifacts{
		SchemaVersion: runtimeArtifactSchemaVersion,
		VesselID:      summary.VesselID,
		Source:        summary.Source,
		Workflow:      summary.Workflow,
		State:         summary.State,
		GeneratedAt:   now.UTC(),
		SummaryPath:   filepath.ToSlash(filepath.Join("phases", vrs.vesselID, summaryFileName)),
		Artifacts: RuntimeArtifactPaths{
			CostReport:       summary.CostReportPath,
			BudgetEvents:     summary.BudgetEventsPath,
			AuditEvents:      summary.AuditEventsPath,
			Trace:            summary.TracePath,
			EvidenceManifest: summary.EvidenceManifestPath,
		},
		Trace:  traceArtifact,
		Budget: budgetArtifact,
		Evidence: RuntimeEvidenceStatus{
			ManifestPath: summary.EvidenceManifestPath,
		},
		Phases: buildPhaseArtifacts(vrs.vesselID, summary.Phases),
	}
	if manifest != nil {
		runtimeArtifact.Evidence.ClaimCount = len(manifest.Claims)
		runtimeArtifact.Evidence.StrongestLevel = manifest.StrongestLevel().String()
	}
	if auditArtifact.VesselID != "" || auditArtifact.EntryCount > 0 {
		runtimeArtifact.Audit = auditArtifact
	}
	runtimePath := filepath.Join(cfg.StateDir, "phases", vrs.vesselID, runtimeFileName)
	if err := saveRuntimeArtifact(runtimePath, runtimeArtifact); err != nil {
		log.Printf("warn: save runtime artifact index: %v", err)
	} else {
		summary.RuntimePath = runtimeArtifactRelativePath(vrs.vesselID)
	}
}

func saveRuntimeArtifact(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create artifact dir: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal artifact: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write artifact: %w", err)
	}
	return nil
}

func buildPhaseArtifacts(vesselID string, phases []PhaseSummary) []PhaseArtifact {
	artifacts := make([]PhaseArtifact, 0, len(phases))
	for _, phase := range phases {
		artifact := PhaseArtifact{
			Name:       phase.Name,
			Type:       phase.Type,
			Status:     phase.Status,
			OutputPath: phaseArtifactRelativePath(vesselID, phase.Name),
		}
		switch phase.Type {
		case "command":
			artifact.CommandPath = filepath.ToSlash(filepath.Join("phases", vesselID, phase.Name+".command"))
		default:
			artifact.PromptPath = filepath.ToSlash(filepath.Join("phases", vesselID, phase.Name+".prompt"))
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts
}

func buildBudgetEventsArtifact(summary *VesselSummary, vrs *vesselRunState, now time.Time) BudgetEventsArtifact {
	artifact := BudgetEventsArtifact{
		VesselID:       summary.VesselID,
		GeneratedAt:    now.UTC(),
		BudgetExceeded: summary.BudgetExceeded,
		Alerts:         []cost.BudgetAlert{},
	}
	if summary.BudgetMaxCostUSD != nil || summary.BudgetMaxTokens != nil {
		artifact.Action = string(vrs.budgetPolicy.Action)
	}
	if artifact.Action != "" && vrs.budgetPolicy.Action == config.BudgetExceededRequireApproval {
		artifact.ApprovalLabel = vrs.budgetPolicy.ApprovalLabel
	}
	if summary.BudgetMaxCostUSD != nil {
		v := *summary.BudgetMaxCostUSD
		artifact.MaxCostUSD = &v
	}
	if summary.BudgetMaxTokens != nil {
		v := *summary.BudgetMaxTokens
		artifact.MaxTokens = &v
	}
	if vrs.costTracker != nil {
		artifact.Alerts = vrs.costTracker.Alerts()
	}
	return artifact
}

func buildAuditEventsArtifact(auditLog *intermediary.AuditLog, vesselID string, now time.Time) (AuditEventsArtifact, error) {
	artifact := AuditEventsArtifact{
		VesselID:    vesselID,
		GeneratedAt: now.UTC(),
		Entries:     []intermediary.AuditEntry{},
	}
	if auditLog == nil {
		return artifact, nil
	}

	entries, err := auditLog.Entries()
	if err != nil {
		return artifact, fmt.Errorf("read audit log: %w", err)
	}

	filtered := make([]intermediary.AuditEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Intent.AgentID == vesselID {
			filtered = append(filtered, entry)
		}
	}
	artifact.SourcePath = filepath.ToSlash(auditLog.Path())
	artifact.EntryCount = len(filtered)
	artifact.Entries = filtered
	return artifact, nil
}

func buildTraceArtifact(ctx context.Context, cfg *config.Config, vesselID string, now time.Time) TraceArtifact {
	artifact := TraceArtifact{
		VesselID:    vesselID,
		GeneratedAt: now.UTC(),
	}
	if cfg != nil {
		artifact.CollectorEndpoint = cfg.Observability.Endpoint
	}
	spanCtx := oteltrace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return artifact
	}
	artifact.Enabled = true
	artifact.TraceID = spanCtx.TraceID().String()
	artifact.SpanID = spanCtx.SpanID().String()
	artifact.Traceparent = fmt.Sprintf("00-%s-%s-%02x", artifact.TraceID, artifact.SpanID, spanCtx.TraceFlags())
	return artifact
}
