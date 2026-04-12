package reporter

import (
	"fmt"
	"strings"
	"time"
)

// FormatMarkdown renders the report as GitHub-flavored Markdown suitable for
// posting to a Discussion or comment body.
func FormatMarkdown(r StatusReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Xylem Status -- %s\n\n", r.Timestamp.UTC().Format("2006-01-02 15:04 UTC"))

	// Daemon section.
	b.WriteString("### Daemon\n")
	b.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| PID | %d |\n", r.Daemon.PID)
	fmt.Fprintf(&b, "| Uptime | %s |\n", formatStatusDuration(r.Daemon.Uptime))
	if r.Daemon.Binary != "" {
		fmt.Fprintf(&b, "| Binary | `%s` |\n", r.Daemon.Binary)
	}
	if !r.Daemon.LastUpgradeAt.IsZero() {
		fmt.Fprintf(&b, "| Last upgrade | %s |\n", r.Daemon.LastUpgradeAt.UTC().Format("15:04 UTC"))
	}
	b.WriteString("\n")

	// Vessel metrics.
	b.WriteString("### Vessels\n")
	b.WriteString("| State | Count |\n|-------|-------|\n")
	fmt.Fprintf(&b, "| Pending | %d |\n", r.Vessels.Pending)
	fmt.Fprintf(&b, "| Running | %d |\n", r.Vessels.Running)
	fmt.Fprintf(&b, "| Completed | %d |\n", r.Vessels.Completed)
	fmt.Fprintf(&b, "| Failed | %d |\n", r.Vessels.Failed)
	fmt.Fprintf(&b, "| Timed Out | %d |\n", r.Vessels.TimedOut)
	fmt.Fprintf(&b, "| Waiting | %d |\n", r.Vessels.Waiting)
	fmt.Fprintf(&b, "| Cancelled | %d |\n", r.Vessels.Cancelled)
	b.WriteString("\n")

	// Active vessels.
	if len(r.ActiveVessels) > 0 {
		b.WriteString("### Active Vessels\n")
		b.WriteString("| Vessel | Phase | Duration | Workflow |\n")
		b.WriteString("|--------|-------|----------|----------|\n")
		for _, av := range r.ActiveVessels {
			phase := av.Phase
			if phase == "" {
				phase = "-"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
				av.ID, phase, formatStatusDuration(av.Duration), av.Workflow)
		}
		b.WriteString("\n")
	}

	// Fleet health.
	total := r.Fleet.Healthy + r.Fleet.Degraded + r.Fleet.Unhealthy
	if total > 0 {
		b.WriteString("### Fleet Health\n")
		healthPct := float64(r.Fleet.Healthy) / float64(total) * 100
		fmt.Fprintf(&b, "- %.0f%% healthy (%d/%d vessels)\n", healthPct, r.Fleet.Healthy, total)
		if len(r.Fleet.Patterns) > 0 {
			fmt.Fprintf(&b, "- Patterns: %s\n", formatFleetPatterns(r.Fleet.Patterns))
		}
		b.WriteString("\n")
	}

	// Recent failures.
	if len(r.RecentFailures) > 0 {
		b.WriteString("### Recent Failures (24h)\n")
		b.WriteString("| Vessel | Phase | Error |\n|--------|-------|-------|\n")
		limit := len(r.RecentFailures)
		if limit > 10 {
			limit = 10
		}
		for _, f := range r.RecentFailures[:limit] {
			errMsg := f.Error
			if len(errMsg) > 80 {
				errMsg = errMsg[:80] + "..."
			}
			phase := f.Phase
			if phase == "" {
				phase = "-"
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", f.ID, phase, errMsg)
		}
		if len(r.RecentFailures) > 10 {
			fmt.Fprintf(&b, "\n*+%d more failures not shown*\n", len(r.RecentFailures)-10)
		}
		b.WriteString("\n")
	}

	// Warnings.
	if len(r.Warnings) > 0 {
		b.WriteString("### Warnings\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}

	return b.String()
}

// FormatPlainText renders the report for terminal output.
func FormatPlainText(r StatusReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Xylem Status -- %s\n\n", r.Timestamp.UTC().Format("2006-01-02 15:04 UTC"))

	fmt.Fprintf(&b, "Daemon: PID %d | Uptime %s | Binary %s\n",
		r.Daemon.PID, formatStatusDuration(r.Daemon.Uptime), r.Daemon.Binary)
	if !r.Daemon.LastUpgradeAt.IsZero() {
		fmt.Fprintf(&b, "  Last upgrade: %s\n", r.Daemon.LastUpgradeAt.UTC().Format("15:04 UTC"))
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "Vessels: %d pending, %d running, %d completed, %d failed, %d timed_out, %d waiting, %d cancelled\n",
		r.Vessels.Pending, r.Vessels.Running, r.Vessels.Completed,
		r.Vessels.Failed, r.Vessels.TimedOut, r.Vessels.Waiting, r.Vessels.Cancelled)
	b.WriteString("\n")

	if len(r.ActiveVessels) > 0 {
		b.WriteString("Active:\n")
		for _, av := range r.ActiveVessels {
			phase := av.Phase
			if phase == "" {
				phase = "-"
			}
			fmt.Fprintf(&b, "  %-20s %s (%s) [%s]\n",
				av.ID, phase, formatStatusDuration(av.Duration), av.Workflow)
		}
		b.WriteString("\n")
	}

	total := r.Fleet.Healthy + r.Fleet.Degraded + r.Fleet.Unhealthy
	if total > 0 {
		fmt.Fprintf(&b, "Fleet: %d healthy, %d degraded, %d unhealthy\n",
			r.Fleet.Healthy, r.Fleet.Degraded, r.Fleet.Unhealthy)
		if len(r.Fleet.Patterns) > 0 {
			fmt.Fprintf(&b, "  Patterns: %s\n", formatFleetPatterns(r.Fleet.Patterns))
		}
		b.WriteString("\n")
	}

	if len(r.Warnings) > 0 {
		b.WriteString("Warnings:\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "  - %s\n", w)
		}
	}

	return b.String()
}

// formatStatusDuration renders a duration as a compact human-readable string.
// Named with "Status" prefix to avoid collision with any existing
// formatDuration in the package.
func formatStatusDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}
