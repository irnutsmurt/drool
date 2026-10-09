// Package sched is DROOL's scheduling and fleet automation (M13):
// reusable workflows with revisions, schedules (a job or a workflow on a
// recurrence, or a recurring maintenance window), durable occurrences and
// the scheduler loop that turns due occurrences into ordinary jobs (through
// the job engine) or ordinary M12 maintenance.
package sched

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Recurrence kinds. The UI offers exactly these; there are no cron
// expressions.
const (
	KindOnce    = "once"    // one local date and time
	KindHourly  = "hourly"  // every N hours, counted from a first run
	KindDaily   = "daily"   // every day at a local time
	KindWeekly  = "weekly"  // selected weekdays at a local time
	KindMonthly = "monthly" // a day of the month at a local time
)

// Recurrence is when a schedule runs, in its time zone.
//
// Wall-clock kinds (once, daily, weekly, monthly) follow the local clock
// through daylight-saving changes:
//
//   - a local time that doesn't exist that day (the clocks spring
//     forward over it) runs at the first valid local time after the gap
//     (02:30 on a 02:00→03:00 day runs at 03:00);
//   - a local time that happens twice (the clocks fall back) runs once,
//     at its first occurrence (the earlier instant).
//
// Hourly schedules count elapsed time: every N hours is always exactly N
// hours apart, so across a DST change their wall-clock times shift.
type Recurrence struct {
	Kind string `json:"kind"`
	// At: once, and the first run of hourly ("2026-10-11T03:00", local).
	At string `json:"at,omitempty"`
	// EveryHours: hourly, 1–168.
	EveryHours int `json:"every_hours,omitempty"`
	// Time: daily, weekly, monthly ("03:00", local).
	Time string `json:"time,omitempty"`
	// Weekdays: weekly ("mon" … "sun").
	Weekdays []string `json:"weekdays,omitempty"`
	// DayOfMonth: monthly, 1–31; days a month doesn't have mean its last day.
	DayOfMonth int `json:"day_of_month,omitempty"`
}

var weekdayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
var weekdayWords = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// LoadZone loads an explicit IANA time zone. The controller's own local
// zone is never assumed.
func LoadZone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "Local" {
		return nil, errors.New("choose a time zone (for example Europe/Berlin or America/Los_Angeles)")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	return loc, nil
}

func parseClock(s string) (int, int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, 0, fmt.Errorf("time must be HH:MM (24-hour), not %q", s)
	}
	return t.Hour(), t.Minute(), nil
}

func parseLocal(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02T15:04", strings.TrimSpace(s))
	if err != nil {
		return t, fmt.Errorf("date and time must be YYYY-MM-DDTHH:MM, not %q", s)
	}
	return t, nil
}

// Normalize validates the recurrence.
func (r *Recurrence) Normalize() error {
	switch r.Kind {
	case KindOnce, KindHourly:
		if _, err := parseLocal(r.At); err != nil {
			return err
		}
		if r.Kind == KindHourly && (r.EveryHours < 1 || r.EveryHours > 168) {
			return errors.New("every_hours must be 1–168")
		}
		if r.Kind == KindOnce {
			r.EveryHours = 0
		}
		r.Time, r.Weekdays, r.DayOfMonth = "", nil, 0
	case KindDaily, KindWeekly, KindMonthly:
		if _, _, err := parseClock(r.Time); err != nil {
			return err
		}
		r.At, r.EveryHours = "", 0
		switch r.Kind {
		case KindWeekly:
			var ds []string
			for _, d := range r.Weekdays {
				d = strings.ToLower(strings.TrimSpace(d))
				if len(d) > 3 {
					d = d[:3]
				}
				if !slices.Contains(weekdayNames, d) {
					return fmt.Errorf("unknown weekday %q", d)
				}
				if !slices.Contains(ds, d) {
					ds = append(ds, d)
				}
			}
			if len(ds) == 0 {
				return errors.New("choose at least one weekday")
			}
			sort.Slice(ds, func(i, j int) bool { return slices.Index(weekdayNames, ds[i]) < slices.Index(weekdayNames, ds[j]) })
			r.Weekdays, r.DayOfMonth = ds, 0
		case KindMonthly:
			if r.DayOfMonth < 1 || r.DayOfMonth > 31 {
				return errors.New("day_of_month must be 1–31")
			}
			r.Weekdays = nil
		default:
			r.Weekdays, r.DayOfMonth = nil, 0
		}
	default:
		return errors.New(`kind must be "once", "hourly", "daily", "weekly" or "monthly"`)
	}
	return nil
}

// Recurring reports whether the schedule repeats.
func (r Recurrence) Recurring() bool { return r.Kind != KindOnce }

// wallUTC represents a local wall time as a UTC value (to compare walls).
func wallUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// Wall returns the instant of a local wall time in loc, with DROOL's DST
// rules: the earliest instant showing exactly that wall time; if none
// exists (a spring-forward gap), the first instant after the gap.
func Wall(y int, mo time.Month, d, hh, mm int, loc *time.Location) time.Time {
	want := time.Date(y, mo, d, hh, mm, 0, 0, time.UTC)
	guess := time.Date(y, mo, d, hh, mm, 0, 0, loc)
	var cands []time.Time
	seen := map[int]bool{}
	for _, probe := range []time.Time{guess.Add(-26 * time.Hour), guess.Add(-3 * time.Hour), guess, guess.Add(3 * time.Hour), guess.Add(26 * time.Hour)} {
		_, off := probe.Zone()
		if seen[off] {
			continue
		}
		seen[off] = true
		inst := want.Add(-time.Duration(off) * time.Second)
		if wallUTC(inst.In(loc)).Equal(want) {
			cands = append(cands, inst)
		}
	}
	if len(cands) > 0 {
		slices.SortFunc(cands, func(a, b time.Time) int { return a.Compare(b) })
		return cands[0].In(loc)
	}
	// A gap: binary-search the first instant whose wall time is at or
	// after the requested one (wall time is monotonic around a gap).
	lo, hi := guess.Add(-4*time.Hour), guess.Add(4*time.Hour)
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2)
		if wallUTC(mid.In(loc)).Before(want) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi.Truncate(time.Second).In(loc)
}

