package cadence

import (
	"fmt"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
)

var cronParser = cron.NewParser(
	cron.Minute |
		cron.Hour |
		cron.Dom |
		cron.Month |
		cron.Dow |
		cron.Descriptor,
)

// Spec represents either a fixed interval or a cron-style schedule.
type Spec struct {
	raw      string
	interval time.Duration
	cron     cron.Schedule
}

func Parse(raw string) (Spec, error) {
	expr := strings.TrimSpace(raw)
	if expr == "" {
		return Spec{}, fmt.Errorf("cadence is required")
	}
	if d, err := time.ParseDuration(expr); err == nil {
		if d <= 0 {
			return Spec{}, fmt.Errorf("cadence duration must be greater than 0")
		}
		return Spec{raw: expr, interval: d}, nil
	}
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return Spec{}, fmt.Errorf("parse cadence %q: %w", expr, err)
	}
	return Spec{raw: expr, cron: sched}, nil
}

func (s Spec) Raw() string {
	return s.raw
}

func (s Spec) FireTime(last *time.Time, now time.Time) (time.Time, bool) {
	now = now.UTC().Truncate(time.Second)
	if last == nil {
		return now, true
	}

	base := last.UTC().Truncate(time.Second)
	if s.interval > 0 {
		next := base.Add(s.interval)
		if !next.After(now) {
			return next, true
		}
		return time.Time{}, false
	}

	next := s.cron.Next(base)
	if !next.After(now) {
		return next.UTC().Truncate(time.Second), true
	}
	return time.Time{}, false
}
