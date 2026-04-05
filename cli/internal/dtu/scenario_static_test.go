package dtu_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	dtu "github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
)

func staticIssueConfig(stateDir string, task config.Task) *config.Config {
	cfg := baseScenarioConfig(stateDir)
	if len(task.Labels) == 0 {
		task.Labels = []string{"bug"}
	}
	if task.Workflow == "" {
		task.Workflow = "fix-bug"
	}
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": task,
			},
		},
	}
	return cfg
}

func scanScenario(t *testing.T, env *dtuScenarioEnv, cfg *config.Config) scanner.ScanResult {
	t.Helper()

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	result, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	return result
}

func eventDelta(t *testing.T, store *dtu.Store, before int) []dtu.Event {
	t.Helper()

	events := readEvents(t, store)
	if before > len(events) {
		t.Fatalf("before index %d exceeds event count %d", before, len(events))
	}
	return append([]dtu.Event(nil), events[before:]...)
}

func readScenarioFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(data)
}

func filterPhaseInvocations(events []dtu.Event, command string, phase string) []dtu.Event {
	filtered := make([]dtu.Event, 0, len(events))
	for _, event := range events {
		if event.Kind != dtu.EventKindShimInvocation || event.Shim == nil {
			continue
		}
		if event.Shim.Command != command || event.Shim.Phase != phase {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func TestScenarioIssueDedupBranchExistsSkipsScan(t *testing.T) {
	env := newScenarioEnv(t, "issue-dedup-branch-exists.yaml")
	defer withWorkingDir(t, env.repoDir)()

	cfg := staticIssueConfig(env.stateDir, config.Task{})
	before := len(readEvents(t, env.store))

	result := scanScenario(t, env, cfg)
	if result.Added != 0 {
		t.Fatalf("ScanResult.Added = %d, want 0", result.Added)
	}

	vessels, err := env.queue.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("len(vessels) = %d, want 0", len(vessels))
	}

	delta := eventDelta(t, env.store, before)
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "gh", []string{"search", "issues"})); got != 1 {
		t.Fatalf("len(search invocations) = %d, want 1", got)
	}
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "git", []string{"ls-remote", "--heads", "origin", "fix/issue-11-*"})); got != 1 {
		t.Fatalf("len(branch checks) = %d, want 1", got)
	}
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "gh", []string{"pr", "list"})); got != 0 {
		t.Fatalf("len(pr list invocations) = %d, want 0", got)
	}
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "gh", []string{"issue", "edit"})); got != 0 {
		t.Fatalf("len(issue edit invocations) = %d, want 0", got)
	}
}

func TestScenarioIssueDedupOpenPRExistsSkipsScan(t *testing.T) {
	env := newScenarioEnv(t, "issue-dedup-open-pr-exists.yaml")
	defer withWorkingDir(t, env.repoDir)()

	cfg := staticIssueConfig(env.stateDir, config.Task{})
	before := len(readEvents(t, env.store))

	result := scanScenario(t, env, cfg)
	if result.Added != 0 {
		t.Fatalf("ScanResult.Added = %d, want 0", result.Added)
	}

	vessels, err := env.queue.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("len(vessels) = %d, want 0", len(vessels))
	}

	delta := eventDelta(t, env.store, before)
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "git", []string{"ls-remote", "--heads", "origin"})); got != 2 {
		t.Fatalf("len(branch checks) = %d, want 2", got)
	}
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "gh", []string{"pr", "list", "--repo", "owner/repo", "--search", "head:fix/issue-12-", "--state", "open"})); got != 1 {
		t.Fatalf("len(open pr checks) = %d, want 1", got)
	}
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "gh", []string{"pr", "list", "--repo", "owner/repo", "--search", "head:fix/issue-12-", "--state", "merged"})); got != 0 {
		t.Fatalf("len(merged pr checks) = %d, want 0", got)
	}
}

func TestScenarioIssueDedupMergedPRExistsSkipsScan(t *testing.T) {
	env := newScenarioEnv(t, "issue-dedup-merged-pr-exists.yaml")
	defer withWorkingDir(t, env.repoDir)()

	cfg := staticIssueConfig(env.stateDir, config.Task{})
	before := len(readEvents(t, env.store))

	result := scanScenario(t, env, cfg)
	if result.Added != 0 {
		t.Fatalf("ScanResult.Added = %d, want 0", result.Added)
	}

	vessels, err := env.queue.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("len(vessels) = %d, want 0", len(vessels))
	}

	delta := eventDelta(t, env.store, before)
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "git", []string{"ls-remote", "--heads", "origin"})); got != 2 {
		t.Fatalf("len(branch checks) = %d, want 2", got)
	}
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "gh", []string{"pr", "list", "--repo", "owner/repo", "--search", "head:fix/issue-13-", "--state", "open"})); got != 1 {
		t.Fatalf("len(open fix pr checks) = %d, want 1", got)
	}
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "gh", []string{"pr", "list", "--repo", "owner/repo", "--search", "head:feat/issue-13-", "--state", "open"})); got != 1 {
		t.Fatalf("len(open feat pr checks) = %d, want 1", got)
	}
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "gh", []string{"pr", "list", "--repo", "owner/repo", "--search", "head:fix/issue-13-", "--state", "merged"})); got != 1 {
		t.Fatalf("len(merged pr checks) = %d, want 1", got)
	}
}