// Next returns the first occurrence strictly after `after`, and false
// when there is none (a one-time schedule that has passed).
func (r Recurrence) Next(after time.Time, loc *time.Location) (time.Time, bool) {
	switch r.Kind {
	case KindOnce:
		l, _ := parseLocal(r.At)
		t := Wall(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), loc)
		return t, t.After(after)
	case KindHourly:
		l, _ := parseLocal(r.At)
		t0 := Wall(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), loc)
		if t0.After(after) {
			return t0, true
		}
		step := time.Duration(r.EveryHours) * time.Hour
		k := after.Sub(t0)/step + 1
		return t0.Add(k * step).In(loc), true
	}
	hh, mm, _ := parseClock(r.Time)
	local := after.In(loc)
	switch r.Kind {
	case KindDaily, KindWeekly:
		days := map[time.Weekday]bool{}
		for _, d := range r.Weekdays {
			days[time.Weekday(slices.Index(weekdayNames, d))] = true
		}
		for i := -1; i <= 9; i++ {
			day := time.Date(local.Year(), local.Month(), local.Day()+i, 12, 0, 0, 0, time.UTC)
			if r.Kind == KindWeekly && !days[day.Weekday()] {
				continue
			}
			if t := Wall(day.Year(), day.Month(), day.Day(), hh, mm, loc); t.After(after) {
				return t, true
			}
		}
	case KindMonthly:
		for i := -1; i <= 14; i++ {
			first := time.Date(local.Year(), local.Month()+time.Month(i), 1, 12, 0, 0, 0, time.UTC)
			last := first.AddDate(0, 1, -1).Day()
			if t := Wall(first.Year(), first.Month(), min(r.DayOfMonth, last), hh, mm, loc); t.After(after) {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// Between lists the occurrences in (from, to], at most limit of them, and
// whether there were more.
func (r Recurrence) Between(from, to time.Time, loc *time.Location, limit int) ([]time.Time, bool) {
	var out []time.Time
	t := from
	for {
		n, ok := r.Next(t, loc)
		if !ok || n.After(to) {
			return out, false
		}
		if len(out) == limit {
			return out, true
		}
		out = append(out, n)
		t = n
	}
}

// Describe says when it runs, for people.
func (r Recurrence) Describe(loc *time.Location) string {
	zone := loc.String()
	switch r.Kind {
	case KindOnce:
		l, _ := parseLocal(r.At)
		return fmt.Sprintf("Once on %s at %s (%s)", l.Format("Mon 2 Jan 2006"), l.Format("15:04"), zone)
	case KindHourly:
		l, _ := parseLocal(r.At)
		every := "hour"
		if r.EveryHours > 1 {
			every = fmt.Sprintf("%d hours", r.EveryHours)
		}
		return fmt.Sprintf("Every %s from %s at %s (%s)", every, l.Format("Mon 2 Jan 2006"), l.Format("15:04"), zone)
	case KindDaily:
		return fmt.Sprintf("Every day at %s (%s)", r.Time, zone)
	case KindWeekly:
		var ws []string
		for _, d := range r.Weekdays {
			ws = append(ws, weekdayWords[slices.Index(weekdayNames, d)])
		}
		days := strings.Join(ws, ", ")
		switch len(ws) {
		case 7:
			days = "day"
		case 5:
			if !slices.Contains(r.Weekdays, "sat") && !slices.Contains(r.Weekdays, "sun") {
				days = "weekday"
			}
		}
		return fmt.Sprintf("Every %s at %s (%s)", days, r.Time, zone)
	case KindMonthly:
		extra := ""
		if r.DayOfMonth > 28 {
			extra = ", or the month's last day"
		}
		return fmt.Sprintf("Monthly on day %d%s at %s (%s)", r.DayOfMonth, extra, r.Time, zone)
	}
	return r.Kind
}

// MinSpacing is the shortest gap between consecutive occurrences (for
// maintenance windows, which must not overlap themselves).
func (r Recurrence) MinSpacing() time.Duration {
	switch r.Kind {
	case KindOnce:
		return 0
	case KindHourly:
		return time.Duration(r.EveryHours) * time.Hour
	case KindMonthly:
		return 27 * 24 * time.Hour
	}
	// Daily and weekly: a day, less an hour for a DST change.
	return 23 * time.Hour
}

// Shifted explains a run that doesn't happen at its configured wall time
// (a spring-forward gap), or "".
func (r Recurrence) Shifted(t time.Time, loc *time.Location) string {
	if r.Kind == KindHourly {
		return ""
	}
	want := r.Time
	if r.Kind == KindOnce {
		l, _ := parseLocal(r.At)
		want = l.Format("15:04")
	}
	if got := t.In(loc).Format("15:04"); got != want {
		return fmt.Sprintf("at %s instead of %s: the clocks skip %s that day", got, want, want)
	}
	return ""
}
