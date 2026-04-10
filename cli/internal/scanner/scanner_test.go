package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/review"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRunner struct {
	calls   [][]string
	outputs map[string][]byte
	errs    map[string]error
}

func newMock() *mockRunner {
	return &mockRunner{
		outputs: make(map[string][]byte),
		errs:    make(map[string]error),
	}
}

func (m *mockRunner) set(out []byte, args ...string) {
	m.outputs[strings.Join(args, " ")] = out
}

func (m *mockRunner) setErr(err error, args ...string) {
	m.errs[strings.Join(args, " ")] = err
}

func (m *mockRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	parts := append([]string{name}, args...)
	m.calls = append(m.calls, parts)
	key := strings.Join(parts, " ")
	if err, ok := m.errs[key]; ok {
		return nil, err
	}
	if out, ok := m.outputs[key]; ok {
		return out, nil
	}
	return []byte("[]"), nil
}

// ghIssue mirrors the GitHub issue JSON structure for test helpers.
type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func makeConfig(dir string) *config.Config {
	return &config.Config{
		Repo:        "owner/repo",
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Exclude:     []string{"wontfix", "duplicate"},
		Claude:      config.ClaudeConfig{Command: "claude", Template: "{{.Command}} -p \"/{{.Workflow}} {{.Ref}}\" --max-turns {{.MaxTurns}}"},
		Tasks: map[string]config.Task{
			"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"},
		},
		Sources: map[string]config.SourceConfig{
			"github": {
				Type:    "github",
				Repo:    "owner/repo",
				Exclude: []string{"wontfix", "duplicate"},
				Tasks: map[string]config.Task{
					"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"},
				},
			},
		},
	}
}

func issueJSON(issues []ghIssue) []byte {
	b, _ := json.Marshal(issues)
	return b
}

func scanFingerprint(title, body string, labels []string) string {
	sorted := append([]string(nil), labels...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join([]string{
		title,
		body,
		strings.Join(sorted, ","),
	}, "\n")))
	return fmt.Sprintf("%x", sum)
}