func TestScenarioIssueDedupFailedFingerprintSkipsUnchangedFailure(t *testing.T) {
	env := newScenarioEnv(t, "issue-dedup-failed-fingerprint.yaml")
	defer withWorkingDir(t, env.repoDir)()

	cfg := staticIssueConfig(env.stateDir, config.Task{})
	first := scanScenario(t, env, cfg)
	if first.Added != 1 {
		t.Fatalf("first ScanResult.Added = %d, want 1", first.Added)
	}

	seed, err := env.queue.FindByID("issue-14")
	if err != nil {
		t.Fatalf("FindByID(issue-14) error = %v", err)
	}
	fingerprint := seed.Meta["source_input_fingerprint"]
	if fingerprint == "" {
		t.Fatal("source_input_fingerprint = empty")
	}

	if _, err := env.queue.Dequeue(); err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if err := env.queue.Update("issue-14", queue.StateFailed, "boom"); err != nil {
		t.Fatalf("Update(issue-14, failed) error = %v", err)
	}

	before := len(readEvents(t, env.store))
	second := scanScenario(t, env, cfg)
	if second.Added != 0 {
		t.Fatalf("second ScanResult.Added = %d, want 0", second.Added)
	}

	vessels, err := env.queue.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("len(vessels) = %d, want 1", len(vessels))
	}

	latest, err := env.queue.FindLatestByRef("https://github.com/owner/repo/issues/14")
	if err != nil {
		t.Fatalf("FindLatestByRef() error = %v", err)
	}
	if latest.State != queue.StateFailed {
		t.Fatalf("latest.State = %q, want %q", latest.State, queue.StateFailed)
	}
	if latest.Meta["source_input_fingerprint"] != fingerprint {
		t.Fatalf("latest fingerprint = %q, want %q", latest.Meta["source_input_fingerprint"], fingerprint)
	}

	delta := eventDelta(t, env.store, before)
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "gh", []string{"search", "issues"})); got != 1 {
		t.Fatalf("len(search invocations) = %d, want 1", got)
	}
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "git", []string{"ls-remote"})); got != 0 {
		t.Fatalf("len(branch checks) = %d, want 0", got)
	}
	if got := len(filterShimEvents(delta, dtu.EventKindShimInvocation, "gh", []string{"pr", "list"})); got != 0 {
		t.Fatalf("len(pr list invocations) = %d, want 0", got)
	}
}

func TestScenarioIssueCommandGateRetryPassesOnRetry(t *testing.T) {
	env := newScenarioEnv(t, "issue-command-gate-retry.yaml")
	defer withWorkingDir(t, env.repoDir)()

	gateTarget := filepath.Join(env.stateDir, "phases", "issue-7", "implement.output")
	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{
			name:           "implement",
			prompt:         "Implement issue {{.Issue.Number}}\n{{.GateResult}}",
			gateType:       "command",
			gateRun:        fmt.Sprintf("grep -qx 'fixed' %q || { echo 'gate output: missing fixed marker'; exit 1; }", gateTarget),
			gateRetries:    1,
			gateRetryDelay: "1ms",
		},
		{name: "pr", prompt: "Create PR for issue {{.Issue.Number}}"},
	})

	cfg := staticIssueConfig(env.stateDir, config.Task{
		Labels:   []string{"bug"},
		Workflow: "fix-bug",
		StatusLabels: &config.StatusLabels{
			Completed: "done",
		},
	})

	scanResult := scanScenario(t, env, cfg)
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	vessel, err := env.queue.FindByID("issue-7")
	if err != nil {
		t.Fatalf("FindByID(issue-7) error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}
	if vessel.CurrentPhase != 2 {
		t.Fatalf("vessel.CurrentPhase = %d, want 2", vessel.CurrentPhase)
	}
	if vessel.GateRetries != 0 {
		t.Fatalf("vessel.GateRetries = %d, want 0", vessel.GateRetries)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 7), []string{"bug", "done"})

	phasesDir := filepath.Join(env.stateDir, "phases", "issue-7")
	promptPath := filepath.Join(phasesDir, "implement.prompt")
	prompt := readScenarioFile(t, promptPath)
	if !strings.Contains(prompt, "gate check failed") {
		t.Fatalf("implement prompt = %q, want gate failure context", prompt)
	}
	if !strings.Contains(prompt, "gate output: missing fixed marker") {
		t.Fatalf("implement prompt = %q, want gate output", prompt)
	}

	output := strings.TrimSpace(readScenarioFile(t, filepath.Join(phasesDir, "implement.output")))
	if output != "fixed" {
		t.Fatalf("implement output = %q, want %q", output, "fixed")
	}

	events := readEvents(t, env.store)
	implementInvocations := filterPhaseInvocations(events, "claude", "implement")
	if len(implementInvocations) != 2 {
		t.Fatalf("len(implement invocations) = %d, want 2", len(implementInvocations))
	}
	if implementInvocations[0].Shim.Attempt != 1 {
		t.Fatalf("first implement attempt = %d, want 1", implementInvocations[0].Shim.Attempt)
	}
	if implementInvocations[1].Shim.Attempt != 2 {
		t.Fatalf("second implement attempt = %d, want 2", implementInvocations[1].Shim.Attempt)
	}
	if !strings.Contains(implementInvocations[1].Shim.Prompt, "gate output: missing fixed marker") {
		t.Fatalf("second implement prompt = %q, want gate output", implementInvocations[1].Shim.Prompt)
	}

	prInvocations := filterPhaseInvocations(events, "claude", "pr")
	if len(prInvocations) != 1 {
		t.Fatalf("len(pr invocations) = %d, want 1", len(prInvocations))
	}
}
