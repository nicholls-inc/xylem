package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/daemonhealth"
	"github.com/nicholls-inc/xylem/cli/internal/discussion"
	"github.com/nicholls-inc/xylem/cli/internal/notify"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
)

// daemonNotifier holds the state for periodic status reporting and escalation
// alerting within the daemon loop.
type daemonNotifier struct {
	multi    *notify.Multi
	rules    []notify.EscalationRule
	cfg      *config.Config
	q        *queue.Queue
	interval time.Duration

	lastReportAt time.Time
}

// defaultBotAllowedCommands is the safe read-only set used when bot_enabled is
// true but allowed_commands is not configured.
var defaultBotAllowedCommands = []string{
	"xylem status",
	"xylem queue list",
	"xylem doctor",
}

// newDaemonNotifier constructs the notifier from config. Returns nil if no
// notifications are configured. ctx is required to start the Telegram bot
// polling goroutine when bot_enabled is true; cmdRunner executes bot-dispatched
// commands.
func newDaemonNotifier(ctx context.Context, cfg *config.Config, q *queue.Queue, cmdRunner notify.CommandRunner) *daemonNotifier {
	ncfg := cfg.Notifications
	if !ncfg.GitHubDiscussion.Enabled && !ncfg.Telegram.Enabled {
		return nil
	}

	var notifiers []notify.Notifier

	if ncfg.GitHubDiscussion.Enabled {
		repoSlug := ncfg.GitHubDiscussion.Repo
		if repoSlug == "" {
			repoSlug = resolveDefaultRepo(cfg)
		}
		if repoSlug != "" {
			discCmdRunner := newCmdRunner(cfg)
			pub := &discussion.Publisher{Runner: discCmdRunner}
			disc, err := notify.NewDiscussion(pub, repoSlug,
				ncfg.GitHubDiscussion.Category,
				ncfg.GitHubDiscussion.Title)
			if err != nil {
				slog.Warn("daemon: failed to create discussion notifier", "error", err)
			} else {
				notifiers = append(notifiers, disc)
			}
		} else {
			slog.Warn("daemon: discussion notifications enabled but no repo configured")
		}
	}

	if ncfg.Telegram.Enabled {
		token := os.Getenv(ncfg.Telegram.TokenEnv)
		chatIDStr := os.Getenv(ncfg.Telegram.ChatIDEnv)
		if token != "" && chatIDStr != "" {
			tg := notify.NewTelegram(token, chatIDStr, ncfg.Telegram.Levels)
			notifiers = append(notifiers, tg)

			if ncfg.Telegram.BotEnabled {
				chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
				if err != nil {
					slog.Warn("daemon: telegram bot_enabled but chat_id is not a valid int64; bot disabled",
						"chat_id_env", ncfg.Telegram.ChatIDEnv,
						"error", err)
				} else {
					allowed := ncfg.Telegram.AllowedCommands
					if len(allowed) == 0 {
						allowed = defaultBotAllowedCommands
					}
					dispatcher := notify.NewBotDispatcher(tg, chatID, allowed, cmdRunner)
					tg.StartPolling(ctx, dispatcher.Dispatch)
					slog.Info("daemon: telegram bot polling started",
						"allowed_commands", len(allowed))
				}
			}
		} else {
			slog.Warn("daemon: telegram notifications enabled but token/chat_id env vars empty",
				"token_env", ncfg.Telegram.TokenEnv,
				"chat_id_env", ncfg.Telegram.ChatIDEnv)
		}
	}

	if len(notifiers) == 0 {
		return nil
	}

	interval := time.Hour
	if ncfg.GitHubDiscussion.Interval != "" {
		if d, err := time.ParseDuration(ncfg.GitHubDiscussion.Interval); err == nil && d > 0 {
			interval = d
		}
	}

	return &daemonNotifier{
		multi:    &notify.Multi{Notifiers: notifiers},
		rules:    notify.DefaultRules(),
		cfg:      cfg,
		q:        q,
		interval: interval,
	}
}

