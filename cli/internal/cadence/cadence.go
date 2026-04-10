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

// Bucket returns a stable bucket identifier and the enclosing schedule window
// for a given time. Interval specs use the historical integer bucket index so
// persisted scheduled-source state remains compatible. Cron specs use the slot
// start timestamp as the bucket key.
func (s Spec) Bucket(now time.Time) (int64, time.Time, time.Time, error) {
	now = now.UTC()
	if s.interval > 0 {
		size := s.interval.Nanoseconds()
		bucket := now.UnixNano() / size
		startUnix := bucket * size
		start := time.Unix(0, startUnix).UTC()
		return bucket, start, start.Add(s.interval), nil
	}
	if s.cron == nil {
		return 0, time.Time{}, time.Time{}, fmt.Errorf("cadence is not initialized")
	}
	start, end, err := currentCronWindow(s.cron, now)
	if err != nil {
		return 0, time.Time{}, time.Time{}, err
	}
	return start.Unix(), start, end, nil
}

func currentCronWindow(schedule cron.Schedule, now time.Time) (time.Time, time.Time, error) {
	now = now.UTC().Truncate(time.Minute)
	if now.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("current time is required")
	}

	if cronFiresAt(schedule, now) {
		end := schedule.Next(now).UTC().Truncate(time.Minute)
		return now, end, nil
	}

	end := schedule.Next(now).UTC().Truncate(time.Minute)
	start, err := previousCronOccurrence(schedule, end)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, end, nil
}

func cronFiresAt(schedule cron.Schedule, ts time.Time) bool {
	ts = ts.UTC().Truncate(time.Minute)
	if ts.Second() != 0 || ts.Nanosecond() != 0 {
		return false
	}
	return schedule.Next(ts.Add(-time.Minute)).UTC().Truncate(time.Minute).Equal(ts)
}

func previousCronOccurrence(schedule cron.Schedule, next time.Time) (time.Time, error) {
	next = next.UTC().Truncate(time.Minute)
	upper := next.Add(-time.Minute)
	if upper.IsZero() {
		return time.Time{}, fmt.Errorf("next occurrence is required")
	}

	lower := upper
	step := time.Minute
	const maxLookback = 20 * 366 * 24 * time.Hour
	for upper.Sub(next.Add(-maxLookback)) > 0 {
		candidate := upper.Add(-step)
		if !schedule.Next(candidate).UTC().Truncate(time.Minute).Equal(next) {
			lower = candidate
			break
		}
		upper = candidate
		step *= 2
	}
	if schedule.Next(lower).UTC().Truncate(time.Minute).Equal(next) {
		return time.Time{}, fmt.Errorf("could not determine previous cron occurrence before %s", next.Format(time.RFC3339))
	}

	for upper.Sub(lower) > time.Minute {
		mid := lower.Add(upper.Sub(lower) / 2).UTC().Truncate(time.Minute)
		if !mid.Before(upper) {
			mid = upper.Add(-time.Minute)
		}
		if schedule.Next(mid).UTC().Truncate(time.Minute).Equal(next) {
			upper = mid
			continue
		}
		lower = mid
	}

	return upper.UTC().Truncate(time.Minute), nil
}
