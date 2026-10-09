package sched

import (
	"strings"
	"testing"
	"time"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := LoadZone(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func next(t *testing.T, r Recurrence, after time.Time, loc *time.Location) time.Time {
	t.Helper()
	if err := r.Normalize(); err != nil {
		t.Fatal(err)
	}
	n, ok := r.Next(after, loc)
	if !ok {
		t.Fatalf("no next run after %s", after)
	}
	return n
}

func TestRecurrenceDST(t *testing.T) {
	la := zone(t, "America/Los_Angeles")
	t.Run("spring forward: Sunday 02:30 runs at 03:00, the first valid time after the gap", func(t *testing.T) {
		r := Recurrence{Kind: KindWeekly, Time: "02:30", Weekdays: []string{"sun"}}
		n := next(t, r, utc("2027-03-10T00:00:00Z"), la)
		if !n.Equal(utc("2027-03-14T10:00:00Z")) || n.In(la).Format("15:04 MST") != "03:00 PDT" {
			t.Fatalf("%s (%s)", n.UTC(), n.In(la))
		}
		if s := r.Shifted(n, la); !strings.Contains(s, "at 03:00 instead of 02:30") {
			t.Fatal(s)
		}
		// The following Sunday is 02:30 again.
		if n2 := next(t, r, n, la); n2.In(la).Format("2006-01-02 15:04") != "2027-03-21 02:30" {
			t.Fatal(n2.In(la))
		}
	})
	t.Run("fall back: 01:30 happens twice and runs once, at the first", func(t *testing.T) {
		r := Recurrence{Kind: KindDaily, Time: "01:30"}
		n := next(t, r, utc("2027-11-06T12:00:00Z"), la)
		if !n.Equal(utc("2027-11-07T08:30:00Z")) || n.In(la).Format("MST") != "PDT" {
			t.Fatalf("%s %s", n.UTC(), n.In(la))
		}
		n2 := next(t, r, n, la)
		if n2.In(la).Format("2006-01-02 15:04 MST") != "2027-11-08 01:30 PST" {
			t.Fatalf("not twice on Nov 7: %s", n2.In(la))
		}
	})
	t.Run("Lord Howe's 30-minute shift", func(t *testing.T) {
		lh := zone(t, "Australia/Lord_Howe")
		r := Recurrence{Kind: KindDaily, Time: "02:15"}
		n := next(t, r, utc("2026-10-03T00:00:00Z"), lh)
		if n.In(lh).Format("2006-01-02 15:04") != "2026-10-04 02:30" {
			t.Fatalf("gap 02:00–02:30: %s", n.In(lh))
		}
		r = Recurrence{Kind: KindDaily, Time: "01:45"}
		n = next(t, r, utc("2027-04-03T00:00:00Z"), lh)
		n2 := next(t, r, n, lh)
		if n.In(lh).Format("2006-01-02 15:04 -0700") != "2027-04-04 01:45 +1100" || n2.In(lh).Format("2006-01-02") != "2027-04-05" {
			t.Fatalf("repeated 01:30–02:00: %s, %s", n.In(lh), n2.In(lh))
		}
	})
	t.Run("hourly counts elapsed hours across DST", func(t *testing.T) {
		r := Recurrence{Kind: KindHourly, At: "2027-03-13T22:00", EveryHours: 6}
		n := next(t, r, utc("2027-03-14T06:30:00Z"), la) // after the first run (22:00 PST = 06:00Z)
		n2 := next(t, r, n, la)
		if n2.Sub(n) != 6*time.Hour || n.In(la).Format("15:04") != "05:00" {
			t.Fatalf("%s %s", n.In(la), n2.In(la))
		}
	})
	t.Run("one-time in a gap; one-time in the past", func(t *testing.T) {
		r := Recurrence{Kind: KindOnce, At: "2027-03-14T02:10"}
		if n := next(t, r, utc("2027-01-01T00:00:00Z"), la); n.In(la).Format("15:04") != "03:00" {
			t.Fatal(n.In(la))
		}
		if _, ok := r.Next(utc("2027-12-01T00:00:00Z"), la); ok {
			t.Fatal("a past one-time schedule has no next run")
		}
	})
	t.Run("monthly on the 31st", func(t *testing.T) {
		r := Recurrence{Kind: KindMonthly, Time: "04:00", DayOfMonth: 31}
		utcLoc := time.UTC
		got := []string{}
		after := utc("2027-01-01T00:00:00Z")
		for range 5 {
			after = next(t, r, after, utcLoc)
			got = append(got, after.Format("2006-01-02"))
		}
		if strings.Join(got, " ") != "2027-01-31 2027-02-28 2027-03-31 2027-04-30 2027-05-31" {
			t.Fatal(got)
		}
		if r := (Recurrence{Kind: KindMonthly, Time: "04:00", DayOfMonth: 29}); next(t, r, utc("2028-02-01T00:00:00Z"), utcLoc).Format("01-02") != "02-29" {
			t.Fatal("leap year")
		}
	})
}

// Every local date gets exactly one daily run, in increasing order, in
// every zone, for any time of day (gaps and repeats included).
func TestRecurrenceOncePerLocalDate(t *testing.T) {
	zones := []string{"America/Los_Angeles", "America/New_York", "Europe/Berlin", "Europe/London", "Australia/Lord_Howe", "Australia/Sydney",
		"America/Santiago", "America/Havana", "Asia/Kolkata", "Asia/Tehran", "Pacific/Chatham", "America/Sao_Paulo", "UTC"}
	for _, zn := range zones {
		loc := zone(t, zn)
		for _, clock := range []string{"00:00", "00:30", "01:00", "01:30", "02:00", "02:30", "03:00", "23:30"} {
			r := Recurrence{Kind: KindDaily, Time: clock}
			if err := r.Normalize(); err != nil {
				t.Fatal(err)
			}
			from := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
			to := time.Date(2028, 12, 31, 0, 0, 0, 0, loc)
			runs, more := r.Between(from, to, loc, 2000)
			if more {
				t.Fatal("too many runs")
			}
			dates := map[string]bool{}
			for i, x := range runs {
				d := x.In(loc).Format("2006-01-02")
				if dates[d] {
					t.Fatalf("%s %s: two runs on %s", zn, clock, d)
				}
				dates[d] = true
				if i > 0 && !x.After(runs[i-1]) {
					t.Fatalf("%s %s: not increasing at %s", zn, clock, x)
				}
				// Either the exact wall time, or the first valid time after a gap.
				w := x.In(loc).Format("15:04")
				if w != clock {
					before := x.Add(-time.Second).In(loc)
					if !wallUTC(before).Before(wallUTC(x.In(loc))) || before.Format("15:04") >= clock && before.Day() == x.In(loc).Day() {
						t.Fatalf("%s %s: %s isn't the first valid time after a gap", zn, clock, x.In(loc))
					}
				}
			}
			days := int(to.Sub(from).Hours()/24+0.5) + 1
			if len(runs) < days-2 || len(runs) > days+1 {
				t.Fatalf("%s %s: %d runs over %d days", zn, clock, len(runs), days)
			}
		}
	}
}

func TestRecurrenceValidationAndText(t *testing.T) {
	for _, bad := range []Recurrence{{Kind: "cron", Time: "* * * * *"}, {Kind: KindDaily, Time: "25:00"}, {Kind: KindWeekly, Time: "03:00"},
		{Kind: KindWeekly, Time: "03:00", Weekdays: []string{"funday"}}, {Kind: KindHourly, At: "2026-10-10T01:00"}, {Kind: KindMonthly, Time: "03:00", DayOfMonth: 32},
		{Kind: KindOnce, At: "tomorrow"}} {
		r := bad
		if r.Normalize() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if _, err := LoadZone(""); err == nil {
		t.Error("an empty zone must be refused (never the controller's local zone)")
	}
	if _, err := LoadZone("Local"); err == nil {
		t.Error("Local refused")
	}
	la := zone(t, "America/Los_Angeles")
	for r, want := range map[*Recurrence]string{
		{Kind: KindWeekly, Time: "03:00", Weekdays: []string{"Sunday"}}:                          "Every Sunday at 03:00 (America/Los_Angeles)",
		{Kind: KindWeekly, Time: "03:00", Weekdays: []string{"fri", "mon", "tue", "wed", "thu"}}: "Every weekday at 03:00 (America/Los_Angeles)",
		{Kind: KindDaily, Time: "02:30"}:                                                         "Every day at 02:30 (America/Los_Angeles)",
		{Kind: KindHourly, At: "2026-10-09T01:30", EveryHours: 6}:                                "Every 6 hours from Fri 9 Oct 2026 at 01:30 (America/Los_Angeles)",
		{Kind: KindMonthly, Time: "04:00", DayOfMonth: 31}:                                       "Monthly on day 31, or the month's last day at 04:00 (America/Los_Angeles)",
		{Kind: KindOnce, At: "2026-10-11T03:00"}:                                                 "Once on Sun 11 Oct 2026 at 03:00 (America/Los_Angeles)",
	} {
		if err := r.Normalize(); err != nil {
			t.Fatal(err)
		}
		if got := r.Describe(la); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}
