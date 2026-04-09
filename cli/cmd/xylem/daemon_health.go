package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
)

const daemonHealthFileName = "daemon-health.json"

type daemonStatusAlert struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type daemonStatusSnapshot struct {
	PID           int                 `json:"pid"`
	StartedAt     time.Time           `json:"started_at"`
	HeartbeatAt   time.Time           `json:"heartbeat_at"`
	Build         string              `json:"build,omitempty"`
	AutoUpgrade   bool                `json:"auto_upgrade"`
	LastUpgradeAt *time.Time          `json:"last_upgrade_at,omitempty"`
	Alerts        []daemonStatusAlert `json:"alerts,omitempty"`
}

type daemonHealthRecorder struct {
	cfg      *config.Config
	snapshot daemonStatusSnapshot
}

type daemonBacklogMonitor struct {
	cfg        *config.Config
	cmdRunner  source.CommandRunner
	idleSince  time.Time
	lastAlerts []daemonStatusAlert
}

type ghIssueBacklogItem struct {
	Number int `json:"number"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type ghPRBacklogItem struct {
	Number    int    `json:"number"`
	Mergeable string `json:"mergeable"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func newDaemonHealthRecorder(cfg *config.Config, startedAt time.Time, autoUpgradeActive bool) *daemonHealthRecorder {
	return &daemonHealthRecorder{
		cfg: cfg,
		snapshot: daemonStatusSnapshot{
			PID:         os.Getpid(),
			StartedAt:   startedAt.UTC(),
			HeartbeatAt: startedAt.UTC(),
			Build:       buildInfo(),
			AutoUpgrade: autoUpgradeActive,
		},
	}
}

func (r *daemonHealthRecorder) Update(now time.Time, alerts []daemonStatusAlert, lastUpgrade time.Time) {
	if r == nil || r.cfg == nil {
		return
	}
	r.snapshot.HeartbeatAt = now.UTC()
	r.snapshot.Alerts = cloneDaemonAlerts(alerts)
	if r.snapshot.AutoUpgrade && !lastUpgrade.IsZero() {
		upgradeAt := lastUpgrade.UTC()
		r.snapshot.LastUpgradeAt = &upgradeAt
	}
	if err := saveDaemonStatusSnapshot(daemonHealthPath(r.cfg), r.snapshot); err != nil {
		slog.Warn("persist daemon health snapshot", "error", err)
	}
}

func newDaemonBacklogMonitor(cfg *config.Config, cmdRunner source.CommandRunner) *daemonBacklogMonitor {
	return &daemonBacklogMonitor{cfg: cfg, cmdRunner: cmdRunner}
}

func (m *daemonBacklogMonitor) ObserveScan(ctx context.Context, now time.Time, result scanner.ScanResult, tracker inFlightTracker, q *queue.Queue) []daemonStatusAlert {
	if m == nil {
		return nil
	}
	if result.Added > 0 || !daemonQueueIdle(q, tracker) {
		m.idleSince = time.Time{}
		m.lastAlerts = nil
		return nil
	}

	threshold, err := time.ParseDuration(m.cfg.Daemon.StallMonitor.ScannerIdleThreshold)
	if err != nil {
		slog.Warn("parse scanner idle threshold", "error", err)
		return nil
	}
	if m.idleSince.IsZero() {
		m.idleSince = now.UTC()
		m.lastAlerts = nil
		return nil
	}
	if now.Sub(m.idleSince) < threshold {
		m.lastAlerts = nil
		return nil
	}

	backlogCount, err := countEligibleGitHubBacklog(ctx, m.cfg, m.cmdRunner)
	if err != nil {
		slog.Warn("query daemon backlog", "error", err)
		m.lastAlerts = nil
		return nil
	}
	if backlogCount <= 0 {
		m.lastAlerts = nil
		return nil
	}

	m.lastAlerts = []daemonStatusAlert{{
		Code:     "idle_with_backlog",
		Severity: "warning",
		Message:  fmt.Sprintf("Daemon idle with %d backlog items on GitHub", backlogCount),
	}}
	slog.Warn("daemon idle with backlog", "count", backlogCount)
	return cloneDaemonAlerts(m.lastAlerts)
}

func (m *daemonBacklogMonitor) CurrentAlerts(tracker inFlightTracker, q *queue.Queue) []daemonStatusAlert {
	if m == nil {
		return nil
	}
	if !daemonQueueIdle(q, tracker) {
		m.lastAlerts = nil
		return nil
	}
	return cloneDaemonAlerts(m.lastAlerts)
}

func countEligibleGitHubBacklog(ctx context.Context, cfg *config.Config, cmdRunner source.CommandRunner) (int, error) {
	if cfg == nil || cmdRunner == nil {
		return 0, nil
	}

	seenIssues := map[string]struct{}{}
	seenPRs := map[string]struct{}{}
	for _, srcCfg := range cfg.Sources {
		switch srcCfg.Type {
		case "github":
			excluded := make(map[string]struct{}, len(srcCfg.Exclude))
			for _, label := range srcCfg.Exclude {
				excluded[label] = struct{}{}
			}
			for _, task := range srcCfg.Tasks {
				for _, label := range task.Labels {
					out, err := cmdRunner.Run(ctx, "gh", "search", "issues", "--repo", srcCfg.Repo, "--state", "open", "--json", "number,labels", "--limit", "100", "--label", label)
					if err != nil {
						return 0, fmt.Errorf("gh search issues: %w", err)
					}
					var issues []ghIssueBacklogItem
					if err := json.Unmarshal(out, &issues); err != nil {
						return 0, fmt.Errorf("parse gh search output: %w", err)
					}
					for _, issue := range issues {
						if hasExcludedBacklogLabel(issue.Labels, excluded) {
							continue
						}
						seenIssues[fmt.Sprintf("%s#%d", srcCfg.Repo, issue.Number)] = struct{}{}
					}
				}
			}
		case "github-pr":
			excluded := make(map[string]struct{}, len(srcCfg.Exclude))
			for _, label := range srcCfg.Exclude {
				excluded[label] = struct{}{}
			}
			for _, task := range srcCfg.Tasks {
				if task.Workflow != "resolve-conflicts" {
					continue
				}
				for _, label := range task.Labels {
					out, err := cmdRunner.Run(ctx, "gh", "pr", "list", "--repo", srcCfg.Repo, "--state", "open", "--label", label, "--json", "number,labels,mergeable", "--limit", "100")
					if err != nil {
						return 0, fmt.Errorf("gh pr list: %w", err)
					}
					var prs []ghPRBacklogItem
					if err := json.Unmarshal(out, &prs); err != nil {
						return 0, fmt.Errorf("parse gh pr list output: %w", err)
					}
					for _, pr := range prs {
						if pr.Mergeable != "CONFLICTING" || hasExcludedBacklogLabel(pr.Labels, excluded) {
							continue
						}
						seenPRs[fmt.Sprintf("%s#%d", srcCfg.Repo, pr.Number)] = struct{}{}
					}
				}
			}
		}
	}

	return len(seenIssues) + len(seenPRs), nil
}

func hasExcludedBacklogLabel(labels []struct {
	Name string `json:"name"`
}, excluded map[string]struct{}) bool {
	for _, label := range labels {
		if _, ok := excluded[label.Name]; ok {
			return true
		}
	}
	return false
}

func daemonQueueIdle(q *queue.Queue, tracker inFlightTracker) bool {
	if trackerInFlightCount(tracker) > 0 {
		return false
	}
	if q == nil {
		return true
	}
	vessels, err := q.List()
	if err != nil {
		return false
	}
	for _, vessel := range vessels {
		switch vessel.State {
		case queue.StatePending, queue.StateRunning, queue.StateWaiting:
			return false
		}
	}
	return true
}

func daemonHealthPath(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir, daemonHealthFileName)
}

func saveDaemonStatusSnapshot(path string, snapshot daemonStatusSnapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create daemon health dir: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal daemon health snapshot: %w", err)
	}
	tmp, err := os.CreateTemp(dir, daemonHealthFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp daemon health snapshot: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp daemon health snapshot: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp daemon health snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp daemon health snapshot: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp daemon health snapshot: %w", err)
	}
	return nil
}

func loadDaemonStatusSnapshot(path string) (*daemonStatusSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snapshot daemonStatusSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("parse daemon health snapshot: %w", err)
	}
	return &snapshot, nil
}

func daemonHeartbeatFreshness(cfg *config.Config) time.Duration {
	scanInterval, drainInterval := parseDaemonIntervals(cfg.Daemon)
	freshness := scanInterval
	if drainInterval > freshness {
		freshness = drainInterval
	}
	return freshness*2 + 5*time.Second
}

func cloneDaemonAlerts(alerts []daemonStatusAlert) []daemonStatusAlert {
	if len(alerts) == 0 {
		return nil
	}
	out := make([]daemonStatusAlert, len(alerts))
	copy(out, alerts)
	return out
}
