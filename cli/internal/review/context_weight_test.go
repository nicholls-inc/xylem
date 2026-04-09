package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

type mockIssueRunner struct {
	calls       [][]string
	issueList   []contextWeightIssue
	createCount int
}

func (m *mockIssueRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	m.calls = append(m.calls, call)
	if name != "gh" || len(args) < 2 {
		return []byte{}, nil
	}
	if args[0] == "issue" && args[1] == "list" {
		return json.Marshal(m.issueList)
	}
	if args[0] == "issue" && args[1] == "create" {
		m.createCount++
		return []byte("https://github.com/owner/repo/issues/91"), nil
	}
	return []byte{}, nil
}

func TestGenerateContextWeightAuditDetectsPersistentOutlier(t *testing.T) {
	stateDir := t.TempDir()
	base := time.Date(2026, time.April, 9, 12, 0, 0, 0, time.UTC)

	for i := range 3 {
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:    "stable-" + string(rune('a'+i)),
			source:      "github-issue",
			workflow:    "stable",
			state:       "completed",
			startedAt:   base.Add(time.Duration(i) * time.Minute),
			endedAt:     base.Add(time.Duration(i)*time.Minute + time.Minute),
			totalInput:  120 + i*10,
			totalOutput: 40 + i*5,
			phases: []runner.PhaseSummary{{
				Name:            "plan",
				Type:            "prompt",
				Status:          "completed",
				InputTokensEst:  120 + i*10,
				OutputTokensEst: 40 + i*5,
			}},
		})
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:    "peer-" + string(rune('a'+i)),
			source:      "github-issue",
			workflow:    "peer",
			state:       "completed",
			startedAt:   base.Add(time.Duration(10+i) * time.Minute),
			endedAt:     base.Add(time.Duration(10+i)*time.Minute + time.Minute),
			totalInput:  150 + i*10,
			totalOutput: 60 + i*5,
			phases: []runner.PhaseSummary{{
				Name:            "plan",
				Type:            "prompt",
				Status:          "completed",
				InputTokensEst:  150 + i*10,
				OutputTokensEst: 60 + i*5,
			}},
		})
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:    "heavy-" + string(rune('a'+i)),
			source:      "github-issue",
			workflow:    "heavy",
			state:       "completed",
			startedAt:   base.Add(time.Duration(20+i) * time.Minute),
			endedAt:     base.Add(time.Duration(20+i)*time.Minute + time.Minute),
			totalInput:  620 + i*20,
			totalOutput: 220 + i*10,
			phases: []runner.PhaseSummary{
				{
					Name:            "plan",
					Type:            "prompt",
					Status:          "completed",
					InputTokensEst:  430 + i*10,
					OutputTokensEst: 140 + i*5,
				},
				{
					Name:            "implement",
					Type:            "prompt",
					Status:          "completed",
					InputTokensEst:  190 + i*10,
					OutputTokensEst: 80 + i*5,
				},
			},
		})
	}

	result, err := GenerateContextWeightAudit(stateDir, ContextWeightOptions{
		LookbackRuns: 20,
		MinSamples:   3,
		OutputDir:    "reviews",
		Now:          base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("GenerateContextWeightAudit() error = %v", err)
	}

	if len(result.Report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(result.Report.Findings))
	}
	finding := result.Report.Findings[0]
	if finding.Workflow != "heavy" {
		t.Fatalf("workflow = %q, want %q", finding.Workflow, "heavy")
	}
	if finding.AverageInputTokens < 600 {
		t.Fatalf("AverageInputTokens = %d, want >= 600", finding.AverageInputTokens)
	}
	if finding.RepeatedHighFootprintRuns != 3 {
		t.Fatalf("RepeatedHighFootprintRuns = %d, want 3", finding.RepeatedHighFootprintRuns)
	}
	if len(finding.LargestPhases) == 0 || finding.LargestPhases[0].Name != "plan" {
		t.Fatalf("largest phase = %+v, want plan first", finding.LargestPhases)
	}
	if finding.Fingerprint == "" {
		t.Fatal("Fingerprint = empty, want populated fingerprint")
	}
}

func TestGenerateContextWeightAuditIgnoresOneOffSpike(t *testing.T) {
	stateDir := t.TempDir()
	base := time.Date(2026, time.April, 9, 13, 0, 0, 0, time.UTC)

	for i := range 3 {
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:    "stable-" + string(rune('a'+i)),
			source:      "github-issue",
			workflow:    "stable",
			state:       "completed",
			startedAt:   base.Add(time.Duration(i) * time.Minute),
			endedAt:     base.Add(time.Duration(i)*time.Minute + time.Minute),
			totalInput:  100,
			totalOutput: 30,
		})
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:    "peer-" + string(rune('a'+i)),
			source:      "github-issue",
			workflow:    "peer",
			state:       "completed",
			startedAt:   base.Add(time.Duration(10+i) * time.Minute),
			endedAt:     base.Add(time.Duration(10+i)*time.Minute + time.Minute),
			totalInput:  120,
			totalOutput: 35,
		})
	}

	inputs := []int{100, 100, 350}
	for i, input := range inputs {
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:    "spiky-" + string(rune('a'+i)),
			source:      "github-issue",
			workflow:    "spiky",
			state:       "completed",
			startedAt:   base.Add(time.Duration(20+i) * time.Minute),
			endedAt:     base.Add(time.Duration(20+i)*time.Minute + time.Minute),
			totalInput:  input,
			totalOutput: 40,
		})
	}

	result, err := GenerateContextWeightAudit(stateDir, ContextWeightOptions{
		LookbackRuns: 20,
		MinSamples:   3,
		OutputDir:    "reviews",
		Now:          base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("GenerateContextWeightAudit() error = %v", err)
	}

	for _, finding := range result.Report.Findings {
		if finding.Workflow == "spiky" {
			t.Fatalf("unexpected finding for one-off spike: %+v", finding)
		}
	}
}

