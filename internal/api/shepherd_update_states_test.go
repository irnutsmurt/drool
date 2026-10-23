package api

import (
	"strings"
	"testing"

	"drool/internal/agentsvc"
	"drool/internal/release"
)

// Each Shepherd's update state (ARCHITECTURE-ALPHA §46.3): "up to date"
// rests on a release check that completed; a missing release is
// information, not a failure, and the controller's reason for it is said
// once, not listed against every Shepherd; the Shepherds list and the
// host's page show the state Shepherds → Updates judged.
type updStates struct {
	State    string `json:"state"`
	Summary  string `json:"summary"`
	Verified bool   `json:"verified"`
	Counts   struct {
		Total, Current, Behind, Blocked, Unavailable, Unknown int
	} `json:"counts"`
	Shepherds []agentsvc.ShepherdUpdate `json:"shepherds"`
	Blocked   []struct {
		HostID string `json:"host_id"`
	} `json:"blocked"`
	Problems []struct {
		Kind string `json:"kind"`
	} `json:"problems"`
}

func (e *agentEnv) updStates(t *testing.T) updStates {
	t.Helper()
	var s updStates
	r := e.c.do("GET", "/api/v1/agents/updates/status", nil)
	if r.status != 200 {
		t.Fatalf("update status: %d %s", r.status, r.body)
	}
	r.json(t, &s)
	return s
}

// shepherdState is a host's Shepherd state from the update status, the
// Shepherds list and the host's page, which must agree.
func (e *agentEnv) shepherdState(t *testing.T, st updStates, hostID string) (agentsvc.ShepherdUpdate, string, string) {
	t.Helper()
	var su *agentsvc.ShepherdUpdate
	for i := range st.Shepherds {
		if st.Shepherds[i].HostID == hostID {
			su = &st.Shepherds[i]
		}
	}
	if su == nil {
		t.Fatalf("no update state for %s: %+v", hostID, st.Shepherds)
	}
	var list struct {
		Items []struct {
			HostID        string                   `json:"host_id"`
			Update        *agentsvc.ShepherdUpdate `json:"update"`
			Compatibility struct {
				Status string `json:"status"`
			} `json:"compatibility"`
		} `json:"items"`
	}
	e.c.do("GET", "/api/v1/agents", nil).json(t, &list)
	compat := ""
	for _, a := range list.Items {
		if a.HostID == hostID {
			if a.Update == nil || a.Update.State != su.State || a.Update.Target != su.Target {
				t.Fatalf("the Shepherds list says %+v, the status %+v", a.Update, su)
			}
			compat = a.Compatibility.Status
		}
	}
	var ha struct {
		Identity struct {
			Update *agentsvc.ShepherdUpdate `json:"update"`
		} `json:"identity"`
		UpdateAvailable string `json:"update_available"`
	}
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/agent", nil).json(t, &ha)
	if ha.Identity.Update == nil || ha.Identity.Update.State != su.State {
		t.Fatalf("the host's page says %+v, the status %+v", ha.Identity.Update, su)
	}
	return *su, ha.UpdateAvailable, compat
}

