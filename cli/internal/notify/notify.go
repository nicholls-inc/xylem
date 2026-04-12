package notify

import (
	"context"
	"log/slog"
	"time"
)

// AlertSeverity indicates how urgent an alert is.
type AlertSeverity string

const (
	SeverityCritical AlertSeverity = "critical"
	SeverityWarning  AlertSeverity = "warning"
)

// Alert represents an escalation event requiring operator attention.
type Alert struct {
	Severity  AlertSeverity
	Code      string // machine-readable: "auth_failure", "high_failure_rate", etc.
	Title     string // short heading
	Detail    string // longer description
	Timestamp time.Time
	VesselIDs []string // affected vessels, if any
}

// StatusReport is passed by the reporter package (defined here as an interface
// to avoid circular imports -- reporter imports notify, not the other way).
type StatusReport struct {
	Markdown string // pre-rendered Markdown for Discussion
}

// Notifier sends notifications to a single channel.
type Notifier interface {
	PostStatus(ctx context.Context, report StatusReport) error
	SendAlert(ctx context.Context, alert Alert) error
	Name() string
}

// Multi dispatches to multiple notifiers. Errors are logged, not propagated --
// a failure in one channel must not block others.
type Multi struct {
	Notifiers []Notifier
}

func (m *Multi) PostStatus(ctx context.Context, report StatusReport) error {
	for _, n := range m.Notifiers {
		if err := n.PostStatus(ctx, report); err != nil {
			slog.Warn("notify: status post failed", "notifier", n.Name(), "error", err)
		}
	}
	return nil
}

func (m *Multi) SendAlert(ctx context.Context, alert Alert) error {
	for _, n := range m.Notifiers {
		if err := n.SendAlert(ctx, alert); err != nil {
			slog.Warn("notify: alert send failed", "notifier", n.Name(), "error", err)
		}
	}
	return nil
}
