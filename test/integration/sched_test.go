//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSchedulerRestart runs the scheduler in the real controller process
// (ticking every second; runs more than 20 s late count as missed) and
// kills or stops it around due times: no occurrence ever gets a second job,
// an interrupted run is never re-run, and what was due while DROOL was down
// follows the missed-run policies, including recurring maintenance windows.
func TestSchedulerRestart(t *testing.T) {
	freshController(t) // a controller of its own: nothing from other tests (M17)
	d := selected()[0]
	c := startHost(t, image(t, d), "sched", true, netOuter)
	h := addHost(t, "sched", c, hostOpts{})

	type occurrence struct {
		Status        string `json:"status"`
		Reason        string `json:"reason"`
		JobID         string `json:"job_id"`
		JobStatus     string `json:"job_status"`
		MaintenanceID string `json:"maintenance_id"`
		Outcome       string `json:"outcome"`
	}
	occurrences := func(t *testing.T, id string) []occurrence {
		t.Helper()
		var out struct{ Items []occurrence }
		ctl.must(t, "GET", "/api/v1/schedules/"+id+"/occurrences", nil, &out)
		return out.Items
	}
	// minute returns the first whole minute at least lead from now.
	minute := func(lead time.Duration) time.Time {
		return time.Now().UTC().Add(lead).Truncate(time.Minute).Add(time.Minute)
	}
	create := func(t *testing.T, body map[string]any) string {
		t.Helper()
		var sc struct {
			ID string `json:"id"`
		}
		body["time_zone"] = "UTC"
		ctl.must(t, "POST", "/api/v1/schedules", body, &sc)
		return sc.ID
	}
	once := func(at time.Time) map[string]any {
		return map[string]any{"kind": "once", "at": at.Format("2006-01-02T15:04")}
	}
	daily := func(at time.Time) map[string]any {
		return map[string]any{"kind": "daily", "time": at.Format("15:04")}
	}
	restart := func(t *testing.T) {
		t.Helper()
		if err := ctl.start(); err != nil {
			t.Fatal(err)
		}
		if err := ctl.login(); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a crash during a burst creates no duplicate", func(t *testing.T) {
		const n = 200
		at := minute(20 * time.Second)
		var ids []string
		for i := range n {
			ids = append(ids, create(t, map[string]any{"name": fmt.Sprintf("burst %02d", i), "recurrence": once(at),
				"targets": map[string]any{"hosts": []string{h.ID}}, "steps": []any{map[string]any{"type": "command", "command": "sleep 2", "read_only": true}}}))
		}
		time.Sleep(time.Until(at))
		// Kill the controller as soon as the first scheduled job appears:
		// the rest of the burst is still being dispatched.
		deadline := time.Now().Add(30 * time.Second)
		for {
			var jobs struct {
				Items []struct {
					Origin *struct{ ScheduleID string } `json:"origin"`
				}
			}
			ctl.must(t, "GET", "/api/v1/jobs?limit=1", nil, &jobs)
			if len(jobs.Items) > 0 && jobs.Items[0].Origin != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the burst didn't start")
			}
			time.Sleep(10 * time.Millisecond)
		}
		ctl.crash()
		restart(t)
		seen := map[string]bool{}
		interrupted, ran := 0, 0
		waitFor(t, 3*time.Minute, "every occurrence handled", func() bool {
			for _, id := range ids {
				o := occurrences(t, id)
				if len(o) != 1 || o[0].JobID == "" || o[0].JobStatus == "pending" || o[0].JobStatus == "running" {
					return false
				}
			}
			return true
		})
		for _, id := range ids {
			o := occurrences(t, id)
			expect(t, len(o) == 1 && o[0].Status == "dispatched", "schedule %s: %+v", id, o)
			if len(o) != 1 {
				continue
			}
			expect(t, !seen[o[0].JobID], "job %s belongs to two occurrences", o[0].JobID)
			seen[o[0].JobID] = true
			switch o[0].JobStatus {
			case "interrupted":
				interrupted++
				expect(t, strings.Contains(o[0].Outcome, "not re-run automatically"), "outcome %q", o[0].Outcome)
			case "completed":
				ran++
			default:
				t.Errorf("schedule %s: job %s is %s", id, o[0].JobID, o[0].JobStatus)
			}
		}
		t.Logf("%d occurrences: %d jobs interrupted by the crash (not re-run), %d dispatched or finished after the restart", n, interrupted, ran)
		expect(t, interrupted > 0 && ran > 0, "the crash should have landed mid-burst (interrupted %d, ran %d)", interrupted, ran)
	})

	t.Run("while DROOL was down", func(t *testing.T) {
		at := minute(15 * time.Second)
		step := []any{map[string]any{"type": "command", "command": "true", "read_only": true}}
		tgt := map[string]any{"hosts": []string{h.ID}}
		oneTime := create(t, map[string]any{"name": "one-time", "recurrence": once(at), "targets": tgt, "steps": step})
		skip := create(t, map[string]any{"name": "daily skip", "recurrence": daily(at), "targets": tgt, "steps": step})
		runOnce := create(t, map[string]any{"name": "daily run once", "recurrence": daily(at), "targets": tgt, "steps": step, "missed_policy": "run_once"})
		short := create(t, map[string]any{"kind": "maintenance", "name": "short window", "recurrence": daily(at), "targets": tgt, "duration_minutes": 1, "reason": "patching"})
		long := create(t, map[string]any{"kind": "maintenance", "name": "long window", "recurrence": daily(at), "targets": tgt, "duration_minutes": 3, "reason": "patching"})

		// Down from just before the due minute until well after it (past
		// the short window's end, within the long window).
		time.Sleep(time.Until(at.Add(-5 * time.Second)))
		ctl.stop()
		time.Sleep(time.Until(at.Add(70 * time.Second)))
		restart(t)
		waitFor(t, time.Minute, "late runs", func() bool {
			a, b := occurrences(t, oneTime), occurrences(t, runOnce)
			return len(a) == 1 && a[0].JobStatus == "completed" && len(b) == 1 && b[0].JobStatus == "completed"
		})

		o := occurrences(t, oneTime)
		expect(t, len(o) == 1 && strings.Contains(o[0].Reason, "started late"), "one-time run starts late: %+v", o)
		o = occurrences(t, runOnce)
		expect(t, len(o) == 1 && strings.Contains(o[0].Reason, "runs one missed occurrence"), "run once: %+v", o)
		o = occurrences(t, skip)
		expect(t, len(o) == 1 && o[0].Status == "missed" && o[0].JobID == "", "skip policy: %+v", o)
		o = occurrences(t, short)
		expect(t, len(o) == 1 && o[0].Status == "missed" && o[0].MaintenanceID == "", "a window that passed entirely is missed: %+v", o)
		o = occurrences(t, long)
		expect(t, len(o) == 1 && o[0].Status == "dispatched" && o[0].MaintenanceID != "" && strings.Contains(o[0].Reason, "original end"), "long window: %+v", o)

		var m struct {
			Maintenance []struct {
				ID         string `json:"id"`
				Reason     string `json:"reason"`
				EndsAt     *int64 `json:"ends_at"`
				ScheduleID string `json:"schedule_id"`
			} `json:"maintenance"`
		}
		ctl.must(t, "GET", "/api/v1/maintenance?state=active", nil, &m)
		var found bool
		for _, x := range m.Maintenance {
			expect(t, x.ScheduleID != short, "the missed window started maintenance: %+v", x)
			if x.ScheduleID == long {
				found = true
				expect(t, x.EndsAt != nil && *x.EndsAt == at.Add(3*time.Minute).UnixMilli(), "the late window keeps its original end: %+v", x)
				expect(t, strings.Contains(x.Reason, `Recurring window "long window"`), "reason %q", x.Reason)
			}
		}
		expect(t, found, "the long window isn't active: %+v", m.Maintenance)
		// No event for a window that never started.
		var ev struct {
			Events []struct{ Kind, Title string } `json:"events"`
		}
		ctl.must(t, "GET", "/api/v1/events?limit=200", nil, &ev)
		started := 0
		for _, e := range ev.Events {
			if e.Kind == "maintenance.started" && strings.Contains(e.Title, "short window") {
				t.Errorf("event for the missed window: %+v", e)
			}
			if e.Kind == "maintenance.started" && strings.Contains(e.Title, "long window") {
				started++
			}
		}
		expect(t, started == 1, "the late window's start was recorded %d times", started)
		// The recurring schedules continue tomorrow.
		var sc struct {
			NextRunAt *int64 `json:"next_run_at"`
		}
		ctl.must(t, "GET", "/api/v1/schedules/"+skip, nil, &sc)
		expect(t, sc.NextRunAt != nil && *sc.NextRunAt == at.Add(24*time.Hour).UnixMilli(), "next run %v", sc.NextRunAt)
	})
}