func TestShepherdUpdateStates(t *testing.T) {
	t.Run("a development build: Shepherds on its version, an older one not checked (not a missing release)", func(t *testing.T) {
		o := newOfficialSource(t, "v1.4.0-12-gabcdef0", "development")
		e := newAgentEnv(t)
		o.attach(e)
		cur := e.shepherd(t, "v1.4.0-12-gabcdef0", "")
		old := e.shepherd(t, "v1.3.0", "")
		e.check(t)
		st := e.updStates(t)
		// It can't check official releases: whether one could update the
		// older Shepherd isn't known. Neither "no update available" nor a
		// failure.
		if st.State != "unknown" || st.Verified || st.Counts.Current != 1 || st.Counts.Unknown != 1 || st.Counts.Unavailable != 0 ||
			st.Counts.Blocked != 0 || len(st.Blocked) != 0 || len(st.Problems) != 0 {
			t.Fatalf("an unverifiable source: %+v", st)
		}
		want := "1 Shepherd is older than this DROOL (v1.4.0-12-gabcdef0), and whether a release can update it isn't known: this DROOL can't check official Shepherd releases."
		if st.Summary != want {
			t.Errorf("summary %q, want %q", st.Summary, want)
		}
		if su, upd, _ := e.shepherdState(t, st, cur.host); su.State != "current" || su.Verified || upd != "" {
			t.Errorf("on DROOL's version: %+v %q", su, upd)
		}
		if su, upd, compat := e.shepherdState(t, st, old.host); su.State != "unknown" || su.Reason != "this DROOL can't check official Shepherd releases" ||
			upd != "" || compat == agentsvc.CompatUpdateAvailable {
			t.Errorf("older, not checked: %+v %q %q", su, upd, compat)
		}
	})

	t.Run("a check that completed and found no release published: no update available", func(t *testing.T) {
		o := newOfficialSource(t, "v1.4.0", "stable")
		e := newAgentEnv(t)
		o.attach(e)
		old := e.shepherd(t, "v1.3.0", "")
		e.check(t)
		st := e.updStates(t)
		if st.State != "unavailable" || !st.Verified || st.Counts.Unavailable != 1 || st.Counts.Unknown != 0 || len(st.Blocked) != 0 ||
			!strings.Contains(st.Summary, "1 Shepherd is older than this DROOL (v1.4.0), and no release DROOL can install is newer: ") {
			t.Fatalf("checked, nothing published: %+v", st)
		}
		if su, upd, _ := e.shepherdState(t, st, old.host); su.State != "unavailable" || upd != "" {
			t.Errorf("older, checked, nothing to install: %+v %q", su, upd)
		}
	})

	t.Run("no release check possible: Shepherds run DROOL's version, not up to date", func(t *testing.T) {
		o := newOfficialSource(t, "v1.4.0", "stable")
		release.SetOfficialKeysForTest(nil) // undone by the source's cleanup
		e := newAgentEnv(t)
		o.attach(e)
		a := e.shepherd(t, "v1.4.0", "")
		b := e.shepherd(t, "v1.4.0", "")
		e.check(t)
		st := e.updStates(t)
		if st.State != "current" || st.Verified || st.Counts.Current != 2 || st.Summary != "All 2 Shepherds run this DROOL's version (v1.4.0)." {
			t.Fatalf("unverified: %+v", st)
		}
		for _, h := range []string{a.host, b.host} {
			if su, _, _ := e.shepherdState(t, st, h); su.State != "current" || su.Verified {
				t.Errorf("%s: %+v", h, su)
			}
		}
	})

	t.Run("the release source can't be reached: an older Shepherd is not checked, not failed", func(t *testing.T) {
		o := newOfficialSource(t, "v1.4.0", "stable")
		e := newAgentEnv(t)
		o.attach(e)
		old := e.shepherd(t, "v1.3.0", "")
		o.srv.Close()
		e.check(t)
		st := e.updStates(t)
		if st.State != "unknown" || st.Counts.Unknown != 1 || st.Counts.Blocked != 0 || len(st.Blocked) != 0 ||
			st.Summary != "1 Shepherd is older than this DROOL (v1.4.0), and whether a release can update it isn't known: DROOL couldn't reach its release source." {
			t.Fatalf("unreachable: %+v", st)
		}
		if su, upd, _ := e.shepherdState(t, st, old.host); su.State != "unknown" || upd != "" {
			t.Errorf("older, not checked: %+v %q", su, upd)
		}
	})

	t.Run("verified: up to date, and an update the host's page offers", func(t *testing.T) {
		o := newOfficialSource(t, "v1.4.0", "stable")
		e := newAgentEnv(t)
		o.attach(e)
		p := agentsvc.DefaultUpdatePolicy()
		p.Policy = "manual"
		if r := e.c.do("PUT", "/api/v1/agents/updates/policy", p); r.status != 200 {
			t.Fatalf("policy: %d %s", r.status, r.body)
		}
		o.publishOwn()
		cur := e.shepherd(t, "v1.4.0", "")
		old := e.shepherd(t, "v1.3.0", "")
		e.check(t)
		st := e.updStates(t)
		if st.State != "available" || !st.Verified || st.Counts.Current != 1 || st.Counts.Behind != 1 {
			t.Fatalf("verified: %+v", st)
		}
		if su, upd, _ := e.shepherdState(t, st, cur.host); su.State != "current" || !su.Verified || upd != "" {
			t.Errorf("up to date: %+v %q", su, upd)
		}
		if su, upd, compat := e.shepherdState(t, st, old.host); su.State != "available" || su.Target != "v1.4.0" || upd != "v1.4.0" ||
			compat != agentsvc.CompatUpdateAvailable {
			t.Errorf("update available: %+v %q %q", su, upd, compat)
		}
	})
}