// tick is called from the daemon's tickHook on every loop iteration. It
// handles both periodic status reports and real-time escalation checks.
func (dn *daemonNotifier) tick(ctx context.Context, now time.Time, checks []daemonhealth.Check, upgradeOverdue bool, upgradeOverdueBy time.Duration) {
	// Escalation check: runs every tick (~30s).
	dn.checkEscalations(ctx, now, checks, upgradeOverdue, upgradeOverdueBy)

	// Status report: runs at configured interval (default 1h).
	if now.Sub(dn.lastReportAt) >= dn.interval {
		dn.postReport(ctx, now)
		dn.lastReportAt = now
	}
}

func (dn *daemonNotifier) checkEscalations(ctx context.Context, now time.Time, checks []daemonhealth.Check, upgradeOverdue bool, upgradeOverdueBy time.Duration) {
	vessels, err := dn.q.List()
	if err != nil {
		return
	}

	state := buildFleetState(vessels, checks, now, upgradeOverdue, upgradeOverdueBy)
	alerts := notify.Evaluate(dn.rules, state)
	for _, alert := range alerts {
		if err := dn.multi.SendAlert(ctx, alert); err != nil {
			slog.Warn("daemon: escalation alert failed", "code", alert.Code, "error", err)
		}
	}
}

func (dn *daemonNotifier) postReport(ctx context.Context, now time.Time) {
	report := reporter.CollectStatus(ctx, reporter.StatusDeps{
		StateDir:      dn.cfg.StateDir,
		Queue:         dn.q,
		FleetAnalyzer: fleetAnalyzerFromRunner,
	}, now)

	md := reporter.FormatMarkdown(report)
	if err := dn.multi.PostStatus(ctx, notify.StatusReport{Markdown: md}); err != nil {
		slog.Warn("daemon: status report post failed", "error", err)
	} else {
		slog.Info("daemon: status report posted")
	}
}

// buildFleetState constructs the notify.FleetState from current queue state.
func buildFleetState(vessels []queue.Vessel, checks []daemonhealth.Check, now time.Time, upgradeOverdue bool, upgradeOverdueBy time.Duration) notify.FleetState {
	state := notify.FleetState{
		UpgradeOverdue:   upgradeOverdue,
		UpgradeOverdueBy: upgradeOverdueBy,
	}

	cutoff := now.Add(-time.Hour)
	var oldestPendingCreated time.Time

	for _, v := range vessels {
		switch v.State {
		case queue.StatePending:
			state.Pending++
			if oldestPendingCreated.IsZero() || v.CreatedAt.Before(oldestPendingCreated) {
				oldestPendingCreated = v.CreatedAt
			}
		case queue.StateRunning:
			state.Running++
		case queue.StateCompleted:
			state.Completed++
			if v.EndedAt != nil && v.EndedAt.After(cutoff) {
				state.RecentCompletions++
			}
		case queue.StateFailed:
			state.Failed++
			if v.EndedAt != nil && v.EndedAt.After(cutoff) {
				state.RecentFailures = append(state.RecentFailures, notify.VesselFailure{
					VesselID:  v.ID,
					Error:     v.Error,
					Timestamp: *v.EndedAt,
				})
			}
		case queue.StateTimedOut:
			state.TimedOut++
			if v.EndedAt != nil && v.EndedAt.After(cutoff) {
				state.RecentFailures = append(state.RecentFailures, notify.VesselFailure{
					VesselID:  v.ID,
					Error:     v.Error,
					Timestamp: *v.EndedAt,
				})
			}
		case queue.StateWaiting:
			state.Waiting++
		case queue.StateCancelled:
			state.Cancelled++
		}
	}

	if !oldestPendingCreated.IsZero() {
		state.OldestPendingAge = now.Sub(oldestPendingCreated)
	}

	for _, c := range checks {
		state.HealthChecks = append(state.HealthChecks, notify.HealthCheck{
			Code:    c.Code,
			Level:   string(c.Level),
			Message: c.Message,
		})
	}

	return state
}