func TestGenerateContextWeightAuditIgnoresZeroTokenRunsInBaseline(t *testing.T) {
	stateDir := t.TempDir()
	base := time.Date(2026, time.April, 9, 13, 30, 0, 0, time.UTC)

	for i := range 3 {
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:    "command-only-" + string(rune('a'+i)),
			source:      "scheduled",
			workflow:    ContextWeightAuditWorkflow,
			state:       "completed",
			startedAt:   base.Add(time.Duration(i) * time.Minute),
			endedAt:     base.Add(time.Duration(i)*time.Minute + time.Minute),
			totalInput:  0,
			totalOutput: 0,
			phases: []runner.PhaseSummary{{
				Name:   "publish",
				Type:   "command",
				Status: "completed",
			}},
		})
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:    "stable-" + string(rune('a'+i)),
			source:      "github-issue",
			workflow:    "stable",
			state:       "completed",
			startedAt:   base.Add(time.Duration(10+i) * time.Minute),
			endedAt:     base.Add(time.Duration(10+i)*time.Minute + time.Minute),
			totalInput:  120 + i*10,
			totalOutput: 40 + i*5,
			phases: []runner.PhaseSummary{{
				Name:            "plan",
				Type:            "prompt",
				Status:          "completed",
				InputTokensEst:  120 + i*10,
				OutputTokensEst: 40 + i*5,
			}},
		})
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:    "peer-" + string(rune('a'+i)),
			source:      "github-issue",
			workflow:    "peer",
			state:       "completed",
			startedAt:   base.Add(time.Duration(15+i) * time.Minute),
			endedAt:     base.Add(time.Duration(15+i)*time.Minute + time.Minute),
			totalInput:  150 + i*10,
			totalOutput: 60 + i*5,
			phases: []runner.PhaseSummary{{
				Name:            "plan",
				Type:            "prompt",
				Status:          "completed",
				InputTokensEst:  150 + i*10,
				OutputTokensEst: 60 + i*5,
			}},
		})
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:    "heavy-" + string(rune('a'+i)),
			source:      "github-issue",
			workflow:    "heavy",
			state:       "completed",
			startedAt:   base.Add(time.Duration(20+i) * time.Minute),
			endedAt:     base.Add(time.Duration(20+i)*time.Minute + time.Minute),
			totalInput:  620 + i*20,
			totalOutput: 220 + i*10,
			phases: []runner.PhaseSummary{{
				Name:            "plan",
				Type:            "prompt",
				Status:          "completed",
				InputTokensEst:  620 + i*20,
				OutputTokensEst: 220 + i*10,
			}},
		})
	}

	result, err := GenerateContextWeightAudit(stateDir, ContextWeightOptions{
		LookbackRuns: 20,
		MinSamples:   3,
		OutputDir:    "reviews",
		Now:          base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("GenerateContextWeightAudit() error = %v", err)
	}

	if result.Report.Baseline.WorkflowInputTokens < 100 {
		t.Fatalf("WorkflowInputTokens baseline = %d, want command-only runs excluded", result.Report.Baseline.WorkflowInputTokens)
	}
	if len(result.Report.Findings) != 1 || result.Report.Findings[0].Workflow != "heavy" {
		t.Fatalf("findings = %+v, want only heavy workflow", result.Report.Findings)
	}
}

func TestPublishContextWeightIssuesDedupsRepeatedFindings(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 9, 14, 0, 0, 0, time.UTC)
	report := &ContextWeightReport{
		GeneratedAt: now,
		Baseline: ContextWeightBaseline{
			WorkflowInputTokens:  150,
			WorkflowOutputTokens: 50,
		},
		Findings: []ContextWeightFinding{{
			Fingerprint:               "deadbeef",
			Source:                    "github-issue",
			Workflow:                  "heavy",
			Samples:                   3,
			AverageInputTokens:        600,
			AverageOutputTokens:       200,
			RepeatedHighFootprintRuns: 3,
			LargestPhases:             []ContextWeightPhase{{Name: "plan", Type: "prompt", Samples: 3, AverageInputTokens: 400, AverageOutputTokens: 120}},
			Reasons:                   []string{"average input tokens 600 are 4.0x the repo baseline 150"},
			Remediations:              []string{"Trim the heaviest phase."},
		}},
	}
	runner := &mockIssueRunner{}

	first, err := PublishContextWeightIssues(context.Background(), stateDir, "owner/repo", runner, report, "reviews", now)
	if err != nil {
		t.Fatalf("first PublishContextWeightIssues() error = %v", err)
	}
	if runner.createCount != 1 {
		t.Fatalf("createCount after first publish = %d, want 1", runner.createCount)
	}
	if len(first) != 1 || !first[0].Created {
		t.Fatalf("first publish = %+v, want created issue", first)
	}

	second, err := PublishContextWeightIssues(context.Background(), stateDir, "owner/repo", runner, report, "reviews", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second PublishContextWeightIssues() error = %v", err)
	}
	if runner.createCount != 1 {
		t.Fatalf("createCount after second publish = %d, want 1", runner.createCount)
	}
	if len(second) != 1 || second[0].Created {
		t.Fatalf("second publish = %+v, want deduped existing issue", second)
	}
	if !strings.Contains(renderContextWeightIssueBody(report.Findings[0], report.Baseline), "xylem:context-weight-fingerprint=deadbeef") {
		t.Fatal("issue body missing fingerprint marker")
	}
}
