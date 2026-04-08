package dtu_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	dtu "github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
)

func TestScenarioIssueLabelGateTimesOut(t *testing.T) {
	env := newScenarioEnv(t, "issue-label-gate-timeout.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "plan", prompt: "Plan issue {{.Issue.Number}}", gateWaitFor: "plan-approved"},
		{name: "implement", prompt: "Implement issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Queued:   "queued",
						Running:  "in-progress",
						TimedOut: "timed-out",
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 3), []string{"bug", "queued"})

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainer.Reporter = &reporter.Reporter{Runner: env.cmdRunner, Repo: "owner/repo"}

	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Waiting != 1 {
		t.Fatalf("DrainResult.Waiting = %d, want 1", drainResult.Waiting)
	}

	vessel, err := env.queue.FindByID("issue-3")
	if err != nil {
		t.Fatalf("FindByID(issue-3) error = %v", err)
	}
	if vessel.State != queue.StateWaiting {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateWaiting)
	}
	if vessel.WaitingSince == nil {
		t.Fatal("vessel.WaitingSince = nil, want waiting timestamp")
	}
	worktreePath := filepath.Join(env.repoDir, vessel.WorktreePath)
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("Stat(%q): %v", worktreePath, err)
	}

	if err := dtu.AdvanceRuntimeClock(61 * time.Minute); err != nil {
		t.Fatalf("AdvanceRuntimeClock() error = %v", err)
	}

	drainer.CheckWaitingVessels(context.Background())

	vessel, err = env.queue.FindByID("issue-3")
	if err != nil {
		t.Fatalf("FindByID(issue-3) after timeout: %v", err)
	}
	if vessel.State != queue.StateTimedOut {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateTimedOut)
	}
	if vessel.Error != "label gate timed out" {
		t.Fatalf("vessel.Error = %q, want %q", vessel.Error, "label gate timed out")
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 3), []string{"bug", "timed-out"})
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree %q still exists, err = %v", worktreePath, err)
	}

	state := loadState(t, env.store)
	issue := state.RepositoryBySlug("owner/repo").IssueByNumber(3)
	if issue == nil {
		t.Fatal("IssueByNumber(3) = nil")
		return
	}
	if len(issue.Comments) != 2 {
		t.Fatalf("len(issue.Comments) = %d, want 2", len(issue.Comments))
	}
	if got := issue.Comments[0].Body; !strings.Contains(got, "**xylem — phase `plan` completed**") {
		t.Fatalf("issue phase-complete comment = %q, want phase completion body", got)
	}
	if got, want := issue.Comments[1].Body, "xylem — timed out waiting for label `plan-approved` on phase `plan` after 1h1m0s"; got != want {
		t.Fatalf("issue timeout comment = %q, want %q", got, want)
	}

	events := readEvents(t, env.store)
	labelPolls := filterShimEvents(events, dtu.EventKindShimResult, "gh", []string{"issue", "view", "3", "--repo", "owner/repo", "--json", "labels"})
	if len(labelPolls) != 0 {
		t.Fatalf("len(label poll results) = %d, want 0 after direct timeout check", len(labelPolls))
	}
	commentCalls := filterShimEvents(events, dtu.EventKindShimInvocation, "gh", []string{"issue", "comment", "3", "--repo", "owner/repo", "--body"})
	if len(commentCalls) != 2 {
		t.Fatalf("len(issue comment invocations) = %d, want 2", len(commentCalls))
	}
	if !strings.Contains(strings.Join(commentCalls[1].Shim.Args, "\n"), "timed out waiting for label `plan-approved` on phase `plan` after 1h1m0s") {
		t.Fatalf("comment args = %v, want timeout message", commentCalls[1].Shim.Args)
	}
	editCalls := filterShimEvents(events, dtu.EventKindShimInvocation, "gh", []string{"issue", "edit", "3", "--repo", "owner/repo", "--add-label", "timed-out", "--remove-label", "in-progress"})
	if len(editCalls) != 1 {
		t.Fatalf("len(timeout label edit invocations) = %d, want 1", len(editCalls))
	}
}
