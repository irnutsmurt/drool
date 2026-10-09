package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func hostsN(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("h%02d", i)
	}
	return out
}

func TestPlanWaves(t *testing.T) {
	sizes := func(p WavePlan) []int {
		var out []int
		for _, w := range p.Waves {
			out = append(out, len(w))
		}
		return out
	}
	t.Run("30 hosts, canary 1, batches of 5", func(t *testing.T) {
		p := PlanWaves(hostsN(30), Strategy{CanaryCount: 1, BatchSize: 5})
		if !p.Canary || !reflect.DeepEqual(sizes(p), []int{1, 5, 5, 5, 5, 5, 4}) || p.Waves[0][0] != 0 || p.Waves[1][0] != 1 {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("no rollout options: one wave, as before", func(t *testing.T) {
		p := PlanWaves(hostsN(7), Strategy{})
		if p.Canary || !reflect.DeepEqual(sizes(p), []int{7}) {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("explicit canaries come first, in host order", func(t *testing.T) {
		p := PlanWaves(hostsN(6), Strategy{CanaryHosts: []string{"h04", "h01"}, BatchSize: 2})
		if !reflect.DeepEqual(p.Waves, [][]int{{1, 4}, {0, 2}, {3, 5}}) {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("an explicit canary that is no longer targeted", func(t *testing.T) {
		p := PlanWaves(hostsN(4), Strategy{CanaryHosts: []string{"gone"}})
		if !p.Canary || !reflect.DeepEqual(p.Waves, [][]int{{0}, {1, 2, 3}}) || !strings.Contains(p.Note, "not among") {
			t.Fatalf("a canary stays: %+v", p)
		}
	})
	t.Run("a single host is not its own canary", func(t *testing.T) {
		p := PlanWaves(hostsN(1), Strategy{CanaryCount: 1})
		if p.Canary || !reflect.DeepEqual(sizes(p), []int{1}) {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("deterministic", func(t *testing.T) {
		a := PlanWaves(hostsN(23), Strategy{CanaryCount: 2, BatchSize: 4})
		for range 20 {
			if b := PlanWaves(hostsN(23), Strategy{CanaryCount: 2, BatchSize: 4}); !reflect.DeepEqual(a, b) {
				t.Fatal("waves changed between plans")
			}
		}
	})
}

func TestWorkflowSteps(t *testing.T) {
	for _, c := range []struct {
		in, err string
	}{
		{`{"type":"wait","seconds":30}`, ""},
		{`{"type":"wait","seconds":0}`, "1 second"},
		{`{"type":"verify_host"}`, ""},
		{`{"type":"verify_services","services":["jellyfin","ssh.service","jellyfin.service"]}`, ""},
		{`{"type":"verify_services","services":[]}`, "at least one"},
		{`{"type":"verify_services","services":["bad name;rm"]}`, "invalid service"},
		{`{"type":"verify_health","policy":"no_warning"}`, ""},
		{`{"type":"verify_health","policy":"cpu < 50"}`, "policy"},
		{`{"type":"verify_health","expression":"x"}`, "unknown field"},
	} {
		var op Operation
		err := json.Unmarshal([]byte(c.in), &op)
		if err == nil {
			err = op.Normalize()
		}
		if (c.err == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: %v", c.in, err)
		}
	}
	var op Operation
	json.Unmarshal([]byte(`{"type":"verify_services","services":["jellyfin","ssh.service","jellyfin.service"]}`), &op)
	op.Normalize()
	if !reflect.DeepEqual(op.VerifyServices.Services, []string{"jellyfin.service", "ssh.service"}) || op.VerifyServices.TimeoutS != 120 ||
		!IsLocal(op.Type) || op.Meta().Lock != LockShared || op.Summary() != "Verify services: jellyfin.service, ssh.service" {
		t.Fatalf("%+v %s", op.VerifyServices, op.Summary())
	}
	b, _ := json.Marshal(op)
	var back Operation
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back.VerifyServices, op.VerifyServices) {
		t.Fatalf("round trip: %s %v", b, err)
	}
	spec := JobSpec{Steps: []Operation{{Type: OpSystemUpdate, SystemUpdate: &SystemUpdateOp{Reboot: "if_required"}},
		{Type: OpVerifyHost, VerifyHost: &VerifyHostOp{}}}, Targets: TargetSpec{Hosts: []string{"a"}}}
	if err := spec.Normalize(); err != nil || spec.LockMode() != LockExclusive {
		t.Fatalf("a mutating step keeps the job exclusive: %v", err)
	}
	for _, bad := range []Strategy{{CanaryCount: 11}, {CanaryCount: 1, CanaryHosts: []string{"a"}}, {BatchSize: -1}} {
		s := bad
		if NormalizeStrategy(&s) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