func TestScanFindsIssues(t *testing.T) {
	dir := t.TempDir()
	queueFile := filepath.Join(dir, "queue.jsonl")
	cfg := makeConfig(dir)
	q := queue.New(queueFile)
	r := newMock()

	issues := []ghIssue{
		{Number: 1, Title: "fix null response", URL: "https://github.com/owner/repo/issues/1", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
		{Number: 2, Title: "fix panic on empty", URL: "https://github.com/owner/repo/issues/2", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 2 {
		t.Errorf("expected 2 added, got %d", result.Added)
	}
	if result.Paused {
		t.Error("expected not paused")
	}
	vessels, _ := q.List()
	if len(vessels) != 2 {
		t.Errorf("expected 2 vessels in queue, got %d", len(vessels))
	}
	// Verify new vessel format
	for _, v := range vessels {
		if v.Source != "github-issue" {
			t.Errorf("expected source github-issue, got %q", v.Source)
		}
		if v.Ref == "" {
			t.Error("expected Ref to be set")
		}
		if v.Meta["issue_num"] == "" {
			t.Error("expected Meta[issue_num] to be set")
		}
	}
}

func TestBacklogCountDeduplicatesAndSkipsExcludedIssues(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	srcCfg := cfg.Sources["github"]
	srcCfg.Tasks["triage-incidents"] = config.Task{Labels: []string{"incident"}, Workflow: "triage"}
	cfg.Sources["github"] = srcCfg
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	bugIssues := []ghIssue{
		{Number: 1, Title: "fix null response", URL: "https://github.com/owner/repo/issues/1", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
		{Number: 2, Title: "investigate prod incident", URL: "https://github.com/owner/repo/issues/2", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "incident"}}},
		{Number: 3, Title: "skip duplicate backlog item", URL: "https://github.com/owner/repo/issues/3", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "wontfix"}}},
	}
	incidentIssues := []ghIssue{
		{Number: 2, Title: "investigate prod incident", URL: "https://github.com/owner/repo/issues/2", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "incident"}}},
		{Number: 4, Title: "on-call handoff", URL: "https://github.com/owner/repo/issues/4", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "incident"}}},
	}
	r.set(issueJSON(bugIssues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	r.set(issueJSON(incidentIssues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "incident")

	s := New(cfg, q, r)
	count, err := s.BacklogCount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestScanExcludedLabel(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 1, Title: "won't fix", URL: "https://github.com/owner/repo/issues/1", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "wontfix"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 0 {
		t.Errorf("expected 0 added (excluded), got %d", result.Added)
	}
}

func TestScanAlreadyQueued(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	queueFile := filepath.Join(dir, "queue.jsonl")
	q := queue.New(queueFile)
	r := newMock()

	// Pre-enqueue using new format
	_, _ = q.Enqueue(queue.Vessel{
		ID: "issue-1", Source: "github-issue",
		Ref: "https://github.com/owner/repo/issues/1", Workflow: "fix-bug",
		Meta:  map[string]string{"issue_num": "1"},
		State: queue.StatePending, CreatedAt: queue.Vessel{}.CreatedAt,
	})

	issues := []ghIssue{
		{Number: 1, Title: "already queued", URL: "https://github.com/owner/repo/issues/1", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 0 {
		t.Errorf("expected 0 added (already queued), got %d", result.Added)
	}
}

func TestScanExistingBranch(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 42, Title: "has branch", URL: "https://github.com/owner/repo/issues/42", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	r.set([]byte("abc123\trefs/heads/fix/issue-42-something"), "git", "ls-remote", "--heads", "origin", "fix/issue-42-*")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 0 {
		t.Errorf("expected 0 added (existing branch), got %d", result.Added)
	}
}

func TestScanBuildsScheduledSource(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	cfg.Sources = map[string]config.SourceConfig{
		"sota-gap": {
			Type:     "scheduled",
			Repo:     "owner/repo",
			Schedule: "@weekly",
			Tasks: map[string]config.Task{
				"sota": {Workflow: "sota-gap-analysis", Ref: "sota-gap-analysis"},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	s := New(cfg, q, newMock())

	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("Added = %d, want 1", result.Added)
	}
	vessels, err := q.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vessels) != 1 || vessels[0].Source != "scheduled" {
		t.Fatalf("vessels = %#v, want one scheduled vessel", vessels)
	}
	if got := vessels[0].Meta["config_source"]; got != "sota-gap" {
		t.Fatalf("config_source = %q, want sota-gap", got)
	}
}

func TestScanExistingPR(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 55, Title: "has pr", URL: "https://github.com/owner/repo/issues/55", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	r.set([]byte(`[{"number":99,"headRefName":"fix/issue-55-null-fix"}]`),
		"gh", "pr", "list", "--repo", "owner/repo", "--search", "head:fix/issue-55-", "--state", "open", "--json", "number,headRefName", "--limit", "5")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 0 {
		t.Errorf("expected 0 added (open PR exists), got %d", result.Added)
	}
}

func TestScanScheduleSource(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Claude:      config.ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]config.SourceConfig{
			"doc-gardener": {
				Type:     "schedule",
				Cadence:  "1h",
				Workflow: "doc-garden",
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	s := New(cfg, q, newMock())
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("expected 1 added, got %d", result.Added)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if vessels[0].Source != "schedule" {
		t.Errorf("Source = %q, want schedule", vessels[0].Source)
	}
	if vessels[0].Workflow != "doc-garden" {
		t.Errorf("Workflow = %q, want doc-garden", vessels[0].Workflow)
	}
	if vessels[0].Meta["config_source"] != "doc-gardener" {
		t.Errorf("config_source = %q, want doc-gardener", vessels[0].Meta["config_source"])
	}
	if vessels[0].Meta["schedule.cadence"] != "1h" {
		t.Errorf("schedule.cadence = %q, want 1h", vessels[0].Meta["schedule.cadence"])
	}
}

func TestScanMultipleScheduleSourcesKeepConfigNamesDistinct(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Claude:      config.ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]config.SourceConfig{
			"doc-gardener": {
				Type:     "schedule",
				Cadence:  "1h",
				Workflow: "doc-garden",
			},
			"doctor": {
				Type:     "schedule",
				Cadence:  "@daily",
				Workflow: "doctor",
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	s := New(cfg, q, newMock())
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Added != 2 {
		t.Fatalf("Added = %d, want 2", result.Added)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vessels) != 2 {
		t.Fatalf("len(vessels) = %d, want 2", len(vessels))
	}

	byConfigSource := make(map[string]queue.Vessel, len(vessels))
	for _, vessel := range vessels {
		byConfigSource[vessel.Meta["config_source"]] = vessel
		if vessel.Source != "schedule" {
			t.Fatalf("vessel.Source = %q, want schedule", vessel.Source)
		}
	}

	docGardener, ok := byConfigSource["doc-gardener"]
	if !ok {
		t.Fatal("missing doc-gardener vessel")
	}
	if docGardener.Workflow != "doc-garden" {
		t.Fatalf("doc-gardener workflow = %q, want doc-garden", docGardener.Workflow)
	}
	if docGardener.Meta["schedule.cadence"] != "1h" {
		t.Fatalf("doc-gardener cadence = %q, want 1h", docGardener.Meta["schedule.cadence"])
	}

	doctor, ok := byConfigSource["doctor"]
	if !ok {
		t.Fatal("missing doctor vessel")
	}
	if doctor.Workflow != "doctor" {
		t.Fatalf("doctor workflow = %q, want doctor", doctor.Workflow)
	}
	if doctor.Meta["schedule.cadence"] != "@daily" {
		t.Fatalf("doctor cadence = %q, want @daily", doctor.Meta["schedule.cadence"])
	}
	if doctor.Ref == docGardener.Ref {
		t.Fatalf("schedule refs collided: %q", doctor.Ref)
	}
}

func TestScanSkipsUnchangedFailedIssue(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 1, Title: "same title", Body: "same body", URL: "https://github.com/owner/repo/issues/1", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	for _, prefix := range []string{"fix", "feat"} {
		r.set([]byte(""), "git", "ls-remote", "--heads", "origin", fmt.Sprintf("%s/issue-%d-*", prefix, 1))
		r.set([]byte("[]"), "gh", "pr", "list", "--repo", "owner/repo", "--search", fmt.Sprintf("head:%s/issue-%d-", prefix, 1), "--state", "open", "--json", "number,headRefName", "--limit", "5")
	}

	fingerprint := scanFingerprint("same title", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-1",
		Source:   "github-issue",
		Ref:      "https://github.com/owner/repo/issues/1",
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "1",
			"source_input_fingerprint": fingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("enqueue seed: %v", err)
	}
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if err := q.Update("issue-1", queue.StateFailed, "boom"); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 0 {
		t.Fatalf("expected unchanged failed issue to be skipped, added=%d", result.Added)
	}
}

func TestScanReenqueuesChangedFailedIssue(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 1, Title: "same title", Body: "updated body", URL: "https://github.com/owner/repo/issues/1", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	for _, prefix := range []string{"fix", "feat"} {
		r.set([]byte(""), "git", "ls-remote", "--heads", "origin", fmt.Sprintf("%s/issue-%d-*", prefix, 1))
		r.set([]byte("[]"), "gh", "pr", "list", "--repo", "owner/repo", "--search", fmt.Sprintf("head:%s/issue-%d-", prefix, 1), "--state", "open", "--json", "number,headRefName", "--limit", "5")
	}

	oldFingerprint := scanFingerprint("same title", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-1",
		Source:   "github-issue",
		Ref:      "https://github.com/owner/repo/issues/1",
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "1",
			"source_input_fingerprint": oldFingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("enqueue seed: %v", err)
	}
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if err := q.Update("issue-1", queue.StateFailed, "boom"); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("expected changed failed issue to be re-enqueued, added=%d", result.Added)
	}
	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list queue: %v", err)
	}
	if len(vessels) != 2 {
		t.Fatalf("expected 2 queue entries, got %d", len(vessels))
	}
	if vessels[1].Meta["source_input_fingerprint"] == oldFingerprint {
		t.Fatal("expected updated fingerprint for changed issue input")
	}
}

func TestScanPRFalsePositiveIgnored(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 1, Title: "real bug", URL: "https://github.com/owner/repo/issues/1", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	r.set([]byte(`[{"number":200,"headRefName":"chore/priority-1-ci-fix"}]`),
		"gh", "pr", "list", "--repo", "owner/repo", "--search", "head:fix/issue-1-", "--state", "open", "--json", "number,headRefName", "--limit", "5")
	r.set([]byte(`[]`),
		"gh", "pr", "list", "--repo", "owner/repo", "--search", "head:feat/issue-1-", "--state", "open", "--json", "number,headRefName", "--limit", "5")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 1 {
		t.Errorf("expected 1 added (false positive PR should be ignored), got %d", result.Added)
	}
}

func TestScanCrossTaskDedupDeterministic(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	cfg.Tasks = map[string]config.Task{
		"fix-bugs":  {Labels: []string{"bug"}, Workflow: "fix-bug"},
		"emergency": {Labels: []string{"urgent"}, Workflow: "fix-bug"},
	}
	cfg.Sources = map[string]config.SourceConfig{
		"github": {
			Type:    "github",
			Repo:    "owner/repo",
			Exclude: []string{"wontfix", "duplicate"},
			Tasks:   cfg.Tasks,
		},
	}
	r := newMock()

	sharedIssue := []ghIssue{
		{Number: 10, Title: "shared issue", URL: "https://github.com/owner/repo/issues/10", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "urgent"}}},
	}
	r.set(issueJSON(sharedIssue), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	r.set(issueJSON(sharedIssue), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "urgent")

	for i := 0; i < 5; i++ {
		qFile := filepath.Join(dir, fmt.Sprintf("queue-%d.jsonl", i))
		qi := queue.New(qFile)
		si := New(cfg, qi, r)
		_, err := si.Scan(context.Background())
		if err != nil {
			t.Fatalf("run %d: unexpected error: %v", i, err)
		}
		vessels, _ := qi.List()
		if len(vessels) != 1 {
			t.Errorf("run %d: expected exactly 1 vessel in queue, got %d", i, len(vessels))
		}
	}
}

func TestScanPaused(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	os.WriteFile(filepath.Join(dir, "paused"), []byte{}, 0o644)

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Paused {
		t.Error("expected Paused=true")
	}
	if result.Added != 0 {
		t.Errorf("expected 0 added when paused, got %d", result.Added)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no gh calls when paused, got %d", len(r.calls))
	}
}

func TestScanGHFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	r.setErr(errors.New("network error"), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	s := New(cfg, q, r)
	_, err := s.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error from gh failure, got nil")
	}
}

func TestScanMultipleTasks(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	cfg.Tasks = map[string]config.Task{
		"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"},
		"features": {Labels: []string{"low-effort"}, Workflow: "implement-feature"},
	}
	cfg.Sources = map[string]config.SourceConfig{
		"github": {
			Type:    "github",
			Repo:    "owner/repo",
			Exclude: []string{"wontfix", "duplicate"},
			Tasks:   cfg.Tasks,
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	bugIssues := []ghIssue{
		{Number: 1, Title: "null bug", URL: "https://github.com/owner/repo/issues/1", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	featureIssues := []ghIssue{
		{Number: 2, Title: "add feature", URL: "https://github.com/owner/repo/issues/2", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "low-effort"}}},
	}

	r.set(issueJSON(bugIssues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	r.set(issueJSON(featureIssues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "low-effort")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 2 {
		t.Errorf("expected 2 total added, got %d", result.Added)
	}
	vessels, _ := q.List()
	workflows := make(map[string]bool)
	for _, j := range vessels {
		workflows[j.Workflow] = true
	}
	if !workflows["fix-bug"] || !workflows["implement-feature"] {
		t.Errorf("expected both workflows queued, got: %v", workflows)
	}
}

func TestScanGHReturnsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	r.set([]byte(`{not valid json`), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	s := New(cfg, q, r)
	_, err := s.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed gh JSON output")
	}
}

func TestHasOpenPRMalformedJSONIgnored(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 77, Title: "test issue", URL: "https://github.com/owner/repo/issues/77", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	r.set([]byte(`not json at all`),
		"gh", "pr", "list", "--repo", "owner/repo", "--search", "head:fix/issue-77-", "--state", "open", "--json", "number,headRefName", "--limit", "5")
	r.set([]byte(`not json`),
		"gh", "pr", "list", "--repo", "owner/repo", "--search", "head:feat/issue-77-", "--state", "open", "--json", "number,headRefName", "--limit", "5")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 1 {
		t.Errorf("expected 1 added (malformed PR JSON should not block), got %d", result.Added)
	}
}

func TestHasOpenPRGHErrorIgnored(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 88, Title: "test issue", URL: "https://github.com/owner/repo/issues/88", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	r.setErr(errors.New("gh auth error"),
		"gh", "pr", "list", "--repo", "owner/repo", "--search", "head:fix/issue-88-", "--state", "open", "--json", "number,headRefName", "--limit", "5")
	r.setErr(errors.New("gh auth error"),
		"gh", "pr", "list", "--repo", "owner/repo", "--search", "head:feat/issue-88-", "--state", "open", "--json", "number,headRefName", "--limit", "5")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 1 {
		t.Errorf("expected 1 added (gh errors for PR check should not block), got %d", result.Added)
	}
}

func TestScanExistingBranchFeatPrefix(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 99, Title: "has feat branch", URL: "https://github.com/owner/repo/issues/99", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")
	r.set([]byte(""), "git", "ls-remote", "--heads", "origin", "fix/issue-99-*")
	r.set([]byte("abc123\trefs/heads/feat/issue-99-add-feature"), "git", "ls-remote", "--heads", "origin", "feat/issue-99-*")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 0 {
		t.Errorf("expected 0 added (feat branch exists), got %d", result.Added)
	}
}

func TestScanAppliesQueuedLabel(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	cfg.Sources = map[string]config.SourceConfig{
		"github": {
			Type:    "github",
			Repo:    "owner/repo",
			Exclude: []string{"wontfix"},
			Tasks: map[string]config.Task{
				"fix-bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Queued: "queued",
					},
				},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 5, Title: "fix something", URL: "https://github.com/owner/repo/issues/5", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("expected 1 added, got %d", result.Added)
	}

	// Verify OnEnqueue triggered gh issue edit --add-label queued
	foundLabel := false
	for _, call := range r.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "issue edit") && strings.Contains(joined, "queued") && strings.Contains(joined, "--add-label") {
			foundLabel = true
			break
		}
	}
	if !foundLabel {
		t.Errorf("expected gh issue edit --add-label queued call, calls were: %v", r.calls)
	}
}

func TestScanNoQueuedLabelWhenNotConfigured(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{Number: 6, Title: "fix other", URL: "https://github.com/owner/repo/issues/6", Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}}},
	}
	r.set(issueJSON(issues), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("expected 1 added, got %d", result.Added)
	}

	// No gh issue edit calls expected when status_labels not configured
	for _, call := range r.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "issue edit") {
			t.Errorf("unexpected gh issue edit call when status_labels not configured: %v", joined)
		}
	}
}

