package alerts

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	base, maxD := 30*time.Second, 30*time.Minute
	for n, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 6: 16 * time.Minute, 7: 30 * time.Minute, 20: 30 * time.Minute} {
		for range 50 {
			d := Backoff(n, base, maxD)
			if d < want*8/10 || d > want*12/10 {
				t.Fatalf("attempt %d: %v not within ±20%% of %v", n, d, want)
			}
		}
	}
	// Jitter: a burst of failures doesn't retry in step.
	seen := map[time.Duration]bool{}
	for range 20 {
		seen[Backoff(3, base, maxD)] = true
	}
	if len(seen) < 10 {
		t.Fatalf("no jitter: %v", seen)
	}
}

func TestRouteMatching(t *testing.T) {
	r := Route{Enabled: true}
	if !r.matches("warning", "h1", nil, "cpu", "cpu") {
		t.Fatal("an empty route matches everything")
	}
	r = Route{Enabled: true, Severities: []string{"critical"}, HostIDs: []string{"h1"}, GroupIDs: []string{"nas"}, Categories: []string{"storage"}, Checks: []string{"service_failed"}}
	for _, c := range []struct {
		sev, host string
		groups    []string
		cat, chk  string
		want      bool
	}{
		{"critical", "h1", nil, "storage", "media_errors", true},
		{"critical", "h2", []string{"nas"}, "storage", "probe", true},    // group membership
		{"critical", "h1", nil, "services", "service_failed", true},      // check instead of category
		{"warning", "h1", nil, "storage", "media_errors", false},         // severity
		{"critical", "h3", []string{"media"}, "storage", "probe", false}, // neither host nor group
		{"critical", "h1", nil, "services", "service_stopped", false},    // neither category nor check
	} {
		if got := r.matches(c.sev, c.host, c.groups, c.cat, c.chk); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
	r.Enabled = false
	if r.matches("critical", "h1", nil, "storage", "media_errors") {
		t.Fatal("a disabled route matched")
	}
}
