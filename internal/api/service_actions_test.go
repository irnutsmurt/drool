package api

import (
	"regexp"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"drool/internal/auth"
	"drool/internal/sshx/sshtest"
)

// fakeUnits answers systemctl over SSH for a few units (active state and
// unit-file state); nothing about services or privileges reaches the
// machine running the test, other commands run as sshtest.ExecLocal would.
type fakeUnits struct {
	mu    sync.Mutex
	units map[string][2]string
}

var (
	fakeShowRe = regexp.MustCompile(`systemctl show (?:--no-pager )?-p \S+ -- '?([\w.@-]+)'?`)
	fakeVerbRe = regexp.MustCompile(`systemctl --no-ask-password (\w+) -- '?([\w.@-]+)'?`)
)

func (f *fakeUnits) exec(cmd string, stdin []byte) (string, string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := cmd + "\n" + string(stdin)
	if m := fakeVerbRe.FindStringSubmatch(all); m != nil {
		u := f.units[m[2]]
		switch m[1] {
		case "start", "restart":
			u[0] = "active"
		case "stop":
			u[0] = "inactive"
		case "enable":
			u[1] = "enabled"
		case "disable":
			u[1] = "disabled"
		}
		f.units[m[2]] = u
		return "", "", 0
	}
	if m := fakeShowRe.FindStringSubmatch(all); m != nil {
		u, ok := f.units[m[1]]
		if !ok {
			return "LoadState=not-found\nActiveState=inactive\nSubState=dead\nUnitFileState=\nCanReload=no\n", "", 0
		}
		sub := map[string]string{"active": "running", "inactive": "dead"}[u[0]]
		return "LoadState=loaded\nActiveState=" + u[0] + "\nSubState=" + sub + "\nUnitFileState=" + u[1] + "\nCanReload=no\n", "", 0
	}
	if strings.Contains(all, "systemctl status") {
		return "", "", 0
	}
	// Nothing about services or privileges ever reaches this machine.
	if strings.Contains(all, "systemctl") || strings.Contains(all, "sudo") {
		return "", "fake host: unexpected command: " + cmd, 1
	}
	return sshtest.ExecLocal(cmd, stdin)
}

func (f *fakeUnits) state(unit string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.units[unit]
	return u[0] + "/" + u[1]
}

