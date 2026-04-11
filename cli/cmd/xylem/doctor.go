package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/daemonhealth"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
)

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose queue, daemon, and vessel health issues",
		Long: `Run deterministic health checks against the xylem control plane.

Checks performed:
  - Daemon liveness (PID file + process signal)
  - Zombie vessels (running state with no live daemon)
  - Stale worktrees (registered but associated with terminal vessels)
  - Queue integrity (malformed entries, duplicate IDs)
  - Fleet health summary (failure rate, timeout rate)

Use --fix to automatically remediate safe issues (e.g., reap zombie vessels).
Use --json for machine-readable output.
Use --root to inspect another checkout or daemon root.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fix, _ := cmd.Flags().GetBool("fix")
			jsonMode, _ := cmd.Flags().GetBool("json")
			root, _ := cmd.Flags().GetString("root")
			scopedDeps, err := doctorDepsForRoot(deps, root)
			if err != nil {
				return err
			}
			return cmdDoctor(scopedDeps.cfg, scopedDeps.q, scopedDeps.wt, fix, jsonMode)
		},
	}
	cmd.Flags().Bool("fix", false, "Automatically remediate safe issues")
	cmd.Flags().Bool("json", false, "Output as JSON")
	cmd.Flags().String("root", "", "Run checks against the xylem state rooted at this directory")
	return cmd
}

func doctorDepsForRoot(base *appDeps, root string) (*appDeps, error) {
	if base == nil || base.cfg == nil {
		return nil, fmt.Errorf("doctor dependencies not initialized")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return base, nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve doctor root %q: %w", root, err)
	}
	cfg := *base.cfg
	cfg.StateDir = config.ResolveStateDir(absRoot, cfg.StateDir)
	wt := worktree.New(absRoot, &realCmdRunner{})
	wt.DefaultBranch = cfg.DefaultBranch
	return &appDeps{
		cfg: &cfg,
		q:   queue.New(config.RuntimePath(cfg.StateDir, "queue.jsonl")),
		wt:  wt,
	}, nil
}

// doctorCheck represents a single diagnostic finding.
type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "ok", "warn", "fail"
	Message string `json:"message"`
	Fixed   bool   `json:"fixed,omitempty"`
}

// doctorReport aggregates all diagnostic checks.
type doctorReport struct {
	Checks  []doctorCheck `json:"checks"`
	Summary struct {
		OK   int `json:"ok"`
		Warn int `json:"warn"`
		Fail int `json:"fail"`
	} `json:"summary"`
}

func (r *doctorReport) add(name, status, message string) {
	r.Checks = append(r.Checks, doctorCheck{
		Name:    name,
		Status:  status,
		Message: message,
	})
	switch status {
	case "ok":
		r.Summary.OK++
	case "warn":
		r.Summary.Warn++
	case "fail":
		r.Summary.Fail++
	}
}

func (r *doctorReport) addFixed(name, message string) {
	r.Checks = append(r.Checks, doctorCheck{
		Name:    name,
		Status:  "ok",
		Message: message,
		Fixed:   true,
	})
	r.Summary.OK++
}

func cmdDoctor(cfg *config.Config, q *queue.Queue, wt *worktree.Manager, fix, jsonMode bool) error {
	report := &doctorReport{}

	checkDaemonLiveness(cfg, report)
	daemonAlive := isDaemonAlive(cfg)
	checkZombieVessels(cfg, q, wt, report, fix, daemonAlive)
	checkStaleWorktrees(wt, q, report, fix)
	checkQueueHealth(q, report)
	checkFleetHealth(cfg, q, report)
	checkConfig(cfg, report)

	if jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	renderDoctorReport(report)
	return nil
}

func checkDaemonLiveness(cfg *config.Config, report *doctorReport) {
	snapshot, err := daemonhealth.Load(cfg.StateDir)
	if err != nil {
		report.add("daemon", "warn", "No daemon health snapshot found (daemon may have never run)")
		return
	}

	if daemonProcessAlive(snapshot.PID) {
		uptime := time.Since(snapshot.StartedAt).Round(time.Second)
		report.add("daemon", "ok", fmt.Sprintf("Daemon alive (pid=%d, uptime=%s, binary=%s)", snapshot.PID, uptime, snapshot.Binary))

		// Check for stale heartbeat even if process is alive
		heartbeatAge := time.Since(snapshot.UpdatedAt)
		if heartbeatAge > 5*time.Minute {
			report.add("daemon_heartbeat", "warn", fmt.Sprintf("Daemon heartbeat stale (%s ago)", heartbeatAge.Round(time.Second)))
		}
	} else {
		lastSeen := snapshot.UpdatedAt.UTC().Format("2006-01-02 15:04:05 UTC")
		report.add("daemon", "fail", fmt.Sprintf("Daemon not running (pid=%d last seen %s)", snapshot.PID, lastSeen))
	}

	// Report daemon health checks
	for _, check := range snapshot.Checks {
		status := "ok"
		switch check.Level {
		case daemonhealth.LevelWarning:
			status = "warn"
		case daemonhealth.LevelCritical:
			status = "fail"
		}
		report.add("daemon_check_"+check.Code, status, check.Message)
	}
}

func isDaemonAlive(cfg *config.Config) bool {
	snapshot, err := daemonhealth.Load(cfg.StateDir)
	if err != nil {
		return false
	}
	return daemonProcessAlive(snapshot.PID)
}

func checkZombieVessels(cfg *config.Config, q *queue.Queue, wt *worktree.Manager, report *doctorReport, fix, daemonAlive bool) {
	running, err := q.ListByState(queue.StateRunning)
	if err != nil {
		report.add("zombie_vessels", "warn", fmt.Sprintf("Failed to list running vessels: %v", err))
		return
	}

	if len(running) == 0 {
		report.add("zombie_vessels", "ok", "No running vessels")
		return
	}

	timeout, _ := time.ParseDuration(cfg.Timeout)
	if timeout == 0 {
		timeout = 45 * time.Minute // fallback default
	}

	var zombies []queue.Vessel
	for _, v := range running {
		if v.StartedAt == nil {
			zombies = append(zombies, v)
			continue
		}
		elapsed := time.Since(*v.StartedAt)
		// A vessel is a zombie if:
		// 1. The daemon is dead and the vessel is running, OR
		// 2. The vessel has been running for > 2x the timeout
		if !daemonAlive || elapsed > 2*timeout {
			zombies = append(zombies, v)
		}
	}

	if len(zombies) == 0 {
		report.add("zombie_vessels", "ok", fmt.Sprintf("%d running vessel(s), none are zombies", len(running)))
		return
	}

	if !fix {
		ids := make([]string, len(zombies))
		for i, v := range zombies {
			elapsed := "unknown"
			if v.StartedAt != nil {
				elapsed = time.Since(*v.StartedAt).Round(time.Second).String()
			}
			ids[i] = fmt.Sprintf("%s (running %s)", v.ID, elapsed)
		}
		report.add("zombie_vessels", "fail", fmt.Sprintf("%d zombie vessel(s) found: %s. Run with --fix to reap", len(zombies), strings.Join(ids, ", ")))
		return
	}

	// Fix: transition zombies to timed_out
	reaped := 0
	for _, v := range zombies {
		errMsg := "reaped by xylem doctor --fix (orphaned vessel)"
		if err := q.Update(v.ID, queue.StateTimedOut, errMsg); err != nil {
			report.add("zombie_reap_"+v.ID, "warn", fmt.Sprintf("Failed to reap %s: %v", v.ID, err))
			continue
		}
		reaped++

		// Best-effort worktree cleanup — only remove vessel worktrees
		// under .claude/worktrees/ to avoid destroying the daemon root
		// or other important worktrees.
		if v.WorktreePath != "" && wt != nil && strings.Contains(v.WorktreePath, ".claude/worktrees/") {
			_ = wt.Remove(context.Background(), v.WorktreePath)
		}
	}
	report.addFixed("zombie_vessels", fmt.Sprintf("Reaped %d/%d zombie vessel(s)", reaped, len(zombies)))
}

func checkStaleWorktrees(wt *worktree.Manager, q *queue.Queue, report *doctorReport, fix bool) {
	ctx := context.Background()
	trees, err := wt.ListXylem(ctx)
	if err != nil {
		report.add("worktrees", "warn", fmt.Sprintf("Failed to list worktrees: %v", err))
		return
	}

	if len(trees) == 0 {
		report.add("worktrees", "ok", "No xylem worktrees")
		return
	}
	pruner := runner.New(nil, q, wt, nil)
	stale, err := pruner.FindStaleWorktrees(ctx)
	if err != nil {
		report.add("worktrees", "warn", fmt.Sprintf("Failed to identify stale worktrees: %v", err))
		return
	}

	if len(stale) == 0 {
		report.add("worktrees", "ok", fmt.Sprintf("%d worktree(s), all active", len(trees)))
		return
	}

	if !fix {
		report.add("worktrees", "warn", fmt.Sprintf("%d stale worktree(s) of %d total. Run xylem cleanup or doctor --fix to remove", len(stale), len(trees)))
		return
	}

	removed := 0
	for _, t := range stale {
		if err := wt.Remove(ctx, t.Path); err != nil {
			continue
		}
		removed++
	}
	report.addFixed("worktrees", fmt.Sprintf("Removed %d/%d stale worktree(s)", removed, len(stale)))
}

func checkQueueHealth(q *queue.Queue, report *doctorReport) {
	vessels, err := q.List()
	if err != nil {
		report.add("queue", "fail", fmt.Sprintf("Failed to read queue: %v", err))
		return
	}

	// Check for duplicate IDs (non-terminal)
	seen := make(map[string]int)
	for _, v := range vessels {
		if !v.State.IsTerminal() {
			seen[v.ID]++
		}
	}
	var dupes []string
	for id, count := range seen {
		if count > 1 {
			dupes = append(dupes, fmt.Sprintf("%s (x%d)", id, count))
		}
	}
	if len(dupes) > 0 {
		report.add("queue_duplicates", "warn", fmt.Sprintf("Duplicate active vessel IDs: %s", strings.Join(dupes, ", ")))
	}

	// Check queue compaction potential
	compactable, err := q.CompactDryRun()
	if err == nil && compactable > 0 {
		report.add("queue_compaction", "warn", fmt.Sprintf("%d stale queue records could be compacted. Run xylem cleanup", compactable))
	} else {
		report.add("queue_compaction", "ok", "Queue is compact")
	}

	report.add("queue", "ok", fmt.Sprintf("%d vessel(s) in queue", len(vessels)))
}

func checkFleetHealth(cfg *config.Config, q *queue.Queue, report *doctorReport) {
	vessels, err := q.List()
	if err != nil {
		return
	}

	ids := make([]string, len(vessels))
	for i, v := range vessels {
		ids[i] = v.ID
	}
	summaries, _ := runner.LoadVesselSummaries(cfg.StateDir, ids)
	fleet := runner.AnalyzeFleetStatus(vessels, summaries)

	total := fleet.Healthy + fleet.Degraded + fleet.Unhealthy
	if total == 0 {
		return
	}

	failRate := float64(fleet.Unhealthy) / float64(total) * 100
	if failRate > 40 {
		report.add("fleet_health", "fail", fmt.Sprintf("%.0f%% unhealthy (%d/%d vessels)", failRate, fleet.Unhealthy, total))
	} else if failRate > 20 {
		report.add("fleet_health", "warn", fmt.Sprintf("%.0f%% unhealthy (%d/%d vessels)", failRate, fleet.Unhealthy, total))
	} else {
		report.add("fleet_health", "ok", fmt.Sprintf("%.0f%% healthy (%d/%d vessels)", float64(fleet.Healthy)/float64(total)*100, fleet.Healthy, total))
	}

	// Check for dominant failure patterns
	for _, p := range fleet.Patterns {
		if p.Count >= 5 {
			pct := float64(p.Count) / float64(total) * 100
			report.add("fleet_pattern_"+p.Code, "warn", fmt.Sprintf("Recurring pattern: %s (%d vessels, %.0f%%)", p.Code, p.Count, pct))
		}
	}

	// Check pending backlog
	counts := map[queue.VesselState]int{}
	for _, v := range vessels {
		counts[v.State]++
	}
	if counts[queue.StatePending] > 10 {
		report.add("pending_backlog", "warn", fmt.Sprintf("%d pending vessels in backlog", counts[queue.StatePending]))
	}
}

func checkConfig(cfg *config.Config, report *doctorReport) {
	// Check timeout is parseable
	if cfg.Timeout != "" {
		if _, err := time.ParseDuration(cfg.Timeout); err != nil {
			report.add("config_timeout", "fail", fmt.Sprintf("Invalid timeout %q: %v", cfg.Timeout, err))
		}
	}

	// Check concurrency
	if cfg.Concurrency <= 0 {
		report.add("config_concurrency", "warn", "Concurrency not set or zero (defaults to 1)")
	}

	// Check stall monitor
	if cfg.Daemon.StallMonitor.PhaseStallThreshold == "" {
		report.add("config_stall_monitor", "warn", "Phase stall threshold not configured (stall detection disabled)")
	}

	// Check auto-upgrade
	if !cfg.Daemon.AutoUpgrade {
		report.add("config_auto_upgrade", "warn", "Daemon auto-upgrade is disabled")
	}
}

func renderDoctorReport(report *doctorReport) {
	for _, check := range report.Checks {
		icon := checkIcon(check.Status)
		suffix := ""
		if check.Fixed {
			suffix = " [FIXED]"
		}
		fmt.Printf("  %s %s%s\n", icon, check.Message, suffix)
	}

	fmt.Printf("\n%d ok, %d warnings, %d failures\n", report.Summary.OK, report.Summary.Warn, report.Summary.Fail)

	if report.Summary.Fail > 0 {
		fmt.Println("\nRun with --fix to attempt automatic remediation of fixable issues.")
	}
}

func checkIcon(status string) string {
	switch status {
	case "warn":
		return "!"
	case "fail":
		return "X"
	default:
		return "."
	}
}
