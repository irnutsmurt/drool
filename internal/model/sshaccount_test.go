package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// DROOL's own SSH account is set up only through its own endpoint: never a
// job step a user writes, a workflow, a schedule or a re-run.
func TestSSHAccountIsInternal(t *testing.T) {
	step := Operation{Type: OpSSHAccount, SSHAccount: &SSHAccountOp{}}
	spec := JobSpec{Steps: []Operation{step}, Targets: TargetSpec{Hosts: []string{"host_a"}}}
	if err := spec.Normalize(); err == nil || !strings.Contains(err.Error(), "host's page") {
		t.Fatalf("a submitted job: %v", err)
	}
	if err := NormalizeSteps([]Operation{step}); err == nil || !strings.Contains(err.Error(), "host's page") {
		t.Fatalf("a workflow: %v", err)
	}
	ok := JobSpec{Steps: []Operation{{Type: OpSSHAccount, SSHAccount: &SSHAccountOp{}}}, Targets: TargetSpec{Hosts: []string{"host_a"}}}
	if err := ok.NormalizeInternal(); err != nil {
		t.Fatal(err)
	}
	if ok.Steps[0].SSHAccount.User != "drool" || ok.Name != "Set up DROOL's own SSH account (drool)" {
		t.Fatalf("defaults: %+v %q", ok.Steps[0].SSHAccount, ok.Name)
	}
	for name, bad := range map[string]JobSpec{
		"two hosts":   {Steps: []Operation{step}, Targets: TargetSpec{Hosts: []string{"a", "b"}}},
		"a group":     {Steps: []Operation{step}, Targets: TargetSpec{Hosts: []string{"a"}, Groups: []string{"g"}}},
		"other steps": {Steps: []Operation{step, {Type: OpWait, Wait: &WaitOp{Seconds: 1}}}, Targets: TargetSpec{Hosts: []string{"a"}}},
	} {
		if err := bad.NormalizeInternal(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The firewall's internal operation stays refused even internally.
	fw := JobSpec{Steps: []Operation{{Type: OpFirewall, Firewall: &FirewallOp{}}}, Targets: TargetSpec{Hosts: []string{"a"}}}
	if err := fw.NormalizeInternal(); err == nil {
		t.Fatal("firewall step accepted")
	}
	m := step.Meta()
	if m.Lock != LockExclusive || m.Idempotent || !IsLocal(OpSSHAccount) {
		t.Fatalf("meta %+v, local %v", m, IsLocal(OpSSHAccount))
	}
}

func TestSSHAccountUser(t *testing.T) {
	for _, u := range []string{"drool", "drool-mgmt", "_svc", "d0", strings.Repeat("a", 32)} {
		if err := ValidSSHAccountUser(u); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
	for _, u := range []string{"root", "Drool", "drool.admin", "-drool", "0drool", "", "dr ool", strings.Repeat("a", 33), "drool$"} {
		if err := ValidSSHAccountUser(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestSSHAccountJSON(t *testing.T) {
	var op Operation
	if err := json.Unmarshal([]byte(`{"type":"ssh_account","user":"drool-mgmt"}`), &op); err != nil {
		t.Fatal(err)
	}
	if op.SSHAccount == nil || op.SSHAccount.User != "drool-mgmt" {
		t.Fatalf("%+v", op)
	}
	b, err := json.Marshal(op)
	if err != nil || string(b) != `{"type":"ssh_account","user":"drool-mgmt"}` {
		t.Fatalf("%s %v", b, err)
	}
	if err := json.Unmarshal([]byte(`{"type":"ssh_account","user":"x","password":"y"}`), &op); err == nil {
		t.Fatal("unknown field accepted")
	}
}