// TestServiceActionsLeaveExpectedState (final After-M17 batch): service
// actions from the Services view are ordinary, audited runs for those who
// may run things (operators and administrators, not auditors or viewers),
// and never change what DROOL expects of a service: stopping and disabling
// a service expected to run, or starting one expected to be stopped, leaves
// the expected state as it was; changing it is a separate, explicit step.
func TestServiceActionsLeaveExpectedState(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("op", auth.RoleOperator)
	h.mkUser("aud", auth.RoleAuditor)
	h.mkUser("view", auth.RoleViewer)
	c := h.login("admin")
	// A host DROOL signs in to as root (no sudo), whose systemctl is fake.
	units := &fakeUnits{units: map[string][2]string{"demo.service": {"active", "enabled"}, "batch.service": {"inactive", "disabled"}}}
	var ck controllerKeyResponse
	c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)
	key := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, key)
	srv.SetExec(units.exec)
	authorizeCred(t, srv, ck.PublicKey)
	addr, port := srv.Host()
	var host hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "svc-01", "address": addr, "port": port, "user": "root", "become_method": "none"}).json(t, &host)
	c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/scan", nil)
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": ssh.FingerprintSHA256(key.PublicKey())}); r.status != 200 {
		t.Fatalf("approve: %s", r.body)
	}
	id := host.ID
	expect := func(unit, value string) {
		t.Helper()
		if r := c.do("PUT", "/api/v1/monitoring/health/overrides", map[string]any{"scope": "host", "host_id": id, "check": "service_expected",
			"subject": "unit:" + unit, "subject_label": unit, "fields": map[string]any{"expected": value}}); r.status != 200 {
			t.Fatalf("expect %s %s: %d %s", unit, value, r.status, r.body)
		}
	}
	expect("demo.service", "running")
	expect("batch.service", "stopped")
	expected := func(unit string) string {
		t.Helper()
		var f string
		h.st.Reader.QueryRow(`SELECT fields_json FROM mon_health_overrides WHERE host_id = ? AND check_id = 'service_expected' AND subject = ?`, id, "unit:"+unit).Scan(&f)
		return f
	}
	demoBefore, batchBefore := expected("demo.service"), expected("batch.service")
	if !strings.Contains(demoBefore, "running") || !strings.Contains(batchBefore, "stopped") {
		t.Fatalf("expected states: %s %s", demoBefore, batchBefore)
	}
	job := func(cl *client, unit string, fields map[string]any) (int, string) {
		t.Helper()
		step := map[string]any{"type": "service", "name": unit}
		for k, v := range fields {
			step[k] = v
		}
		r := cl.do("POST", "/api/v1/jobs", map[string]any{"name": "Disable & Stop " + unit, "steps": []any{step}, "targets": map[string]any{"hosts": []string{id}}})
		if r.status != 201 {
			return r.status, string(r.body)
		}
		var j jobJSON
		r.json(t, &j)
		waitJob(t, cl, j.ID)
		var jh jobHostResp
		cl.do("GET", "/api/v1/jobs/"+j.ID+"/hosts/"+id, nil).json(t, &jh)
		return r.status, jh.State + " " + jh.ErrorMessage
	}

	// Who may: not auditors or viewers.
	for _, who := range []string{"aud", "view"} {
		if st, body := job(h.login(who), "demo.service", map[string]any{"state": "stopped"}); st != 403 {
			t.Fatalf("%s ran a service action: %d %s", who, st, body)
		}
	}
	if units.state("demo.service") != "active/enabled" {
		t.Fatal("a refused action changed the unit")
	}

	// An operator disables and stops a service DROOL expects to run: done,
	// verified on the host, and still expected to run.
	op := h.login("op")
	if st, res := job(op, "demo.service", map[string]any{"state": "stopped", "enabled": false}); st != 201 || !strings.HasPrefix(res, "succeeded") {
		t.Fatalf("disable & stop: %d %s", st, res)
	}
	if units.state("demo.service") != "inactive/disabled" {
		t.Fatalf("on the host: %s", units.state("demo.service"))
	}
	if got := expected("demo.service"); got != demoBefore {
		t.Fatalf("stopping changed the expected state: %s → %s", demoBefore, got)
	}
	// Starting (and enabling) one expected to be stopped likewise.
	if st, res := job(op, "batch.service", map[string]any{"state": "running", "enabled": true}); st != 201 || !strings.HasPrefix(res, "succeeded") {
		t.Fatalf("enable & start: %d %s", st, res)
	}
	if got := expected("batch.service"); got != batchBefore {
		t.Fatalf("starting changed the expected state: %s → %s", batchBefore, got)
	}

	// Audited as runs, by whom, with the service step; no expected-state
	// change was recorded for them.
	assertAuditLike(t, h, "job.create", `"steps":["service"]`)
	var n int
	h.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'job.create' AND actor_name = 'op'`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d runs audited for the operator", n)
	}
	h.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action LIKE 'monitoring.health_override.%' AND actor_name = 'op'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d expected-state changes by the operator's actions", n)
	}

	// The explicit follow-up the dialog offers: an operator may set it on
	// the host.
	if r := op.do("PUT", "/api/v1/monitoring/health/overrides", map[string]any{"scope": "host", "host_id": id, "check": "service_expected",
		"subject": "unit:demo.service", "subject_label": "demo.service", "fields": map[string]any{"expected": "stopped"}}); r.status != 200 {
		t.Fatalf("mark it expected stopped: %d %s", r.status, r.body)
	}
	if !strings.Contains(expected("demo.service"), "stopped") {
		t.Fatalf("after the follow-up: %s", expected("demo.service"))
	}
}
