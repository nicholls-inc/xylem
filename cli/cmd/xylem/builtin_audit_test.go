package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	reviewpkg "github.com/nicholls-inc/xylem/cli/internal/review"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type auditTestRunner struct {
	calls       [][]string
	createCount int
}

func (r *auditTestRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if name == "git" && len(args) == 4 && args[0] == "rev-list" && args[1] == "--left-right" && args[2] == "--count" {
		return []byte("0\t0\n"), nil
	}
	if name == "gh" && len(args) >= 2 && args[0] == "issue" && args[1] == "create" {
		r.createCount++
		return []byte("https://github.com/owner/repo/issues/9" + strconv.Itoa(r.createCount)), nil
	}
	return []byte("[]"), nil
}

func (r *auditTestRunner) hasCallPrefix(want []string) bool {
	for _, call := range r.calls {
		if len(call) < len(want) {
			continue
		}
		match := true
		for i := range want {
			if call[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestRunBuiltInScheduledVesselsCompletesBuiltInAudits(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		sourceName       string
		schedule         string
		taskName         string
		vesselID         string
		workflow         string
		reportFile       string
		wantCreateCount  int
		wantCreatePrefix []string
	}{
		{
			name:            "context-weight-audit",
			sourceName:      "scheduled-audit",
			schedule:        "24h",
			taskName:        "context",
			vesselID:        "scheduled-audit-1",
			workflow:        reviewpkg.ContextWeightAuditWorkflow,
			reportFile:      "context-weight-audit.json",
			wantCreateCount: 0,
		},
		{
			name:            "harness-gap-analysis",
			sourceName:      "harness-gap",
			schedule:        "4h",
			taskName:        "analyze-gaps",
			vesselID:        "scheduled-gap-1",
			workflow:        reviewpkg.HarnessGapAnalysisWorkflow,
			reportFile:      "harness-gap-analysis.json",
			wantCreateCount: 0,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			cfg := &config.Config{
				StateDir: dir,
				Claude: config.ClaudeConfig{
					Command:      "claude",
					DefaultModel: "claude-sonnet-4-6",
				},
				Sources: map[string]config.SourceConfig{
					tc.sourceName: {
						Type:     "scheduled",
						Repo:     "owner/repo",
						Schedule: tc.schedule,
						Tasks: map[string]config.Task{
							tc.taskName: {Workflow: tc.workflow},
						},
					},
				},
			}

			q := queue.New(filepath.Join(dir, "queue.jsonl"))
			_, err := q.Enqueue(queue.Vessel{
				ID:        tc.vesselID,
				Source:    "scheduled",
				Ref:       "scheduled://" + tc.sourceName + "/" + tc.taskName + "@1",
				Workflow:  tc.workflow,
				State:     queue.StatePending,
				CreatedAt: time.Now().UTC(),
				Meta: map[string]string{
					"config_source":         tc.sourceName,
					"scheduled_task_name":   tc.taskName,
					"scheduled_bucket":      "1",
					"scheduled_config_name": tc.sourceName,
				},
			})
			if err != nil {
				t.Fatalf("Enqueue() error = %v", err)
			}

			cmdRunner := &auditTestRunner{}
			result, err := runBuiltInScheduledVessels(context.Background(), cfg, q, cmdRunner)
			if err != nil {
				t.Fatalf("runBuiltInScheduledVessels() error = %v", err)
			}
			if result.Completed != 1 {
				t.Fatalf("Completed = %d, want 1", result.Completed)
			}
			if result.Failed != 0 {
				t.Fatalf("Failed = %d, want 0", result.Failed)
			}
			if cmdRunner.createCount != tc.wantCreateCount {
				t.Fatalf("createCount = %d, want %d", cmdRunner.createCount, tc.wantCreateCount)
			}
			if len(tc.wantCreatePrefix) > 0 && !cmdRunner.hasCallPrefix(tc.wantCreatePrefix) {
				t.Fatalf("missing issue creation call with prefix %q", strings.Join(tc.wantCreatePrefix, " "))
			}

			vessel, err := q.FindByID(tc.vesselID)
			if err != nil {
				t.Fatalf("FindByID() error = %v", err)
			}
			if vessel.State != queue.StateCompleted {
				t.Fatalf("vessel.State = %s, want %s", vessel.State, queue.StateCompleted)
			}

			if _, err := os.Stat(filepath.Join(dir, "reviews", tc.reportFile)); err != nil {
				t.Fatalf("%s missing: %v", tc.reportFile, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "phases", tc.vesselID, "summary.json")); err != nil {
				t.Fatalf("summary.json missing: %v", err)
			}
		})
	}
}

func TestSmoke_S4_ScheduledWorkflowHealthVesselPublishesWeeklyReport(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		StateDir: dir,
		Claude: config.ClaudeConfig{
			Command:      "claude",
			DefaultModel: "claude-sonnet-4-6",
		},
		Sources: map[string]config.SourceConfig{
			"workflow-health": {
				Type:     "scheduled",
				Repo:     "owner/repo",
				Schedule: "@weekly",
				Tasks: map[string]config.Task{
					"report": {Workflow: reviewpkg.WorkflowHealthReportWorkflow},
				},
			},
		},
	}

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(queue.Vessel{
		ID:        "scheduled-health-1",
		Source:    "scheduled",
		Ref:       "scheduled://workflow-health/report@1",
		Workflow:  reviewpkg.WorkflowHealthReportWorkflow,
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
		Meta: map[string]string{
			"config_source":         "workflow-health",
			"scheduled_task_name":   "report",
			"scheduled_bucket":      "1",
			"scheduled_config_name": "workflow-health",
		},
	})
	require.NoError(t, err)

	cmdRunner := &auditTestRunner{}
	result, err := runBuiltInScheduledVessels(context.Background(), cfg, q, cmdRunner)
	require.NoError(t, err)

	require.Equal(t, 1, result.Completed)
	assert.Zero(t, result.Failed)
	assert.Equal(t, 1, cmdRunner.createCount)
	assert.True(t, cmdRunner.hasCallPrefix([]string{
		"gh", "issue", "create", "--repo", "owner/repo", "--title", "[xylem] weekly workflow health",
	}))

	vessel, err := q.FindByID("scheduled-health-1")
	require.NoError(t, err)
	require.Equal(t, queue.StateCompleted, vessel.State)

	_, err = os.Stat(filepath.Join(dir, "reviews", "workflow-health-report.json"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "phases", "scheduled-health-1", "summary.json"))
	require.NoError(t, err)
}