func TestScanEmptyIssuesList(t *testing.T) {
	dir := t.TempDir()
	cfg := makeConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 0 {
		t.Errorf("expected 0 added, got %d", result.Added)
	}
}

// ghPRForScanner mirrors the GitHub PR JSON structure for test helpers.
type ghPRForScanner struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	HeadRefName string `json:"headRefName"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func prJSON(prs []ghPRForScanner) []byte {
	b, _ := json.Marshal(prs)
	return b
}

func TestScanPREvents(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Claude:      config.ClaudeConfig{Command: "claude"},
		Sources: map[string]config.SourceConfig{
			"pr-events": {
				Type: "github-pr-events",
				Repo: "owner/repo",
				Tasks: map[string]config.Task{
					"review": {
						Workflow: "handle-review",
						On: &config.PREventsConfig{
							Labels: []string{"needs-review"},
						},
					},
				},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPRForScanner{
		{
			Number:      10,
			Title:       "test PR",
			URL:         "https://github.com/owner/repo/pull/10",
			HeadRefName: "feature-branch",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "needs-review"}},
		},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 1 {
		t.Errorf("expected 1 added, got %d", result.Added)
	}
	vessels, _ := q.List()
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel in queue, got %d", len(vessels))
	}
	if vessels[0].Source != "github-pr-events" {
		t.Errorf("expected source github-pr-events, got %q", vessels[0].Source)
	}
	if vessels[0].Workflow != "handle-review" {
		t.Errorf("expected workflow handle-review, got %q", vessels[0].Workflow)
	}
}

func TestConvertPREventsTasksParsesDebounce(t *testing.T) {
	tasks := convertPREventsTasks(map[string]config.Task{
		"review": {
			Workflow: "review-pr",
			On: &config.PREventsConfig{
				PROpened:        true,
				PRHeadUpdated:   true,
				Debounce:        "10m",
				ReviewSubmitted: true,
			},
		},
		"defaulted": {
			Workflow: "review-pr",
			On: &config.PREventsConfig{
				Commented: true,
			},
		},
	})

	if got := tasks["review"].Debounce; got != 10*time.Minute {
		t.Fatalf("review debounce = %v, want 10m", got)
	}
	if !tasks["review"].PROpened || !tasks["review"].PRHeadUpdated || !tasks["review"].ReviewSubmitted {
		t.Fatalf("review task triggers were not copied: %#v", tasks["review"])
	}
	if got := tasks["defaulted"].Debounce; got != source.UnsetPREventsDebounce {
		t.Fatalf("defaulted debounce = %v, want unset sentinel %v", got, source.UnsetPREventsDebounce)
	}
}

type stubBudgetGate struct {
	decision cost.Decision
	classes  []string
}

func (g *stubBudgetGate) Check(class string) cost.Decision {
	g.classes = append(g.classes, class)
	return g.decision
}

func TestScanBudgetGateSkipDoesNotEnqueueOrRunHooks(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Claude:      config.ClaudeConfig{Command: "claude"},
		Sources: map[string]config.SourceConfig{
			"github": {
				Type: "github",
				Repo: "owner/repo",
				Tasks: map[string]config.Task{
					"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"},
				},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()
	r.set(issueJSON([]ghIssue{{
		Number: 1,
		Title:  "blocked by budget",
		Body:   "body",
		URL:    "https://github.com/owner/repo/issues/1",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	gate := &stubBudgetGate{decision: cost.Decision{Allowed: false, Reason: "stub exhausted", RemainingUSD: 0}}
	s := New(cfg, q, r)
	s.BudgetGate = gate

	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("Scan result = %#v, want 0 added and 1 skipped", result)
	}
	if len(gate.classes) != 1 || gate.classes[0] != "fix-bug" {
		t.Fatalf("budget gate classes = %#v, want [fix-bug]", gate.classes)
	}
	vessels, err := q.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected no queued vessels, got %d", len(vessels))
	}
	for _, call := range r.calls {
		if len(call) >= 3 && call[0] == "gh" && call[1] == "issue" && call[2] == "edit" {
			t.Fatalf("unexpected OnEnqueue label edit call: %#v", call)
		}
	}
}

func TestSmoke_S47_BudgetGateSkipLeavesSourceUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Claude:      config.ClaudeConfig{Command: "claude"},
		Sources: map[string]config.SourceConfig{
			"github": {
				Type: "github",
				Repo: "owner/repo",
				Tasks: map[string]config.Task{
					"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"},
				},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()
	r.set(issueJSON([]ghIssue{{
		Number: 1,
		Title:  "blocked by budget",
		Body:   "body",
		URL:    "https://github.com/owner/repo/issues/1",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}), "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	gate := &stubBudgetGate{decision: cost.Decision{Allowed: false, Reason: "stub exhausted", RemainingUSD: 0}}
	s := New(cfg, q, r)
	s.BudgetGate = gate

	result, err := s.Scan(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, result.Added)
	assert.Equal(t, 1, result.Skipped)
	require.Equal(t, []string{"fix-bug"}, gate.classes)

	vessels, err := q.List()
	require.NoError(t, err)
	assert.Empty(t, vessels)
	for _, call := range r.calls {
		assert.False(t, len(call) >= 3 && call[0] == "gh" && call[1] == "issue" && call[2] == "edit", "unexpected OnEnqueue label edit call: %#v", call)
	}

	gate.decision = cost.Decision{Allowed: true, RemainingUSD: 12.5}
	result, err = s.Scan(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Added)

	vessels, err = q.List()
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "github-issue", vessels[0].Source)
	assert.Equal(t, "fix-bug", vessels[0].Workflow)
}

type ghMergeCommitForScanner struct {
	OID string `json:"oid"`
}

type ghMergedPRForScanner struct {
	Number      int                     `json:"number"`
	Title       string                  `json:"title"`
	URL         string                  `json:"url"`
	MergeCommit ghMergeCommitForScanner `json:"mergeCommit"`
	HeadRefName string                  `json:"headRefName"`
}

func mergedPRJSON(prs []ghMergedPRForScanner) []byte {
	b, _ := json.Marshal(prs)
	return b
}

func TestScanMerge(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Claude:      config.ClaudeConfig{Command: "claude"},
		Sources: map[string]config.SourceConfig{
			"merge": {
				Type: "github-merge",
				Repo: "owner/repo",
				Tasks: map[string]config.Task{
					"deploy": {Workflow: "post-merge"},
				},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghMergedPRForScanner{
		{
			Number:      20,
			Title:       "merged PR",
			URL:         "https://github.com/owner/repo/pull/20",
			MergeCommit: ghMergeCommitForScanner{OID: "abcdef1234567890"},
			HeadRefName: "feature-x",
		},
	}
	r.set(mergedPRJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName", "--limit", "20")

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Added != 1 {
		t.Errorf("expected 1 added, got %d", result.Added)
	}
	vessels, _ := q.List()
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel in queue, got %d", len(vessels))
	}
	if vessels[0].Source != "github-merge" {
		t.Errorf("expected source github-merge, got %q", vessels[0].Source)
	}
	if vessels[0].Workflow != "post-merge" {
		t.Errorf("expected workflow post-merge, got %q", vessels[0].Workflow)
	}
}

func TestScanScheduledSourceHonorsCadence(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Claude:      config.ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]config.SourceConfig{
			"audit": {
				Type:     "scheduled",
				Repo:     "owner/repo",
				Schedule: "24h",
				Tasks: map[string]config.Task{
					"context": {Workflow: review.ContextWeightAuditWorkflow},
				},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	s := New(cfg, q, r)
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("first scan error: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("first scan added = %d, want 1", result.Added)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if vessels[0].Source != "scheduled" {
		t.Fatalf("vessel.Source = %q, want %q", vessels[0].Source, "scheduled")
	}
	if vessels[0].Workflow != review.ContextWeightAuditWorkflow {
		t.Fatalf("vessel.Workflow = %q, want %q", vessels[0].Workflow, review.ContextWeightAuditWorkflow)
	}
	if vessels[0].Meta["config_source"] != "audit" {
		t.Fatalf("config_source = %q, want %q", vessels[0].Meta["config_source"], "audit")
	}

	result, err = s.Scan(context.Background())
	if err != nil {
		t.Fatalf("second scan error: %v", err)
	}
	if result.Added != 0 {
		t.Fatalf("second scan added = %d, want 0", result.Added)
	}
}
